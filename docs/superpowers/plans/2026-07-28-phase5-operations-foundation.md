# Phase 5 Operations Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add typed operational settings, a cross-process maintenance lease and write gate, and safe teacher audit/settings surfaces.

**Architecture:** Migration 19 adds one settings row and one operational-mode row. `internal/operations` owns validation and compare-and-swap leases. Unsafe HTTP requests hold a PostgreSQL shared advisory lock; backup/release orchestration later takes the exclusive lock, which closes the race between a maintenance check and a business transaction.

**Tech Stack:** Go 1.26.5, PostgreSQL 18.4, Chi, Vue 3, TypeScript, Vitest.

---

## File structure

- Create `db/migrations/00019_operations_foundation.sql`: settings and mode schema.
- Create `internal/platform/database/operations_migration_test.go`: schema and down-migration proof.
- Create `internal/operations/model.go`: public types, defaults, validation, errors.
- Create `internal/operations/store.go`: settings, lease, and write-gate interfaces.
- Create `internal/operations/postgres_store.go`: transactional PostgreSQL implementation.
- Create `internal/operations/service.go`: role checks, version conflicts, audit transactions.
- Create `internal/operations/http.go`: admin settings and audit HTTP boundary.
- Create `internal/operations/write_gate.go`: unsafe-method shared-lock middleware.
- Modify `internal/audit/model.go`, `store.go`, `postgres_store.go`: safe filtered audit reads.
- Modify `internal/app/app.go`: operations mount and maintenance gate.
- Modify `cmd/server/main.go`: production wiring.
- Modify `cmd/worker/main.go`, `internal/notifications/runner.go`,
  `internal/aiqa/runner.go`, `internal/processing/runner.go`: stop new claims while draining.
- Create `web/src/features/operations/types.ts`, `api.ts`: typed client.
- Create `web/src/features/operations/SystemSettingsView.vue`,
  `SystemSettingsView.test.ts`: settings UI.
- Create `web/src/features/operations/AuditView.vue`, `AuditView.test.ts`: safe audit browser.
- Modify `web/src/router/index.ts`, `index.test.ts`,
  `web/src/layouts/ConsoleLayout.vue`, `ConsoleLayout.test.ts`: admin routes.

### Task 1: Add the operations foundation schema

- [ ] **Step 1: Write the failing migration contract**

Create `internal/platform/database/operations_migration_test.go` with a test that
migrates the database and verifies the singleton rows, constraints, and indexes:

```go
func TestOperationsFoundationMigrationContracts(t *testing.T) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil { t.Fatal(err) }
	var tables, singletonRows, constraints int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM information_schema.tables
		   WHERE table_schema='public' AND table_name IN
		     ('system_settings','operational_modes')),
		  (SELECT (SELECT count(*) FROM system_settings)
		        + (SELECT count(*) FROM operational_modes)),
		  (SELECT count(*) FROM pg_constraint
		   WHERE conname IN (
		     'system_settings_retention_check',
		     'system_settings_backup_clock_check',
		     'system_settings_threshold_order_check',
		     'operational_modes_state_check',
		     'operational_modes_lease_shape_check'))`).
		Scan(&tables, &singletonRows, &constraints); err != nil { t.Fatal(err) }
	if tables != 2 || singletonRows != 2 || constraints != 5 {
		t.Fatalf("tables=%d rows=%d constraints=%d", tables, singletonRows, constraints)
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/database \
  -run '^TestOperationsFoundationMigrationContracts$' -count=1
```

Expected: FAIL because `system_settings` does not exist.

- [ ] **Step 3: Add migration 19**

Create `db/migrations/00019_operations_foundation.sql`. Use concrete typed
columns, insert both singleton rows in the up migration, and remove them in the
down migration:

```sql
-- +goose Up
CREATE TABLE system_settings (
  singleton_id boolean PRIMARY KEY DEFAULT true CHECK (singleton_id),
  version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  site_name text NOT NULL DEFAULT 'HappyLearn'
    CHECK (char_length(site_name) BETWEEN 1 AND 80),
  site_announcement text NOT NULL DEFAULT ''
    CHECK (char_length(site_announcement) <= 1000),
  soft_delete_retention_days integer NOT NULL DEFAULT 30,
  audit_retention_days integer NOT NULL DEFAULT 365,
  operational_sample_retention_days integer NOT NULL DEFAULT 7,
  backup_hour integer NOT NULL DEFAULT 3,
  backup_minute integer NOT NULL DEFAULT 0,
  backup_timezone text NOT NULL DEFAULT 'Asia/Shanghai'
    CHECK (backup_timezone='Asia/Shanghai'),
  disk_warning_percent integer NOT NULL DEFAULT 75,
  disk_critical_percent integer NOT NULL DEFAULT 90,
  ai_error_warning_percent integer NOT NULL DEFAULT 10,
  ai_error_critical_percent integer NOT NULL DEFAULT 25,
  processing_queue_warning integer NOT NULL DEFAULT 20,
  processing_queue_critical integer NOT NULL DEFAULT 100,
  updated_by uuid REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT system_settings_retention_check CHECK (
    soft_delete_retention_days BETWEEN 30 AND 365
    AND audit_retention_days BETWEEN 365 AND 2555
    AND operational_sample_retention_days BETWEEN 1 AND 30),
  CONSTRAINT system_settings_backup_clock_check CHECK (
    backup_hour BETWEEN 0 AND 23 AND backup_minute BETWEEN 0 AND 59),
  CONSTRAINT system_settings_threshold_order_check CHECK (
    disk_warning_percent BETWEEN 1 AND 99
    AND disk_critical_percent > disk_warning_percent
    AND disk_critical_percent <= 100
    AND ai_error_warning_percent BETWEEN 1 AND 99
    AND ai_error_critical_percent > ai_error_warning_percent
    AND ai_error_critical_percent <= 100
    AND processing_queue_warning >= 1
    AND processing_queue_critical > processing_queue_warning)
);
INSERT INTO system_settings(singleton_id) VALUES(true);

CREATE TABLE operational_modes (
  singleton_id boolean PRIMARY KEY DEFAULT true CHECK (singleton_id),
  mode text NOT NULL DEFAULT 'normal',
  owner_id uuid,
  lease_token_hash bytea,
  lease_expires_at timestamptz,
  entered_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  CONSTRAINT operational_modes_state_check
    CHECK (mode IN ('normal','draining','backup','release')),
  CONSTRAINT operational_modes_lease_shape_check CHECK (
    (mode='normal' AND owner_id IS NULL AND lease_token_hash IS NULL
      AND lease_expires_at IS NULL AND entered_at IS NULL)
    OR
    (mode<>'normal' AND owner_id IS NOT NULL
      AND octet_length(lease_token_hash)=32
      AND lease_expires_at IS NOT NULL AND entered_at IS NOT NULL))
);
INSERT INTO operational_modes(singleton_id) VALUES(true);

-- +goose Down
DROP TABLE operational_modes;
DROP TABLE system_settings;
```

- [ ] **Step 4: Add down-migration proof**

In the same test file, use the existing `migrationProvider` pattern to migrate
down to 18 and assert both tables are absent, then migrate up and assert both
singleton rows are recreated with the documented defaults.

- [ ] **Step 5: Run migration tests and verify GREEN**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/database \
  -run 'OperationsFoundation' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add db/migrations/00019_operations_foundation.sql \
  internal/platform/database/operations_migration_test.go
git commit -m "feat(operations): add settings and maintenance schema"
```

### Task 2: Implement settings and exclusive operational leases

- [ ] **Step 1: Write model and service tests**

Create `internal/operations/service_test.go` covering defaults, every range
boundary, stale version, inactive/non-admin principals, atomic audit rollback,
lease acquire/renew/release, expired takeover, and stale-owner rejection.

Use these public contracts:

```go
var (
	ErrForbidden = errors.New("forbidden")
	ErrInvalid = errors.New("invalid operations input")
	ErrConflict = errors.New("operations conflict")
	ErrLeaseHeld = errors.New("operational lease held")
	ErrStaleLease = errors.New("stale operational lease")
)

type Settings struct {
	Version int64
	SiteName, SiteAnnouncement string
	SoftDeleteRetentionDays, AuditRetentionDays int
	OperationalSampleRetentionDays int
	BackupHour, BackupMinute int
	BackupTimezone string
	DiskWarningPercent, DiskCriticalPercent int
	AIErrorWarningPercent, AIErrorCriticalPercent int
	ProcessingQueueWarning, ProcessingQueueCritical int
	UpdatedBy uuid.UUID
	UpdatedAt time.Time
}

type Lease struct {
	Mode string
	OwnerID uuid.UUID
	Token []byte
	ExpiresAt time.Time
	Version int64
}
```

- [ ] **Step 2: Run and verify RED**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/operations -count=1
```

Expected: FAIL because the package does not exist.

- [ ] **Step 3: Implement the model and interfaces**

Create `model.go` with the exact types and one `ValidateSettings` function.
Create `store.go`:

```go
type Store interface {
	GetSettings(context.Context) (Settings, error)
	UpdateSettings(context.Context, Principal, Settings) (Settings, error)
	GetMode(context.Context) (ModeSnapshot, error)
	AcquireLease(context.Context, LeaseRequest) (Lease, error)
	RenewLease(context.Context, Lease, time.Time) (Lease, error)
	TransitionLease(context.Context, Lease, string, time.Time) (Lease, error)
	ReleaseLease(context.Context, Lease) error
}

type WriteGate interface {
	AcquireShared(context.Context) (release func(), err error)
}
```

Use a 32-byte random token and store only SHA-256. All mutations use
`SELECT ... FOR UPDATE`, compare the expected version or token hash, update the
row, and write `audit.Event` through the same `pgx.Tx`.

- [ ] **Step 4: Implement PostgreSQL behavior**

Create `postgres_store.go`. Use advisory key `845103120`: shared mode for public
writes and exclusive mode for maintenance/release. The exclusive lease flow
must acquire the session advisory lock before changing the row and retain that
connection until release.

Do not return raw PostgreSQL uniqueness or check errors. Map them to the domain
errors above.

- [ ] **Step 5: Run service and PostgreSQL tests**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/operations -count=1
GOENV=off GOFLAGS='' go test -race ./internal/operations -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/operations
git commit -m "feat(operations): manage settings and operational leases"
```

### Task 3: Gate unsafe HTTP writes and background claims

- [ ] **Step 1: Write the failing middleware tests**

Create `internal/operations/write_gate_test.go`. Verify:

```go
func TestWriteGateBlocksUnsafeMethodsButAllowsReadsAndLogout(t *testing.T) {
	// GET passes without a shared lock.
	// POST acquires and releases one shared lock around the complete handler.
	// ErrMaintenance maps to 503 maintenance_mode.
	// /api/v1/auth/logout remains callable.
	// A panic still releases the shared lock before Recoverer completes.
}
```

Add focused runner tests proving a non-normal mode prevents new claims while an
already claimed item can finish and settle.

- [ ] **Step 2: Run and verify RED**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/operations ./internal/app \
  ./internal/notifications ./internal/aiqa ./internal/processing \
  -run 'Maintenance|OperationalGate' -count=1
```

Expected: FAIL because the gate is not wired.

- [ ] **Step 3: Implement HTTP middleware**

Create:

```go
func UnsafeWriteGate(gate WriteGate) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead ||
				r.Method == http.MethodOptions || r.URL.Path == "/api/v1/auth/logout" {
				next.ServeHTTP(w, r)
				return
			}
			release, err := gate.AcquireShared(r.Context())
			if err != nil {
				httpx.Error(w, r, http.StatusServiceUnavailable,
					"maintenance_mode", "系统维护中，请稍后重试")
				return
			}
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}
```

Add `OperationsWriteGate operations.WriteGate` to `app.Dependencies` and apply
the middleware to `/api/v1` before route registration.

- [ ] **Step 4: Gate background claims**

Add a minimal interface shared by runners:

```go
type ClaimGate interface {
	ClaimsAllowed(context.Context) (bool, error)
}
```

Check it immediately before each claim in notification, AI, processing, and
cleanup polling loops. Treat gate errors as “do not claim”, log only the safe
category, and retry on the normal poll interval. Do not cancel work already
leased.

- [ ] **Step 5: Wire server and worker**

Construct one PostgreSQL operations store in `cmd/server/main.go` and
`cmd/worker/main.go`. Pass it to HTTP and runner gates. Extend wiring tests so a
missing gate fails startup in production wiring but remains injectable in
unit tests.

- [ ] **Step 6: Run focused and regression tests**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/app ./internal/operations \
  ./internal/notifications ./internal/aiqa ./internal/processing ./cmd/server ./cmd/worker \
  -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/operations internal/app internal/notifications internal/aiqa \
  internal/processing cmd/server cmd/worker
git commit -m "feat(operations): gate writes during maintenance"
```

