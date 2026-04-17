package initcmd

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"golang.org/x/term"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// EnsureAuth confirms SSH to host works. If the first attempt fails with
// an auth-ish error and we're on a TTY, it walks the user through:
//
//  1. Generate a new ed25519 key at ~/.ssh/gitea2forgejo (if no key works)
//  2. `ssh-keyscan` the host to prime ~/.ssh/known_hosts
//  3. `ssh-copy-id` to install the new key on the host (asks for the
//     remote password once)
//  4. Retry the connection
//
// On success, sshCfg is mutated so subsequent Dial calls use whatever key
// worked. On non-TTY stdin, returns the original error unchanged.
func EnsureAuth(label string, sshCfg *config.SSH, log *slog.Logger) error {
	// Quick probe.
	cli, err := remote.Dial(sshCfg)
	if err == nil {
		cli.Close()
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return err
	}

	w := os.Stderr
	fmt.Fprintf(w, "\nSSH to %s %s failed:\n  %v\n\n", label, sshCfg.Host, err)

	r := bufio.NewReader(os.Stdin)
	if !promptYN(r, w, fmt.Sprintf("Generate a new key and install it on %s?", sshCfg.Host), true) {
		return err
	}

	// 1. Ensure a keypair exists. Default location — never overwrite
	//    existing key silently.
	home, herr := os.UserHomeDir()
	if herr != nil {
		return fmt.Errorf("cannot determine home dir: %w", herr)
	}
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", sshDir, err)
	}
	keyPath := filepath.Join(sshDir, "gitea2forgejo")
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		fmt.Fprintf(w, "Generating ed25519 keypair at %s ...\n", keyPath)
		if err := runVisible(w, "ssh-keygen",
			"-t", "ed25519",
			"-f", keyPath,
			"-N", "", // empty passphrase
			"-C", "gitea2forgejo",
		); err != nil {
			return fmt.Errorf("ssh-keygen: %w", err)
		}
	} else {
		fmt.Fprintf(w, "Reusing existing key at %s\n", keyPath)
	}
	sshCfg.Key = keyPath

	// 2. ssh-keyscan → known_hosts (idempotent: ssh-copy-id and later dial
	//    will fail with "host key verification" without this).
	known := filepath.Join(sshDir, "known_hosts")
	fmt.Fprintf(w, "Scanning host key of %s ...\n", sshCfg.Host)
	if err := appendKeyscan(sshCfg.Host, sshCfg.Port, known, w); err != nil {
		fmt.Fprintf(w, "  warning: ssh-keyscan failed (%v); continuing\n", err)
	}
	if sshCfg.KnownHosts == "" {
		sshCfg.KnownHosts = known
	}

	// 3. ssh-copy-id. Prompts interactively for the remote password.
	target := sshCfg.User + "@" + sshCfg.Host
	if sshCfg.User == "" {
		target = sshCfg.Host
	}
	fmt.Fprintf(w, "Installing public key on %s (you will be prompted for its password)...\n", target)
	args := []string{"-i", keyPath + ".pub"}
	if sshCfg.Port != 22 && sshCfg.Port != 0 {
		args = append(args, "-p", strconv.Itoa(sshCfg.Port))
	}
	args = append(args, target)
	if err := runVisible(w, "ssh-copy-id", args...); err != nil {
		return fmt.Errorf("ssh-copy-id: %w", err)
	}

	// 4. Retry.
	fmt.Fprintln(w, "Retrying SSH connection with the new key ...")
	cli, err = remote.Dial(sshCfg)
	if err != nil {
		return fmt.Errorf("ssh still failing after bootstrap: %w", err)
	}
	cli.Close()
	fmt.Fprintln(w, "SSH to "+target+": OK")
	return nil
}

// runVisible runs cmd inheriting stdin/stdout/stderr so interactive prompts
// (ssh-keygen's overwrite warning, ssh-copy-id's password prompt) reach the
// user's TTY and their responses reach the tool.
func runVisible(w *os.File, name string, args ...string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s not found in PATH: %w", name, err)
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

// appendKeyscan runs ssh-keyscan and appends its output to known_hosts.
// Skips re-adding if the host is already present.
func appendKeyscan(host string, port int, known string, w *os.File) error {
	if port == 0 {
		port = 22
	}
	// ssh-keyscan output is what we want in known_hosts verbatim.
	cmd := exec.Command("ssh-keyscan", "-H", "-p", strconv.Itoa(port), host)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(known, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(out); err != nil {
		return err
	}
	fmt.Fprintf(w, "  added %d bytes to %s\n", len(out), known)
	return nil
}

// ProposeSSHConfig constructs a config.SSH from the parsed init options so
// EnsureAuth can be called before probe(). Returns nil if the option block
// is incomplete (e.g., SSH host missing — meaning the user passed URLs only).
func ProposeSSHConfig(host string, port int, user, key string) *config.SSH {
	if host == "" {
		return nil
	}
	return &config.SSH{
		Host:       host,
		Port:       port,
		User:       user,
		Key:        key,
		KnownHosts: defaultKnownHosts(),
	}
}
