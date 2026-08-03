# Phase 6 Final Review

Review date: 2026-08-03 UTC

## Scope and commits

The review baseline is Phase 5 completion commit `e5f3ff0` (`fix(e2e): preserve age identity ownership`). Phase 6 is currently an intentional uncommitted working-tree slice; this review does not create commits, tags, releases, or external changes.

The slice covers the production Compose/Caddy topology, immutable release manifests, locked migrations, internal acceptance, host preflight, durable release and rollback, guarded restore, systemd rendering, operator runbooks, disposable local production acceptance, security/resource runners, failure injection, contracts, mutations, and CI integration.

## Specification coverage

- Immutable eight-image manifests, schema bounds, canonical parsing, build identity, migration locking, and internal smoke acceptance are implemented and contract-tested.
- The release state machine persists every approved transition, supports bounded resume, performs a verified pre-release backup, uses static 503 maintenance, and never performs an automatic database restore.
- Automatic rollback validates the previous manifest with the previous application image, checks forward-schema compatibility, captures bounded diagnostics, restores the image environment, and reopens traffic only after readiness and smoke checks.
- Destructive recovery accepts only an exact disposable target and confirmation, restores into new detached volumes, revokes sessions, verifies authorization/CSRF/object integrity, and writes a non-automatic switch proposal.
- The local harness covers desktop/mobile Phase 1–5 regression, release, rollback, restart, recovery, security, resources, signal interruption, and exact cleanup.

## Correctness and failure handling

The 15-case release matrix now has a production adapter rather than a self-authored fake-evidence adapter. It invokes the real local production scripts and Compose services, observes PostgreSQL operational mode and schema, probes Caddy traffic, verifies active manifests and recovery evidence, and covers all 16 durable release transitions with signal interruption. Local-only release and rollback injectors are absent by default and rejected in server mode.

Interruption after manifest activation or traffic reopening can resume only when the persisted manifest hash, state, and result agree. The persisted previous-manifest hash is retained across that resume. Migration-stage injections enter the rollback-allowed interval before failing. Early failures remain pre-maintenance or fail safe under maintenance as specified.

## Security and privacy

Production services use immutable image digests, explicit non-root identities, read-only roots where applicable, dropped capabilities, `no-new-privileges`, bounded logs/resources, separate private and edge networks, and file-backed per-UID secrets. Only Caddy publishes ports. Server-mode scripts reject local/test controls from both production environment files and inherited process variables.

Diagnostics contain only allowlisted lifecycle fields. Raw logs, environment expansion, object keys, row bodies, credentials, browser traces, and backup contents are not uploadable. The sanitizer fails closed and deletes the artifact set on unsafe content.

## Backup, restore, release, and rollback evidence

Earlier disposable runs in this workspace proved the restart group and the successful A-to-B release path, including a real pre-release PostgreSQL/AIStor/Restic backup and smoke acceptance. A rollback run exposed and led to correction of the missing `/release-input` mount on the validation service path.

The current recovery runner uses four distinct real recovery points. It proves wrong-key rejection, tampered Restic pack rejection, deletion of one database-referenced object from the restored AIStor copy followed by a real `restore-check` verification failure, and finally a clean positive restore. Failed negative cases cannot publish a handoff or switch proposal.

The corrected rollback path, real missing-object case, and production failure-matrix adapter require fresh Docker-backed execution before a final PASS may be recorded; no result is inferred from their static contracts.

## Production topology and resources

The configured steady peak is 1.85 CPU and 3 GiB; the worker-drained backup peak is 1.10 CPU and 1.75 GiB. AIStor runs with the shared data group needed by the read-only backup mount, with a one-shot narrowly-capable volume initializer.

The live resource runner schedules exactly 180 sample epochs over 1,800 seconds. It records numeric per-service and aggregate CPU and working-set memory, restart/health state, timestamps, and cumulative request-latency buckets, and enforces the live 2 CPU/4 GiB ceiling. In the same window the harness runs the complete Phase 1–5 browser workload, submits a signed sample through the real private host-samples endpoint, and invokes one real backup. The runner fails unless it observes both steady state and a backup container while the worker is absent. A fresh 30-minute live capture remains pending on a Docker-capable runner.