### Task 4: Add settings and safe audit APIs

- [ ] **Step 1: Write failing HTTP and audit-store tests**

Cover admin-only role enforcement, exact query allowlists, keyset pagination,
metadata redaction, stale settings conflict, No-Store, CSRF/origin behavior,
method-not-allowed, and uniform error envelopes.

Define:

```go
type AuditFilter struct {
	Action, TargetType, Outcome string
	ActorID uuid.UUID
	From, To time.Time
	BeforeID int64
	Limit int
}

type AuditPage struct {
	Items []audit.Record
	NextBeforeID int64
}
```

- [ ] **Step 2: Run and verify RED**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/audit ./internal/operations \
  -run 'Audit|HTTP|Settings' -count=1
```

Expected: FAIL because filtered reads and handlers are absent.

- [ ] **Step 3: Extend audit reads safely**

Replace the positional `List(context.Context, int, int64)` dependency with a
filtered query method while preserving a compatibility wrapper for existing
tests. Allow only safe metadata keys:

```go
var publicAuditMetadata = map[string]struct{}{
	"status": {}, "reason": {}, "version": {}, "count": {},
	"provider_id": {}, "model_id": {}, "file_purpose": {},
}
```

Drop all other metadata keys from admin DTOs. Never return IP, request payload,
credentials, object key, filename, prompt, or response content.

- [ ] **Step 4: Implement operations HTTP routes**

Mount these exact routes under `/api/v1/admin/operations`:

```go
r.Get("/settings", h.getSettings)
r.Put("/settings", h.updateSettings)
r.Get("/audit", h.listAudit)
```

The update DTO includes every typed setting and `version`. Unknown JSON fields,
missing values, explicit empty query values, and non-canonical UUIDs are
invalid. Map stale versions to HTTP 409 `settings_conflict`.

- [ ] **Step 5: Wire and run tests**

Add `AdminOperations operations.HTTPService` to `app.Dependencies`, construct it
in `cmd/server/main.go`, and extend app route/wiring tests.

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/audit ./internal/operations \
  ./internal/app ./cmd/server -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/audit internal/operations internal/app cmd/server
git commit -m "feat(operations): expose settings and safe audit reads"
```

