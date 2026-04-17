package initcmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Interactive prompts only for fields that are missing. If every required
// field is already set (via flags or environment variables) it returns
// silently — running `gitea2forgejo init --source-url ... --target-ssh ...`
// with the relevant env vars exported should NOT drop into an interactive
// prompt.
//
// On non-TTY stdin it returns without prompting (CI / scripted users still
// get the non-interactive "missing flag" error from validate()).
func Interactive(opt *Options) error {
	in := os.Stdin
	if !term.IsTerminal(int(in.Fd())) {
		return nil
	}

	// Decide what's actually missing BEFORE printing any banner.
	needSourceURL := opt.SourceURL == ""
	needSourceSSH := opt.SourceSSHHost == ""
	needSourceTok := opt.SourceToken == "" && os.Getenv("SOURCE_ADMIN_TOKEN") == ""
	needTargetURL := opt.TargetURL == ""
	needTargetSSH := opt.TargetSSHHost == ""
	needTargetTok := opt.TargetToken == "" && os.Getenv("TARGET_ADMIN_TOKEN") == ""

	if !needSourceURL && !needSourceSSH && !needSourceTok &&
		!needTargetURL && !needTargetSSH && !needTargetTok {
		return nil
	}

	w := os.Stderr
	r := bufio.NewReader(in)
	fmt.Fprintln(w, "gitea2forgejo init — answer the prompts below to fill in missing values.")
	fmt.Fprintln(w)

	if needSourceURL {
		opt.SourceURL = promptLine(r, w, "Source Gitea URL", "")
	}
	if needSourceSSH {
		dest := promptLine(r, w, "Source SSH destination (user@host[:port])", "")
		parseIntoSource(opt, dest)
	}
	if needSourceTok {
		if t := promptSecret(in, w, "Source admin token"); t != "" {
			opt.SourceToken = t
		}
	}
	if needTargetURL {
		opt.TargetURL = promptLine(r, w, "Target Forgejo URL", "")
	}
	if needTargetSSH {
		dest := promptLine(r, w, "Target SSH destination (user@host[:port])", "")
		parseIntoTarget(opt, dest)
	}
	if needTargetTok {
		if t := promptSecret(in, w, "Target admin token"); t != "" {
			opt.TargetToken = t
		}
	}
	fmt.Fprintln(w)
	return nil
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

func promptSecret(in *os.File, w io.Writer, label string) string {
	fmt.Fprintf(w, "%s: ", label)
	b, err := term.ReadPassword(int(in.Fd()))
	fmt.Fprintln(w) // term eats the newline
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
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

// sshDestFromOpt reassembles user@host[:port] from parsed fields so the
// interactive default has the shape the user expects.
func sshDestFromOpt(user, host string, port int) string {
	if host == "" {
		return ""
	}
	out := host
	if user != "" {
		out = user + "@" + out
	}
	if port != 0 && port != 22 {
		out = fmt.Sprintf("%s:%d", out, port)
	}
	return out
}

func parseIntoSource(opt *Options, dest string) {
	opt.SourceSSHUser, opt.SourceSSHHost, opt.SourceSSHPort = parseSSH(dest)
}

func parseIntoTarget(opt *Options, dest string) {
	opt.TargetSSHUser, opt.TargetSSHHost, opt.TargetSSHPort = parseSSH(dest)
}

func parseSSH(dest string) (user, host string, port int) {
	port = 22
	if i := strings.Index(dest, "@"); i > 0 {
		user = dest[:i]
		dest = dest[i+1:]
	}
	host = dest
	if i := strings.LastIndex(dest, ":"); i > 0 {
		var p int
		fmt.Sscanf(dest[i+1:], "%d", &p)
		if p > 0 {
			host = dest[:i]
			port = p
		}
	}
	return
}
