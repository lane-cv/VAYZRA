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
- Post-review refinement: narrowed `PublicationReader` from generic SQL query methods to the typed `PublicationBlockers(ctx, lessonID, sourceDraftVersion)` capability required by the secure-files plan; the relevant package suite passed again.

## Remediation 2 review-fix pass

### Implementation

- Replaced the audience eligibility count with `SELECT id ... ORDER BY id FOR SHARE` inside the publication transaction. The store verifies the exact selected ID set while holding row locks through commit, preventing active/nondeleted audience TOCTOU races.
- The publication checker now receives an unexported, narrow `publicationReadScope`, not `TxStore` or `*PostgresStore`. The scope contains only `PublicationBlockers`, is invalidated as soon as the synchronous checker returns, and retained calls return `ErrPublicationReaderExpired`.
- Checker results preserve error identity. Only `ErrNotPublishable` (including wrappers) is treated as a semantic 422 by the HTTP boundary; unexpected checker or blocker-query failures remain infrastructure errors and map to 500 while the unit of work rolls back.
- Publication rejects any persisted external-video URL for which `normalizeExternalURL(raw) != raw`. A SaveDraft round trip remains the supported way to produce a canonical snapshot URL.
- Replaced delimiter substring/count checks with a deterministic bounded structural validator. Limits are documented in code: 200,000 body runes, 8,192 runes and 1,024 commands per math expression, brace depth 64, environment depth 32, and environment names 64 runes. It rejects disallowed controls, unclosed backtick/tilde fences, unmatched math delimiters/braces/environments, mismatched environments, dangerous LaTeX I/O commands, and excessive size/complexity while accepting representative Chinese/high-school Markdown and LaTeX.
- `GetAdminLesson` now joins lesson→chapter→subject→term→grade and reports the effective archive timestamp from any of the five levels.
- No student/Task 3 or secure-file implementation was changed.

### Exact RED evidence

The first focused test run used the cached local Go 1.26.5 toolchain and existing module cache (Docker API access was denied in this sandbox):

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestPublicationReaderHasLeastAuthorityAndExpiresAfterCheck|TestPublicationCheckErrorClassificationAndRollback|TestPersistedDraftRequiresCanonicalExternalURLs|TestValidatePublicationBody' -count=1
FAIL	happylearn.local/app/internal/teaching [build failed]
# happylearn.local/app/internal/teaching [happylearn.local/app/internal/teaching.test]
internal\teaching\publication_review_remediation_test.go:30:100: undefined: ErrPublicationReaderExpired
internal\teaching\publication_review_remediation_test.go:114:13: undefined: validatePublicationBody
internal\teaching\publication_review_remediation_test.go:133:51: undefined: maxPublicationBraceDepth
internal\teaching\publication_review_remediation_test.go:134:61: undefined: maxPublicationMathRunes
internal\teaching\publication_review_remediation_test.go:135:49: undefined: maxPublicationBodyRunes
internal\teaching\publication_review_remediation_test.go:139:14: undefined: validatePublicationBody
FAIL
```

A second RED test tightened dangerous-command coverage before its production change:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestValidatePublicationBody/dangerous_write_command' -count=1
--- FAIL: TestValidatePublicationBody (0.00s)
    --- FAIL: TestValidatePublicationBody/dangerous_write_command (0.00s)
        publication_review_remediation_test.go:142: invalid body accepted
FAIL
FAIL	happylearn.local/app/internal/teaching	1.318s
FAIL
```

### Exact GREEN and covering evidence

