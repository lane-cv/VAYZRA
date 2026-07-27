# Phase 5 Backup and Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce encrypted, deduplicated PostgreSQL and AIStor recovery points with local retention, optional S3 replication, teacher-visible state, and disposable empty-environment restore verification.

**Architecture:** Migration 20 stores durable run, artifact, and verification state. `internal/backup` owns the state machine and admin reads. A non-root one-shot backup image contains PostgreSQL client tools, Restic 0.19.1, and Age 1.3.1. Host orchestration controls the maintenance window without mounting the Docker socket into application containers.

**Tech Stack:** Go 1.26.5, PostgreSQL 18.4, AIStor, Restic 0.19.1, Age 1.3.1, Docker Compose, Vue 3, Playwright.

---

## File structure

- Create `db/migrations/00020_backup_restore.sql`.
- Create `internal/platform/database/backup_restore_migration_test.go`.
- Create `internal/backup/model.go`, `store.go`, `service.go`.
- Create `internal/backup/postgres_store.go`, `postgres_store_test.go`.
- Create `internal/backup/http.go`, `http_test.go`.
- Create `internal/backup/executor.go`, `executor_test.go`.
- Create `internal/backup/manifest.go`, `manifest_test.go`.
- Create `cmd/backup/main.go`, `main_test.go`.
- Create `Dockerfile.backup`.
- Create `scripts/phase5-backup.sh`, `phase5-backup_contract_test.sh`.
- Create `scripts/phase5-restore-verify.sh`,
  `phase5-restore_contract_test.sh`.
- Modify `internal/app/app.go`, `cmd/server/main.go`, `deploy/compose.dev.yml`,
  `Makefile`, `package.json`.
- Create `web/src/features/operations/BackupsView.vue`,
  `BackupsView.test.ts`.
- Modify `web/src/features/operations/api.ts`, `types.ts`,
  `web/src/router/index.ts`, `index.test.ts`,
  `web/src/layouts/ConsoleLayout.vue`, `ConsoleLayout.test.ts`.

### Task 1: Add durable backup and restore state

- [ ] **Step 1: Write the failing migration test**

Create a test that expects `backup_runs`, `backup_artifacts`, and
`restore_verifications`, their state checks, idempotency constraint, and query
indexes:

```go
func TestBackupRestoreMigrationContracts(t *testing.T) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil { t.Fatal(err) }
	var tables, constraints, indexes int
	err := pool.QueryRow(ctx, `
	  SELECT
	    (SELECT count(*) FROM information_schema.tables
	     WHERE table_schema='public' AND table_name IN
	       ('backup_runs','backup_artifacts','restore_verifications')),
	    (SELECT count(*) FROM pg_constraint
	     WHERE conname IN ('backup_runs_state_check','backup_runs_terminal_check',
	       'backup_artifacts_kind_check','backup_artifacts_repository_check',
	       'restore_verifications_state_check')),
	    (SELECT count(*) FROM pg_indexes
	     WHERE indexname IN ('backup_runs_requested_idx',
	       'backup_runs_due_idx','restore_verifications_backup_idx'))`).
	  Scan(&tables, &constraints, &indexes)
	if err != nil { t.Fatal(err) }
	if tables != 3 || constraints != 5 || indexes != 3 {
		t.Fatalf("tables=%d constraints=%d indexes=%d", tables, constraints, indexes)
	}
}
```

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/database \
  -run '^TestBackupRestoreMigrationContracts$' -count=1
