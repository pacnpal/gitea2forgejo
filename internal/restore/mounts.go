package restore

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	inspectCmd := fmt.Sprintf(
		"%s inspect --format '{{range .Mounts}}{{.Source}}\t{{.Destination}}\n{{end}}' %s",
		shQuote(bin), shQuote(d.Container))

	// A previous restore may have just `docker start`ed this container;
	// if Forgejo crashed immediately after boot the container can be
	// in a transient "restarting" / "removing" / stopping state where
	// `docker inspect` returns metadata without the .Mounts field
	// populated. Retry with a short delay before declaring the mount
	// list truly empty.
	var lastOut string
	var mounts []config.Mount
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			time.Sleep(2 * time.Second)
		}
		out, err := ssh.Run(inspectCmd)
		if err != nil {
			return fmt.Errorf("docker inspect mounts: %w (%s)", err, string(out))
		}
		lastOut = string(out)
		mounts = parseDockerMounts(lastOut)
		if len(mounts) > 0 {
			break
		}
		log.Warn("docker inspect returned no bind mounts; retrying",
			"container", d.Container, "attempt", attempt, "raw", strings.TrimSpace(lastOut))
	}
	if len(mounts) == 0 {
		return fmt.Errorf("docker inspect %q returned no bind mounts after 3 attempts — container may be in a transient state; try `docker start %s` then re-run (raw inspect output: %q)",
			d.Container, d.Container, strings.TrimSpace(lastOut))
	}
	d.Mounts = append(d.Mounts, mounts...)
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
