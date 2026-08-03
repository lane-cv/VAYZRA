# Phase 6 release and rollback runbook

Use this runbook when `prod-release.sh` or `prod-rollback.sh` reports a trace ID.
Run production commands as root from the canonical, clean deployment checkout.

## Render host services without installing them

The repository renderer writes only to an operator-selected owner-only staging
directory. Use the dedicated deployment account, not an interactive personal
account:

```bash
install -d -m 0700 /srv/happylearn/systemd-staging
scripts/render-systemd.sh \
  --project-dir /srv/happylearn/current \
  --deployment-user happylearn \
  --output-dir /srv/happylearn/systemd-staging
systemd-analyze verify /srv/happylearn/systemd-staging/*.service \
  /srv/happylearn/systemd-staging/*.timer
sha256sum -c /path/to/operator-reviewed-systemd.sha256
```

Before installation, separately confirm that the deployment account can reach
Docker, `/usr/local/libexec/happylearn/host-sampler` is the reviewed Phase 4
binary, `deploy/production.env` is mode `0600`, and every external state,
backup, and secret path has the ownership required by the host scripts.

Copying reviewed files into the system unit directory, running `daemon-reload`,
and enabling or starting services are real-host mutations and require explicit
operator approval. The renderer never performs them. After approval, install
only the listed rendered filenames, compare their SHA-256 hashes again, then
enable `happylearn-compose.service` and the five timers. Do not enable the
oneshot services directly.

## Failed-safe invariants

- Keep Caddy on `Caddyfile.maintenance`; do not reopen traffic manually.
- Do not run a down migration or restore the pre-release database as an image rollback.
- Do not delete the failed images, `failed-manifest.json`, release state, backup evidence, or rollback diagnostics.
- Do not edit `release-state.json` or any manifest by hand.
- Treat `rollback.env` as owner-only release evidence even though it must not contain secret values.

## Inspect safe evidence

Set the deployment-specific absolute state directory, then inspect only the bounded fields:

```bash
jq '{releaseId,traceId,state,result,originalFailureState,originalFailureStatus,rollbackFailureCategory,updatedAt}' \
  /srv/happylearn/releases/release-state.json
jq . /srv/happylearn/releases/rollback-diagnostics.json
```

Confirm that the trace ID matches the command output. Preserve copies of the state,
active/previous/failed manifests, diagnostics, and the referenced pre-release recovery
evidence before taking another action.

## Resume an eligible rollback

Rollback is eligible only after migration started and before public traffic reopened.
Use the same canonical project and environment file from the failed release:

```bash
scripts/prod-rollback.sh \
  --project-dir /srv/happylearn/current \
  --env-file /etc/happylearn/production.env \
  --mode server \
  --expected-host-address '<approved-public-address>'
```

The command revalidates the previous manifest hash and live schema compatibility. It
keeps maintenance active until the previous app and worker pass readiness and the same
acceptance suite. A second `failed_safe` result requires investigation; do not bypass
the failed check.

## Escalation boundaries

If the previous manifest is absent, hash-invalid, schema-incompatible, or unhealthy,
leave the system in maintenance and preserve the current database. A database restore
is a separate destructive workflow that must target new volumes and must never be used
as an automatic rollback step.

DNS changes, firewall changes, package installation, public TLS provisioning,
systemd service installation, host reboot, and publishing either `v1.0.0-rc.1`
or `v1.0.0` are outside automatic repository execution. Record each as a
separately approved operator action.