```

Expected: FAIL because the tables are absent.

- [ ] **Step 3: Create migration 20**

Implement `db/migrations/00020_backup_restore.sql` with these essential
contracts:

```sql
-- +goose Up
CREATE TABLE backup_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  idempotency_key text NOT NULL CHECK (
    char_length(idempotency_key) BETWEEN 1 AND 128),
  trigger_kind text NOT NULL CHECK (
    trigger_kind IN ('scheduled','manual','pre_release')),
  state text NOT NULL DEFAULT 'queued',
  requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
  requested_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  finished_at timestamptz,
  database_migration_version bigint,
  encryption_key_id text,
  local_snapshot_id text,
  remote_snapshot_id text,
  manifest_sha256 bytea,
  logical_bytes bigint CHECK (logical_bytes IS NULL OR logical_bytes >= 0),
  stored_bytes bigint CHECK (stored_bytes IS NULL OR stored_bytes >= 0),
  local_expires_at timestamptz,
  remote_expires_at timestamptz,
  error_category text NOT NULL DEFAULT '',
  error_trace_id text NOT NULL DEFAULT '',
  owner_id uuid,
  lease_expires_at timestamptz,
  CONSTRAINT backup_runs_idempotency_key UNIQUE(trigger_kind,idempotency_key),
  CONSTRAINT backup_runs_state_check CHECK (state IN (
    'queued','draining','snapshotting','encrypting','verifying','syncing',
    'succeeded','degraded','failed')),
  CONSTRAINT backup_runs_terminal_check CHECK (
    (state IN ('succeeded','degraded','failed') AND finished_at IS NOT NULL)
    OR
    (state NOT IN ('succeeded','degraded','failed') AND finished_at IS NULL))
);
CREATE INDEX backup_runs_requested_idx
  ON backup_runs(requested_at DESC,id DESC);
CREATE INDEX backup_runs_due_idx
  ON backup_runs(state,requested_at,id)
  WHERE state NOT IN ('succeeded','degraded','failed');

CREATE TABLE backup_artifacts (
  backup_run_id uuid NOT NULL REFERENCES backup_runs(id) ON DELETE CASCADE,
  kind text NOT NULL,
  repository text NOT NULL,
  snapshot_id text NOT NULL,
  sha256 bytea NOT NULL CHECK (octet_length(sha256)=32),
  size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
  verified_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY(backup_run_id,kind,repository),
  CONSTRAINT backup_artifacts_kind_check CHECK (
    kind IN ('database_dump','object_snapshot','manifest','recovery_report')),
  CONSTRAINT backup_artifacts_repository_check CHECK (
    repository IN ('local','remote'))
);

CREATE TABLE restore_verifications (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  backup_run_id uuid NOT NULL REFERENCES backup_runs(id),
  state text NOT NULL DEFAULT 'queued',
  started_at timestamptz,
  finished_at timestamptz,
  restored_migration_version bigint,
  database_row_counts jsonb NOT NULL DEFAULT '{}'::jsonb,
  checked_object_count bigint NOT NULL DEFAULT 0,
  missing_object_count bigint NOT NULL DEFAULT 0,
  unexpected_object_count bigint NOT NULL DEFAULT 0,
  session_revocation_verified boolean NOT NULL DEFAULT false,
  rto_seconds bigint,
  report_sha256 bytea,
  error_category text NOT NULL DEFAULT '',
  error_trace_id text NOT NULL DEFAULT '',
  CONSTRAINT restore_verifications_state_check CHECK (
    state IN ('queued','restoring','checking','succeeded','failed'))
);
CREATE INDEX restore_verifications_backup_idx
  ON restore_verifications(backup_run_id,started_at DESC,id DESC);

-- +goose Down
DROP TABLE restore_verifications;
DROP TABLE backup_artifacts;
DROP TABLE backup_runs;
```

- [ ] **Step 4: Add invariant and down tests**

Prove invalid transitions/terminal shapes fail, duplicate idempotency is
rejected, artifact hashes are 32 bytes, and migration down to 19 removes only
the backup schema.

- [ ] **Step 5: Run and verify GREEN**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/database \
  -run 'BackupRestore' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add db/migrations/00020_backup_restore.sql \
  internal/platform/database/backup_restore_migration_test.go
git commit -m "feat(backup): add recovery point schema"
```

### Task 2: Implement the backup state machine and admin API

- [ ] **Step 1: Write failing domain tests**

Cover scheduled idempotency by Shanghai date, manual idempotency, one active
claim, lease expiry/takeover, every allowed transition, forbidden skips,
local-success/remote-failure degradation, retention selection, safe DTOs, and
admin-only HTTP behavior.

Use:

