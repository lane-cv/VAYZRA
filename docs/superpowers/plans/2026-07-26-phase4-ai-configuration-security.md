# HappyLearn Phase 4 AI Configuration and Security Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建立可审计、不可泄露、可防 SSRF 的 AI 供应商、模型、提示词与额度配置基础。

**Architecture:** `internal/aiqa` 提供独立配置领域。PostgreSQL 保存配置和 AES-256-GCM 密文，32 字节主密钥只从运行环境注入；URL 策略在保存和实际连接时都验证，老师 API 只暴露 capability-safe DTO。

**Tech Stack:** Go 1.26.5、chi 5、pgx 5、PostgreSQL 18.4、标准库 `crypto/aes`/`cipher`/`net/url`/`net/netip`、现有认证/CSRF/审计中间件。

## Global Constraints

- 生产环境供应商 Base URL 必须使用 HTTPS。
- 拒绝 URL 用户信息、fragment、非预期 query、loopback、RFC1918、链路本地、组播、保留地址和云元数据地址。
- 重定向和每次实际拨号都必须重新验证目标；不能只在保存配置时验证字符串。
- API Key 只能以 AES-256-GCM 密文保存；主密钥不得进入数据库、日志、镜像或仓库。
- 配置读 API 只返回 `hasKey` 和 `keyUpdatedAt`，不能返回明文、密文、nonce、指纹或 Authorization。
- 同时最多一个 active provider。
- 模型价格单位固定为每百万 Token 的 micro-USD；业务时区固定为 `Asia/Shanghai`。
- 所有写操作必须要求 admin、CSRF/Origin、严格 JSON、审计和乐观版本。
- 日志不得包含 Base URL query、API Key、Authorization、提示词正文或原始上游错误体。

---

### Task 1: Add Configuration Schema and Database Invariants

**Files:**
- Create: `db/migrations/00015_ai_configuration.sql`
- Create: `internal/platform/database/ai_configuration_migration_test.go`
- Modify: `db/migrations/embed.go`

**Interfaces:**
- Produces tables `ai_providers`, `ai_models`, `prompt_templates`, `ai_global_limits`, and `student_ai_limits`.
- Produces protocol values `chat_completions|responses`, modality values `text|vision`, and subject values `math|physics`.
- Produces one-active-provider partial unique index used by `aiqa.PostgresConfigStore`.

- [ ] **Step 1: Write the RED migration contract test**

Add an integration test that migrates a fresh PostgreSQL database and asserts:

```go
func TestAIConfigurationSchemaEnforcesSecretsAndOneActiveProvider(t *testing.T) {
    pool := integration.StartPostgres(t)
    adminID := seedActiveAdmin(t, pool)
    first := uuid.New()
    second := uuid.New()
    insertProvider(t, pool, first, adminID, false)
    insertProvider(t, pool, second, adminID, false)
    mustExec(t, pool, `UPDATE ai_providers SET active=true WHERE id=$1`, first)
    _, err := pool.Exec(context.Background(), `UPDATE ai_providers SET active=true WHERE id=$1`, second)
    if err == nil {
        t.Fatal("expected one-active-provider constraint")
    }
    _, err = pool.Exec(context.Background(), `UPDATE ai_providers SET encrypted_api_key='' WHERE id=$1`, first)
    if err == nil {
        t.Fatal("expected ciphertext constraint")
    }
}
```

Also assert invalid protocol, duplicate `(provider_id, upstream_model_id, modality)`, two active prompt versions for one subject, negative prices, inconsistent quota-block fields, and invalid limit values fail at SQL level.

- [ ] **Step 2: Run the migration test to verify RED**

Run: `go test ./internal/platform/database -run AIConfiguration -count=1`

Expected: FAIL because migration `00015_ai_configuration.sql` and its tables do not exist.

- [ ] **Step 3: Implement the migration**

Create:

```sql
CREATE TABLE ai_providers (
  id uuid PRIMARY KEY,
  name text NOT NULL CHECK (char_length(btrim(name)) BETWEEN 1 AND 80),
  base_url text NOT NULL CHECK (char_length(base_url) BETWEEN 8 AND 2048),
  protocol_mode text NOT NULL CHECK (protocol_mode IN ('chat_completions','responses')),
  encrypted_api_key bytea NOT NULL CHECK (octet_length(encrypted_api_key) BETWEEN 29 AND 8192),
  key_version smallint NOT NULL CHECK (key_version > 0),
  key_updated_at timestamptz NOT NULL,
  active boolean NOT NULL DEFAULT false,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ai_providers_one_active_idx ON ai_providers(active) WHERE active;
```

