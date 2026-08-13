# Local Stack All-Interface Bindings Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bind every local/test host-published service port to `0.0.0.0`, require the exact browser public origin, and publish the verified result as immutable Release `v0.1.3`.

**Architecture:** Docker Compose remains the source of truth for local/test host mappings. The GitHub deployment script accepts one explicit `--public-origin` value and persists it beside the existing port settings, while contracts parse rendered Compose JSON and exercise missing-origin failure behavior. Production Compose is not modified.

**Tech Stack:** Bash, Docker Compose v5, jq, Git, GitHub Actions, Go application configuration, Markdown.

## Global Constraints

- Local/test Web/API, internal App, PostgreSQL, Redis, AIStor S3, and AIStor console mappings bind to `0.0.0.0`.
- Production continues to publish only Caddy ports 80 and 443; production service networking is unchanged.
- `--public-origin` is mandatory and identifies the exact browser origin, including a non-default App port.
- The update-agent HTTP listener remains unpublished.
- Documentation requires a trusted LAN or host firewall because development services use plain HTTP and development credentials.
- Do not add the redundant `happylearn-public` network; GitHub deployments retain `host_access`.
- Do not create `v0.1.3` until the exact final `master` commit passes the complete Verify workflow.
- Release assets are exactly `VAYZRA-v0.1.3.tar.gz` and `SHA256SUMS`, with `immutable=true`.

---

### Task 1: Lock the six all-interface Compose mappings

**Files:**
- Modify: `scripts/ci-compose_contract_test.sh:149-181`
- Modify: `deploy/compose.dev.yml:45-52,65-72,123-132,203-263,454-460`
- Modify: `deploy/compose.github.yml:5-16,88-110`

**Interfaces:**
- Consumes: `HAPPYLEARN_APP_PORT`, `HAPPYLEARN_INTERNAL_PORT`, `HAPPYLEARN_POSTGRES_PORT`, `HAPPYLEARN_REDIS_PORT`, `HAPPYLEARN_AISTOR_API_PORT`, and `HAPPYLEARN_AISTOR_CONSOLE_PORT`.
- Produces: rendered Compose with six host ports on `0.0.0.0` and no update-agent host port.

- [ ] **Step 1: Write the failing rendered-Compose contract**

Add `assert_all_interface_ports()` to `scripts/ci-compose_contract_test.sh`. It must extract and sort `{service,host_ip,published,target}` records and compare them with:

```json
[
  {"service":"app","host_ip":"0.0.0.0","published":"8080","target":8080},
  {"service":"app","host_ip":"0.0.0.0","published":"9090","target":9090},
  {"service":"minio","host_ip":"0.0.0.0","published":"59000","target":9000},
  {"service":"minio","host_ip":"0.0.0.0","published":"59001","target":9001},
  {"service":"postgres","host_ip":"0.0.0.0","published":"54329","target":5432},
  {"service":"redis","host_ip":"0.0.0.0","published":"56379","target":6379}
]
```

Call it for `base_json` and `merged_json`. Mutate the App host IP to `127.0.0.1` in a copied JSON fixture and require rejection. Extend the OTA contract to require update-agent `ports` is absent or empty.

- [ ] **Step 2: Run the contract and verify RED**

Run `bash scripts/ci-compose_contract_test.sh`.

Expected: FAIL because several mappings still render with `127.0.0.1` and the prior contract requires loopback.

- [ ] **Step 3: Implement the minimum Compose changes**

Use the six existing port variables in `deploy/compose.dev.yml` and bind all mappings to `0.0.0.0`. Keep `HAPPYLEARN_PUBLIC_ORIGIN` variable-driven. Remove the App's `happylearn-public` membership and that network definition. In `deploy/compose.github.yml`, bind all six mappings to `0.0.0.0`, preserve `happylearn` plus `host_access`, and leave update-agent unpublished.

- [ ] **Step 4: Run focused GREEN verification**

```bash
bash scripts/ci-compose_contract_test.sh
bash scripts/github-ota_contract_test.sh
```

Expected: both commands print PASS and exit 0.

- [ ] **Step 5: Commit the Compose slice**

```bash
git add scripts/ci-compose_contract_test.sh deploy/compose.dev.yml deploy/compose.github.yml
git commit -m "feat: expose local stack ports"
```

---

### Task 2: Require and persist the browser public origin

**Files:**
- Modify: `scripts/github-ota_contract_test.sh:420-510`
- Modify: `scripts/deploy-from-github.sh:20-145,160-200,264-299,332-339`
- Modify: `README.md:5,17-40,102-145,203-277,320-325`

**Interfaces:**
- Consumes: `--public-origin URL` and existing `--app-port PORT`.
- Produces: `.env.github-deploy` entry `HAPPYLEARN_PUBLIC_ORIGIN=<URL>` and success output using that URL.

- [ ] **Step 1: Write failing deployment-script contracts**

Require the option in usage/parsing, the persistence statement below, and the exact missing-value error:

```bash
printf 'HAPPYLEARN_PUBLIC_ORIGIN=%s\n' "$public_origin"
```

```text
deploy-from-github: --public-origin is required
```

Invoke the script without the option and require nonzero before Docker or deployment mutations. Add a source mutation that removes required-origin validation and require contract failure.

- [ ] **Step 2: Run the contract and verify RED**

Run `bash scripts/github-ota_contract_test.sh`.

Expected: FAIL because the deployment script does not recognize or require `--public-origin`.

