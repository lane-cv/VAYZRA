# Phase 5 Backup Task 3 Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bind every recovery manifest to the exact online-verifiable Restic snapshot and make the backup command safely resumable without claiming or mutating an unrelated run.

**Architecture:** The executor creates the canonical manifest before one Restic recovery snapshot containing the database dump, object data, manifest, and Age-encrypted offline bundle. Verification checks the exact snapshot ID and exact batch/hash tags, restores the manifest from that snapshot, enforces canonical bytes, and recomputes its hash. The store adds exact-ID claiming, while the command reconciles durable database state with the owner-only local state file, renewing a live fence or taking over only an expired lease.

**Tech Stack:** Go 1.26.5, PostgreSQL 18.4, Restic 0.19.1, Age 1.3.1.

---

## File structure

- Modify `internal/backup/manifest.go` and `manifest_test.go` for byte-canonical decoding.
- Modify `internal/backup/executor.go` and `executor_test.go` for one recovery snapshot and typed verification.
- Modify `internal/backup/store.go`, `service.go`, `postgres_store.go`, and their tests for exact run claims.
- Modify `cmd/backup/main.go` and `main_test.go` for durable/local reconciliation and crash recovery.

### Task 1: Canonical manifest and exact snapshot binding

- [ ] **Step 1: Write the failing manifest tests**

Add cases that remove valid zero-valued keys, reorder keys, add insignificant
whitespace, or use a noncanonical time representation:

```go
func TestManifestDecoderRequiresExactCanonicalBytes(t *testing.T) {
	for _, encoded := range []string{
		strings.Replace(canonicalManifestJSON, `,"objectCount":12`, "", 1),
		`{"batchId":"10000000-0000-4000-8000-000000000001","schemaVersion":1,"createdAt":"2026-07-28T01:02:03.000000004Z","databaseMigrationVersion":20,"databaseDumpSha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","objectSnapshotId":"8f7e6d5c4b3a2100","objectCount":12,"referencedBytes":3456}`,
		" " + canonicalManifestJSON,
	} {
		if _, err := DecodeManifest(strings.NewReader(encoded)); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("err=%v", err)
		}
	}
}
```

- [ ] **Step 2: Verify manifest RED**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/backup -run 'ManifestDecoderRequiresExactCanonicalBytes' -count=1
```

Expected: FAIL because reordered/whitespace/missing zero-valued fields decode.

- [ ] **Step 3: Implement byte-canonical decoding**

After strict decoding and semantic validation, marshal the value again and
require `bytes.Equal(encoded, canonical)`. Preserve the 64 KiB limit,
duplicate-key rejection, `DisallowUnknownFields`, and EOF check.

- [ ] **Step 4: Write executor binding RED tests**

Change the wished-for API to:

```go
type VerifyInput struct {
	RunID         string
	SnapshotID    string
	ManifestSHA256 [sha256.Size]byte
}
type VerifyResult struct {
	Manifest Manifest
}
func (Executor) Verify(context.Context, VerifyInput) (VerifyResult, error)
```

Test wrong batch tags, a valid snapshot from another run, a missing or
mismatched manifest-hash tag, restored manifest tampering, and an expected hash
that differs from the canonical restored bytes. Each must return only
`ErrIntegrity`.

- [ ] **Step 5: Verify executor RED**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/backup -run 'Executor.*(Snapshot|Verify|Manifest)' -count=1
```

Expected: FAIL because verification currently checks only repository data and
stats and cannot restore or hash the manifest.

- [ ] **Step 6: Implement one exact recovery snapshot**

Compute the object-set content identity before the Restic call. Marshal and
write `manifest.json`, create the Age-encrypted offline bundle, then call
Restic once with relative `database.dump`, relative `manifest.json`, the
encrypted bundle, and the object root. Apply exactly:

```go
[]string{
	"happylearn-batch:" + runID,
	"happylearn-manifest-sha256:" + hex.EncodeToString(manifestHash[:]),
}
```

Verification runs `check --read-data`, obtains the exact snapshot JSON, checks
the full snapshot ID and exact tag set, checks stats, dumps `manifest.json`
from that same snapshot with a 64 KiB bound, calls `DecodeManifest`, recomputes
SHA-256, and compares both batch ID and expected hash.