```go
type State string
const (
	StateQueued State = "queued"
	StateDraining State = "draining"
	StateSnapshotting State = "snapshotting"
	StateEncrypting State = "encrypting"
	StateVerifying State = "verifying"
	StateSyncing State = "syncing"
	StateSucceeded State = "succeeded"
	StateDegraded State = "degraded"
	StateFailed State = "failed"
)

type Store interface {
	Create(context.Context, CreateInput) (Run, error)
	Claim(context.Context, uuid.UUID, time.Duration) (Run, error)
	Transition(context.Context, TransitionInput) (Run, error)
	AddArtifact(context.Context, Artifact) error
	List(context.Context, Filter) ([]RunSummary, Cursor, error)
	Get(context.Context, uuid.UUID) (RunDetail, error)
	RetentionCandidates(context.Context, RetentionPolicy) ([]Artifact, error)
}
```

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/backup -count=1
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement model, store, and service**

Create exact transition validation:

```go
var nextStates = map[State]map[State]bool{
	StateQueued: {StateDraining: true, StateFailed: true},
	StateDraining: {StateSnapshotting: true, StateFailed: true},
	StateSnapshotting: {StateEncrypting: true, StateFailed: true},
	StateEncrypting: {StateVerifying: true, StateFailed: true},
	StateVerifying: {StateSyncing: true, StateSucceeded: true, StateFailed: true},
	StateSyncing: {StateSucceeded: true, StateDegraded: true, StateFailed: true},
}
```

Store only opaque Restic snapshot IDs and SHA-256 values. Redact repository
paths, object names, subprocess output, and credentials from errors and DTOs.

- [ ] **Step 4: Implement admin routes**

Mount:

```go
r.Get("/backups", h.list)
r.Post("/backups", h.create)
r.Get("/backups/{id}", h.detail)
```

`POST` requires one `Idempotency-Key` header matching
`^[A-Za-z0-9._:-]{8,128}$`. It creates a `manual` queued run and returns 202.
List uses stable `(requested_at,id)` keyset pagination. Detail includes
artifacts and restore verifications but no storage path.

- [ ] **Step 5: Wire and verify**

Add the backup service to the existing operations handler and server wiring.

```bash
GOENV=off GOFLAGS='' go test ./internal/backup ./internal/operations \
  ./internal/app ./cmd/server -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/backup internal/operations internal/app cmd/server
git commit -m "feat(backup): manage durable recovery runs"
```

### Task 3: Build the hardened backup image and executor

- [ ] **Step 1: Write failing executor and image contracts**

Tests must prove:

- subprocess arguments never contain repository or database passwords;
- JSON output is bounded to 1 MiB and strictly decoded;
- cancellation terminates child processes;
- wrong/tampered snapshot is a safe integrity error;
- no temporary plaintext remains on success, failure, or signal;
- image runs as `10003:0`, has a read-only root, and contains exactly Restic
  0.19.1 and Age 1.3.1.

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/backup ./cmd/backup \
  -run 'Executor|Manifest|Command' -count=1
bash scripts/phase5-backup_contract_test.sh
```

Expected: FAIL because executor, command, image, and script are absent.

- [ ] **Step 3: Implement the manifest**

Use one canonical JSON structure:

```go
type Manifest struct {
	SchemaVersion int `json:"schemaVersion"`
	BatchID string `json:"batchId"`
	CreatedAt time.Time `json:"createdAt"`
	DatabaseMigrationVersion int64 `json:"databaseMigrationVersion"`
	DatabaseDumpSHA256 string `json:"databaseDumpSha256"`
	ObjectSnapshotID string `json:"objectSnapshotId"`
	ObjectCount int64 `json:"objectCount"`
	ReferencedBytes int64 `json:"referencedBytes"`
}
```

Reject unknown fields, non-canonical UUID, non-UTC timestamps, non-lowercase
64-character SHA-256, negative counts, and schema versions other than 1.

- [ ] **Step 4: Implement `Dockerfile.backup`**

Build tools from pinned Go module tags:

```dockerfile
FROM golang:1.26.5-bookworm AS tools
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/restic/restic/cmd/restic@v0.19.1 \
 && CGO_ENABLED=0 GOBIN=/out go install filippo.io/age/cmd/age@v1.3.1 \
 && CGO_ENABLED=0 GOBIN=/out go install filippo.io/age/cmd/age-keygen@v1.3.1
