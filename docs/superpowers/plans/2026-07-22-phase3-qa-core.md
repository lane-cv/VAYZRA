# HappyLearn Phase 3 QA Core Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver private one-question-per-thread teacher Q&A with immutable messages, SQL-level student isolation, transactional audit, idempotent writes, teacher workflow states, and private notes.

**Architecture:** A new `internal/qanda` module owns domain validation, state transitions, HTTP contracts, and PostgreSQL queries. Write operations run through one PostgreSQL transaction containing the thread/message/note mutation, audit event, and notification-writer call; the notification implementation is injected and may be a transaction-local no-op until the next plan wires durable notifications.

**Tech Stack:** Go 1.26.5, chi 5, pgx 5, PostgreSQL 18.4, UUID, existing auth/audit/httpx packages.

## Global Constraints

- One teacher administrator and at most 50 teacher-created students.
- A student may read only their own Q&A records; non-existent and unauthorized UUIDs both return `404 not_found`.
- Messages and teacher notes are append-only and cannot be updated or deleted.
- Message bodies are plain text, 1–20,000 Unicode code points; titles are 1–160 code points.
- All write endpoints require strict JSON, CSRF/Origin protection, request IDs, and an `Idempotency-Key` where specified.
- Never log or audit title/body/note text, attachment names, object keys, tokens, or other students' identifiers.
- Use TDD, parameterized SQL, stable keyset cursors, bounded request bodies, and focused commits.

---

### Task 1: Add the Q&A Schema and Immutable History Contract

**Files:**
- Create: `db/migrations/00009_teacher_qa.sql`
- Modify: `internal/platform/database/migrate_test.go`
- Create: `internal/platform/database/qa_migration_test.go`
- Create: `tests/integration/qa_schema_test.go`

**Interfaces:**
- Produces tables `qa_threads`, `qa_messages`, `qa_message_files`, and `teacher_notes`.
- Produces PostgreSQL enum-by-check values `pending`, `in_progress`, `waiting_student`, and `completed`.
- Produces immutable triggers for messages, message-file bindings, and teacher notes.

- [ ] **Step 1: Write the failing migration gate**

Add a test that runs migrations and asserts all four tables, the composite message idempotency key, the thread list indexes, and immutable triggers exist:

```go
func TestQASchemaAndHistoryAreDatabaseEnforced(t *testing.T) {
    pool := integration.StartPostgres(t)
    if err := database.Migrate(context.Background(), pool); err != nil { t.Fatal(err) }
    var tables, triggers int
    if err := pool.QueryRow(context.Background(), `
      SELECT count(*) FROM information_schema.tables
      WHERE table_schema='public' AND table_name IN
        ('qa_threads','qa_messages','qa_message_files','teacher_notes')`).Scan(&tables); err != nil { t.Fatal(err) }
    if err := pool.QueryRow(context.Background(), `
      SELECT count(*) FROM pg_trigger
      WHERE NOT tgisinternal AND tgname IN
        ('qa_messages_immutable','qa_message_files_immutable','teacher_notes_immutable')`).Scan(&triggers); err != nil { t.Fatal(err) }
    if tables != 4 || triggers != 3 { t.Fatalf("tables=%d triggers=%d", tables, triggers) }
}
```

- [ ] **Step 2: Run the migration test to verify RED**

Run: `go test ./internal/platform/database ./tests/integration -run 'QASchema|QAMigration' -count=1`  
Expected: FAIL because migration `00009_teacher_qa.sql` and the tables do not exist.

- [ ] **Step 3: Add the exact schema and constraints**

Create the migration with these essential definitions and matching down migration:

```sql
-- +goose Up
CREATE TABLE qa_threads (
  id uuid PRIMARY KEY,
  student_id uuid NOT NULL REFERENCES users(id),
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  status text NOT NULL CHECK (status IN ('pending','in_progress','waiting_student','completed')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  last_message_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CHECK ((status='completed') = (completed_at IS NOT NULL))
);
CREATE TABLE qa_messages (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES qa_threads(id),
  sender_user_id uuid NOT NULL REFERENCES users(id),
  sender_role text NOT NULL CHECK (sender_role IN ('admin','student')),
  body_text text NOT NULL CHECK (char_length(btrim(body_text)) BETWEEN 1 AND 20000),
  idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 16 AND 128),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(sender_user_id,idempotency_key)
);
CREATE TABLE qa_message_files (
  message_id uuid NOT NULL REFERENCES qa_messages(id),
  file_version_id uuid NOT NULL REFERENCES file_versions(id),
  sort_position smallint NOT NULL CHECK (sort_position BETWEEN 0 AND 19),
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 255),
  PRIMARY KEY(message_id,file_version_id),
  UNIQUE(message_id,sort_position)
);
CREATE TABLE teacher_notes (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES qa_threads(id),
  author_user_id uuid NOT NULL REFERENCES users(id),
  body_text text NOT NULL CHECK (char_length(btrim(body_text)) BETWEEN 1 AND 20000),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX qa_threads_student_activity_idx ON qa_threads(student_id,last_message_at DESC,id DESC);
CREATE INDEX qa_threads_teacher_queue_idx ON qa_threads(status,last_message_at DESC,id DESC);
CREATE INDEX qa_messages_thread_time_idx ON qa_messages(thread_id,created_at,id);
```

Use one `reject_qa_history_mutation()` trigger function on `BEFORE UPDATE OR DELETE` for the three append-only tables. The down migration drops indexes, triggers, function, and tables in reverse dependency order.

- [ ] **Step 4: Prove immutability and downgrade behavior**

Insert a teacher, student, thread, message, binding, and note. Assert `UPDATE` and `DELETE` fail for each append-only table, then run the provider down migration and assert all four tables are absent.

Run: `go test ./internal/platform/database ./tests/integration -run 'QA' -count=1`  
Expected: PASS.

- [ ] **Step 5: Run the database regression suite**

Run: `go test ./internal/platform/database ./tests/integration -count=1`  
Expected: PASS, including all Phase 1–2 migration and downgrade tests.

- [ ] **Step 6: Commit the schema contract**

```bash
git add db/migrations/00009_teacher_qa.sql internal/platform/database tests/integration/qa_schema_test.go
git commit -m "feat: add immutable teacher qa schema"
```

---

### Task 2: Implement Domain Validation, State Machine, and Student Threads

**Files:**
- Create: `internal/qanda/model.go`
- Create: `internal/qanda/store.go`
- Create: `internal/qanda/service.go`
- Create: `internal/qanda/service_test.go`
- Create: `internal/qanda/postgres_store.go`
- Create: `internal/qanda/postgres_store_test.go`

**Interfaces:**
- Produces `Service`, `Store`, `UnitOfWork`, `NotificationWriter`, and `Principal`.
- Produces `CreateThread`, `ListStudentThreads`, `GetStudentThread`, and `AddStudentMessage`.
- Notification calls use `Notify(context.Context, NotificationIntent) error`; the next plan supplies the durable implementation.

- [ ] **Step 1: Write RED table-driven state and validation tests**

Define and test these contracts:

```go
type Status string
const (
    StatusPending Status = "pending"
    StatusInProgress Status = "in_progress"
    StatusWaitingStudent Status = "waiting_student"
    StatusCompleted Status = "completed"
)
func NextStatus(current Status, action Action, actor auth.Role) (Status, error)
```

Test every permitted transition from the spec and assert all other combinations return `ErrInvalidStatusTransition`. Test normalized title/body bounds by Unicode code points, nil/disabled/wrong-role principals, UUID nil values, 16–128 character idempotency keys, and maximum page sizes.

- [ ] **Step 2: Run the domain tests to verify RED**

Run: `go test ./internal/qanda -run 'State|Validate|Create|Student' -count=1`  
Expected: FAIL because package `internal/qanda` does not exist.

- [ ] **Step 3: Define focused domain types and interfaces**

Use these public shapes:

```go
type Principal struct { User auth.User; RequestID string; IP net.IP }
type AttachmentInput struct { FileVersionID uuid.UUID; SortPosition int }
type CreateThreadInput struct { Title, Body, IdempotencyKey string; Attachments []AttachmentInput }
type AddMessageInput struct { ThreadID uuid.UUID; Body, IdempotencyKey string; Attachments []AttachmentInput }
type ThreadCursor struct { LastMessageAt time.Time; ID uuid.UUID; Limit int }
type MessageCursor struct { CreatedAt time.Time; ID uuid.UUID; Limit int }
type Thread struct { ID, StudentID uuid.UUID; Title string; Status Status; Version int64; LastMessageAt, CreatedAt, UpdatedAt time.Time; CompletedAt *time.Time }
type Message struct { ID, ThreadID, SenderUserID uuid.UUID; SenderRole auth.Role; Body string; CreatedAt time.Time; Attachments []Attachment }
type ThreadDetail struct { Thread Thread; Messages []Message }
type NotificationIntent struct { RecipientUserID uuid.UUID; Kind, Title, Summary, TargetType, TargetPath, DedupeKey string; TargetID uuid.UUID }
```

