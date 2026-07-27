# Phase 6 Local Production Acceptance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the complete repository-owned production stack, release, rollback, recovery, restart, security, resource, CI, and cleanup behavior in a disposable local environment, then record the exact repository-ready boundary.

**Architecture:** Extend the proven disposable harness library with a unique production Compose project and local Caddy issuer. Build two immutable application image sets, exercise the real host scripts in local mode, run all prior browser groups, recover encrypted data into empty volumes, inject release failures, and fail closed unless all artifacts are sanitized and all resources are removed.

**Tech Stack:** Bash, Docker Compose, Caddy local TLS, PostgreSQL 18.4, Redis, licensed AIStor, Restic 0.19.1, Age 1.3.1, Playwright 1.57, GitHub Actions, Trivy, govulncheck.

---

## File structure

- Create `scripts/e2e-phase6.sh`, `e2e-phase6_contract_test.sh`.
- Create `scripts/e2e-phase6_security.sh`,
  `e2e-phase6_security_contract_test.sh`.
- Create `scripts/e2e-phase6_resources.sh`,
  `e2e-phase6_resources_contract_test.sh`.
- Create `tests/e2e/production.spec.ts`, `release-rollback.spec.ts`.
- Modify `playwright.config.ts`, `scripts/e2e-harness-lib.sh`,
  `scripts/e2e-harness_semantics_contract_test.sh`,
  `scripts/e2e-artifact-sanitization_contract_test.sh`,
  `scripts/sanitize-e2e-artifacts.sh`.
- Create `scripts/phase6-docs_contract_test.sh`.
- Create `docs/runbooks/phase6-local-production-acceptance.md`,
  `docs/runbooks/phase6-real-server-acceptance.md`.
- Modify `.github/workflows/verify.yml`,
  `scripts/ci-compose_contract_test.sh`,
  `scripts/ci-compose_contract_mutation_test.sh`,
  `Makefile`, `package.json`.
- Create `docs/superpowers/plans/2026-07-28-phase6-final-review.md` at the end.

### Task 1: Define browser-visible production acceptance

- [ ] **Step 1: Write failing production browser tests**

Create `tests/e2e/production.spec.ts`:

```ts
test('@phase6 public edge enforces TLS and privacy', async ({ page, request }) => {
  // HTTP redirects to the disposable HTTPS hostname.
  // Security headers satisfy the existing Markdown, KaTeX, preview, and video contracts.
  // /internal/metrics and /internal/readiness return 404 publicly.
  // Oversized normal and upload requests are rejected at the edge.
  // AI SSE remains incremental and unbuffered.
})

test('@phase6 maintenance mode is static and fail-closed', async ({ page }) => {
  // Maintenance returns 503 plus Retry-After.
  // It exposes no version, trace, dependency, or user detail.
  // Business API writes do not reach app.
})

test('@phase6 production restart preserves durable data', async ({ page }) => {
  // Confirm published course, file metadata, Q&A, operations, and backup evidence after restart.
  // Confirm Redis-only coordination state may be rebuilt safely.
})
```

- [ ] **Step 2: Write failing release browser tests**

Create `tests/e2e/release-rollback.spec.ts`:

```ts
test('@phase6 successful release exposes the second safe version', async ({ page }) => {
  // Observe maintenance during release, normal traffic afterward, and preserved data.
  // Read safe version evidence through the authenticated admin operations view.
})

test('@phase6 failed release restores the previous compatible image', async ({ page }) => {
  // Inject private readiness failure, observe maintenance, then previous version.
  // Confirm no database restore, session resurrection, or lost accepted write.
})
```

- [ ] **Step 3: Run and verify RED**

```bash
pnpm exec playwright test tests/e2e/production.spec.ts \
  tests/e2e/release-rollback.spec.ts
```

Expected: FAIL because the production harness is not running.

- [ ] **Step 4: Update Playwright projects**

Add stable `phase6` and `phase6-mobile` selection without weakening Phase 1–5
desktop or mobile coverage. Trust only the harness-exported local Caddy root
certificate for the disposable hostname.

- [ ] **Step 5: Commit the RED browser contracts**

```bash
git add tests/e2e/production.spec.ts tests/e2e/release-rollback.spec.ts \
  playwright.config.ts
git commit -m "test(phase6): define production browser acceptance"
```

