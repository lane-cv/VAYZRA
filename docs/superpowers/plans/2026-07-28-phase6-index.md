# HappyLearn Phase 6 Execution Index

Execute these plans in order after Phase 5 acceptance:

1. `docs/superpowers/plans/2026-07-28-phase6-production-stack.md`
   - production Compose and Caddy, secret-file configuration, immutable images,
     private networks, resource budgets, production readiness metadata and
     deployment contracts.
2. `docs/superpowers/plans/2026-07-28-phase6-release-rollback.md`
   - release manifest, preflight, maintenance-window deployment, migration,
     health/smoke verification, previous-image rollback, destructive restore
     safeguards and systemd templates.
3. `docs/superpowers/plans/2026-07-28-phase6-local-production-acceptance.md`
   - disposable local production deployment, full Phase 1–5 desktop/mobile
     acceptance, backup/restore, release failure injection, rollback, restart,
     security, resource capture, CI and repository-ready review.

The three plans implement
`docs/superpowers/specs/2026-07-28-phase6-production-release-acceptance-design.md`.

## Delivery boundary

The plans close the Phase 6 repository-ready gate. They do not log in to a real
server, change DNS or firewall state, request public certificates, create the
final `v1.0.0` tag, or claim final Phase 6 completion. Those actions require the
user's later server authorization and the real-server acceptance gate.

## Repository-ready gate

1. Phase 5 remains green.
2. Production configuration and scripts pass their contract and mutation tests.
3. The disposable production stack passes full browser, recovery, rollback,
   restart, security, resource, and cleanup checks.
4. Complete-diff review has no open Critical or Important finding.
5. The review record states: “Phase 6 repository production-ready; real-server
   acceptance pending.”
6. A `v1.0.0-rc.1` candidate may be prepared only after separate user
   authorization.
