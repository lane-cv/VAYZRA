# Phase 5 Operations Foundation Gate Report

Date: 2026-07-28

## Reviewed range

- Base: `63973540b960a94902af5be2605bd89d85560ba3`
- Reviewed implementation tip: `74f6a09d4e31f8720bbf59dfbbebe33c009af6e5`
- Range: `63973540b960a94902af5be2605bd89d85560ba3..74f6a09d4e31f8720bbf59dfbbebe33c009af6e5`

This range contains Phase 5 Operations Foundation Tasks 1–5. Task 6 adds only
this verification report.

## Fresh complete gate

- `GOENV=off GOFLAGS='' go test -p 1 ./internal/operations ./internal/audit
  ./internal/app ./internal/notifications ./internal/aiqa
  ./internal/processing ./cmd/server ./cmd/worker -count=1` — PASS;
  `internal/operations` 9.564s, `internal/audit` 0.091s,
  `internal/app` 0.005s, `internal/notifications` 0.910s,
  `internal/aiqa` 3.162s, `internal/processing` 0.434s,
  `cmd/server` 0.139s, and `cmd/worker` 0.025s; exit 0.
- `GOENV=off GOFLAGS='' go test -race -p 1 ./internal/operations
  ./internal/audit ./internal/app ./internal/notifications ./internal/aiqa
  ./internal/processing ./cmd/server ./cmd/worker -count=1` — PASS;
  `internal/operations` 11.666s, `internal/audit` 1.166s,
  `internal/app` 1.034s, `internal/notifications` 2.381s,
  `internal/aiqa` 7.180s, `internal/processing` 1.561s,
  `cmd/server` 1.168s, and `cmd/worker` 1.050s; exit 0.
- `pnpm test` — PASS; 60 test files and 350 tests passed.
- `pnpm typecheck` — PASS; `vue-tsc --noEmit` exited 0.
- `pnpm lint` — PASS; ESLint exited 0 with `--max-warnings=0`.
- `pnpm build` — PASS; Vite transformed 261 modules and completed the
  production build.
- `git diff --check` — PASS before this report was created.

### Actual sanitized invocation

This subsection records the commands that were actually run. Only the absolute
worktree path and the URL-safe test password have been replaced with
`$PHASE5_SOURCE` and `$PHASE5_PG_PASSWORD`; every Docker option, image tag,
container/volume name, port, package, and Go flag remains as executed. The
operator-local values are not repository data and must not be committed:

```sh
export PHASE5_SOURCE='<redacted absolute phase5-phase6 worktree path>'
export PHASE5_PG_PASSWORD='<redacted URL-safe test password>'
export PHASE5_TEST_DATABASE_URL="postgres://happylearn:${PHASE5_PG_PASSWORD}@host.docker.internal:54330/happylearn?sslmode=disable"
```

Before starting the containers, read-only `docker image inspect` checks
verified that the tags used by the actual CLI resolved to:

- `golang:1.26.5-bookworm` →
  `sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651`;
- `postgres:18.4` →
  `sha256:3a82e1f56c8f0f5616a11103ac3d47e632c3938698946a7ad26da0df1334744a`.

The fresh database was started by the following actual command. It used `-d`,
did not use `--rm`, stored PostgreSQL data in a 512 MiB tmpfs, and published
only loopback port 54330:

```sh
docker run -d \
  --name vayzra-phase5-gate-postgres-019fa57b \
  --tmpfs /var/lib/postgresql:rw,size=512m \
  -e POSTGRES_USER=happylearn \
  -e POSTGRES_PASSWORD="$PHASE5_PG_PASSWORD" \
  -e POSTGRES_DB=happylearn \
  -p 127.0.0.1:54330:5432 \
  --health-cmd 'pg_isready -U happylearn -d happylearn' \
  --health-interval 2s \
  --health-timeout 5s \
  --health-retries 20 \
  postgres:18.4
```

After startup, the actual health verification was one read:

