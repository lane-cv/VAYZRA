# Phase 5 Monitoring, Alerts, and Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collect safe operational samples, expose protected Prometheus metrics, evaluate durable alerts, deliver optional safe webhooks, and replace the current empty teacher home with the Phase 5 dashboard.

**Architecture:** Migration 21 stores short-lived aggregate samples and durable alert timelines. Application collectors query bounded business/dependency metrics; a signed host sampler submits Docker and disk aggregates without a socket mount. A separate internal HTTP listener protects metrics and ingestion. Alert evaluation uses threshold hysteresis and reuses a platform-level pinned outbound transport extracted from the proven Phase 4 SSRF implementation.

**Tech Stack:** Go 1.26.5, PostgreSQL 18.4, Redis 8.8, AIStor, Vue 3, TypeScript, Prometheus text exposition.

---

## File structure

- Create `db/migrations/00021_operations_monitoring.sql`.
- Create `internal/platform/database/operations_monitoring_migration_test.go`.
- Create `internal/operations/sample.go`, `postgres_samples.go`.
- Create `internal/operations/dashboard.go`, `postgres_dashboard.go`.
- Create `internal/operations/alerts.go`, `postgres_alerts.go`,
  `alert_runner.go`.
- Create `internal/operations/metrics.go`, `metrics_test.go`.
- Create `internal/operations/internal_http.go`, `internal_http_test.go`.
- Create `internal/operations/webhook.go`, `webhook_test.go`.
- Create `internal/platform/secretfile/secretfile.go`,
  `secretfile_test.go`.
- Create `internal/platform/safehttp/policy.go`, `transport.go` and tests.
- Create `internal/platform/safelog/logger.go`, `logger_test.go`.
- Create `internal/operations/retention.go`, `retention_test.go`.
- Modify `internal/aiqa/url_policy.go`, `safe_transport.go` and tests to wrap
  `safehttp`.
- Modify `internal/platform/config/config.go`, `config_test.go`.
- Modify `cmd/server/main.go`, `cmd/worker/main.go`, tests.
- Modify `deploy/compose.dev.yml`.
- Create `cmd/host-sampler/main.go`, `main_test.go`.
- Create `scripts/collect-host-metrics.sh`,
  `host-metrics_contract_test.sh`.
- Create `web/src/features/operations/AlertsView.vue`,
  `AlertsView.test.ts`.
- Replace `web/src/features/home/AdminHomeView.vue`; create
  `AdminHomeView.test.ts`.
- Modify operations API/types, router, layout, Makefile, package scripts.

### Task 1: Add samples and alert schema

- [ ] **Step 1: Write the failing migration contract**

Expect `operational_samples`, `operational_alerts`, and `alert_deliveries`,
fixed state checks, one unresolved alert per dedupe key, and retention/query
indexes.

```go
func TestOperationsMonitoringMigrationContracts(t *testing.T) {
	pool := integration.StartPostgres(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil { t.Fatal(err) }
	var tables, constraints, indexes int
	if err := pool.QueryRow(ctx, `
	  SELECT
	    (SELECT count(*) FROM information_schema.tables
	     WHERE table_schema='public' AND table_name IN
	       ('operational_samples','operational_alerts','alert_deliveries')),
	    (SELECT count(*) FROM pg_constraint WHERE conname IN
	       ('operational_samples_source_check','operational_samples_value_check',
	        'operational_alerts_severity_check','operational_alerts_state_check',
	        'operational_alerts_value_check',
	        'operational_alerts_acknowledgement_check',
	        'operational_alerts_resolution_check',
	        'alert_deliveries_outcome_check')),
	    (SELECT count(*) FROM pg_indexes WHERE indexname IN
	       ('operational_samples_metric_time_idx',
	        'operational_alerts_open_dedupe_key',
	        'operational_alerts_state_time_idx',
	        'alert_deliveries_alert_attempt_key'))`).
	  Scan(&tables,&constraints,&indexes); err != nil { t.Fatal(err) }
	if tables != 3 || constraints != 8 || indexes != 4 {
		t.Fatalf("tables=%d constraints=%d indexes=%d", tables,constraints,indexes)
	}
}
```

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/database \
  -run '^TestOperationsMonitoringMigrationContracts$' -count=1