Focused unit GREEN:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestPublicationReaderHasLeastAuthorityAndExpiresAfterCheck|TestPublicationCheckErrorClassificationAndRollback|TestPersistedDraftRequiresCanonicalExternalURLs|TestValidatePublicationBody' -count=1
ok  	happylearn.local/app/internal/teaching	2.418s
```

Focused PostgreSQL GREEN (test database at `127.0.0.1:54329`):

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./tests/integration -run 'TestSelectedAudienceUsersStayLockedThroughPublication|TestPublishRejectsNonCanonicalPersistedURLThenAcceptsSavedCanonicalURL|TestGetAdminLessonReportsEffectiveAncestorArchive' -count=1 -v
=== RUN   TestSelectedAudienceUsersStayLockedThroughPublication
=== RUN   TestSelectedAudienceUsersStayLockedThroughPublication/disable
=== RUN   TestSelectedAudienceUsersStayLockedThroughPublication/soft_delete
--- PASS: TestSelectedAudienceUsersStayLockedThroughPublication (1.49s)
    --- PASS: TestSelectedAudienceUsersStayLockedThroughPublication/disable (0.54s)
    --- PASS: TestSelectedAudienceUsersStayLockedThroughPublication/soft_delete (0.40s)
=== RUN   TestPublishRejectsNonCanonicalPersistedURLThenAcceptsSavedCanonicalURL
--- PASS: TestPublishRejectsNonCanonicalPersistedURLThenAcceptsSavedCanonicalURL (0.51s)
=== RUN   TestGetAdminLessonReportsEffectiveAncestorArchive
=== RUN   TestGetAdminLessonReportsEffectiveAncestorArchive/grade
=== RUN   TestGetAdminLessonReportsEffectiveAncestorArchive/term
=== RUN   TestGetAdminLessonReportsEffectiveAncestorArchive/subject
=== RUN   TestGetAdminLessonReportsEffectiveAncestorArchive/chapter
=== RUN   TestGetAdminLessonReportsEffectiveAncestorArchive/lesson
--- PASS: TestGetAdminLessonReportsEffectiveAncestorArchive (0.81s)
    --- PASS: TestGetAdminLessonReportsEffectiveAncestorArchive/grade (0.07s)
    --- PASS: TestGetAdminLessonReportsEffectiveAncestorArchive/term (0.05s)
    --- PASS: TestGetAdminLessonReportsEffectiveAncestorArchive/subject (0.11s)
    --- PASS: TestGetAdminLessonReportsEffectiveAncestorArchive/chapter (0.06s)
    --- PASS: TestGetAdminLessonReportsEffectiveAncestorArchive/lesson (0.05s)
PASS
ok  	happylearn.local/app/tests/integration	6.409s
```

Semantic and infrastructure error rollback coverage:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./tests/integration -run 'TestTeachingPublicationFailureLeavesCurrentRevisionUntouched|TestPublicationCheckRunsUnderDraftLockAndFailureRollsBack' -count=1 -v
=== RUN   TestPublicationCheckRunsUnderDraftLockAndFailureRollsBack
--- PASS: TestPublicationCheckRunsUnderDraftLockAndFailureRollsBack (0.60s)
=== RUN   TestTeachingPublicationFailureLeavesCurrentRevisionUntouched
--- PASS: TestTeachingPublicationFailureLeavesCurrentRevisionUntouched (0.20s)
PASS
ok  	happylearn.local/app/tests/integration	3.371s
```

Final full repository GREEN:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./... -count=1
ok  	happylearn.local/app/cmd/admin	6.360s
ok  	happylearn.local/app/cmd/server	4.637s
?   	happylearn.local/app/db/migrations	[no test files]
ok  	happylearn.local/app/internal/app	4.053s
ok  	happylearn.local/app/internal/audit	6.499s
ok  	happylearn.local/app/internal/auth	9.263s
ok  	happylearn.local/app/internal/buildinfo	2.205s
ok  	happylearn.local/app/internal/platform/config	2.370s
ok  	happylearn.local/app/internal/platform/database	8.090s
ok  	happylearn.local/app/internal/platform/httpx	3.323s
ok  	happylearn.local/app/internal/platform/redisx	9.457s
ok  	happylearn.local/app/internal/platform/staticweb	3.384s
ok  	happylearn.local/app/internal/students	7.235s
ok  	happylearn.local/app/internal/teaching	5.669s
ok  	happylearn.local/app/tests/integration	28.570s
```

Static analysis:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' vet ./...
# exit 0; no output
```

Race verification was attempted proportionately but is unavailable in this Windows environment:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test -race ./internal/teaching -count=1
go: -race requires cgo; enable cgo by setting CGO_ENABLED=1

$env:CGO_ENABLED='1'; & 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test -race ./internal/teaching -count=1
# runtime/cgo
cgo: C compiler "gcc" not found: exec: "gcc": executable file not found in %PATH%
FAIL	happylearn.local/app/internal/teaching [build failed]
```