```

Build `/app/happylearn-backup` from `cmd/backup`, copy `pg_dump`, `pg_restore`,
Restic and Age into a Debian 12.12 runtime, create UID 10003/group 0, and set a
non-shell entrypoint. Do not include Docker CLI or a socket.

- [ ] **Step 5: Implement executor and command**

`cmd/backup` exposes:

```text
happylearn-backup prepare --run-id <uuid>
happylearn-backup snapshot --run-id <uuid>
happylearn-backup verify --run-id <uuid>
happylearn-backup sync --run-id <uuid>
happylearn-backup finish --run-id <uuid>
happylearn-backup fail --run-id <uuid> --category <allowlisted>
```

Passwords and repository locations come from fixed secret-file paths. Use
`exec.CommandContext`, closed stdin, bounded stdout/stderr capture, and safe
error categories. `snapshot` creates the PostgreSQL custom dump in an
owner-only work directory, invokes Restic for the dump and stopped AIStor
volume, and Age-encrypts the offline recovery bundle to
`HAPPYLEARN_BACKUP_AGE_RECIPIENT`.

- [ ] **Step 6: Verify image and unit tests**

```bash
GOENV=off GOFLAGS='' go test ./internal/backup ./cmd/backup -count=1
docker build -f Dockerfile.backup -t happylearn-backup:phase5 .
test "$(docker image inspect -f '{{.Config.User}}' happylearn-backup:phase5)" = '10003:0'
docker run --rm --entrypoint restic happylearn-backup:phase5 version
docker run --rm --entrypoint age happylearn-backup:phase5 --version
```

Expected: tests pass and versions are exactly 0.19.1 and 1.3.1.

- [ ] **Step 7: Commit**

```bash
git add Dockerfile.backup internal/backup cmd/backup
git commit -m "feat(backup): add encrypted recovery executor"
```

### Task 4: Orchestrate local/remote backup and retention

- [ ] **Step 1: Write the failing shell contract**

Create `scripts/phase5-backup_contract_test.sh` that requires:

- exact project and Compose file arguments;
- `flock` or atomic lock directory before mutation;
- prepare/drain before stopping services;
- worker and AIStor stop before snapshot;
- AIStor restart and authenticated readiness before finish;
- traps that restart stopped services and record failure;
- local `check` before `forget --prune`;
- remote copy only when fully configured;
- no `set -x`, `env`, `printenv`, Docker inspect, secret in argv, broad removal,
  or Docker socket mount.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/phase5-backup_contract_test.sh
```

Expected: FAIL because `scripts/phase5-backup.sh` is absent.

- [ ] **Step 3: Implement the orchestrator**

The script accepts only:

```text
scripts/phase5-backup.sh --project happylearn-dev --trigger scheduled|manual|pre_release
```

It resolves the repository root, validates the AIStor license and backup secret
files, queues or claims the run, enters drain, waits on durable active counts,
stops worker and AIStor, runs the one-shot backup container, restarts AIStor,
checks authenticated readiness, starts worker, verifies the local repository,
copies to optional S3, applies `keep-daily 7` locally and
`keep-daily 30 --keep-monthly 12` remotely, and records success/degraded/failure.

- [ ] **Step 4: Add the development backup profile**

Extend `deploy/compose.dev.yml` with a profile-only `backup` service. It has no
published port, read-only root, `10003:0`, all capabilities dropped, tmpfs work,
read-only AIStor volume, owner-only local repository bind, and secret files.
It is not a long-running service.

- [ ] **Step 5: Run live local and remote fixture tests**

Start the existing licensed dependencies plus an isolated second
S3-compatible fixture. Prove local success, remote success, remote outage
degraded state, retention, and recovery after retry.

Expected: the local recovery point remains verified when remote replication is
unavailable.

- [ ] **Step 6: Commit**

```bash
git add scripts/phase5-backup.sh scripts/phase5-backup_contract_test.sh \
  deploy/compose.dev.yml Makefile package.json
git commit -m "feat(backup): orchestrate local and remote recovery points"
```

### Task 5: Restore into an empty disposable environment

