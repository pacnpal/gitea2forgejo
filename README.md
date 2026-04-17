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
  https://github.com/pacnpal/gitea2forgejo/releases/download/v0.1.0/gitea2forgejo-linux-amd64
chmod +x gitea2forgejo && sudo mv gitea2forgejo /usr/local/bin/
gitea2forgejo --version
```

### Step 2 — Install the OS-level helpers mig-host shells out to

```sh
# Debian / Ubuntu
sudo apt install rsync postgresql-client mysql-client zstd openssh-client

# Fedora / RHEL
sudo dnf install rsync postgresql mysql zstd openssh-clients

# macOS (Homebrew)
brew install rsync postgresql mysql-client zstd
```

Additionally:

- [mc (MinIO client)](https://min.io/docs/minio/linux/reference/minio-mc.html)
  if your source uses S3/MinIO storage
- [skopeo](https://github.com/containers/skopeo) if your source has OCI
  container packages in its registry

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

5. **DO NOT start Forgejo yet**, and DO NOT run its web-based initial
   setup wizard. The target must stay at "empty DB, binary installed,
   service stopped" until `gitea2forgejo restore` drops data into it.
   Running the setup wizard will populate `version` + create the first
   admin user, which breaks the import.

   If you accidentally ran it: `DROP DATABASE forgejo; CREATE DATABASE…`
   again.

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