Add `ai_models` with modality, model ID, context/max-output bounds, timeouts, conservative `image_quota_tokens`, micro-USD input/output prices, enabled state, nullable `quota_blocked_at`, and nullable `quota_block_reason='quota_estimation_anomaly'`; require both block fields to be null or both non-null. Add `prompt_templates` with immutable versioned body plus one-active-per-subject index. Add one-row `ai_global_limits` and per-student override rows whose four limit columns allow `NULL`, `0`, or positive values. Add admin ownership/active-user triggers and reversible `Down` ordering.

- [ ] **Step 4: Run migration and full database tests**

Run:

```bash
go test ./internal/platform/database -run 'AIConfiguration|Migrate' -count=1
go test ./tests/integration -run 'Schema|Migration' -count=1
```

Expected: PASS, including down/up round-trip and all SQL constraint failures.

- [ ] **Step 5: Commit the schema**

```bash
git add db/migrations/00015_ai_configuration.sql db/migrations/embed.go internal/platform/database/ai_configuration_migration_test.go
git commit -m "feat: add ai configuration schema"
```

---

### Task 2: Load the Master Key and Implement Authenticated Secret Encryption

**Files:**
- Create: `internal/aiqa/crypto.go`
- Create: `internal/aiqa/crypto_test.go`
- Modify: `internal/platform/config/config.go`
- Modify: `internal/platform/config/config_test.go`
- Modify: `.env.example`

**Interfaces:**
- Produces:

```go
type EncryptedSecret struct {
    KeyVersion int16
    Blob       []byte // nonce || GCM ciphertext || tag
}

type SecretBox interface {
    Seal(providerID uuid.UUID, plaintext []byte) (EncryptedSecret, error)
    Open(providerID uuid.UUID, secret EncryptedSecret) ([]byte, error)
}

func NewAESGCMSecretBox(key []byte, version int16, random io.Reader) (SecretBox, error)
```

- Adds `Config.AIMasterKey []byte`, `Config.AIMasterKeyVersion int16`, `Config.AIBusinessTimezone string`, `Config.AIGlobalConcurrency int`, `Config.AIPerStudentConcurrency int`, and `Config.AIAllowPrivateProvider bool`.

- [ ] **Step 1: Write RED configuration and crypto tests**

Test exact configuration behavior:

```go
func TestProductionRequires32ByteBase64AIMasterKey(t *testing.T) {
    _, err := Load(mapEnv(map[string]string{
        "HAPPYLEARN_ENV": "production",
        "HAPPYLEARN_AI_MASTER_KEY": base64.StdEncoding.EncodeToString(make([]byte, 31)),
    }))
    if err == nil || !strings.Contains(err.Error(), "HAPPYLEARN_AI_MASTER_KEY") {
        t.Fatalf("err=%v", err)
    }
}
```

