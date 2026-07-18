# Phase 2 Catalog and Publishing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the fixed five-level teaching catalog, optimistic lesson drafts, immutable publication snapshots, per-lesson student audiences, authorized search, and lightweight learning state.

**Architecture:** Add a focused `internal/teaching` module following the existing service/store/HTTP pattern. PostgreSQL enforces hierarchy, revision immutability, audience integrity, and publication consistency; the Go service owns validation and audit semantics. File readiness is deliberately introduced by the next plan through a narrow publication-check interface.

**Tech Stack:** Go 1.26.5, chi v5, pgx v5, PostgreSQL 18.4 with `pg_trgm`, Vue-facing JSON REST APIs, existing opaque-session/CSRF/origin/audit infrastructure.

## Global Constraints

- Target Ubuntu 24.04 on 2 CPU cores and 4 GB RAM.
- Preserve Phase 1 authentication, `must_change_password`, CSRF, Origin, no-store, request-ID, trusted-proxy, and immutable-audit behavior.
- Catalog hierarchy is exactly grade → term → subject → chapter → lesson and begins empty.
- Published revisions are immutable; mutable auto-save state lives only in `lesson_drafts`.
- Audience mode is exactly `all` or `selected`; selected students are individual active student UUIDs.
- Unauthorized student reads return the same `404 not_found` response as missing resources.
- Use TDD, parameterized SQL, database constraints, stable error codes, and focused commits.
- Run database tests only against an isolated disposable PostgreSQL database.

---

## Locked File Structure

- `db/migrations/00004_teaching_catalog.sql`: catalog, drafts, revisions, audience, external-video, outbox, and progress schema.
- `internal/teaching/model.go`: domain enums, inputs, views, and sentinel errors.
- `internal/teaching/store.go`: store and transaction interfaces shared by services.
- `internal/teaching/postgres_store.go`: pgx implementation and authorized student queries.
- `internal/teaching/service.go`: catalog/draft/publication rules.
- `internal/teaching/student_service.go`: student browse/search/progress rules.
- `internal/teaching/http_admin.go`: teacher routes and strict JSON boundary.
- `internal/teaching/http_student.go`: student routes with non-enumerating errors.
- `internal/teaching/*_test.go`: unit and HTTP tests colocated with the module.
- `tests/integration/teaching_test.go`: PostgreSQL constraints, transactions, and authorization.
- `internal/app/app.go` and `cmd/server/main.go`: dependency wiring only.

### Task 1: Teaching schema and domain contracts

**Files:**
- Create: `db/migrations/00004_teaching_catalog.sql`
- Create: `internal/teaching/model.go`
- Create: `internal/teaching/store.go`
- Test: `internal/platform/database/migrate_test.go`
- Test: `tests/integration/teaching_test.go`

**Interfaces:**
- Consumes: existing `users(id, role, status, deleted_at)` and immutable `audit_logs`.
- Produces: `teaching.Draft`, `teaching.Revision`, `teaching.Audience`, `teaching.CatalogStore`, `teaching.UnitOfWork`, and the database tables used by every later Phase 2 task.

- [ ] **Step 1: Write failing migration and constraint tests**

Add tests that migrate an empty isolated database, create one complete five-level path, and assert these failures: term without grade, duplicate normalized sibling name, selected audience containing an admin, mutation/deletion of a published revision, and two progress rows for the same student/revision.

```go
func TestTeachingSchemaEnforcesHierarchyAudienceAndImmutableRevisions(t *testing.T) {
    pool := integration.OpenMigratedPostgres(t)
    teacher := integration.InsertUser(t, pool, "teacher", "admin", "active")
    student := integration.InsertUser(t, pool, "student-1", "student", "active")
    ids := insertCatalogPath(t, pool)
    lessonID := insertLessonDraft(t, pool, ids.ChapterID, teacher)
    revisionID := publishFixtureRevision(t, pool, lessonID, teacher, student)
    _, err := pool.Exec(context.Background(), `UPDATE lesson_revisions SET title='mutated' WHERE id=$1`, revisionID)
    require.Error(t, err)
}
```

- [ ] **Step 2: Run the focused tests to verify RED**

Run: `go test ./internal/platform/database ./tests/integration -run 'Teaching|Migration' -count=1`

Expected: FAIL because migration `00004` and teaching tables do not exist.

- [ ] **Step 3: Add the migration with explicit constraints and indexes**

