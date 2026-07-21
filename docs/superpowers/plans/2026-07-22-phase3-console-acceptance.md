# HappyLearn Phase 3 QA Console and Acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver responsive student and teacher Q&A consoles, a real notification center with safe polling, and full-stack proof of Phase 3 privacy, consistency, files, operations, and deployment constraints.

**Architecture:** Typed Vue API modules consume the Phase 3 endpoints. Student and teacher experiences share presentation-only timeline and attachment components while retaining separate role-specific stores and routes; a root notification store owns exactly one visibility-aware polling lifecycle per authenticated application.

**Tech Stack:** Vue 3.5, TypeScript 5.9, Vite 8, Pinia 3, Vue Router 4, Vitest 4, Playwright 1.57, Go 1.26.5, Docker Compose.

## Global Constraints

- Student render trees and responses never include teacher notes, other students, object keys, or admin navigation.
- Messages are plain text rendered with escaped content and CSS `white-space: pre-wrap`; never use `v-html` for Q&A bodies.
- Every async state has accessible loading, empty, error, retry, and request-ID behavior.
- Desktop teacher queue uses list/detail panes; mobile uses accessible list-to-detail navigation; student timeline stays single-column.
- Notification polling is 15 seconds, pauses while hidden, refreshes on visibility/focus, and is cleaned on logout/unmount/account change.
- Passwords, session tokens, message/note bodies, attachment names, and real student data never enter logs, snapshots, fixtures, or CI artifacts.
- Add and enforce frontend lint as part of the cross-phase quality gate.

---

### Task 1: Build the Student Question List, Composer, and Timeline

**Files:**
- Create: `web/src/features/questions/types.ts`
- Create: `web/src/features/questions/studentApi.ts`
- Create: `web/src/features/questions/studentApi.test.ts`
- Create: `web/src/features/questions/QuestionTimeline.vue`
- Create: `web/src/features/questions/QuestionTimeline.test.ts`
- Create: `web/src/features/questions/QuestionAttachmentUploader.vue`
- Create: `web/src/features/questions/QuestionAttachmentUploader.test.ts`
- Create: `web/src/features/questions/StudentQuestionListView.vue`
- Create: `web/src/features/questions/StudentQuestionListView.test.ts`
- Create: `web/src/features/questions/NewQuestionView.vue`
- Create: `web/src/features/questions/NewQuestionView.test.ts`
- Create: `web/src/features/questions/StudentQuestionDetailView.vue`
- Create: `web/src/features/questions/StudentQuestionDetailView.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/router/index.test.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/layouts/ConsoleLayout.test.ts`

**Interfaces:**
- Produces routes `/student/questions`, `/student/questions/new`, and `/student/questions/:questionId`.
- Produces `QuestionThread`, `QuestionMessage`, `QuestionAttachment`, `ThreadPage`, and `MessagePage` types.
- Reuses `createUploadManager` with a Q&A transport prefix and purpose-specific IndexedDB key namespace.

- [ ] **Step 1: Write RED typed API and escaping tests**

Define:

```ts
export type QuestionStatus = 'pending'|'in_progress'|'waiting_student'|'completed'
export type QuestionAttachment = { fileVersionId:string; displayName:string; detectedMime:string; size:number; previewAvailable:boolean }
export type QuestionMessage = { id:string; senderRole:'admin'|'student'; body:string; createdAt:string; attachments:QuestionAttachment[] }
export type QuestionThread = { id:string; title:string; status:QuestionStatus; version:number; lastMessageAt:string; createdAt:string }
export type AttachmentInput = { fileVersionId:string; sortPosition:number }
export type QuestionDetail = { thread:QuestionThread; messages:QuestionMessage[]; nextMessageCursor?:string }
export type ThreadPage = { items:QuestionThread[]; nextCursor?:string }
export type MessagePage = { items:QuestionMessage[]; nextCursor?:string }
```

Test URL encoding, cursor/status query construction, random UUID-format idempotency headers, strict mutation bodies, APIError propagation, and that `<img src=x onerror=...>` renders as text with no image element or `innerHTML` use.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `pnpm --dir web test -- studentApi QuestionTimeline`  
Expected: FAIL because the modules/components do not exist.

- [ ] **Step 3: Implement the API and shared safe timeline**

Provide:

```ts
listStudentQuestions(filters,cursor?,signal?): Promise<ThreadPage>
createQuestion(input:{title:string;body:string;attachments:AttachmentInput[]}, key:string): Promise<QuestionDetail>
getStudentQuestion(id:string,signal?): Promise<QuestionDetail>
addStudentMessage(id:string,input:{body:string;attachments:AttachmentInput[]},key:string): Promise<QuestionDetail>
```

Render bodies via `{{ message.body }}` inside an element with `white-space:pre-wrap`. Attachment links use only server-provided same-origin `/api/v1/question-files/...` URLs and safe labels.

- [ ] **Step 4: Adapt upload transport without cloning the manager**

Create a transport that targets `/student/question-uploads`. Store resume IDs under keys prefixed `qa:student:<userId>:` so teaching and Q&A sessions cannot collide. Enforce client-side count/size feedback, but treat server validation as authoritative. Poll processing through a dedicated safe status endpoint rather than the admin file center.

- [ ] **Step 5: Write RED view workflow tests**

Test list filters/cursor, empty/error/retry, new-question validation, upload pending/ready/rejected, disabled submit while pending, duplicate-click idempotency, cleared composer after success, chronological timeline, follow-up on completed thread, request-ID support text, focus management, and absence of teacher-note labels/data.

- [ ] **Step 6: Implement student views and role routes**

Use a single-column detail layout. Route guards remain backend-independent usability checks; backend remains authoritative. Add student sidebar links “老师答疑” and “通知中心”. On successful creation navigate with `router.replace('/student/questions/'+id)`; on reply update from the returned server detail rather than optimistic duplicate insertion.

- [ ] **Step 7: Run the student UI gate**

Run:

```bash
pnpm --dir web test -- questions router ConsoleLayout
pnpm --dir web typecheck
pnpm --dir web build
```

Expected: PASS with escaped bodies, no admin link in student snapshots, and no TypeScript errors.

- [ ] **Step 8: Commit the student question console**

```bash
git add web/src/features/questions web/src/router web/src/layouts
git commit -m "feat: add student teacher-question console"
```

---

### Task 2: Build the Teacher Queue, Reply Workflow, Statuses, and Notes

**Files:**
- Create: `web/src/features/questions/adminApi.ts`
- Create: `web/src/features/questions/adminApi.test.ts`
- Create: `web/src/features/questions/TeacherQuestionListView.vue`
- Create: `web/src/features/questions/TeacherQuestionListView.test.ts`
- Create: `web/src/features/questions/TeacherQuestionDetailView.vue`
- Create: `web/src/features/questions/TeacherQuestionDetailView.test.ts`
- Create: `web/src/features/questions/TeacherNotePanel.vue`
- Create: `web/src/features/questions/TeacherNotePanel.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/router/index.test.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/layouts/ConsoleLayout.test.ts`

**Interfaces:**
- Produces `/admin/questions` and `/admin/questions/:questionId`.
- Produces `AdminQuestionDetail = QuestionDetail & { student:{id:string;username:string;displayName:string}; notes:TeacherNote[] }` only in the admin API module.

- [ ] **Step 1: Write RED admin API and privacy type tests**

Test exact status/student/date/cursor query encoding, reply idempotency, expected-version status body, conflict mapping, and note creation. Keep `TeacherNote` out of `types.ts`; define it in `adminApi.ts` so student imports cannot acquire note fields accidentally.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `pnpm --dir web test -- adminApi TeacherQuestion`  
Expected: FAIL because teacher modules and views are absent.

- [ ] **Step 3: Implement the teacher API and queue state**

Provide:

```ts
listAdminQuestions(filters,cursor?,signal?): Promise<AdminThreadPage>
getAdminQuestion(id,signal?): Promise<AdminQuestionDetail>
replyToQuestion(id,body,attachments,key,expectedVersion): Promise<AdminQuestionDetail>
changeQuestionStatus(id,status,expectedVersion): Promise<AdminQuestionDetail>
addTeacherNote(id,body): Promise<TeacherNote>
```

Use abort controllers when filters/selection change so stale responses cannot overwrite the current detail.

- [ ] **Step 4: Write RED desktop/mobile workflow tests**

Test filtering, equal-time cursor pages, selection, loading/error isolation per pane, reply, status badges, complete/reopen confirmations, stale-version reload prompt, Q&A attachment upload, note append, exact “仅老师可见” label, keyboard focus after mobile navigation, and no note text appearing in timeline DOM.

- [ ] **Step 5: Implement responsive queue and detail views**

At widths above 900 px use a list/detail grid; below it route to the detail view and show a labelled back control. Keep message replies and notes as separate forms. Completed/reopen/status actions require confirmation naming the student and thread title but never repeat message text.

