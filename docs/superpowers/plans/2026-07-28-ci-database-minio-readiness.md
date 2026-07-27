# CI Database Serialization and MinIO Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `verify` workflow wait for authenticated MinIO readiness and prevent PostgreSQL-backed Go packages from timing out in a cross-package advisory-lock queue.

**Architecture:** Keep application code unchanged. Encode the desired CI behavior in the existing shell and Go deployment-contract tests, then minimally change the workflow and Compose health check to satisfy those contracts. Serialize Go package binaries with `-p 1`, explicitly clear inherited `GOFLAGS` for both repository test commands, use the AIStor readiness endpoint, and add a bounded authenticated S3 probe plus sanitized failure evidence.

**Tech Stack:** GitHub Actions YAML, Docker Compose, Bash contract tests, Go 1.26 deployment-contract tests, MinIO Client (`mc`) included in the pinned AIStor image.

---

## File Structure

- Modify `.github/workflows/verify.yml`: serialize Go package tests, add the authenticated MinIO probe, and emit bounded dependency failure evidence.
- Modify `deploy/compose.dev.yml`: change the MinIO health check from liveness to readiness.
- Modify `scripts/ci-compose_contract_test.sh`: enforce workflow ordering, `-p 1`, bounded authenticated readiness, and sanitized diagnostics.
- Modify `internal/platform/objectstore/deployment_test.go`: lock the Compose MinIO readiness endpoint.

### Task 1: Add failing CI scheduling and readiness contracts

**Files:**
- Modify: `scripts/ci-compose_contract_test.sh`
- Modify: `internal/platform/objectstore/deployment_test.go`
- Test: `scripts/ci-compose_contract_test.sh`
- Test: `internal/platform/objectstore/deployment_test.go`

- [ ] **Step 1: Require serialized Go package tests in the workflow contract**

After the existing host-port probe assertions in `scripts/ci-compose_contract_test.sh`, add:

```bash
go_test_line="$(exact_line "      - run: GOFLAGS='' go test -p 1 ./... -count=1")"
go_race_line="$(exact_line "      - run: GOFLAGS='' go test -race -p 1 ./... -count=1")"
test "$go_test_line" -gt "$probe_done_line" ||
  fail "ordinary Go tests must run after dependency verification"
test "$go_race_line" -gt "$go_test_line" ||
  fail "race-enabled Go tests must run after ordinary Go tests"
! grep -Eq '^[[:space:]]+- run: go test( -race)? \./\.\.\.' "$workflow" ||
  fail "repository Go package tests must use -p 1"
```

- [ ] **Step 2: Require an authenticated MinIO readiness step**

Add exact-line and ordering assertions:

```bash
minio_probe_name_line="$(exact_line '      - name: Verify authenticated MinIO readiness')"
minio_probe_run_line="$((minio_probe_name_line + 1))"
minio_probe_timeout_line="$((minio_probe_name_line + 2))"

test "$minio_probe_name_line" -eq "$((probe_done_line + 1))" ||
  fail "authenticated MinIO readiness must immediately follow host-port verification"
line_is "$minio_probe_run_line" '        run: |'
line_is "$minio_probe_timeout_line" \
  "          timeout 30 docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml exec -T minio /bin/sh -ceu 'until mc alias set local http://127.0.0.1:9000 \"\$MINIO_ROOT_USER\" \"\$MINIO_ROOT_PASSWORD\" >/dev/null 2>&1 && mc ls local >/dev/null 2>&1; do sleep 1; done' || { echo 'MinIO did not accept an authenticated ListBuckets request within 30s.' >&2; exit 1; }"
test "$go_test_line" -gt "$minio_probe_timeout_line" ||
  fail "Go tests must run after authenticated MinIO readiness"
```

- [ ] **Step 3: Require bounded, sanitized failure evidence before cleanup**

Add:

```bash
report_name_line="$(exact_line '      - name: Report integration dependency failure')"
report_if_line="$((report_name_line + 1))"
report_run_line="$((report_name_line + 2))"
report_ps_line="$((report_name_line + 3))"
report_logs_line="$((report_name_line + 4))"

line_is "$report_if_line" '        if: failure()'
line_is "$report_run_line" '        run: |'
line_is "$report_ps_line" \
  '          docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml ps'
line_is "$report_logs_line" \
  '          docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml logs --no-color --tail 100 minio'
test "$report_name_line" -lt "$cleanup_name_line" ||
  fail "dependency failure evidence must precede cleanup"
! sed -n "${report_name_line},${report_logs_line}p" "$workflow" |
  grep -Eq '(printenv|docker inspect|cat .*license|MINIO_ROOT_(USER|PASSWORD))' ||
  fail "dependency failure evidence must not expose secrets"
```

