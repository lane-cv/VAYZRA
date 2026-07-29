# Alert Webhook C0 Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the C0/I6/M0 findings on durable alert webhook delivery while preserving all public and lifecycle contracts.

**Architecture:** Add reversible migration 25 for relational integrity and bounded claim indexes, fence completion with authoritative lease time, isolate legacy reads, and make the exported sender constructor incapable of bypassing `safehttp`. Use the production claim SQL itself in real PostgreSQL plan tests.

**Tech Stack:** Go 1.26, pgx v5, PostgreSQL 18, goose SQL migrations, `safehttp`, `net/http`, Go race detector.

---

### Task 1: Lease and legacy regression tests

**Files:**
- Modify: `internal/operations/postgres_webhook_delivery_test.go`
- Modify: `internal/operations/postgres_alerts.go`
- Modify: `internal/operations/postgres_webhook_delivery.go`

- [ ] **Step 1: Write the failing stale-lease test**

Add table cases that claim real jobs and call `Complete` before expiry, exactly
at expiry, and after expiry. Query the attempt states to prove rejected
completion does not cancel later jobs. Retain the existing re-owner/old-token
case.

- [ ] **Step 2: Run the stale-lease test and verify RED**

Run:

```bash
go test ./internal/operations -run 'WebhookDeliveryCompletionRequiresLiveLease' -count=1
```

Expected: exact-expiry and expired cases incorrectly complete.

- [ ] **Step 3: Add authoritative lease fencing**

Add the following predicate to the same completion update:

```sql
AND claim_expires_at > $10
```

- [ ] **Step 4: Write and run the legacy coexistence RED**

Seed event attempts, insert a legacy row with the same alert/attempt/destination,
then replay it and submit a conflicting legacy record. Expected before the fix:
the replay query reads an event row or reports cardinality ambiguity.

- [ ] **Step 5: Isolate the legacy replay query and verify GREEN**

Add:

```sql
AND event_id IS NULL
```

Run both focused tests against real PostgreSQL.

### Task 2: Migration 25 relational and snapshot integrity

**Files:**
- Create: `db/migrations/00025_webhook_delivery_hardening.sql`
- Create: `internal/platform/database/webhook_delivery_hardening_migration_test.go`
- Modify: `internal/operations/postgres_webhook_outbox.go`
- Modify: `internal/operations/webhook_sender_test.go`

- [ ] **Step 1: Write migration and Go shape RED tests**

Cover version-24 mismatched delivery/event alerts and invalid transition
snapshots, migration repair, direct invalid inserts and updates, valid
acknowledged critical upgrade, composite FK rejection, successful claim, and
DownTo24. Add Go validation cases for every transition/state/severity rule.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/platform/database -run 'WebhookDeliveryHardeningMigration' -count=1
go test ./internal/operations -run 'WebhookTransitionSnapshot' -count=1
```

Expected: migration 25 is absent and invalid Go combinations are accepted.

- [ ] **Step 3: Add deterministic repair and constraints**

Migration Up normalizes version-24 rows, adds event `(id,alert_id)` uniqueness,
the delivery composite foreign key, and this snapshot rule:

```sql
(transition_kind='opened' AND state='open')
OR (transition_kind='upgraded'
    AND severity='critical'
    AND state IN ('open','acknowledged'))
OR (transition_kind='resolved' AND state='resolved')
```

Down restores the version-24 single-column foreign key.

- [ ] **Step 4: Mirror the snapshot rule in Go and verify GREEN**

Extend `validWebhookTransition` with the same transition-specific semantics and
run the focused migration, outbox, claim, payload, and sender tests.

### Task 3: Stable errors and private transport injection

**Files:**
- Modify: `internal/operations/webhook_sender.go`
- Modify: `internal/operations/webhook_sender_test.go`

- [ ] **Step 1: Write marker-bearing RED tests**

Use raw URL, hostname, IP, resolver-error, authorization, and timeout markers.
Assert `errors.Is(err, ErrInvalid)` and that none occurs in `err.Error()`.
Reflect over `WebhookSenderConfig` and assert no `Doer` field exists.

- [ ] **Step 2: Run focused sender tests and verify RED**

Run:

```bash
go test ./internal/operations -run 'WebhookSenderConfigurationErrorsAreStable|WebhookSenderConfigCannotInjectTransport' -count=1
```

Expected: wrapped normalize/resolver details leak and exported `Doer` exists.

- [ ] **Step 3: Privatize the test seam and stabilize failures**

Use:

```go
type webhookHTTPDoer interface {
    Do(*http.Request) (*http.Response, error)
}

