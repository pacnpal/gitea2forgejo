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

Each release attaches static binaries for 6 platforms — `linux`, `darwin`,
and `windows` on both `amd64` and `arm64` — built and signed by the
[SLSA3 Go builder](https://github.com/slsa-framework/slsa-github-generator)
along with one `.intoto.jsonl` provenance attestation per binary.

```sh
VERSION=v0.1.0
# Pick one. Windows binaries have a .exe suffix.
PLATFORM=linux-amd64        # linux-amd64 | linux-arm64 | darwin-amd64 |
                            # darwin-arm64 | windows-amd64.exe | windows-arm64.exe

curl -L -o gitea2forgejo \
  https://github.com/pacnpal/gitea2forgejo/releases/download/$VERSION/gitea2forgejo-$PLATFORM
chmod +x gitea2forgejo
sudo mv gitea2forgejo /usr/local/bin/
gitea2forgejo --version
```

**Platform notes:**

- **Linux**: primary target. All external commands (`rsync`, `pg_dump`, `tar`,
  `mc`, `skopeo`) are in distro package repos.
- **macOS**: works fully; install `rsync`, `postgresql` (for `pg_dump`),
  `zstd`, `mc` and `skopeo` via Homebrew.
- **Windows**: native binaries build and the API-only flows (`preflight`,
  manifest harvest, API supplement) work, but the dump/restore stages shell
  out to rsync/pg_dump/tar-with-zstd. Use from WSL2 or Git Bash with MSYS2
  packages installed; native PowerShell is not supported.

Verify the provenance before running (optional but recommended):

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

| Command      | Purpose                                                        |
|--------------|----------------------------------------------------------------|
| `preflight`  | Read-only checks: versions, SSH, DB, disk, `SECRET_KEY`.       |
| `dump`       | `gitea dump` + native DB dump + S3 mirror + source manifest.   |
| `restore`    | File copy, DB import, schema trick, `forgejo doctor`.          |
| `supplement` | API fixes: hostname rewrites, runner tokens, Actions CSVs.     |
| `verify`     | Re-harvest target manifest, diff against source, emit report.  |
| `migrate`    | Run all five in order, with `--resume-from=<phase>`.           |

## Usage

```sh
cp example.config.yaml config.yaml
$EDITOR config.yaml

# Read-only check first.
./gitea2forgejo preflight --config config.yaml

# End-to-end migration (staging first!).
./gitea2forgejo migrate --config config.yaml
```

## Requirements

- Go 1.26+ (only if building from source or using `go install`)
- Source host: SSH access + admin token
- Target host: SSH access + admin token + empty Forgejo v15 install
- On the machine running the tool: `rsync`, `psql` or `mysql`, `mc` (MinIO
  client) if S3 storage is in use, `skopeo` if OCI packages are in use
