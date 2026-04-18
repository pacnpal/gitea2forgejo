package restore

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/pacnpal/gitea2forgejo/internal/config"
	"github.com/pacnpal/gitea2forgejo/internal/remote"
)

// ensureTargetMounts populates cfg.Target.Docker.Mounts from `docker
// inspect` when the list is empty. Mirrors dump.inspectMounts so older
// config.yaml files (pre-runtime-mount-capture init, or hand-edited)
// don't force a re-run of init. No-op when the target is not
// containerized or when mounts were already recorded.
//
// Without this, chown-in-container, doctor, and regenerate-hooks
// silently receive host paths that don't resolve from inside the
// container — chown emits a WARN and skips; doctor / regenerate-hooks
// fail with "Unable to load config file".
func ensureTargetMounts(ssh *remote.Client, cfg *config.Config, log *slog.Logger) error {
	d := cfg.Target.Docker
	if d == nil || d.Container == "" {
		return nil
	}
	if len(d.Mounts) > 0 {
		return nil
	}
	bin := d.Binary
	if bin == "" {
		bin = "docker"
	}
	out, err := ssh.Run(fmt.Sprintf(
		"%s inspect --format '{{range .Mounts}}{{.Source}}\t{{.Destination}}\n{{end}}' %s",
		shQuote(bin), shQuote(d.Container)))
	if err != nil {
		return fmt.Errorf("docker inspect mounts: %w (%s)", err, string(out))
	}
	d.Mounts = append(d.Mounts, parseDockerMounts(string(out))...)
	if len(d.Mounts) == 0 {
		return fmt.Errorf("docker inspect %q returned no bind mounts — is the container running?", d.Container)
	}
	log.Info("discovered docker mounts for target container",
		"container", d.Container, "count", len(d.Mounts))
	return nil
}

// parseDockerMounts parses the tab-separated "<host>\t<container>\n"
// output of `docker inspect --format '{{range .Mounts}}{{.Source}}\t{{.Destination}}\n{{end}}'`.
func parseDockerMounts(out string) []config.Mount {
	var ms []config.Mount
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
			continue
		}
		ms = append(ms, config.Mount{Host: fields[0], Container: fields[1]})
	}
	return ms
}
