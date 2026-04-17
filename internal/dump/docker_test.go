package dump

import (
	"strings"
	"testing"

	"github.com/pacnpal/gitea2forgejo/internal/config"
)

func TestBuildGiteaDumpCmd_docker(t *testing.T) {
	src := config.Instance{
		Binary:        "gitea",
		ConfigFile:    "/data/gitea/conf/app.ini",
		RemoteWorkDir: "/data/dump",
		Docker: &config.Docker{
			Container: "gitea",
			User:      "git",
			Binary:    "docker",
		},
	}
	cmd := buildGiteaDumpCmd(src, "/data/dump/gitea-dump.tar.zst", "tar.zst")
	if !strings.HasPrefix(cmd, "'docker' exec -u 'git' 'gitea' sh -c ") {
		t.Errorf("unexpected docker prefix: %s", cmd)
	}
	if !strings.Contains(cmd, "gitea") ||
		!strings.Contains(cmd, "dump") ||
		!strings.Contains(cmd, "--file") {
		t.Errorf("inner command missing pieces: %s", cmd)
	}
	if strings.Contains(cmd, "sudo") {
		t.Errorf("should not sudo when Docker is set: %s", cmd)
	}
}

func TestBuildGiteaDumpCmd_dockerNoUser(t *testing.T) {
	src := config.Instance{
		Binary:        "gitea",
		ConfigFile:    "/data/app.ini",
		RemoteWorkDir: "/data",
		Docker:        &config.Docker{Container: "gitea", Binary: "docker"},
	}
	cmd := buildGiteaDumpCmd(src, "/data/x.tar", "tar")
	if strings.Contains(cmd, "-u ") {
		t.Errorf("should not emit -u when Docker.User empty: %s", cmd)
	}
}

func TestBuildGiteaDumpCmd_podman(t *testing.T) {
	src := config.Instance{
		Binary:        "gitea",
		ConfigFile:    "/data/app.ini",
		RemoteWorkDir: "/data",
		Docker:        &config.Docker{Container: "gitea", User: "git", Binary: "podman"},
	}
	cmd := buildGiteaDumpCmd(src, "/data/x.tar", "tar")
	if !strings.HasPrefix(cmd, "'podman' exec") {
		t.Errorf("should use podman: %s", cmd)
	}
}
