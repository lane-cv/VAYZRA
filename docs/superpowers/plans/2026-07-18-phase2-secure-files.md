# Phase 2 Secure Files Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add private MinIO storage, durable resumable multipart uploads, revision-bound file policy, constant-memory preview/download delivery, video Range support, and immutable access logging.

**Architecture:** PostgreSQL owns upload/file state; MinIO owns bytes. Browser traffic always passes through authenticated Go endpoints, which stream one upload part or response range without buffering a complete object. Teaching publication calls a narrow file-readiness checker before switching revisions.

**Tech Stack:** Go 1.26.5, pgx v5, `github.com/minio/minio-go/v7` v7.2.1, PostgreSQL 18.4, MinIO AIStor Free `quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120`, chi HTTP streaming.

## Global Constraints

- Teacher file size limit is exactly 500 MiB (`524288000` bytes).
- Default upload part size is 8 MiB; allow at most two in-flight parts for one upload.
- Upload sessions expire after 24 hours; cleanup uses a grace window and never deletes referenced objects.
- File bytes never enter PostgreSQL or logs; MinIO object keys are random and never reach the browser.
- Development S3 and console host ports bind only to loopback. Production deployment must omit S3 and console host-port mappings.
- Every file access rechecks session, active user, current published revision, audience, and file policy.
- “Preview only” blocks the download endpoint but is not represented as DRM.
- Preserve no-store, CSRF/origin for mutations, request IDs, trusted client IP, and audit behavior.
- Use TDD and isolated disposable PostgreSQL/MinIO instances.

---

## Locked File Structure

- `db/migrations/00005_secure_files.sql`: files, versions, previews, bindings, upload sessions/parts, and access logs.
- `internal/platform/objectstore/store.go`: object-store interface and multipart/range value types.
- `internal/platform/objectstore/minio.go`: MinIO implementation and bucket bootstrap.
- `internal/files/model.go`, `store.go`: domain and persistence contracts.
- `internal/files/postgres_store.go`: durable upload/file state and access-log implementation.
- `internal/files/upload_service.go`, `http_upload.go`: resumable upload domain and teacher HTTP API.
- `internal/files/access_service.go`, `http_access.go`: authorized preview/download/Range delivery.
- `internal/files/readiness.go`: teaching `PublicationCheck` implementation.
- `internal/files/*_test.go`, `tests/integration/files_test.go`: unit/integration coverage.
- `internal/platform/config/config.go`, `.env.example`, `deploy/compose.dev.yml`: MinIO configuration.

### Task 1: Private object store and durable file schema

**Files:**
- Create: `db/migrations/00005_secure_files.sql`
- Create: `internal/platform/objectstore/store.go`
- Create: `internal/platform/objectstore/minio.go`
- Test: `internal/platform/objectstore/minio_test.go`
- Modify: `internal/platform/config/config.go`
- Modify: `internal/platform/config/config_test.go`
- Modify: `.env.example`
- Modify: `deploy/compose.dev.yml`
- Modify: `go.mod`, `go.sum`
- Test: `tests/integration/files_test.go`

**Interfaces:**
- Consumes: `lessons`, `lesson_drafts`, `lesson_revisions`, users, config validation, and readiness aggregation.
- Produces: `objectstore.Store`, schema version 5, private `happylearn-originals` and `happylearn-previews` buckets.

- [ ] **Step 1: Write RED config, schema, and MinIO contract tests**

```go
type Store interface {
    CreateMultipart(context.Context, string, ObjectMeta) (uploadID string, err error)
    PutPart(context.Context, string, string, int, io.Reader, int64, string) (Part, error)
    CompleteMultipart(context.Context, string, string, []Part) (ObjectInfo, error)
    AbortMultipart(context.Context, string, string) error
    Stat(context.Context, string) (ObjectInfo, error)
    Get(context.Context, string, *ByteRange) (io.ReadCloser, ObjectInfo, error)
    Put(context.Context, string, io.Reader, int64, ObjectMeta) (ObjectInfo, error)
    Delete(context.Context, string) error
}
```

