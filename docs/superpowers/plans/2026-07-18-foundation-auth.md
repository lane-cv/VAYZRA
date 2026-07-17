# HappyLearn Foundation and Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish reproducible Go/Vue workspaces, validated configuration and health checks, the authentication database, Argon2id password handling, and opaque session behavior required by the later admin API plan.

**Architecture:** A Go modular monolith serves `/api/v1` and the built Vue application. PostgreSQL is authoritative for users, sessions, login events, and immutable audit events; Redis is used only for login throttling. Authentication uses Argon2id passwords and opaque, server-side sessions in secure cookies.

**Tech Stack:** Go 1.26.5, chi, pgx v5, goose, Redis 8.8, PostgreSQL 18.4, Node.js 24 LTS, pnpm 11, Vue 3, TypeScript, Vite, Pinia, Vue Router, Element Plus, Vitest, Playwright, Docker Compose.

## Global Constraints

- Target Ubuntu 24.04 on 2 CPU cores and 4 GB RAM.
- Exactly one teacher super-administrator is supported in the UI and service layer.
- Public registration and student self-registration do not exist.
- Every student account is created by the teacher and must change its temporary password on first login.
- Passwords use Argon2id; sessions are opaque, revocable, `HttpOnly`, `Secure`, and `SameSite=Lax` in production.
- Ordinary sessions expire after 7 idle days and no later than 30 days after creation.
- State-changing browser requests require same-origin validation and a double-submit CSRF token.
- Backend authorization and ownership checks are mandatory even when a frontend menu is hidden.
- Logs and API responses never include passwords, full session tokens, CSRF tokens, or sensitive request bodies.
- PostgreSQL and Redis are attached only to a private Compose network and publish no production host ports.
- Use TDD, parameterized SQL, explicit errors, UTC database timestamps, Asia/Shanghai presentation, and frequent commits.
- Pin resolved dependencies in `go.sum` and `pnpm-lock.yaml`; do not use unbounded `latest` image tags.

---

## File and Module Map

```text
.
├── cmd/
│   ├── server/main.go                 # application entry point and graceful shutdown
│   └── admin/main.go                  # one-time teacher bootstrap command
├── db/
│   ├── migrations/                    # users, sessions, login_events, audit_logs
│   └── queries/                       # parameterized SQL grouped by module
├── deploy/
│   └── compose.dev.yml                # local PostgreSQL and Redis only
├── internal/
│   ├── app/app.go                     # composition root and HTTP router
│   ├── auth/                          # password, session, service, repository, HTTP handlers
│   ├── students/                      # teacher-only student management
│   ├── audit/                         # immutable audit writer and queries
│   └── platform/
│       ├── config/                    # validated environment configuration
│       ├── database/                  # pgx pool and migrations
│       ├── httpx/                     # JSON errors, request IDs, origin/CSRF middleware
│       └── redisx/                    # Redis client and rate limiter
├── tests/integration/                 # PostgreSQL/Redis integration tests
├── web/
│   ├── src/api/                       # typed fetch client
│   ├── src/features/auth/             # login and forced password change
│   ├── src/features/students/         # teacher student-management UI
│   ├── src/layouts/                    # Sub2API-style console shell
│   ├── src/router/                     # role-aware routes and guards
│   └── src/stores/                     # Pinia session state
├── .env.example                       # non-secret configuration contract
├── Dockerfile                         # multi-stage frontend/backend image
├── Makefile                           # repeatable build/test commands
├── go.mod
├── package.json                       # pinned pnpm/runtime policy
└── pnpm-workspace.yaml
```

Module boundaries are enforced as follows: `auth` knows users and sessions but not student screens; `students` consumes the `auth.UserStore` interface and `audit.Writer`; `audit` accepts sanitized metadata only; `platform` contains no business rules; `app` is the only package that wires concrete implementations.

---

### Task 1: Establish Reproducible Go and Vue Workspaces

**Files:**
- Create: `go.mod`
- Create: `internal/buildinfo/info.go`
- Test: `internal/buildinfo/info_test.go`
- Create: `package.json`
- Create: `pnpm-workspace.yaml`
- Create: `web/package.json`
- Create: `web/src/App.vue`
- Test: `web/src/App.test.ts`
- Create: `web/vite.config.ts`
- Create: `web/vitest.config.ts`
- Create: `web/tsconfig.json`
- Create: `Makefile`