```

Expected: FAIL because migration 21 is absent.

- [ ] **Step 3: Add migration 21**

Implement:

```sql
-- +goose Up
CREATE TABLE operational_samples (
  source text NOT NULL,
  metric_name text NOT NULL,
  scope text NOT NULL,
  value double precision NOT NULL,
  unit text NOT NULL,
  observed_at timestamptz NOT NULL,
  window_started_at timestamptz,
  CONSTRAINT operational_samples_source_check CHECK (
    source IN ('app','postgres','redis','object_store','worker','host')),
  CONSTRAINT operational_samples_value_check CHECK (
    value = value AND
    value > '-Infinity'::float8 AND
    value < 'Infinity'::float8 AND
    char_length(metric_name) BETWEEN 1 AND 64
    AND char_length(scope) BETWEEN 1 AND 32
    AND char_length(unit) BETWEEN 1 AND 24)
);
CREATE INDEX operational_samples_metric_time_idx
  ON operational_samples(metric_name,scope,observed_at DESC);

CREATE TABLE operational_alerts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  dedupe_key text NOT NULL CHECK (char_length(dedupe_key) BETWEEN 1 AND 128),
  category text NOT NULL CHECK (char_length(category) BETWEEN 1 AND 64),
  severity text NOT NULL,
  state text NOT NULL DEFAULT 'open',
  first_observed_at timestamptz NOT NULL,
  last_observed_at timestamptz NOT NULL,
  acknowledged_by uuid REFERENCES users(id) ON DELETE SET NULL,
  acknowledged_at timestamptz,
  resolved_at timestamptz,
  current_value double precision NOT NULL,
  threshold_value double precision NOT NULL,
  summary text NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 240),
  trace_id text NOT NULL DEFAULT '',
  consecutive_failures integer NOT NULL DEFAULT 1 CHECK (consecutive_failures >= 0),
  consecutive_successes integer NOT NULL DEFAULT 0 CHECK (consecutive_successes >= 0),
  version bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  CONSTRAINT operational_alerts_severity_check
    CHECK (severity IN ('warning','critical')),
  CONSTRAINT operational_alerts_state_check
    CHECK (state IN ('open','acknowledged','resolved')),
  CONSTRAINT operational_alerts_value_check CHECK (
    current_value = current_value
    AND current_value > '-Infinity'::float8
    AND current_value < 'Infinity'::float8
    AND threshold_value = threshold_value
    AND threshold_value > '-Infinity'::float8
    AND threshold_value < 'Infinity'::float8),
  CONSTRAINT operational_alerts_acknowledgement_check CHECK (
    (acknowledged_by IS NULL) = (acknowledged_at IS NULL)
    AND (state <> 'open' OR acknowledged_at IS NULL)
    AND (state <> 'acknowledged' OR acknowledged_at IS NOT NULL)),
  CONSTRAINT operational_alerts_resolution_check CHECK (
    (state = 'resolved') = (resolved_at IS NOT NULL))
);
CREATE UNIQUE INDEX operational_alerts_open_dedupe_key
  ON operational_alerts(dedupe_key) WHERE state<>'resolved';
CREATE INDEX operational_alerts_state_time_idx
  ON operational_alerts(state,last_observed_at DESC,id DESC);

CREATE TABLE alert_deliveries (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  alert_id uuid NOT NULL REFERENCES operational_alerts(id) ON DELETE CASCADE,
  attempt integer NOT NULL CHECK (attempt BETWEEN 1 AND 4),
  destination text NOT NULL CHECK (destination='webhook'),
  outcome text NOT NULL,
  http_status_class integer,
  error_category text NOT NULL DEFAULT '',
  started_at timestamptz NOT NULL,
  finished_at timestamptz NOT NULL,
  CONSTRAINT alert_deliveries_outcome_check CHECK (
    outcome IN ('succeeded','failed','cancelled'))
);
CREATE UNIQUE INDEX alert_deliveries_alert_attempt_key
  ON alert_deliveries(alert_id,attempt,destination);

