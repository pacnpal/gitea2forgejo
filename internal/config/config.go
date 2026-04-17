// Package config loads the gitea2forgejo YAML configuration and resolves
// "env:VAR" references.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Source           Instance       `yaml:"source"`
	Target           Instance       `yaml:"target"`
	WorkDir          string         `yaml:"work_dir"`
	HostnameRewrites []Rewrite      `yaml:"hostname_rewrites"`
	Options          Options        `yaml:"options"`
}

type Instance struct {
	URL         string   `yaml:"url"`
	AdminToken  string   `yaml:"admin_token"`
	InsecureTLS bool     `yaml:"insecure_tls"`
	SSH         *SSH     `yaml:"ssh"`
	ConfigFile  string   `yaml:"config_file"`
	DataDir     string   `yaml:"data_dir"`
	RepoRoot    string   `yaml:"repo_root"`
	CustomDir   string   `yaml:"custom_dir"`
	DB          DB       `yaml:"db"`
	Storage     *Storage `yaml:"storage"`

	// Binary is the gitea/forgejo CLI path on the remote host; defaults to
	// "gitea" (source) or "forgejo" (target) in PATH.
	Binary string `yaml:"binary"`
	// RunAs is a user to sudo to when invoking the binary; empty = no sudo.
	// Ignored when Docker.Container is set (use Docker.User instead).
	RunAs string `yaml:"run_as"`
	// RemoteWorkDir is a scratch dir on the remote host for dump output
	// before download. Must be writable by RunAs.
	// When Docker is used: this is a HOST path that's bind-mounted into
	// the container; `gitea dump` writes inside the container, the file
	// appears on the host for SFTP to retrieve.
	RemoteWorkDir string `yaml:"remote_work_dir"`

	// Docker wraps binary invocations in `docker exec`. When set, SSH
	// targets the Docker host (not the container), and Binary/RunAs are
	// interpreted inside the container.
	Docker *Docker `yaml:"docker"`
}

type SSH struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	Key  string `yaml:"key"`

	// KnownHosts is the path to an OpenSSH-format known_hosts file used to
	// verify the remote host key. Defaults to ~/.ssh/known_hosts.
	KnownHosts string `yaml:"known_hosts"`

	// HostKeyFingerprint is an optional SHA256 fingerprint (as printed by
	// `ssh-keygen -lf key.pub`, e.g. "SHA256:AbCdEf..."). When set, it is
	// checked in addition to KnownHosts; useful in CI where no known_hosts
	// file exists.
	HostKeyFingerprint string `yaml:"host_key_fingerprint"`
}

type DB struct {
	Dialect string `yaml:"dialect"` // postgres | mysql | sqlite3
	DSN     string `yaml:"dsn"`
}

// Docker wraps remote binary invocations in `docker exec`.
//
// When set, the SSH connection still targets the Docker host (so rsync,
// systemctl, and filesystem operations work unchanged), but `gitea dump`,
// `forgejo doctor`, `forgejo admin regenerate hooks` etc. run inside the
// named container.
//
// The Mounts list records the bind mounts reported by `docker inspect` at
// init time. Path translation (host ↔ container) uses this list so the
// tool doesn't have to re-query Docker on every run, and so the operator
// can see and edit the mapping.
type Docker struct {
	// Container is the container name or ID (as shown by `docker ps`).
	Container string `yaml:"container"`
	// User is the user inside the container to run commands as. Typically
	// "git" for Gitea (uid 1000) and "git" for Forgejo. Empty = container
	// default user.
	User string `yaml:"user"`
	// Binary is the docker CLI to invoke. Default "docker"; set to
	// "podman" for Podman or a custom path.
	Binary string `yaml:"binary"`
	// Mounts lists the bind mounts Docker reports for this container.
	// Populated by `gitea2forgejo init` via `docker inspect`; the operator
	// may edit entries here to override the discovered layout.
	Mounts []Mount `yaml:"mounts,omitempty"`
}