- [ ] **Step 4: Require the MinIO readiness endpoint in the Go deployment contract**

In `internal/platform/objectstore/deployment_test.go`, replace the required health-check string with:

```go
`test: ["CMD", "curl", "--fail", "--silent", "http://127.0.0.1:9000/minio/health/ready"]`,
```

Also add this rejection next to the existing exposure checks:

```go
if strings.Contains(server, "/minio/health/live") {
	t.Fatal("MinIO service uses liveness instead of readiness for dependency gating")
}
```

- [ ] **Step 5: Run the shell contract and verify RED**

Run:

```bash
HAPPYLEARN_AISTOR_LICENSE_FILE=/Users/lane/Downloads/minio.license bash scripts/ci-compose_contract_test.sh
```

Expected: FAIL with `missing verify-job workflow line:       - run: GOFLAGS='' go test -p 1 ./... -count=1`.

- [ ] **Step 6: Run the Go deployment contract and verify RED**

Run:

```bash
docker run --rm \
  -v "$PWD:/workspace" \
  -w /workspace \
  golang:1.26.5 \
  go test ./internal/platform/objectstore -run '^TestMinIODeploymentSecurityContract$' -count=1
```

Expected: FAIL because `deploy/compose.dev.yml` still contains `/minio/health/live`.

- [ ] **Step 7: Commit the failing contracts**

```bash
git add scripts/ci-compose_contract_test.sh internal/platform/objectstore/deployment_test.go
git commit -m "test(ci): require serialized database tests and MinIO readiness"
```

### Task 2: Implement the minimal workflow and Compose changes

**Files:**
- Modify: `.github/workflows/verify.yml`
- Modify: `deploy/compose.dev.yml`
- Test: `scripts/ci-compose_contract_test.sh`
- Test: `internal/platform/objectstore/deployment_test.go`

- [ ] **Step 1: Gate Compose on MinIO readiness**

In `deploy/compose.dev.yml`, change only the MinIO health-check URL:

```yaml
healthcheck:
  test: ["CMD", "curl", "--fail", "--silent", "http://127.0.0.1:9000/minio/health/ready"]
```

- [ ] **Step 2: Add the authenticated readiness probe**

Immediately after `Verify host integration ports` in `.github/workflows/verify.yml`, add:

```yaml
      - name: Verify authenticated MinIO readiness
        run: |
          timeout 30 docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml exec -T minio /bin/sh -ceu 'until mc alias set local http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1 && mc ls local >/dev/null 2>&1; do sleep 1; done' || { echo 'MinIO did not accept an authenticated ListBuckets request within 30s.' >&2; exit 1; }
```

- [ ] **Step 3: Serialize both repository Go test commands**

Replace the two workflow commands with:

```yaml
      - run: GOFLAGS='' go test -p 1 ./... -count=1
      - run: GOFLAGS='' go test -race -p 1 ./... -count=1
```

- [ ] **Step 4: Add sanitized failure evidence**

Immediately before `Stop integration dependencies`, add:

```yaml
      - name: Report integration dependency failure
        if: failure()
        run: |
          docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml ps
          docker compose -p happylearn-ci -f deploy/compose.dev.yml -f deploy/compose.ci.yml logs --no-color --tail 100 minio
```

- [ ] **Step 5: Run focused contracts and verify GREEN**

Run:

```bash
HAPPYLEARN_AISTOR_LICENSE_FILE=/Users/lane/Downloads/minio.license bash scripts/ci-compose_contract_test.sh
```

Expected: `CI Compose host-port contract: PASS`.

Run:

```bash
docker run --rm \
  -v "$PWD:/workspace" \
  -w /workspace \
  golang:1.26.5 \
  go test ./internal/platform/objectstore -run '^TestMinIODeploymentSecurityContract$' -count=1
```

Expected: `ok happylearn.local/app/internal/platform/objectstore`.

- [ ] **Step 6: Validate both Compose configurations**

Run:

```bash
HAPPYLEARN_AISTOR_LICENSE_FILE=/Users/lane/Downloads/minio.license \
  docker compose -f deploy/compose.dev.yml config --quiet
HAPPYLEARN_AISTOR_LICENSE_FILE=/Users/lane/Downloads/minio.license \
  docker compose -f deploy/compose.dev.yml -f deploy/compose.ci.yml config --quiet
```

Expected: both commands exit 0 with no output.

- [ ] **Step 7: Commit the minimal implementation**

```bash
git add .github/workflows/verify.yml deploy/compose.dev.yml
git commit -m "fix(ci): serialize database tests and await MinIO readiness"
```

### Task 3: Verify the real dependency behavior

**Files:**
- No source changes.
- Test: `internal/aiqa/postgres_runtime_test.go`
- Test: `tests/integration/files_test.go`

