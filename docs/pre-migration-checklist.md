# Pre-migration checklist

Run through this before `gitea2forgejo migrate`.

## On the source Gitea host

- [ ] Note the Gitea version (`gitea --version`); confirm it is ≥ 1.23
- [ ] Confirm `SECRET_KEY`, `INTERNAL_TOKEN`, and `[oauth2].JWT_SECRET` are
      present in `app.ini` — without these, 2FA/secrets/OAuth cannot survive
- [ ] Record DB backend (postgres/mysql/sqlite) and DSN
- [ ] If using S3/MinIO storage, record bucket + endpoint + credentials
- [ ] Announce migration window to users
- [ ] Take an out-of-band snapshot (VM snapshot, filesystem snapshot) as a
      rollback plane — `gitea2forgejo` itself does not provide rollback

## On the target Forgejo host

- [ ] Install Forgejo v15.x, same arch
- [ ] Create an empty DB with the same dialect as source
- [ ] Ensure `data_dir` filesystem has ≥ 2× source data_dir free space
- [ ] If Redis is in use on both sides, confirm target Redis DB number DIFFERS
      from source — shared `db=0` cross-consumes queues during cutover
- [ ] If using S3, create target bucket with appropriate IAM
- [ ] Do NOT run Forgejo initial setup wizard; the restore needs an empty DB
- [ ] Install `rsync`, `psql`/`mysql` client, `skopeo` (for OCI packages),
      `mc` (for S3 sync)

## On the machine running `gitea2forgejo`

- [ ] Go 1.22+ (`go build ./cmd/gitea2forgejo`)
- [ ] SSH access to both hosts (key-based, no passphrase prompts)
- [ ] Both admin tokens in environment
- [ ] `config.yaml` filled in (copied from `example.config.yaml`)
- [ ] Run `./gitea2forgejo preflight --config config.yaml` — must be GO
- [ ] Read `docs/what-breaks.md` to understand the data-loss envelope

## Staging dry-run (strongly recommended)

- [ ] Restore source DB dump into a disposable Postgres
- [ ] Boot a disposable Gitea pointed at that DB
- [ ] Boot a disposable Forgejo v15 on another VM
- [ ] Run full `migrate` against the disposable pair
- [ ] Verify `verify-report.md` shows zero `MISSING`
- [ ] Manually spot-check:
  - Log in as a 2FA user; TOTP still works
  - Fire a webhook; signature verifies on receiver
  - Run an Actions workflow with a secret; succeeds
  - Clone a repo with LFS content; LFS fetches
  - Pull a package from the OCI registry; succeeds

## Cutover window

- [ ] Queue drain + source freeze (first phase of `dump`)
- [ ] Full dump
- [ ] Restore on target
- [ ] `supplement` phase
- [ ] `verify` phase — must be clean
- [ ] DNS cutover / reverse-proxy swap
- [ ] Un-freeze
