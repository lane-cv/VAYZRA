# Phase 5 Backup Task 4 Specification Repair Plan

> **Scope:** Repair Task 4 only. Do not begin Task 5.

**Goal:** Make backup orchestration retention-safe across random Restic paths
and hosts, bound every PostgreSQL control query, recover cleanly from a failed
remote probe, prove the real local/remote lifecycle, and stay within the
2 CPU / 4 GiB maintenance budget.

**Architecture:** Keep retention policy in `internal/backup`. A small
retention helper uses the existing `Service.RetentionCandidates` selection,
validates that every committed/prospective run has exactly the three expected
artifacts sharing one 64-hex snapshot ID, parses bounded Restic snapshot JSON,
and computes the success-eviction plus 24-hour-orphan set. The host
orchestrator consumes only the helper's strict opaque-ID/count result and never
parses Restic JSON or reimplements the policy. PostgreSQL control queries run
in bounded process groups with libpq connect and server statement timeouts.

---

### Task 1: Write RED retention and input-validation tests

**Files:**
- Modify: `scripts/phase5-backup_contract_test.sh`
- Create: `cmd/backup-retention/main_test.go`
- Modify: `internal/backup/postgres_store_test.go`

- Add contract fixtures with random Restic paths and different hosts,
  multiple successful and failed snapshots, local 7-day and remote
  30-daily/12-monthly boundaries, 30-day pre-release protection, last-good
  protection, and 24-hour orphan cleanup.
- Add malformed remote repository cases matching `validRemoteRepository`:
  userinfo, query, fragment, empty bucket, non-HTTPS, backslash,
  `insecure-tls`, and surrounding whitespace.
- Add malformed secret-content cases matching Task 3 single-line rules.
- Run focused tests and retain the expected failures before implementation.

### Task 2: Reuse the Go retention selector

**Files:**
- Create: `cmd/backup-retention/main.go`
- Modify: `internal/backup/postgres_store.go`
- Modify: `Dockerfile.backup`

- Add a bounded helper path that calls the existing
  `Service.RetentionCandidates` policy and deduplicates 64-hex snapshot IDs.
- Include the currently verified recovery point prospectively without allowing
  failed or unverified historical runs into successful day/month slots.
- Require exactly the database dump, object snapshot, and manifest artifacts
  with one common 64-hex snapshot ID for every committed/prospective run.
- Parse bounded Restic snapshots JSON and perform all protected/orphan set
  operations in Go; emit only strict opaque IDs/counts and never repository
  locations, paths, credentials, or raw Restic output.
- Build the helper into the existing hardened backup image.

### Task 3: Make repository cleanup explicit and crash-safe

**Files:**
- Modify: `scripts/phase5-backup.sh`
- Modify: `scripts/phase5-backup_contract_test.sh`

- Require a successful `restic check --read-data`.
- Let the Go helper add only uncommitted snapshots older than 24 hours to the
  deletion set; the shell must not parse or filter Restic JSON.
- Fail closed when protected/current snapshots or IDs are inconsistent.
- Consume the helper's strictly validated opaque deletion result and invoke
  Restic 0.19.1 with global grouping (`--group-by ''`) and explicit,
  deduplicated, deterministically sorted, bounded batches before `--prune`.
- Prove retries preserve current, last-good, and protected pre-release points.

### Task 4: Bound PostgreSQL control queries and lock teardown

**Files:**
- Modify: `scripts/phase5-backup.sh`
- Modify: `scripts/phase5-backup_contract_test.sh`

- Add a host deadline to every ordinary `database_query`, plus
  `PGCONNECTTIMEOUT` and PostgreSQL `statement_timeout`.
- Keep the advisory-lock session long-held, but bound acquisition and shutdown.
- Require a release marker, then kill its process group if marker or exit misses
  the deadline.
- Add hanging queue, renew, release, drain, and protected-tag/plan SQL fixtures;
  assert deadline return and non-hanging cleanup.

### Task 5: Repair remote recovery and resource limits

**Files:**
- Modify: `scripts/phase5-backup.sh`
- Modify: `deploy/compose.dev.yml`
- Modify: `scripts/phase5-backup_contract_test.sh`

- Clear an earlier repository-probe failure when Task 3 sync later succeeds and
  the remote snapshot validates, then run remote check and retention.
- Preserve degraded completion when sync or validation still fails.
- Add CPU and memory limits to backup and both backup init services.
- Calculate the maintenance peak with worker stopped and prove it remains at or
  below 2 CPU / 4 GiB without weakening isolation.

### Task 6: Add and execute a real Task 4 fixture

**Files:**
- Create: `scripts/phase5-backup_live_test.sh`
- Modify: `Makefile`
- Modify: `package.json`

- Create uniquely named disposable resources and exact-target cleanup.
- Use the required AIStor license path without reading or logging its content.
- Prove real local success, HTTPS S3-compatible remote success,
  outage-to-degraded behavior, recovery retry, actual retention eviction, and
  no orphan containers.
- Keep the fake-Docker contract as the fast deterministic gate.

### Task 7: Verify and commit

- Run focused Go and shell tests.
- Run all Go tests, serial full race tests, `go vet`, Compose base/profile
  rendering, shell contracts, and the real live fixture.
- Inspect and remove only resources created by the fixture.
- Review the complete Task 4 repair diff for specification and security.
- Commit one fix commit and require a clean worktree.
