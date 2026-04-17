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
	RunAs string `yaml:"run_as"`
	// RemoteWorkDir is a scratch dir on the remote host for dump output
	// before download. Must be writable by RunAs.
	RemoteWorkDir string `yaml:"remote_work_dir"`
}

type SSH struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	Key  string `yaml:"key"`
}

type DB struct {
	Dialect string `yaml:"dialect"` // postgres | mysql | sqlite3
	DSN     string `yaml:"dsn"`
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
	if c.WorkDir == "" {
		errs = append(errs, "work_dir is required")
	}
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
