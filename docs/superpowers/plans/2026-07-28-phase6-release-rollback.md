# Phase 6 Release, Rollback, and Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement an idempotent maintenance-window release protocol with hash-verified configuration evidence, verified pre-release recovery, forward-only migration, safe previous-image rollback, interruption recovery, and separately guarded destructive restore.

**Architecture:** A typed Go release-manifest validator owns compatibility and hash rules; small one-shot Go commands own database-aware operations. Host Bash scripts coordinate Compose through a durable owner-only state file and exclusive lock. Every mutation follows a proven precondition, while automatic rollback changes images only and destructive restore always targets new volumes.

**Tech Stack:** Go, Bash, jq, Docker Compose, PostgreSQL advisory locks, systemd, Restic, Age.

---

## File structure

- Create `internal/release/manifest.go`, `manifest_test.go`.
- Create `internal/release/state.go`, `state_test.go`.
- Create `cmd/release-manifest/main.go`, `main_test.go`.
- Create `cmd/migrate/main.go`, `main_test.go`.
- Create `cmd/acceptance/main.go`, `main_test.go`.
- Create `scripts/prod-common.sh`, `prod-preflight.sh`,
  `prod-release.sh`, `prod-rollback.sh`, `prod-restore.sh`.
- Create matching `scripts/*_contract_test.sh`.
- Create `scripts/phase6-release_failure_matrix.sh`,
  `phase6-release_failure_matrix_contract_test.sh`.
- Create `deploy/systemd/happylearn-compose.service`,
  `happylearn-host-sample.service`, `happylearn-host-sample.timer`,
  `happylearn-backup-dispatch.service`, `happylearn-backup-dispatch.timer`,
  `happylearn-backup-retry.service`, `happylearn-backup-retry.timer`,
  `happylearn-backup-retention.service`, `happylearn-backup-retention.timer`,
  `happylearn-restore-verify.service`, `happylearn-restore-verify.timer`.
- Create `scripts/render-systemd.sh`,
  `scripts/phase6-systemd_contract_test.sh`.
- Create `docs/runbooks/phase6-release-rollback.md`,
  `docs/runbooks/phase6-disaster-restore.md`.
- Modify `Dockerfile`, `Dockerfile.worker`, `Makefile`, `package.json`.

### Task 1: Define and validate immutable release manifests

- [ ] **Step 1: Write failing manifest tests**

Create `internal/release/manifest_test.go` for:

```go
func TestManifestAcceptsCompleteImmutableRelease(t *testing.T)
func TestManifestRejectsInvalidSemanticVersion(t *testing.T)
func TestManifestRejectsNonDigestImage(t *testing.T)
func TestManifestRejectsInvalidSchemaInterval(t *testing.T)
func TestManifestRejectsMissingConfigurationHash(t *testing.T)
func TestManifestRejectsSecretPathOrValue(t *testing.T)
func TestManifestRejectsUnknownField(t *testing.T)
func TestManifestCanonicalJSONIsStable(t *testing.T)
func TestPreviousImagesCompatibleWithMigratedSchema(t *testing.T)
```

Use:

```go
type Manifest struct {
    Version          string            `json:"version"`
    Commit           string            `json:"commit"`
    BuiltAt          time.Time         `json:"builtAt"`
    Images           map[string]string `json:"images"`
    MinSchemaVersion int64             `json:"minSchemaVersion"`
    MaxSchemaVersion int64             `json:"maxSchemaVersion"`
    ComposeSHA256    string            `json:"composeSha256"`
    CaddySHA256      string            `json:"caddySha256"`
    BackupEvidenceID string            `json:"backupEvidenceId"`
    CreatedBy        string            `json:"createdBy"`
    CreatedAt        time.Time         `json:"createdAt"`
}
```