### Task 2: Build the disposable production harness

- [ ] **Step 1: Write the failing harness contract**

Create `scripts/e2e-phase6_contract_test.sh`. Require:

- prefix `happylearn_phase6_` plus a unique run suffix;
- production Compose, local override, and local Caddy configuration;
- real PostgreSQL, Redis, licensed AIStor, Caddy, app, worker, migration,
  backup, restore, acceptance, and browser services;
- two image sets identified by locally computed immutable digests;
- a run-scoped loopback-only OCI registry used to publish and resolve those
  local digests without relaxing the production Compose image rules;
- unique networks, volumes, bind paths, ports, hostname, and certificate store;
- owner-only generated secret files with test-run-scoped values;
- no secrets in argv, environment, Compose output, image history, diagnostics,
  browser traces, or uploadable artifacts;
- public reachability through Caddy only;
- real host release scripts in `--mode local`;
- bounded waits, signal-safe cleanup, first-failure preservation, and a final
  zero-resource proof.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/e2e-phase6_contract_test.sh
```

Expected: FAIL because `scripts/e2e-phase6.sh` is absent.

- [ ] **Step 3: Implement the harness lifecycle**

Reuse `scripts/e2e-harness-lib.sh`; add generic helpers there only when Phase 4
and Phase 5 contracts remain green. The lifecycle is:

```text
validate licensed prerequisites
create owner-only run root
allocate loopback ports and disposable hostname
generate secret files
start a loopback-only run-scoped OCI registry
build image set A, push it to the registry, and resolve immutable digests
render manifests and configuration hashes
start production data services
run migrate and admin bootstrap
start app, worker, and Caddy local TLS
export browser trust
run selected group
capture bounded sanitized diagnostics on first failure
sanitize canonical artifacts
remove exact project resources and temporary image tags
remove the exact run-scoped OCI registry and repository data
prove zero residue
```

Support:

```text
HAPPYLEARN_E2E_GROUP=all|install|regression|mobile|recovery|release|rollback|restart|security|resources
```

- [ ] **Step 4: Keep test-only controls out of server mode**

Failure injectors, local issuer, high loopback ports, fixture DNS, local image
digest mapping, and license test paths are accepted only when the production
scripts receive `--mode local`. Contracts must prove server mode rejects every
test-only variable.

- [ ] **Step 5: Run install and cleanup groups**

```bash
HAPPYLEARN_E2E_GROUP=install bash scripts/e2e-phase6.sh
HAPPYLEARN_E2E_GROUP=restart bash scripts/e2e-phase6.sh
```

Expected: both pass and leave no prefixed resource.

- [ ] **Step 6: Commit**

```bash
git add scripts/e2e-phase6.sh scripts/e2e-phase6_contract_test.sh \
  scripts/e2e-harness-lib.sh scripts/e2e-harness_semantics_contract_test.sh
