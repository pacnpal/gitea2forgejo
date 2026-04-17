package preflight

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// OfferRemediationsFromResult is the entrypoint callers should use from
// a cobra command — it re-opens the source SSH connection if needed
// (the one used during checks has been closed by the time we get here).
func OfferRemediationsFromResult(cfg *config.Config, r *Result, configPath string, log interface{ Warn(string, ...any) }) bool {
	if cfg.Source.SSH == nil {
		return false
	}
	srcSSH, err := remote.Dial(cfg.Source.SSH)
	if err != nil {
		log.Warn("remediation: reopen ssh failed", "err", err)
		return false
	}
	defer srcSSH.Close()
	return OfferRemediations(cfg, r, configPath, srcSSH)
}

// OfferRemediations walks the preflight Result looking for WARN/FAIL
// findings that have a well-defined auto-fix. If stdin is a TTY, it
// prompts the operator with the real impact data and applies the
// chosen fix to configPath, returning true if any fix was applied.
//
// No-op on non-TTY stdin so CI / scripted runs aren't blocked.
func OfferRemediations(cfg *config.Config, r *Result, configPath string, srcSSH *remote.Client) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	for _, c := range r.Checks {
		if c.Name == "source: secret keys" && (c.Status == "WARN" || c.Status == "FAIL") {
			if offerSecretKeyFix(cfg, c, configPath, srcSSH) {
				return true
			}
		}
	}
	return false
}

// offerSecretKeyFix handles the missing-SECRET_KEY case. Three branches:
//
//   - Lossless (no user data depends on SECRET_KEY): one-tap accept.
//   - Real loss (counts > 0 for at-risk categories): show the breakdown
//     and offer two paths (accept loss, or abort and fix source).
//   - Can't get counts: fall back to a single y/N accept prompt.
func offerSecretKeyFix(cfg *config.Config, c Check, configPath string, srcSSH *remote.Client) bool {
	w := os.Stderr
	r := bufio.NewReader(os.Stdin)

	impact, _ := countSecretKeyImpact(srcSSH, cfg)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "──── REMEDIATION: missing SECRET_KEY ────────────────────────────────")
	fmt.Fprintln(w, "SECRET_KEY is empty in the source app.ini. Gitea encrypts certain")
	fmt.Fprintln(w, "columns with it (TOTP, OAuth client_secret, Actions secrets, mirror")
	fmt.Fprintln(w, "credentials, LDAP bind password). A missing key means those values")
	fmt.Fprintln(w, "are already being regenerated at every restart and are effectively")
	fmt.Fprintln(w, "ephemeral.")
	fmt.Fprintln(w)

	if impact != nil {
		fmt.Fprintln(w, "Impact analysis on your DB:")
		fmt.Fprintf(w, "  TOTP 2FA users                     %d\n", impact.TOTP)
		fmt.Fprintf(w, "  User-owned OAuth2 apps (active)    %d   ← would be lost\n", impact.OAuth2Active)
		fmt.Fprintf(w, "  User-owned OAuth2 apps (dead)      %d   (already broken)\n", impact.OAuth2DeadUser)
		fmt.Fprintf(w, "  Built-in OAuth2 apps (PKCE/safe)   %d   (migrate as-is)\n", impact.OAuth2BuiltIn)
		fmt.Fprintf(w, "  Push mirrors with creds            %d   ← would be lost\n", impact.PushMirrors)
		fmt.Fprintf(w, "  Actions secrets                    %d   ← would be lost\n", impact.ActionsSecrets)
		fmt.Fprintf(w, "  LDAP login sources                 %d   ← bind password lost\n", impact.LDAPSources)
		fmt.Fprintf(w, "  WebAuthn passkeys                  %d   (public key, always safe)\n", impact.Webauthn)
		fmt.Fprintln(w)
	}

	// Lossless path: single-tap accept.
	if impact != nil && impact.Lossless() {
		fmt.Fprintln(w, "RECOMMENDATION — safe to accept:")
		fmt.Fprintln(w, "   No user-owned data depends on SECRET_KEY on this instance. Accepting")
		fmt.Fprintln(w, "   writes `options.accept_missing_secret_key: true` to your config.yaml")
		fmt.Fprintln(w, "   and lets the migration proceed.")
		fmt.Fprintln(w)
		if !promptYN(r, w, "Update config.yaml and proceed?", true) {
			fmt.Fprintln(w, "No change made. Edit config.yaml yourself to add options.accept_missing_secret_key: true.")
			return false
		}
		if err := setAcceptMissingSecretKey(configPath); err != nil {
			fmt.Fprintf(w, "Failed to update config.yaml: %v\n", err)
			return false
		}
		fmt.Fprintln(w, "✓ config.yaml updated.")
		return true
	}

	// Real-loss path: three choices.
	fmt.Fprintln(w, "RECOMMENDATION — real data will be lost. Choose how to proceed:")
	fmt.Fprintln(w, "   1. Accept the loss — set options.accept_missing_secret_key: true and")
	fmt.Fprintln(w, "      regenerate/re-enter the affected credentials manually on the target.")
	fmt.Fprintln(w, "   2. Fix the source first — set a persistent SECRET_KEY in the source")
	fmt.Fprintln(w, "      app.ini (gitea generate secret SECRET_KEY), restart Gitea, and")
	fmt.Fprintln(w, "      re-run preflight. NOTE: existing encrypted blobs stay garbage; only")
	fmt.Fprintln(w, "      future writes become recoverable.")
	fmt.Fprintln(w, "   3. Abort and investigate.")
	fmt.Fprintln(w)
	choice := promptLine(r, w, "Choose [1/2/3]", "3")
	switch strings.TrimSpace(choice) {
	case "1":
		if err := setAcceptMissingSecretKey(configPath); err != nil {
			fmt.Fprintf(w, "Failed to update config.yaml: %v\n", err)
			return false
		}
		fmt.Fprintln(w, "✓ config.yaml updated. Re-run preflight to verify.")
		return true
	case "2":
		fmt.Fprintln(w, "Commands to run on the source host (adjust for your install):")
		fmt.Fprintln(w, "  docker exec Gitea gitea generate secret SECRET_KEY")
		fmt.Fprintln(w, "  # Paste the printed value into [security] SECRET_KEY = ... in app.ini")
		fmt.Fprintln(w, "  docker restart Gitea")
		fmt.Fprintln(w, "Then re-run preflight.")
		return false
	default:
		fmt.Fprintln(w, "Aborted.")
		return false
	}
}

