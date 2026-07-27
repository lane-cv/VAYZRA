# CI Compose Host-Port Access Design

## Problem

The `verify` GitHub Actions job starts PostgreSQL, Redis, and AIStor with
`deploy/compose.dev.yml`, then runs Go integration tests on the runner host.
The Compose network is intentionally marked `internal: true`, so Docker does
not publish the declared loopback ports to the host. Container health checks
pass, but the tests cannot connect to PostgreSQL at `127.0.0.1:54329`.

## Considered approaches

1. Remove `internal: true` from the development Compose network. This is the
   smallest edit, but it weakens the default development and deployment
   isolation and is therefore rejected.
2. Run all Go integration tests in another container on the internal network.
   This preserves isolation, but duplicates the existing Go setup, cache, and
   test execution flow and creates unnecessary CI complexity.
3. Add a CI-only Compose override that sets the network to non-internal. This
   keeps the hardened base configuration unchanged while allowing the
   GitHub-hosted runner to reach loopback-published service ports. This is the
   selected approach.

## Design

- Add `deploy/compose.ci.yml` containing only the CI network override.
- Load both `deploy/compose.dev.yml` and `deploy/compose.ci.yml` for the
  workflow's dependency startup and cleanup commands.
- Keep the existing base-only Compose validation and add merged CI
  configuration validation.
- After Compose reports healthy services, verify the PostgreSQL, Redis, and
  AIStor loopback ports are reachable before running Go tests.
- Keep the AIStor license secret handling and all service credentials
  unchanged.

## Safety properties

- `deploy/compose.dev.yml` retains `internal: true`.
- Only the ephemeral CI service network is made non-internal.
- Published ports remain bound to `127.0.0.1`, not all interfaces.
- Cleanup uses the same pair of Compose files and the same project name as
  startup.

## Verification

- A contract test must fail before the override exists and pass only when:
  the base network remains internal, the CI override is non-internal, and the
  workflow consistently loads the override for startup and cleanup.
- `docker compose config` must show `internal: false` for the merged CI
  configuration while the base configuration still shows `internal: true`.
- The complete `pnpm e2e-contracts` suite must pass.
- A new push to `master` must trigger GitHub Actions for remote verification.
