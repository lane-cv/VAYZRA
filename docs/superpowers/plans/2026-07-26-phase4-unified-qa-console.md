# HappyLearn Phase 4 Unified Q&A Console Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 AI 与老师答疑合并为学生统一答疑中心，并交付安全流式 AI 会话、老师 AI 配置和用量统计界面。

**Architecture:** 后端统一摘要是只读聚合，AI 与老师答疑写模型仍隔离。Vue 共享展示层类型与附件/时间线组件，但每个通道保留独立 API；AI SSE 使用可中止的 fetch 流客户端从持久化事件恢复。

**Tech Stack:** Vue 3.5、TypeScript 5.9、Vite 8、Pinia 3、Vue Router 4、Vitest 4、Element Plus 2.14、DOMPurify、Markdown-It、KaTeX、Go 1.26.5。

## Global Constraints

- 学生侧边栏只显示“答疑中心”，不同时显示“AI 答疑”和“老师答疑”。
- 统一列表只聚合当前学生的摘要；不能把老师备注、其他学生、对象键、模型秘密或管理员路径带入 DTO。
- 统一列表支持全部/AI/老师、标题搜索和稳定游标，不跨状态机提供统一 status 筛选。
- 新建问题必须先选择 AI 或老师；AI 还必须选择数学或物理，学生不能选择模型。
- AI streaming 内容只用转义纯文本 `white-space:pre-wrap`；只有 succeeded 后走现有清洗 Markdown/KaTeX 管线。
- SSE 重连携带最后持久化事件序号；页面卸载只停止订阅，不取消运行。
- 所有异步状态提供 loading、empty、error、retry 和 request ID。
- 手机使用列表到详情路由；返回操作恢复焦点，流式状态不逐 token 打扰屏幕阅读器。
- API Key 输入只用于新建/替换；任何组件状态、快照和错误不得包含已保存密钥。
- 不把通知、AI 内容、答疑正文或学生真实数据写入 localStorage、日志、截图或测试快照。

---

### Task 1: Add Unified Summary and Admin Usage Read APIs

**Files:**
- Create: `internal/aiqa/summary.go`
- Create: `internal/aiqa/summary_test.go`
- Create: `internal/aiqa/postgres_summary.go`
- Create: `internal/aiqa/postgres_summary_test.go`
- Create: `internal/aiqa/admin_usage.go`
- Create: `internal/aiqa/admin_usage_test.go`
- Create: `internal/aiqa/http_admin_usage.go`
- Create: `internal/aiqa/http_admin_usage_test.go`
- Modify: `internal/aiqa/http_student.go`
- Modify: `internal/aiqa/http_student_test.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/ai_routes_test.go`
- Modify: `cmd/server/main.go`

**Interfaces:**
- Produces:

```go
type QuestionSummary struct {
    ID            uuid.UUID
    Channel       string // ai|teacher
    Title         string
    RawStatus     string
    LastMessageAt time.Time
    CreatedAt     time.Time
}

type SummaryFilter struct {
    Channel string
    Search  string
    Cursor  SummaryCursor
    Limit   int
}

type UsageFilter struct {
    StudentID uuid.UUID
    ModelID   uuid.UUID
    Status    RunStatus
    From      time.Time
    To        time.Time
    Cursor    UsageCursor
    Limit     int
}

type UsageSummary struct {
    Requests, Succeeded, Failed int64
    InputTokens, OutputTokens   int64
    CostMicroUSD                int64
    UnknownUsage                int64
    AverageFirstByteMS          int64
    AverageTotalMS              int64
}
```

- Exposes:

```text
GET /api/v1/student/question-summaries
GET /api/v1/admin/ai/usage/summary
GET /api/v1/admin/ai/usage/runs
```

HTTP DTOs encode `costMicroUSD` as a base-10 string even though Go/PostgreSQL keep `int64`, so TypeScript never loses integer precision.

- [ ] **Step 1: Write RED summary SQL tests**

Seed teacher and AI threads with equal timestamps. Assert deterministic ordering by `(last_message_at DESC, channel DESC, id DESC)`, opaque cursor round-trip, channel filter, case-insensitive title-only search, student ownership in both UNION branches, and no message/note bodies in rows.

- [ ] **Step 2: Write RED usage tests**

Assert filters, equal-time pagination, successful/failed/cancelled counts, actual versus estimated usage source, unknown usage, micro-USD sum, integer average latencies, no prompt/message text, and admin-only access.

- [ ] **Step 3: Run tests to verify RED**

Run:

```bash
go test ./internal/aiqa -run 'Summary|AdminUsage' -count=1
go test ./internal/app -run AIRoutes -count=1
```

