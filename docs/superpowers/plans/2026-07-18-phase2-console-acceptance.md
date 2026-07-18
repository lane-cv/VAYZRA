# Phase 2 Teaching Console and Acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the teacher teaching/file console and the student hybrid learning space, then prove the complete Phase 2 behavior in browsers, containers, security checks, and target-resource smoke tests.

**Architecture:** Extend the existing Vue console with feature-scoped API clients and components. A single safe Markdown/KaTeX rendering module serves both teacher preview and student reading. Browser tests drive a disposable PostgreSQL/Redis/MinIO/worker/app stack and exercise real upload, conversion, publication, authorization, Range, and progress flows.

**Tech Stack:** Vue 3, TypeScript, Vite, Vitest, Vue Test Utils, Vue Router, `markdown-it`, `dompurify`, `katex`, Playwright, Go app/worker images, Docker Compose.

## Global Constraints

- Preserve existing login, forced password change, role routing, no-store client behavior, accessible console layout, and mobile navigation.
- Teacher layout supports an empty catalog and one teacher; student layout supports 1–50 students.
- Markdown raw HTML is disabled; sanitized output is the only content passed to `v-html`; KaTeX trust is false.
- Autosave exposes saving/saved/failed/conflict states and never silently overwrites a newer draft.
- Upload UI resumes persisted 8 MiB parts and allows at most two in-flight requests.
- Student layout is the approved hybrid: top filters, chapter/course navigation, reader panel, collapsible mobile navigation.
- No student UI for completion rate, grades, groups, archive internals, drafts, audiences, object keys, or processing internals.
- External video accepts only server-validated HTTPS URLs and uses restricted sandbox plus safe fallback link.
- Use TDD, accessibility tests, desktop/mobile browser tests, and focused commits.

---

## Locked File Structure

- `web/src/features/teaching/types.ts`, `api.ts`: Phase 2 transport contracts.
- `web/src/features/teaching/TeachingManagerView.vue`, `CatalogTree.vue`: teacher catalog shell.
- `web/src/features/teaching/LessonEditorView.vue`, `MarkdownPreview.vue`: safe editor, preview, autosave, publish.
- `web/src/features/teaching/AudiencePicker.vue`, `UploadPanel.vue`, `ExternalVideoEditor.vue`: draft resources.
- `web/src/features/files/FileCenterView.vue`, `api.ts`: teacher file center.
- `web/src/features/learning/LearningView.vue`, `LessonReader.vue`, `ExternalVideoFrame.vue`, `api.ts`: student experience.
- `web/src/features/teaching/renderMarkdown.ts`: shared safe Markdown/KaTeX renderer.
- `web/src/router/index.ts`, `web/src/layouts/ConsoleLayout.vue`: routes and navigation.
- `tests/e2e/teaching.spec.ts`, `tests/e2e/files.spec.ts`, `tests/e2e/learning.spec.ts`: end-to-end proof.
- `scripts/e2e-phase2.sh`, `.github/workflows/verify.yml`, `docs/runbooks/local-development.md`: acceptance automation.

### Task 1: Safe rendering and teacher catalog shell

**Files:**
- Create: `web/src/features/teaching/types.ts`
- Create: `web/src/features/teaching/api.ts`
- Create: `web/src/features/teaching/renderMarkdown.ts`
- Create: `web/src/features/teaching/renderMarkdown.test.ts`
- Create: `web/src/features/teaching/TeachingManagerView.vue`
- Create: `web/src/features/teaching/TeachingManagerView.test.ts`
- Create: `web/src/features/teaching/CatalogTree.vue`
- Create: `web/src/features/teaching/CatalogTree.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/router/index.test.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/layouts/ConsoleLayout.test.ts`
- Modify: `web/package.json`, `pnpm-lock.yaml`

**Interfaces:**
- Consumes: Plan 1 teacher JSON APIs and existing `apiRequest`/session/router patterns.
- Produces: safe renderer, `/admin/teaching` route, catalog operations, and selected lesson navigation.

- [ ] **Step 1: Write RED renderer security tests**

```ts
it.each([
  ['<img src=x onerror=alert(1)>', 'onerror'],
  ['[x](javascript:alert(1))', 'javascript:'],
  ['<script>alert(1)</script>', '<script'],
  ['\\href{javascript:alert(1)}{x}', 'javascript:'],
])('removes active content from %s', (source, forbidden) => {
  expect(renderMarkdown(source).toLowerCase()).not.toContain(forbidden)
})

it('renders inline and display math with katex trust disabled', () => {
  expect(renderMarkdown('$x^2$\n\n$$F=ma$$')).toContain('katex')
})
```

