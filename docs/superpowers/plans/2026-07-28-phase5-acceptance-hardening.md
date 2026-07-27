# Phase 5 Acceptance and Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove Phase 5 end to end with real licensed dependencies, empty-environment recovery, alert and backup failure injection, resource capture, CI, runbooks, and complete-diff security review.

**Architecture:** Extend the disposable Phase 4 harness rather than weakening it. The Phase 5 harness creates unique networks, volumes, repositories, owner-only secrets, an optional remote S3 fixture, app/worker/backup images, and one browser runner. Diagnostics remain fail-closed beneath `test-results/phase5`.

**Tech Stack:** Bash, Docker, PostgreSQL 18.4, AIStor, Restic, Age, Playwright 1.57, GitHub Actions.

---

## File structure

- Create `tests/e2e/operations.spec.ts`.
- Create `tests/e2e/backup-restore.spec.ts`.
- Modify `playwright.config.ts`.
- Create `scripts/e2e-phase5.sh`, `e2e-phase5_contract_test.sh`.
- Create `scripts/e2e-phase5_failure_matrix.sh`,
  `e2e-phase5_failure_matrix_contract_test.sh`.
- Create `scripts/phase5-operations-docs_contract_test.sh`.
- Create `docs/runbooks/phase5-operations-backup.md`.
- Modify `scripts/e2e-harness-lib.sh`,
  `scripts/e2e-harness_semantics_contract_test.sh`,
  `scripts/e2e-artifact-sanitization_contract_test.sh`,
  `scripts/sanitize-e2e-artifacts.sh`, `Makefile`, `package.json`.
- Modify `.github/workflows/verify.yml`,
  `scripts/ci-compose_contract_test.sh`,
  `scripts/ci-compose_contract_mutation_test.sh`.
- Create `docs/superpowers/plans/2026-07-28-phase5-final-review.md` at the end.

### Task 1: Add Phase 5 browser acceptance

- [ ] **Step 1: Write failing operations browser tests**

`tests/e2e/operations.spec.ts` must use role tags:

```ts
test('@phase5 teacher manages operations without exposing secrets', async ({ page }) => {
  // Sign in as teacher.
  // Verify dashboard health, observed time, backup and alert summaries.
  // Change non-secret settings and verify optimistic version update.
  // Verify infrastructure secrets show configured/not-configured only.
  // Filter audit events and confirm no forbidden metadata key appears.
  // Acknowledge one injected alert and prove it remains unresolved.
})

test('@phase5 student cannot access operations', async ({ page, request }) => {
  // Every operations route redirects or returns 403.
  // /internal/metrics returns 404 through the public listener.
})

test('@phase5-mobile operations remain usable on mobile', async ({ page }) => {
  // Alerts, backup, service health, then summaries.
  // Open drawer, settings and alert detail with keyboard-visible controls.
})
```

- [ ] **Step 2: Write failing backup history test**

`backup-restore.spec.ts` queues a manual backup with one idempotency key,
retries the same action, observes one run, distinguishes local and remote state,
and displays a successful restore-verification record without offering restore.

- [ ] **Step 3: Run and verify RED**

Run against the current Phase 4 stack:

```bash
pnpm exec playwright test tests/e2e/operations.spec.ts \
  tests/e2e/backup-restore.spec.ts
```

Expected: FAIL because Phase 5 routes are absent from the stack.

- [ ] **Step 4: Update Playwright projects**

Rename the current mobile project to a stable `mobile` project and include
`@phase4-mobile|@phase5-mobile` without changing existing Phase 4 coverage.
Desktop excludes both mobile tags.

- [ ] **Step 5: Commit the RED browser contracts**

```bash
git add tests/e2e/operations.spec.ts tests/e2e/backup-restore.spec.ts \
  playwright.config.ts
git commit -m "test(phase5): define operations browser acceptance"
```

### Task 2: Build the disposable Phase 5 harness

- [ ] **Step 1: Write the failing shell contract**

Require:

- absolute readable AIStor license;
- prefix `happylearn_phase5_`;
- unique internal network;
- PostgreSQL, Redis, primary AIStor, remote S3 fixture, app, worker, backup,
  host-sample helper, and browser runner;
- owner-only AI, database, object, metrics, HMAC, Restic, Age, webhook, and
  remote repository secrets;
