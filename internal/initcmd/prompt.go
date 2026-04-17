package initcmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Interactive fills in any empty fields on opt by prompting at the TTY.
// If stdin isn't a TTY, it returns without prompting (so CI / scripted
// users still get the non-interactive "missing flag" error from validate()).
//
// Each prompt shows the current default (if any) in brackets; pressing
// Enter accepts the default. Secret fields (tokens) don't echo.
func Interactive(opt *Options) error {
	in := os.Stdin
	if !term.IsTerminal(int(in.Fd())) {
		return nil
	}
	w := os.Stderr
	r := bufio.NewReader(in)

	fmt.Fprintln(w, "gitea2forgejo init — interactive setup")
	fmt.Fprintln(w, "Press Enter to accept the default shown in [brackets].")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "# Source (your existing Gitea)")
	opt.SourceURL = promptLine(r, w, "Source Gitea URL", opt.SourceURL)
	sourceSSH := sshDestFromOpt(opt.SourceSSHUser, opt.SourceSSHHost, opt.SourceSSHPort)
	sourceSSH = promptLine(r, w, "Source SSH destination (user@host[:port])", sourceSSH)
	parseIntoSource(opt, sourceSSH)
	if opt.SourceToken == "" || strings.HasPrefix(opt.SourceToken, "env:") {
		t := promptSecret(in, w, "Source admin token (leave blank to use $SOURCE_ADMIN_TOKEN)")
		if t != "" {
			opt.SourceToken = t
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "# Target (your new Forgejo)")
	opt.TargetURL = promptLine(r, w, "Target Forgejo URL", opt.TargetURL)
	targetSSH := sshDestFromOpt(opt.TargetSSHUser, opt.TargetSSHHost, opt.TargetSSHPort)
	targetSSH = promptLine(r, w, "Target SSH destination (user@host[:port])", targetSSH)
	parseIntoTarget(opt, targetSSH)
	if opt.TargetToken == "" || strings.HasPrefix(opt.TargetToken, "env:") {
		t := promptSecret(in, w, "Target admin token (leave blank to use $TARGET_ADMIN_TOKEN)")
		if t != "" {
			opt.TargetToken = t
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "# Local options")
	opt.WorkDir = promptLine(r, w, "Local work dir", opt.WorkDir)
	opt.Output = promptLine(r, w, "Output config path", opt.Output)

	if promptYN(r, w, "Skip TLS verification (only if either side uses self-signed certs)", opt.InsecureTLS) {
		opt.InsecureTLS = true
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