-- +goose Down
DROP TABLE alert_deliveries;
DROP TABLE operational_alerts;
DROP TABLE operational_samples;
```

- [ ] **Step 4: Add rejection and down tests**

Prove non-finite values, unknown state, duplicate unresolved dedupe keys,
acknowledgement shapes, and invalid delivery attempts fail. Migrate down to 20
and prove backup tables remain.

- [ ] **Step 5: Run and verify GREEN**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/database \
  -run 'OperationsMonitoring' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add db/migrations/00021_operations_monitoring.sql \
  internal/platform/database/operations_monitoring_migration_test.go
git commit -m "feat(operations): add samples and alert schema"
```

### Task 2: Collect bounded dashboard metrics

- [ ] **Step 1: Write failing collector and dashboard tests**

Use clock-controlled tests for healthy, degraded, unavailable, stale, timeout,
and empty data. PostgreSQL tests insert minimal users, Q&A, AI runs, processing
jobs, audit events, and backup runs and verify exact aggregate counts without
returning business content.

Define:

```go
type Dashboard struct {
	ObservedAt time.Time `json:"observedAt"`
	Students StudentSummary `json:"students"`
	Questions QuestionSummary `json:"questions"`
	AI AISummary `json:"ai"`
	Storage StorageSummary `json:"storage"`
	Services []ServiceHealth `json:"services"`
	Queues []QueueSummary `json:"queues"`
	Backup BackupSummary `json:"backup"`
	Alerts AlertSummary `json:"alerts"`
	RecentAudit []AuditSummary `json:"recentAudit"`
}

type Collector interface {
	Collect(context.Context, time.Time) ([]Sample, error)
}
```

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/operations \
  -run 'Collector|Dashboard' -count=1
```

Expected: FAIL because collectors are absent.

- [ ] **Step 3: Implement sample validation and storage**

Use fixed metric and scope allowlists. Reject user IDs, UUID-like labels,
slashes, whitespace, and arbitrary label maps. Insert samples in batches of at
most 100. Clean expired samples in batches of 1,000 using the configured
retention.

- [ ] **Step 4: Implement bounded collectors**

Each dependency collector has a two-second timeout. SQL uses aggregate queries
and returns only counts, rates, durations, bytes, and safe service state.
Unknown/unavailable is explicit and never replaced with stale healthy data.

Recent audit uses the safe DTO from the foundation plan and returns at most ten
items.

- [ ] **Step 5: Add dashboard API**

Mount:

```go
r.Get("/dashboard", h.dashboard)
```

It is admin-only, No-Store, and returns one bounded DTO. Use request
single-flight so concurrent dashboard requests do not multiply dependency
probes.

- [ ] **Step 6: Run and commit**

```bash
GOENV=off GOFLAGS='' go test ./internal/operations ./internal/app \
  ./cmd/server -run 'Collector|Dashboard' -count=1
git add internal/operations internal/app cmd/server
git commit -m "feat(operations): aggregate the teacher dashboard"
```

### Task 3: Add the private metrics and signed host-ingestion listener

- [ ] **Step 1: Write failing config and internal HTTP tests**

Add tests for:

- `HAPPYLEARN_INTERNAL_LISTEN`, default `:9090`;
- `HAPPYLEARN_METRICS_BEARER_SECRET_FILE` and
  `HAPPYLEARN_HOST_METRICS_HMAC_SECRET_FILE`;
- `HAPPYLEARN_WEBHOOK_URL_SECRET_FILE` and
  `HAPPYLEARN_WEBHOOK_AUTHORIZATION_SECRET_FILE`;
- production rejection of missing, symlinked, group/world-writable, empty, or
  oversized secret files;
- missing/wrong bearer returns 404, not authentication details;
- HMAC timestamp ±90 seconds, nonce replay, canonical JSON, unknown metric,
  UUID-like label, oversized body, and public route denial.

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/config \
  ./internal/operations ./cmd/server -run 'Internal|Metrics|HostSample' -count=1
```