// Mount is one bind-mount entry linking a host path to its container-side
// destination. Matches the `Source`/`Destination` pair from `docker inspect`.
type Mount struct {
	Host      string `yaml:"host"`
	Container string `yaml:"container"`
}

// HostToContainer performs longest-prefix lookup over d.Mounts and
// returns the container-side equivalent of hostPath. Returns "" if no
// mount matches (the caller should then treat the path as not reachable
// from inside the container).
func (d *Docker) HostToContainer(hostPath string) string {
	if d == nil || hostPath == "" {
		return ""
	}
	hp := trimRight(hostPath, "/")
	best := -1
	for i, m := range d.Mounts {
		host := trimRight(m.Host, "/")
		if hp == host || hasPathPrefix(hp, host+"/") {
			if best < 0 || len(trimRight(d.Mounts[best].Host, "/")) < len(host) {
				best = i
			}
		}
	}
	if best < 0 {
		return ""
	}
	host := trimRight(d.Mounts[best].Host, "/")
	cont := trimRight(d.Mounts[best].Container, "/")
	rel := hp[len(host):]
	return cleanPath(cont + rel)
}

// Local string utilities so config doesn't grow stdlib imports.
func trimRight(s, cutset string) string {
	for len(s) > 0 {
		if !containsByte(cutset, s[len(s)-1]) {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}
func containsByte(s string, b byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return true
		}
	}
	return false
}
func hasPathPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
func cleanPath(p string) string {
	// Collapse any accidental double slashes. Good enough for the formats
	// docker inspect emits on Linux.
	out := make([]byte, 0, len(p))
	prevSlash := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c == '/' {
			if prevSlash {
				continue
			}
			prevSlash = true
		} else {
			prevSlash = false
		}
		out = append(out, c)
	}
	return string(out)
}

