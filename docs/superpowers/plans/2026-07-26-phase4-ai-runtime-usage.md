# HappyLearn Phase 4 AI Runtime and Usage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付私密 AI 多轮会话、附件上下文、持久化流式运行、两种 OpenAI 兼容协议、幂等额度账本和可恢复 SSE。

**Architecture:** AI 写模型与 Phase 3 老师答疑完全隔离。创建问题时 PostgreSQL 原子写入消息、运行和额度预留；后台 Go 运行器认领任务并经安全网关调用供应商，批量持久化事件，SSE 独立回放；成功事务写最终助手消息和 usage，失败释放硬额度。

**Tech Stack:** Go 1.26.5、PostgreSQL 18.4、pgx 5、chi 5、MinIO、现有文件处理 Worker、SSE、OpenAI-compatible Chat Completions/Responses。

## Global Constraints

- AI 表不能放宽或替换 Phase 3 `qa_*` 表的触发器、外键和不可变约束。
- 学生对象查询必须在 SQL 层携带 `student_id`；猜测他人 thread/message/run/event/file 统一返回 `404`。
- 运行状态固定为 `queued|streaming|succeeded|failed|cancelled`。
- 同一学生最多一个 queued/streaming 运行；全站实际供应商调用默认最多两个。
- 浏览器断线不取消运行；重连只回放同一 run，不创建新供应商调用。
- 只有 failed/cancelled 可主动重试；收到任何上游内容后不得自动重放。
- 失败/取消释放硬额度；真实或估算 usage、未知 usage 和费用仍需保存。
- 流式事件每约 250ms 或 4KiB 持久化一次；单事件、单运行输出、总时长均有服务端上限。
- streaming 内容只作为未完成事件；只有 succeeded 创建不可变 assistant message。
- PDF/Word 文本提取和图片输入只读取归属正确、`ai_attachment`、扫描通过且处理完成的文件。
- 模型输出、学生正文、附件内容、API Key 和上游原始错误体不得进入日志。

---

### Task 1: Add AI Conversation, Run, Event, and Ledger Schema

**Files:**
- Create: `db/migrations/00016_ai_runtime.sql`
- Create: `internal/platform/database/ai_runtime_migration_test.go`
- Create: `tests/integration/ai_schema_test.go`
- Modify: `db/migrations/embed.go`

**Interfaces:**
- Produces `ai_threads`, `ai_messages`, `ai_message_files`, `ai_runs`, `ai_run_events`, and `ai_usage_ledger`.
- Extends `upload_sessions.purpose` and `file_versions.purpose` with `ai_attachment`.
- Extends `file_previews.preview_kind` with `ai_text`.
- Extends `file_access_logs` with `ai_message_id` and a constraint that exactly zero or one of lesson revision, teacher-QA message, and AI message is present.

- [ ] **Step 1: Write RED schema isolation tests**

Prove:

```go
func TestAIRuntimeDoesNotWeakenTeacherQASchema(t *testing.T) {
    pool := integration.StartPostgres(t)
    assertQAMessageStillRejectsAssistantRole(t, pool)
    assertQAMessageStillRequiresSenderUser(t, pool)
    assertAIMessageAcceptsAssistantWithoutUserID(t, pool)
}
```

Also test immutable AI messages/events/ledger, one active run per student, one `(student_id,idempotency_key)`, one `(trigger_message_id,attempt_no)`, strictly positive event sequence, one final assistant message per succeeded run, attachment purpose/owner trigger, and one settle/release ledger terminal action per run.

- [ ] **Step 2: Run migration tests to verify RED**

Run:

```bash
go test ./internal/platform/database -run AIRuntime -count=1
go test ./tests/integration -run AISchema -count=1
```

Expected: FAIL because migration `00016_ai_runtime.sql` does not exist.

- [ ] **Step 3: Implement the runtime migration**

Use fixed core columns:

```sql
CREATE TABLE ai_threads (
  id uuid PRIMARY KEY,
  student_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  title text NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 160),
  subject text NOT NULL CHECK (subject IN ('math','physics')),
  last_message_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ai_messages (
  id uuid PRIMARY KEY,
  thread_id uuid NOT NULL REFERENCES ai_threads(id) ON DELETE RESTRICT,
  role text NOT NULL CHECK (role IN ('student','assistant')),
  sender_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  body_text text NOT NULL CHECK (char_length(btrim(body_text)) BETWEEN 1 AND 100000),
  trigger_run_id uuid,
  idempotency_key text,
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((role='student')=(sender_user_id IS NOT NULL)),
  CHECK ((role='student')=(idempotency_key IS NOT NULL))
);
```

Create `ai_runs` with immutable config snapshot fields, quota reservation fields, lease fields, usage/cost/timing/error fields and status-dependent checks. Add the post-create foreign key from messages to runs. Store `ai_run_events.payload_text` with a 16KiB check. Ledger rows use action `reserve|settle|release`, period kind `day|month`, period key, request/token deltas and unique `(run_id,period_kind,action)`.
Add `file_access_logs.ai_message_id REFERENCES ai_messages(id) ON DELETE RESTRICT`; replace the existing single-business-target check so lesson, teacher-QA and AI message targets are mutually exclusive.

- [ ] **Step 4: Add database triggers**

Add functions that:

- reject update/delete of AI messages, message files, events and ledger;
- keep thread owner/subject/created time immutable;
- require student message sender to be the active thread owner;
- require assistant message `trigger_run_id` to reference a succeeded run for the same thread;
- require AI file purpose, active ownership and unique binding;
- prevent transition out of terminal run states;
- allow only `queued→streaming|failed|cancelled` and `streaming→succeeded|failed|cancelled`.

- [ ] **Step 5: Run schema and existing QA gates**

Run:

```bash
go test ./internal/platform/database -run 'AIRuntime|QAMigration' -count=1
go test ./tests/integration -run 'AISchema|QASchema' -count=1
```

Expected: PASS and Phase 3 constraints remain unchanged.

- [ ] **Step 6: Commit the runtime schema**

```bash
git add db/migrations/00016_ai_runtime.sql db/migrations/embed.go internal/platform/database/ai_runtime_migration_test.go tests/integration/ai_schema_test.go
git commit -m "feat: add ai runtime schema"
```

---

### Task 2: Produce Safe AI Attachment Context

**Files:**
- Modify: `internal/files/model.go`
- Modify: `internal/files/upload_policy.go`
- Modify: `internal/files/upload_policy_test.go`
- Modify: `internal/files/postgres_store.go`
- Modify: `internal/processing/model.go`
- Modify: `internal/processing/postgres_store.go`
- Modify: `internal/processing/postgres_store_test.go`
- Create: `internal/processing/text_extract.go`
- Create: `internal/processing/text_extract_test.go`
- Modify: `internal/processing/pipeline.go`
- Modify: `internal/processing/pipeline_test.go`
- Modify: `cmd/worker/main.go`
- Modify: `cmd/worker/main_test.go`
- Create: `internal/aiqa/attachments.go`
- Create: `internal/aiqa/attachments_test.go`
- Create: `internal/aiqa/postgres_attachments.go`
- Create: `internal/aiqa/postgres_attachments_test.go`
- Modify: `internal/app/app.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Adds `files.UploadPurposeAI UploadPurpose = "ai_attachment"` and `files.AIUploadPolicy`.
- Extends processing:

```go
type Result struct {
    // existing fields
    AIText *PreviewResult
}

type AttachmentInput struct {
    FileVersionID uuid.UUID
    SortPosition int
}

type AttachmentMetadata struct {
    FileVersionID uuid.UUID
    DisplayName, DetectedMIME string
    Modality Modality
    Size int64
}

type AttachmentContext struct {
    FileVersionID uuid.UUID
    DisplayName   string
    DetectedMIME string
    Kind         Modality
    Text         string
    OpenImage    func(context.Context) (io.ReadCloser, error)
    Size         int64
}