- [ ] **Step 2: Run focused frontend tests to verify RED**

Run: `pnpm --dir web test -- renderMarkdown TeachingManagerView CatalogTree`

Expected: FAIL because feature files and route are absent.

- [ ] **Step 3: Implement the safe renderer and exact transport types**

Configure `markdown-it({html:false, linkify:true, breaks:true})`; reject oversized input before parsing; render math with KaTeX `throwOnError:false`, `trust:false`, and strict macros; sanitize with DOMPurify allowlists; force external links to `rel="noopener noreferrer"` and no referrer.

```ts
export interface LessonDraft {
  lessonId: string; chapterId: string; title: string; summary: string
  bodyMarkdown: string; sortKey: number; lockVersion: number
  audience: { mode: 'all' | 'selected'; studentIds: string[] }
  externalVideos: ExternalVideo[]; updatedAt: string
}
export const saveDraft = (draft: LessonDraft) => apiRequest<LessonDraft>(
  `/api/v1/admin/lessons/${draft.lessonId}/draft`,
  { method: 'PUT', body: JSON.stringify(draft) },
)
```

- [ ] **Step 4: Implement accessible empty-state catalog shell**

Catalog tree supports keyboard selection, create/rename/archive dialogs, explicit parent type, drag controls with button alternatives “上移/下移”, loading/error/empty states, and confirmation before archiving nonempty nodes. Never use array indexes as keys.

Add teacher navigation “教学管理” and route guard. Student role must never render the link and direct route remains protected by router plus backend.

- [ ] **Step 5: Run tests and quality checks**

Run:

```bash
pnpm --dir web test -- renderMarkdown TeachingManagerView CatalogTree router ConsoleLayout
pnpm --dir web typecheck
pnpm --dir web build
```

Expected: all pass; accessibility queries find labeled tree/actions; malicious Markdown tests pass.

- [ ] **Step 6: Commit teacher catalog shell**

```bash
git add web/package.json pnpm-lock.yaml web/src/features/teaching web/src/router web/src/layouts
git commit -m "feat: add teaching catalog console"
```

### Task 2: Lesson editor, audience, uploads, publication, and file center

**Files:**
- Create: `web/src/features/teaching/LessonEditorView.vue`
- Create: `web/src/features/teaching/LessonEditorView.test.ts`
- Create: `web/src/features/teaching/MarkdownPreview.vue`
- Create: `web/src/features/teaching/AudiencePicker.vue`
- Create: `web/src/features/teaching/AudiencePicker.test.ts`
- Create: `web/src/features/teaching/UploadPanel.vue`
- Create: `web/src/features/teaching/UploadPanel.test.ts`
- Create: `web/src/features/teaching/uploadManager.ts`
- Create: `web/src/features/teaching/uploadManager.test.ts`
- Create: `web/src/features/teaching/ExternalVideoEditor.vue`
- Create: `web/src/features/files/api.ts`
- Create: `web/src/features/files/FileCenterView.vue`
- Create: `web/src/features/files/FileCenterView.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`

**Interfaces:**
- Consumes: Plans 1–3 teacher lesson/upload/file-center APIs and Task 1 renderer.
- Produces: complete teacher authoring and file lifecycle UI.

- [ ] **Step 1: Write RED autosave, conflict, upload-resume, and publish tests**

Use fake timers and mocked fetch to assert 800 ms debounced autosave, saving/saved/failed labels, no overlapping saves, `409 draft_conflict` stops autosave and offers reload, navigation warns only for unsaved local changes, and publish button remains disabled while blockers exist.

For upload manager, assert 8 MiB slicing, at most two concurrent PUTs, persisted session lookup, skipping completed parts after remount, retry with bounded jitter, pause/cancel, exact SHA-256 metadata, and no whole-file `arrayBuffer()` call.

- [ ] **Step 2: Run tests to verify RED**

Run: `pnpm --dir web test -- LessonEditor AudiencePicker UploadPanel uploadManager FileCenter`

Expected: FAIL because components are absent.

- [ ] **Step 3: Implement editor and audience behavior**

Use a local draft state machine:

```ts
type SaveState =
  | { kind: 'clean'; version: number }
  | { kind: 'dirty'; version: number }
  | { kind: 'saving'; version: number }
  | { kind: 'failed'; version: number; message: string }
  | { kind: 'conflict'; serverVersion: number }
```

Serialize autosaves; after a successful save, immediately save again if changes arrived mid-request. Audience picker loads paginated students, supports `all|selected`, searchable individual checkboxes, selected count, and removal of disabled/missing students surfaced by server preflight.