- [ ] **Step 7: Verify Task 1 GREEN**

Run:

```bash
GOENV=off GOFLAGS='' go test ./internal/backup -run 'Manifest|Executor' -count=1
```

Expected: PASS.

### Task 2: Exact target claim semantics

- [ ] **Step 1: Write targeted claim RED tests**

Add `ClaimRunByID(context.Context, uuid.UUID, uuid.UUID, time.Duration)` to the
wished-for store/service API. Insert two queued rows directly, claim the second
ID, and prove the first row's owner, generation, and lease remain unchanged.
Also prove a missing or terminal target never claims another row.

- [ ] **Step 2: Verify targeted claim RED**

Run:

```bash
HAPPYLEARN_TEST_DATABASE_URL="$HAPPYLEARN_TEST_DATABASE_URL" \
GOENV=off GOFLAGS='' go test ./internal/backup -run 'ClaimRunByID' -count=1
```

Expected: FAIL because the interface and implementation do not exist.

- [ ] **Step 3: Implement exact target claims**

Share the existing advisory-lock, database-clock, generation increment,
expiry/takeover, commit-loss reconciliation, and error mapping logic. The
targeted query must use `WHERE id=$1 FOR UPDATE`; it must return
`ErrActiveClaim` for a live owner, allow takeover only after database-clock
expiry, and reconcile commit loss by exact run ID + owner + generation.

- [ ] **Step 4: Verify Task 2 GREEN**

Run the targeted unit/integration tests and the existing claim, renewal,
transition, artifact, and commit-reconciliation tests.

### Task 3: Durable/local state reconciliation

- [ ] **Step 1: Write command recovery RED tests**

Extend the workflow fixture with exact claim and durable lookup. Inject one
state save failure after each nonterminal database transition. Prove a retry:

```go
prepare: queued -> draining
snapshot: draining -> snapshotting -> encrypting
verify: encrypting -> verifying
sync: verifying -> syncing
finish/fail: nonterminal -> terminal before state deletion
```

Also prove a continuation renews the current fence, takes over only after lease
expiry with a new owner/generation, rejects a live different owner, and never
allows the stale owner to mutate after takeover.

- [ ] **Step 2: Verify command recovery RED**

Run:

```bash
GOENV=off GOFLAGS='' go test ./cmd/backup -run 'CommandApplication.*(Reconcile|Crash|Takeover|Target)' -count=1
```

Expected: FAIL because `renew` requires local and durable states to be
identical and cannot take over or reconcile an advanced durable state.

- [ ] **Step 3: Implement reconciliation**

Before every command, read the exact durable run. If the local owner/generation
still has an unexpired lease, renew it. Otherwise call exact target claim with
a fresh owner, which succeeds only after expiry. Accept only the same durable
state or the exact forward state caused by the command's prior transition;
refresh local owner/generation/state and save before continuing.

Persist `RecoveryEvidence` on `snapshotting -> encrypting`, so the durable row
contains the expected snapshot and manifest hash. Rebuild manifest-derived
artifact metadata from the verified manifest instead of trusting stale local
bytes. Treat an already-durable terminal transition as an idempotent retry that
only removes the stale state file. All mutations continue to carry current
owner and lease generation.

- [ ] **Step 4: Verify Task 3 GREEN**

Run all `cmd/backup` tests, including normal and `-race`.

### Final gate: Verification and commits

- [ ] **Step 1: Run formatting and static checks**

```bash
gofmt -w internal/backup/manifest.go internal/backup/manifest_test.go \
  internal/backup/executor.go internal/backup/executor_test.go \
  internal/backup/store.go internal/backup/service.go \
  internal/backup/postgres_store.go internal/backup/postgres_store_test.go \
  cmd/backup/main.go cmd/backup/main_test.go
git diff --check
go vet ./internal/backup ./cmd/backup
```

- [ ] **Step 2: Run full focused gates**

```bash
go test ./internal/backup ./cmd/backup -count=1
go test -race ./internal/backup ./cmd/backup -count=1
```

Expected: PASS with the PostgreSQL 18.4 test database.

- [ ] **Step 3: Review and commit**

Review the diff against all five findings, verify Task 4 production work is
absent, then create logical commits for executor/manifest binding and
claim/reconciliation hardening.
