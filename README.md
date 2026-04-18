# gitea2forgejo

[![Latest release][release-badge]][release-url]
[![Release date][release-date-badge]][release-url]
[![Build status][build-badge]][build-url]
[![SLSA 3][slsa-badge]][slsa-url]
[![Downloads][downloads-badge]][downloads-url]
[![License][license-badge]][license-url]

[![Go version][go-version-badge]][go-version-url]
[![Go reference][go-ref-badge]][go-ref-url]
[![Go report card][goreport-badge]][goreport-url]
[![Code size][codesize-badge]][codesize-url]
[![Top language][lang-badge]][lang-url]
[![Repo size][reposize-badge]][reposize-url]

[![Open issues][issues-badge]][issues-url]
[![Open PRs][prs-badge]][prs-url]
[![Stars][stars-badge]][stars-url]
[![Forks][forks-badge]][forks-url]
[![Contributors][contrib-badge]][contrib-url]
[![Last commit][lastcommit-badge]][lastcommit-url]
[![Commit activity][activity-badge]][activity-url]

[![Linux][linux-badge]](#install) [![macOS][macos-badge]](#install) [![Windows][windows-badge]](#install) [![amd64][amd64-badge]](#install) [![arm64][arm64-badge]](#install)

[![Source: Gitea ≥1.23][source-badge]](#complete-walkthrough-gitea--forgejo-from-zero)
[![Target: Forgejo v15+][target-badge]](#complete-walkthrough-gitea--forgejo-from-zero)
[![Docker supported][docker-badge]](#running-gitea-or-forgejo-in-docker)
[![Unraid tested][unraid-badge]](#unraid)
[![Made with Go][made-badge]][go-ref-url]
[![Maintained][maintained-badge]][lastcommit-url]

One-time, full-fidelity migration from **Gitea ≥ 1.23** to **Forgejo v15+**.

Forgejo's official drop-in path was severed at Gitea 1.22 (see
[Forgejo's Dec 2024 announcement](https://forgejo.org/2024-12-gitea-compatibility/)).
For Gitea 1.23+ there is no supported route — only DB surgery
([forgejo#7638](https://codeberg.org/forgejo/forgejo/issues/7638)) or API-driven
migration with documented data loss. This tool combines both: native DB dump +
filesystem copy + schema-version trick, supplemented by API sync for items the
DB migration misses (webhook URL rewrites, runner tokens, OAuth callback URLs,
Actions secrets CSV).

## Status

Work in progress. See `docs/what-breaks.md` for the authoritative list of what
this tool handles and what requires manual operator action.

## Install

### Homebrew (macOS + Linux)

```sh
brew tap pacnpal/gitea2forgejo
brew install gitea2forgejo
```

Pulls in the required external tools (`rsync`, `libpq` for `pg_dump`,
`mysql-client`, `sqlite`, `zstd`) as formula dependencies automatically.

Update:

```sh
brew update && brew upgrade gitea2forgejo
```

Tap repo: https://github.com/pacnpal/homebrew-gitea2forgejo — formula is
auto-bumped by GitHub Actions on every release.

### One-line installer (recommended)

**Linux / macOS:**

```sh
curl -fsSL https://raw.githubusercontent.com/pacnpal/gitea2forgejo/main/install.sh | bash
```

**Windows (PowerShell):**

```powershell
iwr -useb https://raw.githubusercontent.com/pacnpal/gitea2forgejo/main/install.ps1 | iex
```

The installer script:

- Detects your OS and CPU (amd64 / arm64)
- **Installs all external tool dependencies** via the platform package manager:
  - **Debian/Ubuntu**: `apt` → `rsync openssh-client sqlite3 postgresql-client default-mysql-client zstd`
  - **Fedora/RHEL/CentOS**: `dnf`/`yum` → `rsync openssh-clients sqlite postgresql mariadb zstd`
  - **Arch**: `pacman` → `rsync openssh sqlite postgresql-libs mariadb-clients zstd`
  - **Alpine**: `apk` → `rsync openssh-client sqlite postgresql-client mariadb-client zstd`
  - **openSUSE**: `zypper` → `rsync openssh sqlite3 postgresql mariadb-client zstd`
  - **macOS**: `brew` → `postgresql mysql-client zstd` (rsync/ssh/sqlite preinstalled)
  - **Windows**: `winget` → `OpenSSH, Git, PostgreSQL, SQLite, zstd` (for full `dump`/`restore` flows, WSL2 is recommended)
- Resolves the latest release tag via GitHub's `/releases/latest` redirect
- Downloads the matching binary
- On Linux: installs to `/usr/local/bin/gitea2forgejo` (prompts for `sudo` if the directory isn't writable)
- On macOS: clears `com.apple.quarantine` and applies an ad-hoc `codesign` so Gatekeeper doesn't block the first run
- On Windows: installs to `%LOCALAPPDATA%\Programs\gitea2forgejo\`, unblocks the file (removes SmartScreen zone marker), and adds the directory to your user `PATH`
- Verifies the install with `gitea2forgejo --version`

**Environment variable overrides** (same on both platforms):

- `INSTALL_DIR` — override the target directory
- `VERSION` — pin a specific release tag (`VERSION=v0.2.9 curl ... | bash`)
- `SKIP_DEPS` — set to `1` to skip the dependency install step

**To update later:** just run `gitea2forgejo update`, or re-run the installer — both are idempotent.

### Pre-built release binary (manual)

Each release attaches static binaries for 6 platforms, built and signed by
the [SLSA3 Go builder](https://github.com/slsa-framework/slsa-github-generator)
with one `.intoto.jsonl` provenance attestation per binary.

| Platform (Go)          | OS                         | CPU             | File                                   |
|------------------------|----------------------------|-----------------|----------------------------------------|
| `linux/amd64`          | Linux                      | x86-64 / Intel  | `gitea2forgejo-linux-amd64`            |
| `linux/arm64`          | Linux                      | ARM64 / aarch64 | `gitea2forgejo-linux-arm64`            |
| `darwin/amd64`         | macOS (Intel Macs)         | x86-64          | `gitea2forgejo-darwin-amd64`           |
| `darwin/arm64`         | macOS (Apple Silicon)      | ARM64 (M1/M2/M3/M4) | `gitea2forgejo-darwin-arm64`       |
| `windows/amd64`        | Windows                    | x86-64          | `gitea2forgejo-windows-amd64.exe`      |
| `windows/arm64`        | Windows                    | ARM64           | `gitea2forgejo-windows-arm64.exe`      |

```sh
PLATFORM=linux-amd64        # see table above

curl -L -o gitea2forgejo \
  https://github.com/pacnpal/gitea2forgejo/releases/latest/download/gitea2forgejo-$PLATFORM
chmod +x gitea2forgejo
sudo mv gitea2forgejo /usr/local/bin/
gitea2forgejo --version
```

GitHub's `/releases/latest/download/` URLs always redirect to the newest
non-prerelease asset, so this command keeps working across future releases
without edits. To pin to a specific version, swap `/latest/download/` for
`/download/v0.2.0/` (or whatever tag you want).

#### macOS: running the unsigned binary

The release binaries are **not** Apple Developer-ID signed or notarized —
Gatekeeper will refuse to run them by default. Two mitigation options:

**Option A: strip the quarantine attribute (simplest).**

```sh
curl -L -o gitea2forgejo \
  https://github.com/pacnpal/gitea2forgejo/releases/latest/download/gitea2forgejo-darwin-arm64
xattr -dr com.apple.quarantine gitea2forgejo      # remove Gatekeeper flag
chmod +x gitea2forgejo
./gitea2forgejo --version
```

**Option B: ad-hoc self-sign (survives `xattr` resets and works across
subsequent runs without Gatekeeper prompting).**

```sh
codesign --force --sign - gitea2forgejo
```

**macOS 26 ("Tahoe") extra step.** Tahoe hardened Gatekeeper: double-clicking
an unsigned binary no longer offers the old "right-click → Open" override
from a Finder contextual menu. Workflow:

1. Try to run once from Terminal — it will fail with a Gatekeeper message.
2. Open **System Settings → Privacy & Security**, scroll to the
   *"'gitea2forgejo' was blocked to protect your Mac"* banner, and click
   **"Open Anyway"** (Touch ID / admin password required).
3. Run the binary again from Terminal; you'll be prompted once more to
   confirm, then it executes normally thereafter.

If `xattr -dr com.apple.quarantine` + `codesign --force --sign -` are both
applied **before** first launch, Tahoe skips the Settings step entirely
because there's no quarantine flag for Gatekeeper to act on.

**Avoid `sudo spctl --master-disable`** — that disables Gatekeeper
system-wide and is stronger than you want.

#### Platform-specific dependencies

- **Linux**: primary target. All external commands (`rsync`, `pg_dump`,
  `tar`, `mc`, `skopeo`) are in distro package repos.
- **macOS**: install `rsync`, `postgresql` (for `pg_dump`), `zstd`, `mc` and
  `skopeo` via Homebrew.
- **Windows**: native binaries run and the API-only flows (`preflight`,
  manifest harvest, API supplement) work, but dump/restore shell out to
  `rsync` / `pg_dump` / tar-with-zstd. Use from WSL2 or Git Bash with MSYS2
  packages installed; native PowerShell is not supported.

#### Verify the SLSA provenance (recommended)

```sh
# Install once.
go install github.com/slsa-framework/slsa-verifier/v2/cli/slsa-verifier@latest

PLATFORM=linux-amd64

# Fetch binary + its provenance from the latest release.
curl -L -o gitea2forgejo-$PLATFORM \
  https://github.com/pacnpal/gitea2forgejo/releases/latest/download/gitea2forgejo-$PLATFORM
curl -L -o gitea2forgejo-$PLATFORM.intoto.jsonl \
  https://github.com/pacnpal/gitea2forgejo/releases/latest/download/gitea2forgejo-$PLATFORM.intoto.jsonl

# slsa-verifier needs the exact tag to cross-check against; resolve it from
# the release API in one step.
VERSION=$(gh release view --repo pacnpal/gitea2forgejo --json tagName --jq .tagName)
# Or without gh:
# VERSION=$(curl -sI https://github.com/pacnpal/gitea2forgejo/releases/latest | \
#   awk -F/ '/^location:/ {print $NF}' | tr -d '\r')

slsa-verifier verify-artifact \
  --provenance-path gitea2forgejo-$PLATFORM.intoto.jsonl \
  --source-uri github.com/pacnpal/gitea2forgejo \
  --source-tag $VERSION \
  gitea2forgejo-$PLATFORM
```

### `go install`

```sh
go install github.com/pacnpal/gitea2forgejo/cmd/gitea2forgejo@latest
```

The binary lands at `$(go env GOPATH)/bin/gitea2forgejo`. This route does
NOT produce a SLSA provenance; use the release binary if you want supply-chain
attestations.

### Build from source

```sh
git clone https://github.com/pacnpal/gitea2forgejo
cd gitea2forgejo
go build -o gitea2forgejo ./cmd/gitea2forgejo
```

Requires Go 1.26+. The binary is fully static (`CGO_ENABLED=0`) and works on
any linux/amd64 host.

<!-- ------------------------ badge reference block --------------------- -->
[release-badge]: https://img.shields.io/github/v/release/pacnpal/gitea2forgejo?display_name=tag&sort=semver&style=flat-square&logo=github
[release-url]: https://github.com/pacnpal/gitea2forgejo/releases/latest
[release-date-badge]: https://img.shields.io/github/release-date/pacnpal/gitea2forgejo?style=flat-square
[build-badge]: https://img.shields.io/github/actions/workflow/status/pacnpal/gitea2forgejo/slsa-go-releaser.yml?branch=main&style=flat-square&logo=githubactions&label=SLSA%20build
[build-url]: https://github.com/pacnpal/gitea2forgejo/actions/workflows/slsa-go-releaser.yml
[slsa-badge]: https://img.shields.io/badge/SLSA-level%203-9cf?style=flat-square
[slsa-url]: https://slsa.dev
[downloads-badge]: https://img.shields.io/github/downloads/pacnpal/gitea2forgejo/total?style=flat-square&logo=github
[downloads-url]: https://github.com/pacnpal/gitea2forgejo/releases
[license-badge]: https://img.shields.io/github/license/pacnpal/gitea2forgejo?style=flat-square
[license-url]: https://github.com/pacnpal/gitea2forgejo/blob/main/LICENSE

[go-version-badge]: https://img.shields.io/github/go-mod/go-version/pacnpal/gitea2forgejo?style=flat-square&logo=go
[go-version-url]: https://go.dev
[go-ref-badge]: https://pkg.go.dev/badge/github.com/pacnpal/gitea2forgejo.svg
[go-ref-url]: https://pkg.go.dev/github.com/pacnpal/gitea2forgejo
[goreport-badge]: https://goreportcard.com/badge/github.com/pacnpal/gitea2forgejo?style=flat-square
[goreport-url]: https://goreportcard.com/report/github.com/pacnpal/gitea2forgejo
[codesize-badge]: https://img.shields.io/github/languages/code-size/pacnpal/gitea2forgejo?style=flat-square
[codesize-url]: https://github.com/pacnpal/gitea2forgejo
[lang-badge]: https://img.shields.io/github/languages/top/pacnpal/gitea2forgejo?style=flat-square&logo=go
[lang-url]: https://github.com/pacnpal/gitea2forgejo
[reposize-badge]: https://img.shields.io/github/repo-size/pacnpal/gitea2forgejo?style=flat-square
[reposize-url]: https://github.com/pacnpal/gitea2forgejo

[issues-badge]: https://img.shields.io/github/issues/pacnpal/gitea2forgejo?style=flat-square
[issues-url]: https://github.com/pacnpal/gitea2forgejo/issues
[prs-badge]: https://img.shields.io/github/issues-pr/pacnpal/gitea2forgejo?style=flat-square
[prs-url]: https://github.com/pacnpal/gitea2forgejo/pulls
[stars-badge]: https://img.shields.io/github/stars/pacnpal/gitea2forgejo?style=flat-square&logo=github
[stars-url]: https://github.com/pacnpal/gitea2forgejo/stargazers
[forks-badge]: https://img.shields.io/github/forks/pacnpal/gitea2forgejo?style=flat-square&logo=github
[forks-url]: https://github.com/pacnpal/gitea2forgejo/network/members
[contrib-badge]: https://img.shields.io/github/contributors/pacnpal/gitea2forgejo?style=flat-square
[contrib-url]: https://github.com/pacnpal/gitea2forgejo/graphs/contributors
[lastcommit-badge]: https://img.shields.io/github/last-commit/pacnpal/gitea2forgejo?style=flat-square
[lastcommit-url]: https://github.com/pacnpal/gitea2forgejo/commits/main
[activity-badge]: https://img.shields.io/github/commit-activity/m/pacnpal/gitea2forgejo?style=flat-square
[activity-url]: https://github.com/pacnpal/gitea2forgejo/commits/main

[linux-badge]: https://img.shields.io/badge/linux-supported-blue?style=flat-square&logo=linux&logoColor=white
[macos-badge]: https://img.shields.io/badge/macOS-supported-blue?style=flat-square&logo=apple&logoColor=white
[windows-badge]: https://img.shields.io/badge/windows-supported-blue?style=flat-square&logo=windows&logoColor=white
[amd64-badge]: https://img.shields.io/badge/amd64-supported-blue?style=flat-square
[arm64-badge]: https://img.shields.io/badge/arm64-supported-blue?style=flat-square

[source-badge]: https://img.shields.io/badge/source-Gitea%20%E2%89%A51.23-609926?style=flat-square&logo=gitea&logoColor=white
[target-badge]: https://img.shields.io/badge/target-Forgejo%20v15%2B-ff671f?style=flat-square
[docker-badge]: https://img.shields.io/badge/Docker-supported-2496ED?style=flat-square&logo=docker&logoColor=white
[unraid-badge]: https://img.shields.io/badge/Unraid-tested-f15a2c?style=flat-square
[made-badge]: https://img.shields.io/badge/made%20with-Go-00ADD8?style=flat-square&logo=go&logoColor=white
[maintained-badge]: https://img.shields.io/maintenance/yes/2026?style=flat-square

## Updating

Check what's running:

```sh
gitea2forgejo --version
```

See what's new for each release at
https://github.com/pacnpal/gitea2forgejo/releases.

### Update a release binary

Same `curl` as initial install, overwriting the file in place. Uses the
`/latest/` URL so you never need to edit the version:

```sh
PLATFORM=linux-amd64
curl -L -o /tmp/gitea2forgejo \
  https://github.com/pacnpal/gitea2forgejo/releases/latest/download/gitea2forgejo-$PLATFORM
chmod +x /tmp/gitea2forgejo
sudo mv /tmp/gitea2forgejo /usr/local/bin/gitea2forgejo
gitea2forgejo --version          # confirm new version shown
```

Pin to a specific tag by swapping `/latest/download/` for
`/download/vX.Y.Z/`.

On macOS, reapply the Gatekeeper mitigation (`xattr -dr com.apple.quarantine`
or `codesign --force --sign -`) after downloading the new binary — the
quarantine flag is set on the new download even if you cleared it on the
old one.

### Update a `go install` binary

```sh
go install github.com/pacnpal/gitea2forgejo/cmd/gitea2forgejo@latest
```

Or pin to a specific version: `…@v0.1.2`.

### Update a source build

```sh
cd /path/to/gitea2forgejo
git fetch --tags
git checkout v0.1.2                 # or: git checkout main
go build -o gitea2forgejo ./cmd/gitea2forgejo
```

### Upgrade during an in-flight migration

Once you've run `dump` against a source, prefer finishing that migration with
the **same** binary version that produced the dump. Upgrading between `dump`
and `restore` is low-risk on patch bumps (everything in `work_dir` is plain
files + JSON), but newer versions may add manifest fields the old `restore`
doesn't know about.

Breaking changes that would affect in-flight runs are flagged as **MAJOR**
version bumps in the release notes.

## Subcommands

| Command      | Status      | Purpose                                                        |
|--------------|-------------|----------------------------------------------------------------|
| `init`       | ✅ shipped  | SSH to source, read app.ini, auto-populate `config.yaml`.      |
| `preflight`  | ✅ shipped  | Read-only checks: versions, SSH, DB, disk, `SECRET_KEY`.       |
| `dump`       | ✅ shipped  | `gitea dump` + native DB dump + S3 mirror + source manifest.   |
| `restore`    | ✅ shipped  | File copy, DB import, schema trick, `forgejo doctor`.          |
| `supplement` | 🚧 planned  | API fixes: hostname rewrites, runner tokens, Actions CSVs.     |
| `verify`     | 🚧 planned  | Re-harvest target manifest, diff against source, emit report.  |
| `migrate`    | 🚧 planned  | Run all five in order, with `--resume-from=<phase>`.           |

Until `migrate` lands, run `preflight` → `dump` → `restore` by hand in that
order (see [Usage](#usage) below).

## Complete walkthrough: Gitea → Forgejo from zero

This section is the "I only have a Gitea server and want everything on
Forgejo" guide. It walks through infrastructure provisioning, data
handoff, cutover, and decommission end to end. Expect **3 – 10 hours**
total elapsed time depending on repo/LFS volume; most of that is the
`gitea dump` tarball transfer and DB dump/restore.

### Terminology

| Term            | Meaning                                                              |
|-----------------|----------------------------------------------------------------------|
| **source**      | Your existing Gitea server (≥ 1.23). Call this `gitea.example.com`.  |
| **target**      | The new Forgejo server you will stand up. `forgejo.example.com`.     |
| **mig-host**    | The machine you run `gitea2forgejo` *on*. Can be your laptop.        |
| **work_dir**    | Local scratch directory on mig-host where all dump artifacts land.   |

The mig-host needs network reachability + SSH access to both source and
target, plus DB reachability to both databases. It does NOT have to be
either the source or the target — in fact you'll have fewer surprises if
it's a third box.

### What you need before you start

Open a note-taking scratchpad; you'll fill in these values as you go.

**Source Gitea (existing):**
- [ ] URL: e.g. `https://gitea.example.com`
- [ ] Gitea version (run `gitea --version` on the host); must be ≥ 1.23
- [ ] `app.ini` path: typically `/etc/gitea/app.ini` or `/var/lib/gitea/custom/conf/app.ini`
- [ ] Data directory (`[server].APP_DATA_PATH`): typically `/var/lib/gitea/data`
- [ ] Repo root (`[repository].ROOT`): typically `/var/lib/gitea/git/repositories`
- [ ] DB dialect (`postgres` / `mysql` / `sqlite3`) from `[database].DB_TYPE`
- [ ] DB DSN (reconstructed from `[database].HOST` / `.NAME` / `.USER` / `.PASSWD`)
- [ ] SSH user with sudo rights on the source host
- [ ] Object storage? If `[storage.*]` is configured with an S3/MinIO
      backend, capture endpoint, bucket, access key, secret key
- [ ] Size of data dir: `du -sh /var/lib/gitea` — used for free-space planning

**Target (you will build this):**
- [ ] A host with ≥ 2× source data-dir disk space, same CPU arch as source
      (for LFS compatibility — doesn't matter for most users)
- [ ] DNS name you'll cut over to: e.g. `forgejo.example.com`
- [ ] DB server (can be the same Postgres/MySQL cluster, different database)
- [ ] TLS strategy: Let's Encrypt / existing reverse proxy / self-signed

### Step 1 — Install `gitea2forgejo` on mig-host

Pick any of the three install paths under [Install](#install) above. Quick
version for Linux:

```sh
curl -L -o gitea2forgejo \
  https://github.com/pacnpal/gitea2forgejo/releases/latest/download/gitea2forgejo-linux-amd64
chmod +x gitea2forgejo && sudo mv gitea2forgejo /usr/local/bin/
gitea2forgejo --version
```

### Step 2 — Install the OS-level helpers mig-host shells out to

```sh
# Debian 13+ / recent Ubuntu — mysql-client was dropped;
# MariaDB's client is a drop-in (provides mysql / mysqldump).
sudo apt install rsync postgresql-client default-mysql-client zstd openssh-client

# Debian 12 / older Ubuntu
sudo apt install rsync postgresql-client mysql-client zstd openssh-client

# Fedora / RHEL — `mariadb` provides mysql + mysqldump.
sudo dnf install rsync postgresql mariadb zstd openssh-clients

# macOS (Homebrew)
brew install rsync postgresql mysql-client zstd
```

Skip the MySQL/MariaDB package if your source + target both use Postgres
(or both SQLite). Skip `postgresql-client` / `postgresql` if neither uses
Postgres. You only need the client tools for the DB engine(s) your
instances actually run.

Additionally:

- [mc (MinIO client)](https://min.io/docs/minio/linux/reference/minio-mc.html)
  if your source uses S3/MinIO storage
- [skopeo](https://github.com/containers/skopeo) if your source has OCI
  container packages in its registry

### Speed run: `gitea2forgejo init`

If you want `gitea2forgejo` to figure out as much of `config.yaml` as it
can on its own:

```sh
# Interactive: just run it and answer the prompts.
gitea2forgejo init

# Or one-shot:
export SOURCE_ADMIN_TOKEN=gta_...
export TARGET_ADMIN_TOKEN=fjo_...

gitea2forgejo init \
  --source-url   https://gitea.example.com \
  --source-ssh   root@gitea.example.com \
  --target-url   https://forgejo.example.com \
  --target-ssh   root@forgejo.example.com \
  -o config.yaml
```

If any required flags are missing, `init` prompts for them at the TTY
(admin tokens are masked). Pass every flag to skip the prompts — useful
for CI / scripted runs.

`init` does:

1. **SSH bootstrap** — handles three common setup states automatically:

   - **Host not in `~/.ssh/known_hosts`** → silently runs
     `ssh-keyscan -H <host>` and appends the result. No prompting; it's
     safe because a CONFLICTING host key would produce a different error
     and fall through to the interactive path (we never auto-accept a
     changed key).
   - **Key file missing** → looks for `~/.ssh/id_ed25519`, `id_ecdsa`,
     `id_rsa`, then falls back to `$SSH_AUTH_SOCK` (ssh-agent).
   - **No usable credentials at all** → at a TTY, offers to fix:
     ```
     SSH to source 192.168.86.3 failed:
       ssh dial: ... no usable SSH auth
     Generate a new key and install it on 192.168.86.3? [Y/n]:
     ```
     On yes, it runs `ssh-keygen -t ed25519 -f ~/.ssh/gitea2forgejo`,
     then `ssh-keyscan` to prime known_hosts, then `ssh-copy-id` (which
     prompts once for the remote password), then retries. Repeats for
     the target host.

   On non-TTY stdin (CI/scripted), interactive prompts are skipped; the
   silent known_hosts fix still applies.
2. Runs `docker ps` on the remote to detect whether Gitea is in a
   container (and if so, `docker inspect` to resolve the bind-mounted
   `app.ini` path).
3. Reads the source `app.ini` and extracts: `data_dir`, `repo_root`,
   DB type + host + port + name + user, S3 storage config.
4. Does the same for the target (best-effort — fresh Forgejo installs
   often won't have an app.ini yet, in which case it falls back to
   standard defaults).
5. Writes `config.yaml` with secrets as `env:<NAME>` references so you
   never commit them to disk.

After it runs, export the env vars it refers to and run
`gitea2forgejo preflight --config config.yaml`. That's usually all the
setup you need.

Requirements: `ssh-keygen`, `ssh-keyscan`, and `ssh-copy-id` must be on
`$PATH` for the bootstrap to work (they ship with `openssh-client` on
all major distros and come preinstalled on macOS).

### Running Gitea or Forgejo in Docker

If either side runs in Docker (or Podman), add a `docker:` block to that
instance. The tool still SSHes to the **Docker host** (not the container);
the block just wraps `gitea dump`, `forgejo doctor`, etc. in `docker exec`.

```yaml
source:
  url: https://gitea.example.com
  ssh:
    host: docker-host.example.com    # the VM running Docker, not a container
    user: root
    key: ~/.ssh/gitea2forgejo
  # Paths below are HOST paths — the bind-mounted volumes on the Docker
  # host. Rsync reads from them directly; gitea dump writes to them from
  # inside the container.
  config_file: /srv/gitea/data/gitea/conf/app.ini
  data_dir:    /srv/gitea/data
  repo_root:   /srv/gitea/data/git/repositories
  custom_dir:  /srv/gitea/data/gitea
  remote_work_dir: /srv/gitea/data/migration   # must be bind-mounted!
  docker:
    container: gitea          # from `docker ps`
    user: git                 # user inside the container
    binary: docker            # or "podman"
```

**The critical constraint** is that `remote_work_dir` must be a host path
that is bind-mounted at the same path inside the container. `gitea dump`
runs inside the container and writes its tarball to that path; the host
sees the file at the bind-mount location and SFTP fetches it from there.

If your `docker-compose.yml` mounts `/srv/gitea/data → /data`, `gitea dump`
will happily write to `/data/migration/…` inside the container, but the
host path is `/srv/gitea/data/migration`. Set `remote_work_dir` to the
**host** side path and make the container-internal binding match:

```yaml
# docker-compose.yml (for the source Gitea)
services:
  gitea:
    image: gitea/gitea:1.23
    volumes:
      - /srv/gitea/data:/srv/gitea/data    # <<< bind at same path both sides
```

OR keep the container's internal `/data/...` and bind to the matching
host path:

```yaml
services:
  gitea:
    volumes:
      - /srv/gitea:/data
# and in gitea2forgejo config:
source:
  data_dir: /srv/gitea                       # host side
  remote_work_dir: /srv/gitea/migration      # host side
  docker:
    container: gitea
# inside the container /data/migration is writable and appears on host
# at /srv/gitea/migration — but the paths don't match. gitea dump
# will write using the --file arg you pass (host path), which the
# container sees at a different location and fails.
# Safer: keep paths IDENTICAL on both sides via bind-mount same-path.
```

Similarly for the target Forgejo's `docker:` block.

#### Ready-to-use target Compose stack

Instead of hand-rolling the target Forgejo + Postgres setup, copy the
templates in [`templates/`](templates/):

```sh
curl -L -o docker-compose.yml \
  https://raw.githubusercontent.com/pacnpal/gitea2forgejo/main/templates/docker-compose.target.yml
curl -L -o .env \
  https://raw.githubusercontent.com/pacnpal/gitea2forgejo/main/templates/docker-compose.env.example
$EDITOR .env                            # set FORGEJO_DOMAIN + DB credentials

docker compose up -d db                 # wait for DB to be healthy
docker compose up -d forgejo            # starts, creates schema — DO NOT visit the web UI
docker compose stop forgejo             # leave stopped until restore completes
```

The compose file uses **identical host and container paths**
(`/srv/forgejo/data` → `/srv/forgejo/data`) so `remote_work_dir` works
with no path translation; match it in `config.yaml`:

```yaml
target:
  data_dir: /srv/forgejo/data
  repo_root: /srv/forgejo/repositories
  custom_dir: /srv/forgejo/custom
  remote_work_dir: /srv/forgejo/data/migration
  docker:
    container: forgejo
    user: git
```

#### Unraid

Unraid Community Applications installs Gitea (and Forgejo) as managed
Docker containers. For gitea2forgejo:

- **SSH target**: `root@<unraid-host>` on port 22. Unraid's root shell is
  enabled by default.
- **Paths on the Unraid host** (standard CA template):
  - `/mnt/user/appdata/gitea/gitea/conf/app.ini` — the app.ini
  - `/mnt/user/appdata/gitea/` — data/repos/custom all live under here
- **Container name**: usually `Gitea` (capital G — Unraid's CA templates
  preserve the casing). Check with `docker ps --format '{{.Names}}'` via
  Unraid's terminal.
- **`docker:` block in `config.yaml`**:
  ```yaml
  source:
    ssh: { host: tower.local, user: root, key: ~/.ssh/gitea2forgejo }
    config_file: /mnt/user/appdata/gitea/gitea/conf/app.ini
    data_dir:   /mnt/user/appdata/gitea
    repo_root:  /mnt/user/appdata/gitea/git/repositories
    custom_dir: /mnt/user/appdata/gitea/gitea
    remote_work_dir: /mnt/user/appdata/gitea/migration
    docker:
      container: Gitea
      user: git
      binary: docker
  ```
- `gitea2forgejo init --source-ssh root@tower.local ...` handles all
  of the above automatically on most Unraid installs; verify the detected
  paths before running preflight.

Unraid caveats:

- Don't run migration during an Unraid "Parity Check" — disk I/O will
  be miserable.
- The gitea user inside the CA-templated container is usually `git`
  (uid 1000). If you customized PUID/PGID in the Gitea template, update
  the `docker.user` in your config accordingly.

### Step 3 — Provision the target host and install Forgejo v15

**Do this before you start the cutover** — installing Forgejo takes time
you don't want on your downtime critical path.

1. **Stand up the target host** (cloud VM, bare metal, container — whatever
   your Linux distro strategy is). Give it a private IP on a network the
   mig-host can SSH to.

2. **Create a `forgejo` system user**:
   ```sh
   sudo useradd --system --home /var/lib/forgejo --shell /bin/bash forgejo
   sudo mkdir -p /var/lib/forgejo /etc/forgejo /var/log/forgejo
   sudo chown forgejo: /var/lib/forgejo /var/log/forgejo
   sudo chmod 750 /etc/forgejo && sudo chown root:forgejo /etc/forgejo
   ```

3. **Install the Forgejo v15 binary** and systemd unit:
   ```sh
   # see https://forgejo.org/download/ for the current LTS URL
   VER=v15.0.0
   sudo curl -L -o /usr/local/bin/forgejo \
     https://codeberg.org/forgejo/forgejo/releases/download/$VER/forgejo-15.0.0-linux-amd64
   sudo chmod +x /usr/local/bin/forgejo

   sudo curl -o /etc/systemd/system/forgejo.service \
     https://codeberg.org/forgejo/forgejo/raw/branch/forgejo/contrib/systemd/forgejo.service
   sudo systemctl daemon-reload
   ```

4. **Provision the target DB.** Empty schema, dedicated user.
   ```sh
   # Postgres example:
   sudo -u postgres psql <<'SQL'
   CREATE USER forgejo WITH PASSWORD 'change-me-now';
   CREATE DATABASE forgejo OWNER forgejo ENCODING 'UTF8';
   SQL
   ```

5. **Ideally, do not start Forgejo yet** and do not run its web-based
   initial setup wizard. The target is simplest when it stays at "empty DB,
   binary installed, service stopped" until `gitea2forgejo restore` drops
   data into it.

   **If you already ran the setup wizard (very common):** set
   `options.reset_target_db: true` in `config.yaml`. `preflight` will flag
   the pre-populated schema as a FAIL; `restore` will wipe the target DB
   (`DROP SCHEMA public CASCADE` on Postgres, `DROP DATABASE` on MySQL,
   `rm` the sqlite file) before importing the source dump. The reset is
   gated behind this flag specifically because it's destructive — you
   don't want to silently nuke a production target.

6. **Leave the service stopped but enable it** so it starts automatically
   on boot:
   ```sh
   sudo systemctl enable forgejo      # enabled, not started
   ```

7. **Set up your reverse proxy / TLS** (nginx / Caddy / Traefik) pointing
   at `127.0.0.1:3000` on the target. You can do this now even though
   Forgejo isn't running — the proxy will just return 502 until we start it.

### Step 4 — Admin tokens

On the **source** Gitea (still running at this point):

1. Log in as a site admin user
2. **User menu → Settings → Applications → Generate New Token**
3. Name it "gitea2forgejo-migration"
4. Tick **all** scopes (this is a one-time admin migration)
5. Copy the token immediately — it is shown once only

Save as env var on mig-host:
```sh
export GITEA_ADMIN_TOKEN=gta_...
```

You'll create the target token *after* `restore` completes — at that point
Forgejo will have imported your source user records, so you log in with the
same admin credentials you had on source.

For preflight + restore, `gitea2forgejo` needs to hit the target API. Two
options:

- **Option A (recommended):** during target Forgejo install (step 3), also
  create a throwaway admin user via the CLI and generate a token for it:
  ```sh
  sudo -u forgejo forgejo admin user create --admin \
    --username bootstrap --email root@localhost --random-password
  sudo -u forgejo forgejo admin user generate-access-token \
    --username bootstrap --scopes all
  ```
  This user gets overwritten during restore, so it's disposable. Save the
  token as `FORGEJO_ADMIN_TOKEN`.

- **Option B:** skip preflight's target-side checks (accept the warning) and
  only populate `FORGEJO_ADMIN_TOKEN` between restore and the eventual
  `supplement` / `verify` phases.

### Step 5 — SSH key access from mig-host to both hosts

The migration user needs to run commands on both source and target. Set up
a dedicated ed25519 key and deploy the public half to both sides:

```sh
ssh-keygen -t ed25519 -f ~/.ssh/gitea2forgejo -C gitea2forgejo-migration
ssh-copy-id -i ~/.ssh/gitea2forgejo.pub root@gitea.example.com
ssh-copy-id -i ~/.ssh/gitea2forgejo.pub root@forgejo.example.com
```

Prime `known_hosts` so the tool's strict host-key verification succeeds:

```sh
ssh-keyscan -H gitea.example.com forgejo.example.com >> ~/.ssh/known_hosts
```

Verify both work:
```sh
ssh -i ~/.ssh/gitea2forgejo root@gitea.example.com true && echo source OK
ssh -i ~/.ssh/gitea2forgejo root@forgejo.example.com true && echo target OK
```

### Step 6 — Populate `config.yaml`

Copy the template and fill it in with the values from your scratchpad:

```sh
curl -L -o example.config.yaml \
  https://raw.githubusercontent.com/pacnpal/gitea2forgejo/main/example.config.yaml
cp example.config.yaml config.yaml
$EDITOR config.yaml
```

All the information the binary needs (organized by config field):

| Config field                         | What it is                                                  |
|--------------------------------------|-------------------------------------------------------------|
| `source.url`                         | Public URL of source Gitea                                  |
| `source.admin_token`                 | `env:GITEA_ADMIN_TOKEN` — from step 4                       |
| `source.insecure_tls`                | `true` if source uses self-signed cert; else `false`        |
| `source.ssh.host`                    | Hostname of source host                                     |
| `source.ssh.user`                    | SSH user with sudo rights                                   |
| `source.ssh.key`                     | Path to your private key (e.g. `~/.ssh/gitea2forgejo`)      |
| `source.ssh.known_hosts`             | `~/.ssh/known_hosts` (or leave default)                     |
| `source.config_file`                 | Absolute path to `app.ini` on source                        |
| `source.data_dir`                    | `[server].APP_DATA_PATH` from source `app.ini`              |
| `source.repo_root`                   | `[repository].ROOT` from source `app.ini`                   |
| `source.custom_dir`                  | Usually `$data_dir/../custom` or `/var/lib/gitea/custom`    |
| `source.binary`                      | Path or name of gitea binary on source (default: `gitea`)   |
| `source.run_as`                      | User to `sudo -u` for `gitea dump` (usually `gitea`)        |
| `source.remote_work_dir`             | Writable scratch dir on source (default: `/tmp/gitea2forgejo`) |
| `source.db.dialect`                  | `postgres` / `mysql` / `sqlite3`                            |
| `source.db.dsn`                      | `env:GITEA_DB_DSN` — see format below                       |
| `source.storage.*`                   | Only if you use S3/MinIO (else omit the block)              |
| `target.*`                           | Same fields for target Forgejo                              |
| `work_dir`                           | Local scratch on mig-host; needs ≥ 2× source data size free |
| `hostname_rewrites`                  | List of `{from, to}` pairs for webhook URL / OAuth callback |
| `options.dump_format`                | `tar.zst` (default) / `tar.gz` / `tar` / `zip`              |
| `options.skip_*`                     | Skip specific dump stages (rehearsal use)                   |
| `options.reset_target_db`            | DESTRUCTIVE. `true` if you already ran Forgejo setup wizard |

DSN formats:
```
# Postgres
postgres://user:password@host:5432/dbname?sslmode=disable

# MySQL (go-sql-driver form)
user:password@tcp(host:3306)/dbname?parseTime=true

# SQLite3 (just the file path)
/var/lib/gitea/data/gitea.db
```

Export the secrets as env vars (the config's `env:FOO` references resolve
them at runtime):

```sh
export GITEA_ADMIN_TOKEN=gta_...
export FORGEJO_ADMIN_TOKEN=fjo_...
export GITEA_DB_DSN='postgres://gitea:secret@gitea-db.example.com:5432/gitea?sslmode=disable'
export FORGEJO_DB_DSN='postgres://forgejo:secret@forgejo-db.example.com:5432/forgejo?sslmode=disable'
```

### Step 7 — Preflight (read-only; run as many times as you want)

```sh
gitea2forgejo preflight --config config.yaml
cat $(yq -r .work_dir config.yaml)/preflight-report.md
```

Every check must be PASS (some WARNs are tolerable — read them). In
particular, **`SECRET_KEY` / `INTERNAL_TOKEN` / `JWT_SECRET` present**
is non-negotiable. If any are empty in source `app.ini`, STOP and
regenerate them (then users will need to re-login). Proceeding without
them means every 2FA secret, OAuth app client secret, and encrypted
Actions secret on source becomes unrecoverable garbage.

Also watch for the **target: db empty** check. If it FAILs reporting
tables present, someone has run Forgejo's setup wizard — set
`options.reset_target_db: true` in `config.yaml` and re-run preflight.

### Step 8 — Rehearse against a disposable pair (strongly recommended)

1. Snapshot source DB: `pg_dump -Fc $GITEA_DB_DSN > pre-migration.dump`
2. Restore into a disposable Postgres on a throwaway VM
3. Boot a disposable Gitea pointed at the disposable DB
4. Point a second throwaway VM at that disposable Gitea as the "source"
   in a staging `config.yaml`, with a *third* throwaway VM as the
   "target" Forgejo
5. Run the full `preflight` → `dump` → `restore` flow
6. Log in as a real user from source; verify 2FA works, webhooks fire,
   an Actions workflow with a secret succeeds, LFS clones, a package pulls

Every surprise discovered during rehearsal is a surprise you don't hit
during the real cutover.

### Step 9 — Announce downtime and freeze source

**Announcement template** (24 – 72 hours before):

> Subject: Gitea migration, $DATE, downtime expected $N hours.
> We're moving from Gitea to Forgejo. During the cutover window you'll
> be logged out, git push/pull will be unavailable, webhooks will not
> fire, and Actions runs will not start. After the window, please
> **re-login**, **re-enroll 2FA if the Authenticator app doesn't
> accept your old token** (unlikely but possible), and **regenerate
> your Personal Access Tokens** (these cannot be migrated — old PATs
> will not work on the new server).

When the window opens:

```sh
ssh root@gitea.example.com 'sudo systemctl stop gitea'
```

Confirm it's really stopped: `curl -I https://gitea.example.com/` should
fail to connect.

### Step 10 — Run `dump`

```sh
gitea2forgejo dump --config config.yaml
```

Stages and their outputs in `work_dir`:

| Stage         | Output                                                              |
|---------------|---------------------------------------------------------------------|
| API harvest   | `source-manifest.json` (full entity inventory of users, orgs, repos, etc.) |
| login_source  | merged into the same manifest (LDAP/OAuth2/SMTP auth source definitions)   |
| `gitea dump`  | `gitea-dump.tar.zst` (app.ini + data/repos/custom + xorm SQL)       |
| native DB     | `gitea.dump` (Postgres) / `gitea.sql` (MySQL) / `gitea.sqlite`      |
| S3 mirror     | `s3/` (attachments, lfs, packages, avatars)                         |

Duration: **30 min – 6 hours** depending on repo + LFS volume.

### Step 11 — Run `restore`

```sh
gitea2forgejo restore --config config.yaml
```

11 steps, all logged:

1. SSH to target, `systemctl stop forgejo`
2. Extract `gitea-dump.tar.zst` into `work_dir/extracted/`
3. `rsync` `data/`, `repos/`, `custom/` to the target host
4. Translate source `app.ini` → target `app.ini` (preserve `SECRET_KEY`,
   rewrite hostname, rewrite data paths, set `COOKIE_REMEMBER_NAME`,
   set `[actions].DEFAULT_ACTIONS_URL`)
5. `pg_restore` / `mysql < dump.sql` / sqlite copy into target DB
6. `UPDATE version SET version = 305` ([forgejo#7638](https://codeberg.org/forgejo/forgejo/issues/7638) schema trick)
7. Remove stale Bleve indexer files
8. `chown -R forgejo:forgejo` data/repos/custom
9. Start Forgejo — it runs forward DB migrations on boot (watch journal for errors)
10. `forgejo doctor check --all --fix --log-file …` on target
11. `forgejo admin regenerate hooks`

Duration: **30 min – 2 hours**.

### Step 12 — Smoke test BEFORE cutting DNS

Hit the target directly by IP / internal DNS first — don't cut
`gitea.example.com` → `forgejo.example.com` yet.

- [ ] `https://forgejo-internal/` loads and shows existing users/repos
- [ ] Log in as an admin; admin panel loads
- [ ] Log in as a 2FA user; TOTP still works (proves `SECRET_KEY` preserved)
- [ ] Open a repo; issues + PRs + comments render
- [ ] `git clone ssh://git@forgejo-internal/org/repo` works
- [ ] Fetch an LFS file in a cloned repo
- [ ] Fire a webhook test delivery (`Repo → Settings → Webhooks → Test`)
      and confirm signature verifies on the receiver side
- [ ] Run an Actions workflow that uses a secret; output is correct
- [ ] Pull a package from the OCI registry (if used)

If anything fails, now is the time to stop and dig in. The source is
still frozen-but-intact.

### Step 13 — Cut the DNS / reverse proxy

Point `gitea.example.com` (or whatever your canonical URL was) at the
target IP. Wait for DNS propagation (the `TTL` you used to set — hopefully
short for this change). Let's Encrypt / your TLS layer should already be
issuing for the target hostname (you set this up in step 3.7).

For a sharper cutover, run a temporary HTTP-level redirect on the old
source IP: 301 `https://gitea.example.com/*` → `https://forgejo.example.com/*`.
This catches anyone who cached DNS and gives them a clear signal.

### Step 14 — Post-migration: manual items

Until `supplement` and `verify` subcommands ship, work through the
[`docs/post-migration-checklist.md`](docs/post-migration-checklist.md).
The must-do items:

- **Re-register Actions runners.** Each runner's registration token is
  hostname-scoped. Generate new tokens via `forgejo admin actions
  generate-runner-token` and re-run the registration command on each
  runner host.
- **Announce PAT regeneration.** Users who had PATs must issue new ones;
  old ones cannot be decrypted.
- **Update external systems that integrate via webhooks or OAuth** if
  they pointed at the old hostname (or rely on any of the above rotated
  tokens).
- **Verify LFS storage transfer.** `SELECT SUM(size) FROM lfs_meta_object`
  on both sides should match.
- **Re-login everyone.** Sessions don't always survive the `COOKIE_REMEMBER_NAME`
  preservation; it's safer to tell users to log out and log back in.

### Step 15 — Celebrate, then decommission source (2 – 4 weeks later)

Leave the source running, offline-mode or stopped, for 2 – 4 weeks as an
in-case-we-missed-something fallback. During that time:

- Monitor target for silent failures (integrations complaining about
  missing webhooks, CI runners that never re-registered)
- Re-run your smoke test suite every few days
- Keep the `work_dir` dump tarballs — they are your only on-disk
  complete snapshot of the source state at cutover time

Once you're confident:

```sh
# On the source host:
sudo systemctl disable --now gitea
# After one final snapshot:
sudo apt purge gitea                 # or equivalent
# Archive (don't delete) /var/lib/gitea and its DB for compliance
```

## Quick reference

Minimum commands, assuming all prep is done:

```sh
# Sanity check — read-only, always safe
gitea2forgejo preflight --config config.yaml

# During downtime window
ssh root@gitea.example.com 'sudo systemctl stop gitea'
gitea2forgejo dump    --config config.yaml      # 30 min – 6 hr
gitea2forgejo restore --config config.yaml      # 30 min – 2 hr
# Smoke test, then cut DNS
```

### Artifacts and debugging

- Everything the tool produces lands in `work_dir`: dump tarball, native DB
  dump, S3 mirror, `source-manifest.json`, `preflight-report.md`,
  translated `target-app.ini`, and (after restore) the remote doctor log.
- Increase verbosity with `--log-level debug`.
- Each subcommand is mostly idempotent: rerunning `dump` re-harvests and
  overwrites; rerunning `restore` re-extracts and re-rsyncs. Useful when
  a prior run failed partway through.
- If target Forgejo fails to start after step 9 of restore, read its
  journal: `ssh root@forgejo.example.com journalctl -u forgejo -e`. The
  most common cause is a path in `app.ini` that wasn't rewritten; edit
  the translated `target-app.ini` locally, SFTP it up, and restart.

## Requirements

- Go 1.26+ (only if building from source or using `go install`)
- Source host: SSH access + admin token
- Target host: SSH access + admin token + empty Forgejo v15 install
- On the machine running the tool: `rsync`, `psql` or `mysql`, `mc` (MinIO
  client) if S3 storage is in use, `skopeo` if OCI packages are in use