### Self-review and minor note

- The concurrency test proves both `status='disabled'` and `deleted_at=now()` updates cannot complete while publication holds the audience rows; each update succeeds after commit and the revision audience row remains frozen.
- The least-authority test rejects dynamic assertions to both `TxStore` and `*PostgresStore`; a retained reader fails after checker return. A separate test preserves blocker-query infrastructure errors, and the admin HTTP test verifies they map to `internal_error`/500 rather than 422.
- Tampered uppercase-host/default-port/empty-fragment persisted URLs return `ErrNotPublishable` with zero revisions; SaveDraft canonicalization followed by Publish succeeds.
- Archive coverage explicitly exercises grade, term, subject, chapter, and lesson.
- Minor cursor coverage note: deterministic SQL ordering and bounded cursor decoding remain covered by the existing implementation/contract tests, but an end-to-end non-empty, multi-page admin cursor round-trip is still a minor test-coverage gap. It was recorded rather than expanded because this pass was scoped to the six Important findings.
- Task-local `.tmp/` Go caches created for the Docker fallback were validated as workspace-local and removed before staging. Existing workspace `.tmp-go-*` caches were not modified for source control.

## Remediation 2 second review-fix pass

### Implementation

- `publicationReadScope.PublicationBlockers` now holds its read lock for the complete underlying query. `invalidate` therefore waits for every in-flight read before clearing the capability. `Service.checkPublication` defers invalidation around the checker call, so normal returns, error returns, and propagated panics all expire the reader deterministically.
- Math delimiter entry now snapshots brace depth and environment-stack depth. Matching `$`/`$$` and `\(`/`\[` closures require both baselines to be restored, preventing braces or environments from crossing a math boundary. `\begin{...}` and `\end{...}` now consume their full command/name/brace rune count and one command unit inside math; the exact 8,192-rune/1,024-command boundary passes, while one extra environment pair fails.
- SaveDraft and locked publication revalidation now reject disallowed controls in lesson title, summary, external-video title, and description before database writes. Single-line titles reject all control characters (including newline/tab); optional multiline summary/description fields allow CR/LF/tab but reject other controls. Direct database tampering returns `ErrNotPublishable` and creates no revision.

### Exact RED evidence

Reader lifetime RED:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestPublicationReadScopeInvalidationWaitsForInflightRead|TestPublicationReaderExpiresWhenCheckerPanics' -count=1 -v
=== RUN   TestPublicationReadScopeInvalidationWaitsForInflightRead
    publication_review_remediation_test.go:220: reader invalidated before the in-flight underlying read completed
--- FAIL: TestPublicationReadScopeInvalidationWaitsForInflightRead (0.00s)
=== RUN   TestPublicationReaderExpiresWhenCheckerPanics
    publication_review_remediation_test.go:258: reader after checker panic error = <nil>, want expired
--- FAIL: TestPublicationReaderExpiresWhenCheckerPanics (0.00s)
FAIL
FAIL	happylearn.local/app/internal/teaching	1.345s
FAIL
```

Math state/complexity RED:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestPublicationMathStateBoundariesAndEnvironmentComplexity' -count=1 -v
=== RUN   TestPublicationMathStateBoundariesAndEnvironmentComplexity
=== RUN   TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_budget_boundary
=== RUN   TestPublicationMathStateBoundariesAndEnvironmentComplexity/brace_crosses_math_closure
    publication_review_remediation_test.go:286: validatePublicationBody error = <nil>, wantErr=true
=== RUN   TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_opens_inside_math_and_closes_outside
    publication_review_remediation_test.go:286: validatePublicationBody error = <nil>, wantErr=true
=== RUN   TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_opens_outside_math_and_closes_inside
    publication_review_remediation_test.go:286: validatePublicationBody error = <nil>, wantErr=true
=== RUN   TestPublicationMathStateBoundariesAndEnvironmentComplexity/repeated_environments_exceed_expression_limits
    publication_review_remediation_test.go:286: validatePublicationBody error = <nil>, wantErr=true
--- FAIL: TestPublicationMathStateBoundariesAndEnvironmentComplexity (0.00s)
    --- PASS: TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_budget_boundary (0.00s)
    --- FAIL: TestPublicationMathStateBoundariesAndEnvironmentComplexity/brace_crosses_math_closure (0.00s)
    --- FAIL: TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_opens_inside_math_and_closes_outside (0.00s)
    --- FAIL: TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_opens_outside_math_and_closes_inside (0.00s)
    --- FAIL: TestPublicationMathStateBoundariesAndEnvironmentComplexity/repeated_environments_exceed_expression_limits (0.00s)
FAIL
FAIL	happylearn.local/app/internal/teaching	1.470s
FAIL
```

