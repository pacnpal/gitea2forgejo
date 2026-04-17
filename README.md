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

## Build

```sh
go build -o gitea2forgejo ./cmd/gitea2forgejo
```

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

- Go 1.22+ (to build)
- Source host: SSH access + admin token
- Target host: SSH access + admin token + empty Forgejo v15 install
- On the machine running the tool: `rsync`, `psql` or `mysql`, `mc` (MinIO
  client) if S3 storage is in use, `skopeo` if OCI packages are in use

## Plan

See `/home/pac/.claude/plans/heavily-research-and-plan-cheerful-popcorn.md` for
the full design rationale and the research reports it was built from.