Keep `Store` read methods separate from `TxStore` writes. `UnitOfWork.WithinTx` receives `TxStore`, `audit.Writer`, and `NotificationWriter` from the same PostgreSQL transaction.

- [ ] **Step 4: Implement student operations with a fake transactional store**

`CreateThread` normalizes input, checks student role/status, calls `CreateThreadWithFirstMessage`, writes `qa.thread_created` audit metadata containing only `messageCount` and `attachmentCount`, and notifies the active admin. `AddStudentMessage` locks the owned thread, applies student-follow-up transition, appends once by `(sender,idempotency_key)`, increments version, and notifies the admin. Duplicate requests return the existing message and thread without another audit or notification.

- [ ] **Step 5: Verify service GREEN and rollback behavior**

Run: `go test ./internal/qanda -run 'Service|Student|Idempotent|Rollback' -count=1`  
Expected: PASS. Include a fake UOW that discards copied state when audit or notification returns an error.

- [ ] **Step 6: Implement PostgreSQL student isolation inside queries**

Queries must include the student predicate, not filter after loading:

```sql
SELECT id,student_id,title,status,version,last_message_at,created_at,updated_at,completed_at
FROM qa_threads
WHERE student_id=$1
  AND (last_message_at,id) < ($2,$3)
ORDER BY last_message_at DESC,id DESC LIMIT $4;
```

`GetStudentThread`, `ListStudentMessages`, and `LockStudentThread` also join `users` and require the student user to remain `status='active'`. Map zero rows and cross-student identifiers to `ErrNotFound`.

- [ ] **Step 7: Run PostgreSQL concurrency and isolation tests**

Test two students, duplicate idempotency keys scoped to different users, same-user duplicate keys, stable cursor traversal with equal timestamps, concurrent follow-ups, disabled-student denial, and transaction rollback when notification insertion fails.

Run: `go test ./internal/qanda -run Postgres -count=1`  
Expected: PASS with PostgreSQL available through `HAPPYLEARN_TEST_DATABASE_URL` or the documented default.

- [ ] **Step 8: Commit the student Q&A core**

```bash
git add internal/qanda
git commit -m "feat: add private student qa threads"
```

---

### Task 3: Add Strict Student HTTP Boundaries and Application Wiring

**Files:**
- Create: `internal/qanda/http_student.go`
- Create: `internal/qanda/http_common.go`
- Create: `internal/qanda/http_student_test.go`
- Modify: `internal/app/app.go`
- Create: `internal/app/qanda_routes_test.go`
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/wiring_test.go`

**Interfaces:**
- Produces student routes under `/api/v1/student/questions`.
- `StudentHTTPService` contains `CreateThread`, `ListStudentThreads`, `GetStudentThread`, `ListStudentMessages`, and `AddStudentMessage`.
- Produces `HTTPServices struct { Student StudentHTTPService; Admin AdminHTTPService }` so one PostgreSQL service/UOW can be wired into both role-specific handlers without exposing one role's methods through the other interface.

- [ ] **Step 1: Write RED HTTP contract tests**

Cover strict `Content-Type: application/json`, unknown fields, one JSON object only, 64 KiB body cap, exactly one `Idempotency-Key`, UUID parsing, status filter, cursor validation, limit `1..50`, role denial, disabled user, response `Cache-Control: no-store, private`, and request IDs on every error.

Assert a cross-student service `ErrNotFound` maps to the same body and status as a missing UUID:

```go
if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
    t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
}
```

- [ ] **Step 2: Run the HTTP tests to verify RED**

Run: `go test ./internal/qanda ./internal/app -run 'StudentHTTP|QARoutes' -count=1`  
Expected: FAIL because the handler and dependency wiring do not exist.

- [ ] **Step 3: Implement routes and stable DTOs**

Mount:

```go
r.Get("/", h.List)
r.Post("/", h.Create)
r.Get("/{id}", h.Get)
r.Get("/{id}/messages", h.ListMessages)
r.Post("/{id}/messages", h.AddMessage)
```

Return message attachment views without object keys. Encode cursors as base64url JSON with exact timestamp and UUID; reject padding, unknown cursor fields, zero UUIDs, future timestamps, and non-canonical re-encoding.

- [ ] **Step 4: Wire the service without weakening role checks**

Add `StudentQuestions qanda.StudentHTTPService` to `app.Dependencies`, mount it only inside authenticated routes, and keep `auth.RequireRole(auth.RoleStudent)` inside the handler. Add `newQuestions func(*pgxpool.Pool) qanda.HTTPServices` to server dependencies and create the production PostgreSQL service/UOW.

- [ ] **Step 5: Run focused and full route tests**

Run: `go test ./internal/qanda ./internal/app ./cmd/server -count=1`  
Expected: PASS, including nil optional dependency tests and role-boundary regressions.

- [ ] **Step 6: Commit the student HTTP boundary**

```bash
git add internal/qanda internal/app cmd/server
git commit -m "feat: expose student teacher-question APIs"
```

---

### Task 4: Add Teacher Queue, Replies, Statuses, and Private Notes

**Files:**
- Modify: `internal/qanda/model.go`
- Modify: `internal/qanda/store.go`
- Modify: `internal/qanda/service.go`
- Modify: `internal/qanda/service_test.go`
- Modify: `internal/qanda/postgres_store.go`
- Modify: `internal/qanda/postgres_store_test.go`
- Create: `internal/qanda/http_admin.go`
- Create: `internal/qanda/http_admin_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/qanda_routes_test.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Produces `ListAdminThreads`, `GetAdminThread`, `AddAdminMessage`, `ChangeStatus`, and `AddTeacherNote`.
- Produces admin routes under `/api/v1/admin/questions`.
- `AdminThreadDetail` contains notes; `StudentThreadDetail` has no notes field.