type AttachmentContextStore interface {
    ValidateForAI(context.Context, uuid.UUID, uuid.UUID, []AttachmentInput) ([]AttachmentMetadata, error)
    LoadAIText(context.Context, uuid.UUID, uuid.UUID) (string, error)
    OpenAIImage(context.Context, uuid.UUID, uuid.UUID) (io.ReadCloser, string, int64, error)
}
```

- Mounts student-only upload transport at `/api/v1/student/ai-uploads`.

- [ ] **Step 1: Write RED purpose and policy tests**

Test that `AIUploadPolicy` permits student image/PDF/docx/txt/md limits already approved for Q&A, rejects admin, macros/executables/archives, and persists `ai_attachment` rather than `qa_attachment`.

- [ ] **Step 2: Write RED extraction tests**

Use fake `processing.Runner` calls and assert:

- PDF invokes `pdftotext -layout -- input.pdf output.txt`;
- Office conversion output PDF is passed to the same extractor;
- UTF-8 text is normalized to LF, rejects invalid UTF-8/NUL and caps output at 2 MiB;
- images create no `ai_text`;
- `Result.AIText.Kind == "ai_text"` and content type is `text/plain; charset=utf-8`.

- [ ] **Step 3: Write RED attachment ownership tests**

Create two students and AI/QA/teaching files. Assert `ValidateForAI` accepts only current-owner ready `ai_attachment`; wrong owner returns `ErrNotFound`; pending/failed returns `ErrAttachmentNotReady`; non-image without ready `ai_text` returns `ErrAttachmentNotReady`; no object key appears in returned metadata.

- [ ] **Step 4: Run focused tests to verify RED**

Run:

```bash
go test ./internal/files -run AIUpload -count=1
go test ./internal/processing -run AIText -count=1
go test ./internal/aiqa -run Attachment -count=1
```

Expected: FAIL because AI purpose, extraction and context store do not exist.

- [ ] **Step 5: Implement upload purpose and route**

Reuse `UploadService` with `AIUploadPolicy`; do not clone multipart logic. Add a separate resume namespace in the later frontend transport. Update cleanup queries to treat unbound AI files as reclaimable without considering them bound to teacher QA.

- [ ] **Step 6: Implement text extraction**

Add `pdftotext` to required worker commands. Store extracted text as a private MinIO preview object with kind `ai_text`, SHA-256 and size. Never persist extracted text in PostgreSQL or logs. For `ai_attachment`, file processing is only `ready` after every required artifact succeeds.

- [ ] **Step 7: Implement context loading**

Load text through the private object store and cap reads at 2 MiB. Open images only after matching owner/purpose/ready state and detected MIME `image/jpeg|png|webp|gif`. Return closers to the gateway and close them on every path.

- [ ] **Step 8: Run file/processing regression gates**

Run:

```bash
go test ./internal/files ./internal/processing ./internal/aiqa -count=1
go test ./tests/integration -run 'Files|Processing|AI' -count=1
go test ./cmd/worker ./cmd/server -count=1
```

Expected: PASS; teaching and teacher-QA uploads are unchanged.

- [ ] **Step 9: Commit AI attachment context**

```bash
git add internal/files internal/processing internal/aiqa cmd/worker cmd/server internal/app/app.go
git commit -m "feat: prepare safe ai attachments"
```

---

### Task 3: Implement Chat Completions and Responses Stream Adapters

**Files:**
- Create: `internal/aiqa/gateway.go`
- Create: `internal/aiqa/gateway_test.go`
- Create: `internal/aiqa/chat_completions.go`
- Create: `internal/aiqa/chat_completions_test.go`
- Create: `internal/aiqa/responses.go`
- Create: `internal/aiqa/responses_test.go`
- Create: `internal/aiqa/stream_reader.go`
- Create: `internal/aiqa/stream_reader_test.go`

**Interfaces:**
- Consumes `NewSafeHTTPClient`, active runtime config, and attachment context from earlier tasks.
- Produces:

```go
type GatewayRequest struct {
    RunID          uuid.UUID
    Model          string
    SystemPrompt   string
    Turns          []GatewayTurn
    Images         []GatewayImage
    MaxOutputTokens int
}

type GatewayTurn struct {
    Role string // student|assistant
    Text string
}

type GatewayImage struct {
    MediaType string
    Size int64
    Open func(context.Context) (io.ReadCloser, error)
}

