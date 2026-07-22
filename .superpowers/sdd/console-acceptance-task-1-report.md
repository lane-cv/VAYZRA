# Console/Acceptance Task 1 Implementation Report

Status: DONE

## Scope delivered

- Added the student-only question list, status filter, bounded cursor paging, new-question composer, detail timeline, message continuation, and completed-thread follow-up workflow.
- Added strict typed clients for the actual student Q&A HTTP contracts. List and message pages consume `data` plus `meta.nextCursor`; detail consumes `{thread,messages,nextMessageCursor}`; create and reply normalize the actual `{thread,message}` mutation result.
- Added UUID idempotency keys, duplicate-submit locks, request-ID error support, abortable/stale-safe reads, Unicode length checks, retryable loading/error/empty states, and error focus management.
- Rendered all message bodies through Vue text interpolation with `white-space: pre-wrap`. Attachment status and download paths are same-origin paths constructed only from encoded `fileVersionId`; no server-provided URL is trusted. Message attachments expose download only because their DTO has no authoritative preview capability.
- Reused `createUploadManager` with the `/student/question-uploads` transport and isolated `qa:student:<userId>:` IndexedDB resume namespace. Client count, per-type size, aggregate size, progress, pending, ready, rejected, and timeout feedback complements authoritative `/question-files/:version/status` validation.
- Added student-only routes and navigation for “老师答疑”. Notification navigation remains hidden until its real route/store lands atomically in the later task; no placeholder redirect, notification store, or teacher console was added.

## TDD evidence

1. API/timeline RED: focused tests failed because `studentApi` and `QuestionTimeline` did not exist.
2. API/timeline GREEN: URL encoding, exact mutation bodies, idempotency headers, API errors, hostile-text escaping, chronology, and safe attachment paths passed.
3. View RED: uploader/list/new/detail tests failed because the components did not exist.
4. View GREEN: upload readiness/lifecycle cancellation, list state/filter flow, stable mutation idempotency, duplicate creation, server-result navigation, stale-route isolation, completed follow-up, and absence of teacher-note UI passed.
5. Router/layout RED: route and sidebar tests failed before the student question routes/links existed.
6. Router/layout GREEN: student routes are usable, admin sessions are redirected by the existing role guard, invalid detail UUIDs are rejected client-side, and student navigation contains no admin-question link.

## Verification

- `pnpm --dir web test -- questions router ConsoleLayout` — PASS, 29 files / 127 tests.
- `pnpm --dir web test` — PASS, 29 files / 127 tests.
- `pnpm --dir web typecheck` — PASS.
- `pnpm --dir web build` — PASS.
- `git diff --check` — PASS.

## Known non-blocking warning

Vite retains the pre-existing production chunk-size warning; the final reviewed build is 593.94 kB before gzip (203.11 kB gzip). This task adds no build failure and preserves the existing bundling strategy.

## Independent-review remediation

- Upload lifecycle now tracks every active manager and status controller. Unmount or student identity changes advance a generation, cancel managers, abort polling/delays, stop queued files, and suppress all stale state/emissions. Tests cover teardown during hashing with a queued second file and identity switching during status polling.
- New-question and follow-up mutations now retain one idempotency key for the same trimmed text plus ordered attachment-version IDs across uncertain failures. Keys rotate only for a changed payload, successful mutation, or question route reset; concurrent submits remain locked to one request.
- Detail reads, message paging, and mutation results capture both route ID and generation. Route changes abort active reads, reset the composer/uploader/key, and reject late detail pages, message pages, replies, errors, and loading-finalizers from the previous thread.
- Removed the premature notification sidebar link and redirect. Removed unverified preview links; timeline attachments remain safe downloads until authoritative per-message preview capability is available.
