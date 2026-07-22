# HappyLearn Phase 3 QA Files and Notifications Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add purpose-bound Q&A uploads, immutable message attachments, controlled Q&A file delivery, durable notifications, polling APIs, and idempotent lesson-publication outbox consumption.

**Architecture:** Extend the existing file pipeline through injected upload policies rather than duplicating multipart code. A new `notifications` module provides a PostgreSQL writer that participates in Q&A transactions and a lease-based outbox runner for publication fan-out; API polling reads only recipient-scoped indexed rows.

**Tech Stack:** Go 1.26.5, PostgreSQL 18.4, pgx 5, MinIO AIStor, existing processing worker, chi 5, Vue-facing JSON APIs.

## Global Constraints

- Q&A uploads are owner- and purpose-bound; a completed teaching upload cannot be rebound as a Q&A attachment.
- Images are at most 20 MiB each and 10 per message; PDF 50 MiB; Office 30 MiB; TXT/Markdown 10 MiB; all attachments total at most 100 MiB per message.
- Only `ready` file versions may bind to messages; ZIP/archive, executable, macro Office, SVG/HTML, mismatch, rejected, and failed files remain forbidden.
- Message-file bindings are immutable and Q&A delivery rechecks current thread authorization on every request.
- Notifications never contain message/note bodies, attachment names, object keys, secrets, or unauthorized student identifiers.
- Outbox processing is leased, bounded, retryable, and idempotent across crashes and multiple application replicas.
- PostgreSQL is business truth; Redis downtime cannot lose uploads, bindings, or notifications.

---

### Task 1: Add Purpose-Bound Upload Policies Without Duplicating Multipart Logic