SaveDraft control-validation RED:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestSaveDraftRejectsDisallowedControlsByField|TestSaveDraftAllowsLayoutWhitespaceInMultilineFields' -count=1 -v
=== RUN   TestSaveDraftRejectsDisallowedControlsByField
=== RUN   TestSaveDraftRejectsDisallowedControlsByField/lesson_title_control
    publication_review_remediation_test.go:316: SaveDraft error = <nil>, want invalid
=== RUN   TestSaveDraftRejectsDisallowedControlsByField/lesson_title_newline
    publication_review_remediation_test.go:316: SaveDraft error = <nil>, want invalid
=== RUN   TestSaveDraftRejectsDisallowedControlsByField/summary_control
    publication_review_remediation_test.go:316: SaveDraft error = <nil>, want invalid
=== RUN   TestSaveDraftRejectsDisallowedControlsByField/video_title_control
    publication_review_remediation_test.go:316: SaveDraft error = <nil>, want invalid
=== RUN   TestSaveDraftRejectsDisallowedControlsByField/video_title_tab
    publication_review_remediation_test.go:316: SaveDraft error = <nil>, want invalid
=== RUN   TestSaveDraftRejectsDisallowedControlsByField/video_description_control
    publication_review_remediation_test.go:316: SaveDraft error = <nil>, want invalid
--- FAIL: TestSaveDraftRejectsDisallowedControlsByField (0.00s)
    --- FAIL: TestSaveDraftRejectsDisallowedControlsByField/lesson_title_control (0.00s)
    --- FAIL: TestSaveDraftRejectsDisallowedControlsByField/lesson_title_newline (0.00s)
    --- FAIL: TestSaveDraftRejectsDisallowedControlsByField/summary_control (0.00s)
    --- FAIL: TestSaveDraftRejectsDisallowedControlsByField/video_title_control (0.00s)
    --- FAIL: TestSaveDraftRejectsDisallowedControlsByField/video_title_tab (0.00s)
    --- FAIL: TestSaveDraftRejectsDisallowedControlsByField/video_description_control (0.00s)
=== RUN   TestSaveDraftAllowsLayoutWhitespaceInMultilineFields
--- PASS: TestSaveDraftAllowsLayoutWhitespaceInMultilineFields (0.00s)
FAIL
FAIL	happylearn.local/app/internal/teaching	1.662s
FAIL
```

Locked persisted-tamper RED:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./tests/integration -run 'TestPublishRejectsPersistedControlsInNonBodyFields' -count=1 -v
=== RUN   TestPublishRejectsPersistedControlsInNonBodyFields
=== RUN   TestPublishRejectsPersistedControlsInNonBodyFields/lesson_title
    teaching_review_remediation_test.go:265: Publish error = <nil>, want not publishable
=== RUN   TestPublishRejectsPersistedControlsInNonBodyFields/summary
    teaching_review_remediation_test.go:265: Publish error = <nil>, want not publishable
=== RUN   TestPublishRejectsPersistedControlsInNonBodyFields/video_title
    teaching_review_remediation_test.go:265: Publish error = <nil>, want not publishable
=== RUN   TestPublishRejectsPersistedControlsInNonBodyFields/video_description
    teaching_review_remediation_test.go:265: Publish error = <nil>, want not publishable
--- FAIL: TestPublishRejectsPersistedControlsInNonBodyFields (1.09s)
    --- FAIL: TestPublishRejectsPersistedControlsInNonBodyFields/lesson_title (0.26s)
    --- FAIL: TestPublishRejectsPersistedControlsInNonBodyFields/summary (0.25s)
    --- FAIL: TestPublishRejectsPersistedControlsInNonBodyFields/video_title (0.23s)
    --- FAIL: TestPublishRejectsPersistedControlsInNonBodyFields/video_description (0.25s)
FAIL
FAIL	happylearn.local/app/tests/integration	3.764s
FAIL
```