Expected: FAIL because summary and usage readers do not exist.

- [ ] **Step 4: Implement the unified SQL read model**

Use one parameterized `UNION ALL` of `qa_threads` and `ai_threads`, with `student_id=$1` in both branches before sorting. Search only normalized title with an escaped `ILIKE` pattern. Encode channel in the cursor so equal timestamps remain stable.

- [ ] **Step 5: Implement admin usage queries**

Read `ai_runs` plus immutable usage fields; return IDs, student display metadata, model ID/label, status, tokens, usage source, cost, timings, error category and timestamps. Do not return config snapshots, prompt bodies, message IDs/bodies or provider errors.

- [ ] **Step 6: Implement strict handlers and wiring**

Use canonical filters, limits `1..100`, UTC date bounds, opaque canonical cursors, student uniform 404 semantics and admin role middleware.

- [ ] **Step 7: Run backend read API gate**

Run:

```bash
go test ./internal/aiqa ./internal/app ./cmd/server -count=1
go test -race ./internal/aiqa -run 'Summary|AdminUsage' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit read APIs**

```bash
git add internal/aiqa internal/app cmd/server
git commit -m "feat: add unified qanda and ai usage reads"
```

---

### Task 2: Add Typed AI APIs and a Replayable SSE Client

**Files:**
- Create: `web/src/features/ai/types.ts`
- Create: `web/src/features/ai/studentApi.ts`
- Create: `web/src/features/ai/studentApi.test.ts`
- Create: `web/src/features/ai/adminApi.ts`
- Create: `web/src/features/ai/adminApi.test.ts`
- Create: `web/src/features/ai/eventStream.ts`
- Create: `web/src/features/ai/eventStream.test.ts`
- Create: `web/src/features/ai/aiUpload.ts`
- Create: `web/src/features/ai/aiUpload.test.ts`

**Interfaces:**
- Produces:

```ts
export type AIChannel = 'ai' | 'teacher'
export type AISubject = 'math' | 'physics'
export type AIRunStatus = 'queued'|'streaming'|'succeeded'|'failed'|'cancelled'
export type AIMessage = {
  id:string; role:'student'|'assistant'; body:string; createdAt:string
  attachments:QuestionAttachment[]; runId?:string
}
export type AIRun = {
  id:string; status:AIRunStatus; attemptNo:number; lastSequence:number
  errorCode?:string; usage?:AIUsage
}

export type AIUsage = {
  inputTokens:number; outputTokens:number; costMicroUSD:string
  source:'provider'|'estimated'|'unknown'
}

export type StreamEvent = {
  sequence:number; kind:'delta'|'status'|'error'
  delta?:string; status?:AIRunStatus; errorCode?:string
}

export type StreamCallbacks = {
  onEvent(event:StreamEvent):void
  onRequestId?(requestId:string):void
}

