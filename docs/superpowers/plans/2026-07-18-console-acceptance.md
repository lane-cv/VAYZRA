# HappyLearn Authenticated Console and Phase Acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the Sub2API-style role-aware Vue console, teacher student-management screens, a non-root production image, and end-to-end proof of authentication and authorization.

**Architecture:** A typed fetch client and Pinia session store consume the administration APIs from the preceding plans. Vue Router applies usability guards for login, first-password change, and role navigation while backend authorization remains authoritative. The Vue build is served by the Go application from one production container.

**Tech Stack:** Node.js 24 LTS, pnpm 11, Vue 3, TypeScript, Vite, Pinia, Vue Router, Element Plus, Vitest, Mock Service Worker, Playwright, Go 1.26.5, Docker.

## Global Constraints

- Execute `2026-07-18-foundation-auth.md` and `2026-07-18-admin-api.md` first.
- Use the approved high-density Sub2API-style left-sidebar layout.
- Students never see admin navigation and receive backend 403 responses for direct admin API calls.
- First-login password change overrides every normal route.
- No public registration, password recovery, social login, payment, teaching content, or Q&A is added in this phase.
- Password fields are cleared after completion/error and never enter persistent Pinia state, logs, notifications, or snapshots.
- Desktop and mobile layouts must remain keyboard accessible and labelled in Chinese.
- Use TDD, strict TypeScript, frozen lockfiles, frequent commits, and a final clean-build gate.

---

### Task 1: Build the Typed Client, Session Store, Router Guards, and Console Shell

**Files:**
- Create: `web/src/main.ts`
- Create: `web/src/api/client.ts`
- Test: `web/src/api/client.test.ts`
- Create: `web/src/stores/session.ts`
- Test: `web/src/stores/session.test.ts`
- Create: `web/src/router/index.ts`
- Test: `web/src/router/index.test.ts`
- Create: `web/src/layouts/ConsoleLayout.vue`
- Create: `web/src/features/auth/LoginView.vue`
- Test: `web/src/features/auth/LoginView.test.ts`
- Create: `web/src/features/auth/ChangePasswordView.vue`
- Test: `web/src/features/auth/ChangePasswordView.test.ts`
- Create: `web/src/features/home/AdminHomeView.vue`
- Create: `web/src/features/home/StudentHomeView.vue`
- Modify: `web/src/App.vue`

**Interfaces:**
- Consumes: `UserView` and auth endpoints from the administration API plan.
- Produces: `request<T>(path, options)`, `APIError`, `useSessionStore`, and route meta `roles`/`allowDuringPasswordChange`.
- Produces routes `/login`, `/change-password`, `/admin`, and `/student`.

- [ ] **Step 1: Write a failing typed-client test**

```ts
it('sends credentials and CSRF header on mutations', async () => {
  document.cookie = 'hl_csrf=csrf-value'
  const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{}'))
  await request('/auth/logout', { method: 'POST' })
  expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/logout', expect.objectContaining({
    credentials: 'include',
    headers: expect.objectContaining({ 'X-CSRF-Token': 'csrf-value' }),
  }))
})
```

- [ ] **Step 2: Implement the client and stable error mapping**

The client sets `Accept: application/json`, adds `Content-Type` only for a JSON body, reads only `hl_csrf` from `document.cookie`, attaches `credentials: 'include'`, and converts backend errors into:

```ts
export class APIError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly requestId: string,
  ) { super(message) }
}
```

It never logs bodies. For non-health success responses it unwraps `{"data":T}` and preserves optional `meta`; malformed envelopes raise `APIError` code `invalid_response`. HTTP 401 clears the session through a registered callback without creating a redirect loop.

- [ ] **Step 3: Write failing store and router tests**

```ts
it('forces first-login students to change password', async () => {
  const session = useSessionStore()
  session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: true }
  await router.push('/student')
  expect(router.currentRoute.value.fullPath).toBe('/change-password')
})

it('redirects a student away from admin routes', async () => {
  const session = useSessionStore()
  session.user = { id: 'u1', username: 'student01', displayName: '林同学', role: 'student', mustChangePassword: false }
  await router.push('/admin')
  expect(router.currentRoute.value.fullPath).toBe('/student')
})
```

- [ ] **Step 4: Implement session bootstrap and route guards**

On first navigation call `/auth/me` once. Unauthenticated users go to `/login`; authenticated users leave `/login`; `mustChangePassword` overrides every route except password change and logout; role mismatch redirects to that role's home. Store only the safe `UserView`, bootstrap state, and no tokens/passwords.

- [ ] **Step 5: Write failing login and change-password view tests**

Test required fields, accessible labels, Enter submission, disabled submit while pending, generic login error, successful role redirect, password confirmation, request-ID display for support, cleared password fields after failure/success, and the `login_challenge_required` path fetching and displaying an accessible CAPTCHA image with refresh and answer input.