type GatewayEvent struct {
    Kind        string // delta|usage|completed
    Delta       string
    InputTokens int64
    OutputTokens int64
    FinishReason string
}

type Gateway interface {
    Stream(context.Context, RuntimeProviderConfig, GatewayRequest, func(GatewayEvent) error) error
}
```

- [ ] **Step 1: Write RED Chat Completions request tests**

Assert exact `POST {baseURL}/chat/completions`, bearer header, JSON body with `stream:true`, `stream_options.include_usage:true`, text/image content parts, max output, and no unsupported tools. Assert request JSON never includes local file paths, object keys or student IDs, and a 20MiB fake image is read incrementally rather than retained as a second full in-memory copy.

- [ ] **Step 2: Write RED Responses request tests**

Assert exact `POST {baseURL}/responses`, `input` role/content mapping, `instructions`, `max_output_tokens`, text/image inputs, `stream:true`, and no tools.

- [ ] **Step 3: Write RED stream/error matrix**

For both adapters cover split TCP frames, CRLF, comments, empty events, `[DONE]`, usage-only terminal chunks, Responses event names, malformed JSON, 16KiB event cap, 100,000-character answer cap, 401/429/5xx capped bodies, idle timeout, context cancellation and callback failure. Stable errors must expose categories only.

- [ ] **Step 4: Run focused tests to verify RED**

Run: `go test ./internal/aiqa -run 'ChatCompletions|Responses|StreamReader|Gateway' -count=1`

Expected: FAIL because adapters do not exist.

- [ ] **Step 5: Implement the bounded SSE reader**

Read line-by-line with a 16KiB maximum, assemble `data:` fields per SSE rules, reset the idle deadline after valid bytes, and stop on context cancellation. Never log payloads.

- [ ] **Step 6: Implement both adapters**

Map both protocols to `GatewayEvent`. Stream request JSON through `io.Pipe`, Base64-encoding each image from `GatewayImage.Open` without buffering the entire request; close every reader and propagate encoder failure through the pipe. Classify `auth`, `rate_limited`, `upstream_4xx`, `upstream_5xx`, `timeout`, `stream_interrupted`, `malformed_stream`, `response_too_large`, and `cancelled`. Preserve no raw body in `error.Error()`.

- [ ] **Step 7: Add safe pre-write retry proof**

Use `httptrace.ClientTrace.WroteRequest`. Permit at most one reconnect only when the request was never written and no response/event was observed; rebuild the request pipe and reopen every image for that attempt. Tests must prove a post-write EOF is returned to the runner without a second server hit.

- [ ] **Step 8: Run gateway race and leak tests**

Run:

```bash
go test ./internal/aiqa -run 'Gateway|ChatCompletions|Responses|StreamReader' -count=1
go test -race ./internal/aiqa -run Gateway -count=1
```

Expected: PASS; all response bodies and image readers are closed.

- [ ] **Step 9: Commit the gateway**

```bash
git add internal/aiqa/gateway.go internal/aiqa/gateway_test.go internal/aiqa/chat_completions.go internal/aiqa/chat_completions_test.go internal/aiqa/responses.go internal/aiqa/responses_test.go internal/aiqa/stream_reader.go internal/aiqa/stream_reader_test.go
git commit -m "feat: add compatible ai streaming gateway"
```

---

### Task 4: Connect the Safe Provider Connectivity Test

**Files:**
- Create: `internal/aiqa/config_test_gateway.go`
- Create: `internal/aiqa/config_test_gateway_test.go`
- Modify: `internal/aiqa/config_service.go`
- Modify: `internal/aiqa/config_service_test.go`
- Modify: `internal/aiqa/http_admin_config.go`
- Modify: `internal/aiqa/http_admin_config_test.go`

**Interfaces:**
- Consumes the `Gateway` implemented in Task 3.
- Adds:

```go
type ConnectivityTester interface {
    Test(context.Context, RuntimeProviderConfig) (ConnectivityResult, error)
}