- [ ] **Step 3: Implement the public-origin CLI**

Add `public_origin=''`, parse `--public-origin`, and validate immediately after parsing:

```bash
[[ -n $public_origin ]] || fail '--public-origin is required'
[[ $public_origin != *$'\n'* && $public_origin != *$'\r'* && $public_origin != *[[:space:]]* ]] ||
  fail '--public-origin is invalid'
case $public_origin in
  http://*|https://*) ;;
  *) fail '--public-origin is invalid' ;;
esac
```

Persist the exact value and print `web=$public_origin` after readiness. Keep host-local readiness on `127.0.0.1`.

- [ ] **Step 4: Update README**

Add `--public-origin http://192.168.1.20:8080` to every deployment example, document all six all-interface mappings and the firewall/trusted-LAN requirement, preserve the production exclusion, and change the current source Release line to `v0.1.3` in the final release commit.

- [ ] **Step 5: Run focused GREEN verification**

```bash
bash -n scripts/deploy-from-github.sh scripts/github-ota_contract_test.sh
shellcheck scripts/deploy-from-github.sh scripts/github-ota_contract_test.sh
bash scripts/github-ota_contract_test.sh
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 6: Commit deployment and documentation**

```bash
git add scripts/deploy-from-github.sh scripts/github-ota_contract_test.sh README.md
git commit -m "docs: configure local network origin"
```

---

### Task 3: Verify and independently review the candidate

**Files:**
- Verify: `deploy/compose.dev.yml`
- Verify: `deploy/compose.github.yml`
- Verify: `scripts/deploy-from-github.sh`
- Verify: `scripts/ci-compose_contract_test.sh`
- Verify: `scripts/github-ota_contract_test.sh`
- Verify: `README.md`

**Interfaces:**
- Consumes: Tasks 1 and 2 commits.
- Produces: clean `master` candidate with matching implementation, contracts, and docs.

- [ ] **Step 1: Render default and custom Compose configurations**

Run both default Compose files with `config --quiet`. Then render custom ports `18080`, `19090`, `55429`, `56380`, `59002`, and `59003` with `HAPPYLEARN_PUBLIC_ORIGIN=http://192.168.1.20:18080`. Use jq to require every host IP is `0.0.0.0`, the App origin is exact, and update-agent has no ports.

- [ ] **Step 2: Run the complete relevant suite**

```bash
bash scripts/ci-compose_contract_test.sh
bash scripts/github-ota_contract_test.sh
corepack pnpm e2e-contracts
git diff --check
git status --short
```

Expected: tests exit 0 and tracked worktree is clean after commits.

- [ ] **Step 3: Request independent review**

Review bind scope, Origin/CSRF consistency, update-agent non-exposure, production isolation, documentation accuracy, and mutation strength. Resolve every Critical or Important finding through a new RED/GREEN cycle.

---

### Task 4: Synchronize verified master

**Files:**
- No source changes.

**Interfaces:**
- Consumes: clean candidate from Task 3.
- Produces: `origin/master` at the exact local commit.

- [ ] **Step 1: Check local and remote preconditions**

Require clean status, branch `master`, and authenticated remote `master` still at base `68a9f20358da1793f5bc0b72f5fdff6ad477a845`. Stop on unexpected advancement.

- [ ] **Step 2: Push over authenticated HTTPS**

Use Git Credential Manager without printing a token:

```bash
git push https://github.com/lane-cv/VAYZRA.git master:master
```

Verify GitHub API `branches/master` equals local HEAD.

- [ ] **Step 3: Monitor exact-SHA Verify**

Wait for the `verify.yml` push run for the pushed SHA. Require the main verify, Phase 2, Phase 3, and Phase 5 jobs to succeed. On failure, collect logs, fix through TDD, push a new SHA, and repeat; never tag a failed or superseded commit.

---

### Task 5: Publish and verify immutable v0.1.3

**Files:**
- No source changes.

**Interfaces:**
- Consumes: clean synchronized fully verified `master` SHA.
- Produces: annotated `v0.1.3` and immutable Release with two assets.

- [ ] **Step 1: Check release preconditions**

Require clean status, local HEAD equals remote master, no local/remote `v0.1.3` tag, no `v0.1.3` Release, immutable releases enabled, and exact-HEAD Verify successful.

- [ ] **Step 2: Create and push the annotated tag**

```bash
git tag -a v0.1.3 -m "VAYZRA v0.1.3" HEAD
test "$(git cat-file -t v0.1.3)" = tag
test "$(git rev-parse 'v0.1.3^{commit}')" = "$(git rev-parse HEAD)"
git push https://github.com/lane-cv/VAYZRA.git refs/tags/v0.1.3
```

Never force, move, delete, or recreate the pushed tag.

- [ ] **Step 3: Monitor the Release workflow**

Require `github-release` validate and publish jobs to succeed. If a draft exists after failure, safely resume that same Release; never create a duplicate or alter the tag.

- [ ] **Step 4: Verify published assets independently**

Require `tag_name=v0.1.3`, `draft=false`, `prerelease=false`, `immutable=true`, and exactly `SHA256SUMS` plus `VAYZRA-v0.1.3.tar.gz`. Download both into a new temporary directory, verify GitHub digests and `SHA256SUMS`, require every archive entry starts with `VAYZRA-v0.1.3/` with no absolute or `..` traversal path, and confirm the remote annotated tag directly targets the verified master commit.
