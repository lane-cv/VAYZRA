# HappyLearn Phase 5 Execution Index

Execute these plans in order:

1. `docs/superpowers/plans/2026-07-28-phase5-operations-foundation.md`
   - migration 19, typed settings, operational leases, maintenance write gate,
     safe audit reads, system settings and audit UI.
2. `docs/superpowers/plans/2026-07-28-phase5-backup-restore.md`
   - migration 20, backup runs and artifacts, encrypted local recovery points,
     optional S3 replication, retention, disposable restore verification, backup
     APIs and UI.
3. `docs/superpowers/plans/2026-07-28-phase5-monitoring-alerts-dashboard.md`
   - migration 21, health samples, host ingestion, private Prometheus metrics,
     alert evaluation, safe webhook delivery, structured safe logs, bounded
     metadata retention, teacher dashboard and alert UI.
4. `docs/superpowers/plans/2026-07-28-phase5-acceptance-hardening.md`
   - Phase 5 disposable E2E, real backup/restore and failure matrix, resource
     capture, CI, operations runbook, security review, and final completion gate.

The four plans implement
`docs/superpowers/specs/2026-07-28-phase5-operations-backup-monitoring-design.md`.
Do not start Phase 6 implementation until the final Phase 5 gate passes and the
repository is clean.

## Fixed cross-plan interfaces

- Go domains: `internal/operations`, `internal/backup`
- Admin API root: `/api/v1/admin/operations`
- Internal listener: `HAPPYLEARN_INTERNAL_LISTEN`, default `:9090`
- Internal metrics route: `/internal/metrics`
- Host sample route: `/internal/host-samples`
- Admin pages: `/admin`, `/admin/settings`, `/admin/audit`,
  `/admin/backups`, `/admin/alerts`
- Business timezone: `Asia/Shanghai`
- Backup states: `queued | draining | snapshotting | encrypting | verifying |
  syncing | succeeded | degraded | failed`
- Operational modes: `normal | draining | backup | release`
- Alert states: `open | acknowledged | resolved`

## Phase 5 completion gate

1. Execute every task with fresh RED/GREEN evidence.
2. Run the complete backend, frontend, contract, security, and browser matrix.
3. Create a licensed PostgreSQL and AIStor recovery point and restore it into an
   empty disposable environment.
4. Prove optional S3 success and degraded behavior.
5. Prove RPO no greater than 24 hours, RTO no greater than 4 hours, and the
   2 CPU/4 GB resource ceiling.
6. Review the complete Phase 5 diff for specification, security, privacy,
   recovery, operations, and test adequacy.
7. Fix every Critical and Important finding and rerun the complete gate.
8. Commit the final review record and keep the repository clean.
