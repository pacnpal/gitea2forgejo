// Package initcmd implements `gitea2forgejo init` — it SSHes to the source
// host, reads the Gitea app.ini, extracts as many config.yaml fields as
// possible, and writes a ready-to-run config file. Cuts typical setup from
// "fill in 25 fields" to "answer 3 prompts."
package initcmd

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/pacnpal/gitea2forgejo/internal/appini"
	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// Options is what the CLI layer passes in. All string fields are resolved
// (not "env:FOO" indirections) by the time we receive them.
type Options struct {
	SourceURL       string
	SourceToken     string
	SourceSSHUser   string
	SourceSSHHost   string
	SourceSSHPort   int
	SourceSSHKey    string
	SourceAppIni    string // optional override; else auto-discovered
	SourceContainer string // optional; else auto-detected

	TargetURL       string
	TargetToken     string
	TargetSSHUser   string
	TargetSSHHost   string
	TargetSSHPort   int
	TargetSSHKey    string
	TargetAppIni    string
	TargetContainer string

	WorkDir string
	Output  string // path to write config.yaml

	InsecureTLS bool
}

// Run performs discovery and writes the config file.
func Run(opt *Options, log *slog.Logger) error {
	if err := opt.validate(); err != nil {
		return err
	}

	// Ensure SSH is usable before attempting any probes. On failure in an
	// interactive session, this offers to generate a key and ssh-copy-id
	// it to the remote. On non-TTY, it returns the original error.
	if err := ensureSSHOrBootstrap("source", opt, log); err != nil {
		return fmt.Errorf("source ssh: %w", err)
	}
	if err := ensureSSHOrBootstrap("target", opt, log); err != nil {
		return fmt.Errorf("target ssh: %w", err)
	}

	log.Info("init: probing source", "url", opt.SourceURL, "ssh", opt.SourceSSHHost)
	srcSummary, srcContainer, err := probe(
		opt.SourceURL, opt.InsecureTLS,
		opt.SourceSSHHost, opt.SourceSSHPort, opt.SourceSSHUser, opt.SourceSSHKey,
		opt.SourceAppIni, opt.SourceContainer, log,
	)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	log.Info("init: probing target", "url", opt.TargetURL, "ssh", opt.TargetSSHHost)
	// Target probe is best-effort — target Forgejo may not have an app.ini
	// yet if it's fresh, or it may refuse API calls until the setup wizard
	// is run. We fall back to sensible defaults.
	tgtSummary, tgtContainer, _ := probe(
		opt.TargetURL, opt.InsecureTLS,
		opt.TargetSSHHost, opt.TargetSSHPort, opt.TargetSSHUser, opt.TargetSSHKey,
		opt.TargetAppIni, opt.TargetContainer, log,
	)

	cfg := buildConfig(opt, srcSummary, srcContainer, tgtSummary, tgtContainer)
	return writeYAML(cfg, opt.Output, log)
}

// ensureSSHOrBootstrap ensures SSH works for either the source or target. On
// failure in a TTY it drives the ssh-keygen / ssh-keyscan / ssh-copy-id
// flow in EnsureAuth. Side-effect: updates opt.<Label>SSHKey if a new key
// was generated.
//
// When called for the target and target's SSHKey is empty, it inherits the
// source's (already-bootstrapped) key as a starting point. That means when
// source and target are the same host (or the source's key was already
// copy-id'd to the target), the target's first Dial succeeds and we skip
// a redundant keygen/keyscan/copy-id round-trip.
func ensureSSHOrBootstrap(label string, opt *Options, log *slog.Logger) error {
	var sshCfg *config.SSH
	switch label {
	case "source":
		sshCfg = ProposeSSHConfig(opt.SourceSSHHost, opt.SourceSSHPort, opt.SourceSSHUser, opt.SourceSSHKey)
	case "target":
		key := opt.TargetSSHKey
		if key == "" {
			key = opt.SourceSSHKey // piggy-back on whatever source ended up using
		}
		sshCfg = ProposeSSHConfig(opt.TargetSSHHost, opt.TargetSSHPort, opt.TargetSSHUser, key)
	}
	if sshCfg == nil {
		return fmt.Errorf("no SSH host configured")
	}
	if err := EnsureAuth(label, sshCfg, log); err != nil {
		return err
	}
	// Persist any key change back into opt.
	if label == "source" {
		opt.SourceSSHKey = sshCfg.Key
	} else {
		opt.TargetSSHKey = sshCfg.Key
	}
	return nil
}

func (opt *Options) validate() error {
	var missing []string
	if opt.SourceURL == "" {
		missing = append(missing, "source-url")
	}
	if opt.SourceSSHHost == "" {
		missing = append(missing, "source-ssh")
	}
	if opt.TargetURL == "" {
		missing = append(missing, "target-url")
	}
	if opt.TargetSSHHost == "" {
		missing = append(missing, "target-ssh")
	}
	if opt.Output == "" {
		opt.Output = "config.yaml"
	}
	if opt.WorkDir == "" {
		opt.WorkDir = "/var/cache/gitea2forgejo"
	}
	if opt.SourceSSHPort == 0 {
		opt.SourceSSHPort = 22
	}
	if opt.TargetSSHPort == 0 {
		opt.TargetSSHPort = 22
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required: %s", strings.Join(missing, ", "))
	}
	return nil
}