### Exact GREEN and final verification

Reader lifetime GREEN:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestPublicationReadScopeInvalidationWaitsForInflightRead|TestPublicationReaderExpiresWhenCheckerPanics' -count=1 -v
=== RUN   TestPublicationReadScopeInvalidationWaitsForInflightRead
--- PASS: TestPublicationReadScopeInvalidationWaitsForInflightRead (0.10s)
=== RUN   TestPublicationReaderExpiresWhenCheckerPanics
--- PASS: TestPublicationReaderExpiresWhenCheckerPanics (0.00s)
PASS
ok  	happylearn.local/app/internal/teaching	1.688s
```

Math state/complexity GREEN:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestPublicationMathStateBoundariesAndEnvironmentComplexity|TestValidatePublicationBody' -count=1 -v
--- PASS: TestValidatePublicationBody (0.00s)
--- PASS: TestPublicationMathStateBoundariesAndEnvironmentComplexity (0.00s)
    --- PASS: TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_budget_boundary (0.00s)
    --- PASS: TestPublicationMathStateBoundariesAndEnvironmentComplexity/brace_crosses_math_closure (0.00s)
    --- PASS: TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_opens_inside_math_and_closes_outside (0.00s)
    --- PASS: TestPublicationMathStateBoundariesAndEnvironmentComplexity/environment_opens_outside_math_and_closes_inside (0.00s)
    --- PASS: TestPublicationMathStateBoundariesAndEnvironmentComplexity/repeated_environments_exceed_expression_limits (0.00s)
PASS
ok  	happylearn.local/app/internal/teaching	2.156s
```

Control validation GREEN:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching -run 'TestSaveDraftRejectsDisallowedControlsByField|TestSaveDraftAllowsLayoutWhitespaceInMultilineFields' -count=1 -v
--- PASS: TestSaveDraftRejectsDisallowedControlsByField (0.00s)
    --- PASS: TestSaveDraftRejectsDisallowedControlsByField/lesson_title_control (0.00s)
    --- PASS: TestSaveDraftRejectsDisallowedControlsByField/lesson_title_newline (0.00s)
    --- PASS: TestSaveDraftRejectsDisallowedControlsByField/summary_control (0.00s)
    --- PASS: TestSaveDraftRejectsDisallowedControlsByField/video_title_control (0.00s)
    --- PASS: TestSaveDraftRejectsDisallowedControlsByField/video_title_tab (0.00s)
    --- PASS: TestSaveDraftRejectsDisallowedControlsByField/video_description_control (0.00s)
=== RUN   TestSaveDraftAllowsLayoutWhitespaceInMultilineFields
--- PASS: TestSaveDraftAllowsLayoutWhitespaceInMultilineFields (0.00s)
PASS
ok  	happylearn.local/app/internal/teaching	1.883s

& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./tests/integration -run 'TestPublishRejectsPersistedControlsInNonBodyFields' -count=1 -v
--- PASS: TestPublishRejectsPersistedControlsInNonBodyFields (0.75s)
    --- PASS: TestPublishRejectsPersistedControlsInNonBodyFields/lesson_title (0.18s)
    --- PASS: TestPublishRejectsPersistedControlsInNonBodyFields/summary (0.18s)
    --- PASS: TestPublishRejectsPersistedControlsInNonBodyFields/video_title (0.18s)
    --- PASS: TestPublishRejectsPersistedControlsInNonBodyFields/video_description (0.13s)
PASS
ok  	happylearn.local/app/tests/integration	3.451s
```

Full relevant packages and integration:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching ./tests/integration -count=1
ok  	happylearn.local/app/internal/teaching	2.174s
ok  	happylearn.local/app/tests/integration	19.098s
```