The migration must enable `pg_trgm`, create separate `grades`, `terms`, `subjects`, `chapters`, `lessons`, `lesson_drafts`, `lesson_revisions`, draft/revision audience tables, draft/revision external-video tables, `outbox_events`, and `lesson_progress`. Use `citext`-style normalized-name columns or generated `lower(btrim(name))` uniqueness; use foreign keys with restrictive deletes; use a trigger that raises SQLSTATE `55000` on update/delete of `lesson_revisions` and their frozen child rows.

```sql
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE grades (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  name_norm text GENERATED ALWAYS AS (lower(btrim(name))) STORED,
  sort_key bigint NOT NULL DEFAULT 1024,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (name_norm)
);

CREATE TABLE lessons (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  chapter_id uuid NOT NULL REFERENCES chapters(id) ON DELETE RESTRICT,
  published_revision_id uuid,
  archived_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE lesson_drafts (
  lesson_id uuid PRIMARY KEY REFERENCES lessons(id) ON DELETE CASCADE,
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  summary text NOT NULL DEFAULT '' CHECK (char_length(summary) <= 500),
  body_markdown text NOT NULL DEFAULT '' CHECK (char_length(body_markdown) <= 200000),
  sort_key bigint NOT NULL DEFAULT 1024,
  lock_version bigint NOT NULL DEFAULT 1 CHECK (lock_version > 0),
  updated_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  updated_at timestamptz NOT NULL DEFAULT now()
);
```

In the same migration, define the remaining schema exactly as follows: `terms(grade_id,name,name_norm,sort_key,archived_at)` unique on `(grade_id,name_norm)`; `subjects(term_id,name,name_norm,sort_key,archived_at)` unique on `(term_id,name_norm)`; `chapters(subject_id,name,name_norm,description,sort_key,archived_at)` unique on `(subject_id,name_norm)`; `lesson_revisions(lesson_id,version,title,summary,body_markdown,sort_key,published_by,published_at)` unique on `(lesson_id,version)`; draft and revision audience headers with `mode IN ('all','selected')`; selected-user join tables with `(owner_id,user_id)` primary keys and a trigger requiring `users.role='student'`; draft/revision external-video rows with HTTPS URL/title/description/sort key; `outbox_events(id,kind,payload,created_at,published_at)`; and `lesson_progress(user_id,revision_id,viewed,anchor,scroll_ratio,observed_at,first_viewed_at,last_viewed_at)` primary-keyed by `(user_id,revision_id)`. Add active-tree, published-revision, trigram title/body, outbox unpublished, and progress-recent indexes. Add a deferred foreign key from `lessons.published_revision_id` to `lesson_revisions.id` after both tables exist. Do not defer any of these constraints to application code.

- [ ] **Step 4: Define compile-time domain and store contracts**

```go
package teaching

type AudienceMode string
const (
    AudienceAll AudienceMode = "all"
    AudienceSelected AudienceMode = "selected"
)

var (
    ErrNotFound = errors.New("teaching resource not found")
    ErrForbidden = errors.New("teaching operation forbidden")
    ErrInvalid = errors.New("invalid teaching input")
    ErrConflict = errors.New("teaching draft conflict")
    ErrNotPublishable = errors.New("lesson not publishable")
)

type Draft struct {
    LessonID uuid.UUID
    ChapterID uuid.UUID
    Title, Summary, BodyMarkdown string
    SortKey, LockVersion int64
    Audience Audience
    ExternalVideos []ExternalVideo
    UpdatedAt time.Time
}

type PublicationCheck interface {
    Check(ctx context.Context, lessonID uuid.UUID) error
}

type UnitOfWork interface {
    WithinTx(context.Context, func(TxStore, audit.Writer) error) error
}
```

Define exact inputs for create/rename/reorder/archive catalog operations, save draft, publish, withdraw, browse, search, and progress updates. Keep HTTP JSON structs outside these domain types.

- [ ] **Step 5: Run migration, integration, and package tests to verify GREEN**

Run: `go test ./internal/platform/database ./internal/teaching ./tests/integration -count=1`

Expected: PASS; migration test reports schema version 4 and constraint tests pass.

- [ ] **Step 6: Commit the schema contract**

```bash
git add db/migrations/00004_teaching_catalog.sql internal/teaching/model.go internal/teaching/store.go internal/platform/database/migrate_test.go tests/integration/teaching_test.go
git commit -m "feat: add teaching catalog schema"
```

### Task 2: Teacher catalog, draft, and publication APIs

