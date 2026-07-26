# HappyLearn Phase 4 Acceptance and Operations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用可控伪供应商和一次性 Docker 环境证明 Phase 4 的协议、隐私、恢复、记账、资源和运维约束全部成立。

**Architecture:** 仓库内的测试专用伪供应商在 Docker 内部网络模拟两种 OpenAI 协议及故障。Phase 4 harness 复用现有安全复制、产物清洗和一次性资源规则，运行 Phase 1–4 浏览器测试；运维文档只使用 UUID、聚合计数和稳定错误类别。

**Tech Stack:** Go 1.26.5、Docker、PostgreSQL 18.4、Redis 8.8、MinIO AIStor、Playwright 1.57、Bash strict mode、现有 Go/Vue 质量工具。

## Global Constraints

- 伪供应商只用于测试，不进入生产镜像或生产 Compose。
- E2E 使用合成账号、问题、答案和附件；真实学生数据不得进入 fixture、日志、截图、trace 或视频。
- 失败诊断必须先经过现有 artifact sanitizer；不得发布连接串、密码、API Key、Authorization、消息正文、答案、附件名或对象键。
- 测试网络必须是一次性 `--internal` 网络；数据库、Redis、MinIO 和伪供应商不发布主机端口。
- 所有容器、网络、卷和镜像使用 harness 前缀并在 EXIT/INT/TERM 清理。
- 默认 app 资源限制保持 256MiB，并发为全站 2、学生 1；完整环境符合 2 核 4 GB 目标。
- Phase 4 回滚保留迁移、AI 行、账本和对象；不得通过 down migration 删除业务数据。
- 只有本计划完整质量门通过后才能宣称 Phase 4 完成。

---

### Task 1: Build a Deterministic Dual-Protocol Fake Provider

**Files:**
- Create: `cmd/fake-ai-provider/main.go`
- Create: `cmd/fake-ai-provider/main_test.go`
- Create: `Dockerfile.fake-ai`
- Create: `tests/fixtures/ai/README.md`

**Interfaces:**
- Listens on `:8090`.
- Implements:

```text
GET  /health/live
POST /v1/chat/completions
POST /v1/responses
```

- Selects behavior from safe synthetic prompt marker:

```text
[case:success]
[case:slow-first-byte]
[case:idle-timeout]
[case:disconnect-after-delta]
[case:malformed-event]
[case:no-usage]
[case:usage-over-reservation]
[case:429]
[case:500]
```

- Records only aggregate hit counts by protocol/case at `GET /test/counts`; it never stores Authorization or request bodies.

- [ ] **Step 1: Write RED protocol fixture tests**

Assert exact Chat and Responses event shapes, split-frame writes, deterministic final text/usage, no-usage terminal, disconnect, malformed event, slow cases controlled by context, and capped request body.

- [ ] **Step 2: Write RED privacy tests**

Send a synthetic bearer secret and assert handler logs/count responses do not contain it or request text. Assert `/test/counts` returns numeric labels only.

- [ ] **Step 3: Run tests to verify RED**

Run: `go test ./cmd/fake-ai-provider -count=1`

Expected: FAIL because the command does not exist.

- [ ] **Step 4: Implement the fake provider**

Use only the Go standard library. Require `Authorization: Bearer e2e-provider-key`; parse bounded JSON and emit deterministic SSE with `http.Flusher`. Do not support arbitrary URLs, tools, files or proxying.

- [ ] **Step 5: Add the test-only image**

Use a multi-stage Dockerfile that builds only `/cmd/fake-ai-provider` and copies it into the same minimal runtime base used by the app. Run as non-root, read-only, no capabilities and with a healthcheck.

- [ ] **Step 6: Run unit and image smoke tests**

Run:

```bash
go test ./cmd/fake-ai-provider -count=1
docker build -f Dockerfile.fake-ai -t happylearn-fake-ai:local .
docker run --rm --read-only --cap-drop ALL happylearn-fake-ai:local -help
```

Expected: tests PASS and command exits without printing secrets.

- [ ] **Step 7: Commit the fake provider**