```sh
docker inspect --format '{{.State.Health.Status}}' \
  vayzra-phase5-gate-postgres-019fa57b
# healthy
```

The passing normal gate used the Go tag, read-only source bind, and the two
named cache volumes shown below:

```sh
docker run --rm \
  -v $PHASE5_SOURCE:/src:ro \
  -v vayzra-phase5-go-mod:/go/pkg/mod \
  -v vayzra-phase5-go-build:/root/.cache/go-build \
  -w /src \
  -e GOENV=off \
  -e GOFLAGS= \
  -e HAPPYLEARN_TEST_DATABASE_URL="$PHASE5_TEST_DATABASE_URL" \
  golang:1.26.5-bookworm \
  go test -p 1 \
    ./internal/operations ./internal/audit ./internal/app \
    ./internal/notifications ./internal/aiqa ./internal/processing \
    ./cmd/server ./cmd/worker -count=1
```

The passing race gate used the same database, read-only source, named caches,
and sanitized URL:

```sh
docker run --rm \
  -v $PHASE5_SOURCE:/src:ro \
  -v vayzra-phase5-go-mod:/go/pkg/mod \
  -v vayzra-phase5-go-build:/root/.cache/go-build \
  -w /src \
  -e GOENV=off \
  -e GOFLAGS= \
  -e HAPPYLEARN_TEST_DATABASE_URL="$PHASE5_TEST_DATABASE_URL" \
  golang:1.26.5-bookworm \
  go test -race -p 1 \
    ./internal/operations ./internal/audit ./internal/app \
    ./internal/notifications ./internal/aiqa ./internal/processing \
    ./cmd/server ./cmd/worker -count=1
```

### Equivalent digest-pinned reproduction

This recipe was not the executed CLI above; it is an equivalent
digest-pinned reproduction. It deliberately generates a URL-safe hexadecimal
password before composing the database URL:

```sh
export PHASE5_SOURCE='<absolute path to the phase5-phase6 worktree>'
export PHASE5_PG_PASSWORD="$(openssl rand -hex 24)"
export PHASE5_TEST_DATABASE_URL="postgres://happylearn:${PHASE5_PG_PASSWORD}@host.docker.internal:54330/happylearn?sslmode=disable"
export PHASE5_GO_IMAGE='golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651'
export PHASE5_PG_IMAGE='postgres:18.4@sha256:3a82e1f56c8f0f5616a11103ac3d47e632c3938698946a7ad26da0df1334744a'
```

Use the actual commands above with only the final `postgres:18.4` token
replaced by `"$PHASE5_PG_IMAGE"` and each final
`golang:1.26.5-bookworm` token replaced by `"$PHASE5_GO_IMAGE"`. All other
arguments remain identical.

### Post-review cleanup

After both independent reviews reported Critical/Important/Minor `0/0/0`, the
temporary resources created for this gate were removed. The container cleanup
completed with exit 0 and printed both exact container names:

```text
$ docker rm -f vayzra-phase5-gate-019fa57b vayzra-phase5-gate-postgres-019fa57b
vayzra-phase5-gate-019fa57b
vayzra-phase5-gate-postgres-019fa57b
```

The named Go module/build cache cleanup also completed with exit 0 and printed
both exact volume names:

```text
$ docker volume rm vayzra-phase5-go-mod vayzra-phase5-go-build
vayzra-phase5-go-mod
vayzra-phase5-go-build
```

These were only this review's temporary in-memory PostgreSQL container, failed
container, and Go caches. A post-cleanup read-only check returned no rows for
the two Phase 5 container names or the two Phase 5 volume names. The existing
`vayzra-phase3-baseline-postgres` container remained present and running
(`Up 6 days`) and was not touched.

### Pre-success attempts not counted as passing gates

These attempts were diagnostic failures. None is represented by the PASS
results above:

1. Running the planned Go command directly in the host login shell exited 127
   with `go: command not found`.
2. The first Go container attempt used the test suite's default
   `127.0.0.1:54329` URL, which points back into that container, and failed
   with connection refused.