## Browser, mobile, restart, and cleanup evidence

Earlier local production runs passed Phase 1–5 browser/mobile regression, restart, and successful release groups. Every run uses a unique Compose project, loopback ports, local registry, generated secrets, bounded commands, signal-aware cleanup, and a zero-resource proof. The latest recovery and failure-matrix changes require a fresh `all` run; old artifacts are not treated as evidence for changed code.

## CI and operator documentation

The Phase 6 workflow has a repository-only contract/mutation job, pinned Caddy runtime-parser validation, a protected licensed production job, and a separately protected scheduled/manual 30-minute resource job. The production job runs the complete `all` group plus the live security group. Actions and tool versions are pinned, required failures cannot continue, and uploads are confined to sanitized `containers.log` files below `test-results/phase6`.

Local acceptance, real-server acceptance, release/rollback, and disaster-restore runbooks preserve the approval boundary. Real-server installation, DNS, firewall, public TLS, reboot, restore switching, and release approval are separate actions.

## Findings

Resolved Important findings:

1. The release matrix driver had no real adapter and was never called by the local harness.
2. The rollback validation command used a service without the read-only candidate-manifest mount.
3. AIStor and backup container group ownership prevented real object snapshots.
4. Backup evidence expected a nonexistent fourth artifact kind.
5. Explicit PostgreSQL `sslmode=disable` and owner-only `0600` secrets were rejected.
6. Recovery did not execute real wrong-key, repository-tamper, or missing-object cases.
7. Resource sampling counted output rows rather than scheduled sample epochs.
8. Traffic-open interruption could not resume after active-manifest promotion.
9. Repository-only Caddy and preflight contracts depended on Docker daemon/cache state.
10. Recovery-only acceptance could run an A-based supervisor after releasing B.
11. Resource acceptance sampled idle containers without representative browser/host traffic, aggregate or latency evidence, or a proven worker-drained live backup window.

No unresolved Critical or Important implementation finding is known from the static complete-diff review. Dynamic verification items below remain evidence blockers, not waived findings.

## Fixes and re-verification

Fresh passing checks on 2026-08-03 UTC:

- `make phase6-contracts`
- `make phase6-shellcheck`
- `bash scripts/phase6-release_failure_matrix_contract_test.sh`
- `bash scripts/prod-release_contract_test.sh`
- `bash scripts/prod-rollback_contract_test.sh`
- `bash scripts/prod-restore_contract_test.sh`
- `bash scripts/e2e-phase6_contract_test.sh`
- `bash scripts/phase6-ci_contract_test.sh`
- `bash scripts/phase6-ci_contract_mutation_test.sh`
- `bash scripts/phase6-docs_contract_test.sh`
- `bash scripts/e2e-artifact-sanitization_contract_test.sh`
- `go test -run '^$' ./...` with the existing offline module cache (all packages compiled)
- `go vet ./...` with the existing offline module cache
- `git diff --check`

Focused Go unit packages for release, configuration, secret files, manifests, migration, acceptance, release control, build info, and the changed backup executor passed. Full runtime Go tests cannot run in the current sandbox because local TCP listeners and PostgreSQL connections are prohibited. The cached pnpm 11.9 executable is usable directly despite the system Node 22 Corepack shim failure, but the locked frontend dependencies are not cached; sandboxed registry access is prohibited and unsandboxed registry access was not authorized, so frontend execution remains pending. No project-level Go or ShellCheck installation was introduced.

The uploadable dynamic evidence hash table remains intentionally empty until the changed disposable groups run and their sanitized `containers.log` files are produced. Existing pre-change artifacts are not re-labelled.

## Repository-ready result

Pending fresh Docker-backed `all`, `resources`, and security execution plus frontend verification. The target boundary sentence is not asserted as a completed result in this review yet.

## Real-server work still pending

No real server, DNS, firewall, public certificate, service installation, reboot, production restore switch, `v1.0.0-rc.1`, or `v1.0.0` action was performed. Those actions require the independent approvals and real-server acceptance described in the runbook.