- [ ] **Step 6: Implement login and forced password change**

The login form initially contains account and password. After `login_challenge_required`, it fetches `/auth/challenge`, shows the no-store PNG with refresh control, and submits `challengeId` plus `challengeAnswer`; it never persists the answer. The password-change form sends current password, new password, and confirmation; on success it refreshes `/auth/me` and redirects by role. Do not render registration, recovery, or social-login links.

- [ ] **Step 7: Implement the approved console shell**

Use a fixed desktop sidebar and collapsible mobile drawer. Admin navigation contains “仪表盘” and “学生管理”; student navigation contains “学习首页”. The header shows display name, unread placeholder `0`, active-session action, and logout. Student render trees contain no admin links.

- [ ] **Step 8: Run frontend tests and build**

Run: `pnpm --dir web test`  
Expected: client, store, router, login, and password-change tests PASS.  
Run: `pnpm --dir web typecheck && pnpm --dir web build`  
Expected: PASS without TypeScript errors.

- [ ] **Step 9: Commit the authenticated console**

```bash
git add web pnpm-lock.yaml
git commit -m "feat: add role-aware authenticated console"
```

---

### Task 2: Build the Teacher Student-Management Screen

**Files:**
- Create: `web/src/features/students/api.ts`
- Create: `web/src/features/students/StudentListView.vue`
- Test: `web/src/features/students/StudentListView.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`

**Interfaces:**
- Consumes: teacher-only student endpoints from `2026-07-18-admin-api.md`.
- Produces: `/admin/students` with list, create, enable/disable, and reset-password dialogs.

- [ ] **Step 1: Write the failing create-student component test**

```ts
it('creates a student without retaining the temporary password', async () => {
  server.use(http.post('/api/v1/admin/students', () => HttpResponse.json({
    data: { id: 's1', username: 'student01', displayName: '林同学', status: 'active', mustChangePassword: true }
  })))
  const wrapper = mountStudentList()
  await wrapper.get('[aria-label="创建学生"]').trigger('click')
  await wrapper.get('[aria-label="学生账号"]').setValue('student01')
  await wrapper.get('[aria-label="学生姓名"]').setValue('林同学')
  await wrapper.get('[aria-label="临时密码"]').setValue('Temporary Password 42!')
  await wrapper.get('form').trigger('submit')
  expect(wrapper.text()).toContain('student01')
  expect(wrapper.html()).not.toContain('Temporary Password 42!')
})
```

- [ ] **Step 2: Add all failing management-state tests**

Test keyset pagination, active/disabled badges, empty/loading/error states, confirmation before disable/enable/reset, request ID in error details, immediate row update after success, and absence of controls when mounted with a non-admin session.

- [ ] **Step 3: Implement typed student API functions**

```ts
export type Student = {
  id: string
  username: string
  displayName: string
  status: 'active' | 'disabled'
  mustChangePassword: boolean
  createdAt: string
}

export const listStudents = (cursor?: string) => request<Page<Student>>(`/admin/students${cursor ? `?cursor=${encodeURIComponent(cursor)}` : ''}`)
export const createStudent = (input: { username: string; displayName: string; temporaryPassword: string }) => request<Student>('/admin/students', { method: 'POST', json: input })
```

Add equivalent `setStudentStatus` and `resetStudentPassword` functions. Never return or retain the submitted temporary password.

- [ ] **Step 4: Implement the management view**

The table shows account, display name, status, first-password-change state, and creation time. Dialogs implement create, status change, and reset. Password inputs use `autocomplete="new-password"` and clear on close, error, and completion. Destructive status/reset actions require an explicit confirmation naming the target account.

- [ ] **Step 5: Run view tests, typecheck, and build**

Run: `pnpm --dir web test -- StudentListView`  
Expected: PASS.  
Run: `pnpm --dir web typecheck && pnpm --dir web build`  
Expected: PASS.

- [ ] **Step 6: Commit student management UI**

```bash
git add web/src/features/students web/src/router web/src/layouts pnpm-lock.yaml
git commit -m "feat: add teacher student-management console"
```

---

### Task 3: Package the Application and Prove Phase Acceptance

**Files:**
- Create: `tests/e2e/auth-students.spec.ts`
- Create: `tests/e2e/helpers.ts`
- Create: `playwright.config.ts`
- Create: `Dockerfile`
- Create: `internal/platform/staticweb/handler.go`
- Test: `internal/platform/staticweb/handler_test.go`
- Modify: `internal/app/app.go`
- Modify: `Makefile`
- Create: `.github/workflows/verify.yml`
- Create: `docs/runbooks/local-development.md`

**Interfaces:**
- Produces: one non-root image serving API and Vue assets.
- Produces: `make verify` and a Playwright phase-acceptance scenario.

- [ ] **Step 1: Write the failing SPA/static handler tests**