**Files:**
- Create: `internal/teaching/postgres_store.go`
- Create: `internal/teaching/service.go`
- Create: `internal/teaching/http_admin.go`
- Test: `internal/teaching/service_test.go`
- Test: `internal/teaching/http_admin_test.go`
- Test: `internal/teaching/postgres_store_test.go`
- Modify: `internal/app/app.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Consumes: Task 1 domain/store contracts, `auth.UserFromContext`, `auth.RequireRole`, `audit.Writer`, and `httpx.ClientIP`.
- Produces: `teaching.AdminHTTPService`, `teaching.NewAdminHandler`, and mounted `/api/v1/admin/catalog` plus `/api/v1/admin/lessons` routes.

- [ ] **Step 1: Write RED service tests for authorization, autosave conflicts, and publication**

```go
func TestSaveDraftRejectsStaleLockVersionWithoutOverwrite(t *testing.T) {
    store := newFakeStore(Draft{LessonID: lessonID, LockVersion: 7, Title: "current"})
    svc := NewService(store, allowPublication{}, time.Now)
    _, err := svc.SaveDraft(ctx, adminPrincipal(), SaveDraftInput{LessonID: lessonID, ExpectedVersion: 6, Title: "stale"})
    require.ErrorIs(t, err, ErrConflict)
    require.Equal(t, "current", store.draft.Title)
}

func TestPublishFreezesDraftAudienceAndWritesAuditAndOutbox(t *testing.T) {
    // Assert one transaction creates a revision snapshot, copies selected students,
    // switches published_revision_id, writes lesson.published, and writes audit.
}
```

- [ ] **Step 2: Run focused service tests to verify RED**

Run: `go test ./internal/teaching -run 'SaveDraft|Publish|Catalog' -count=1`

Expected: FAIL because `NewService` and PostgreSQL operations are not implemented.

- [ ] **Step 3: Implement the PostgreSQL store and transaction boundary**

Use the exact optimistic `UPDATE lesson_drafts` query shown below, `SELECT lesson_id FROM lesson_drafts WHERE lesson_id=$1 FOR UPDATE` during publication, restrictive catalog queries, and explicit pg error mapping.

```go
func (s *PostgresStore) SaveDraft(ctx context.Context, in SaveDraftInput) (Draft, error) {
    row := s.q.QueryRow(ctx, `
      UPDATE lesson_drafts SET title=$3, summary=$4, body_markdown=$5,
        sort_key=$6, lock_version=lock_version+1, updated_by=$7, updated_at=now()
      WHERE lesson_id=$1 AND lock_version=$2
      RETURNING lesson_id, title, summary, body_markdown, sort_key, lock_version, updated_at`,
      in.LessonID, in.ExpectedVersion, in.Title, in.Summary, in.BodyMarkdown, in.SortKey, in.ActorID)
    draft, err := scanDraft(row)
    if errors.Is(err, pgx.ErrNoRows) { return Draft{}, ErrConflict }
    return draft, mapTeachingError(err)
}
```

- [ ] **Step 4: Implement service validation and audit semantics**

Require active admin principals with request ID and canonical IP. Normalize names, enforce rune-count limits, validate audience mode, verify every selected user is an active student, validate external videos as absolute HTTPS URLs without credentials, and write audit actions `catalog.*`, `lesson.draft_saved`, `lesson.published`, `lesson.withdrawn`, and `lesson.archived`.

Wire a constructor-injected no-op `PublicationCheck` only for the catalog gate so text-only publication is testable. Do not expose a runtime setting that selects the no-op. Plan 2 replaces that injected checker with the file readiness implementation before file bindings are enabled.

- [ ] **Step 5: Write and run strict HTTP boundary tests**

Test role rejection, unknown JSON fields, body limits, invalid UUIDs, `If-Match`/lock-version conflicts, stable `409 draft_conflict`, stable `422 lesson_not_publishable`, and no-store responses.

```go
type AdminHTTPService interface {
    CreateCatalog(context.Context, Principal, CatalogCreateInput) (CatalogNode, error)
    ReorderCatalog(context.Context, Principal, CatalogReorderInput) error
    ArchiveCatalog(context.Context, Principal, CatalogArchiveInput) error
    CreateLesson(context.Context, Principal, CreateLessonInput) (Draft, error)
    SaveDraft(context.Context, Principal, SaveDraftInput) (Draft, error)
    Publish(context.Context, Principal, PublishInput) (Revision, error)
    Withdraw(context.Context, Principal, uuid.UUID) error
}
```

Run: `go test ./internal/teaching ./internal/app ./cmd/server -count=1`

Expected: PASS, including route mounting tests.

- [ ] **Step 6: Run isolated PostgreSQL integration tests**

Run: `go test ./tests/integration -run 'TeachingAdmin|Publication' -count=1`

Expected: PASS; concurrent saves produce one success and one `ErrConflict`; failed publication leaves the previous published revision unchanged.

- [ ] **Step 7: Commit teacher catalog and publication**

```bash
git add internal/teaching internal/app/app.go cmd/server/main.go tests/integration/teaching_test.go
git commit -m "feat: add teaching publication APIs"
```

### Task 3: Student catalog, authorized search, and learning state

**Files:**
- Create: `internal/teaching/student_service.go`
- Create: `internal/teaching/http_student.go`
- Test: `internal/teaching/student_service_test.go`
- Test: `internal/teaching/http_student_test.go`
- Modify: `internal/teaching/postgres_store.go`
- Modify: `internal/app/app.go`
- Modify: `cmd/server/main.go`
- Test: `tests/integration/teaching_test.go`

**Interfaces:**
- Consumes: immutable revision/audience schema and Phase 1 authenticated principal.
- Produces: authorized `/api/v1/student/catalog`, `/lessons`, `/search`, `/progress` APIs used by the Vue plan.

- [ ] **Step 1: Write RED authorization and non-enumeration tests**

Create two students and three lessons: all-students, selected-for-A, selected-for-B. Assert A sees only the first two in browse/search; direct access to B returns `ErrNotFound`; disabled A sees none; draft text never appears in search.

```go
func TestStudentLessonReadDoesNotRevealUnauthorizedLesson(t *testing.T) {
    svc := NewStudentService(fakeStudentStore{lesson: publishedFor(otherStudent)})
    _, err := svc.GetLesson(ctx, studentPrincipal(studentA), lessonID)
    require.ErrorIs(t, err, ErrNotFound)
}
```

- [ ] **Step 2: Run tests to verify RED**

Run: `go test ./internal/teaching -run 'Student|Search|Progress' -count=1`

Expected: FAIL because student service and authorized SQL are absent.

- [ ] **Step 3: Implement authorization inside SQL**

Every browse, search, and read query must join the current `published_revision_id`, active catalog ancestors, active student row, and either `audience_mode='all'` or a matching frozen audience-user row. Do not load then filter in Go.

At this gate, search title and Markdown body only. Escape wildcard characters and use `pg_trgm` indexes with a stable `(sort_key,id)` cursor and a maximum page size of 50. Keep the search projection in one PostgreSQL query function so Plan 2 can add authorized revision attachment display names after the file-binding tables exist.

```sql
WHERE u.id = $1 AND u.role = 'student' AND u.status = 'active' AND u.deleted_at IS NULL
  AND l.published_revision_id = r.id
  AND (ra.mode = 'all' OR EXISTS (
      SELECT 1 FROM lesson_revision_audience_users rau
      WHERE rau.revision_id = r.id AND rau.user_id = $1
  ))
