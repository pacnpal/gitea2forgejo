# gitea2forgejo

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

### Pre-built release binary (recommended)

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
VERSION=v0.1.0
PLATFORM=linux-amd64        # see table above

curl -L -o gitea2forgejo \
  https://github.com/pacnpal/gitea2forgejo/releases/download/$VERSION/gitea2forgejo-$PLATFORM
chmod +x gitea2forgejo
sudo mv gitea2forgejo /usr/local/bin/
gitea2forgejo --version
```

#### macOS: running the unsigned binary

The release binaries are **not** Apple Developer-ID signed or notarized —
Gatekeeper will refuse to run them by default. Two mitigation options:

**Option A: strip the quarantine attribute (simplest).**

```sh
curl -L -o gitea2forgejo \
  https://github.com/pacnpal/gitea2forgejo/releases/download/v0.1.0/gitea2forgejo-darwin-arm64
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

# Fetch binary + its provenance, then verify.
VERSION=v0.1.0
PLATFORM=linux-amd64
curl -L -o gitea2forgejo-$PLATFORM \
  https://github.com/pacnpal/gitea2forgejo/releases/download/$VERSION/gitea2forgejo-$PLATFORM
curl -L -o gitea2forgejo-$PLATFORM.intoto.jsonl \
  https://github.com/pacnpal/gitea2forgejo/releases/download/$VERSION/gitea2forgejo-$PLATFORM.intoto.jsonl

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

## Subcommands

| Command      | Status      | Purpose                                                        |
|--------------|-------------|----------------------------------------------------------------|
| `preflight`  | ✅ shipped  | Read-only checks: versions, SSH, DB, disk, `SECRET_KEY`.       |
| `dump`       | ✅ shipped  | `gitea dump` + native DB dump + S3 mirror + source manifest.   |
| `restore`    | ✅ shipped  | File copy, DB import, schema trick, `forgejo doctor`.          |
| `supplement` | 🚧 planned  | API fixes: hostname rewrites, runner tokens, Actions CSVs.     |
| `verify`     | 🚧 planned  | Re-harvest target manifest, diff against source, emit report.  |
| `migrate`    | 🚧 planned  | Run all five in order, with `--resume-from=<phase>`.           |