**Interfaces:**
- Produces: `buildinfo.Name() string` and `buildinfo.Version() string` for health responses and release diagnostics.
- Produces: `pnpm test`, `pnpm typecheck`, `pnpm build`, `go test ./...`, and `make verify` as stable commands consumed by every later task.

- [ ] **Step 1: Write the failing Go build-info test**

```go
// internal/buildinfo/info_test.go
package buildinfo

import "testing"

func TestDefaults(t *testing.T) {
	if Name() != "HappyLearn" {
		t.Fatalf("Name() = %q", Name())
	}
	if Version() == "" {
		t.Fatal("Version() must not be empty")
	}
}
```

- [ ] **Step 2: Run the Go test and confirm the expected failure**

Run: `go test ./internal/buildinfo -run TestDefaults -v`  
Expected: FAIL because `go.mod` and `Name` do not exist.

- [ ] **Step 3: Create the Go module and minimal build-info implementation**

```go
// go.mod
module happylearn.local/app

go 1.26.0

toolchain go1.26.5
```

```go
// internal/buildinfo/info.go
package buildinfo

var version = "dev"

func Name() string    { return "HappyLearn" }
func Version() string { return version }
```

- [ ] **Step 4: Run the test and verify it passes**

Run: `go test ./internal/buildinfo -run TestDefaults -v`  
Expected: PASS.

- [ ] **Step 5: Write the failing Vue application smoke test**

```ts
// web/src/App.test.ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import App from './App.vue'

describe('App', () => {
  it('renders the product name', () => {
    expect(mount(App).text()).toContain('HappyLearn')
  })
})
```

- [ ] **Step 6: Create the pinned workspace and minimal Vue application**

```json
// package.json
{
  "name": "happylearn-workspace",
  "private": true,
  "engines": { "node": ">=24 <25", "pnpm": ">=11 <12" },
  "packageManager": "pnpm@11.9.0",
  "scripts": {
    "test": "pnpm --dir web test",
    "typecheck": "pnpm --dir web typecheck",
    "build": "pnpm --dir web build"
  }
}
```

```yaml
# pnpm-workspace.yaml
packages:
  - web
```

```json
// web/package.json
{
  "name": "@happylearn/web",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "test": "vitest run",
    "typecheck": "vue-tsc --noEmit",
    "build": "vue-tsc --noEmit && vite build"
  },
  "dependencies": {
    "element-plus": ">=2.11 <3",
    "pinia": ">=3 <4",
    "vue": ">=3.5 <4",
    "vue-router": ">=4.5 <5"
  },
  "devDependencies": {
    "@vitejs/plugin-vue": ">=6 <7",
    "@vue/test-utils": ">=2.4 <3",
    "jsdom": ">=26 <28",
    "typescript": ">=5.9 <6",
    "vite": ">=7 <9",
    "vitest": ">=3 <5",
    "vue-tsc": ">=3 <4"
  }
}
```

```vue
<!-- web/src/App.vue -->
<template><main><h1>HappyLearn</h1></main></template>
```

Use a standard Vue/Vite `tsconfig.json`, configure the Vue plugin in `vite.config.ts`, and configure `environment: 'jsdom'` in `vitest.config.ts`. Run `pnpm install` once and commit the generated `pnpm-lock.yaml`.

- [ ] **Step 7: Add the repeatable verification target**

```make
# Makefile
.PHONY: test-go test-web verify
test-go:
	go test ./...
test-web:
	pnpm test && pnpm typecheck && pnpm build
verify: test-go test-web
```

- [ ] **Step 8: Verify both workspaces**

Run: `go test ./...`  
Expected: PASS.  
Run: `pnpm install --frozen-lockfile=false && pnpm test && pnpm typecheck && pnpm build`  
Expected: one passing Vitest test and a successful Vite production build.

- [ ] **Step 9: Commit the reproducible skeleton**

```bash
git add go.mod go.sum internal/buildinfo package.json pnpm-workspace.yaml pnpm-lock.yaml web Makefile
git commit -m "build: initialize Go and Vue workspaces"
```