- [ ] **Step 1: Write RED teacher workflow tests**

Test filters by status/student/date, stable cursor pagination, admin-only access, default reply transition to `waiting_student`, explicit `in_progress`, completion timestamp, reopen to `in_progress`, version conflicts, duplicate replies, and append-only notes. Serialize a student detail response and assert neither `notes` nor a note-derived count exists.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/qanda -run 'Admin|Teacher|Status|Note' -count=1`  
Expected: FAIL because teacher methods and routes are missing.

- [ ] **Step 3: Implement teacher transaction methods**

`AddAdminMessage` locks by thread ID, checks the expected version, appends idempotently, transitions to `waiting_student`, notifies the owning student, and audits only counts and old/new status. `ChangeStatus` applies `NextStatus`, maintains `completed_at`, increments the version, and emits a status notification. `AddTeacherNote` appends the note and writes a body-free audit event; it does not create a student notification.

- [ ] **Step 4: Implement queue SQL and DTO separation**

Use parameterized optional filters and `(last_message_at,id)` keyset pagination. Fetch admin notes only through `GetAdminThread`; student store methods must not select the `teacher_notes` table. Add a compile-time DTO regression test so adding a `Notes` field to student output fails review/tests.

- [ ] **Step 5: Implement strict admin routes**

Mount:

```go
r.Get("/", h.List)
r.Get("/{id}", h.Get)
r.Post("/{id}/messages", h.AddMessage)
r.Post("/{id}/status", h.ChangeStatus)
r.Post("/{id}/notes", h.AddNote)
```

Replies require `Idempotency-Key`; state requests contain `{ "expectedVersion": 4, "status": "completed" }`; notes contain only `{ "body": "..." }`. Return `409 thread_conflict` on stale versions and `409 invalid_status_transition` on forbidden transitions.

- [ ] **Step 6: Run authorization, transaction, race, and audit tests**

Run:

```bash
go test ./internal/qanda ./internal/app ./cmd/server -count=1
go test -race ./internal/qanda ./internal/app -count=1
```

Expected: PASS. Confirm rollback on audit/notification failure and confirm no body/title/note/filename occurs in audit metadata or error responses.

- [ ] **Step 7: Commit the teacher workflow**

```bash
git add internal/qanda internal/app cmd/server
git commit -m "feat: add teacher qa workflow"
```

## QA Core Gate

Run:

```bash
go test ./internal/qanda ./internal/app ./cmd/server ./internal/platform/database ./tests/integration -count=1
go test -race ./internal/qanda ./internal/app -count=1
go vet ./internal/qanda/... ./internal/app/... ./cmd/server/...
git diff --check
```

Expected: every command exits 0; two students are isolated in SQL; messages and notes are immutable; idempotent retries do not duplicate writes; the repository contains only intended Phase 3 core changes.