type Storage struct {
	Type      string `yaml:"type"` // s3 | local
	Endpoint  string `yaml:"endpoint"`
	Bucket    string `yaml:"bucket"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	Region    string `yaml:"region"`
}

type Rewrite struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

type Options struct {
	StopSource         bool   `yaml:"stop_source"`
	RewriteIssueBodies bool   `yaml:"rewrite_issue_bodies"`
	DumpFormat         string `yaml:"dump_format"` // tar.zst | tar.gz | tar | zip

	// Dump stage skips. Harvest always runs; these let the operator opt out
	// of the heavy stages (useful for staging rehearsals).
	SkipGiteaDump bool `yaml:"skip_gitea_dump"`
	SkipNativeDB  bool `yaml:"skip_native_db"`
	SkipS3Mirror  bool `yaml:"skip_s3_mirror"`

	// ResetTargetDB wipes the target database before restore. Required when
	// the operator has already run Forgejo's initial setup wizard (creating
	// the first admin user + Forgejo-native tables that conflict with the
	// Gitea dump). DESTRUCTIVE — only set this after confirming the target
	// holds nothing you want to keep.
	ResetTargetDB bool `yaml:"reset_target_db"`

	// AcceptMissingSecretKey downgrades the "missing SECRET_KEY" preflight
	// check from FAIL to WARN. Only set this if you don't use 2FA, OAuth2
	// apps, Actions secrets, or push-mirror credentials — those values are
	// encrypted with SECRET_KEY and become unrecoverable without it.
	//
	// Gitea auto-generates a SECRET_KEY on first boot. Its absence almost
	// always means the Gitea data volume isn't persisted (Docker without
	// a mount), in which case the encrypted data is already being lost at
	// every container restart.
	AcceptMissingSecretKey bool `yaml:"accept_missing_secret_key"`
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.resolve(); err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	if c.Options.DumpFormat == "" {
		c.Options.DumpFormat = "tar.zst"
	}
	if c.WorkDir == "" {
		c.WorkDir = "./gitea2forgejo-work"
	}
	if c.Source.Binary == "" {
		c.Source.Binary = "gitea"
	}
	if c.Target.Binary == "" {
		c.Target.Binary = "forgejo"
	}
	if c.Source.RemoteWorkDir == "" {
		c.Source.RemoteWorkDir = "/tmp/gitea2forgejo"
	}
	if c.Target.RemoteWorkDir == "" {
		c.Target.RemoteWorkDir = "/tmp/gitea2forgejo"
	}
	for _, inst := range []*Instance{&c.Source, &c.Target} {
		if inst.Docker != nil && inst.Docker.Binary == "" {
			inst.Docker.Binary = "docker"
		}
	}
	return &c, nil
}

// resolve expands env:VAR references and ~/ paths in-place.
func (c *Config) resolve() error {
	for _, inst := range []*Instance{&c.Source, &c.Target} {
		inst.AdminToken = expandEnv(inst.AdminToken)
		inst.DB.DSN = expandEnv(inst.DB.DSN)
		if inst.SSH != nil {
			inst.SSH.Key = expandHome(inst.SSH.Key)
			if inst.SSH.Port == 0 {
				inst.SSH.Port = 22
			}
			if inst.SSH.KnownHosts == "" {
				home, err := os.UserHomeDir()
				if err == nil {
					inst.SSH.KnownHosts = filepath.Join(home, ".ssh", "known_hosts")
				}
			} else {
				inst.SSH.KnownHosts = expandHome(inst.SSH.KnownHosts)
			}
		}
		if inst.Storage != nil {
			inst.Storage.AccessKey = expandEnv(inst.Storage.AccessKey)
			inst.Storage.SecretKey = expandEnv(inst.Storage.SecretKey)
		}
	}
	c.WorkDir = expandHome(c.WorkDir)
	return nil
}

func (c *Config) validate() error {
	var errs []string
	for label, inst := range map[string]*Instance{"source": &c.Source, "target": &c.Target} {
		if inst.URL == "" {
			errs = append(errs, label+": url is required")
		} else if u, err := url.Parse(inst.URL); err != nil || u.Scheme == "" || u.Host == "" {
			errs = append(errs, label+": url must be absolute (got "+inst.URL+")")
		}
		if inst.AdminToken == "" {
			errs = append(errs, label+": admin_token is empty (set env var or literal)")
		}
		if inst.DataDir == "" {
			errs = append(errs, label+": data_dir is required")
		}
		if inst.ConfigFile == "" {
			errs = append(errs, label+": config_file is required")
		}
		if inst.DB.Dialect == "" {
			errs = append(errs, label+": db.dialect is required")
		} else if !validDialect(inst.DB.Dialect) {
			errs = append(errs, label+": db.dialect must be postgres|mysql|sqlite3 (got "+inst.DB.Dialect+")")
		}
		if inst.DB.DSN == "" {
			errs = append(errs, label+": db.dsn is empty")
		}
		if inst.SSH != nil && inst.SSH.Host == "" {
			errs = append(errs, label+": ssh.host is required when ssh block is present")
		}
	}
	// work_dir can be empty — we fill in a CWD-local default during
	// resolve(). Keep the validator quiet about it.
	switch c.Options.DumpFormat {
	case "", "tar.zst", "tar.gz", "tar", "zip":
	default:
		errs = append(errs, "options.dump_format must be tar.zst|tar.gz|tar|zip (got "+c.Options.DumpFormat+")")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func validDialect(d string) bool {
	switch d {
	case "postgres", "mysql", "sqlite3":
		return true
	}
	return false
}

// expandEnv returns os.Getenv(v) for "env:VAR", otherwise v verbatim.
func expandEnv(v string) string {
	const prefix = "env:"
	if strings.HasPrefix(v, prefix) {
		return os.Getenv(strings.TrimPrefix(v, prefix))
	}
	return v
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