---

### Task 2: Add Validated Configuration, Request IDs, and Health Endpoints

**Files:**
- Create: `internal/platform/config/config.go`
- Test: `internal/platform/config/config_test.go`
- Create: `internal/platform/httpx/errors.go`
- Create: `internal/platform/httpx/request_id.go`
- Test: `internal/platform/httpx/request_id_test.go`
- Create: `internal/app/app.go`
- Test: `internal/app/app_test.go`
- Create: `cmd/server/main.go`
- Create: `.env.example`

**Interfaces:**
- Produces: `config.Load(getenv func(string) string) (config.Config, error)`.
- Produces: `app.New(app.Dependencies) http.Handler`.
- Produces: stable endpoints `GET /api/v1/health/live` and `GET /api/v1/health/ready`.
- Produces: JSON error shape `{"error":{"code":string,"message":string,"requestId":string}}`.

- [ ] **Step 1: Write failing configuration tests**

```go
func TestLoadRejectsMissingRequiredValues(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_DATABASE_URL") {
		t.Fatalf("expected missing database URL error, got %v", err)
	}
}

func TestLoadUsesSessionDurationsFromSpec(t *testing.T) {
	env := map[string]string{
		"HAPPYLEARN_DATABASE_URL": "postgres://app:test@localhost/app",
		"HAPPYLEARN_REDIS_URL": "redis://localhost:6379/0",
		"HAPPYLEARN_PUBLIC_ORIGIN": "https://learn.example.com",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil { t.Fatal(err) }
	if cfg.SessionIdleTTL != 7*24*time.Hour || cfg.SessionAbsoluteTTL != 30*24*time.Hour {
		t.Fatalf("unexpected session TTLs: %#v", cfg)
	}
}
```

- [ ] **Step 2: Run the test and confirm failure**

Run: `go test ./internal/platform/config -v`  
Expected: FAIL because `Load` is undefined.

- [ ] **Step 3: Implement typed configuration without secret defaults**

```go
type Config struct {
	Environment        string
	ListenAddress      string
	DatabaseURL        string
	RedisURL           string
	PublicOrigin       string
	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration
	CookieSecure       bool
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{
		Environment: "development", ListenAddress: ":8080",
		SessionIdleTTL: 7 * 24 * time.Hour,
		SessionAbsoluteTTL: 30 * 24 * time.Hour,
	}
	if v := getenv("HAPPYLEARN_ENV"); v != "" { c.Environment = v }
	if v := getenv("HAPPYLEARN_LISTEN"); v != "" { c.ListenAddress = v }
	c.DatabaseURL = getenv("HAPPYLEARN_DATABASE_URL")
	c.RedisURL = getenv("HAPPYLEARN_REDIS_URL")
	c.PublicOrigin = strings.TrimRight(getenv("HAPPYLEARN_PUBLIC_ORIGIN"), "/")
	c.CookieSecure = c.Environment == "production"
	for name, value := range map[string]string{
		"HAPPYLEARN_DATABASE_URL": c.DatabaseURL,
		"HAPPYLEARN_REDIS_URL": c.RedisURL,
		"HAPPYLEARN_PUBLIC_ORIGIN": c.PublicOrigin,
	} {
		if value == "" { return Config{}, fmt.Errorf("%s is required", name) }
	}
	return c, nil
}
```

`.env.example` must contain only documented placeholders, never working secrets.

- [ ] **Step 4: Write failing router tests**

```go
func TestLivenessIncludesRequestID(t *testing.T) {
	h := New(Dependencies{Ready: func(context.Context) error { return nil }})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK { t.Fatalf("status = %d", w.Code) }
	if w.Header().Get("X-Request-ID") == "" { t.Fatal("missing request ID") }
	if !strings.Contains(w.Body.String(), `"status":"ok"`) { t.Fatal(w.Body.String()) }
}
```

- [ ] **Step 5: Implement request IDs, stable errors, and health routes**

Use `github.com/go-chi/chi/v5`. `request_id.go` must accept a client request ID only when it matches `^[A-Za-z0-9_-]{8,64}$`; otherwise generate 16 random bytes encoded as hex. Store it in request context and return it as `X-Request-ID`.