Expected: FAIL because the internal listener is absent.

- [ ] **Step 3: Extend configuration**

Add:

```go
InternalListenAddress string
MetricsBearerSecret string
HostMetricsHMACSecret []byte
WebhookURL string
WebhookAuthorization string
```

Create `internal/platform/secretfile` with one owner-safe descriptor-based
reader that rejects symlinks and files larger than 8 KiB, trims one trailing
newline, and never includes content or the sensitive path in errors. Reuse it
to populate all four fields above. In Phase 5, `config.Load` accepts these
secrets only through the four documented `_FILE` variables; it never retains
or exposes their source paths.

- [ ] **Step 4: Implement metrics exposition**

Render fixed Prometheus text names such as:

```text
happylearn_service_up{service="postgres"} 1
happylearn_queue_items{queue="processing"} 7
happylearn_backup_age_seconds{repository="local"} 3600
happylearn_ai_requests_total{status="succeeded"} 12
```

Sort metric families and labels for deterministic output. Reject dynamic labels
outside fixed service/queue/state/repository sets. Set
`Content-Type: text/plain; version=0.0.4; charset=utf-8` and `Cache-Control:
no-store`.

- [ ] **Step 5: Implement host sample authentication**

Require:

```text
X-HL-Timestamp: Unix seconds
X-HL-Nonce: 32 lowercase hex
X-HL-Signature: sha256=<64 lowercase hex>
```

The signature is HMAC-SHA256 over
`timestamp + "\n" + nonce + "\n" + rawBody`. Store the nonce with Redis
`SET NX EX 120`; reject a replay before inserting samples.

- [ ] **Step 6: Run two graceful servers**

Refactor `cmd/server/main.go` so public and internal `http.Server` instances
start together, share shutdown cancellation, and cause process exit when either
unexpectedly stops. The internal listener is never registered in `app.New`.

- [ ] **Step 7: Verify and commit**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/config \
  ./internal/operations ./cmd/server -run 'Internal|Metrics|HostSample' -count=1
git add internal/platform/config internal/operations cmd/server
git commit -m "feat(operations): expose protected operational metrics"
```

### Task 4: Build the host sampler without a container socket mount

- [ ] **Step 1: Write failing command and shell contracts**

Prove the sampler accepts only allowlisted service rows, parses bounded
`docker compose ps --format json` and `docker stats --no-stream` output, reads
filesystem capacity, canonicalizes JSON, signs it, and never submits command
lines, environment, mounts, image registry auth, container logs, or arbitrary
names.

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./cmd/host-sampler -count=1
bash scripts/host-metrics_contract_test.sh
```

Expected: FAIL because the command and script are absent.

- [ ] **Step 3: Implement the Go sampler**

The command reads JSON records on stdin and emits one canonical payload:

```go
type HostPayload struct {
	SchemaVersion int `json:"schemaVersion"`
	ObservedAt time.Time `json:"observedAt"`
	Services []HostServiceSample `json:"services"`
	Filesystems []FilesystemSample `json:"filesystems"`
}
```

Allow services `caddy`, `app`, `worker`, `postgres`, `redis`, `minio`.
Reject negative values, percentages over 100, duplicates, more than 16 rows,
or payloads over 64 KiB.

- [ ] **Step 4: Implement `collect-host-metrics.sh`**

The host script uses exact production/development Compose files, bounded
timeouts, fixed service allowlist, owner-only HMAC secret file, and loopback
internal endpoint. It pipes only selected JSON fields to the Go command. It
must not use `docker inspect`, `env`, `printenv`, `set -x`, or a broad process
listing.

- [ ] **Step 5: Run mutation and live tests**

Mutate service names, add environment/mount fields, replay nonce, skew time,
truncate JSON, and break the internal endpoint. All fail closed without leaking
the secret. A live run inserts exactly the expected samples.

