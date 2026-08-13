# Local Stack All-Interface Bindings Design

## Goal

Allow another device on the same trusted network to reach every host-published
port in the local/test Docker Compose stack while preserving the production
network boundary and the application's strict Origin/CSRF checks.

This change applies to the local/test deployment path only. Production keeps
exposing only Caddy on ports 80 and 443; PostgreSQL, Redis, AIStor, the internal
application listener, and the update agent remain private in production.

## Required behavior

1. The local/test host mappings for Web/API, the internal application listener,
   PostgreSQL, Redis, AIStor S3, and the AIStor console bind to `0.0.0.0`.
2. `scripts/deploy-from-github.sh` requires an explicit `--public-origin URL`.
   The value must be the exact browser origin, including scheme and non-default
   port when applicable, for example `http://192.168.1.20:8080`.
3. The deployment script writes that value to
   `HAPPYLEARN_PUBLIC_ORIGIN`. It must not derive an origin from `0.0.0.0`, a
   hostname guess, or an automatically selected network interface.
4. Existing port override flags continue to work. The caller is responsible for
   making `--public-origin` use the selected App port.
5. The extra `happylearn-public` network is not part of the host-port feature and
   is removed. The GitHub deployment override continues to attach the App to the
   existing non-internal `host_access` network for outbound access.

## Security boundary

The local/test stack contains development credentials and plain HTTP. Binding
to all interfaces is an explicit operator choice for a trusted LAN or a
firewall-restricted test host, not a production configuration. Documentation
must warn operators to restrict inbound traffic at the host firewall and never
expose these ports directly to the public internet.

The published internal, database, Redis, and AIStor ports are intentionally
reachable from permitted remote hosts after this change. No authentication or
TLS hardening is added in this scope. Production remains the supported path for
internet-facing deployment.

## Components

- `deploy/compose.dev.yml` defines all six local/test host mappings with
  `0.0.0.0` and keeps the App on its existing private application network.
- `deploy/compose.github.yml` preserves all-interface mappings when the GitHub
  deployment override is used and keeps the update agent HTTP listener
  unpublished.
- `scripts/deploy-from-github.sh` parses, validates, persists, and displays the
  explicit public origin.
- `README.md` documents the required argument, LAN access URL, exposed ports,
  firewall warning, and production exclusion. It identifies `v0.1.3` as the
  current source Release once the final verified commit is tagged.
- Compose and deployment contracts assert the intended bind addresses, public
  origin wiring, and continued absence of update-agent host publication.

## Validation and failure behavior

The implementation follows test-driven development:

1. Add contract assertions for six `0.0.0.0` host bindings and explicit public
   origin handling, then observe them fail against the current implementation.
2. Add negative mutations for a loopback regression, a missing public origin,
   and an exposed update-agent port.
3. Implement the minimum Compose, script, and documentation changes required to
   make the contracts pass.
4. Run Compose rendering for default and custom ports, deployment/OTA contracts,
   the full `pnpm e2e-contracts` suite, and repository whitespace checks.
5. Push the final `master` commit and require its complete GitHub Verify workflow
   to succeed before creating the annotated `v0.1.3` tag.

The deployment script fails before writing configuration or starting services
when `--public-origin` is missing or malformed. Application startup remains the
authoritative validation for the complete origin syntax.

## Release

After the exact final `master` commit passes Verify, create one annotated
`v0.1.3` tag pointing directly to that commit. The existing release workflow
must publish an immutable, non-draft, non-prerelease Release containing exactly
`VAYZRA-v0.1.3.tar.gz` and `SHA256SUMS`. Downloaded assets, SHA-256, archive root,
tag object, and peeled commit are verified after publication.