Required image keys are `app`, `worker`, `migrate`, `backup`, `caddy`,
`postgres`, `redis`, and `minio`. Every value matches
`^[^[:space:]@]+@sha256:[0-9a-f]{64}$`.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/release
```

Expected: FAIL because the package is absent.

- [ ] **Step 3: Implement strict parsing and canonical output**

Decode with `DisallowUnknownFields`, require EOF after one object, validate all
fields, and serialize a stable key order for hashing. Reject strings containing
line breaks, URI user info, assignments that look like credentials, absolute
secret paths, or unsupported image keys. Errors identify the field and rule,
not the rejected value.

- [ ] **Step 4: Implement the manifest command**

`cmd/release-manifest` supports:

```text
validate --file <absolute manifest>
verify-config --file <absolute manifest> --compose <absolute file> --caddy <absolute file>
compatible --file <absolute manifest> --schema-version <integer>
```

It outputs one JSON result with safe categories. It never prints the manifest
body or environment.

- [ ] **Step 5: Verify and commit**

```bash
go test ./internal/release ./cmd/release-manifest
git add internal/release cmd/release-manifest
git commit -m "feat(phase6): validate immutable release manifests"
```

### Task 2: Add locked forward migration and internal acceptance commands

- [ ] **Step 1: Write failing migration tests**

Create `cmd/migrate/main_test.go` for:

```go
func TestMigrateRejectsIncompatibleStartingSchema(t *testing.T)
func TestMigrateUsesDedicatedAdvisoryLock(t *testing.T)
func TestMigrateAppliesPendingVersionsOnce(t *testing.T)
func TestMigrateReportsCurrentSchemaWithoutDSN(t *testing.T)
func TestMigrateLeavesFailedTransactionalDDLUncommitted(t *testing.T)
```

Use advisory-lock key `845103121`, distinct from integration and Phase 5
operational locks. The command accepts the manifest through a required
read-only file and the database URL through its supported secret file.

- [ ] **Step 2: Write failing acceptance tests**

`cmd/acceptance/main_test.go` must require bounded checks for:

```text
schema version and build compatibility
login challenge availability without authentication
one constant database read
one namespaced Redis set/get/delete
one authenticated AIStor bucket list
one static asset response
private metrics authorization
public /internal/metrics denial
```

Each check emits name, status, duration, and trace ID only. No credential,
database content, Redis content, object key, response body, query string, or
network endpoint is printed.

- [ ] **Step 3: Run and verify RED**

```bash
go test ./cmd/migrate ./cmd/acceptance
```

Expected: FAIL because the commands are absent.

- [ ] **Step 4: Implement the commands**

Reuse the repository migration runner and existing safe HTTP utilities. Use
explicit per-check timeouts and a total acceptance deadline. Migration validates
the starting schema against the new manifest before acquiring the lock, applies
only forward migrations, then validates the resulting schema again.

- [ ] **Step 5: Add production image targets**

Extend the Dockerfiles or add build stages named `server`, `worker`, `migrate`,
`backup`, and `acceptance`. All runtime stages use non-root users, read-only
compatible paths, pinned build inputs, and linker-provided build metadata.

- [ ] **Step 6: Verify and commit**

```bash
go test ./cmd/migrate ./cmd/acceptance ./internal/release
docker build --target migrate -t happylearn-migrate:phase6 .
docker build --target acceptance -t happylearn-acceptance:phase6 .
git add cmd/migrate cmd/acceptance Dockerfile Dockerfile.worker
git commit -m "feat(phase6): add migration and acceptance services"
```

### Task 3: Build the safe host command library and preflight

- [ ] **Step 1: Write failing common-library and preflight contracts**

Create `scripts/prod-common_contract_test.sh` and
`scripts/prod-preflight_contract_test.sh`. Require:

- Bash strict mode and a restrictive `umask`;
- exact project name `happylearn-prod`;
- canonical absolute project, environment, secret, backup, and state paths;
- refusal of unresolved, root, home, wildcard, symlink, and non-owned targets;
- exclusive nonblocking `flock`;
- bounded Docker calls and health waits;
- atomic state writes using same-directory temporary files plus rename;
- owner-only state, lock, and evidence files;
- redacted logging and first-failure preservation;
- read-only preflight with no `up`, `down`, `start`, `stop`, migration, backup,
  restore, package-manager, firewall, or file-install action.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/prod-common_contract_test.sh
bash scripts/prod-preflight_contract_test.sh
```