- [ ] **Step 6: Commit**

```bash
git add cmd/host-sampler scripts/collect-host-metrics.sh \
  scripts/host-metrics_contract_test.sh Makefile package.json
git commit -m "feat(operations): collect safe host metrics"
```

### Task 5: Evaluate, acknowledge, and resolve alerts

- [ ] **Step 1: Write failing alert engine tests**

Cover every default rule from the specification, two consecutive failures to
open, critical upgrade, acknowledgement without resolution, three consecutive
successes to resolve, changed thresholds affecting future evaluations, one
unresolved row per dedupe key, lease takeover, and delivery idempotency.

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/operations \
  -run 'Alert' -count=1
```

Expected: FAIL because the evaluator is absent.

- [ ] **Step 3: Implement alert storage and evaluator**

Use:

```go
type Rule struct {
	DedupeKey, Category, Summary string
	Warning, Critical float64
	Direction Direction
	MinimumSamples int
}

type Evaluation struct {
	Rule Rule
	Value float64
	ObservedAt time.Time
	Available bool
}
```

Evaluate in one transaction per dedupe key with `SELECT ... FOR UPDATE`.
Acknowledgement stores actor/time and audits atomically. Only healthy
evaluations resolve.

- [ ] **Step 4: Implement alert routes**

Mount:

```go
r.Get("/alerts", h.listAlerts)
r.Post("/alerts/{id}/acknowledge", h.acknowledgeAlert)
```

Use stable keyset pagination, exact state/severity/category filters, canonical
UUIDs, 409 `alert_already_resolved`, and admin-only role.

- [ ] **Step 5: Start the evaluator**

Add a one-minute runner with a PostgreSQL lease. It evaluates the latest
samples and dependency status, records alerts, queues webhook deliveries, and
shuts down before the database pool.

- [ ] **Step 6: Verify and commit**

```bash
GOENV=off GOFLAGS='' go test -race ./internal/operations ./cmd/server \
  -run 'Alert' -count=1
git add internal/operations cmd/server
git commit -m "feat(operations): evaluate durable alerts"
```

### Task 6: Deliver optional webhooks with pinned safe transport

- [ ] **Step 1: Preserve Phase 4 behavior with extraction tests**

Copy the existing AI URL-policy and safe-transport test vectors into a new
platform package test before moving code. Add webhook-specific tests for HTTPS,
private/metadata ranges, DNS rebinding, redirects, response limits, timeouts,
headers, and payload redaction.

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/safehttp \
  ./internal/operations ./internal/aiqa -run 'Safe|Webhook|URLPolicy' -count=1
```

Expected: FAIL because `safehttp` is absent.

- [ ] **Step 3: Extract the transport core**

Move address classification, resolved-address validation, per-dial DNS pinning,
redirect rejection, TLS policy, and response-size bounding to
`internal/platform/safehttp`. Keep `internal/aiqa` wrapper types and public
behavior unchanged. Do not duplicate the special-range list.

- [ ] **Step 4: Implement webhook delivery**

Read URL and optional authorization from owner-only files. Payload:

```go
type WebhookPayload struct {
	SchemaVersion int `json:"schemaVersion"`
	AlertID string `json:"alertId"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	State string `json:"state"`
	Summary string `json:"summary"`
	FirstObservedAt time.Time `json:"firstObservedAt"`
	LastObservedAt time.Time `json:"lastObservedAt"`
	CurrentValue float64 `json:"currentValue"`
	Threshold float64 `json:"threshold"`
	DashboardPath string `json:"dashboardPath"`
}
```

Send at attempt delays 1, 5, and 30 minutes. Limit request to 8 KiB and response
to 16 KiB. Persist only status class and safe category.

- [ ] **Step 5: Run full AI and webhook regression**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/safehttp \
  ./internal/operations ./internal/aiqa -count=1
GOENV=off GOFLAGS='' go test -race ./internal/platform/safehttp \
  ./internal/operations ./internal/aiqa -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/safehttp internal/aiqa internal/operations
git commit -m "feat(operations): deliver safe alert webhooks"
```