git commit -m "test(phase6): add disposable production harness"
```

### Task 3: Run the complete Phase 1–5 regression through Caddy

- [ ] **Step 1: Add the regression group**

The `regression` group runs every established Phase 1–5 desktop Chromium test
through the HTTPS Caddy origin. It must include:

```text
admin bootstrap and teacher/student authorization
course and teaching publication
secure preview and download
notifications and Q&A
AI streaming, quota, usage, cancellation, and recovery
operations dashboard and non-secret settings
audit privacy, alerts, backup history, and restore evidence
cross-student isolation and CSRF
```

- [ ] **Step 2: Add mobile regression**

The `mobile` group runs all established mobile paths and the Phase 6 mobile
edge path at the same viewport and interaction constraints already approved.

- [ ] **Step 3: Run regression**

```bash
HAPPYLEARN_E2E_GROUP=regression bash scripts/e2e-phase6.sh
HAPPYLEARN_E2E_GROUP=mobile bash scripts/e2e-phase6.sh
```

Expected: PASS without direct app or data-service access.

- [ ] **Step 4: Commit harness fixes**

```bash
git add scripts/e2e-phase6.sh tests/e2e playwright.config.ts
git commit -m "test(phase6): prove prior phases behind production edge"
```

### Task 4: Prove encrypted recovery and restored security state

- [ ] **Step 1: Implement the recovery group**

Create data spanning users, sessions, published teaching, secure files, Q&A,
AI accounting, operations settings, audit events, alerts, and backup evidence.
Create and verify a local encrypted recovery point, then restore into exact new
empty volumes under a non-production project.

- [ ] **Step 2: Verify recovery invariants**

Require:

- database and AIStor inventory hashes match;
- files can be authorized, previewed, and downloaded;
- revoked and pre-backup sessions remain unusable;
- all restored active sessions are revoked by the restore workflow;
- cross-student isolation and CSRF remain enforced;
- operations versions, audit privacy, backup evidence, and alert states remain
  consistent;
- wrong key, tampered pack, and missing object fail closed;
- original volumes remain detached and recoverable;
- RPO evidence is at most 24 hours and measured RTO is at most four hours.

- [ ] **Step 3: Run recovery**

```bash
HAPPYLEARN_E2E_GROUP=recovery bash scripts/e2e-phase6.sh
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add scripts/e2e-phase6.sh tests/e2e
git commit -m "test(phase6): prove production encrypted recovery"
```

### Task 5: Prove release, rollback, interruption, and restart

- [ ] **Step 1: Build image set B**

Build the same code with a distinct safe semantic version and commit marker,
compute local image digests, write a strict manifest, and prove schema
compatibility. Do not change application behavior solely to make the version
visible publicly.

- [ ] **Step 2: Run a successful release**

Use `prod-release.sh --mode local` and verify all durable states, pre-release
backup, static maintenance, migration, readiness, smoke, manifest promotion,
normal mode, traffic reopening, data preservation, and sanitized evidence.

- [ ] **Step 3: Run injected failure and rollback**

Create a third manifest whose app readiness is test-injected to fail. Verify
automatic rollback to image set B, unchanged forward schema, no database
restore, no accepted-write loss, and no session resurrection.

- [ ] **Step 4: Run the full failure matrix**

```bash
bash scripts/phase6-release_failure_matrix.sh
```

Expected: every preflight, backup, drain, migration, readiness, dependency,
smoke, interruption, compatibility, and previous-readiness case passes.

- [ ] **Step 5: Prove restart**

Restart individual app, worker, Caddy, PostgreSQL, Redis, and AIStor containers,
then stop and restart the exact Compose project. Verify automated recovery,
health order, durable data, current manifest, normal operational mode, and
private service boundaries.

- [ ] **Step 6: Run focused groups**

```bash
HAPPYLEARN_E2E_GROUP=release bash scripts/e2e-phase6.sh
HAPPYLEARN_E2E_GROUP=rollback bash scripts/e2e-phase6.sh
HAPPYLEARN_E2E_GROUP=restart bash scripts/e2e-phase6.sh
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add scripts/e2e-phase6.sh scripts/phase6-release_failure_matrix.sh \
  tests/e2e/release-rollback.spec.ts