Test exact config failures for missing endpoint/access key/secret/bucket in production, endpoint values containing a URL path or credentials, and bucket names outside S3 naming rules. Integration-test multipart create/put/complete/stat/get-range/abort against disposable MinIO.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/platform/config ./internal/platform/objectstore ./tests/integration -run 'MinIO|FileSchema|ObjectStore' -count=1`

Expected: FAIL because the object-store package and schema do not exist.

- [ ] **Step 3: Add exact MinIO configuration and Compose service**

Add `HAPPYLEARN_MINIO_ENDPOINT`, `HAPPYLEARN_MINIO_ACCESS_KEY`, `HAPPYLEARN_MINIO_SECRET_KEY`, `HAPPYLEARN_MINIO_USE_TLS`, `HAPPYLEARN_MINIO_ORIGINALS_BUCKET`, and `HAPPYLEARN_MINIO_PREVIEWS_BUCKET`. Secrets must not be printed by config errors.

Compose must use the official MinIO AIStor Free single-node image `quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120`, where the digest is the Quay Linux/amd64 image manifest. Keep data in a named volume. A no-network root initializer may set that volume to UID 1000, while the long-running server must be non-root and use the exact `/minio/health/ready` endpoint. Bind S3 and console ports to `127.0.0.1` only in development. Production deployment must omit S3 and console host-port mappings. Add an idempotent Go bootstrap that creates only the configured private buckets.

AIStor Free requires a separately provisioned single-node license. The operator must download it outside Git, set `HAPPYLEARN_AISTOR_LICENSE_FILE` to that file, and ensure it exists before service startup. Development Compose exposes the `aistor_license` secret only to a no-network one-shot initializer, which atomically copies it into a controlled volume as `/license/minio.license` with ownership `1000:0` and mode `0440`; the long-running server mounts only that volume read-only. Production Compose uses its separate production-secret mount policy. License content must never appear in environment values, source control, logs, or tests. Each upgrade must resolve and pin the target-platform Quay manifest digest, update the deployment contract and docs atomically, fetch the official CycloneDX or SPDX SBOM and available release checksum metadata, and scan for reachable High/Critical vulnerabilities before rollout. An unresolved image, license, or vulnerability gate blocks deployment.

- [ ] **Step 4: Add file/upload tables with hard constraints**

Create `files`, `file_versions`, `file_previews`, `lesson_draft_files`, `lesson_revision_files`, `upload_sessions`, `upload_parts`, and `file_access_logs`. Enforce size `1..524288000`, allowed policies `preview|download`, processing states, unique `(upload_session_id, part_number)`, unique binding sort positions, restrictive foreign keys, and immutable revision bindings/access logs.

```sql
CREATE TABLE upload_sessions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  object_key text NOT NULL UNIQUE,
  minio_upload_id text NOT NULL UNIQUE,
  display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 255),
  declared_mime text NOT NULL CHECK (char_length(declared_mime) BETWEEN 1 AND 160),
  expected_size bigint NOT NULL CHECK (expected_size BETWEEN 1 AND 524288000),
  expected_sha256 text NOT NULL CHECK (expected_sha256 ~ '^[0-9a-f]{64}$'),
  state text NOT NULL CHECK (state IN ('open','completing','completed','cancelled','expired')),
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
```

- [ ] **Step 5: Implement MinIO adapter with bounded streams**

Use `minio.Core` multipart calls, explicit per-operation timeouts, TLS config from validated settings, path-style behavior suitable for local MinIO, and error mapping `ErrNotFound`, `ErrConflict`, `ErrUnavailable`. Never retry a non-rewindable reader inside the adapter.

Run: `go test ./internal/platform/objectstore ./internal/platform/config ./tests/integration -run 'MinIO|ObjectStore|FileSchema' -count=1`

Expected: PASS; a range request returns the exact requested bytes and aborted uploads leave no object.

- [ ] **Step 6: Commit object storage foundation**

```bash
git add db/migrations/00005_secure_files.sql internal/platform/objectstore internal/platform/config .env.example .gitignore deploy/compose.dev.yml docs/superpowers/specs/2026-07-18-phase2-teaching-design.md docs/superpowers/plans/2026-07-18-phase2-secure-files.md go.mod go.sum tests/integration/files_test.go
git commit -m "feat: add private object storage"
```

### Task 2: Resumable multipart upload API

**Files:**
- Create: `internal/files/model.go`
- Create: `internal/files/store.go`
- Create: `internal/files/postgres_store.go`
- Create: `internal/files/upload_service.go`
- Create: `internal/files/http_upload.go`
- Test: `internal/files/upload_service_test.go`
- Test: `internal/files/http_upload_test.go`
- Test: `internal/files/postgres_store_test.go`
- Modify: `internal/app/app.go`
- Modify: `cmd/server/main.go`
- Test: `tests/integration/files_test.go`

**Interfaces:**
- Consumes: Task 1 `objectstore.Store`, schema, Phase 1 admin principal/audit.
- Produces: `/api/v1/admin/uploads` create/status/part/complete/cancel APIs and a completed `files`/`file_versions` record.

- [ ] **Step 1: Write RED upload state-machine tests**

Cover: invalid extension/MIME/name/size/hash; non-admin; expired session; repeat identical part; conflicting repeat part; missing part at completion; duplicate completion; object-store failure; and cleanup not deleting referenced/completed objects.

```go
type CreateUploadInput struct {
    DisplayName string
    DeclaredMIME string
    ExpectedSize int64
    ExpectedSHA256 string
}
type PutPartInput struct {
    SessionID uuid.UUID
    Number int
    Size int64
    SHA256 string
    Body io.Reader
}
```

- [ ] **Step 2: Run upload tests to verify RED**

Run: `go test ./internal/files -run 'Upload|Part|Complete' -count=1`

Expected: FAIL because upload service is absent.

- [ ] **Step 3: Implement durable create/status/part operations**

Generate a random object key server-side, create MinIO multipart state, then persist the session. If persistence fails, abort the MinIO upload. Lock the session row when accepting/completing a part. Hash each incoming part while streaming with `io.TeeReader`; enforce `Content-Length`, 8 MiB for non-final parts, maximum two active HTTP part requests per session, and no body buffering.

```go
func (s *UploadService) PutPart(ctx context.Context, actor Principal, in PutPartInput) (PartView, error) {
    session, err := s.store.LockOpenSession(ctx, in.SessionID, actor.User.ID)
    if err != nil { return PartView{}, err }
    if s.now().After(session.ExpiresAt) { return PartView{}, ErrUploadExpired }
    digest := sha256.New()
    part, err := s.objects.PutPart(ctx, session.ObjectKey, session.MinIOUploadID, in.Number,
        io.TeeReader(io.LimitReader(in.Body, in.Size+1), digest), in.Size, in.SHA256)
    if err != nil { return PartView{}, err }
    return s.store.RecordPart(ctx, session.ID, in, part.ETag, hex.EncodeToString(digest.Sum(nil)))
}
```

- [ ] **Step 4: Implement atomic completion and compensation**

Use a `completing` state transition to serialize completion, validate contiguous part numbers and total size, call MinIO complete, stream-hash the final object to verify SHA-256, then transactionally create `files` and `file_versions` in `pending_scan` state and mark the upload completed. On hash mismatch, delete the completed object and mark the upload cancelled. A repeated completion returns the existing file version.

- [ ] **Step 5: Add strict streaming HTTP routes**

Use JSON only for create/status/complete/cancel. `PUT /{id}/parts/{number}` requires one `Content-Length`, `X-Part-SHA256`, exact media type `application/octet-stream`, admin role, CSRF/origin, and `http.MaxBytesReader` set to part size + 1. Do not use multipart/form-data.

Run: `go test ./internal/files ./internal/app ./cmd/server ./tests/integration -run 'Upload|File' -count=1`

Expected: PASS; memory-focused test verifies the handler never calls `io.ReadAll` and a restarted service resumes from persisted parts.

- [ ] **Step 6: Commit resumable uploads**

```bash
git add internal/files internal/app/app.go cmd/server/main.go tests/integration/files_test.go
git commit -m "feat: add resumable teaching uploads"
```

### Task 3: Revision binding, authorized delivery, Range, and access logs

**Files:**
- Create: `internal/files/access_service.go`
- Create: `internal/files/http_access.go`
- Create: `internal/files/readiness.go`
- Test: `internal/files/access_service_test.go`
- Test: `internal/files/http_access_test.go`
- Test: `internal/files/readiness_test.go`
- Modify: `internal/files/postgres_store.go`
- Modify: `internal/teaching/service.go`
- Modify: `internal/teaching/postgres_store.go`
- Modify: `internal/app/app.go`
- Modify: `cmd/server/main.go`
- Test: `tests/integration/files_test.go`

**Interfaces:**
- Consumes: completed file versions, teaching drafts/revisions/audiences, `objectstore.Get`.
- Produces: draft file binding APIs, `files.ReadinessChecker`, `/api/v1/files/{versionID}/preview`, `/download`, and revision snapshot copying.

- [ ] **Step 1: Write RED policy and cross-student tests**

Assert: preview-ready selected student can preview; other student gets `404`; preview-only download gets `404`; download policy succeeds; draft/historical/unpublished binding cannot be accessed; disabled student fails; Range `bytes=100-199` returns exactly 100 bytes and `206`; multi-range and suffix bombs are rejected with `416`; every allow/deny result writes a sanitized log.

- [ ] **Step 2: Run focused tests to verify RED**

Run: `go test ./internal/files ./tests/integration -run 'Access|Range|Readiness|Binding' -count=1`

Expected: FAIL because access/readiness services are absent.

- [ ] **Step 3: Implement draft binding and publication readiness**

Teacher APIs bind a specific `file_version_id`, policy, display name, description, and sort key to a draft. `ReadinessChecker.Check` rejects versions not in `ready`, scan not passed, or preview-only files without a ready preview. Extend publication transaction to copy draft bindings into immutable revision bindings before switching the published pointer.

```go
type ReadinessChecker struct { store ReadinessStore }
func (c *ReadinessChecker) Check(ctx context.Context, lessonID uuid.UUID) error {
    blockers, err := c.store.PublicationBlockers(ctx, lessonID)
    if err != nil { return err }
    if len(blockers) != 0 { return teaching.ErrNotPublishable }
    return nil
}
```

- [ ] **Step 4: Implement authorization-first delivery**

Resolve access with one SQL query joining active user, current published revision, frozen audience, revision file binding, file version, preview, and policy. Return only an opaque delivery record containing object key after authorization succeeds. For unauthorized/missing/not-ready return the same `ErrNotFound`.

Extend the Plan 1 student search projection with authorized `lesson_revision_files.display_name` values and add integration tests proving attachment-name matches obey the same current-revision and audience predicates.

Parse only one RFC 7233 byte range. Cap a single range at 64 MiB unless it reaches EOF; reject multiple ranges, invalid integers, and ranges outside object size. Stream with a 64 KiB copy buffer and cancel on request context.

- [ ] **Step 5: Set exact response and logging behavior**

Preview uses `Content-Disposition: inline` with RFC 5987-safe display name; download uses `attachment`. Set `Cache-Control: no-store, private`, `X-Content-Type-Options: nosniff`, `Accept-Ranges: bytes` for playable video, and correct `Content-Range`/`Content-Length`. Never expose object keys or MinIO errors.

Write access logs with user ID, version ID, revision ID, action, allow/deny result, request ID, canonical IP, and timestamp. Aggregate successful video ranges by a generated playback session header; never sample denies or malformed ranges.

- [ ] **Step 6: Run secure-files gate verification**

Run:

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
govulncheck ./...
docker compose -f deploy/compose.dev.yml config --quiet
```

Expected: all exit 0; disposable MinIO/PostgreSQL integration suite passes and process memory remains bounded while streaming a generated 500 MiB object.

- [ ] **Step 7: Commit secure file delivery**

```bash
git add internal/files internal/teaching internal/app/app.go cmd/server/main.go tests/integration/files_test.go
git commit -m "feat: enforce teaching file access"
```

## Secure Files Gate Review

Require independent spec and security reviews before processing work. Block on any Critical/Important issue involving object-key exposure, multipart compensation, hash verification, cross-student access, revision binding, preview/download policy, Range parsing, constant-memory streaming, access logs, or MinIO network exposure.