func (s *ConfigService) TestProvider(context.Context, Principal, uuid.UUID) (ConnectivityResult, error)
```

- Exposes `POST /api/v1/admin/ai/providers/{id}/test`.

- [ ] **Step 1: Write RED tests for the minimal paid probe**

Use fake Chat Completions and Responses servers. Assert the probe sends one user token, requests maximum output `1`, parses the first valid event, uses the safe client, times out, maps 401/429/5xx to stable categories, and never returns raw error bodies.

- [ ] **Step 2: Write RED audit and HTTP tests**

Assert action `ai.provider_tested` records provider ID, protocol, success/failure category and latency only. The response is `{ok,protocol,latencyMs,errorCategory}` and never contains upstream text, key, Authorization or request body.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./internal/aiqa -run 'Connectivity|ProviderTestHTTP' -count=1`

Expected: FAIL because tester and endpoint do not exist.

- [ ] **Step 4: Implement the connectivity tester**

Decrypt the key only for request construction, zero the local byte slice after setting the header, invoke `Gateway.Stream` with a synthetic one-token prompt and maximum output `1`, cap response/error bodies, and discard generated text.

- [ ] **Step 5: Implement service and route**

Require admin, allow one active test per provider, audit the result, and return `PROVIDER_UNAVAILABLE` without upstream details.

- [ ] **Step 6: Run the connectivity gate**

Run:

```bash
go test ./internal/aiqa ./internal/app ./cmd/server -count=1
go test -race ./internal/aiqa -run Connectivity -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit connectivity testing**

```bash
git add internal/aiqa
git commit -m "feat: test ai provider connectivity"
```

---

### Task 5: Implement Quota Admission and AI Conversation Service

**Files:**
- Create: `internal/aiqa/runtime_model.go`
- Create: `internal/aiqa/runtime_store.go`
- Create: `internal/aiqa/quota.go`
- Create: `internal/aiqa/quota_test.go`
- Create: `internal/aiqa/service.go`
- Create: `internal/aiqa/service_test.go`
- Create: `internal/aiqa/postgres_runtime.go`
- Create: `internal/aiqa/postgres_runtime_test.go`

**Interfaces:**
- Produces:

```go
type StudentService interface {
    CreateThread(context.Context, Principal, CreateThreadInput) (ThreadDetail, Run, error)
    ListThreads(context.Context, Principal, ThreadCursor) ([]Thread, ThreadCursor, error)
    GetThread(context.Context, Principal, uuid.UUID, MessageCursor) (ThreadDetail, error)
    AddMessage(context.Context, Principal, AddMessageInput) (ThreadDetail, Run, error)
    CancelRun(context.Context, Principal, uuid.UUID) (Run, error)
    RetryRun(context.Context, Principal, uuid.UUID, string) (Run, error)
}

type RunStatus string

const (
    RunQueued RunStatus = "queued"
    RunStreaming RunStatus = "streaming"
    RunSucceeded RunStatus = "succeeded"
    RunFailed RunStatus = "failed"
    RunCancelled RunStatus = "cancelled"
)

type Thread struct {
    ID, StudentID uuid.UUID
    Title string
    Subject Subject
    LastMessageAt, CreatedAt time.Time
}

type Message struct {
    ID, ThreadID uuid.UUID
    Role string
    Body string
    RunID uuid.UUID
    Attachments []AttachmentMetadata
    CreatedAt time.Time
}

type Run struct {
    ID, ThreadID, TriggerMessageID uuid.UUID
    Status RunStatus
    AttemptNo int
    LastSequence int64
    ErrorCode string
    CreatedAt, UpdatedAt time.Time
}

type ThreadDetail struct {
    Thread Thread
    Messages []Message
    ActiveRun *Run
    NextMessageCursor MessageCursor
}

type CreateThreadInput struct {
    Title, Body, IdempotencyKey string
    Subject Subject
    Attachments []AttachmentInput
}

type AddMessageInput struct {
    ThreadID uuid.UUID
    Body, IdempotencyKey string
    Attachments []AttachmentInput
}

type ThreadCursor struct {
    LastMessageAt time.Time
    ID uuid.UUID
    Limit int
}

type MessageCursor struct {
    CreatedAt time.Time
    ID uuid.UUID
    Limit int
}