Expected: FAIL because the scripts are absent.

- [ ] **Step 3: Implement `prod-common.sh`**

Provide narrowly scoped functions for path validation, file mode checks,
release locking, state transitions, Compose invocation, bounded waits,
sanitized evidence, cleanup traps, and JSON status output. Source it only after
validating its absolute repository path.

- [ ] **Step 4: Implement read-only `prod-preflight.sh`**

The command accepts:

```text
--project-dir
--env-file
--manifest
--mode local|server
--expected-host-address <address>   # required only for server mode
```

Validate the approved host, file ownership, 2 CPU, 4 GiB, disk thresholds,
timezone, ports, secret files, Compose, Caddy, digests, pull availability,
manifest hashes, previous release presence, and a verified recovery point no
older than 24 hours. Local mode skips Ubuntu, DNS, and public-certificate
claims but validates every repository-owned invariant.

- [ ] **Step 5: Add mutation tests**

Mutate one fixture at a time and require fail-closed behavior for an unsafe
secret mode, symlink secret, small disk fixture, floating image, hash mismatch,
missing recovery evidence, occupied public port, wrong DNS answer, malformed
Compose, and unavailable previous manifest.

- [ ] **Step 6: Verify and commit**

```bash
bash scripts/prod-common_contract_test.sh
bash scripts/prod-preflight_contract_test.sh
shellcheck scripts/prod-common.sh scripts/prod-preflight.sh
git add scripts/prod-common.sh scripts/prod-preflight.sh \
  scripts/prod-common_contract_test.sh scripts/prod-preflight_contract_test.sh
git commit -m "feat(phase6): add production preflight"
```

### Task 4: Implement the durable release state machine

- [ ] **Step 1: Write failing release contracts**

Create `scripts/prod-release_contract_test.sh`. Require the exact ordered
states:

```text
preflight
backup_started
backup_verified
release_mode
maintenance
drained
images_pulled
schema_compatible
migrated
services_started
ready
smoke_passed
activated
normal
traffic_open
succeeded
```

The durable JSON state contains release ID, manifest hash, previous manifest
hash, state, attempt, safe timestamps, backup evidence ID, trace ID, and
result. It contains no environment, secret path, Docker inspect result, request
body, or log body.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/prod-release_contract_test.sh
```

Expected: FAIL because `prod-release.sh` is absent.

- [ ] **Step 3: Implement release arguments and locking**

Require:

```text
--project-dir <absolute path>
--env-file <absolute path>
--manifest <absolute path>
--version <semantic version matching manifest>
--mode local|server
--confirm-maintenance-window
```

Reject interactive guessing, missing confirmation, concurrent release locks,
dirty or mismatched configuration hashes, and an already-active manifest.

- [ ] **Step 4: Implement each durable transition**

For every state:

1. prove its precondition;
2. perform one bounded operation;
3. verify the postcondition;
4. atomically persist the next state;
5. emit sanitized evidence.

Use Phase 5 APIs/commands to create and verify `pre_release` backup evidence,
enter `release` mode, stop new writes and worker claims, and drain for no more
than ten minutes. Reload `Caddyfile.maintenance`, stop the worker, pull only
manifest digests, validate schema compatibility, run `migrate`, start new app
and worker privately, run readiness and acceptance, atomically promote active
and previous manifests, return `normal`, then reload normal Caddy.

- [ ] **Step 5: Make interruption behavior idempotent**

Re-running the identical manifest resumes only when the persisted postcondition
can be reproved. A manifest mismatch or ambiguous external state leaves
maintenance enabled and ends in `failed_safe`. Signals persist the current
state, preserve the first failure, and never reopen traffic in a trap.

- [ ] **Step 6: Verify and commit**

```bash
bash scripts/prod-release_contract_test.sh
shellcheck scripts/prod-release.sh
git add scripts/prod-release.sh scripts/prod-release_contract_test.sh
git commit -m "feat(phase6): implement maintenance release protocol"
```

### Task 5: Implement automatic image rollback

- [ ] **Step 1: Write failing rollback contracts**

Create `scripts/prod-rollback_contract_test.sh`. Automatic rollback is allowed
only after migration starts and before public traffic reopens. Require states:

```text
rollback_diagnostics
rollback_compatibility
rollback_stopped
rollback_started
rollback_ready
rollback_smoke_passed
rolled_back
normal
traffic_open
```

Reject a previous manifest that is missing, hash-invalid, or incompatible with
the current schema. Reject any down migration, database restore, row/object
deletion, DNS change, or failed-image deletion.

Require `--mode local|server`; server mode rejects every failure-injection and
local-issuer variable.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/prod-rollback_contract_test.sh
```

