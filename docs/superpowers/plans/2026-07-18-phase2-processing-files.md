# Phase 2 File Processing and File Center Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Process uploaded teaching files safely with durable single-concurrency jobs, malware/type checks, Office preview conversion, video probing, and teacher file-center/version operations.

**Architecture:** A separate `cmd/worker` process leases PostgreSQL jobs and downloads one private object into a bounded tmpfs. A command-runner interface invokes pinned scanner/converter/prober binaries with deadlines and no shell interpolation. Results are written to MinIO and PostgreSQL; the web app remains available when processing is slow or unavailable.

**Tech Stack:** Go 1.26.5, PostgreSQL 18.4, MinIO, ClamAV, LibreOffice headless, Poppler, ffprobe, Docker multi-stage worker image.

## Global Constraints

- Processing concurrency is exactly 1 on the 2-core/4-GB target.
- Worker runs non-root with read-only root, tmpfs workspace, dropped capabilities, `no-new-privileges`, memory/CPU limits, access only to the private PostgreSQL/MinIO network, and no public-internet egress.
- Commands use argv arrays through `exec.CommandContext`; never invoke a shell or concatenate file names.
- Reject archives, executables, macro Office, SVG/HTML, type mismatches, and files larger than 500 MiB.
- Do not transcode video.
- Preview-only bindings are publishable only when a ready preview exists.
- Replacement creates a new version; unreferenced old versions remain recoverable for 30 days.
- Processing failures use stable categories, bounded retries, leases, and sanitized logs.

---

## Locked File Structure

- `db/migrations/00006_file_processing.sql`: job leases, type/probe metadata, retention and immutability constraints.
- `internal/processing/model.go`, `store.go`, `postgres_store.go`: durable job domain.
- `internal/processing/worker.go`: lease loop, heartbeat, timeout, retry, and result transaction.
- `internal/processing/runner.go`: safe command-runner interface.
- `internal/processing/detect.go`, `scan.go`, `office.go`, `video.go`: focused processors.
- `cmd/worker/main.go`: worker wiring and health endpoint.
- `Dockerfile.worker`: pinned processing image and non-root runtime.
- `internal/files/file_center_service.go`, `http_file_center.go`: teacher file-center APIs.
- `internal/processing/*_test.go`, `internal/files/file_center_service_test.go`, `tests/integration/processing_test.go`: coverage.

### Task 1: Durable job leasing and worker lifecycle

**Files:**
- Create: `db/migrations/00006_file_processing.sql`
- Create: `internal/processing/model.go`
- Create: `internal/processing/store.go`
- Create: `internal/processing/postgres_store.go`
- Create: `internal/processing/worker.go`
- Create: `cmd/worker/main.go`
- Modify: `internal/files/upload_service.go`
- Modify: `internal/files/postgres_store.go`
- Test: `internal/processing/worker_test.go`
- Test: `internal/processing/postgres_store_test.go`
- Test: `tests/integration/processing_test.go`

**Interfaces:**
- Consumes: Plan 2 file versions, MinIO store, database pool, readiness health pattern.
- Produces: `processing.Store`, `processing.Processor`, `processing.Worker.Run`, and reliable `pending_scan` → terminal state transitions.

- [ ] **Step 1: Write RED lease/retry/concurrency tests**

Assert two workers cannot own the same job; expired leases are reclaimed; heartbeats extend only the current owner lease; permanent errors stop; transient errors use delays 1m/5m/30m and stop after 4 total attempts; graceful cancellation releases no completed job; one worker never processes two jobs concurrently.