type QuotaReservation struct {
    RequestCount int64
    TokenCount   int64
    DayKey       string // YYYY-MM-DD in Asia/Shanghai
    MonthKey     string // YYYY-MM
    EstimatorVersion int16
}
```

- `PostgresRuntimeStore.AdmitRun` performs message/run/reservation writes in one transaction.

- [ ] **Step 1: Write RED quota tests**

Cover global inheritance, `0` disable, positive overrides, day/month keys at Shanghai midnight/month boundaries, conservative UTF-8 byte/image/max-output reservation, exact remaining quota, failed release, successful unused release, idempotent settle/release, and actual usage above reservation marking `quota_estimation_anomaly` plus blocking the selected model.

- [ ] **Step 2: Write RED service tests**

Cover authorization, subject validation, text/vision routing, attachment readiness, context-too-large, idempotent create/follow-up, one active run, cancel, retry eligibility, attempt increments, no retry of succeeded/queued/streaming, immutable history, and no assistant message before success.

- [ ] **Step 3: Write RED PostgreSQL concurrency tests**

Synchronize two goroutines at admission. Assert:

- limit 1 admits one run and rejects one with `ErrQuotaExceeded`;
- duplicate idempotency key returns the same run;
- one-student active index prevents two active runs;
- concurrent settle calls write one terminal ledger set;
- wrong-student IDs return `ErrNotFound`.

- [ ] **Step 4: Run tests to verify RED**

Run: `go test ./internal/aiqa -run 'Quota|StudentService|PostgresRuntime' -count=1`

Expected: FAIL because runtime service/store do not exist.

- [ ] **Step 5: Implement deterministic quota calculation**

Resolve effective limits before locking buckets. Compute reservation as:

```go
quotaTokens := int64(len([]byte(systemPrompt + selectedTextTurns + extractedText)))
quotaTokens += int64(imageCount) * model.ImageQuotaTokens
quotaTokens += int64(model.MaxOutputTokens)
```

Reject before mutation if any request/token day/month remainder is insufficient. Store estimator version and exact reservation on the run.

- [ ] **Step 6: Implement service boundaries**

Select complete historical user/assistant pairs newest-first; always retain system prompt and current message; reject instead of truncating the current input. Use typed errors `ErrAIDisabled`, `ErrQuotaExceeded`, `ErrAIBusy`, `ErrAttachmentNotReady`, `ErrContextTooLarge`, `ErrNotFound`, `ErrRunConflict`.

- [ ] **Step 7: Implement PostgreSQL transactions**

Use `SELECT ... FOR UPDATE` for relevant limits and period aggregates. Insert day/month reserve rows for request and token deltas. On success insert settle rows and release unused reservation; on failed/cancelled insert release rows. If actual usage exceeds reservation, save the full actual usage/cost and set the model quota-block fields in the same transaction while charging only the reservation. Unique constraints make every terminal action idempotent.

- [ ] **Step 8: Run service, PostgreSQL and race gates**

Run:

```bash
go test ./internal/aiqa -run 'Quota|StudentService|PostgresRuntime' -count=1
go test -race ./internal/aiqa -run 'Quota|StudentService' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit conversation and quota**

```bash
git add internal/aiqa/runtime_model.go internal/aiqa/runtime_store.go internal/aiqa/quota.go internal/aiqa/quota_test.go internal/aiqa/service.go internal/aiqa/service_test.go internal/aiqa/postgres_runtime.go internal/aiqa/postgres_runtime_test.go
git commit -m "feat: add ai conversations and quotas"
```

---

### Task 6: Add the Durable AI Runner and Event Checkpoints

**Files:**
- Create: `internal/aiqa/runner.go`
- Create: `internal/aiqa/runner_test.go`
- Create: `internal/aiqa/postgres_runner.go`
- Create: `internal/aiqa/postgres_runner_test.go`
- Modify: `cmd/server/main.go`
- Create: `cmd/server/ai_runner_wiring_test.go`

**Interfaces:**
- Produces:

```go
type RunnerStore interface {
    LeaseNext(context.Context, string, time.Time, time.Duration) (LeasedRun, error)
    Heartbeat(context.Context, uuid.UUID, string, time.Time) error
    AppendEvents(context.Context, uuid.UUID, string, []RunEvent) error
    Complete(context.Context, LeasedRun, Completion) error
    Fail(context.Context, LeasedRun, Failure) error
    ReconcileExpired(context.Context, time.Time, int) error
}

type LeasedRun struct {
    Run Run
    Config RuntimeProviderConfig
    Request GatewayRequest
    LeaseOwner string
}

type RunEvent struct {
    Sequence int64
    Kind, Delta, ErrorCode string
    CreatedAt time.Time
}

type Completion struct {
    Answer string
    InputTokens, OutputTokens, CostMicroUSD int64
    UsageSource, FinishReason string
    FirstByteMS, TotalMS int64
}

type Failure struct {
    Status RunStatus
    ErrorCode, UsageSource string
    InputTokens, OutputTokens, CostMicroUSD int64
    TotalMS int64
}

type Runner struct {
    Store RunnerStore
    Gateway Gateway
    Owner string
    GlobalConcurrency int
    PollInterval time.Duration
    LeaseDuration time.Duration
    FlushInterval time.Duration
    FlushBytes int
}

func StartRunner(Runner) func()
```

- [ ] **Step 1: Write RED runner lifecycle tests**

Test queued→streaming→succeeded, 4KiB flush, 250ms flush, monotonic sequence, usage capture, first-byte/total latency, callback/store failure, cancel, lease loss, bounded shutdown and no content in log hooks.

- [ ] **Step 2: Write RED crash recovery tests**

Assert expired queued leases become claimable; expired streaming leases become failed with category `runner_lost`, release quota once and preserve partial events; terminal runs are untouched.

- [ ] **Step 3: Write RED global concurrency tests**

Use a blocking fake gateway and prove maximum simultaneous `Stream` calls equals configured `2`; the store may contain more queued runs without creating goroutines per row.

- [ ] **Step 4: Run tests to verify RED**

Run: `go test ./internal/aiqa -run 'Runner|Lease|Reconcile' -count=1`

Expected: FAIL because runner does not exist.

- [ ] **Step 5: Implement the runner**

Use a fixed worker pool of `GlobalConcurrency`. Each worker leases one row with `FOR UPDATE SKIP LOCKED`, transitions to streaming before gateway I/O, heartbeats at one-third lease duration, and batches deltas. Completion transaction inserts final event, assistant message, actual usage/cost, quota settlement and succeeded state; an over-reservation usage anomaly also blocks that model before commit.

- [ ] **Step 6: Implement failure and cancellation**

Map gateway errors to stable categories. A cancel request sets `cancel_requested_at`; runner cancellation stops local context and writes cancelled terminal state. Any partial text remains events only.

- [ ] **Step 7: Wire startup and shutdown**

Construct one runner after all services and before returning the handler. Stop it before closing Redis/PostgreSQL. Add ordering assertion:

```go
want := []string{"open", "migrate", "services", "ai-runner-start", "ai-runner-stop", "database-close"}
```

- [ ] **Step 8: Run runner and wiring gates**

Run:

```bash
go test ./internal/aiqa ./cmd/server -run 'Runner|AI.*Wiring' -count=1
go test -race ./internal/aiqa -run Runner -count=1
```

Expected: PASS with prompt shutdown.

- [ ] **Step 9: Commit the runner**

```bash
git add internal/aiqa/runner.go internal/aiqa/runner_test.go internal/aiqa/postgres_runner.go internal/aiqa/postgres_runner_test.go cmd/server/main.go cmd/server/ai_runner_wiring_test.go
git commit -m "feat: run durable ai streams"
```

---

### Task 7: Expose Student AI APIs, SSE Replay, and Controlled Files

