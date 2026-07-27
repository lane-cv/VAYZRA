# CI Compose Host-Port Access Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make PostgreSQL, Redis, and AIStor reachable from GitHub-hosted Go tests without weakening the default internal Compose network.

**Architecture:** Keep `deploy/compose.dev.yml` as the hardened base configuration and add a minimal CI-only Compose override that makes the merged CI network non-internal. The workflow will use the same merged configuration for startup, validation, and cleanup, and will verify loopback port reachability before running tests.

**Tech Stack:** GitHub Actions YAML, Docker Compose, Bash contract tests, pnpm/Make.

## Global Constraints

- `deploy/compose.dev.yml` must retain `internal: true`.
- Only the ephemeral CI network may use `internal: false`.
- Published service ports must remain bound to `127.0.0.1`.
- Startup and cleanup must use project name `happylearn-ci` and the same Compose file pair.
- AIStor license handling and service credentials must remain unchanged.

---

### Task 1: Lock the CI network contract

**Files:**
- Create: `scripts/ci-compose_contract_test.sh`
- Modify: `Makefile:45-52`

**Interfaces:**
- Consumes: `deploy/compose.dev.yml`, `deploy/compose.ci.yml`, `.github/workflows/verify.yml`.
- Produces: an executable Bash contract test invoked by `make e2e-contracts`.

- [ ] **Step 1: Write the failing contract test**

Create `scripts/ci-compose_contract_test.sh`:

```bash
#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
base="$repo_root/deploy/compose.dev.yml"
ci="$repo_root/deploy/compose.ci.yml"
workflow="$repo_root/.github/workflows/verify.yml"
compose_args='-f deploy/compose.dev.yml -f deploy/compose.ci.yml'

grep -Fq 'internal: true' "$base"
test -f "$ci"
grep -Fq 'internal: false' "$ci"
test "$(grep -Fc "$compose_args" "$workflow")" -ge 3
grep -Fq 'Verify host integration ports' "$workflow"
for port in 54329 56379 59000; do
  grep -Fq "$port" "$workflow"
done

echo 'CI Compose host-port contract: PASS'
```

Add this line to the `e2e-contracts` target in `Makefile`:

```make
	bash scripts/ci-compose_contract_test.sh
```

- [ ] **Step 2: Run the contract and verify RED**

Run:

```bash
bash scripts/ci-compose_contract_test.sh
```

Expected: FAIL at `test -f "$ci"` because `deploy/compose.ci.yml` does not exist.

- [ ] **Step 3: Commit the failing contract**

```bash
git add scripts/ci-compose_contract_test.sh Makefile
git commit -m "test(ci): specify Compose host port access"
```

---

### Task 2: Add the CI-only network override

**Files:**
- Create: `deploy/compose.ci.yml`

**Interfaces:**
- Consumes: the `happylearn` network declared in `deploy/compose.dev.yml`.
- Produces: a Compose override that changes only `networks.happylearn.internal`.

- [ ] **Step 1: Add the minimal override**

Create `deploy/compose.ci.yml`:

```yaml
networks:
  happylearn:
    internal: false
```

- [ ] **Step 2: Verify base and merged Compose semantics**

Run:

```bash
HAPPYLEARN_AISTOR_LICENSE_FILE="/Users/lane/Downloads/minio.license" \
  docker compose -f deploy/compose.dev.yml config --format json
HAPPYLEARN_AISTOR_LICENSE_FILE="/Users/lane/Downloads/minio.license" \
  docker compose -f deploy/compose.dev.yml -f deploy/compose.ci.yml config --format json
```

Expected: base JSON contains `"internal": true`; merged JSON contains `"internal": false`; all published addresses remain `127.0.0.1`.

- [ ] **Step 3: Commit the override**

```bash
git add deploy/compose.ci.yml
git commit -m "fix(ci): expose integration services to runner"
```

---

### Task 3: Use the override consistently in GitHub Actions

**Files:**
- Modify: `.github/workflows/verify.yml:39-66`

**Interfaces:**
- Consumes: the Compose file pair from Task 2.
- Produces: runner-visible dependencies, an early port-reachability failure, and symmetric cleanup.

- [ ] **Step 1: Update dependency startup**

Replace the startup command with:

```yaml
      - name: Start private integration dependencies
        run: docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml up -d --wait --wait-timeout 120 postgres redis minio
```

- [ ] **Step 2: Add the host port readiness check**

Immediately after startup, add:

```yaml
      - name: Verify host integration ports
        run: |
          for port in 54329 56379 59000; do
            timeout 30 bash -c "until </dev/tcp/127.0.0.1/$port; do sleep 1; done"
          done
```

- [ ] **Step 3: Validate both base and merged configurations**

Keep the existing base validation and add:

```yaml
      - run: docker compose -f deploy/compose.dev.yml -f deploy/compose.ci.yml config --quiet
```

- [ ] **Step 4: Make cleanup symmetric**

Replace the cleanup command with:

```yaml
      - name: Stop integration dependencies
        if: always()
        run: docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml down --volumes --remove-orphans
```

- [ ] **Step 5: Run the focused contract and verify GREEN**

Run:

```bash
bash scripts/ci-compose_contract_test.sh
```

Expected: `CI Compose host-port contract: PASS`.

- [ ] **Step 6: Commit the workflow change**

```bash
git add .github/workflows/verify.yml
git commit -m "fix(ci): use runner-accessible Compose network"
```

---

### Task 4: Complete verification and publish

**Files:**
- Verify: `.github/workflows/verify.yml`
- Verify: `deploy/compose.dev.yml`
- Verify: `deploy/compose.ci.yml`
- Verify: `scripts/ci-compose_contract_test.sh`
- Verify: `Makefile`

**Interfaces:**
- Consumes: all changes from Tasks 1-3.
- Produces: a validated `master` push that triggers GitHub Actions.

- [ ] **Step 1: Validate shell and workflow syntax**

Run:

```bash
bash -n scripts/ci-compose_contract_test.sh
actionlint .github/workflows/verify.yml
git diff --check
```

Expected: all commands exit 0 with no diagnostics.

- [ ] **Step 2: Run the complete contract suite**

Run:

```bash
pnpm e2e-contracts
```

Expected: all Phase 2, Phase 3, Phase 4, harness semantics, artifact sanitization, and CI Compose contracts print `PASS`.

- [ ] **Step 3: Verify the final repository state**

Run:

```bash
git status --short --branch
git log -4 --oneline --decorate
```

Expected: only intentional commits are ahead of `origin/master`, with no uncommitted files.

- [ ] **Step 4: Push and trigger remote verification**

Run:

```bash
git push origin master
```

Expected: `master -> master`, triggering the `verify` workflow.