**Files:**
- Create: `db/migrations/00011_qa_file_purpose.sql`
- Modify: `internal/files/model.go`
- Modify: `internal/files/upload_service.go`
- Modify: `internal/files/upload_service_test.go`
- Modify: `internal/files/http_upload.go`
- Modify: `internal/files/http_upload_test.go`
- Modify: `internal/files/postgres_store.go`
- Modify: `internal/files/postgres_store_test.go`
- Create: `internal/files/upload_policy.go`
- Create: `internal/files/upload_policy_test.go`
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/wiring_test.go`

**Interfaces:**
- Produces `UploadPurposeTeaching` and `UploadPurposeQA`.
- Produces `UploadPolicy` with `Purpose() UploadPurpose`, `Authorize(auth.User) error`, and `Validate(CreateUploadInput) error`.
- Existing teaching upload behavior remains available through `TeachingUploadPolicy`; new student/admin Q&A routes use `QAUploadPolicy`.

- [ ] **Step 1: Write RED migration and policy tests**

Assert `upload_sessions.purpose` and `file_versions.purpose` are non-null, constrained to `teaching`/`qa_attachment`, and existing rows migrate as `teaching`. Test exact Q&A size/type policy boundaries and roles:

```go
tests := []struct{ role auth.Role; mime string; size int64; want error }{
  {auth.RoleStudent, "image/png", 20<<20, nil},
  {auth.RoleStudent, "image/png", (20<<20)+1, files.ErrFileTooLarge},
  {auth.RoleStudent, "application/pdf", 50<<20, nil},
  {auth.RoleStudent, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", 30<<20, nil},
  {auth.RoleStudent, "application/zip", 1024, files.ErrFileTypeRejected},
}
```

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/files ./internal/platform/database -run 'UploadPurpose|QAUploadPolicy' -count=1`
Expected: FAIL because purpose fields and policies are absent.

- [ ] **Step 3: Add purpose columns with safe Phase 2 compatibility**

Migration essentials:

```sql
ALTER TABLE upload_sessions ADD COLUMN purpose text NOT NULL DEFAULT 'teaching'
  CHECK (purpose IN ('teaching','qa_attachment'));
ALTER TABLE file_versions ADD COLUMN purpose text NOT NULL DEFAULT 'teaching'
  CHECK (purpose IN ('teaching','qa_attachment'));
ALTER TABLE upload_sessions ALTER COLUMN purpose DROP DEFAULT;
ALTER TABLE file_versions ALTER COLUMN purpose DROP DEFAULT;
CREATE INDEX file_versions_owner_purpose_state_idx
  ON file_versions(file_id,purpose,processing_state,id);
ALTER TABLE file_access_logs
  ADD COLUMN qa_message_id uuid REFERENCES qa_messages(id) ON DELETE RESTRICT,
  ADD CONSTRAINT file_access_logs_single_business_target CHECK (
    NOT (lesson_revision_id IS NOT NULL AND qa_message_id IS NOT NULL)
  );
CREATE UNIQUE INDEX file_access_logs_qa_playback_sample_key
  ON file_access_logs(actor_user_id,requested_file_version_id,qa_message_id,access_policy,playback_session_hash)
  WHERE result='allow' AND qa_message_id IS NOT NULL AND playback_session_hash<>'';
```

Down migration first rejects downgrade when a Q&A access log has `qa_message_id IS NOT NULL`, then removes the constraint, Q&A target column, index, and purpose columns without touching file bytes or prior metadata. This makes otherwise-unrepresentable audit history fail closed instead of silently discarding it.

- [ ] **Step 4: Inject a fixed policy into `UploadService`**

Change construction to:

```go
func NewUploadService(store UploadStore, objects objectstore.Store, policy UploadPolicy, now func() time.Time) *UploadService
```

`Create` calls `policy.Authorize(actor.User)` and `policy.Validate(in)` before creating a MinIO multipart object, stores `policy.Purpose()` in the session, and `FinishCompletion` copies the session purpose to `file_versions`. Status/part/complete/cancel remain owner-scoped and reject a service whose configured purpose does not match the stored session.

- [ ] **Step 5: Generalize the HTTP role gate explicitly**

Extend `UploadHTTPConfig` with `AllowedRoles []auth.Role`; build one middleware that rejects users not in the immutable configured set. Keep teaching at admin-only. Mount Q&A upload handlers under student and admin routes with `QAUploadPolicy` and their exact role.

- [ ] **Step 6: Run regression, boundary, and compensation tests**

Run:

```bash
go test ./internal/files ./internal/app ./cmd/server -count=1
go test -race ./internal/files -count=1
```

Expected: PASS. Existing teaching upload tests remain unchanged in outcome; denied Q&A inputs create no multipart object; database failures still abort or compensate the right object.

- [ ] **Step 7: Commit purpose-bound uploads**

```bash
git add db/migrations/00011_qa_file_purpose.sql internal/files cmd/server internal/app
git commit -m "feat: add purpose-bound qa uploads"
```

---

### Task 2: Bind Ready Attachments and Enforce Q&A File Delivery

**Files:**
- Modify: `internal/qanda/model.go`
- Modify: `internal/qanda/store.go`
- Modify: `internal/qanda/service.go`
- Modify: `internal/qanda/service_test.go`
- Modify: `internal/qanda/postgres_store.go`
- Modify: `internal/qanda/postgres_store_test.go`
- Create: `internal/files/qa_access.go`
- Create: `internal/files/qa_access_test.go`
- Create: `internal/files/http_qa_access.go`
- Create: `internal/files/http_qa_access_test.go`
- Modify: `internal/files/postgres_store.go`
- Modify: `tests/integration/files_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/qanda_routes_test.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Produces `QAAttachmentValidator.ValidateForMessage(ctx, actor, []AttachmentInput) ([]Attachment, error)`.
- Produces `QAAccessService.Open(ctx, Principal, QAOpenInput) (OpenedFile, error)`.
- Produces `/api/v1/question-files/{version}/status`, `/preview`, and `/download`.

- [ ] **Step 1: Write RED attachment validation tests**

Test ownership, `purpose='qa_attachment'`, `processing_state='ready'`, allowed detected type, maximum ten images, maximum twenty total attachment records, per-file bounds, aggregate 100 MiB, duplicate version IDs, sort-position uniqueness, and display-name normalization. Assert any invalid attachment rolls back the whole message transaction.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/qanda ./internal/files -run 'QAAttachment|QAAccess' -count=1`
Expected: FAIL because validation and access services are missing.

- [ ] **Step 3: Validate and bind while holding the message transaction**

Implement a single parameterized query that locks all requested `file_versions`, joins `files.created_by`, and returns purpose/state/type/size/display name. Compare the returned UUID set exactly with the requested set before inserting `qa_message_files`. Do not rely on client MIME, name, size, or status.

Map rejected conditions to `ErrAttachmentNotReady`, `ErrAttachmentLimit`, `ErrForbidden`, or `ErrInvalid` without exposing which foreign UUID exists.

- [ ] **Step 4: Resolve file access through thread authorization**

For students, resolve only when `qa_threads.student_id=$actor` and the user remains active. For admins, require the admin role. Both paths join `qa_message_files → qa_messages → qa_threads → file_versions`; neither accepts a thread ID from the browser. Select original or preview object internally and never return bucket/object keys in DTOs.

The `status` endpoint returns only `{fileVersionId,processingState,failureCategory,detectedMime,size,previewAvailable}` after proving the requester owns the unbound Q&A upload or can access a bound message. It never exposes `fileId`, creator IDs, checksums, object keys, buckets, processing commands, or another user's upload existence.

- [ ] **Step 5: Reuse bounded streaming and access-log behavior**

The Q&A access handler uses the existing Range parser and `OpenedFile` response writer. Preview prefers a ready preview for PDF/Office/image and may stream browser-playable video; download streams the original. Log allow/deny/failure with `file_access_logs.qa_message_id` and the normalized actor IP. Cross-student and random UUID requests return identical `404` responses.

- [ ] **Step 6: Run integration and authorization regression tests**

Run:

```bash
go test ./internal/qanda ./internal/files ./internal/app ./tests/integration -count=1
go test -race ./internal/qanda ./internal/files -count=1
```

Expected: PASS, including cross-student UUID guessing, disabled students, unbound ready files, teaching-purpose files, rejected files, Range delivery, and access-log privacy.

- [ ] **Step 7: Commit immutable Q&A attachments**

```bash
git add internal/qanda internal/files internal/app cmd/server tests/integration
git commit -m "feat: secure teacher qa attachments"
```

---

### Task 3: Add Durable Notification Storage and Recipient-Scoped APIs

**Files:**
- Create: `db/migrations/00012_notifications_outbox.sql`
- Create: `internal/notifications/model.go`
- Create: `internal/notifications/store.go`
- Create: `internal/notifications/service.go`
- Create: `internal/notifications/service_test.go`
- Create: `internal/notifications/postgres_store.go`
- Create: `internal/notifications/postgres_store_test.go`
- Create: `internal/notifications/http.go`
- Create: `internal/notifications/http_test.go`
- Modify: `internal/qanda/postgres_store.go`
- Modify: `internal/qanda/postgres_store_test.go`
- Modify: `internal/app/app.go`
- Create: `internal/app/notification_routes_test.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Produces transaction-compatible `notifications.Writer` implementing the Q&A `NotificationWriter` contract.
- Produces `List`, `UnreadCount`, `MarkRead`, and `MarkAllRead`.
- Produces routes `/api/v1/notifications`, `/unread-count`, `/{id}/read`, and `/read-all`.

- [ ] **Step 1: Write RED schema and service tests**

Test table constraints, recipient-scoped dedupe keys, unread partial index, immutable content fields, only `read_at` transitioning from null to a timestamp, list keysets, count, foreign-recipient mark-read returning `ErrNotFound`, and mark-all cutoff semantics.

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/notifications ./internal/platform/database -run 'Notification' -count=1`
Expected: FAIL because the module and migration do not exist.

- [ ] **Step 3: Add notification and outbox lease schema**

Use:

```sql
CREATE TABLE notifications (
  id uuid PRIMARY KEY,
  recipient_user_id uuid NOT NULL REFERENCES users(id),
  kind text NOT NULL CHECK (kind IN ('qa_created','qa_replied','qa_followed_up','qa_status_changed','lesson_published')),
  title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 160),
  summary text NOT NULL CHECK (char_length(summary) BETWEEN 1 AND 240),
  target_type text NOT NULL CHECK (target_type IN ('qa_thread','lesson')),
  target_id uuid NOT NULL,
  target_path text NOT NULL CHECK (target_path LIKE '/%'),
  dedupe_key text NOT NULL CHECK (char_length(dedupe_key) BETWEEN 16 AND 200),
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(recipient_user_id,dedupe_key)
);
CREATE INDEX notifications_recipient_time_idx ON notifications(recipient_user_id,created_at DESC,id DESC);
CREATE INDEX notifications_unread_idx ON notifications(recipient_user_id,created_at DESC,id DESC) WHERE read_at IS NULL;
ALTER TABLE outbox_events
  ADD COLUMN dedupe_key text,
  ADD COLUMN lease_owner text,
  ADD COLUMN lease_until timestamptz,
  ADD COLUMN attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN last_error_category text;
CREATE UNIQUE INDEX outbox_events_dedupe_key ON outbox_events(dedupe_key) WHERE dedupe_key IS NOT NULL;
```

Add constraints tying owner/until together and bounding attempts. Preserve existing Phase 2 outbox rows during up/down migration.

- [ ] **Step 4: Implement safe writer and recipient-scoped store**

`Writer.Notify` inserts `ON CONFLICT (recipient_user_id,dedupe_key) DO NOTHING`. Validate kind-specific target paths: student Q&A links start `/student/questions/`, admin Q&A links `/admin/questions/`, and lesson links `/student/learning/`. Store DTOs never contain another recipient ID.

- [ ] **Step 5: Wire Q&A transactions to the real writer**

In `qanda.PostgresUnitOfWork.WithinTx`, instantiate the Q&A transaction store, audit writer, and notification writer using the same `pgx.Tx`. Add integration tests that force notification insertion failure and prove thread/message/status/audit all roll back.

- [ ] **Step 6: Implement strict notification HTTP routes**

`GET /notifications` allows only `cursor` and `limit=1..100`; `GET /unread-count` returns `{data:{count:N}}`; mutation requests require exact empty JSON objects. `read-all` captures the server timestamp inside the transaction and updates only `recipient_user_id=$current AND created_at <= $cutoff`.

- [ ] **Step 7: Run module, route, and race tests**

Run:

```bash
go test ./internal/notifications ./internal/qanda ./internal/app ./cmd/server -count=1
go test -race ./internal/notifications ./internal/qanda -count=1
```

Expected: PASS with no cross-user counts or mutations and no duplicate Q&A notifications.

- [ ] **Step 8: Commit notification storage and APIs**

```bash
git add db/migrations/00012_notifications_outbox.sql internal/notifications internal/qanda internal/app cmd/server
git commit -m "feat: add private in-app notifications"
```

---

### Task 4: Consume Lesson Publication Outbox Reliably

**Files:**
- Create: `internal/notifications/outbox.go`
- Create: `internal/notifications/outbox_test.go`
- Create: `internal/notifications/postgres_outbox.go`
- Create: `internal/notifications/postgres_outbox_test.go`
- Create: `internal/notifications/runner.go`
- Create: `internal/notifications/runner_test.go`
- Modify: `internal/teaching/postgres_store.go`
- Modify: `internal/teaching/postgres_store_test.go`
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/wiring_test.go`
- Modify: `tests/integration/teaching_admin_test.go`

**Interfaces:**
- Produces `OutboxStore.Claim`, `DeliverLessonPublication`, `Complete`, and `Fail`.
- Produces `StartOutboxRunner(Runner) func()` with bounded shutdown.
- Publication payload is versioned and contains only `schemaVersion`, `lessonId`, and `revisionId`.

- [ ] **Step 1: Write RED lease, retry, audience, and crash-idempotency tests**

Cover exclusive `FOR UPDATE SKIP LOCKED` claims, lease expiry takeover, attempt increments, exponential retry capped at 15 minutes, malformed payload terminal failure, all-student and selected-student snapshots, disabled-student exclusion, withdrawn-current-revision safety, duplicate delivery, and crash after notifications insert but before outbox completion.

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/notifications ./tests/integration -run 'Outbox|LessonPublicationNotification' -count=1`
Expected: FAIL because runner/store methods are absent.

- [ ] **Step 3: Version the teaching event payload**

Publish exact JSON:

```go
payload := struct {
    SchemaVersion int       `json:"schemaVersion"`
    LessonID      uuid.UUID `json:"lessonId"`
    RevisionID    uuid.UUID `json:"revisionId"`
}{1, revision.LessonID, revision.ID}
```

Set deterministic `dedupe_key='lesson.published:'+revision.ID.String()` in the publication transaction.

- [ ] **Step 4: Implement lease-based batch processing**

Claim at most 50 ready events for 30 seconds. For each lesson event, use one transaction to lock the outbox row, recheck the lease owner, resolve the immutable audience snapshot, insert notifications with per-recipient dedupe keys, and mark `published_at`. On transient failure, clear the lease and set bounded `next_attempt_at`; on malformed/permanent failure, record a stable category and stop retrying after the documented maximum.

- [ ] **Step 5: Start and stop the runner in the application lifecycle**

Inject `startOutbox func(*pgxpool.Pool) func()` into server wiring. Start only after migrations and service construction succeed. Stop the runner before closing PostgreSQL. Poll with a cancelable timer (not `time.Sleep`), apply a 10-second per-batch timeout, and never log payloads or student IDs.

- [ ] **Step 6: Run integration, race, and shutdown tests**

Run:

```bash
go test ./internal/notifications ./internal/teaching ./cmd/server ./tests/integration -count=1
go test -race ./internal/notifications ./cmd/server -count=1
```

Expected: PASS; a process crash/retry produces exactly one notification per recipient; shutdown returns within the bound.

- [ ] **Step 7: Commit durable publication notifications**

```bash
git add internal/notifications internal/teaching cmd/server tests/integration
git commit -m "feat: deliver lesson publication notifications"
```

## Files and Notifications Gate

Run:

```bash
go test ./internal/files ./internal/qanda ./internal/notifications ./internal/teaching ./internal/app ./cmd/server ./internal/platform/database ./tests/integration -count=1
go test -race ./internal/files ./internal/qanda ./internal/notifications ./cmd/server -count=1
go vet ./internal/files/... ./internal/qanda/... ./internal/notifications/... ./cmd/server/...
git diff --check
```

Expected: every command exits 0; attachment ownership/purpose/status are database-authoritative; recipient queries are isolated; Q&A and publication notifications are transactionally durable and idempotent.
