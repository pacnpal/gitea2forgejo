# What `gitea2forgejo` handles and what it cannot

Authoritative reference. Keep in sync with `internal/supplement/` behavior.

## Handled automatically (~98% fidelity if `SECRET_KEY` is preserved)

| Category           | Notes                                                        |
|--------------------|--------------------------------------------------------------|
| Users              | Username, email, password hash, admin flag, created_at       |
| 2FA TOTP/WebAuthn  | Encrypted with `SECRET_KEY`; survives only if key preserved  |
| SSH / GPG keys     | Via DB migration                                             |
| OAuth2 apps        | Client ID + secret; secret encrypted with `SECRET_KEY`       |
| Emails             | Primary + alternates                                         |
| Orgs + teams       | Memberships, permissions                                     |
| Repos              | Git refs, tags, wiki repo, LFS objects, attachments          |
| Issues + PRs       | Title, body, state, comments, reactions, assignees, timeline |
| Reviews            | Inline comments AND post-review comments (full fidelity)     |
| Labels, milestones | Per repo and per org                                         |
| Branch protections | Rules, approvals, required status checks                     |
| Collaborators      | Per repo, with permission level                              |
| Deploy keys        | Via DB migration                                             |
| Webhooks           | URL, content type, events, secret (encrypted)                |
| Topics             | Per repo                                                     |
| Packages           | OCI/container, npm, maven, generic, etc.                     |
| Actions workflows  | `.gitea/workflows/*` carried with the repo                   |
| Actions secrets    | Values encrypted with `SECRET_KEY`; preserve key or lose them|
| Actions variables  | Plain-text; survive regardless                               |
| Login sources      | LDAP, SMTP, OAuth2 login providers                           |
| Avatars            | Users, repos, orgs                                           |
| Stars, watches     | Subscription state preserved                                 |
| Mirrors            | Pull/push mirror config + credentials (encrypted)            |
| Cron job history   | Admin cron run history                                       |

## Cannot be handled — manual operator action required

| Item                          | Why                                                 | Operator action                                   |
|-------------------------------|-----------------------------------------------------|---------------------------------------------------|
| Actions runners               | Registration tied to source hostname                | Re-register using emitted shell playbook          |
| Personal Access Tokens        | Hashed; not decryptable                             | Users must re-issue; email list emitted           |
| Live web sessions             | Cookie domain change; safer to force re-login       | Announce cutover; users re-login                  |
| In-flight webhook deliveries  | Lost at cutover                                     | Replay via Forgejo UI if critical                 |
| Redis queue contents          | Drained at Phase 1                                  | None; this is intentional                         |
| In-flight Actions runs        | Cancelled; workflows preserved                      | Re-run manually if needed                         |
| External CI / webhook peers   | Point at old hostname                               | Update their config; `supplement` rewrites local  |
| Gitea-1.23-only DB columns    | Dropped during forward migration                    | Expected; Forgejo forward-migrates                |
| Bleve indexer files           | Not portable across major versions                  | Wiped + regenerated on first boot                 |
| External OIDC/OAuth identity links | May need re-consent if issuer hostname changes | Announce cutover to users                          |

## If `SECRET_KEY` is NOT preserved

The following additionally become unrecoverable and must be re-entered:

- All 2FA secrets (users must re-enroll)
- Push/pull mirror credentials (write-only; operator re-enters)
- Actions secret values (script emits CSV of names)
- OAuth2 application client secrets (must regenerate)

`preflight` hard-fails if `[security].SECRET_KEY` or `[oauth2].JWT_SECRET` are
empty in the source `app.ini`.