Until `migrate` lands, run `preflight` → `dump` → `restore` by hand in that
order (see [Usage](#usage) below).

## Usage

### 1. Install dependencies on the machine running the migration

The tool shells out to standard utilities; install them first:

```sh
# Debian / Ubuntu
sudo apt install rsync postgresql-client mysql-client zstd openssh-client

# Fedora / RHEL
sudo dnf install rsync postgresql mysql zstd openssh-clients

# macOS (Homebrew)
brew install rsync postgresql mysql-client zstd
```

If your source uses S3/MinIO storage, also install
[mc](https://min.io/docs/minio/linux/reference/minio-mc.html). If you use
OCI container packages, also install [skopeo](https://github.com/containers/skopeo).

### 2. Create an admin access token on each instance

On the source Gitea AND the target Forgejo:
**User menu → Settings → Applications → Generate New Token**, tick **all**
scopes (this is a one-time admin migration), then save each token. Export
both, along with your DB DSNs, as environment variables:

```sh
export GITEA_ADMIN_TOKEN=gta_...
export FORGEJO_ADMIN_TOKEN=fjo_...
export GITEA_DB_DSN='postgres://gitea:secret@gitea-db.example.com:5432/gitea?sslmode=disable'
export FORGEJO_DB_DSN='postgres://forgejo:secret@forgejo-db.example.com:5432/forgejo?sslmode=disable'
```

### 3. Write `config.yaml`

Copy the template and edit it:

```sh
cp example.config.yaml config.yaml
$EDITOR config.yaml
```

Minimum required fields: `source.url`, `source.admin_token`,
`source.config_file`, `source.data_dir`, `source.db.dialect`, `source.db.dsn`,
and the same five for `target`, plus a `work_dir`. Every value prefixed with
`env:FOO` is resolved from `$FOO` at runtime, so you don't have to put
secrets in the YAML.

SSH is required for `dump` and `restore` (but not `preflight`). Add the host
to `~/.ssh/known_hosts` once before running:

```sh
ssh-keyscan -H gitea.example.com >> ~/.ssh/known_hosts
ssh-keyscan -H forgejo.example.com >> ~/.ssh/known_hosts
```

If you prefer not to touch `known_hosts`, pin the fingerprint directly in
the config under `source.ssh.host_key_fingerprint` / same for `target`.

### 4. Run preflight (always safe — read-only)

```sh
gitea2forgejo preflight --config config.yaml
```

Emits `$work_dir/preflight-report.md` with a GO / NO-GO decision. It checks:

- Source + target API reachable and returning an expected version string
- SSH is reachable on both hosts
- DB connectivity on both DSNs
- Target `work_dir` has at least 2× the source `data_dir` size of free space
- `[security].SECRET_KEY`, `[security].INTERNAL_TOKEN`, and
  `[oauth2].JWT_SECRET` are all present in the source `app.ini`
- Source Redis DB number differs from target's (if both use Redis)

**Do not skip this step.** If `SECRET_KEY` is empty on source, 2FA / OAuth
apps / encrypted secrets all turn into unrecoverable garbage after the
migration and this is the last chance to notice.

### 5. Freeze source and run `dump`

```sh
# Put source Gitea in offline/read-only mode, or stop the service outright.
ssh gitea.example.com 'sudo systemctl stop gitea'

gitea2forgejo dump --config config.yaml
```

`dump` runs 5 stages and writes everything into `work_dir`:

| Stage         | Output                                          |
|---------------|-------------------------------------------------|
| API harvest   | `source-manifest.json` (full entity inventory)  |
| login_source  | merged into the same manifest                   |
| `gitea dump`  | `gitea-dump.tar.zst` (app.ini + data/repos/custom + xorm SQL) |
| native DB     | `gitea.dump` (pg_dump -Fc) or `gitea.sql` (mysqldump) or `gitea.sqlite` |
| S3 mirror     | `s3/` with attachments, lfs, packages, avatars  |

Individual stages can be skipped via `options.skip_gitea_dump`,
`options.skip_native_db`, `options.skip_s3_mirror` in the config — useful
when rehearsing against staging.

A full production-size dump typically runs **30 min to 6 hours** depending
on repo + LFS volume. The biggest factor is the `gitea dump` tarball
transfer over SSH; run the tool on a host with fast network to the source.

### 6. Install Forgejo v15 on the target host

Fresh install, empty DB. **Do not run the initial setup wizard** — the
DB must be empty for `restore` to import into. Leave the Forgejo service
stopped.

### 7. Run `restore`

```sh
gitea2forgejo restore --config config.yaml
```

11 steps (all logged):

1. SSH to target, `systemctl stop forgejo`
2. Extract `gitea-dump.tar.zst` into `work_dir/extracted/`
3. `rsync` `data/`, `repos/`, `custom/` to the target host
4. Translate source `app.ini` → target `app.ini` (preserve `SECRET_KEY`,
   rewrite hostname and data paths, set `COOKIE_REMEMBER_NAME`,
   set `[actions].DEFAULT_ACTIONS_URL`)
5. `pg_restore` (or `mysql < dump.sql`, or copy sqlite file) into target DB
6. `UPDATE version SET version = 305` ([forgejo#7638](https://codeberg.org/forgejo/forgejo/issues/7638) schema trick)
7. Remove stale Bleve indexer files
8. `chown -R forgejo:forgejo` data/repos/custom
9. Start Forgejo — runs forward DB migrations on boot (watch for errors)
10. `forgejo doctor check --all --fix --log-file …` on the target
11. `forgejo admin regenerate hooks`

### 8. Post-migration (manual, for now)

Until `supplement` and `verify` ship, work through
[`docs/post-migration-checklist.md`](docs/post-migration-checklist.md).
The items that always need operator action:

- Re-register Actions runners (their tokens are hostname-scoped)
- Announce "please re-login + re-enroll 2FA if lost + regenerate PATs" to users
- Update DNS / reverse-proxy / external CI that points at the old hostname
- Spot-check a 2FA login, a webhook firing, a workflow using a secret,
  an LFS clone, and a package pull

### End-to-end worked example

```sh
# one-time prep
ssh-keyscan -H gitea.example.com forgejo.example.com >> ~/.ssh/known_hosts
export GITEA_ADMIN_TOKEN=...  FORGEJO_ADMIN_TOKEN=...
export GITEA_DB_DSN=...       FORGEJO_DB_DSN=...
cp example.config.yaml config.yaml && $EDITOR config.yaml

# 5 min
gitea2forgejo preflight --config config.yaml
# Inspect work_dir/preflight-report.md — must be GO

# cutover window begins
ssh gitea.example.com 'sudo systemctl stop gitea'

# 30 min – 6 hr depending on repo/LFS volume
gitea2forgejo dump --config config.yaml

# 30 min – 2 hr
gitea2forgejo restore --config config.yaml

# Forgejo now live at target URL — smoke-test, then cut DNS
```

### Logs, artifacts, debugging

- Everything the tool produces lands in `work_dir`: dump tarball, native DB
  dump, S3 mirror, `source-manifest.json`, `preflight-report.md`,
  translated `target-app.ini`, and (after restore) the remote doctor log.
- Increase verbosity with `--log-level debug`.
- Each subcommand is idempotent for the stages that precede it: rerunning
  `dump` will re-harvest the API and re-fetch the tarball (overwriting the
  previous one); rerunning `restore` will re-extract and re-rsync. This is
  useful when the prior run failed partway through.

### Rehearsing against staging

The migration is *not* rollback-safe once `restore` starts writing. Always
rehearse first against disposable copies:

1. Restore the source's DB dump into a disposable Postgres.
2. Boot a disposable Gitea pointed at that DB on a VM.
3. Boot a fresh Forgejo v15 on a second VM.
4. Run the full `preflight` → `dump` → `restore` flow against the disposable pair.
5. Exercise the golden paths (2FA login, webhook fire, Actions run with a
   secret, LFS clone, package pull).
6. Only then repeat against production.

## Requirements

- Go 1.26+ (only if building from source or using `go install`)
- Source host: SSH access + admin token
- Target host: SSH access + admin token + empty Forgejo v15 install
- On the machine running the tool: `rsync`, `psql` or `mysql`, `mc` (MinIO
  client) if S3 storage is in use, `skopeo` if OCI packages are in use