Crypto tests must prove random nonces produce different blobs, round-trip succeeds, wrong provider AAD fails, wrong key/version fails closed, plaintext input is copied/zeroable by caller, and returned errors contain no plaintext.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/aiqa ./internal/platform/config -run 'Secret|AIMaster|AIConcurrency' -count=1`

Expected: FAIL because the package and config fields do not exist.

- [ ] **Step 3: Implement strict config parsing**

Use:

```text
HAPPYLEARN_AI_MASTER_KEY=<standard base64 of exactly 32 bytes>
HAPPYLEARN_AI_MASTER_KEY_VERSION=1
HAPPYLEARN_AI_BUSINESS_TIMEZONE=Asia/Shanghai
HAPPYLEARN_AI_GLOBAL_CONCURRENCY=2
HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY=1
HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=false
```

Production requires the key. Development may use an explicit development-only constant only when `HAPPYLEARN_ENV=development`; emit no key value in errors. Reject timezone values other than `Asia/Shanghai` in Phase 4, versions outside `1..32767`, global concurrency outside `1..8`, and per-student values outside `1..global`.
Reject `HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=true` unless `HAPPYLEARN_ENV=development`; the disposable E2E harness is its only automated user.

- [ ] **Step 4: Implement AES-256-GCM**

Use a 12-byte random nonce and AAD:

```go
func providerAAD(id uuid.UUID, version int16) []byte {
    return []byte("happylearn:ai-provider-key:v1:" + id.String() + ":" + strconv.Itoa(int(version)))
}
```

Return only sentinel `ErrSecretUnavailable` on authentication/decryption failure. Never include crypto library errors or the secret in the returned error.

- [ ] **Step 5: Run focused and complete config tests**

Run:

```bash
go test ./internal/aiqa -run Secret -count=1
go test ./internal/platform/config -count=1
go test ./cmd/server ./cmd/worker -run Config -count=1
```

Expected: PASS with production fail-closed behavior.

- [ ] **Step 6: Commit encryption and configuration**

```bash
git add internal/aiqa/crypto.go internal/aiqa/crypto_test.go internal/platform/config/config.go internal/platform/config/config_test.go .env.example
git commit -m "feat: protect ai provider secrets"
```

---

### Task 3: Enforce Base URL and Dial-Time SSRF Policy

**Files:**
- Create: `internal/aiqa/url_policy.go`
- Create: `internal/aiqa/url_policy_test.go`
- Create: `internal/aiqa/safe_transport.go`
- Create: `internal/aiqa/safe_transport_test.go`

**Interfaces:**
- Produces:

```go
type URLPolicy struct {
    DevelopmentAllowPrivate bool
    Resolver                interface {
        LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
    }
}

type GatewayTimeouts struct {
    Connect        time.Duration
    ResponseHeader time.Duration
    IdleStream     time.Duration
    Total          time.Duration
}

func (p URLPolicy) NormalizeBaseURL(context.Context, string) (*url.URL, error)
func (p URLPolicy) ValidateResolved(context.Context, string) ([]netip.Addr, error)
func NewSafeHTTPClient(policy URLPolicy, timeouts GatewayTimeouts) *http.Client
```

- `GatewayTimeouts` contains connect, response-header, idle-stream, and total limits; all callers consume the returned client rather than `http.DefaultClient`.

- [ ] **Step 1: Write the RED URL matrix**

Table-test:

- accept `https://api.example.com/v1`;
- normalize trailing slash;
- reject HTTP in production;
- reject credentials, fragment, query, scheme-relative, Unicode-confusable host, IPv4-in-IPv6 private forms and non-canonical ports;
- reject `localhost`, `.local`, `127.0.0.1`, `10/8`, `172.16/12`, `192.168/16`, `169.254/16`, `100.64/10`, multicast, unspecified, documentation and metadata IPs;
- reject when any DNS answer is forbidden;
- permit loopback only with `DevelopmentAllowPrivate=true`.

- [ ] **Step 2: Write RED redirect and rebinding tests**

Use `httptest` plus a fake resolver/dialer to prove:

```go
func TestSafeTransportRejectsRedirectToPrivateAddress(t *testing.T) {}
func TestSafeTransportPinsValidatedAddressForDial(t *testing.T) {}
func TestSafeTransportRevalidatesEveryNewRequest(t *testing.T) {}
```

The request Host/TLS server name stays the validated hostname while the TCP dial target uses one validated IP.

- [ ] **Step 3: Run focused tests to verify RED**

Run: `go test ./internal/aiqa -run 'URLPolicy|SafeTransport' -count=1`

Expected: FAIL because URL policy and transport do not exist.

- [ ] **Step 4: Implement normalization and address classification**

Use `netip.Addr` predicates plus explicit prefix tables. Require a hostname, forbid non-default empty ports, strip one trailing slash, and never follow redirects without calling the same policy again.

- [ ] **Step 5: Implement the safe client**

Provide a dedicated `http.Transport` with bounded:

- dial timeout: 5 seconds;
- TLS handshake: 5 seconds;
- response header: configurable, default 30 seconds;
- idle stream: enforced by gateway reader, default 30 seconds;
- total run: context deadline, default 120 seconds;
- redirects: maximum 3, each revalidated.

Disable environment proxy inheritance for supplier calls.

- [ ] **Step 6: Run SSRF tests**

Run:

```bash
go test ./internal/aiqa -run 'URLPolicy|SafeTransport' -count=1
go test -race ./internal/aiqa -run SafeTransport -count=1
```

