# Phase 6 guarded disaster restore

This workflow restores one verified Phase 5 recovery point into new, detached
Docker volumes. It never rewrites production volume mappings and never reopens
traffic. Run it only from the canonical deployment checkout as root.

## Preconditions

- Caddy is serving the maintenance response and PostgreSQL operational mode is
  `release`.
- No release or rollback coordinator holds the production release lock.
- `recovery/latest.json` and `recovery/pre-release.json` are both verified,
  no more than 24 hours old, and identify different backup UUIDs.
- The selected UUID is one of those two current recovery points.
- The local Restic repository and production secret files retain their approved
  ownership and modes.
- The target project has never existed. Existing empty volumes are rejected too.

Prepare the host-only restore directories once:

```bash
sudo install -d -o root -g root -m 0700 \
  /srv/happylearn/releases/restore-control \
  /srv/happylearn/releases/restore-reports
```

Create `/srv/happylearn/releases/restore-control/teacher-credential` as a
root-owned mode `0400` file using the Phase 5 credential procedure. Never put
that credential in an environment variable, command argument, or shell history.

## Execute

Choose a new target suffix and inspect it before typing the confirmation:

```bash
TARGET_PROJECT="happylearn-phase5-restore-$(openssl rand -hex 6)"
BACKUP_ID='<verified-v4-uuid>'
printf 'target=%s backup=%s\n' "$TARGET_PROJECT" "$BACKUP_ID"
```

Then run:

```bash
sudo scripts/prod-restore.sh \
  --project-dir /srv/happylearn/current \
  --env-file /etc/happylearn/production.env \
  --mode server \
  --expected-host-address '<approved-public-address>' \
  --target-project "$TARGET_PROJECT" \
  --backup-id "$BACKUP_ID" \
  --destructive \
  --confirmation "$TARGET_PROJECT:$BACKUP_ID"
```

The Phase 5 verifier checks the repository before restoring data, uses only new
volumes and an isolated network, revokes restored sessions, and verifies schema,
authorization, CSRF isolation, object integrity, and HTTP isolation. Containers
and the temporary network are removed after success; the four verified volumes
remain detached.

## Evidence and handoff

Success creates owner-only files under `/srv/happylearn/releases`:

- `restore-reports/restore-<backup-id>.json` — Phase 5 verification report;
- `restore-reports/restore-<backup-id>-handoff.json` — exact detached volumes;
- `switch-proposal-<backup-id>.json` — hash-bound proposal with
  `switchAutomatic:false`.

Verify these files and preserve them with the incident record. Do not edit the
Compose production volume mapping, start containers on the detached volumes, or
remove the original production volumes as part of this command. A later switch
requires a separate reviewed change and a fresh maintenance-window confirmation.

On any failure, keep production in maintenance. Phase 5 removes incomplete
resources by exact owner labels. If cleanup itself reports failure, inspect only
resources carrying the selected backup ID and target-project labels; never use a
global Docker prune or a broad volume deletion command.

The quarterly `happylearn-restore-verify.timer` exercises the same detached,
cleanup-on-completion verifier against the newest unexpired local recovery
point. Its success is evidence that a restore can be constructed; it is not
approval to switch volumes. Inspect its journal and the owner-only verification
report after every run. A failed timer must not trigger an automatic production
restore, volume switch, DNS change, firewall change, reboot, or service install.