```go
type Dependencies struct { Ready func(context.Context) error }

func New(d Dependencies) http.Handler {
	r := chi.NewRouter()
	r.Use(httpx.RequestID, middleware.Recoverer)
	r.Get("/api/v1/health/live", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/api/v1/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Ready(r.Context()); err != nil {
			httpx.Error(w, r, http.StatusServiceUnavailable, "not_ready", "服务暂不可用")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	return r
}
```

- [ ] **Step 6: Add graceful server startup and shutdown**

`cmd/server/main.go` must load configuration, construct dependencies, start `http.Server` with explicit header/read/write/idle timeouts, listen for `SIGINT`/`SIGTERM`, and call `Shutdown` with a 10-second context. Startup failure logs only the error class and exits non-zero.

- [ ] **Step 7: Run focused and full tests**

Run: `go test ./internal/platform/config ./internal/platform/httpx ./internal/app -v`  
Expected: PASS.  
Run: `go test ./...`  
Expected: PASS with no package skipped due to compile errors.

- [ ] **Step 8: Commit configuration and HTTP foundation**

```bash
git add .env.example cmd/server internal/app internal/platform/config internal/platform/httpx go.mod go.sum
git commit -m "feat: add validated config and health endpoints"
```

---

### Task 3: Create the Authentication Database and Migration Harness

**Files:**
- Create: `deploy/compose.dev.yml`
- Create: `internal/platform/database/pool.go`
- Create: `internal/platform/database/migrate.go`
- Test: `internal/platform/database/migrate_test.go`
- Create: `db/migrations/00001_auth.sql`
- Create: `internal/auth/model.go`
- Create: `internal/auth/store.go`
- Create: `internal/auth/postgres_store.go`
- Test: `tests/integration/auth_store_test.go`

**Interfaces:**
- Produces: `database.Open(ctx, databaseURL) (*pgxpool.Pool, error)` and `database.Migrate(ctx, pool) error`.
- Produces: `auth.UserStore` and `auth.SessionStore` interfaces.
- Produces: tables `users`, `sessions`, `login_events`, and `audit_logs` with UUID primary keys and UTC timestamps.

- [ ] **Step 1: Write the failing migration integration test**

```go
func TestAuthMigrationCreatesConstraints(t *testing.T) {
	pool := integration.StartPostgres(t)
	if err := database.Migrate(context.Background(), pool); err != nil { t.Fatal(err) }
	var count int
	err := pool.QueryRow(context.Background(), `
		select count(*) from information_schema.tables
		where table_schema='public' and table_name in
		('users','sessions','login_events','audit_logs')`).Scan(&count)
	if err != nil || count != 4 { t.Fatalf("count=%d err=%v", count, err) }
}
```

- [ ] **Step 2: Start PostgreSQL and verify the test fails before migrations exist**

Run: `docker compose -f deploy/compose.dev.yml up -d postgres redis`  
Expected: both services become healthy.  
Run: `go test ./internal/platform/database -run TestAuthMigrationCreatesConstraints -v`  
Expected: FAIL because migration files and `Migrate` do not exist.

- [ ] **Step 3: Define the minimal secure schema**

```sql
-- db/migrations/00001_auth.sql
-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE TYPE user_role AS ENUM ('admin', 'student');
CREATE TYPE user_status AS ENUM ('active', 'disabled');

CREATE TABLE users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  username text NOT NULL,
  display_name text NOT NULL,
  role user_role NOT NULL,
  status user_status NOT NULL DEFAULT 'active',
  password_hash text NOT NULL,
  must_change_password boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT users_username_format CHECK (username ~ '^[a-zA-Z0-9._-]{3,64}$')
);
CREATE UNIQUE INDEX users_username_active_key ON users (lower(username)) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX users_single_admin_key ON users (role) WHERE role = 'admin' AND deleted_at IS NULL;

CREATE TABLE sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  user_agent text NOT NULL DEFAULT '',
  ip inet,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  idle_expires_at timestamptz NOT NULL,
  absolute_expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  revoke_reason text
);
CREATE INDEX sessions_user_active_idx ON sessions(user_id, absolute_expires_at) WHERE revoked_at IS NULL;

CREATE TABLE login_events (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  username text NOT NULL,
  success boolean NOT NULL,
  reason text NOT NULL,
  ip inet,
  user_agent text NOT NULL DEFAULT '',
  occurred_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_logs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  action text NOT NULL,
  target_type text NOT NULL,
  target_id text NOT NULL,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  request_id text NOT NULL,
  ip inet,
  occurred_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE audit_logs;
DROP TABLE login_events;
DROP TABLE sessions;
DROP TABLE users;
DROP TYPE user_status;
DROP TYPE user_role;
```

