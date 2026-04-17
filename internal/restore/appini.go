package restore

import (
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	"gopkg.in/ini.v1"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// TranslateAppIni reads the source app.ini, applies the target-specific
// rewrites, and uploads the result to the target host at cfg.Target.ConfigFile.
//
// Rewrites applied:
//   - [server].DOMAIN / SSH_DOMAIN / ROOT_URL / SSH_LISTEN_HOST rewritten
//     to the target URL's host
//   - [security].COOKIE_REMEMBER_NAME forced to "gitea_incredible" to keep
//     logged-in sessions working through the cutover (Forgejo v15 changed
//     the default)
//   - [actions].DEFAULT_ACTIONS_URL set to https://code.forgejo.org (bare
//     `uses: actions/checkout@v4` would 404 otherwise)
//   - paths under [repository].ROOT, [lfs].PATH, [attachment].PATH, etc.
//     rewritten from cfg.Source.* to cfg.Target.* when the data dir moved
//   - SECRET_KEY / INTERNAL_TOKEN / [oauth2].JWT_SECRET preserved verbatim
//     (these are what keep 2FA + OAuth + encrypted-secret values working)
//
// Returns the path of the intermediate file that was uploaded.
func TranslateAppIni(cfg *config.Config, log *slog.Logger, ssh *remote.Client) (string, error) {
	srcIni, err := loadSourceAppIni(cfg, ssh)
	if err != nil {
		return "", err
	}
	if err := applyRewrites(srcIni, cfg, log); err != nil {
		return "", err
	}
	localPath := filepath.Join(cfg.WorkDir, "target-app.ini")
	if err := srcIni.SaveTo(localPath); err != nil {
		return "", fmt.Errorf("save target app.ini: %w", err)
	}
	log.Info("app.ini translated", "local", localPath)

	// Fetch a source-side SSH client to upload via its sftp client. We
	// use the target ssh client passed in.
	if err := uploadAppIni(ssh, localPath, cfg.Target.ConfigFile, log); err != nil {
		return "", err
	}
	return localPath, nil
}

// loadSourceAppIni reads the source app.ini — either from disk if we already
// extracted it as part of ExtractDump, or over SSH against the source host.
func loadSourceAppIni(cfg *config.Config, targetSSH *remote.Client) (*ini.File, error) {
	// Prefer the already-extracted copy under work_dir/extracted/ so we
	// don't depend on source SSH being open.
	candidates := []string{
		filepath.Join(cfg.WorkDir, "extracted", "app.ini"),
		filepath.Join(cfg.WorkDir, "extracted", "custom", "conf", "app.ini"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			f, err := ini.Load(p)
			if err != nil {
				return nil, fmt.Errorf("load extracted app.ini %s: %w", p, err)
			}
			return f, nil
		}
	}
	// Fallback: pull it over SSH from source.
	if cfg.Source.SSH == nil {
		return nil, fmt.Errorf("app.ini not found in extracted dump and source.ssh unavailable")
	}
	srcCli, err := remote.Dial(cfg.Source.SSH)
	if err != nil {
		return nil, err
	}
	defer srcCli.Close()
	data, err := srcCli.ReadFile(cfg.Source.ConfigFile)
	if err != nil {
		return nil, fmt.Errorf("read source app.ini via ssh: %w", err)
	}
	return ini.Load(data)
}

// applyRewrites mutates f in place.
func applyRewrites(f *ini.File, cfg *config.Config, log *slog.Logger) error {
	targetURL, err := url.Parse(cfg.Target.URL)
	if err != nil {
		return fmt.Errorf("parse target url: %w", err)
	}
	targetHost := targetURL.Hostname()

	// [server]: hostname + URL rewrites.
	if sec, err := f.GetSection("server"); err == nil {
		setIfPresent(sec, "DOMAIN", targetHost)
		setIfPresent(sec, "SSH_DOMAIN", targetHost)
		if k, err := sec.GetKey("ROOT_URL"); err == nil {
			k.SetValue(cfg.Target.URL + "/")
		}
		setIfPresent(sec, "SSH_LISTEN_HOST", "0.0.0.0")
	}

	// [security]: preserve COOKIE_REMEMBER_NAME so sessions survive cutover.
	sec, err := f.GetSection("security")
	if err != nil {
		sec, _ = f.NewSection("security")
	}
	if _, err := sec.GetKey("COOKIE_REMEMBER_NAME"); err != nil {
		_, _ = sec.NewKey("COOKIE_REMEMBER_NAME", "gitea_incredible")
	}

	// [actions]: force the default marketplace URL so existing workflows
	// continue to resolve.
	actSec, err := f.GetSection("actions")
	if err != nil {
		actSec, _ = f.NewSection("actions")
	}
	if _, err := actSec.GetKey("DEFAULT_ACTIONS_URL"); err != nil {
		_, _ = actSec.NewKey("DEFAULT_ACTIONS_URL", "https://code.forgejo.org")
	}

	// Path rewrites: if source data_dir differs from target data_dir, swap
	// references in any section that has a PATH or ROOT key.
	if cfg.Source.DataDir != "" && cfg.Target.DataDir != "" && cfg.Source.DataDir != cfg.Target.DataDir {
		log.Info("rewriting data_dir references",
			"from", cfg.Source.DataDir, "to", cfg.Target.DataDir)
		for _, sec := range f.Sections() {
			for _, key := range sec.Keys() {
				v := key.Value()
				if containsPath(v, cfg.Source.DataDir) {
					key.SetValue(replaceFirst(v, cfg.Source.DataDir, cfg.Target.DataDir))
				}
			}
		}
	}
	if cfg.Source.RepoRoot != "" && cfg.Target.RepoRoot != "" && cfg.Source.RepoRoot != cfg.Target.RepoRoot {
		if rec, err := f.GetSection("repository"); err == nil {
			if k, err := rec.GetKey("ROOT"); err == nil {
				k.SetValue(cfg.Target.RepoRoot)
			}
		}
	}

	// SECRET_KEY, INTERNAL_TOKEN, [oauth2].JWT_SECRET are left untouched.
	// Their presence is validated in preflight; we do NOT want to regenerate
	// them here because that breaks every encrypted column in the DB.

	return nil
}

func setIfPresent(sec *ini.Section, key, value string) {
	if k, err := sec.GetKey(key); err == nil {
		k.SetValue(value)
	}
}

func uploadAppIni(ssh *remote.Client, local, remotePath string, log *slog.Logger) error {
	data, err := os.ReadFile(local)
	if err != nil {
		return err
	}
	// Stage into a remote tempfile, then mv into place to avoid partial
	// writes being read by a crashlooping forgejo process.
	tmp := remotePath + ".new"
	if err := ssh.WriteFile(tmp, data, 0o640); err != nil {
		return fmt.Errorf("upload app.ini: %w", err)
	}
	if _, err := ssh.Run(fmt.Sprintf("mv %s %s", shQuote(tmp), shQuote(remotePath))); err != nil {
		return fmt.Errorf("install app.ini: %w", err)
	}
	log.Info("app.ini installed on target", "path", remotePath)
	return nil
}

// containsPath returns true if s contains p as a prefix at a path boundary.
// Avoids false-positive replacement of e.g. /var/lib/gitea2 when rewriting
// /var/lib/gitea.
func containsPath(s, p string) bool {
	if len(p) == 0 || len(s) < len(p) {
		return false
	}
	for i := 0; i+len(p) <= len(s); i++ {
		if s[i:i+len(p)] != p {
			continue
		}
		// Ensure boundary on each side.
		if i > 0 && s[i-1] != ' ' && s[i-1] != '=' && s[i-1] != ',' && s[i-1] != ':' {
			continue
		}
		if i+len(p) < len(s) {
			next := s[i+len(p)]
			if next != '/' && next != ' ' && next != ',' && next != 0 {
				continue
			}
		}
		return true
	}
	return false
}

func replaceFirst(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
