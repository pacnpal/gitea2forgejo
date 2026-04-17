package restore

import (
	"strings"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

// wrapDockerCmd prefixes cmd with `docker exec -u USER CONTAINER sh -c …`.
// The inner command is shell-quoted because `docker exec` argv entries are
// passed straight to exec(3), and we built cmd as a single shell string.
func wrapDockerCmd(d *config.Docker, cmd string) string {
	prefix := shQuote(d.Binary) + " exec"
	if d.User != "" {
		prefix += " -u " + shQuote(d.User)
	}
	parts := []string{prefix, shQuote(d.Container), "sh", "-c", shQuote(cmd)}
	return strings.Join(parts, " ")
}