Fresh repository-wide verification:

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./... -count=1
ok  	happylearn.local/app/cmd/admin	7.105s
ok  	happylearn.local/app/cmd/server	6.060s
?   	happylearn.local/app/db/migrations	[no test files]
ok  	happylearn.local/app/internal/app	5.856s
ok  	happylearn.local/app/internal/audit	6.916s
ok  	happylearn.local/app/internal/auth	10.179s
ok  	happylearn.local/app/internal/buildinfo	1.916s
ok  	happylearn.local/app/internal/platform/config	1.956s
ok  	happylearn.local/app/internal/platform/database	9.140s
ok  	happylearn.local/app/internal/platform/httpx	2.545s
ok  	happylearn.local/app/internal/platform/redisx	9.957s
ok  	happylearn.local/app/internal/platform/staticweb	2.519s
ok  	happylearn.local/app/internal/students	8.235s
ok  	happylearn.local/app/internal/teaching	2.912s
ok  	happylearn.local/app/tests/integration	25.088s
```

```text
& 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' vet ./...
# exit 0; no output
```

The race detector was not rerun because the toolchain/environment is unchanged from the immediately preceding recorded attempt: `-race` requires CGO and no `gcc` is installed. Docker remains inaccessible to this sandbox, so there is no alternative Linux race runner in this task.

### Self-review and recorded Minor items

- In-flight reader testing releases all barriers before assertions and the panic test uses local recovery, so neither test leaves a goroutine or lock behind. The read lock covers the underlying call; deferred invalidation cannot clear its source early.
- Math state baselines are captured on every supported delimiter entry and checked on every matching closure. Environment pairs at the exact 8,192-rune/1,024-command boundary pass; the next pair fails before stack mutation.
- The same `validDraft` path is used before SaveDraft persistence and from `validPersistedDraft` after locking, so control failures are stable `ErrInvalid`/`ErrNotPublishable` results rather than database-dependent errors.
- Minor, recorded only: the selected-audience lock-wait integration assertion still uses a bounded 150 ms non-completion window; replacing it with PostgreSQL lock-state synchronization would make the test less timing-sensitive.
- Minor, recorded only: an end-to-end non-empty multi-page admin cursor round-trip remains a coverage gap; deterministic ordering/bounds are covered, but this pass does not expand cursor integration scope.
- `go test -race` remains blocked by the missing C compiler, as recorded above and in the prior section.
- `git diff --check` passed, `.tmp` is absent, and only the intended Task 2 source/tests/report files remain modified or untracked.
## Final teacher status gate

### Approved design

The approved projection uses a correlated EXISTS over lesson_revisions to add bounded HasRevisions read-model data without multiplying catalog rows. A shared admin DTO mapper applies the exact lesson precedence archived > published > withdrawn > draft. The lower-camel status field is explicit on both catalog and lesson detail responses. Catalog published is intentionally retained as a compatibility shorthand for whether a current published pointer exists; the internal HasRevisions projection is not serialized. Non-lesson catalog items retain their existing active/archived status vocabulary.

### Exact RED evidence

    & 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching ./tests/integration -run 'TestAdminLessonStatusDTOHasExactlyFourEffectiveStates|TestAdminLessonStatusTransitionsAndWithdrawHidesStudentLesson' -count=1 -v
    FAIL    happylearn.local/app/internal/teaching [build failed]
    === RUN   TestAdminLessonStatusTransitionsAndWithdrawHidesStudentLesson
        teaching_status_test.go:41: catalog lesson status="active" published=false, want "draft"/false
    --- FAIL: TestAdminLessonStatusTransitionsAndWithdrawHidesStudentLesson
    FAIL
    FAIL    happylearn.local/app/tests/integration
    FAIL
    # happylearn.local/app/internal/teaching [happylearn.local/app/internal/teaching.test]
    internal\teaching\admin_status_test.go:54:43: unknown field HasRevisions in struct literal of type AdminCatalogItem
    internal\teaching\admin_status_test.go:59:6: unknown field HasRevisions in struct literal of type AdminLessonDetail

This failed for the intended reasons: no revision-history projection existed and the real catalog returned active instead of draft.

### Exact focused GREEN evidence

    & 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching ./tests/integration -run 'TestAdminLessonStatusDTOHasExactlyFourEffectiveStates|TestAdminLessonStatusTransitionsAndWithdrawHidesStudentLesson' -count=1 -v
    === RUN   TestAdminLessonStatusDTOHasExactlyFourEffectiveStates
    === RUN   TestAdminLessonStatusDTOHasExactlyFourEffectiveStates/draft
    === RUN   TestAdminLessonStatusDTOHasExactlyFourEffectiveStates/published
    === RUN   TestAdminLessonStatusDTOHasExactlyFourEffectiveStates/withdrawn
    === RUN   TestAdminLessonStatusDTOHasExactlyFourEffectiveStates/archived_precedence
    --- PASS: TestAdminLessonStatusDTOHasExactlyFourEffectiveStates
        --- PASS: TestAdminLessonStatusDTOHasExactlyFourEffectiveStates/draft
        --- PASS: TestAdminLessonStatusDTOHasExactlyFourEffectiveStates/published
        --- PASS: TestAdminLessonStatusDTOHasExactlyFourEffectiveStates/withdrawn
        --- PASS: TestAdminLessonStatusDTOHasExactlyFourEffectiveStates/archived_precedence
    PASS
    ok      happylearn.local/app/internal/teaching    2.122s
    === RUN   TestAdminLessonStatusTransitionsAndWithdrawHidesStudentLesson
    --- PASS: TestAdminLessonStatusTransitionsAndWithdrawHidesStudentLesson
    PASS
    ok      happylearn.local/app/tests/integration   4.142s

Broader focused store/service/HTTP/integration verification also passed:

    & 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./internal/teaching ./tests/integration -run 'TestAdminLessonStatus|TestAdminRead|TestTeachingAdminPublication|TestTeachingPublicationFailure|TestStudentCatalogDoesNotEnumerate' -count=1 -v
    ok      happylearn.local/app/internal/teaching    1.168s
    ok      happylearn.local/app/tests/integration   2.835s

The transition integration test proves draft -> published -> withdrawn -> archived, ancestor-archive precedence, immediate student ErrNotFound after withdrawal, and preservation of the exact immutable revision after both withdrawal and ancestor archive.

### Fresh final verification

    & 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' test ./... -count=1
    ok      happylearn.local/app/cmd/admin    5.117s
    ok      happylearn.local/app/cmd/server   4.526s
    ?       happylearn.local/app/db/migrations        [no test files]
    ok      happylearn.local/app/internal/app  4.206s
    ok      happylearn.local/app/internal/audit        5.204s
    ok      happylearn.local/app/internal/auth 8.543s
    ok      happylearn.local/app/internal/buildinfo    1.403s
    ok      happylearn.local/app/internal/platform/config      1.436s
    ok      happylearn.local/app/internal/platform/database    6.953s
    ok      happylearn.local/app/internal/platform/httpx       3.459s
    ok      happylearn.local/app/internal/platform/redisx      72.471s
    ok      happylearn.local/app/internal/platform/staticweb   3.469s
    ok      happylearn.local/app/internal/students     5.887s
    ok      happylearn.local/app/internal/teaching     4.034s
    ok      happylearn.local/app/tests/integration     31.858s

    & 'C:\tmp\happylearn-toolchain-1.26.5-24.18.0\go\bin\go.exe' vet ./...
    # exit 0; no output

    git diff --check
    # exit 0; only existing LF-to-CRLF working-copy warnings
    Test-Path .tmp
    False

### Final status self-review

- The store projects history with an indexed correlated EXISTS, avoiding an unbounded join and duplicate catalog rows.
- The mapper has exactly four lesson outcomes and tests archive precedence even when both pointer and history are present.
- Effective ancestor archival is supplied by the existing store projection, so both catalog and lesson detail agree.
- published is intentionally retained only as the documented current-pointer compatibility boolean; HasRevisions stays internal.
- The final full suite includes all concurrent student-remediation changes, which were preserved without modification by this status fix.
- .git is read-only in this environment, so no staging or commit was attempted.