git commit -m "test(phase6): prove release rollback and restart"
```

### Task 6: Add security and privacy acceptance

- [ ] **Step 1: Write the failing security contract**

Create `scripts/e2e-phase6_security_contract_test.sh`. Require checks for:

```text
public 80/443 only
private PostgreSQL Redis AIStor app and internal listener
TLS and approved security headers
upload limits and AI SSE
/internal/* public denial
query-free access logs
secret and PII scan
Compose and rendered configuration scan
image history scan
browser trace and artifact scan
authorization and CSRF regression
wrong-key and repository-tamper recovery
dependency, reachable-Go, frontend, and image vulnerability scans
```

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/e2e-phase6_security_contract_test.sh
```

Expected: FAIL because the security runner is absent.

- [ ] **Step 3: Implement the security runner**

Create `scripts/e2e-phase6_security.sh`. Use bounded scanners with checked-in
policy and severity thresholds. Keep raw scanner caches outside uploadable
artifacts. Normalize findings to package/image identifier, advisory, severity,
fixed version, policy result, and trace ID; never retain environment or file
content.

- [ ] **Step 4: Add artifact sanitizer coverage**

Extend the fail-closed sanitizer and its contract with generated production
secrets, URLs, bearer/HMAC material, repository credentials, license data,
cookies, authorization, query strings, object keys, database row fragments,
home/project paths, and Docker inspect output.

- [ ] **Step 5: Run security acceptance**

```bash
HAPPYLEARN_E2E_GROUP=security bash scripts/e2e-phase6.sh
bash scripts/e2e-artifact-sanitization_contract_test.sh
```

Expected: PASS with no open Critical or Important policy finding.

- [ ] **Step 6: Commit**

```bash
git add scripts/e2e-phase6_security.sh \
  scripts/e2e-phase6_security_contract_test.sh \
  scripts/sanitize-e2e-artifacts.sh \
  scripts/e2e-artifact-sanitization_contract_test.sh
git commit -m "test(phase6): enforce production security acceptance"
```

### Task 7: Prove configured and live resource ceilings

- [ ] **Step 1: Write the failing resource contract**

Create `scripts/e2e-phase6_resources_contract_test.sh`. It must recompute
Compose memory and CPU totals rather than trust documentation:

```text
steady: <= 3072 MiB and <= 1.85 CPU
backup with worker stopped: <= 1792 MiB and <= 1.10 CPU
host ceiling: <= 4096 MiB and <= 2 CPU
steady memory headroom: >= 1024 MiB
worker and backup mutual exclusion: enforced by release/backup state
```

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/e2e-phase6_resources_contract_test.sh
```

Expected: FAIL because the live resource runner is absent.

- [ ] **Step 3: Implement live capture**

Create `scripts/e2e-phase6_resources.sh`. Sample for 30 minutes at a bounded
interval during representative concurrent teaching reads, secure file access,
notifications, Q&A, AI SSE, operations reads, host samples, and Caddy traffic.
Run one live backup sample only after drain and worker stop.

Record per-service and aggregate CPU, working-set memory, restart count,
health, request latency buckets, and timestamps. Do not record container
environment, process arguments, object names, user IDs, URLs with query, or
request bodies.

- [ ] **Step 4: Run configured and live proof**

```bash
bash scripts/e2e-phase6_resources_contract_test.sh
HAPPYLEARN_E2E_GROUP=resources bash scripts/e2e-phase6.sh
```

Expected: configured arithmetic and live capture remain within 2 CPU/4 GiB,
with the documented steady memory headroom.

- [ ] **Step 5: Commit**

```bash
git add scripts/e2e-phase6_resources.sh \
  scripts/e2e-phase6_resources_contract_test.sh scripts/e2e-phase6.sh
git commit -m "test(phase6): prove production resource ceilings"
```

### Task 8: Add operator documentation and boundary contracts

- [ ] **Step 1: Write the failing documentation contract**

Create `scripts/phase6-docs_contract_test.sh`. Require:

- local production prerequisites and exact group commands;
- artifact locations and sanitizer behavior;
- release, rollback, failed-safe, recovery, and cleanup evidence;
- real Ubuntu inventory and read-only preflight;
- separate approvals for package/service installation, DNS, firewall, public
  TLS, reboot, production restore switch, release candidate, and final tag;
- real-server backup/restore, RTO, desktop/mobile, restart, observation, alert,
  and rollback checks;
- the exact status sentence:
  `Phase 6 repository production-ready; real-server acceptance pending.`;
- explicit prohibition on claiming final Phase 6 or creating `v1.0.0` early.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/phase6-docs_contract_test.sh
```

Expected: FAIL because the runbooks are absent.

- [ ] **Step 3: Write local acceptance runbook**

Document licensed prerequisites, groups, expected duration, generated files,
safe cancellation, state inspection, artifact handling, cleanup verification,
common fail-closed outcomes, and how to reproduce one group without preserving
secrets.

- [ ] **Step 4: Write real-server acceptance runbook**

Document read-only host inventory, user approval boundaries, DNS/public TLS,
firewall reachability, first production backup and isolated empty restore,
four-hour RTO measurement, reboot recovery, desktop/mobile tests, resource and
latency observation, timer/alert evidence, rollback rehearsal, post-release
observation, release-candidate approval, and final `v1.0.0` gate.

- [ ] **Step 5: Verify and commit**

```bash
bash scripts/phase6-docs_contract_test.sh
git add scripts/phase6-docs_contract_test.sh docs/runbooks
git commit -m "docs(phase6): define production acceptance gates"
```

### Task 9: Add CI without weakening licensed local acceptance

- [ ] **Step 1: Extend CI contract tests**

Require a `phase6-contracts` job for Go tests, Compose/Caddy contracts,
mutations, release/rollback/restore/systemd contracts, ShellCheck, sanitizer,
security policy, resource arithmetic, and docs.

Require a licensed `phase6-production` job only when the established protected
AIStor license secret is available. It runs disposable install, regression,
mobile, recovery, release, rollback, restart, and short security groups. The
30-minute resource group remains a protected scheduled/manual job.

- [ ] **Step 2: Prove failure semantics**

Mutation tests must reject:

```text
continue-on-error on required jobs
licensed job silently skipped when explicitly requested
unsanitized artifact upload
upload outside canonical test-results root
missing cleanup trap
missing timeout
floating image or unpinned action
resource job without 30-minute capture
```

- [ ] **Step 3: Update workflow and targets**

Add:

```make
phase6-contracts:
	# all repository-only Phase 6 contracts

e2e-phase6:
	bash scripts/e2e-phase6.sh
```

Expose both through `package.json`. Keep the complete Phase 4 and Phase 5 gates
unchanged.

- [ ] **Step 4: Verify**

```bash
make phase6-contracts
bash scripts/ci-compose_contract_test.sh
bash scripts/ci-compose_contract_mutation_test.sh
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/verify.yml scripts Makefile package.json
git commit -m "ci(phase6): add production release acceptance"
```

### Task 10: Execute the repository-ready gate

- [ ] **Step 1: Run static and contract verification**

```bash
go test ./...
go vet ./...
pnpm lint
pnpm typecheck
pnpm test
pnpm build
make phase6-contracts
```

Expected: PASS.

- [ ] **Step 2: Run full Phase 5 regression**

```bash
make e2e-phase5
```

Expected: PASS, including recovery and operations evidence.

- [ ] **Step 3: Run complete disposable Phase 6**

```bash
HAPPYLEARN_E2E_GROUP=all make e2e-phase6
HAPPYLEARN_E2E_GROUP=resources make e2e-phase6
```

Expected: PASS, including 30-minute live capture and zero-resource cleanup.

- [ ] **Step 4: Run final security and artifact gates**

```bash
bash scripts/e2e-phase6_security.sh
bash scripts/e2e-artifact-sanitization_contract_test.sh
git diff --check
git status --short
```

Expected: security policy passes, artifacts are safe, whitespace is clean, and
only intentional review documentation is uncommitted.

### Task 11: Perform complete-diff review and record the boundary

- [ ] **Step 1: Create the review record**

Create `docs/superpowers/plans/2026-07-28-phase6-final-review.md` with:

```markdown
# Phase 6 Final Review

## Scope and commits
## Specification coverage
## Correctness and failure handling
## Security and privacy
## Backup, restore, release, and rollback evidence
## Production topology and resources
## Browser, mobile, restart, and cleanup evidence
## CI and operator documentation
## Findings
## Fixes and re-verification
## Repository-ready result
## Real-server work still pending
```

- [ ] **Step 2: Review the complete Phase 6 diff**

Review from the Phase 5 completion commit through `HEAD`. Trace every approved
design requirement to implementation and fresh evidence. Classify findings as
Critical, Important, or Minor; fix all Critical and Important findings.

- [ ] **Step 3: Rerun affected tests and the final gate**

Run focused tests after each fix, then rerun Task 10 in full. Record commands,
timestamps, safe artifact hashes, result, and remaining Minor findings.

- [ ] **Step 4: Record the exact outcome**

The result must say:

```text
Phase 6 repository production-ready; real-server acceptance pending.
```

Also state that no real server, DNS, firewall, public certificate, service
installation, reboot, production restore switch, `v1.0.0-rc.1`, or `v1.0.0`
action was performed.

- [ ] **Step 5: Commit and prove cleanliness**

```bash
git add docs/superpowers/plans/2026-07-28-phase6-final-review.md
git commit -m "docs(phase6): record repository-ready review"
git status --short
```

Expected: clean repository. Do not create a release tag. A release candidate
requires separate user authorization; final Phase 6 requires the real-server
acceptance gate.