- [ ] **Step 6: Run teacher UI, type, and build gates**

Run:

```bash
pnpm --dir web test -- TeacherQuestion TeacherNote adminApi router ConsoleLayout
pnpm --dir web typecheck
pnpm --dir web build
```

Expected: PASS with admin-only routes/links and no notes in shared/student components.

- [ ] **Step 7: Commit the teacher queue**

```bash
git add web/src/features/questions web/src/router web/src/layouts
git commit -m "feat: add teacher question workflow"
```

---

### Task 3: Add the Notification Center and One Safe Polling Lifecycle

**Files:**
- Create: `web/src/features/notifications/types.ts`
- Create: `web/src/features/notifications/api.ts`
- Create: `web/src/features/notifications/api.test.ts`
- Create: `web/src/stores/notifications.ts`
- Create: `web/src/stores/notifications.test.ts`
- Create: `web/src/features/notifications/NotificationCenterView.vue`
- Create: `web/src/features/notifications/NotificationCenterView.test.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/layouts/ConsoleLayout.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/router/index.test.ts`
- Modify: `web/src/main.ts`

**Interfaces:**
- Produces role-neutral `/notifications` route and `useNotificationStore`.
- Store methods: `start(userId)`, `stop()`, `refresh()`, `list(cursor?)`, `markRead(id)`, and `markAllRead()`.

- [ ] **Step 1: Write RED fake-timer lifecycle tests**

With Vitest fake timers, prove:

1. `start` performs one immediate refresh and schedules exactly one 15,000 ms interval.
2. Calling `start` twice for the same user does not add another timer.
3. `document.hidden=true` pauses timer fetches.
4. `visibilitychange` to visible and window `focus` trigger one coalesced refresh.
5. `stop`, logout, user ID change, and unmount abort in-flight requests and leave zero timers/listeners.
6. A slow request never overlaps another count request.

- [ ] **Step 2: Run polling tests to verify RED**

Run: `pnpm --dir web test -- notifications`  
Expected: FAIL because the store and API are absent.

- [ ] **Step 3: Implement API and polling store**

Use `setInterval` only while authenticated and visible; keep a single controller/promise guard. Do not persist notification data in localStorage. On 401 rely on the existing global unauthorized handler and stop polling. Cap the displayed badge at `99+` while preserving the numeric accessible label.

- [ ] **Step 4: Implement notification list and safe links**

Render title/summary as escaped text. Accept target paths only when they begin with `/student/` for students or `/admin/` for admins; invalid paths render without a link. Mark read before internal navigation, but navigate even if the idempotent mark call fails. Support cursor pagination, one read, all read, loading/empty/error, and request ID.

- [ ] **Step 5: Replace the header placeholder**

Replace hard-coded “消息 0” with a router link and live badge. Start the store from the authenticated console shell and stop it before clearing the session during logout. Add “通知中心” to both role menus without leaking admin paths to students.

- [ ] **Step 6: Run notification and full frontend gates**

Run:

```bash
pnpm --dir web test
pnpm --dir web typecheck
pnpm --dir web build
```

Expected: all tests pass; fake timers report no leaks; production build succeeds.

- [ ] **Step 7: Commit notifications UI**

```bash
git add web/src/features/notifications web/src/stores/notifications.ts web/src/stores/notifications.test.ts web/src/layouts web/src/router web/src/main.ts
git commit -m "feat: add polling notification center"
```

---

### Task 4: Prove Full-Stack Phase 3 Acceptance and Operations

**Files:**
- Create: `tests/e2e/questions.spec.ts`
- Create: `tests/e2e/notifications.spec.ts`
- Modify: `tests/e2e/helpers.ts`
- Create: `scripts/e2e-phase3.sh`
- Create: `scripts/e2e-phase3_contract_test.sh`
- Modify: `scripts/copy-e2e-workspace.sh`
- Modify: `playwright.config.ts`
- Modify: `.github/workflows/verify.yml`
- Modify: `package.json`
- Modify: `web/package.json`
- Create: `eslint.config.js`
- Modify: `pnpm-lock.yaml`
- Create: `docs/runbooks/phase3-qa-notifications.md`
- Modify: `docs/runbooks/local-development.md`
- Modify: `Makefile`