- no secret in Docker argv or configured environment;
- app/worker/backup read-only roots and non-root users;
- no privileged mode, host network, or Docker socket mount;
- real local backup, remote copy, empty restore, and browser groups;
- 30-minute resource sample path;
- signal-safe cleanup and sanitized diagnostics.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/e2e-phase5_contract_test.sh
```

Expected: FAIL because `scripts/e2e-phase5.sh` is absent.

- [ ] **Step 3: Implement the harness**

Reuse `e2e-harness-lib.sh` functions for bounded Docker calls, condition waits,
first-failure preservation, artifact roots, sanitization, and cleanup. Do not
copy helper logic.

Support:

```text
HAPPYLEARN_E2E_GROUP=all|phase5|phase5-mobile|recovery|resources
```

The `all` group runs Phase 1–5 desktop, Phase 4/5 mobile, backup, empty restore,
and the short resource proof. The `resources` group runs the 30-minute sample.

- [ ] **Step 4: Add Make and package targets**

```make
e2e-phase5:
	bash scripts/e2e-phase5.sh
```

```json
"e2e-phase5": "make e2e-phase5"
```

Add both Phase 5 shell contracts to `e2e-contracts`.

- [ ] **Step 5: Run focused groups**

```bash
HAPPYLEARN_E2E_GROUP=phase5 make e2e-phase5
HAPPYLEARN_E2E_GROUP=phase5-mobile make e2e-phase5
HAPPYLEARN_E2E_GROUP=recovery make e2e-phase5
```

Expected: every group passes and leaves no prefixed resource.

- [ ] **Step 6: Commit**

```bash
git add scripts/e2e-phase5.sh scripts/e2e-phase5_contract_test.sh \
  scripts/e2e-harness-lib.sh Makefile package.json
git commit -m "test(phase5): add disposable operations acceptance"
```

### Task 3: Prove the failure matrix

- [ ] **Step 1: Write the failure-matrix contract**

Require named cases:

```text
drain_timeout
database_dump_failure
object_store_stop_failure
snapshot_failure
object_store_restart_failure
repository_integrity_failure
remote_outage
retention_failure
wrong_repository_secret
tampered_pack
missing_restored_object
stale_restored_session
webhook_private_target
webhook_timeout
host_sample_replay
```

Each case must assert terminal state, maintenance-mode recovery, alert state,
no plaintext dump, sanitized artifacts, and cleanup.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/e2e-phase5_failure_matrix_contract_test.sh
```

Expected: FAIL because the matrix runner is absent.

- [ ] **Step 3: Implement deterministic injection**

Use test-only fixture endpoints or command shims inside disposable containers.
Do not add production environment switches. A case receives one unique project
and one bounded deadline. Record a JSON summary with case name, expected state,
actual state, duration, and trace ID only.

- [ ] **Step 4: Run all cases**

```bash
bash scripts/e2e-phase5_failure_matrix.sh
```

Expected: every case PASS; `remote_outage` yields a verified local
`degraded` run; all other unrecoverable snapshot failures yield `failed`.

- [ ] **Step 5: Commit**

```bash
git add scripts/e2e-phase5_failure_matrix.sh \
  scripts/e2e-phase5_failure_matrix_contract_test.sh
git commit -m "test(phase5): cover backup and alert failure matrix"
```

### Task 4: Harden artifacts and resource evidence

- [ ] **Step 1: Extend fail-closed sanitization tests**

Add Phase 5 fixtures containing fake Restic passwords, Age identities, webhook
URLs, HMAC tokens, repository paths, PostgreSQL dump signatures, object keys,
student content, and query strings. Prove the sanitizer rejects all of them and
publishes no unsafe directory.

- [ ] **Step 2: Run RED then implement**

```bash
bash scripts/e2e-artifact-sanitization_contract_test.sh
```

Expected before sanitizer updates: FAIL on at least one Phase 5 marker.

Extend the fixed forbidden patterns and safe diagnostic projection. Never
sanitize by mutating source repositories or backup evidence.

- [ ] **Step 3: Add live resource capture**

During the heaviest allowed operation:

- pause new worker claims;
- run backup with worker stopped;
- capture app, worker/backup, PostgreSQL, Redis, AIStor, and browser memory/CPU;
- prove worker and backup are never simultaneously active;
- inspect `OOMKilled=false` and restart counts through bounded selected fields;
- assert configured and live totals stay within 2 CPU and 4 GB.

- [ ] **Step 4: Run the resource group**

```bash
HAPPYLEARN_E2E_GROUP=resources make e2e-phase5
```

Expected: PASS after 30 minutes with a sanitized aggregate report.

- [ ] **Step 5: Commit**