- [ ] **Step 4: Implement resumable upload and publication panel**

Compute file SHA-256 in a Web Worker or incremental browser-compatible hasher so the main thread remains responsive. Create/resume server sessions, upload two parts concurrently, persist only opaque upload/session IDs in IndexedDB keyed by file fingerprint, and discard state after completion/cancel/expiry.

Show states: hashing, uploading percentage, paused, scanning, converting, ready, rejected, failed. Teachers select preview/download explicitly. If unsupported-preview video is set to preview-only, show the server blocker and prevent publication; never auto-change policy.

Publish opens a preflight dialog listing catalog, audience, Markdown, file, preview, and external-video blockers. Success refreshes revision history; failure retains editor state.

- [ ] **Step 5: Implement file center**

Add `/admin/files`, filters, cursor pagination, reference detail, sanitized failure categories, retry, replace, rollback, and delete request. Confirm destructive actions; render exact course references; handle `409 file_in_use` and `410 file_version_expired` with actionable Chinese messages.

- [ ] **Step 6: Run frontend gate**

Run:

```bash
pnpm --dir web test
pnpm --dir web typecheck
pnpm --dir web build
```

Expected: all tests pass with no unhandled promise rejections.

- [ ] **Step 7: Commit lesson authoring and file center**

```bash
git add web/src/features/teaching web/src/features/files web/src/router web/src/layouts
git commit -m "feat: add lesson authoring and file center"
```

### Task 3: Student hybrid learning space

**Files:**
- Create: `web/src/features/learning/api.ts`
- Create: `web/src/features/learning/LearningView.vue`
- Create: `web/src/features/learning/LearningView.test.ts`
- Create: `web/src/features/learning/LessonReader.vue`
- Create: `web/src/features/learning/LessonReader.test.ts`
- Create: `web/src/features/learning/ExternalVideoFrame.vue`
- Create: `web/src/features/learning/ExternalVideoFrame.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/features/home/StudentHomeView.vue`

**Interfaces:**
- Consumes: Plan 1 student catalog/search/progress APIs, Plan 2 file endpoints, shared safe renderer.
- Produces: `/student/learning` hybrid layout and recent-learning home link.

- [ ] **Step 1: Write RED authorized browse and responsive interaction tests**

Assert empty state, top grade/term/subject filters, chapter/lesson selection, search result highlighting without unsafe HTML, reader loading/error/not-found, file preview/download controls by policy, video `<video>` Range source, external sandbox attributes, mobile drawer Escape/focus restore, and 1-second throttled progress updates with final flush on navigation.

- [ ] **Step 2: Run tests to verify RED**

Run: `pnpm --dir web test -- LearningView LessonReader ExternalVideoFrame StudentHome router`

Expected: FAIL because learning components and routes are absent.

- [ ] **Step 3: Implement approved hybrid layout**

Top filters update URL query parameters. Chapter/lesson navigation uses semantic lists and keeps selection in route params. Reader renders only server-authorized current revision. Search waits 250 ms, cancels stale requests, and never combines results across filter/audience boundaries.

Mobile navigation uses a modal drawer with focus trap, background inert, Escape close, and focus return. Desktop uses persistent navigation and reader columns. Preserve readable line length and keyboard access.

- [ ] **Step 4: Implement files, external video, and progress**

PDF/images/Office previews open a same-origin viewer; videos use the preview endpoint and native controls; download buttons exist only for `download` policy. External frames use `sandbox="allow-scripts allow-forms allow-presentation"`, `referrerpolicy="no-referrer"`, a narrow `allow` list, no `allow-same-origin`, and an explicit new-window fallback.

Progress sends revision ID, heading anchor, scroll ratio, and observed timestamp at most once per second and on route leave/page hide. Treat failures as nonblocking and retry only the newest state.

- [ ] **Step 5: Run student UI gate**

Run:

```bash
pnpm --dir web test
pnpm --dir web typecheck
pnpm --dir web build
```

Expected: all pass at desktop and mobile component widths.

- [ ] **Step 6: Commit student learning space**

```bash
git add web/src/features/learning web/src/features/home/StudentHomeView.vue web/src/router web/src/layouts
git commit -m "feat: add student learning space"
```

### Task 4: Full-stack acceptance, security, CI, and operations

