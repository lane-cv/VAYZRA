# CI Database Serialization and MinIO Readiness Design

## Problem

The `verify` workflow fails for two independent environmental reasons:

1. `go test ./...` starts package test binaries in parallel, while PostgreSQL-backed
   tests across those packages serialize themselves with the same advisory lock.
   Package processes can spend most of Go's default ten-minute test timeout waiting
   in the cross-package lock queue.
2. Compose declares AIStor healthy using `/minio/health/live`. That endpoint proves
   the process is alive, but it does not guarantee that the authenticated S3 API is
   ready when the MinIO integration test starts.

The isolated AIQA test and the complete `internal/aiqa` package pass against a
dedicated PostgreSQL instance, confirming that the observed AIQA timeout is caused
by CI scheduling rather than a deterministic runtime-store deadlock.

## Design

### Serialize Go package test binaries

Run both ordinary and race-enabled repository tests with `-p 1`:

```sh
GOFLAGS='' go test -p 1 ./... -count=1
GOFLAGS='' go test -race -p 1 ./... -count=1
```

This aligns package scheduling with the repository's existing global PostgreSQL
test lock. Individual tests keep their current isolation mechanism, and no
application runtime behavior changes.

### Use MinIO readiness semantics

Change the Compose MinIO health check from `/minio/health/live` to
`/minio/health/ready`. Services depending on MinIO and `docker compose --wait`
will then wait for the S3 service to become ready rather than merely live.

The workflow will also perform an authenticated, bounded readiness probe before
running Go tests. The probe uses the credentials already configured inside the
MinIO container, does not print them, and fails with a stable diagnostic if the S3
API cannot authenticate or answer.

### Preserve failure evidence

When the `verify` job fails, emit bounded, sanitized dependency status and MinIO
container logs before Compose teardown. This must not print environment variables,
license contents, or credentials.

## Testing

Repository contract tests will verify that:

- both workflow Go test commands include `-p 1`;
- the MinIO health check uses `/minio/health/ready` and not `/live`;
- the authenticated readiness probe occurs after dependency startup and before Go
  tests;
- failure diagnostics remain ordered before dependency teardown and avoid commands
  that expose environment variables or secret files.

The contract tests must fail before the workflow and Compose changes are made.
After the minimal changes, run the focused contract tests, Compose configuration
validation, the isolated AIQA database test, the complete AIQA package, and the
MinIO integration test.

## Scope

This change is limited to CI scheduling, dependency readiness, diagnostics, and
their contract tests. It does not change AIQA business logic, quota locking,
object-store client retry behavior, production credentials, or license handling.