Expected: PASS without network access beyond test listeners.

- [ ] **Step 7: Commit URL security**

```bash
git add internal/aiqa/url_policy.go internal/aiqa/url_policy_test.go internal/aiqa/safe_transport.go internal/aiqa/safe_transport_test.go
git commit -m "feat: secure ai provider egress"
```

---

### Task 4: Add Admin Configuration Service and HTTP Contracts

**Files:**
- Create: `internal/aiqa/config_model.go`
- Create: `internal/aiqa/config_store.go`
- Create: `internal/aiqa/config_service.go`
- Create: `internal/aiqa/config_service_test.go`
- Create: `internal/aiqa/postgres_config.go`
- Create: `internal/aiqa/postgres_config_test.go`
- Create: `internal/aiqa/http_admin_config.go`
- Create: `internal/aiqa/http_admin_config_test.go`
- Modify: `internal/app/app.go`
- Create: `internal/app/ai_routes_test.go`
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/wiring_test.go`

**Interfaces:**
- Produces:

```go
type ProtocolMode string
type Modality string
type Subject string

const (
    ProtocolChatCompletions ProtocolMode = "chat_completions"
    ProtocolResponses       ProtocolMode = "responses"
    ModalityText            Modality = "text"
    ModalityVision          Modality = "vision"
    SubjectMath             Subject = "math"
    SubjectPhysics          Subject = "physics"
)

type Principal struct {
    User      auth.User
    RequestID string
    IP        net.IP
}

type ProviderView struct {
    ID, Name, BaseURL string
    ProtocolMode ProtocolMode
    Active bool
    HasKey bool
    KeyUpdatedAt time.Time
    Version int64
}

type ModelView struct {
    ID, ProviderID, UpstreamModelID string
    Modality Modality
    ContextTokens, MaxOutputTokens, ImageQuotaTokens int64
    InputPriceMicroUSD, OutputPriceMicroUSD int64
    Enabled bool
    QuotaBlockedAt *time.Time
    QuotaBlockReason string
    Version int64
}

type PromptView struct {
    ID string
    Subject Subject
    Version int64
    Body string
    Active bool
}

type LimitValue struct {
    Mode string // inherit|disabled|limit
    Value *int64
}

type LimitView struct {
    DailyRequests, MonthlyRequests LimitValue
    DailyTokens, MonthlyTokens LimitValue
    Version int64
}

type LimitViews struct {
    Global LimitView
    Students map[uuid.UUID]LimitView
}

type CreateProviderInput struct {
    Name, BaseURL, APIKey, IdempotencyKey string
    ProtocolMode ProtocolMode
}

type UpdateProviderInput struct {
    ID uuid.UUID
    Name, BaseURL string
    ProtocolMode ProtocolMode
    APIKey *string
    ExpectedVersion int64
}

type PutModelInput struct {
    ProviderID, ID uuid.UUID
    UpstreamModelID string
    Modality Modality
    ContextTokens, MaxOutputTokens, ImageQuotaTokens int64
    InputPriceMicroUSD, OutputPriceMicroUSD int64
    Enabled bool
    ClearQuotaBlock bool
    ExpectedVersion int64
}

type PutPromptInput struct {
    Subject Subject
    Body string
    ExpectedVersion int64
}

type PutLimitsInput struct {
    DailyRequests, MonthlyRequests LimitValue
    DailyTokens, MonthlyTokens LimitValue
    ExpectedVersion int64
}

type RuntimeProviderConfig struct {
    ProviderID uuid.UUID
    BaseURL *url.URL
    ProtocolMode ProtocolMode
    APIKey []byte
    Model ModelView
    Prompt PromptView
    Timeouts GatewayTimeouts
}

type RuntimeConfigSource interface {
    ForRun(context.Context, Subject, Modality) (RuntimeProviderConfig, error)
}