```bash
git add cmd/fake-ai-provider Dockerfile.fake-ai tests/fixtures/ai/README.md
git commit -m "test: add compatible ai fixture provider"
```

---

### Task 2: Add Phase 4 Browser Acceptance

**Files:**
- Create: `tests/e2e/ai-questions.spec.ts`
- Create: `tests/e2e/ai-admin.spec.ts`
- Create: `tests/e2e/ai-privacy.spec.ts`
- Modify: `tests/e2e/helpers.ts`
- Modify: `playwright.config.ts`

**Interfaces:**
- Adds helpers:

```ts
configureAIProvider(page:Page, mode:'chat_completions'|'responses'):Promise<void>
uploadAIFixture(page:Page, path:string, declaredMime:string):Promise<UploadedFile>
waitForAIFile(page:Page, fileVersionId:string):Promise<void>
waitForRunStatus(page:Page, runId:string, status:AIRunStatus):Promise<AIRun>
providerHitCounts(page:Page):Promise<Record<string,number>>
```

- [ ] **Step 1: Write the admin acceptance tests**

Cover create provider, key not reappearing after save/reload, model text/vision routing, math/physics prompts, global/student limits, connection test warning, active provider, Chat/Responses switch and usage page filters.

- [ ] **Step 2: Write the student happy-path tests**

Cover:

- unified list initially empty;
- teacher question and AI question appear together with channel labels;
- text math answer streams and renders final KaTeX safely;
- image uses vision model;
- PDF and DOCX wait for `ai_text` then use text model;
- multi-turn follow-up preserves history;
- refresh during streaming resumes the same run;
- provider hit count stays exactly one for duplicate submit and reconnect.

- [ ] **Step 3: Write failure/retry/quota tests**

Cover 429, 500, no usage, stream disconnect, explicit retry, cancel, context too large, pending attachment, per-student busy, daily request disable/limit and token limit. Assert failed/cancelled attempts release hard quota and successful retry is settled once.

- [ ] **Step 4: Write privacy and role tests**

Create two students. Probe other student's AI thread/run/events/file and require uniform 404. Assert student cannot reach admin AI APIs/routes, admin artificial queue contains no AI thread, and serialized HTML/network responses contain no key/ciphertext/object key/teacher note.

- [ ] **Step 5: Add mobile/accessibility coverage**

Add a mobile Chromium project for only tagged `@phase4-mobile` tests. Verify unified list→detail→back focus, composer channel cards, admin usage cards, no horizontal-only controls and accessible streaming status.

- [ ] **Step 6: Run tests against a local fixture stack to verify RED**

Run:

```bash
E2E_BASE_URL=http://127.0.0.1:8080 \
pnpm exec playwright test tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts
```

Expected: FAIL until the disposable Phase 4 harness supplies the configured fixture environment.

- [ ] **Step 7: Commit browser acceptance**

```bash
git add tests/e2e/ai-questions.spec.ts tests/e2e/ai-admin.spec.ts tests/e2e/ai-privacy.spec.ts tests/e2e/helpers.ts playwright.config.ts
git commit -m "test: cover phase 4 ai workflows"
```

---

### Task 3: Build the Disposable Phase 4 Harness and Contracts

**Files:**
- Create: `scripts/e2e-phase4.sh`
- Create: `scripts/e2e-phase4_contract_test.sh`
- Modify: `scripts/e2e-harness_semantics_contract_test.sh`
- Modify: `scripts/sanitize-e2e-artifacts.sh`
- Modify: `scripts/e2e-artifact-sanitization_contract_test.sh`
- Modify: `scripts/copy-e2e-workspace_test.sh`
- Modify: `Makefile`
- Modify: `package.json`

**Interfaces:**
- Adds:

```make
e2e-phase4:
	bash scripts/e2e-phase4.sh
```

- Accepts `HAPPYLEARN_E2E_GROUP=all|phase4`.
- Publishes sanitized artifacts only under `test-results/phase4`.

- [ ] **Step 1: Write RED harness contract tests**

Require the script to:

- validate an absolute readable AIStor license path;
- create prefixed internal network, PostgreSQL, Redis, MinIO, app, worker, fake provider and Playwright runner;
- inject a generated 32-byte base64 master key without printing it;
- set development private-provider allowance only inside the disposable app;
- never publish backend/fake-provider ports;
- use read-only/non-root/cap-drop/memory/CPU limits;
- include fake provider in diagnostics and sanitizer input;
- clean every prefixed resource.

- [ ] **Step 2: Run harness contract to verify RED**

Run: `bash scripts/e2e-phase4_contract_test.sh`

Expected: FAIL because `e2e-phase4.sh` does not exist.

- [ ] **Step 3: Implement the harness from the Phase 3 semantics**

Copy behavior, not unsafe shell output. Add `fake_ai_image`, `fake_ai` service name and cleanup. Generate provider key/master key into mode-0600 temp files or environment arrays without echoing values. Pass fixture Base URL `http://fake-ai:8090/v1`.

- [ ] **Step 4: Add staged test groups**

For `phase4`, run only the three AI specs plus mobile tag. For `all`, run Phase 1, Phase 2, Phase 3 and Phase 4 groups independently, preserve any failure, and publish only sanitized per-phase output.

- [ ] **Step 5: Extend artifact sanitization**

Reject patterns for:

- `Authorization: Bearer`;
- `HAPPYLEARN_AI_MASTER_KEY`;
- provider key values and ciphertext fields;
- AI prompt/message/answer marker text;
- object keys and MinIO credentials;
- database/Redis URLs.

Remove traces/screenshots/videos on any potential secret/content match.

- [ ] **Step 6: Run all shell contracts**

Run:

```bash
bash scripts/e2e-phase4_contract_test.sh
bash scripts/e2e-harness_semantics_contract_test.sh
bash scripts/e2e-artifact-sanitization_contract_test.sh
bash scripts/copy-e2e-workspace_test.sh
```

Expected: PASS.

- [ ] **Step 7: Run disposable Phase 4 E2E**

Run:

```bash
test -r "${HAPPYLEARN_AISTOR_LICENSE_FILE:?set HAPPYLEARN_AISTOR_LICENSE_FILE to the local AIStor license}"
HAPPYLEARN_E2E_GROUP=phase4 make e2e-phase4
```

Expected: PASS; artifacts are sanitized and all prefixed Docker resources are removed.

- [ ] **Step 8: Commit the harness**

```bash
git add scripts Makefile package.json
git commit -m "test: add disposable phase 4 acceptance"
```

---

### Task 4: Document Environment, Resource Limits, and Safe Operations

**Files:**
- Modify: `.env.example`
- Modify: `deploy/compose.dev.yml`
- Create: `docs/runbooks/phase4-ai-qanda.md`
- Modify: `docs/runbooks/local-development.md`

**Interfaces:**
- Documents and wires:

```text
HAPPYLEARN_AI_MASTER_KEY
HAPPYLEARN_AI_MASTER_KEY_VERSION=1
HAPPYLEARN_AI_BUSINESS_TIMEZONE=Asia/Shanghai
HAPPYLEARN_AI_GLOBAL_CONCURRENCY=2
HAPPYLEARN_AI_PER_STUDENT_CONCURRENCY=1
HAPPYLEARN_AI_ALLOW_PRIVATE_PROVIDER=false
```

- [ ] **Step 1: Write the runbook verification checklist**

Include executable, privacy-safe procedures for:

- injecting/rotating the master key without printing it;
- configuring/testing a provider in the teacher console;
- checking aggregate queue/status/error/unknown-usage counts;
- diagnosing queued, lost-runner, timeout, attachment and quota failures by UUID;
- correcting `quota_estimation_anomaly`;
- stopping new AI runs without deleting config/history;
- confirming no secret in API/logs;
- rollback by deploying Phase 3 images while leaving Phase 4 schema/data/objects intact.

- [ ] **Step 2: Update environment examples**

Use placeholders only for actual user-supplied secrets in `.env.example`, never a working production key. Explain generation with a command that writes directly to a protected file rather than stdout.

- [ ] **Step 3: Update development Compose**

Inject AI environment variables into app only. Keep provider Base URL unset until configured through UI. Preserve app `256m/.2 CPU` default and all private networks. Do not add fake provider to development Compose.

