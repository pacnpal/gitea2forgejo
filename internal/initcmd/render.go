package initcmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

// buildConfig takes the probe results and shapes them into a config.Config
// populated with env: references (for secrets) and literal values (for
// everything else).
func buildConfig(
	opt *Options,
	src *ProbeResult,
	tgt *ProbeResult,
) *config.Config {
	c := &config.Config{
		WorkDir: opt.WorkDir,
	}
	populateInstance(&c.Source, "source", opt.SourceURL, opt.SourceToken, opt.InsecureTLS,
		opt.SourceSSHHost, opt.SourceSSHPort, opt.SourceSSHUser, opt.SourceSSHKey,
		opt.SourceAppIni, src)
	populateInstance(&c.Target, "target", opt.TargetURL, opt.TargetToken, opt.InsecureTLS,
		opt.TargetSSHHost, opt.TargetSSHPort, opt.TargetSSHUser, opt.TargetSSHKey,
		opt.TargetAppIni, tgt)
	// Default target paths when the target had no discoverable app.ini
	// (very common for a fresh Forgejo that hasn't been initialized).
	// For containerized targets we translate forgejo's container-
	// internal conventions to host paths via the probed mounts — chown,
	// rsync and every other host-path consumer needs real host paths.
	// Falls back to the container-style defaults when no mount covers
	// the path (bare-metal or a layout this tool doesn't recognize).
	setTargetDefault(&c.Target.ConfigFile, "/etc/forgejo/app.ini", c.Target.Docker)
	setTargetDefault(&c.Target.DataDir, "/var/lib/forgejo", c.Target.Docker)
	setTargetDefault(&c.Target.RepoRoot, "/var/lib/forgejo/git/repositories", c.Target.Docker)
	setTargetDefault(&c.Target.CustomDir, "/var/lib/forgejo/custom", c.Target.Docker)
	// Hostname rewrites: if URLs differ, pre-populate one rewrite rule.
	if srcHost, tgtHost := hostFromURL(opt.SourceURL), hostFromURL(opt.TargetURL); srcHost != "" && tgtHost != "" && srcHost != tgtHost {
		c.HostnameRewrites = []config.Rewrite{{From: srcHost, To: tgtHost}}
	}
	c.Options = config.Options{DumpFormat: "tar.zst", StopSource: true}
	return c
}

func populateInstance(
	inst *config.Instance, label string,
	apiURL, token string, insecureTLS bool,
	sshHost string, sshPort int, sshUser, sshKey string,
	appIniOverride string,
	probe *ProbeResult,
) {
	inst.URL = apiURL
	inst.InsecureTLS = insecureTLS
	if token == "" {
		inst.AdminToken = "env:" + strings.ToUpper(label) + "_ADMIN_TOKEN"
	} else {
		inst.AdminToken = token
	}
	inst.SSH = &config.SSH{
		Host: sshHost, Port: sshPort, User: sshUser, Key: sshKey,
		KnownHosts: "",
	}

	container := ""
	mounts := []Mount{}
	if probe != nil {
		container = probe.Container
		mounts = probe.Mounts
	}

	// ConfigFile: prefer an explicit override, then the host path the
	// probe resolved via docker inspect, then a reasonable default.
	switch {
	case appIniOverride != "":
		inst.ConfigFile = appIniOverride
	case probe != nil && probe.HostAppIniPath != "":
		inst.ConfigFile = probe.HostAppIniPath
	case container != "":
		inst.ConfigFile = "/data/gitea/conf/app.ini" // last-resort container path
	}

	if probe != nil && probe.Summary != nil {
		s := probe.Summary
		// Translate container-internal paths to HOST paths. `gitea2forgejo`
		// always operates via SSH on the host; container-internal paths
		// won't exist for filesystem operations.
		inst.DataDir = TranslateToHost(s.AppDataPath, mounts)
		inst.RepoRoot = TranslateToHost(s.RepoRoot, mounts)
		if s.CustomDir != "" {
			inst.CustomDir = TranslateToHost(s.CustomDir, mounts)
		}

		inst.DB = config.DB{Dialect: s.DBType}
		if s.DBType == "sqlite3" {
			// SQLite file is a path; translate it too so preflight can
			// `cli.FetchFile` or rsync it via a HOST path.
			inst.DB.DSN = TranslateToHost(s.DBPath, mounts)
		} else if dsn, err := s.BuildDSN(""); err == nil {
			// Postgres/MySQL DSNs contain a password; stash under env.
			if s.DBPassword != "" {
				inst.DB.DSN = "env:" + strings.ToUpper(label) + "_DB_DSN"
			} else {
				inst.DB.DSN = dsn
			}
		}

		if s.StorageType == "minio" || s.StorageType == "s3" {
			inst.Storage = &config.Storage{
				Type:      "s3",
				Endpoint:  s.S3Endpoint,
				Bucket:    s.S3Bucket,
				AccessKey: "env:" + strings.ToUpper(label) + "_S3_ACCESS_KEY",
				SecretKey: "env:" + strings.ToUpper(label) + "_S3_SECRET_KEY",
			}
		}
	}
	if container != "" {
		d := &config.Docker{
			Container: container,
			User:      "git", // gitea/forgejo default
			Binary:    "docker",
		}
		// Copy the mounts the probe captured so they're visible in
		// config.yaml and usable without re-running docker inspect.
		if probe != nil {
			for _, m := range probe.Mounts {
				d.Mounts = append(d.Mounts, config.Mount{
					Host:      m.HostPath,
					Container: m.ContainerPath,
				})
			}
		}
		inst.Docker = d

		// Set RemoteWorkDir to a host path under a bind mount so
		// `gitea dump` can write there and SFTP can read it without
		// any docker cp intermediate. Pick the longest-prefix mount
		// that covers DataDir (if possible), else the first mount.
		inst.RemoteWorkDir = chooseRemoteWorkDir(inst.DataDir, d)
	}
}