- [ ] **Step 4: Implement embedded migrations and a bounded pgx pool**

`database.Open` must parse the URL and set `MaxConns=10`, `MinConns=1`, `MaxConnLifetime=30m`, `MaxConnIdleTime=5m`, and `HealthCheckPeriod=30s`. `database.Migrate` uses embedded `db/migrations` files through goose and returns a wrapped error without including the database URL.

- [ ] **Step 5: Define store interfaces before SQL implementations**

```go
type Role string
const (RoleAdmin Role = "admin"; RoleStudent Role = "student")
type Status string
const (StatusActive Status = "active"; StatusDisabled Status = "disabled")

type User struct {
	ID uuid.UUID
	Username, DisplayName string
	Role Role
	Status Status
	PasswordHash string
	MustChangePassword bool
	CreatedAt time.Time
}

type UserStore interface {
	FindByUsername(context.Context, string) (User, error)
	FindByID(context.Context, uuid.UUID) (User, error)
	Create(context.Context, CreateUserParams) (User, error)
	UpdatePassword(context.Context, uuid.UUID, string, bool) error
	SetStatus(context.Context, uuid.UUID, Status) error
	ListStudents(context.Context, int, uuid.UUID) ([]User, error)
}

type SessionStore interface {
	Create(context.Context, CreateSessionParams) error
	FindActiveByTokenHash(context.Context, [32]byte, time.Time) (Session, User, error)
	Touch(context.Context, uuid.UUID, time.Time, time.Time) error
	Revoke(context.Context, uuid.UUID, string) error
	RevokeAllForUser(context.Context, uuid.UUID, string) error
}
```

- [ ] **Step 6: Implement stores with parameterized pgx queries**

Normalize usernames with `strings.ToLower(strings.TrimSpace(username))`. Map `pgx.ErrNoRows` to `auth.ErrNotFound`; map unique admin/username violations to `auth.ErrConflict`; do not expose SQL text in HTTP errors. Every list query must contain `WHERE role='student' AND deleted_at IS NULL` and use keyset pagination, never an unbounded table scan.

- [ ] **Step 7: Run migration and repository integration tests**

Run: `go test ./internal/platform/database ./tests/integration -run 'TestAuthMigration|TestPostgresUserStore' -v`  
Expected: PASS, including duplicate username rejection and the single-admin constraint.  
Run: `go test -race ./internal/auth ./internal/platform/database`  
Expected: PASS with no race report.

- [ ] **Step 8: Commit the schema and stores**

```bash
git add deploy/compose.dev.yml db/migrations internal/platform/database internal/auth tests/integration go.mod go.sum
git commit -m "feat: add authentication schema and stores"
```

---

### Task 4: Implement Argon2id Passwords and Opaque Session Service

**Files:**
- Create: `internal/auth/password.go`
- Test: `internal/auth/password_test.go`
- Create: `internal/auth/token.go`
- Test: `internal/auth/token_test.go`
- Create: `internal/auth/service.go`
- Test: `internal/auth/service_test.go`

**Interfaces:**
- Produces: `auth.PasswordHasher.Hash(password string) (string, error)` and `Compare(encoded, password string) error`.
- Produces: `auth.NewSessionToken() (raw string, hash [32]byte, error)`.
- Produces: `auth.Service.Login`, `Authenticate`, `ChangePassword`, `Logout`, and `LogoutOthers`.

- [ ] **Step 1: Write failing password-format and verification tests**