// setAcceptMissingSecretKey edits the config file to set
// options.accept_missing_secret_key: true. It preserves formatting by
// doing line-based surgery rather than a full YAML round-trip:
//
//   - If the key already appears, its value is rewritten to "true".
//   - Else, if an `options:` block exists, the key is inserted directly
//     after that line.
//   - Else, a new `options:` block is appended at the end.
func setAcceptMissingSecretKey(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")

	// (1) Replace existing value if present.
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "accept_missing_secret_key:") {
			indent := line[:len(line)-len(trim)]
			lines[i] = indent + "accept_missing_secret_key: true"
			return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
		}
	}
	// (2) Insert under existing options: block.
	for i, line := range lines {
		if strings.TrimSpace(line) == "options:" {
			out := append([]string(nil), lines[:i+1]...)
			out = append(out, "  accept_missing_secret_key: true")
			out = append(out, lines[i+1:]...)
			return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600)
		}
	}
	// (3) Append a fresh options block.
	body := string(data)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "\noptions:\n  accept_missing_secret_key: true\n"
	return os.WriteFile(path, []byte(body), 0o600)
}

func promptLine(r *bufio.Reader, w io.Writer, label, def string) string {
	if def != "" {
		fmt.Fprintf(w, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(w, "%s: ", label)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func promptYN(r *bufio.Reader, w io.Writer, label string, def bool) bool {
	suffix := " [y/N]: "
	if def {
		suffix = " [Y/n]: "
	}
	fmt.Fprintf(w, "%s%s", label, suffix)
	line, err := r.ReadString('\n')
	if err != nil {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "":
		return def
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}