Expected: FAIL because `prod-rollback.sh` is absent.

- [ ] **Step 3: Implement rollback**

Keep maintenance active, collect bounded sanitized status and log tails, stop
the failed app/worker, validate the previous manifest against the live schema,
start previous digests, run the same readiness and acceptance commands, mark
the failed and active manifests, return operations to normal, then reopen
traffic.

If compatibility, readiness, or smoke fails, persist `failed_safe`, leave
maintenance active, and print only the trace ID plus the absolute runbook path.

- [ ] **Step 4: Connect release failures**

`prod-release.sh` invokes rollback only within the allowed state interval. A
pre-migration failure returns safely without rollback; a post-traffic failure
requires a new operator release. Preserve the original release failure even if
rollback also fails.

- [ ] **Step 5: Verify and commit**

```bash
bash scripts/prod-release_contract_test.sh
bash scripts/prod-rollback_contract_test.sh
shellcheck scripts/prod-release.sh scripts/prod-rollback.sh
git add scripts/prod-release.sh scripts/prod-rollback.sh \
  scripts/prod-rollback_contract_test.sh
git commit -m "feat(phase6): add schema-safe image rollback"
```

### Task 6: Implement guarded destructive restore

- [ ] **Step 1: Write failing restore contracts**

Create `scripts/prod-restore_contract_test.sh`. Require:

```text
--mode local|server
--target-project <validated non-production name>
--backup-id <UUID>
--destructive
--confirmation "<target-project>:<backup-id>"
```

Reject:

- target `happylearn-prod`;
- active public traffic;
- non-maintenance operational mode;
- missing second current recovery point;
- non-empty replacement volumes;
- repository check failure;
- wrong typed confirmation;
- unresolved paths, globs, symlinks, and volumes outside the exact target
  Compose project.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/prod-restore_contract_test.sh
```

Expected: FAIL because `prod-restore.sh` is absent.

- [ ] **Step 3: Implement new-volume restore**

Create exact replacement volume names only after target validation. Invoke the
Phase 5 restore command, revoke all restored sessions, run schema,
authorization, CSRF, object integrity, and Phase 5 recovery checks, then write
a switch proposal. Do not switch the production volume mapping automatically.
Keep original and restored volumes detached and recoverable.

- [ ] **Step 4: Verify and commit**

```bash
bash scripts/prod-restore_contract_test.sh
shellcheck scripts/prod-restore.sh
git add scripts/prod-restore.sh scripts/prod-restore_contract_test.sh
git commit -m "feat(phase6): guard destructive restore workflow"
```

### Task 7: Add release failure injection

- [ ] **Step 1: Write the matrix contract**

Create `scripts/phase6-release_failure_matrix_contract_test.sh` and require:

```text
unsafe_secret
insufficient_disk
unavailable_digest
pre_release_backup_failure
drain_timeout
migration_lock_conflict
migration_failure
app_readiness_failure
worker_readiness_failure
object_store_restart_failure
smoke_failure
signal_each_durable_step
previous_image_success
previous_image_incompatible
previous_image_readiness_failure
```

Every injector must be explicit, test-only, absent by default, and rejected in
server mode. Every case checks durable state, maintenance state, traffic state,
active/previous manifests, database schema, recovery evidence, diagnostics
sanitization, and cleanup.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/phase6-release_failure_matrix_contract_test.sh
```