```go
func TestArgon2idRoundTrip(t *testing.T) {
	h := NewPasswordHasher(Argon2Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32})
	encoded, err := h.Hash("Correct Horse Battery Staple 42!")
	if err != nil { t.Fatal(err) }
	if !strings.HasPrefix(encoded, "$argon2id$v=19$") { t.Fatalf("hash=%q", encoded) }
	if err := h.Compare(encoded, "Correct Horse Battery Staple 42!"); err != nil { t.Fatal(err) }
	if err := h.Compare(encoded, "wrong password"); !errors.Is(err, ErrInvalidCredentials) { t.Fatalf("err=%v", err) }
}

func TestPasswordPolicy(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("a", 129)} {
		if err := ValidatePassword(password); err == nil { t.Fatalf("accepted %q", password) }
	}
}
```

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./internal/auth -run 'TestArgon2id|TestPasswordPolicy' -v`  
Expected: FAIL because password functions are undefined.

- [ ] **Step 3: Implement PHC-formatted Argon2id hashing**

Use `golang.org/x/crypto/argon2`, 16 random salt bytes, constant-time comparison, and strict PHC parsing. `ValidatePassword` requires 12–128 Unicode code points and rejects leading/trailing whitespace; it does not require arbitrary character classes. Never log the password or encoded hash.

```go
func (h PasswordHasher) Compare(encoded, password string) error {
	p, salt, want, err := parsePHC(encoded)
	if err != nil { return ErrInvalidCredentials }
	got := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 { return ErrInvalidCredentials }
	return nil
}
```

- [ ] **Step 4: Add token tests and implementation**

```go
func TestSessionTokenHasEntropyAndStableHash(t *testing.T) {
	raw, hash, err := NewSessionToken()
	if err != nil { t.Fatal(err) }
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != 32 { t.Fatalf("raw token invalid") }
	want := sha256.Sum256([]byte(raw))
	if hash != want { t.Fatal("hash mismatch") }
}
```

`NewSessionToken` must use `crypto/rand`, encode 32 bytes with base64url and padding disabled, and return only the SHA-256 hash for database storage.

- [ ] **Step 5: Write service tests with in-memory fake stores**

Cover successful login, nonexistent user, wrong password, disabled user, first-login flag, absolute/idle expiry, revoked session, password change revoking all other sessions, and generic invalid-credential behavior. The nonexistent-user branch must run one Argon2 comparison against a fixed dummy hash to reduce username timing leaks.

```go
func TestLoginCreatesSpecSession(t *testing.T) {
	now := time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC)
	svc := newTestService(t, now)
	result, raw, err := svc.Login(context.Background(), LoginInput{
		Username: "student01", Password: "Long Temporary Password 42!", IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil { t.Fatal(err) }
	if raw == "" || !result.User.MustChangePassword { t.Fatal("missing session or first-login gate") }
	if result.Session.IdleExpiresAt != now.Add(7*24*time.Hour) { t.Fatal(result.Session.IdleExpiresAt) }
	if result.Session.AbsoluteExpiresAt != now.Add(30*24*time.Hour) { t.Fatal(result.Session.AbsoluteExpiresAt) }
}
```

- [ ] **Step 6: Implement the service and login-event writes**

`Login` normalizes the username, verifies the password, checks active status, creates a token/session, and records a login event. Failed attempts record only normalized username, reason category, IP, and user agent. `Authenticate` rejects revoked/expired sessions, touches `last_seen_at` at most once every five minutes, and never extends beyond `absolute_expires_at`. `ChangePassword` verifies the current password, validates and hashes the new password, clears `must_change_password`, and revokes every existing session before creating one replacement session.

- [ ] **Step 7: Run unit and race tests**

Run: `go test -race ./internal/auth -v`  
Expected: PASS.  
Run: `go test ./...`  
Expected: PASS.

- [ ] **Step 8: Commit password and session behavior**

```bash
git add internal/auth go.mod go.sum
git commit -m "feat: add secure password and session service"
```


---

## Foundation Completion Gate

- Go and Vue workspaces build from lockfiles with the documented runtime versions.
- Liveness and readiness return stable JSON and request IDs.
- Migrations create one-admin, user, session, login-event, and audit constraints.
- Argon2id, token entropy, generic credential errors, session expiry, revocation, and first-login behavior pass race-tested unit/integration tests.
- No HTTP authentication endpoints or student-management screens are started until this gate passes and `2026-07-18-admin-api.md` begins.