3. Pointing the container at `host.docker.internal:54329` reached a historical,
   partially migrated database and failed with `relation "ai_runs" does not
   exist`.

Only the complete normal and race reruns against the fresh PostgreSQL 18.4
database on `host.docker.internal:54330` are counted as passing Go gates.

## Security and correctness review

1. **The application never owns the Docker socket — confirmed.**
   The reviewed production `cmd`, `internal`, Dockerfile, and compose paths do
   not reference `docker.sock`, `DOCKER_HOST`, or a Docker client. Compose does
   not mount `/var/run/docker.sock`.

2. **Maintenance write exclusion is race-free — confirmed.**
   Unsafe HTTP requests acquire the PostgreSQL session-level shared advisory
   lock before checking durable `normal` mode and hold the lock through the
   complete handler. Lease acquisition takes the matching exclusive advisory
   lock before the row transition and retains its connection until release.
   Ordinary notification, AI, processing, and direct upload-cleanup claim
   paths acquire the transaction-level shared advisory lock and recheck durable
   mode inside the same transaction as the claim.

   The production upload-cleanup runner is intentionally different: it first
   acquires the outer session-level shared gate, then injects a package-private
   identity marker. `ClaimCleanup` recognizes that marker and skips only the
   duplicate `AdmitClaim`; the already-held outer lock remains live across the
   database cleanup and object settlement. This prevents the cleanup process
   from self-blocking on a second shared acquisition when an exclusive waiter
   is already queued, while still making the exclusive maintenance transition
   wait for the complete cleanup operation.

3. **Logout and safe health reads remain available — confirmed.**
   Liveness/readiness routes are mounted outside the gated API router. GET,
   HEAD, and OPTIONS bypass the gate, and only the exact
   `/api/v1/auth/logout` path is exempted among unsafe methods. Existing
   authentication, origin, and CSRF checks still apply.

4. **Stale lease owners cannot release or transition — confirmed.**
   Lease mutations require a live in-process session keyed by the SHA-256 hash
   of the 32-byte token, then compare both owner UUID and stored token hash
   under `FOR UPDATE`; transition also rejects an expired durable lease.
   Release uses the same owner/hash compare-and-swap and cannot clear a
   replacement owner's row. Recovery and takeover paths re-read authoritative
   state while holding the exclusive advisory lock.

5. **Audit DTOs expose only allowed metadata — confirmed.**
   The admin DTO reconstructs metadata from exactly `status`, `reason`,
   `version`, `count`, `provider_id`, `model_id`, and `file_purpose`. It applies
   per-key finite enums, canonical non-zero UUID validation, and bounded
   canonical integer validation. IP, request ID, payloads, credentials, object
   keys, filenames, prompts, responses, and unknown keys are not serialized.
   Actor and target identifiers are canonicalized or omitted. The web client
   repeats the safe projection before rendering.

6. **Settings contain no infrastructure secrets — confirmed.**
   The table, Go model/DTO, and TypeScript model contain only versioned site
   text, retention periods, backup clock/timezone, operational thresholds, and
   update provenance/time. They do not expose database/Redis/Object Store
   endpoints, credentials, keys, tokens, buckets, or Docker controls.

## Findings

- Critical: 0
- Important: 0
- Minor: 0

## Report amendment verification

- Markdown fenced-code structure — PASS; all 16 fence lines are balanced.
- Markdown trailing-whitespace scan — PASS; no matches.
- Secret/operator-path scan — PASS; no committed credential, private-key
  marker, cloud access-key pattern, operator-specific absolute path, or
  hard-coded PostgreSQL password was found. Only the documented
  `$PHASE5_*` placeholders remain.
- `git diff --check` — PASS after the reproducibility and cleanup-lock
  clarification.

## Known non-blocking advisory

Vite retains the existing informational chunk-size advisory: the main
JavaScript chunk is 710.62 kB before gzip (235.81 kB gzip), above the default
500 kB warning threshold. The build exits 0 and this foundation task does not
change the established bundling strategy.
