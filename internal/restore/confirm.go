package restore

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

// confirmResetTargetDB asks the operator whether to proceed with a
// DESTRUCTIVE wipe of the target DB. Returns true only on an explicit
// "y" / "yes" at the TTY — EOF, blank line, "no", and non-TTY stdin all
// return false so CI / scripted runs behave exactly as before (the caller
// then returns the "set options.reset_target_db: true" error).
func confirmResetTargetDB(cfg *config.Config, state *TargetDBState) bool {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false
	}
	return promptResetDB(os.Stdin, os.Stderr, cfg.Target.DB.Dialect, state)
}

func promptResetDB(in io.Reader, out io.Writer, dialect string, state *TargetDBState) bool {
	action := "DROP SCHEMA public CASCADE; CREATE SCHEMA public"
	switch dialect {
	case "mysql":
		action = "DROP DATABASE; CREATE DATABASE"
	case "sqlite3":
		action = "remove sqlite DB file (+ WAL/SHM sidecars)"
	}
	fmt.Fprintf(out,
		"\nTarget DB is not empty (%d tables, version=%d, forgejo_extras=%v).\n"+
			"This most likely means Forgejo's setup wizard has been run.\n\n"+
			"Continuing requires a DESTRUCTIVE wipe of the target database:\n"+
			"  %s: %s\n\n"+
			"Drop and recreate the target schema now? [y/N]: ",
		state.TableCount, state.VersionRow, state.HasForgejoExtras, dialect, action)
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil {
		fmt.Fprintln(out)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}