**Files:**
- Create: `tests/e2e/teaching.spec.ts`
- Create: `tests/e2e/files.spec.ts`
- Create: `tests/e2e/learning.spec.ts`
- Modify: `tests/e2e/helpers.ts`
- Create: `tests/fixtures/teaching/README.md`
- Create: `scripts/generate-phase2-fixtures.sh`
- Create: `scripts/e2e-phase2.sh`
- Modify: `playwright.config.ts`
- Modify: `.github/workflows/verify.yml`
- Modify: `Dockerfile`
- Modify: `deploy/compose.dev.yml`
- Modify: `docs/runbooks/local-development.md`
- Create: `docs/runbooks/phase2-files.md`

**Interfaces:**
- Consumes: all Phase 2 backend, worker, and Vue deliverables.
- Produces: reproducible disposable acceptance and target-server deployment instructions.

- [ ] **Step 1: Write RED browser acceptance scenarios**

Scenarios must create a teacher and two students and prove:

1. Empty catalog → five-level path → Markdown/LaTeX draft → all-student publish.
2. Edit published draft while student still sees old version, then publish new version.
3. Selected-student lesson visible to A and indistinguishable from missing for B.
4. Interrupted multipart upload resumes after browser reload.
5. DOCX processing produces preview; EICAR/ZIP/macro/type mismatch are rejected.
6. Preview-only download returns `404`; allowed download succeeds.
7. MP4 seeking causes a `206` Range response; unsupported video requires download policy.
8. File replace and rollback operate on draft while current publication remains stable.
9. Disabled student and withdrawn lesson lose access immediately.
10. Desktop and mobile hybrid navigation, search, recent lesson, and reading position work.

- [ ] **Step 2: Run E2E to verify RED**

Run: `pnpm exec playwright test tests/e2e/teaching.spec.ts tests/e2e/files.spec.ts tests/e2e/learning.spec.ts`

Expected: FAIL until the disposable Phase 2 environment and fixtures are wired.

- [ ] **Step 3: Build bounded disposable acceptance script**

`scripts/e2e-phase2.sh` must create uniquely named PostgreSQL, Redis, MinIO, worker, and app resources without host data reuse; wait with bounded deadlines; initialize buckets; migrate; create teacher; run Playwright; print container logs/inspect on failure; and clean only its own resources in a trap. Never map database/Redis/MinIO publicly.

Generate DOCX, MP4, rejected archive/macro/type-mismatch, and EICAR fixtures inside the disposable Linux test environment with `scripts/generate-phase2-fixtures.sh`; do not store malware signatures or large binaries in Git or create them on the Windows host. Document antivirus expectations and delete the disposable fixture volume in the cleanup trap.

- [ ] **Step 4: Extend CI and image hardening**

CI on `master`, `main`, and pull requests runs backend unit/integration/race/vet/vulnerability; frontend test/type/build/audit; worker unit/image smoke; Compose validation; and Phase 2 E2E. Pin action and service versions. Upload Playwright traces and sanitized container logs only on failure.

Final app/worker images run non-root, read-only where applicable, with health checks and no embedded secrets. Verify Compose resource limits fit 2 cores/4 GB and processing concurrency remains 1.

- [ ] **Step 5: Write exact runbooks**

Document required environment variables, MinIO volume/bucket initialization, accepted/rejected file matrix, upload recovery, stuck-job inspection, retry, converter/scanner diagnostics, 30-day rollback/cleanup, private-network verification, backup implications, and rollback to Phase 1 without deleting MinIO data.

- [ ] **Step 6: Run final verification**

Run:

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
govulncheck ./...
pnpm --dir web test
pnpm --dir web typecheck
pnpm --dir web build
pnpm audit --prod
docker compose -f deploy/compose.dev.yml config --quiet
docker build -t happylearn:phase2 .
docker build -f Dockerfile.worker -t happylearn-worker:phase2 .
bash scripts/e2e-phase2.sh
git diff --check
```

Expected: every command exits 0; browser suite passes all desktop/mobile scenarios; images show non-root users; no reachable vulnerabilities or known production dependency vulnerabilities; worktree contains only intended changes.

- [ ] **Step 7: Commit acceptance proof**

```bash
git add tests scripts .github/workflows/verify.yml Dockerfile Dockerfile.worker deploy docs/runbooks playwright.config.ts
git commit -m "test: prove phase 2 teaching acceptance"
```

## Final Phase 2 Gate

1. Generate a complete diff from the Phase 2 starting commit through the final acceptance commit.
2. Request independent spec-compliance, code/security, deployment/operations, and test/acceptance reviews.
3. Fix every Critical/Important finding with focused TDD and rerun the complete verification list.
4. Record nonblocking Minor findings in the durable progress ledger.
5. Do not begin Phase 3 until all four review dimensions are approved and the repository is clean.
