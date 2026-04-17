# Post-migration checklist

Run after `gitea2forgejo verify` reports clean.

## Immediate (within 1 hour of cutover)

- [ ] Log in as an admin user; admin panel loads
- [ ] Site settings in `/-/admin/config` match expectations
- [ ] Login sources list (LDAP/OAuth/SMTP) shows all source entries
- [ ] `forgejo doctor check --all` reports no errors
- [ ] Open 3 representative repos; git clone over SSH succeeds
- [ ] Open 3 repos with issues/PRs; comments and reactions render
- [ ] Fire a webhook test delivery; receiver reports signature valid

## Runners (always breaks, always manual)

- [ ] Consume `{work_dir}/runner-reregister.sh` playbook
- [ ] For each runner host: stop runner, replace registration token, start
- [ ] Confirm each runner appears in `/-/admin/actions/runners` as online

## Users (announce to users)

- [ ] Send "please re-login and recheck 2FA" email
- [ ] Send "please regenerate your Personal Access Tokens" email using
      `{work_dir}/pat-regeneration.csv` (list of users with active PATs)
- [ ] Send "update remotes to new hostname" email if hostname changed

## Actions secrets (only if `SECRET_KEY` was NOT preserved)

- [ ] Open `{work_dir}/actions-secrets-to-reenter.csv`
- [ ] For each (scope, name) pair, PUT the secret value via API
- [ ] Re-run a representative workflow per repo to confirm

## OAuth2 applications

- [ ] Open `/-/admin/applications/oauth2`; verify all source apps listed
- [ ] For each app where the callback URL included the old hostname, confirm
      `supplement` rewrote it (or edit manually)
- [ ] Announce to downstream consumers that OAuth callback URLs may have
      changed

## Packages

- [ ] For each registry type used (OCI, npm, maven, generic):
      pull one package from target; succeeds
- [ ] Spot-check package download counts match source (may lag by a few)

## Mirrors

- [ ] For each push-mirror: confirm next push succeeds
- [ ] For each pull-mirror: confirm next pull succeeds

## External integrations

- [ ] Update DNS or reverse-proxy to send traffic to new host
- [ ] Update any external CI pointing at old hostname
- [ ] Update any documentation referencing old clone URLs

## Decommission source (after a grace period, typically 2–4 weeks)

- [ ] Confirm no external traffic hitting old hostname (access logs)
- [ ] Snapshot old Gitea once more as archive
- [ ] Stop old Gitea service
- [ ] Remove old DB and data directory
- [ ] Remove `work_dir` tarballs (or archive cold-storage)