export function subscribeRun(
  runId:string,
  afterSequence:number,
  callbacks:StreamCallbacks,
  signal:AbortSignal,
): Promise<void>
```

- Reuses `createUploadManager` with transport `/student/ai-uploads` and IndexedDB key prefix `qa:ai:<userId>:`; it does not clone upload state logic.

- [ ] **Step 1: Write RED student API contract tests**

Test exact URLs, URL encoding, strict bodies, UUID idempotency keys, subject/channel fields, expected thread/run DTOs, cancel/retry, cursor construction and `APIError` propagation.

- [ ] **Step 2: Write RED admin API tests**

Test provider create/update/activate/test, model/prompt/limit writes, usage filters, and that TypeScript DTOs contain `hasKey` but no property named `apiKey`, `encryptedApiKey`, `nonce`, or `fingerprint` on read views.

- [ ] **Step 3: Write RED stream parser/reconnect tests**

Using a fake streamed `Response`, prove split UTF-8 frames, CRLF, comments, multi-line data, sequence ordering, duplicate suppression, terminal close, malformed event rejection, abort, and reconnect calls with `afterSequence` equal to the last committed event.

- [ ] **Step 4: Run focused tests to verify RED**

Run: `pnpm --dir web test -- studentApi adminApi eventStream aiUpload`

Expected: FAIL because the modules do not exist.

- [ ] **Step 5: Implement typed APIs**

Use the existing `request` client for JSON calls. Keep provider write input:

```ts
export type ProviderWriteInput = {
  name:string
  baseUrl:string
  protocolMode:'chat_completions'|'responses'
  apiKey?:string
  expectedVersion?:number
}
```

Never merge write input with provider read view.

- [ ] **Step 6: Implement the fetch SSE client**

Use credentialed same-origin `fetch`, `Accept:text/event-stream`, and query `afterSequence`. Parse only JSON event data, enforce monotonically increasing sequence, update the caller only after a complete event, and let the view own bounded reconnect/backoff.

- [ ] **Step 7: Implement the AI upload transport**

Reuse upload manager interfaces, purpose-specific status endpoint and safe same-origin preview links. Treat server validation as authoritative.

- [ ] **Step 8: Run typed client gate**

Run:

```bash
pnpm --dir web test -- studentApi adminApi eventStream aiUpload
pnpm --dir web typecheck
```

Expected: PASS.

- [ ] **Step 9: Commit client modules**

```bash
git add web/src/features/ai
git commit -m "feat: add typed ai clients"
```

---

### Task 3: Build the Unified Question List and Channel-Aware Composer

**Files:**
- Create: `web/src/features/questions/summaryApi.ts`
- Create: `web/src/features/questions/summaryApi.test.ts`
- Modify: `web/src/features/questions/types.ts`
- Modify: `web/src/features/questions/StudentQuestionListView.vue`
- Modify: `web/src/features/questions/StudentQuestionListView.test.ts`
- Modify: `web/src/features/questions/NewQuestionView.vue`
- Modify: `web/src/features/questions/NewQuestionView.test.ts`
- Modify: `web/src/features/questions/QuestionAttachmentUploader.vue`
- Modify: `web/src/features/questions/QuestionAttachmentUploader.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/router/index.test.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/layouts/ConsoleLayout.test.ts`

**Interfaces:**
- Produces routes `/student/questions`, `/student/questions/new`, `/student/questions/ai/:threadId`, and `/student/questions/teacher/:threadId`.
- Redirects legacy `/student/questions/:questionId` to the teacher route.
- Produces summary type:

```ts
export type QuestionSummary = {
  id:string; channel:'ai'|'teacher'; title:string; rawStatus:string
  lastMessageAt:string; createdAt:string
}
```

- [ ] **Step 1: Write RED summary API tests**

Test channel/search/cursor encoding, unknown channel rejection in UI helpers, canonical summaries, equal-time pagination and API errors.

- [ ] **Step 2: Write RED unified list tests**

Test mixed AI/teacher ordering, channel badges, all/AI/teacher filters, debounced title search, aborting stale searches, empty/error/retry, cursor load-more, keyboard activation, and no note/content preview.

- [ ] **Step 3: Write RED composer tests**

Test:

- channel is required;
- AI requires math/physics;
- teacher channel does not send subject;
- no model selector exists;
- shared title/body/attachments;
- switching channel warns before discarding incompatible pending AI upload state;
- duplicate click reuses one idempotency key;
- AI success navigates to `/student/questions/ai/{id}`;
- teacher success navigates to `/student/questions/teacher/{id}`.

- [ ] **Step 4: Run focused tests to verify RED**

Run: `pnpm --dir web test -- summaryApi StudentQuestionList NewQuestion QuestionAttachment router ConsoleLayout`

Expected: FAIL against the old teacher-only list and composer.

- [ ] **Step 5: Implement unified list**

Replace teacher-only list API with `summaryApi`. Render labels “AI” and “老师”, map raw statuses by channel, search title only, and keep cursor/filter state in route query without student data persistence.

- [ ] **Step 6: Implement channel-aware composer**

Show two accessible radio cards. Reuse the existing teacher creation path. For AI show subject selection and AI upload transport, then call `createAIThread`. Keep separate upload-manager instances so purpose-specific resume IDs cannot collide.

- [ ] **Step 7: Update navigation and routes**

Keep a single student “答疑中心” link. Add explicit channel detail routes and canonical UUID guards. Preserve admin question routes and notification route.

- [ ] **Step 8: Run unified list/composer gate**

Run:

```bash
pnpm --dir web test -- summaryApi StudentQuestionList NewQuestion QuestionAttachment router ConsoleLayout
pnpm --dir web typecheck
pnpm --dir web lint
```

Expected: PASS with one student Q&A navigation item.

- [ ] **Step 9: Commit the unified entry**

```bash
git add web/src/features/questions web/src/router web/src/layouts
git commit -m "feat: unify student qanda entry"
```

---

### Task 4: Build the AI Timeline, Durable Stream Store, and Retry Workflow

**Files:**
- Create: `web/src/stores/aiRuns.ts`
- Create: `web/src/stores/aiRuns.test.ts`
- Create: `web/src/features/ai/AIMessageTimeline.vue`
- Create: `web/src/features/ai/AIMessageTimeline.test.ts`
- Create: `web/src/features/ai/AIRunStatusCard.vue`
- Create: `web/src/features/ai/AIRunStatusCard.test.ts`
- Create: `web/src/features/ai/AIQuestionDetailView.vue`
- Create: `web/src/features/ai/AIQuestionDetailView.test.ts`
- Create: `web/src/features/ai/FinalAIAnswer.vue`
- Create: `web/src/features/ai/FinalAIAnswer.test.ts`
- Modify: `web/src/features/teaching/renderMarkdown.ts`
- Modify: `web/src/features/teaching/renderMarkdown.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/layouts/ConsoleLayout.test.ts`

**Interfaces:**
- `useAIRunStore` methods:

```ts
start(runId:string, afterSequence:number): void
stopSubscription(runId:string): void
apply(event:StreamEvent): void
retrySubscription(runId:string): void
clearAll(): void
```

- Component unmount calls `stopSubscription`; it never calls server cancel unless the user explicitly presses “停止生成”.

- [ ] **Step 1: Write RED store lifecycle tests**

With fake timers prove one subscription per run, no overlapping reconnect, exponential delays `500/1000/2000/5000ms` capped at 5s, reset after an event, last-sequence resume, terminal stop, abort on logout/account change/unmount and no leaked timers/listeners.

- [ ] **Step 2: Write RED safe rendering tests**

During streaming, `<img src=x onerror=...>`, Markdown links and KaTeX source render as text with no image/anchor/`innerHTML`. After succeeded, `FinalAIAnswer` uses the existing DOMPurify whitelist and KaTeX, rejects scripts, event handlers, `javascript:` and unsafe external attributes.

- [ ] **Step 3: Write RED detail workflow tests**

Test loading/404/error/retry, historical messages, refresh into streaming run, queued/streaming/succeeded/failed/cancelled cards, stop confirmation, failed retry idempotency, quota display, follow-up attachment pending, AI subject display, request ID, focus after errors and throttled `aria-live`.

- [ ] **Step 4: Run focused tests to verify RED**

Run: `pnpm --dir web test -- aiRuns AIMessageTimeline AIRunStatus AIQuestionDetail FinalAIAnswer renderMarkdown`

Expected: FAIL because store and components do not exist.

- [ ] **Step 5: Implement the run store**

Keep only non-sensitive run IDs/status/deltas in memory. Do not persist to localStorage/IndexedDB. Deduplicate by sequence and replace partial text with server thread detail after succeeded.

- [ ] **Step 6: Implement safe timeline and status**

Use `textContent` interpolation for streaming. Render final messages through `renderMarkdown`. Error cards map stable codes to Chinese actions and show support request ID without raw provider errors.

- [ ] **Step 7: Implement detail and explicit cancel/retry**

Cancel button calls `cancelRun`; route changes only abort the subscription. Retry generates one idempotency key per unchanged retry intent, receives a new run ID and starts that run at sequence 0. `ConsoleLayout.logout` calls `aiRuns.clearAll()` before `session.clear()`, and a session user-ID watcher clears subscriptions before a new account renders.

- [ ] **Step 8: Run AI detail gate**

Run:

```bash
pnpm --dir web test -- aiRuns AIMessageTimeline AIRunStatus AIQuestionDetail FinalAIAnswer renderMarkdown router
pnpm --dir web typecheck
pnpm --dir web lint
pnpm --dir web build
```

Expected: PASS.

- [ ] **Step 9: Commit student AI detail**

```bash
git add web/src/stores/aiRuns.ts web/src/stores/aiRuns.test.ts web/src/features/ai web/src/features/teaching/renderMarkdown.ts web/src/features/teaching/renderMarkdown.test.ts web/src/router/index.ts web/src/layouts/ConsoleLayout.vue web/src/layouts/ConsoleLayout.test.ts
git commit -m "feat: add resilient student ai answers"
```

---

### Task 5: Build Teacher AI Configuration

**Files:**
- Create: `web/src/features/ai/AdminAIConfigView.vue`
- Create: `web/src/features/ai/AdminAIConfigView.test.ts`
- Create: `web/src/features/ai/ProviderEditor.vue`
- Create: `web/src/features/ai/ProviderEditor.test.ts`
- Create: `web/src/features/ai/ModelRoutingEditor.vue`
- Create: `web/src/features/ai/ModelRoutingEditor.test.ts`
- Create: `web/src/features/ai/PromptEditor.vue`
- Create: `web/src/features/ai/PromptEditor.test.ts`
- Create: `web/src/features/ai/AILimitsEditor.vue`
- Create: `web/src/features/ai/AILimitsEditor.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/router/index.test.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/layouts/ConsoleLayout.test.ts`

**Interfaces:**
- Produces `/admin/ai` with tabs `供应商配置|模型路由|提示词|额度策略`.
- Uses only typed admin API from Task 2.

- [ ] **Step 1: Write RED provider workflow tests**

Test list/create/edit, Chat/Responses mode, HTTPS validation hint, new key required, edit key blank means unchanged, explicit replace flow, active provider confirmation, stale version reload, connection test cost warning, loading/error/request ID and no saved secret in DOM after submit.

- [ ] **Step 2: Write RED model/prompt/limit tests**

Test one text and one vision route, bounds/prices/timeouts, math/physics prompt versions, global and student `inherit/disabled/limit` semantics, student search selection and optimistic conflicts.

- [ ] **Step 3: Run tests to verify RED**

Run: `pnpm --dir web test -- AdminAIConfig ProviderEditor ModelRouting PromptEditor AILimits`

Expected: FAIL because admin components do not exist.

- [ ] **Step 4: Implement provider UI**

Never initialize an edit form with an API key. After a successful mutation clear the key input before updating any view state. Display only “已安全保存” and `keyUpdatedAt`.

- [ ] **Step 5: Implement model, prompt and limits tabs**

Represent limit states with an explicit select plus numeric input, not ambiguous blank fields. Store prices as decimal dollars per million Token in the form and convert exactly to/from integer micro-USD without floating-point drift.

- [ ] **Step 6: Add admin route and navigation**

Add “AI 管理” only for admin. Student snapshots and route trees must contain no `/admin/ai`.

- [ ] **Step 7: Run configuration UI gate**

Run:

```bash
pnpm --dir web test -- AdminAIConfig ProviderEditor ModelRouting PromptEditor AILimits router ConsoleLayout
pnpm --dir web typecheck
pnpm --dir web lint
```

Expected: PASS.

- [ ] **Step 8: Commit AI management UI**

```bash
git add web/src/features/ai web/src/router web/src/layouts
git commit -m "feat: add teacher ai management"
```

---

### Task 6: Build Usage Statistics and Run the Console Gate

**Files:**
- Create: `web/src/features/ai/AdminAIUsageView.vue`
- Create: `web/src/features/ai/AdminAIUsageView.test.ts`
- Create: `web/src/features/ai/UsageSummaryCards.vue`
- Create: `web/src/features/ai/UsageRunTable.vue`
- Create: `web/src/features/ai/UsageRunTable.test.ts`
- Modify: `web/src/router/index.ts`
- Modify: `web/src/router/index.test.ts`
- Modify: `web/src/layouts/ConsoleLayout.vue`
- Modify: `web/src/layouts/ConsoleLayout.test.ts`

**Interfaces:**
- Produces `/admin/ai-usage`.
- Displays USD from integer micro-USD and marks usage source `供应商|估算|未知`.

- [ ] **Step 1: Write RED usage UI tests**

Test date/student/model/status filters, summary cards, equal-time cursor load, success/failure/cancelled rows, unknown usage, quota-estimation anomaly, request ID, empty/error/retry, no message/prompt body and no clickable student AI content.

- [ ] **Step 2: Write RED responsive/accessibility tests**

At desktop render table columns; below 900px render labelled cards with the same data and reachable actions. Test headings, table names, focus after retry, reduced motion and no horizontal-only action.

- [ ] **Step 3: Run focused tests to verify RED**

Run: `pnpm --dir web test -- AdminAIUsage UsageRunTable ConsoleLayout router`

Expected: FAIL because usage view/route do not exist.

- [ ] **Step 4: Implement usage view**

Use abort controllers for filter changes, UTC API bounds derived from Asia/Shanghai selected dates, stable cursor pagination and `BigInt(costMicroUSD)` currency formatting; never parse cost through JavaScript floating point.

- [ ] **Step 5: Add route/navigation and complete mobile layout**

Add admin “用量统计”. Verify unified student list/detail and admin configuration/usage at 760px and 900px breakpoints without duplicate navigation.

- [ ] **Step 6: Run the complete console plan gate**

Run:

```bash
pnpm --dir web test
pnpm --dir web typecheck
pnpm --dir web lint
pnpm --dir web build
go test ./internal/aiqa ./internal/app ./cmd/server -count=1
```

Expected: PASS with no timer, listener or stream leaks.

- [ ] **Step 7: Commit usage UI**

```bash
git add web/src/features/ai web/src/router web/src/layouts
git commit -m "feat: add ai usage console"
```