Expected: FAIL because the matrix is absent.

- [ ] **Step 3: Implement and run the matrix**

Reuse the local production stack from the next plan. Do not mock away
PostgreSQL, Redis, AIStor, Restic, Caddy, or Compose behavior.

```bash
bash scripts/phase6-release_failure_matrix.sh
```

Expected: every named case passes, including signal interruption at every
durable transition.

- [ ] **Step 4: Commit**

```bash
git add scripts/phase6-release_failure_matrix.sh \
  scripts/phase6-release_failure_matrix_contract_test.sh
git commit -m "test(phase6): prove release failure handling"
```

### Task 8: Add hardened systemd templates and operator runbooks

- [ ] **Step 1: Write failing systemd contracts**

Create `scripts/phase6-systemd_contract_test.sh`. Require:

- absolute rendered project and command paths;
- exact Compose project `happylearn-prod`;
- `User` and `Group` set to the deployment account;
- `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem`, `ProtectHome`,
  `RestrictSUIDSGID`, and a narrow `ReadWritePaths`;
- no credentials, secret values, shell interpolation, or Docker socket;
- host sample every minute;
- backup dispatch every minute, with database schedule choosing 03:00
  Asia/Shanghai and manual work by local-date idempotency;
- degraded backup retry hourly;
- retention daily;
- restore verification quarterly;
- Compose startup after Docker and network availability.

- [ ] **Step 2: Run and verify RED**

```bash
bash scripts/phase6-systemd_contract_test.sh
```

Expected: FAIL because templates are absent.

- [ ] **Step 3: Implement templates and renderer**

`render-systemd.sh` accepts a validated absolute project directory, deployment
user, and output directory. It renders into that directory only, prints target
filenames and hashes, and never writes `/etc/systemd/system`. Real-host
installation remains a separately approved operator action.

- [ ] **Step 4: Write release and restore runbooks**

Document preflight, maintenance communication, release invocation, state
inspection, safe resume, automatic rollback boundaries, failed-safe response,
new-volume restore, volume switch approval, evidence collection, and explicit
real-server steps. Mark DNS, firewall, package installation, public TLS,
service installation, reboot, `v1.0.0-rc.1`, and `v1.0.0` as outside automatic
repository execution.

- [ ] **Step 5: Verify and commit**

```bash
bash scripts/phase6-systemd_contract_test.sh
shellcheck scripts/render-systemd.sh
git add deploy/systemd scripts/render-systemd.sh \
  scripts/phase6-systemd_contract_test.sh docs/runbooks
git commit -m "docs(phase6): add host operations templates"
```

The contract runs `systemd-analyze verify` when it is available and requires it
on the Ubuntu CI runner. On macOS it still performs the complete parser,
hardening, path, schedule, and secret-free assertions without assuming a host
systemd installation.

### Task 9: Integrate the release gate and review the slice

- [ ] **Step 1: Add stable targets**

Add `phase6-release-contracts` to run manifest tests, all host-script
contracts, mutation cases, ShellCheck, and systemd verification. Include it in
the repository contract aggregate.

- [ ] **Step 2: Run the focused gate**

```bash
go test ./internal/release ./cmd/release-manifest ./cmd/migrate ./cmd/acceptance
make phase6-release-contracts
git diff --check
```

Expected: PASS.

- [ ] **Step 3: Review against the approved protocol**

Trace success, pre-migration failure, migration failure, post-migration
rollback, rollback incompatibility, interruption, and destructive restore.
Confirm every state is durable, every retry reproves its postcondition,
maintenance fails closed, no down migration exists, recovery is verified, and
diagnostics cannot expose secrets or PII.

- [ ] **Step 4: Commit review fixes**

```bash
git add internal/release cmd scripts deploy/systemd docs/runbooks \
  Makefile package.json
git commit -m "chore(phase6): harden release and rollback workflows"
```
