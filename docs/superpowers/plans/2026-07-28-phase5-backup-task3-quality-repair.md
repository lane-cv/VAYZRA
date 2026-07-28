# Phase 5 Backup Task 3 Quality Repair Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close all eight fresh Task 3 quality-review gaps without adding Phase 5 Task 4 production behavior.

**Architecture:** Keep the current executor/workflow boundaries, but make Restic integration match the pinned 0.19.1 wire and copy semantics exactly. Treat every credential, lease, snapshot binding, cancellation, and durable state operation as fail-closed; preserve strict PostgreSQL equality and fencing.

**Tech Stack:** Go 1.26.5, Restic 0.19.1 JSON/CLI, PostgreSQL 18.4, Docker backup runtime.

---

### Task 1: Restic 0.19.1 raw-data schema and cache-free execution

**Files:**
- Modify: `internal/backup/executor.go`
- Test: `internal/backup/executor_test.go`
- Test: `cmd/backup/deployment_test.go`

- [ ] Add a failing decoder test using the captured Restic 0.19.1 v2 output with the exact required fields `total_size`, `total_uncompressed_size`, `compression_ratio`, `compression_progress`, `compression_space_saving`, `total_blob_count`, and `snapshots_count`.
- [ ] Add table cases that reject an unknown key, every missing key, `null`, negative counters, fractional/overflow counters, non-finite/overflow ratios, out-of-range progress, and a snapshot count other than one.
- [ ] Run `go test ./internal/backup -run 'TestDecodeResticStats' -count=1` and confirm the representative output fails under the old three-field schema.
- [ ] Replace `resticStats` with pointer-backed exact 0.19.1 fields, retain `DisallowUnknownFields`, require every pointer, and validate integer/float ranges.
- [ ] Add failing command/deployment tests requiring the global `--no-cache` flag on every Restic invocation so the non-login image user never touches `/home/happylearn-backup`.
- [ ] Prepend `--no-cache` to backup, local verification, copy, and remote verification commands; keep secrets and repositories out of arguments.
- [ ] Run focused tests and commit as `fix(backup): match restic 0.19.1 runtime`.

### Task 2: Authenticated HTTPS S3 remote copy and destination verification

**Files:**
- Modify: `internal/backup/executor.go`
- Test: `internal/backup/executor_test.go`
- Modify as required by secret mounts: `compose.yaml`, `.env.example`, or deployment contract files already used by the backup service

- [ ] Add failing FileSecrets/RemoteConfigured tests for missing and partial four-part tuples, unsafe secret permissions, symlinks, `http://`, embedded credentials/query/fragment, TLS-bypass spellings, and a valid `s3:https://host/bucket/prefix` repository.
- [ ] Add fixed `remote_access_key_id` and `remote_secret_access_key` secret names. Require repository, repository password, access key, and secret key together; all absent means “not configured.”
- [ ] Validate the Restic remote syntax by stripping `s3:` and parsing a credential-free HTTPS URL with a host and bucket path. Do not accept HTTP or any TLS-bypass option.
- [ ] Supply `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` only in the child environment for remote commands; never place them in argv, state, artifacts, or returned errors.
- [ ] Change `Sync` to receive the expected manifest SHA-256 along with the single full source snapshot ID.
- [ ] Add a failing copy test where Restic reports a distinct destination ID with `original` equal to the source. Require an exact two-tag query to return one full destination ID, then require remote `check --read-data`, exact destination stats, destination manifest dump, canonical decode, hash, and run binding before returning.
- [ ] Add negative cases for zero/multiple tag matches, wrong/missing `original`, reused source ID, wrong destination ID length, wrong tags, failed check/stats/dump, and tampered manifest. Assert no workflow remote artifact is recorded when executor verification fails.
- [ ] Implement copy → exact destination lookup → full remote verification, reusing strict repository verification helpers without weakening the local path.
- [ ] Run focused tests and commit as `fix(backup): verify authenticated remote copies`.

### Task 3: Workflow retry correctness

**Files:**
- Modify: `cmd/backup/main.go`
- Test: `cmd/backup/main_test.go`

- [ ] Add a failing remote failure → successful verified retry → finish test that asserts the completion has an empty error category.
- [ ] Clear `ErrorCategory` only after a successful verified remote sync.
- [ ] Add failing Prepare tests for a valid local draining fence with the same live owner, a different live owner, an expired lease takeover, corrupt local state, mismatched run ID, and absent state.
- [ ] Introduce a distinct local-state-not-found error. On an existing valid draining state, renew/reconcile it before generating an owner or calling `ClaimRunByID`; only an actually absent state starts a new claim. Corrupt/mismatched state fails closed.
- [ ] Run focused command tests and the existing real PostgreSQL lease tests, then commit as `fix(backup): make workflow retries fence aware`.

### Task 4: Context-aware hashing and traversal

**Files:**
- Modify: `internal/backup/executor.go`
- Test: `internal/backup/executor_test.go`

- [ ] Add failing tests that cancel after hashing has begun for the dump file and while hashing an object file; assert `ErrCancelled`.
- [ ] Pass `context.Context` into `hashBoundedRegularFile` and `summarizeObjectFiles`. Read fixed-size chunks, check cancellation before each chunk and traversal callback, and preserve size/symlink/same-file/capacity checks.
- [ ] Map cancellation back to `ErrCancelled` from `Snapshot`; verify deferred plaintext cleanup still empties the work directory.
- [ ] Run focused normal/race tests and commit as `fix(backup): cancel local hashing promptly`.

### Task 5: Durable workflow state directory metadata

**Files:**
- Modify: `cmd/backup/main.go`
- Test: `cmd/backup/main_test.go`

- [ ] Add failing tests with an injectable directory-sync failure after rename and after delete. Assert the operation returns `errWorkflowState` and that rename/delete already occurred.
- [ ] Open the fixed owner-only state directory, `Sync` it after atomic rename and after removal, and propagate open/sync/close failures without relaxing existing 0700/0600, ownership, symlink, size, or canonical JSON validation.
- [ ] Run focused normal/race tests and commit as `fix(backup): fsync workflow state directory`.

### Task 6: Final verification

**Files:**
- Review all files changed by Tasks 1–5.

- [ ] Run focused executor, workflow, deployment, cancellation, and state durability tests in normal and race modes.
- [ ] Run `go test ./internal/backup ./cmd/backup -count=1 -timeout=180s` against PostgreSQL 18.4.
- [ ] Run `go test -race ./internal/backup ./cmd/backup -count=1 -timeout=180s` against PostgreSQL 18.4.
- [ ] Run `go vet ./internal/backup ./cmd/backup`, `gofmt -d` on changed Go files, and `git diff --check`.
- [ ] Run available Docker/image contract tests and, if the runtime permits, a pinned-image Restic init/backup/stats fixture using `--no-cache`.
- [ ] Self-review all eight findings line by line, confirm no Task 4 production work, and report commit hashes plus fresh evidence.
