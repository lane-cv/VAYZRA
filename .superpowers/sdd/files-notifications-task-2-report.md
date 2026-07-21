# Files/Notifications Task 2 Report

## Result

Implemented immutable, transaction-bound Q&A attachments and authorized Q&A file status/preview/download delivery.

## RED evidence

- Added `internal/files/qa_access_test.go` first.
- `go test ./internal/files -run QAAccess -count=1` failed to compile because `QADelivery`, `QAFileStatus`, `QAOpenInput`, `NewQAAccessService`, and `AccessLog.QAMessageID` did not exist.

## Implementation

- Added `QAAttachmentValidator.ValidateForMessage` and transaction-local binding.
- PostgreSQL validates all requested version UUIDs with one parameterized `ANY($1)` query and `FOR UPDATE OF fv,f`, compares the exact returned UUID set, and derives owner/purpose/state/detected MIME/size/name only from the database.
- Enforced 20-record, 10-image, per-type/per-file, 100 MiB aggregate, duplicate UUID/sort-position, and normalized safe-name constraints.
- Q&A services validate before message insert and bind all trusted snapshots in the same transaction; any downstream audit/notification/binding failure rolls everything back.
- Added owner-only unbound status and current-thread-authorized bound status/preview/download resolution for active students and active admins. Browser input never includes a thread ID.
- Added safe status DTO and Q&A delivery DTOs that omit file ID, creator, checksum, bucket/object key, processing commands, tokens, and other user/thread IDs.
- Added Q&A access logging through the existing immutable log table with `qa_message_id`, normalized handler IP, allow/deny/failure outcomes, Range metadata, bounded streaming, no-store/nosniff, and canonical content disposition.
- Mounted `/api/v1/question-files/{version}/status|preview|download` and production wiring.
- Removed the old purpose-unchecked, per-file attachment bind helpers.

## Verification

- `go test ./internal/qanda ./internal/files ./internal/app ./cmd/server -count=1` — PASS.
- Focused Q&A attachment/access/route tests — PASS.
- Affected integration selection covering file schema, replacement, teaching/Q&A purpose isolation, access authorization/logs, publication, and Q&A schema — PASS.
- `go test -race ./internal/qanda ./internal/files -count=1` — PASS.
- `go vet ./internal/qanda ./internal/files ./internal/app ./cmd/server` — PASS.
- `git diff --check` — PASS.

Authorization regressions cover random/cross-student uniform 404 envelopes, disabled students, active admin access, unbound owner-only status, teaching-purpose denial, rejected/failed delivery denial, strict methods/query/canonical UUIDs, safe status JSON, Range 206 streaming, and `qa_message_id` access logs.

## Environment concern

The pre-existing MinIO/AIStor integration requires an unavailable local object-store service/license. Per task instructions, this task used in-memory object stores plus PostgreSQL authorization/integration tests and did not wait for that external environment.