**Interfaces:**
- Produces `make e2e-phase3` and `scripts/e2e-phase3.sh`.
- Produces `pnpm lint` at workspace root and `pnpm --dir web lint`.
- Consumes all Phase 1–3 application, worker, PostgreSQL, Redis, MinIO, Vue, and Playwright deliverables.

- [ ] **Step 1: Write RED browser acceptance scenarios**

Scenarios create one teacher and two students and prove:

1. Student A creates a question with ready image/PDF attachment and sees an immutable timeline.
2. Teacher receives one notification, filters the queue, claims, replies, adds a private note, and completes.
3. Student A receives one reply notification, cannot see the note, follows up, and status returns to `pending`.
4. Teacher reopens a completed thread and replies again.
5. Student B receives `404` for A's thread/message/file UUIDs and sees no list/search/notification evidence.
6. Refresh/repeated click/replayed idempotency key creates one thread/message/notification.
7. All-student and selected-student lesson publications create exactly the authorized notifications.
8. Single-read and read-all update the badge and survive page reload.
9. Disabling A invalidates thread, notification, and attachment access immediately.
10. Desktop teacher dual-pane and mobile list/detail/student timeline remain keyboard accessible.

- [ ] **Step 2: Run E2E to verify RED**

Run: `pnpm exec playwright test tests/e2e/questions.spec.ts tests/e2e/notifications.spec.ts`  
Expected: FAIL until Phase 3 disposable environment, APIs, and views are wired.

- [ ] **Step 3: Add frontend lint with an exact zero-warning gate**

Install compatible pinned `eslint`, `typescript-eslint`, and Vue ESLint parser/plugin versions. Configure browser/Node/test globals, Vue essential rules, TypeScript recommended type-checked rules where supported, no floating promises, and no unused variables. Add:

```json
"lint": "eslint web/src tests/e2e playwright.config.ts --max-warnings=0"
```

Fix existing lint findings without changing behavior; do not suppress whole files or use blanket `eslint-disable` comments.

- [ ] **Step 4: Build the bounded disposable acceptance script**

Adapt the Phase 2 harness while retaining unique container/network/volume names, private networking, bounded waits, sanitized failure artifacts, read-only/non-root containers, one processing worker, and cleanup limited to resources carrying the unique prefix. Run all prior E2E suites plus Phase 3 suites. Never embed the AIStor license or real credentials.

- [ ] **Step 5: Extend CI and Make targets**

CI runs unit/integration/race/vet/vulnerability, frontend test/type/lint/build/audit, app/worker images, Compose validation, Phase 2 E2E, and Phase 3 E2E. Upload sanitized traces/logs only on failure. Add `e2e-phase3` and include lint in `make verify`.

- [ ] **Step 6: Write the exact Phase 3 runbook**

Document Q&A limits/types, stuck processing, outbox lease/retry inspection, notification dedupe verification, polling diagnosis, privacy-safe support procedure, backup/restore implications, disabling a compromised student, and rollback to Phase 2 without deleting Q&A file objects. Include only parameterized/sample commands and no secrets.

- [ ] **Step 7: Run the final verification gate**

Run:

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
govulncheck ./...
pnpm test
pnpm typecheck
pnpm lint
pnpm build
pnpm audit --prod
docker compose -f deploy/compose.dev.yml config --quiet
docker build -t happylearn:phase3 .
docker build -f Dockerfile.worker -t happylearn-worker:phase3 .
bash scripts/e2e-phase3.sh
git diff --check
```

Expected: every command exits 0; browser suites pass desktop/mobile scenarios; images remain non-root with health checks; no reachable vulnerability or production dependency vulnerability remains; no timer leak, duplicate notification, cross-student disclosure, or unreviewed Critical/Important finding remains.

- [ ] **Step 8: Commit Phase 3 acceptance proof**

```bash
git add tests/e2e scripts/e2e-phase3.sh scripts/e2e-phase3_contract_test.sh playwright.config.ts .github/workflows/verify.yml package.json web/package.json eslint.config.js pnpm-lock.yaml docs/runbooks Makefile
git commit -m "test: prove phase 3 qa acceptance"
```

## Final Phase 3 Gate

1. Generate the complete diff from commit `4dc504f` through the final Phase 3 acceptance commit.
2. Review independently for spec coverage, cross-student privacy, file authorization, transaction/idempotency safety, polling lifecycle, deployment/operations, and test adequacy.
3. Fix every Critical or Important finding with a failing regression test first.
4. Rerun the entire Step 7 verification list from a clean worktree.
5. Confirm the repository is clean and only then begin the Phase 4 design.