### Task 7: Build the dashboard and alert center

- [ ] **Step 1: Write failing frontend tests**

Cover dashboard healthy/degraded/stale/error states, 60-second visible polling,
hidden-page pause and immediate resume, alert priority, safe recent audit,
backup/restore summary, mobile order, text status, alert filters, stable append,
acknowledgement, live resolution, retry focus, and abort.

- [ ] **Step 2: Run and verify RED**

```bash
pnpm --dir web test -- \
  src/features/home/AdminHomeView.test.ts \
  src/features/operations/AlertsView.test.ts
```

Expected: FAIL because the Phase 5 dashboard and alert view are absent.

- [ ] **Step 3: Add typed clients**

```ts
export const readDashboard = (signal?: AbortSignal) =>
  request<OperationsDashboard>('/admin/operations/dashboard', { signal })

export const listAlerts = (filters: AlertFilters, signal?: AbortSignal) =>
  requestWithMeta<OperationalAlert[]>('/admin/operations/alerts?' +
    alertQuery(filters), { signal })

export const acknowledgeAlert = (id: string) =>
  request<OperationalAlert>(
    `/admin/operations/alerts/${encodeURIComponent(id)}/acknowledge`,
    { method: 'POST', json: {} },
  )
```

- [ ] **Step 4: Replace the current empty admin home**

Implement cards and panels from the approved mockup. Display observation time
and explicit unknown state. Put alerts, backup, and service health before
summary cards on mobile. Link to `/admin/alerts`, `/admin/backups`, and
`/admin/audit`.

- [ ] **Step 5: Implement alert center and routes**

Add `/admin/alerts`, filter controls, detail timeline, and acknowledgement
confirmation. Never expose webhook URL or delivery body.

- [ ] **Step 6: Run frontend gates and commit**

```bash
pnpm --dir web test
pnpm --dir web typecheck
pnpm lint
pnpm --dir web build
git add web/src/features/home web/src/features/operations web/src/router \
  web/src/layouts
git commit -m "feat(operations): add health dashboard and alerts"
```

### Task 8: Harden logging and metadata retention

- [ ] **Step 1: Write failing safe-log tests**

Create `internal/platform/safelog/logger_test.go` for:

```go
func TestJSONLoggerEmitsAllowlistedFields(t *testing.T)
func TestJSONLoggerRejectsSecretLikeFieldNames(t *testing.T)
func TestJSONLoggerRedactsSecretLikeValues(t *testing.T)
func TestJSONLoggerOmitsURLQueryCookiesAndAuthorization(t *testing.T)
func TestJSONLoggerBoundsStringsAndNestedValues(t *testing.T)
```

Use this API:

```go
type Field struct {
	Name string
	Value any
}

func New(output io.Writer, clock func() time.Time, secretValues ...string) (Logger, error)
func (l Logger) Info(event string, fields ...Field)
func (l Logger) Error(event string, fields ...Field)
```

Allow field names from a fixed registry: `trace_id`, `request_id`, `stage`,
`category`, `method`, `path`, `status`, `duration_ms`, `service`, `state`,
`count`, and `bytes`. Reject names containing `secret`, `token`, `password`,
`cookie`, `authorization`, `credential`, `key`, `url`, `query`, `body`, or
`content`. `New` rejects secret markers shorter than eight bytes, copies the
remaining markers into an unexported redactor, and replaces every exact marker
occurrence before JSON encoding. Accept only string, integer, boolean, and
`time.Duration` field values; cap every rendered string at 240 bytes.