- [ ] **Step 4: Add resource inspection commands**

Document:

```bash
docker stats --no-stream happylearn-dev-app-1 happylearn-dev-worker-1
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -c \
  "SELECT status,count(*) FROM ai_runs GROUP BY status ORDER BY status;"
```

Queries must use counts, IDs and categories only—never message, prompt, key, URL query, event payload or object-key columns.

- [ ] **Step 5: Validate Compose and documentation commands**

Run:

```bash
docker compose -f deploy/compose.dev.yml config --quiet
rg -n 'apiKey|Authorization|body_text|payload_text|object_key' docs/runbooks/phase4-ai-qanda.md
```

Expected: Compose passes; every matched sensitive term appears only in a prohibition, never a diagnostic SELECT/log command.

- [ ] **Step 6: Commit operations documentation**

```bash
git add .env.example deploy/compose.dev.yml docs/runbooks/phase4-ai-qanda.md docs/runbooks/local-development.md
git commit -m "docs: add phase 4 ai operations"
```

---

### Task 5: Run the Full Phase 4 Gate and Close Review Findings

**Files:**
- Modify only files required by verified findings.
- Create: `docs/superpowers/plans/2026-07-26-phase4-final-review.md`

**Interfaces:**
- Produces final evidence and finding ledger with severity `Critical|Important|Minor`.
- Does not start Phase 5 work.

- [ ] **Step 1: Run backend quality gates**

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
make tools
.tools/bin/govulncheck ./...
```

Expected: PASS.

- [ ] **Step 2: Run frontend quality gates**

Run:

```bash
pnpm test
pnpm typecheck
pnpm lint
pnpm build
```

Expected: PASS.

- [ ] **Step 3: Run contracts and Compose**

Run:

```bash
make e2e-contracts
bash scripts/e2e-phase4_contract_test.sh
docker compose -f deploy/compose.dev.yml config --quiet
```

Expected: PASS.

- [ ] **Step 4: Run complete disposable E2E**

Run:

```bash
test -r "${HAPPYLEARN_AISTOR_LICENSE_FILE:?set HAPPYLEARN_AISTOR_LICENSE_FILE to the local AIStor license}"
HAPPYLEARN_E2E_GROUP=all make e2e-phase4
```

Expected: Phase 1–4 browser groups PASS and cleanup removes all prefixed resources.

- [ ] **Step 5: Verify the 2-core/4-GB budget**

During a fixture run with two simultaneous suppliers and one pending third run, capture only aggregate `docker stats --no-stream` values. Fail if the app exceeds its 256MiB limit/OOMs, worker exceeds 1792MiB, total configured memory exceeds 4GiB, or CPU limits exceed 2 cores.

- [ ] **Step 6: Perform the final diff review**

Write the review file with explicit checks:

```markdown
## Spec compliance
- [ ] Both protocols and single active provider
- [ ] Unified student Q&A center
- [ ] Durable replay without duplicate supplier calls
- [ ] Request/Token quota and usage/cost ledger

## Security and privacy
- [ ] SQL ownership and uniform 404
- [ ] Secret encryption/no readback
- [ ] SSRF/DNS/redirect protection
- [ ] Safe attachments/SSE/rendering/artifacts

## Operations and tests
- [ ] Crash recovery and idempotent settlement
- [ ] Resource limits and runbook
- [ ] Phase 1–4 regressions
```

List every finding with file, evidence, severity and disposition. Do not write “none” until the complete diff was inspected.

- [ ] **Step 7: Fix Critical and Important findings**

For each finding: add a reproducing test, run it RED, implement the smallest fix, run focused GREEN, then rerun Steps 1–5. Minor findings may be deferred only with exact rationale in the review file.

- [ ] **Step 8: Commit final review fixes and evidence**

```bash
git add docs/superpowers/plans/2026-07-26-phase4-final-review.md
git add -u
git commit -m "test: close phase 4 acceptance"
```

- [ ] **Step 9: Confirm the repository is clean**

Run:

```bash
git status --short
git log -8 --oneline --decorate
```

Expected: no status output. Only now may Phase 4 be reported complete.