```

- [ ] **Step 4: Implement progress with monotonic timestamps**

```go
type ProgressInput struct {
    RevisionID uuid.UUID
    Viewed bool
    Anchor string
    ScrollRatio float64
    ObservedAt time.Time
}

// PostgreSQL update condition:
// ON CONFLICT (user_id, revision_id) DO UPDATE SET
// viewed = lesson_progress.viewed OR EXCLUDED.viewed,
// anchor = EXCLUDED.anchor, scroll_ratio = EXCLUDED.scroll_ratio,
// observed_at = EXCLUDED.observed_at, last_viewed_at = now()
// WHERE lesson_progress.observed_at <= EXCLUDED.observed_at
```

Validate anchor length ≤ 160, ratio in `[0,1]`, and observed time within 10 minutes of server time. Limit progress writes per session in Redis when available, while PostgreSQL correctness remains independent of Redis.

- [ ] **Step 5: Implement student HTTP routes and stable errors**

Mount routes under authenticated student role. Return the same `404 not_found` for missing, unpublished, archived, or unauthorized lessons. Enforce cursor/page limits and JSON body limits.

Run: `go test ./internal/teaching ./internal/app ./tests/integration -count=1`

Expected: PASS; integration tests prove cross-student reads and search enumeration fail.

- [ ] **Step 6: Run the catalog gate**

Run:

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

Expected: all commands exit 0. Also run `govulncheck ./...` and require zero reachable vulnerabilities.

- [ ] **Step 7: Commit student teaching reads**

```bash
git add internal/teaching internal/app/app.go cmd/server/main.go tests/integration/teaching_test.go
git commit -m "feat: add authorized student learning APIs"
```

## Catalog Gate Review

Before Plan 2, require an independent spec review and security review of the complete plan diff. Reject progression for any Critical/Important issue involving revision mutability, student enumeration, cross-student authorization, optimistic concurrency, transaction atomicity, audit coverage, or migration rollback safety.
