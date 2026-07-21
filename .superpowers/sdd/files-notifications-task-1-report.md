# Files/Notifications Task 1 Report

## Outcome

Implemented purpose-bound multipart uploads without duplicating the upload pipeline.

- Added migration `00011_qa_file_purpose.sql` with non-null constrained `teaching` / `qa_attachment` purpose columns, legacy `teaching` backfill, owner-purpose-state index, Q&A access provenance, playback dedupe, and fail-closed downgrade behavior.
- Added fixed `TeachingUploadPolicy` and `QAUploadPolicy`; teaching remains active-admin-only and preserves the existing 500 MiB/type behavior.
- Q&A policy accepts active students/admins and enforces exact image/PDF/OOXML/TXT/Markdown limits. Archive, macro Office, SVG, HTML, executable/opaque, video, and extension/MIME mismatch are rejected before multipart creation.
- `UploadService` now receives an immutable fixed policy, persists session purpose, copies it to completed versions, and enforces owner + purpose for status, part admission, completion, and cancellation.
- `UploadHTTPConfig.AllowedRoles` is copied at construction and converted to a private set. Teaching remains admin-only; Q&A uploads are mounted separately at `/student/question-uploads` and `/admin/question-uploads` with exact roles.
- Production wiring supplies separate teaching and Q&A policy services. Existing direct schema fixtures were updated to name `teaching` explicitly after the migration removes column defaults.

## TDD Evidence

RED was observed first: the new policy boundary tests failed to compile because `QAUploadPolicy`, `TeachingUploadPolicy`, `ErrFileTooLarge`, and `ErrFileTypeRejected` did not exist.

GREEN verification:

- `go test ./internal/files ./internal/app ./cmd/server ./internal/platform/database ./internal/processing ./internal/qanda -count=1` — PASS
- affected PostgreSQL integration subset covering file schema, replace, upload resume, access, publication, Q&A, and processing — PASS
- `go test ./internal/platform/database -run QAFilePurpose -count=1` — PASS, including real fail-closed `DownTo(10)` with a Q&A access-log row
- `go test -race ./internal/files -count=1` — PASS
- `go vet ./internal/files ./internal/app ./cmd/server ./internal/platform/database ./internal/processing ./internal/qanda` — PASS
- `git diff --check` — PASS

## Environment Concern

The complete integration package has one pre-existing environment-only failure: `TestMinIOObjectStoreMultipartRangeAndAbort` cannot bootstrap the unavailable MinIO/AIStor service (`object store unavailable`). All PostgreSQL and in-memory object-store upload tests pass; no object-store service or AIStor license was available for this task.