func newWebhookSenderWithDoer(
    ctx context.Context,
    config WebhookSenderConfig,
    doer webhookHTTPDoer,
) (*WebhookSender, error)
```

The exported constructor calls the private constructor with no doer; every
configuration-stage failure returns `ErrInvalid`.

- [ ] **Step 4: Update package tests and verify all sender vectors GREEN**

Use the private constructor only from package tests. Re-run payload, SSRF,
redirect, rebinding, timeout, and size-limit tests.

### Task 4: Bounded actual claim plan

**Files:**
- Modify: `db/migrations/00025_webhook_delivery_hardening.sql`
- Modify: `internal/operations/postgres_webhook_delivery.go`
- Modify: `internal/operations/postgres_webhook_delivery_test.go`
- Modify: `internal/platform/database/webhook_delivery_hardening_migration_test.go`

- [ ] **Step 1: Extract actual SQL and write the 12k-row RED plan test**

Move the complete claim statement to `claimWebhookDeliverySQL`. Execute
`EXPLAIN (ANALYZE, BUFFERS)` plus that constant with production parameters in
a rollback transaction. Assert the effective-due index is used and reject a
delivery sequential scan or an 11,999-row filter.

- [ ] **Step 2: Run the plan test and verify RED**

Run:

```bash
go test ./internal/operations -run 'WebhookDeliveryClaimPlanStaysBounded' -count=1
```

Expected: the version-24 due index cannot support the claimed-row expiry OR
path and the plan filters historical rows.

- [ ] **Step 3: Add the effective-due and event lookup indexes**

Create:

```sql
CREATE INDEX alert_deliveries_effective_due_claim_idx
ON alert_deliveries (
  (CASE
    WHEN delivery_state='pending' THEN scheduled_at
    WHEN delivery_state='claimed' THEN claim_expires_at
  END),
  event_id,attempt,id
)
WHERE event_id IS NOT NULL
  AND delivery_state IN ('pending','claimed');
```

Also create event-scoped partial indexes for active claims and succeeded events,
and drop the version-24 due index. Down performs the inverse.

- [ ] **Step 4: Make the query match the index and verify GREEN**

Use the identical `CASE` expression in `WHERE` and `ORDER BY`, retaining
`FOR UPDATE OF delivery SKIP LOCKED`. Run the plan, claim competition, lease,
retry, and migration tests.

### Task 5: Version bump and final gates

**Files:**
- Modify: `cmd/backup/restore_check_test.go`
- Verify all files changed above

- [ ] **Step 1: Update embedded latest migration expectations to 25**

Use current/old/future examples `25/24/26`.

- [ ] **Step 2: Run affected ordinary tests**

Run the database-connected affected packages serially:

```bash
go test -p=1 ./internal/platform/database ./internal/operations ./internal/app ./cmd/server ./cmd/backup -count=1
```

- [ ] **Step 3: Run affected race tests**

```bash
go test -race -p=1 ./internal/platform/database ./internal/operations ./internal/app ./cmd/server ./cmd/backup -count=1
```

- [ ] **Step 4: Run static and formatting gates**

```bash
go vet ./...
gofmt -d cmd/backup/restore_check_test.go \
  internal/operations/postgres_alerts.go \
  internal/operations/postgres_webhook_delivery.go \
  internal/operations/postgres_webhook_delivery_test.go \
  internal/operations/postgres_webhook_outbox.go \
  internal/operations/webhook_sender.go \
  internal/operations/webhook_sender_test.go \
  internal/platform/database/webhook_delivery_hardening_migration_test.go
git diff --check
```

- [ ] **Step 5: Commit only the hardening scope**

```bash
git add cmd/backup/restore_check_test.go \
  db/migrations/00025_webhook_delivery_hardening.sql \
  docs/superpowers/specs/2026-07-30-alert-webhook-c0-hardening-design.md \
  docs/superpowers/plans/2026-07-30-alert-webhook-c0-hardening.md \
  internal/operations/postgres_alerts.go \
  internal/operations/postgres_webhook_delivery.go \
  internal/operations/postgres_webhook_delivery_test.go \
  internal/operations/postgres_webhook_outbox.go \
  internal/operations/webhook_sender.go \
  internal/operations/webhook_sender_test.go \
  internal/platform/database/webhook_delivery_hardening_migration_test.go
git commit -m "fix(operations): harden durable alert webhooks"
```
