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
	src, err := probe(
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
	tgt, _ := probe(
		opt.TargetURL, opt.InsecureTLS,
		opt.TargetSSHHost, opt.TargetSSHPort, opt.TargetSSHUser, opt.TargetSSHKey,
		opt.TargetAppIni, opt.TargetContainer, log,
	)
	if tgt == nil {
		tgt = &ProbeResult{}
	}

	cfg := buildConfig(opt, src, tgt)
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
		opt.WorkDir = "./gitea2forgejo-work"
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

// ProbeResult is everything we learned about one side.
type ProbeResult struct {
	Summary        *appini.Summary
	Container      string
	HostAppIniPath string   // HOST path to app.ini (for SSH/SFTP reads)
	Mounts         []Mount  // container<->host bind mappings (Docker only)
}

// Mount is one bind-mount record from `docker inspect`.
type Mount struct {
	ContainerPath string // e.g. /data
	HostPath      string // e.g. /mnt/user/appdata/forgejo
}

// TranslateToHost converts a container-internal path into its host-side
// equivalent by finding the longest-prefix mount that matches. If no mount
// matches, returns p unchanged — caller should treat that as "couldn't
// translate" rather than "it's already a host path."
func TranslateToHost(p string, mounts []Mount) string {
	if p == "" {
		return p
	}
	p = strings.TrimRight(p, "/")
	best := -1
	for i, m := range mounts {
		cp := strings.TrimRight(m.ContainerPath, "/")
		if p == cp || strings.HasPrefix(p, cp+"/") {
			if best < 0 || len(cp) > len(strings.TrimRight(mounts[best].ContainerPath, "/")) {
				best = i
			}
		}
	}
	if best < 0 {
		return p
	}
	m := mounts[best]
	rel := strings.TrimPrefix(p, strings.TrimRight(m.ContainerPath, "/"))
	return path.Clean(strings.TrimRight(m.HostPath, "/") + rel)
}

// probe connects to the API (returns early if token was supplied to verify
// access), then SSHes to the host, auto-discovers the app.ini, parses it,
// and optionally detects a Docker container.
func probe(
	apiURL string, insecureTLS bool,
	sshHost string, sshPort int, sshUser, sshKey string,
	appIniPath, containerName string,
	log *slog.Logger,
) (*ProbeResult, error) {
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
		return nil, fmt.Errorf("ssh dial: %w", err)
	}
	defer cli.Close()

	res := &ProbeResult{}

	// 1. Detect Docker container if not explicitly provided.
	if containerName == "" {
		containerName = autodetectContainer(cli, log)
	}
	res.Container = containerName

	// 2. If Docker, pull bind-mount metadata up front so later logic can
	//    translate container paths to host paths.
	if containerName != "" {
		mounts, err := dockerMounts(cli, containerName)
		if err != nil {
			log.Warn("docker inspect failed (continuing without mounts)", "err", err)
		} else {
			res.Mounts = mounts
		}
	}

	// 3. Locate app.ini.
	iniPath, iniBytes, err := locateAppIni(cli, appIniPath, containerName, res.Mounts, log)
	if err != nil {
		return res, fmt.Errorf("locate app.ini: %w", err)
	}
	log.Info("init: found app.ini", "path", iniPath)
	res.HostAppIniPath = iniPath

	// 4. Parse.
	res.Summary = appini.Summarize(appini.Flat(iniBytes))
	return res, nil
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

// locateAppIni resolves the HOST path to app.ini. For Docker containers,
// it tries a few well-known container-internal paths and uses the mounts
// list to translate them to host paths. Without Docker, it tries common
// host paths directly.
func locateAppIni(cli *remote.Client, explicit, container string, mounts []Mount, log *slog.Logger) (string, []byte, error) {
	if explicit != "" {
		data, err := cli.ReadFile(explicit)
		return explicit, data, err
	}
	if container != "" && len(mounts) > 0 {
		for _, inside := range []string{
			"/data/gitea/conf/app.ini",
			"/etc/gitea/app.ini",
			"/var/lib/forgejo/custom/conf/app.ini",
		} {
			hostPath := TranslateToHost(inside, mounts)
			if hostPath == inside { // no mount matched
				continue
			}
			if data, err := cli.ReadFile(hostPath); err == nil {
				return hostPath, data, nil
			}
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

// dockerMounts returns all bind-mounts from `docker inspect` for the named
// container, suitable for TranslateToHost.
func dockerMounts(cli *remote.Client, container string) ([]Mount, error) {
	out, err := cli.Run(fmt.Sprintf(
		"docker inspect --format '{{range .Mounts}}{{.Source}}\t{{.Destination}}\n{{end}}' %s",
		shQuote(container),
	))
	if err != nil {
		return nil, err
	}
	var ms []Mount
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		ms = append(ms, Mount{HostPath: fields[0], ContainerPath: fields[1]})
	}
	return ms, nil
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