```go
func TestUnknownAPIRouteDoesNotReturnSPA(t *testing.T) {
	h := New(testFS())
	r := httptest.NewRequest(http.MethodGet, "/api/v1/missing", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "<html") { t.Fatal(w.Body.String()) }
}

func TestClientRouteReturnsIndex(t *testing.T) {
	h := New(testFS())
	r := httptest.NewRequest(http.MethodGet, "/admin/students", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") { t.Fatal(w.Body.String()) }
}
```

- [ ] **Step 2: Implement safe static serving and cache policy**

Hashed assets receive `Cache-Control: public,max-age=31536000,immutable`; `index.html` receives `no-cache`; dotfiles are inaccessible; non-GET/HEAD requests are 405; `/api/*` misses remain JSON 404s and never fall back to the SPA.

- [ ] **Step 3: Write the failing end-to-end lifecycle test**

```ts
test('teacher creates student and student is isolated from admin APIs', async ({ browser }) => {
  const admin = await browser.newContext()
  const adminPage = await admin.newPage()
  await login(adminPage, 'admin', process.env.E2E_ADMIN_PASSWORD!)
  await adminPage.goto('/admin/students')
  await createStudent(adminPage, 'student01', '林同学', process.env.E2E_STUDENT_PASSWORD!)

  const student = await browser.newContext()
  const studentPage = await student.newPage()
  await login(studentPage, 'student01', process.env.E2E_STUDENT_PASSWORD!)
  await expect(studentPage).toHaveURL(/change-password/)
  await changePassword(studentPage, process.env.E2E_STUDENT_PASSWORD!, process.env.E2E_STUDENT_NEW_PASSWORD!)
  const response = await studentPage.request.get('/api/v1/admin/students')
  expect(response.status()).toBe(403)
  await expect(studentPage.getByText('学生管理')).toHaveCount(0)
})
```

- [ ] **Step 4: Build a pinned, non-root production image**

Use Node 24 for `pnpm install --frozen-lockfile` and `pnpm build`, Go 1.26.5 for `CGO_ENABLED=0 go build -trimpath`, and a minimal runtime with a numeric non-root user. Copy only the server binary and web assets. Set a read-only root filesystem expectation; runtime writes go only to explicitly mounted paths. Do not embed `.env`, source maps containing secrets, package caches, or test credentials.

- [ ] **Step 5: Add full verification commands and CI**

```make
.PHONY: verify
verify:
	go test -race ./...
	go vet ./...
	govulncheck ./...
	pnpm test
	pnpm typecheck
	pnpm build
	docker compose -f deploy/compose.dev.yml config --quiet
```

CI runs these commands with PostgreSQL/Redis services, builds the image without pushing, and never uploads `.env` or secrets.

- [ ] **Step 6: Add security-focused API assertions**

Assert every student call to admin methods is 403; disabling revokes sessions; duplicate admin creation fails; wrong-account and wrong-password errors match; session cookie never appears in JSON; CSRF and cross-origin mutations fail; request IDs appear on errors; audit metadata contains no password/hash/token fields.

- [ ] **Step 7: Run full verification and browser acceptance**

Run: `make verify`  
Expected: all backend, frontend, static, and vulnerability checks PASS.  
Run: `pnpm exec playwright test tests/e2e/auth-students.spec.ts`  
Expected: PASS.  
Run: `docker build -t happylearn:phase1 .`  
Expected: successful image running as non-root.  
Run: `docker compose -f deploy/compose.dev.yml up -d && docker compose -f deploy/compose.dev.yml ps`  
Expected: PostgreSQL and Redis healthy; application liveness/readiness return success when started with documented test configuration.

- [ ] **Step 8: Write the local runbook and commit Phase 1**

The runbook contains prerequisites, exact Compose/start/migrate/bootstrap/test commands, test-only credential setup, shutdown, and a clearly marked destructive cleanup command limited to the named development project/volumes. It contains no real secrets.

```bash
git add tests/e2e playwright.config.ts Dockerfile internal/platform/staticweb internal/app Makefile .github/workflows/verify.yml docs/runbooks/local-development.md
git commit -m "feat: complete authenticated student administration"
```

---

## Phase 1 Completion Gate

- `make verify` passes from a clean checkout.
- Browser acceptance proves first-password change and direct admin API denial.
- PostgreSQL proves only one active administrator can exist.
- Redis failure retains bounded protection without locking out all users.
- Passwords, hashes, session/CSRF tokens do not appear in fixtures, logs, audit metadata, snapshots, or responses.
- The production image runs non-root and exposes only the application port.
- The teacher can create, list, disable/enable, and reset a student through the Vue console.
- A student can log in, change the temporary password, see only the student shell, and revoke other sessions.
- Review passes against Sections 2.1, 3, 4, 5.1, 5.4, 9, 10, 12, 14, and relevant acceptance items in the approved specification.

