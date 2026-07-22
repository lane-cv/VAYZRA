# Console/Acceptance Task 1 Implementation Report

Status: DONE

## Scope delivered

- Added the student-only question list, status filter, bounded cursor paging, new-question composer, detail timeline, message continuation, and completed-thread follow-up workflow.
- Added strict typed clients for the actual student Q&A HTTP contracts. List and message pages consume `data` plus `meta.nextCursor`; detail consumes `{thread,messages,nextMessageCursor}`; create and reply normalize the actual `{thread,message}` mutation result.
- Added UUID idempotency keys, duplicate-submit locks, request-ID error support, abortable/stale-safe reads, Unicode length checks, retryable loading/error/empty states, and error focus management.
- Rendered all message bodies through Vue text interpolation with `white-space: pre-wrap`. Attachment status, preview, and download paths are same-origin paths constructed only from encoded `fileVersionId`; no server-provided URL is trusted.
- Reused `createUploadManager` with the `/student/question-uploads` transport and isolated `qa:student:<userId>:` IndexedDB resume namespace. Client count, per-type size, aggregate size, progress, pending, ready, rejected, and timeout feedback complements authoritative `/question-files/:version/status` validation.
- Added student-only routes and navigation for “老师答疑” and “通知中心”. The notification placeholder redirects to a valid student route until its later console task lands; no notification store or teacher console was added.

## TDD evidence

1. API/timeline RED: focused tests failed because `studentApi` and `QuestionTimeline` did not exist.
2. API/timeline GREEN: URL encoding, exact mutation bodies, idempotency headers, API errors, hostile-text escaping, chronology, and safe attachment paths passed.
3. View RED: uploader/list/new/detail tests failed because the components did not exist.
4. View GREEN: upload readiness, list state/filter flow, duplicate creation, server-result navigation, completed follow-up, and absence of teacher-note UI passed.
5. Router/layout RED: route and sidebar tests failed before the student question routes/links existed.
6. Router/layout GREEN: student routes are usable, admin sessions are redirected by the existing role guard, invalid detail UUIDs are rejected client-side, and student navigation contains no admin-question link.

## Verification

- `pnpm --dir web test -- questions router ConsoleLayout` — PASS, 29 files / 121 tests.
- `pnpm --dir web test` — PASS, 29 files / 121 tests.
- `pnpm --dir web typecheck` — PASS.
- `pnpm --dir web build` — PASS.
- `git diff --check` — PASS.

## Known non-blocking warning

Vite retains the pre-existing production chunk-size warning; the final built application chunk is 592.82 kB before gzip (202.69 kB gzip). This task adds no build failure and preserves the existing bundling strategy.