### Task 5: Build settings and audit pages

- [ ] **Step 1: Write failing API and component tests**

Create typed API tests for exact URLs and DTO parsing. Component tests must
cover loading, validation, save, 409 conflict reload, dirty navigation warning,
audit filters, stable load-more merge, redaction, keyboard focus after retry,
and mobile semantics.

Use these client contracts:

```ts
export type OperationsSettings = {
  version: number
  siteName: string
  siteAnnouncement: string
  softDeleteRetentionDays: number
  auditRetentionDays: number
  operationalSampleRetentionDays: number
  backupHour: number
  backupMinute: number
  backupTimezone: 'Asia/Shanghai'
  diskWarningPercent: number
  diskCriticalPercent: number
  aiErrorWarningPercent: number
  aiErrorCriticalPercent: number
  processingQueueWarning: number
  processingQueueCritical: number
  updatedAt: string
}
```

- [ ] **Step 2: Run and verify RED**

Run:

```bash
pnpm --dir web test -- src/features/operations src/router/index.test.ts \
  src/layouts/ConsoleLayout.test.ts
```

Expected: FAIL because the feature files and routes are absent.

- [ ] **Step 3: Implement typed clients and views**

Create:

```ts
export const readSettings = (signal?: AbortSignal) =>
  request<OperationsSettings>('/admin/operations/settings', { signal })

export const saveSettings = (value: OperationsSettings) =>
  request<OperationsSettings>('/admin/operations/settings', {
    method: 'PUT',
    json: value,
  })
```

Implement explicit form fields and client validation matching server ranges.
Use abort controllers and generation counters for audit replacement/appends.
Show request IDs on failures. Do not render raw audit metadata.

- [ ] **Step 4: Add routes and navigation**

Add `/admin/settings` and `/admin/audit` under the admin route tree. Add one
“系统设置” sidebar link and links from the current empty dashboard. Prove students
cannot navigate to either route.

- [ ] **Step 5: Run frontend gates**

Run:

```bash
pnpm --dir web test
pnpm --dir web typecheck
pnpm lint
pnpm --dir web build
```

Expected: all PASS; build may retain the existing informational chunk advisory.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/operations web/src/router web/src/layouts \
  web/src/features/home/AdminHomeView.vue
git commit -m "feat(operations): add settings and audit console"
```

### Task 6: Close the foundation plan

- [ ] **Step 1: Run the focused complete gate**

```bash
GOENV=off GOFLAGS='' go test -p 1 ./internal/operations ./internal/audit \
  ./internal/app ./internal/notifications ./internal/aiqa ./internal/processing \
  ./cmd/server ./cmd/worker -count=1
GOENV=off GOFLAGS='' go test -race -p 1 ./internal/operations ./internal/audit \
  ./internal/app ./internal/notifications ./internal/aiqa ./internal/processing \
  ./cmd/server ./cmd/worker -count=1
pnpm test
pnpm typecheck
pnpm lint
pnpm build
git diff --check
```

Expected: all commands PASS.

- [ ] **Step 2: Review the plan diff**

Confirm:

- the application never owns the Docker socket;
- maintenance write exclusion is race-free;
- logout and safe health reads remain available;
- stale lease owners cannot release or transition;
- audit DTOs expose only allowed metadata;
- settings never contain infrastructure secrets.

- [ ] **Step 3: Record and commit the review**

Create `.superpowers/sdd/phase5-operations-foundation-report.md` with commands,
fresh results, reviewed commit range, and findings. Fix every Critical or
Important finding before committing:

```bash
git add .superpowers/sdd/phase5-operations-foundation-report.md
git commit -m "test(operations): close phase 5 foundation gate"
```