```go
type Job struct {
    ID, FileVersionID uuid.UUID
    Kind string
    Attempts int
    LeaseOwner string
    LeaseUntil time.Time
}
type Processor interface {
    Process(context.Context, Job) (Result, error)
}
type Store interface {
    LeaseNext(context.Context, string, time.Time, time.Duration) (Job, error)
    Heartbeat(context.Context, uuid.UUID, string, time.Time) error
    Complete(context.Context, Job, Result) error
    Fail(context.Context, Job, Failure) error
}
```

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/processing ./tests/integration -run 'Lease|Worker|Retry' -count=1`

Expected: FAIL because migration 6 and processing package are absent.

- [ ] **Step 3: Add lease and result schema**

Extend `file_versions` with detected type, scan result, video codec/container/duration/dimensions, failure category, and retention deadline. Add `file_processing_jobs` with unique active job per `(file_version_id,kind)`, attempts, `available_at`, lease owner/until, and last failure category. The migration inserts one queued `process_file` job for every existing `pending_scan` version. Modify upload completion so the same transaction that creates each future `pending_scan` version also inserts its `process_file` job. Claim jobs using `FOR UPDATE SKIP LOCKED` in a short transaction.

```sql
WITH candidate AS (
  SELECT id FROM file_processing_jobs
  WHERE state='queued' AND available_at <= now()
    AND (lease_until IS NULL OR lease_until < now())
  ORDER BY available_at, created_at
  FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE file_processing_jobs j
SET state='running', lease_owner=$1, lease_until=$2, attempts=attempts+1
FROM candidate WHERE j.id=candidate.id
RETURNING j.id, j.file_version_id, j.kind, j.attempts, j.lease_owner, j.lease_until;
```

- [ ] **Step 4: Implement worker loop and atomic result writes**

Use condition-based polling with a maximum 2-second idle wait, 30-second lease, 10-second heartbeat, and per-kind deadline. `Complete` must atomically update file/preview state and job state only when lease owner matches. Lost leases cannot commit stale results. SIGTERM cancels the current command, waits up to 20 seconds, then exits nonzero if cleanup fails.

- [ ] **Step 5: Wire `cmd/worker` and readiness**

Use the same validated database/MinIO config as the app. Worker readiness requires database ping, MinIO stat/bootstrap, writable tmpfs work directory, and detected command versions. Liveness only reflects the Go process. Never expose the worker port outside the private Docker network.

Run: `go test ./internal/processing ./cmd/worker ./tests/integration -run 'Lease|Worker|Retry' -count=1`

Expected: PASS; race test shows maximum observed processor concurrency equals 1.

- [ ] **Step 6: Commit durable processing lifecycle**

```bash
git add db/migrations/00006_file_processing.sql internal/processing internal/files/upload_service.go internal/files/postgres_store.go cmd/worker tests/integration/processing_test.go
git commit -m "feat: add durable file processing jobs"
```

### Task 2: Safe detection, scanning, preview conversion, and video probing

**Files:**
- Create: `internal/processing/runner.go`
- Create: `internal/processing/detect.go`
- Create: `internal/processing/scan.go`
- Create: `internal/processing/office.go`
- Create: `internal/processing/video.go`
- Test: `internal/processing/runner_test.go`
- Test: `internal/processing/detect_test.go`
- Test: `internal/processing/scan_test.go`
- Test: `internal/processing/office_test.go`
- Test: `internal/processing/video_test.go`
- Create: `Dockerfile.worker`
- Modify: `deploy/compose.dev.yml`
- Modify: `docs/runbooks/local-development.md`

**Interfaces:**
- Consumes: Task 1 `Processor`, Plan 2 object store and file metadata.
- Produces: deterministic processing results and ready preview objects.

- [ ] **Step 1: Write RED table-driven type and command safety tests**

Fixtures must include valid PDF/JPEG/PNG/WebP/GIF/DOCX/XLSX/PPTX/MP4/WebM/Ogg/MOV/AVI/MKV/TXT/Markdown and rejected ZIP/RAR/7Z/EXE/DLL/SVG/HTML/DOCM/XLSM/PPTM, double extensions, mismatched MIME, traversal names, malformed Office ZIP, and oversized parser output.

```go
type Runner interface {
    Run(ctx context.Context, executable string, args []string, stdoutLimit, stderrLimit int64) (stdout, stderr []byte, exitCode int, err error)
}
```

Assert the exact input path is one argv element, never part of a shell command; output limits terminate the child process; context timeout terminates descendants; errors never include object key or file contents.

- [ ] **Step 2: Run processor tests to verify RED**

Run: `go test ./internal/processing -run 'Detect|Scan|Office|Video|Runner' -count=1`

Expected: FAIL because processor implementations are absent.

- [ ] **Step 3: Implement safe download, type detection, and malware scan**

Create one random work directory beneath configured tmpfs with mode 0700. Stream the object to a randomly named file while hashing and enforcing 500 MiB + 1. Compare extension, declared MIME, Go sniffing, container signatures, and parser result against the explicit allowlist. Invoke `clamscan --no-summary --infected --max-filesize=500M --max-scansize=500M -- <path>` and map exit 0 clean, 1 rejected, other transient failure.

- [ ] **Step 4: Implement Office/PDF/image preview and video probe**

DOCX/XLSX/PPTX: run LibreOffice headless with a fresh user profile under tmpfs, convert to PDF, validate produced PDF, optionally render bounded page thumbnails with Poppler, then stream results to random preview keys. PDF/images: validate structure and dimensions; reject decompression bombs and animated GIF.

Video: run ffprobe JSON with output cap 1 MiB; validate one video stream, container, codec, duration ≤ 12 hours, dimensions ≤ 7680×4320; mark MP4/WebM/Ogg playable only for approved browser codecs. MOV/AVI/MKV become ready original files without a preview and therefore require download policy.

- [ ] **Step 5: Build and smoke-test the hardened worker image**

Pin Debian and package versions or immutable image digests, including malware definitions refreshed by rebuilding the image. Runtime user must be non-root. Compose configuration must set `read_only: true`, tmpfs size 1024 MiB, `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, memory 1792 MiB, CPU 1.0, an `internal: true` service network with no public-internet route, and one replica.

Run:

```bash
go test ./internal/processing -count=1
docker build -f Dockerfile.worker -t happylearn-worker:phase2 .
docker compose -f deploy/compose.dev.yml config --quiet
```

Expected: tests pass; image user is non-root; EICAR fixture is rejected; DOCX fixture yields valid PDF; MP4 fixture yields playable metadata; worker has no public port.

- [ ] **Step 6: Commit safe processing**

```bash
git add internal/processing Dockerfile.worker deploy/compose.dev.yml docs/runbooks/local-development.md
git commit -m "feat: process teaching files safely"
```

### Task 3: File center, replacement, rollback, cleanup, and readiness integration

**Files:**
- Create: `internal/files/file_center_service.go`
- Create: `internal/files/http_file_center.go`
- Test: `internal/files/file_center_service_test.go`
- Test: `internal/files/http_file_center_test.go`
- Modify: `internal/files/postgres_store.go`
- Modify: `internal/files/readiness.go`
- Modify: `internal/app/app.go`
- Modify: `cmd/server/main.go`
- Create: `cmd/maintenance/main.go`
- Test: `cmd/maintenance/main_test.go`
- Test: `tests/integration/processing_test.go`

**Interfaces:**
- Consumes: processed file metadata, version bindings, immutable access logs, upload service.
- Produces: `/api/v1/admin/files` list/detail/retry/replace/rollback/delete APIs and safe orphan cleanup command.

- [ ] **Step 1: Write RED file-center and retention tests**

Assert filters by display name/type/state/reference/time; details list draft/published references without object keys; only transient failures can retry; replacement creates a new version; rollback binds an existing retained version to the draft; published references prevent deletion; unreferenced versions younger than 30 days remain; eligible deletion writes audit before object removal scheduling.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/files ./cmd/maintenance ./tests/integration -run 'FileCenter|Replace|Rollback|Cleanup' -count=1`

Expected: FAIL because file-center and maintenance services are absent.

- [ ] **Step 3: Implement paginated file-center service and routes**

```go
type FileCenterService interface {
    List(context.Context, Principal, FileFilter, Cursor) (FilePage, error)
    Detail(context.Context, Principal, uuid.UUID) (FileDetail, error)
    Retry(context.Context, Principal, uuid.UUID) error
    Replace(context.Context, Principal, uuid.UUID, uuid.UUID) error
    RollbackDraftBinding(context.Context, Principal, uuid.UUID, uuid.UUID) error
    RequestDelete(context.Context, Principal, uuid.UUID) error
}
```

Use stable `(created_at,id)` cursors, limit ≤ 100, normalized substring search, and exact reference summaries. Map referenced delete to `409 file_in_use`; map expired rollback to `410 file_version_expired`.

- [ ] **Step 4: Implement safe cleanup and publication readiness**

`cmd/maintenance cleanup-files` selects only soft-deleted/unreferenced versions whose retention deadline is older than now, claims rows with `SKIP LOCKED`, rechecks references in the transaction, records audit/outbox, then deletes MinIO objects. Object deletion failure retains the row for retry; database metadata is removed only after object deletion succeeds.

Ensure readiness distinguishes `scan_pending`, `scan_rejected`, `conversion_pending`, `conversion_failed`, and `preview_missing`, returning a structured blocker list to the teacher while student APIs remain non-enumerating.

- [ ] **Step 5: Run the processing gate**

Run:

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
govulncheck ./...
docker compose -f deploy/compose.dev.yml config --quiet
docker build -f Dockerfile.worker -t happylearn-worker:phase2 .
```

Expected: all exit 0; disposable PostgreSQL/MinIO tests prove references and 30-day retention; forced converter/MinIO outages do not affect auth or published text-only lessons.

- [ ] **Step 6: Commit file center and cleanup**

```bash
git add internal/files internal/app/app.go cmd/server/main.go cmd/maintenance tests/integration/processing_test.go
git commit -m "feat: add teaching file lifecycle management"
```

## Processing Gate Review

Require independent spec and security reviews. Block progression for any Critical/Important issue involving command injection, scanner bypass, parser bombs, worker network/resource isolation, stale lease writes, retry storms, preview readiness, reference-safe deletion, retention, rollback, or error/log data leakage.