// setTargetDefault fills in a config field that populateInstance left
// blank. If the target is containerized and the probed mounts cover the
// container-side default, it writes the translated host path so chown
// and other host-side ops succeed. Otherwise the container-side default
// is written as-is (bare-metal targets, or atypical container layouts).
func setTargetDefault(field *string, containerDefault string, d *config.Docker) {
	if *field != "" {
		return
	}
	if d != nil && d.Container != "" {
		if h := d.ContainerToHost(containerDefault); h != "" {
			*field = h
			return
		}
	}
	*field = containerDefault
}

// chooseRemoteWorkDir picks a HOST path for dump scratch space. It
// prefers a subdirectory of data_dir (since that's guaranteed to be
// bind-mounted and usually has ample space). Falls back to a subdir
// of the first mount, else leaves the default /tmp path.
func chooseRemoteWorkDir(dataDir string, d *config.Docker) string {
	const subdir = "gitea2forgejo-work"
	if dataDir != "" && d.HostToContainer(dataDir) != "" {
		return dataDir + "/" + subdir
	}
	if len(d.Mounts) > 0 {
		return d.Mounts[0].Host + "/" + subdir
	}
	return "/tmp/gitea2forgejo"
}

// writeYAML emits the config with a human-friendly leading comment. We
// serialize the struct via yaml.v3 (respecting its `yaml:` tags), then
// pre-pend a banner.
func writeYAML(cfg *config.Config, path string, log *slog.Logger) error {
	buf := &strings.Builder{}
	buf.WriteString(`# Generated by `)
	buf.WriteString("`gitea2forgejo init`.\n")
	buf.WriteString(`#
# Secrets have been written as env:<NAME> references. Export them before
# running any subcommand:
#
#   export SOURCE_ADMIN_TOKEN=<gitea admin token>
#   export TARGET_ADMIN_TOKEN=<forgejo admin token>
#   export SOURCE_DB_DSN=<reconstructed DSN, copy-paste from init output>
#   export TARGET_DB_DSN=<target DSN>
#   # plus S3 keys if the storage block is present
#
# Review every field before running ` + "`gitea2forgejo preflight`.\n\n")
	enc := yaml.NewEncoder(buf)
	enc.SetIndent(2)
	if err := enc.Encode(cfg); err != nil {
		return err
	}
	enc.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
		return err
	}
	log.Info("init: wrote config", "path", path)
	return nil
}

func hostFromURL(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/:"); i >= 0 {
		u = u[:i]
	}
	return u
}

var _ = fmt.Sprintf