```bash
git add scripts/sanitize-e2e-artifacts.sh \
  scripts/e2e-artifact-sanitization_contract_test.sh \
  scripts/e2e-harness_semantics_contract_test.sh scripts/e2e-phase5.sh
git commit -m "test(phase5): harden evidence and resource capture"
```

### Task 5: Write operations and disaster-recovery runbooks

- [ ] **Step 1: Write the failing docs contract**

Require exact sections for:

- owner-only secret creation and permission checks;
- local and optional S3 repository initialization;
- daily schedule and manual backup;
- healthy, degraded, and failed interpretation;
- alert acknowledgement and webhook testing;
- safe diagnostics;
- empty-environment restore verification;
- RPO/RTO measurement;
- destructive restore prohibition from the web UI;
- lost repository credential and lost Age identity response;
- cleanup, retention, and disk pressure;
- Phase 4 rollback compatibility without deleting Phase 5 data.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/phase5-operations-docs_contract_test.sh
```

Expected: FAIL because the runbook is absent.

- [ ] **Step 3: Write the runbook**

Create `docs/runbooks/phase5-operations-backup.md`. Commands use explicit
project names and paths, operator-supplied values marked with explicit
`<OPERATOR_VALUE>` tokens, no real credential, no broad recursive deletion,
and a warning before every destructive restore command.

- [ ] **Step 4: Verify docs and shell syntax**

```bash
bash scripts/phase5-operations-docs_contract_test.sh
bash -n scripts/e2e-phase5.sh scripts/e2e-phase5_failure_matrix.sh
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add docs/runbooks/phase5-operations-backup.md \
  scripts/phase5-operations-docs_contract_test.sh Makefile
git commit -m "docs(phase5): add operations and recovery runbook"
```

### Task 6: Add Phase 5 CI

- [ ] **Step 1: Extend workflow allowlist contracts in RED**

Add one `phase5-e2e` job requiring `verify`, Ubuntu 24.04, a 120-minute timeout,
the existing AIStor license setup, `HAPPYLEARN_E2E_GROUP=all make e2e-phase5`,
sanitized failure artifact upload, and no extra permissions.

The workflow root/job key allowlists must reject any unknown key, action, shell
escape, unpinned action, skipped test, or secret-printing step.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/ci-compose_contract_test.sh
bash scripts/ci-compose_contract_mutation_test.sh
```

Expected: FAIL because the Phase 5 job is absent.

- [ ] **Step 3: Add the job**

Use the same license-file handling and safe artifact rules as existing E2E jobs.
Upload only the sanitized Phase 5 diagnostic file with seven-day retention.

- [ ] **Step 4: Run local contracts**

```bash
pnpm e2e-contracts
actionlint .github/workflows/verify.yml
git diff --check
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/verify.yml scripts/ci-compose_contract_test.sh \
  scripts/ci-compose_contract_mutation_test.sh
git commit -m "ci(phase5): verify operations and recovery"
```

### Task 7: Run the final Phase 5 gate and review

- [ ] **Step 1: Run the complete matrix**

```bash
GOENV=off GOFLAGS='' go test -p 1 ./... -count=1
GOENV=off GOFLAGS='' go test -race -p 1 ./... -count=1
GOENV=off GOFLAGS='' go vet ./...
.tools/bin/govulncheck ./...
pnpm test
pnpm typecheck
pnpm lint
pnpm build
pnpm audit --prod
pnpm e2e-contracts
HAPPYLEARN_E2E_GROUP=all make e2e-phase5
bash scripts/e2e-phase5_failure_matrix.sh
docker compose -f deploy/compose.dev.yml config --quiet
git diff --check
```

Expected: all PASS with licensed AIStor and the optional remote fixture.

- [ ] **Step 2: Perform complete-diff review**

Review from the Phase 4 closure commit through HEAD for:

- specification coverage;
- role and data isolation;
- backup consistency and cryptography;
- secret custody and diagnostics;
- alert correctness and SSRF;
- audit immutability and retention;
- RPO, RTO, resource arithmetic, and cleanup;
- test adequacy and false-positive gates.

- [ ] **Step 3: Fix findings and rerun**

Fix every Critical or Important finding with focused RED/GREEN evidence, then
rerun Step 1 in full.

- [ ] **Step 4: Record final disposition**

Create `docs/superpowers/plans/2026-07-28-phase5-final-review.md` with the fresh
matrix, resource evidence, recovery evidence, findings, and:

```text
Status: PASS — Phase 5 acceptance is complete
```

- [ ] **Step 5: Commit**

```bash
git add docs/superpowers/plans/2026-07-28-phase5-final-review.md
git commit -m "test: close phase 5 acceptance"
```