- [ ] **Step 2: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/platform/safelog -count=1
```

Expected: FAIL because the package is absent.

- [ ] **Step 3: Implement and wire structured production logging**

Implement one JSON object per line with timestamp, level, event, and validated
fields. Production server and worker startup, shutdown, request, operations,
backup, alert, webhook, and cleanup logs use `safelog`; tests reject any call
that passes a raw error, URL, header map, environment, database row, object key,
or provider body. Development keeps the same fields and may use readable
formatting only at the final sink.

HTTP logging receives the escaped path only, never `RequestURI`, `RawQuery`,
cookies, authorization, CSRF data, signed URLs, or request/response bodies.

- [ ] **Step 4: Write failing retention tests**

Create `internal/operations/retention_test.go` with a clock-controlled
PostgreSQL fixture. Prove:

```text
operational samples: configured 1–30 days, default 7
resolved alert and delivery metadata: 365 days
terminal backup and restore metadata: 365 days
open/acknowledged alerts: never removed
nonterminal backup/restore rows: never removed
audit rows: never deleted
batch size: at most 1000 rows per transaction
concurrent cleanup owner: one
cleanup summary: immutable audit event with counts only
```

Use advisory key `845103122` for the cleanup runner and stop when
`pg_try_advisory_lock` reports another owner.

- [ ] **Step 5: Run and verify RED**

```bash
GOENV=off GOFLAGS='' go test ./internal/operations ./internal/backup \
  -run 'Retention|SafeLog' -count=1
```

Expected: FAIL because alert and backup metadata cleanup is absent.

- [ ] **Step 6: Implement bounded retention**

Create:

```go
type RetentionResult struct {
	Samples int64
	AlertDeliveries int64
	Alerts int64
	RestoreVerifications int64
	BackupRuns int64
}

type RetentionRunner interface {
	RunOnce(context.Context, time.Time, int) (RetentionResult, error)
}
```

Delete children before parents with ordered `DELETE ... WHERE id IN (SELECT
... ORDER BY ... LIMIT $1 FOR UPDATE SKIP LOCKED)` statements. Run no more than
one 1,000-row batch per table per invocation. Write one count-only audit event
in the final transaction. Do not add an audit delete method.

- [ ] **Step 7: Enforce container log rotation**

Add `json-file` logging with `max-size: 10m` and `max-file: "5"` to every
Phase 5 long-running and one-shot service in `deploy/compose.dev.yml` and the
disposable Phase 5 harness. Extend shell contracts with a merged-Compose
assertion and a mutation case that removes each limit.

- [ ] **Step 8: Verify and commit**

```bash
GOENV=off GOFLAGS='' go test -race ./internal/platform/safelog \
  ./internal/operations ./internal/backup ./cmd/server ./cmd/worker -count=1
bash scripts/e2e-phase5_contract_test.sh
git add internal/platform/safelog internal/operations internal/backup \
  cmd/server cmd/worker deploy/compose.dev.yml scripts
git commit -m "feat(operations): harden logs and metadata retention"
```

### Task 9: Close the monitoring plan

- [ ] **Step 1: Run focused complete gates**

```bash
GOENV=off GOFLAGS='' go test -p 1 ./internal/operations \
  ./internal/platform/safehttp ./internal/platform/secretfile \
  ./internal/platform/safelog ./internal/backup \
  ./internal/aiqa ./internal/platform/config \
  ./internal/app ./cmd/server ./cmd/host-sampler -count=1
GOENV=off GOFLAGS='' go test -race -p 1 ./internal/operations \
  ./internal/platform/safehttp ./internal/platform/safelog \
  ./internal/aiqa ./internal/backup ./internal/app \
  ./cmd/server ./cmd/host-sampler -count=1
pnpm test
pnpm typecheck
pnpm lint
pnpm build
bash scripts/host-metrics_contract_test.sh
git diff --check
```

Expected: all PASS.

- [ ] **Step 2: Review privacy and operations**

Confirm metrics labels are fixed, internal routes are not public, host payloads
have no Docker-sensitive fields, webhook URLs/authorization never persist,
acknowledgement cannot resolve, and Phase 4 SSRF behavior is unchanged.

- [ ] **Step 3: Record and commit**

Create `.superpowers/sdd/phase5-monitoring-alerts-report.md`, fix every Critical
or Important finding, rerun the gate, then:

```bash
git add .superpowers/sdd/phase5-monitoring-alerts-report.md
git commit -m "test(operations): close monitoring and alerts gate"
```
