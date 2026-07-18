# Phase 2 catalog — Task 2 report

## Status

Completed teacher catalog, lesson draft, and publication APIs only. Student browse/search/progress behavior remains unimplemented for Task 3.

## Implementation

- Added `teaching.Service` with active-admin/request-ID/canonical-IP authorization, catalog and draft validation, HTTPS external-video validation, publication gating, optimistic locking, and transactional audit events.
- Added `PostgresStore` and transaction boundary. Publication locks the draft, snapshots draft/audience/videos, finalizes the revision, switches the deferred published pointer, and writes an outbox row atomically.
- Added authenticated, no-store admin routes for catalog mutation, lesson creation, draft save, publication, and withdrawal. JSON is strict and bounded; stable `draft_conflict` and `lesson_not_publishable` errors are returned.
- Mounted the new admin routes in `internal/app` and production wiring.
- Extended audit validation to permit the Task 2-required `catalog.*` and `lesson.*` events; this is required so the audit row can be written in the same publication transaction instead of forcing a rollback.

## Files

- New: `internal/teaching/service.go`, `postgres_store.go`, `http_admin.go` and their unit tests.
- New: `tests/integration/teaching_admin_test.go`.
- Updated: `internal/app/app.go`, `cmd/server/main.go`, `internal/audit/postgres_store.go`.

## RED/GREEN evidence

- RED: added `service_test.go` before service/store production files. The prescribed local command initially failed because `go` was absent from PATH (`The term 'go' is not recognized`); this environment issue prevented observing the intended compile-red result.
- GREEN focused: cached `golang:1.26.5-bookworm` container ran `go test ./internal/teaching -run 'SaveDraft|Publish|Catalog' -count=1` successfully.
- GREEN package suite: `go test ./internal/teaching ./internal/app ./cmd/server -count=1` passed.
- GREEN isolated integration: disposable PostgreSQL 18 container on localhost:55432 ran `go test ./tests/integration -run 'TeachingAdmin|Publication' -count=1` successfully. It verifies finalization/pointer/audit/outbox and a publication-gate failure retaining the prior published revision.
- A final rerun caught and corrected the integration assertion to scope outbox rows by lesson payload; final package and integration suites both passed after that correction.

## Self-review

- Confirmed the exact optimistic draft `UPDATE ... lock_version=lock_version+1` is used.
- Confirmed publication performs `FOR UPDATE`, writes all revision children before finalization, and sets the deferred published pointer in the same transaction.
- Confirmed route-level role enforcement, strict JSON, UUID and `If-Match` checks, body limits, stable 409/422 response codes, and no-store headers.
- Confirmed the temporary PostgreSQL container was removed after testing; no deployed database or user data was accessed.

## Concern

The local PATH lacked Go, so the initial written RED test could not be executed before implementation. All subsequent focused, package, and disposable-PostgreSQL integration verification passed in the cached Go container.

## Review-fix RED/GREEN (follow-up)

- RED: `go test ./tests/integration -run "SnapshotsExternal|RejectsArchived" -count=1` failed: second publication returned `teaching draft conflict` from duplicate revision-video IDs; every archived hierarchy level published successfully.
- GREEN: publication now generates new revision-video IDs and locks/validates the complete active grade→term→subject→chapter→lesson hierarchy. The same focused suite passed.
- Added lesson archive service/store/HTTP behavior with atomic `lesson.archived` audit, bounded empty-body enforcement for publish/withdraw, and action-to-target audit validation.
- Final suite: `go test ./internal/teaching ./internal/audit ./internal/app ./cmd/server ./tests/integration -run "Teaching|Publication|Admin|Audit|SaveDraft|Publish" -count=1` passed against disposable PostgreSQL on localhost:55433.
- Self-review: second publication preserves draft-video IDs while every revision snapshot receives fresh IDs; archived lessons and archived ancestors cannot publish; cross-domain audit actions are rejected.

## Gate Remediation 2

### Implementation

- Added authenticated no-store admin reads: `GET /api/v1/admin/catalog` supports `kind`, `parentId`, `includeArchived`, bounded `limit` (1–200), and a stable opaque `(rank,sortKey,id)` cursor; `GET /api/v1/admin/lessons/{id}` returns lesson status, draft, audience/videos, and current publication; `GET /api/v1/admin/lessons/{id}/revisions` returns bounded newest-first history with an opaque `(version,id)` cursor.
- Admin HTTP requests/responses now use explicit lower-camel DTOs. Revision DTOs include `sourceDraftVersion`; no domain structs are serialized by the admin handler.
- Publication now runs `LockDraftForPublication` inside the unit of work, revalidates the exact persisted draft and active hierarchy, confirms exact audience semantics and active students, invokes `PublicationCheck.Check(ctx, txReader, lockedDraft)`, then snapshots/finalizes/switches pointer/writes outbox and audit in the same transaction.
- `PublicationReader` is the smallest transaction-scoped query capability compatible with future secure-file binding readiness queries; the catalog no-op remains constructor-injected and private.
- Server-side publication validation rejects invalid bounds/modes, all-mode user IDs, empty selected audiences, inactive selected students, control/NUL content, unsafe Markdown/LaTeX constructs, unbalanced fenced blocks, and invalid external videos.
- Draft saves canonicalize HTTPS scheme/host, strip port 443 and an empty fragment, reject credentials/control characters, and preserve path/query and meaningful fragments.
- Added focused archive success/forbidden/body tests and publish/withdraw oversized-body 413/no-dispatch coverage.

### RED/GREEN evidence

- RED: `go test ./internal/teaching -run "NormalizeExternal|DraftValidation" -count=1` failed to compile on the missing transaction reader/checker contract, URL normalization, persisted validation, and new store methods.
- GREEN focused: `go test ./internal/teaching -run "NormalizeExternal|DraftValidation|AdminRead|SaveDraft|Publish" -count=1` passed.
- GREEN isolated PostgreSQL: `go test ./tests/integration -run "PublicationCheckRuns|PersistedUnsafe|AdminReadStore|TeachingAdmin|Publication" -count=1` passed against disposable PostgreSQL on localhost:55435. It proves the readiness checker runs while the draft row is locked and that checker failure rolls back revision/finalization/pointer/outbox/audit.
- Final: `go vet ./internal/teaching ./internal/app ./cmd/server` passed; `go test -race ./internal/teaching -count=1` passed; `go test ./internal/teaching ./internal/app ./cmd/server ./tests/integration -count=1` passed.

### Self-review

- Admin list cursors are bounded before decoding and use deterministic SQL ordering.
- Active-only catalog reads propagate ancestor archive state; `includeArchived=true` exposes archived branches explicitly rather than making them look active.
- Revision detail/history and current publication all preserve `sourceDraftVersion`; no uniqueness constraint was added, so withdrawal and republish remain supported.
- No student read DTOs or Task 3 behavior were changed.