- [ ] **Step 1: Start isolated PostgreSQL and MinIO services**

Use a unique Compose project and alternate host ports through a temporary override:

```yaml
services:
  postgres:
    ports: !override
      - "127.0.0.1:54330:5432"
  minio:
    ports: !override
      - "127.0.0.1:59010:9000"
      - "127.0.0.1:59011:9001"
```

Create the override under `/private/tmp/vayzra-ci-readiness-override.yml`, then run:

```bash
HAPPYLEARN_AISTOR_LICENSE_FILE=/Users/lane/Downloads/minio.license \
  docker compose -p happylearn-ci-readiness \
  -f deploy/compose.dev.yml \
  -f deploy/compose.ci.yml \
  -f /private/tmp/vayzra-ci-readiness-override.yml \
  up -d --wait --wait-timeout 120 postgres minio
```

Expected: both services report healthy.

- [ ] **Step 2: Verify authenticated MinIO readiness with CI's retry and sanitized timeout behavior**

Run:

```bash
timeout 30 docker compose -p happylearn-ci-readiness \
  -f deploy/compose.dev.yml \
  -f deploy/compose.ci.yml \
  -f /private/tmp/vayzra-ci-readiness-override.yml \
  exec -T minio /bin/sh -ceu \
  'until mc alias set local http://127.0.0.1:9000 "$MINIO_ROOT_USER" "$MINIO_ROOT_PASSWORD" >/dev/null 2>&1 && mc ls local >/dev/null 2>&1; do sleep 1; done' || \
  { echo 'MinIO did not accept an authenticated ListBuckets request within 30s.' >&2; exit 1; }
```

Expected: exit 0 without credential or license output.

- [ ] **Step 3: Run the AIQA lock-order regression**

Run:

```bash
docker run --rm --add-host=host.docker.internal:host-gateway \
  -e 'HAPPYLEARN_TEST_DATABASE_URL=postgres://happylearn:happylearn_dev@host.docker.internal:54330/happylearn?sslmode=disable' \
  -v "$PWD:/workspace" \
  -w /workspace \
  golang:1.26.5 \
  go test ./internal/aiqa \
  -run '^TestPostgresRuntimeAnomalySettlementBlocksCrossStudentAdmission$' \
  -count=1 -timeout=30s
```

Expected: `ok happylearn.local/app/internal/aiqa`.

- [ ] **Step 4: Run the MinIO integration test**

Run:

```bash
docker run --rm --add-host=host.docker.internal:host-gateway \
  -e HAPPYLEARN_TEST_MINIO_ENDPOINT=host.docker.internal:59010 \
  -v "$PWD:/workspace" \
  -w /workspace \
  golang:1.26.5 \
  go test ./tests/integration \
  -run '^TestMinIOObjectStoreMultipartRangeAndAbort$' \
  -count=1 -timeout=60s
```

Expected: `ok happylearn.local/app/tests/integration`.

- [ ] **Step 5: Clean up only the isolated project**

Run:

```bash
HAPPYLEARN_AISTOR_LICENSE_FILE=/Users/lane/Downloads/minio.license \
  docker compose -p happylearn-ci-readiness \
  -f deploy/compose.dev.yml \
  -f deploy/compose.ci.yml \
  -f /private/tmp/vayzra-ci-readiness-override.yml \
  down --volumes --remove-orphans
```

Expected: only `happylearn-ci-readiness` containers, network, and volume are removed.

### Task 4: Final verification, push, and Actions confirmation

**Files:**
- No additional source changes.

- [ ] **Step 1: Run the complete CI contract suite**

Run:

```bash
HAPPYLEARN_AISTOR_LICENSE_FILE=/Users/lane/Downloads/minio.license make e2e-contracts
```

Expected: every contract prints `PASS` and Make exits 0.

- [ ] **Step 2: Check the final repository state**

Run:

```bash
git diff --check
git status --short --branch
git log -4 --oneline --decorate
```

Expected: no whitespace errors; only intended commits are ahead of `origin/master`.

- [ ] **Step 3: Push `master`**

Run:

```bash
git push origin master
```

Expected: the design, contract, and implementation commits are pushed and a new `verify` workflow run starts.

- [ ] **Step 4: Confirm the new workflow run**

Open the repository's `verify` workflow page and confirm that the newest run references the implementation commit and enters `In progress`.

- [ ] **Step 5: Monitor the critical gates**

Confirm that the run passes:

- `Verify authenticated MinIO readiness`;
- `Run GOFLAGS='' go test -p 1 ./... -count=1`;
- `Run GOFLAGS='' go test -race -p 1 ./... -count=1`.

If a gate fails, read its full log and return to systematic root-cause investigation rather than adding another speculative fix.