**Files:**
- Create: `internal/aiqa/http_common.go`
- Create: `internal/aiqa/http_student.go`
- Create: `internal/aiqa/http_student_test.go`
- Create: `internal/aiqa/http_sse.go`
- Create: `internal/aiqa/http_sse_test.go`
- Create: `internal/files/ai_access.go`
- Create: `internal/files/http_ai_access.go`
- Create: `internal/files/http_ai_access_test.go`
- Create: `internal/files/postgres_ai_access.go`
- Create: `internal/files/postgres_ai_access_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/ai_routes_test.go`
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/main_test.go`

**Interfaces:**
- Mounts fixed student routes:

```text
POST /api/v1/student/ai/threads
GET  /api/v1/student/ai/threads
GET  /api/v1/student/ai/threads/{threadId}
POST /api/v1/student/ai/threads/{threadId}/messages
GET  /api/v1/student/ai/runs/{runId}/events
POST /api/v1/student/ai/runs/{runId}/cancel
POST /api/v1/student/ai/runs/{runId}/retries
GET  /api/v1/ai-question-files/{fileVersionId}/preview
GET  /api/v1/ai-question-files/{fileVersionId}/status
```

- SSE event DTO:

```go
type StreamEventDTO struct {
    Sequence int64     `json:"sequence"`
    Kind     string    `json:"kind"`
    Delta    string    `json:"delta,omitempty"`
    Status   RunStatus `json:"status,omitempty"`
    ErrorCode string   `json:"errorCode,omitempty"`
}
```

- [ ] **Step 1: Write RED strict JSON and privacy tests**

Test exact bodies, canonical UUIDs, 20,000-character student body limit, attachment limit, idempotency headers, unknown fields, role checks, stable error mapping, and absence of provider/model secret snapshots/object keys in every DTO.

- [ ] **Step 2: Write RED SSE replay tests**

Prove:

- initial connection replays sequence `1..N`;
- `Last-Event-ID: 2` starts at `3`;
- duplicate/malformed/future/negative IDs fail safely;
- each connection rechecks current student ownership;
- terminal event closes stream;
- disconnect cancels only subscription, not run;
- a stream remains writable beyond the server's existing 15-second ordinary-response deadline by refreshing a bounded per-write deadline;
- headers include `text/event-stream`, `Cache-Control: no-store`, `X-Accel-Buffering: no`;
- heartbeat comments do not advance event sequence.

- [ ] **Step 3: Write RED AI file access tests**

Assert only thread owner can status/preview bound ready AI files; unbound, other owner, QA or teaching purpose return 404; response never redirects to raw MinIO; every allow/deny/failure writes a safe access log linked to AI message.

- [ ] **Step 4: Run tests to verify RED**

Run:

```bash
go test ./internal/aiqa -run 'StudentHTTP|SSE' -count=1
go test ./internal/files -run AIAccess -count=1
go test ./internal/app -run AIRoutes -count=1
```

Expected: FAIL because handlers/routes do not exist.

- [ ] **Step 5: Implement strict student JSON handlers**

Follow existing qanda decoding conventions: one JSON Content-Type, bounded body, `DisallowUnknownFields`, one canonical idempotency header, opaque cursors, uniform student 404 and request IDs.

- [ ] **Step 6: Implement database-backed SSE**

Poll persisted events with an abortable wait interface; never subscribe directly to runner memory. Use `http.NewResponseController(w).SetWriteDeadline` before every event/heartbeat so the ordinary 15-second server deadline cannot terminate a healthy long stream, while every blocked write remains bounded. Flush after each event batch. Cap one connection per session/run combination and close on auth expiry or terminal state.

- [ ] **Step 7: Implement AI controlled file delivery**

Reuse existing ranged/preview delivery code through a narrow AI authorization store. Write access rows through the `file_access_logs.ai_message_id` column created in Task 1; the database mutually excludes lesson, teacher-QA and AI targets.

- [ ] **Step 8: Wire all routes and services**

Add `StudentAI`, `AIFileAccess`, and student-only `AIUploads` dependencies. Keep admin config routes separate. Route tests must prove students cannot reach `/admin/ai` and admins cannot use student AI creation endpoints.

- [ ] **Step 9: Run the runtime plan gate**

Run:

```bash
go test ./internal/aiqa ./internal/files ./internal/app ./cmd/server ./cmd/worker -count=1
go test -race ./internal/aiqa ./internal/files -count=1
go vet ./...
```

Expected: PASS.

- [ ] **Step 10: Commit student runtime APIs**

```bash
git add internal/aiqa internal/files internal/app cmd/server
git commit -m "feat: expose private ai qanda streams"
```