// probe connects to the API (returns early if token was supplied to verify
// access), then SSHes to the host, auto-discovers the app.ini, parses it,
// and optionally detects a Docker container.
func probe(
	apiURL string, insecureTLS bool,
	sshHost string, sshPort int, sshUser, sshKey string,
	appIniPath, containerName string,
	log *slog.Logger,
) (*appini.Summary, string, error) {
	if apiURL != "" {
		if err := pingAPI(apiURL, insecureTLS); err != nil {
			log.Warn("API not reachable (non-fatal for init)", "url", apiURL, "err", err)
		}
	}
	cli, err := remote.Dial(&config.SSH{
		Host: sshHost, Port: sshPort, User: sshUser, Key: sshKey,
		KnownHosts: defaultKnownHosts(),
	})
	if err != nil {
		return nil, "", fmt.Errorf("ssh dial: %w", err)
	}
	defer cli.Close()

	// 1. Detect Docker container if not explicitly provided.
	if containerName == "" {
		containerName = autodetectContainer(cli, log)
	}

	// 2. Locate app.ini. Inside Docker, use `docker inspect` to find the
	//    bind-mounted path on the host; outside, try common paths.
	iniPath, iniBytes, err := locateAppIni(cli, appIniPath, containerName, log)
	if err != nil {
		return nil, containerName, fmt.Errorf("locate app.ini: %w", err)
	}
	log.Info("init: found app.ini", "path", iniPath)

	// 3. Parse.
	s := appini.Summarize(appini.Flat(iniBytes))
	return s, containerName, nil
}

func pingAPI(base string, insecureTLS bool) error {
	u, err := url.Parse(strings.TrimRight(base, "/") + "/api/v1/version")
	if err != nil {
		return err
	}
	cli := &http.Client{}
	if insecureTLS {
		cli.Transport = insecureTransport()
	}
	resp, err := cli.Get(u.String())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

// autodetectContainer tries `docker ps` on the remote host looking for a
// container whose image or name contains "gitea" or "forgejo". Returns ""
// if none (or if docker isn't installed).
func autodetectContainer(cli *remote.Client, log *slog.Logger) string {
	out, err := cli.Run(`docker ps --format '{{.Names}}\t{{.Image}}' 2>/dev/null || true`)
	if err != nil || len(out) == 0 {
		return ""
	}
	var best string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		name, image := parts[0], strings.ToLower(parts[1])
		if strings.Contains(image, "gitea") || strings.Contains(image, "forgejo") {
			best = name
			break
		}
	}
	if best != "" {
		log.Info("autodetected container", "name", best)
	}
	return best
}

// locateAppIni resolves the path to app.ini on the host. For Docker
// containers, it queries `docker inspect` for the bind mount that maps
// into /data or /etc/gitea. Without Docker, it tries common paths.
func locateAppIni(cli *remote.Client, explicit, container string, log *slog.Logger) (string, []byte, error) {
	if explicit != "" {
		data, err := cli.ReadFile(explicit)
		return explicit, data, err
	}
	if container != "" {
		p, err := dockerHostAppIni(cli, container, log)
		if err == nil && p != "" {
			data, err := cli.ReadFile(p)
			if err == nil {
				return p, data, nil
			}
			log.Warn("docker-resolved app.ini not readable", "path", p, "err", err)
		}
	}
	// Fallback: common paths on a non-Docker install.
	for _, p := range []string{
		"/etc/gitea/app.ini",
		"/etc/forgejo/app.ini",
		"/var/lib/gitea/custom/conf/app.ini",
		"/var/lib/forgejo/custom/conf/app.ini",
		"/data/gitea/conf/app.ini",
	} {
		if data, err := cli.ReadFile(p); err == nil {
			return p, data, nil
		}
	}
	return "", nil, fmt.Errorf("app.ini not found at any known location; pass --source-app-ini to override")
}

// dockerHostAppIni returns the HOST path of the bind-mount that corresponds
// to the container's /data/gitea/conf/app.ini (or similar).
//
// Uses `docker inspect --format` to dump the Mounts array in a format we
// can parse.
func dockerHostAppIni(cli *remote.Client, container string, log *slog.Logger) (string, error) {
	out, err := cli.Run(fmt.Sprintf(
		"docker inspect --format '{{range .Mounts}}{{.Source}}\t{{.Destination}}\n{{end}}' %s",
		shQuote(container),
	))
	if err != nil {
		return "", err
	}
	// We want the mount whose destination tree would contain app.ini —
	// typically destination = /data (official gitea/forgejo images)
	// or /etc/gitea.
	candidates := [][2]string{
		{"/data/gitea/conf/app.ini", "/data"},
		{"/etc/gitea/app.ini", "/etc/gitea"},
		{"/var/lib/forgejo/custom/conf/app.ini", "/var/lib/forgejo"},
	}
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 2 {
			continue
		}
		src, dst := fields[0], strings.TrimRight(fields[1], "/")
		for _, c := range candidates {
			inside, mount := c[0], c[1]
			if dst == mount {
				// Translate inside-container path to host path under src.
				rel := strings.TrimPrefix(inside, mount)
				return path.Clean(src + rel), nil
			}
		}
	}
	return "", nil
}

func defaultKnownHosts() string {
	h, _ := os.UserHomeDir()
	if h == "" {
		return ""
	}
	return path.Join(h, ".ssh", "known_hosts")
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// insecureTransport returns an *http.Transport with TLS verification off.
// Separated so we don't import crypto/tls in the main path when unused.
func insecureTransport() http.RoundTripper {
	return &http.Transport{
		TLSClientConfig: tlsInsecureConfig(),
		DialContext: (&net.Dialer{}).DialContext,
	}
}