type AdminConfigService interface {
    ListProviders(context.Context, Principal) ([]ProviderView, error)
    CreateProvider(context.Context, Principal, CreateProviderInput) (ProviderView, error)
    UpdateProvider(context.Context, Principal, UpdateProviderInput) (ProviderView, error)
    ActivateProvider(context.Context, Principal, uuid.UUID, int64) (ProviderView, error)
    ListModels(context.Context, Principal, uuid.UUID) ([]ModelView, error)
    PutModel(context.Context, Principal, PutModelInput) (ModelView, error)
    ListPrompts(context.Context, Principal) ([]PromptView, error)
    PutPrompt(context.Context, Principal, PutPromptInput) (PromptView, error)
    GetLimits(context.Context, Principal) (LimitViews, error)
    PutGlobalLimits(context.Context, Principal, PutLimitsInput) (LimitView, error)
    PutStudentLimits(context.Context, Principal, uuid.UUID, PutLimitsInput) (LimitView, error)
}
```

- Mounts strict admin routes below `/api/v1/admin/ai`.
- Later plans consume `PostgresConfigStore.ActiveRuntimeConfig(ctx)` and never read provider tables directly.

- [ ] **Step 1: Write RED service tests**

Cover admin-only authorization, exact normalization, version conflicts, key replacement versus unchanged key, one active provider, required active text/vision models and prompts, quota-blocked model rejection, explicit block clearing only when quota bounds change, limit inheritance (`NULL`), disabled (`0`), positive limits, and audit records that contain counts/IDs but no Base URL query, key or prompt body.

- [ ] **Step 2: Write RED PostgreSQL concurrency tests**

Run two activation transactions concurrently and assert exactly one active provider. Test stale versions return `ErrConfigConflict`, and verify provider reads expose `HasKey=true` while the store model never reaches the HTTP DTO.

- [ ] **Step 3: Write RED strict HTTP tests**

Assert:

- student receives `403`;
- malformed UUID/unknown field/duplicate Content-Type fail;
- missing/short `Idempotency-Key` fails on create;
- DTO JSON contains `hasKey` and `keyUpdatedAt`;
- serialized bodies never contain `encryptedApiKey`, `nonce`, `fingerprint`, `apiKey`, or test secret;
- stable errors are `AI_DISABLED`, `invalid_request`, `config_conflict`, and `not_found` as applicable.

- [ ] **Step 4: Run tests to verify RED**

Run:

```bash
go test ./internal/aiqa -run 'ConfigService|PostgresConfig|AdminConfigHTTP' -count=1
go test ./internal/app -run AIRoutes -count=1
```

Expected: FAIL because service, store, handlers and routes do not exist.

- [ ] **Step 5: Implement the service and PostgreSQL store**

Use transaction-scoped audit writes for every mutation. `CreateProviderInput.APIKey` is required; `UpdateProviderInput.APIKey` is `*string`, where nil means unchanged and non-empty means replace. Reject empty-string replacement. Activation locks all providers, validates active models/prompts, deactivates the old provider and activates the selected provider in one transaction.

- [ ] **Step 6: Implement strict handlers**

Expose:

```text
GET    /api/v1/admin/ai/providers
POST   /api/v1/admin/ai/providers
PUT    /api/v1/admin/ai/providers/{id}
PUT    /api/v1/admin/ai/active-provider
GET    /api/v1/admin/ai/providers/{id}/models
PUT    /api/v1/admin/ai/providers/{id}/models/{modelId}
GET    /api/v1/admin/ai/prompts
PUT    /api/v1/admin/ai/prompts/{subject}
GET    /api/v1/admin/ai/limits
PUT    /api/v1/admin/ai/limits/global
PUT    /api/v1/admin/ai/limits/students/{studentId}
```

All request DTOs use `DisallowUnknownFields`, bounded bodies, canonical UUIDs, exact enum values and expected version fields.

- [ ] **Step 7: Wire the service**

Add `AdminAI aiqa.AdminConfigHTTPService` to `app.Dependencies`, mount it with `auth.RequireRole(auth.RoleAdmin)`, construct `SecretBox`, `URLPolicy`, and `PostgresConfigStore` in `cmd/server/main.go`, and verify service construction occurs before background runners start.

- [ ] **Step 8: Run the configuration gate**

Run:

```bash
go test ./internal/aiqa ./internal/app ./cmd/server -count=1
go test -race ./internal/aiqa -count=1
go vet ./internal/aiqa ./internal/app ./cmd/server
```

Expected: PASS with no secret values in failure output.

- [ ] **Step 9: Commit the configuration API**

```bash
git add internal/aiqa internal/app/app.go internal/app/ai_routes_test.go cmd/server/main.go cmd/server/wiring_test.go
git commit -m "feat: add secure ai configuration api"
```