- [ ] **Step 1: Write the failing restore contract**

Require a unique project prefix, empty target volumes, exact backup UUID,
repository check before restore, session revocation before app start,
allowlisted row counts, complete database file-reference verification, two
student isolation probes, RTO capture, artifact sanitization, and complete
cleanup.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/phase5-restore_contract_test.sh
```

Expected: FAIL because the restore harness is absent.

- [ ] **Step 3: Implement restore verification**

Create `scripts/phase5-restore-verify.sh` with strict argument validation:

```text
scripts/phase5-restore-verify.sh --backup-id <canonical-uuid>
```

Restore to new PostgreSQL and AIStor volumes, execute:

```sql
UPDATE sessions
SET revoked_at=COALESCE(revoked_at,now()),
    revoke_reason=COALESCE(revoke_reason,'restore_verification');
```

Start the app only after revocation. Run a Go verification command that counts
allowlisted tables and streams every live `file_versions`/preview reference to
an authenticated AIStor `StatObject`, checking size and absence of unexpected
authorization. Store only counts and a SHA-256 report.

- [ ] **Step 4: Inject restore failures**

Prove wrong repository secret, altered pack, missing object, stale session,
non-empty target, invalid backup ID, and timeout all fail closed and leave no
disposable resource.

- [ ] **Step 5: Verify the four-hour RTO contract**

The local gate records duration and asserts it is less than four hours. Use
condition polling, not fixed sleeps.

- [ ] **Step 6: Commit**

```bash
git add scripts/phase5-restore-verify.sh \
  scripts/phase5-restore_contract_test.sh internal/backup cmd/backup Makefile
git commit -m "feat(backup): verify recovery in an empty environment"
```

### Task 6: Add teacher backup history

- [ ] **Step 1: Write failing client and view tests**

Cover list/detail parsing, keyset append, state labels, local/remote distinction,
degraded warning, queue-manual idempotency, restore result, retry focus, abort,
empty state, mobile cards, and absence of paths/credentials.

- [ ] **Step 2: Run and verify RED**

```bash
pnpm --dir web test -- src/features/operations/BackupsView.test.ts
```

Expected: FAIL because the view is absent.

- [ ] **Step 3: Implement the view**

Add `/admin/backups`, an admin-only route, and:

```ts
export const queueBackup = (idempotencyKey: string) =>
  request<BackupRun>('/admin/operations/backups', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    json: {},
  })
```

Generate one UUID idempotency key per user action and retain it across network
retry. Do not offer restore. Manual backup requires a confirmation dialog that
states a short maintenance window will occur.

- [ ] **Step 4: Run frontend gates**

```bash
pnpm --dir web test
pnpm --dir web typecheck
pnpm lint
pnpm --dir web build
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/features/operations web/src/router web/src/layouts
git commit -m "feat(backup): add teacher recovery history"
```

### Task 7: Close the backup plan

- [ ] **Step 1: Run the complete focused gate**

```bash
GOENV=off GOFLAGS='' go test -p 1 ./internal/backup ./internal/operations \
  ./internal/platform/database ./internal/app ./cmd/backup ./cmd/server -count=1
GOENV=off GOFLAGS='' go test -race -p 1 ./internal/backup \
  ./internal/operations ./internal/app ./cmd/backup ./cmd/server -count=1
pnpm test
pnpm typecheck
pnpm lint
pnpm build
bash scripts/phase5-backup_contract_test.sh
bash scripts/phase5-restore_contract_test.sh
git diff --check
```

Expected: all PASS.

- [ ] **Step 2: Review security and recovery invariants**

Confirm no plaintext dump survives; Restic/Age versions are pinned; no secret is
in argv, logs, metrics, browser DTOs, or image history; remote failure is
degraded; prune cannot delete the last good point; empty restore checks all live
references and revokes sessions.

- [ ] **Step 3: Record and commit the review**

Create `.superpowers/sdd/phase5-backup-restore-report.md`, fix every Critical or
Important finding, rerun the gate, then:

```bash
git add .superpowers/sdd/phase5-backup-restore-report.md
git commit -m "test(backup): close recovery point gate"
```
