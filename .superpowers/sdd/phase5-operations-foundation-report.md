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

### Reproducible Go gate environment

The host login shell did not expose a `go` executable, so both passing Go gates
used the following pinned local images:

- `golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651`
- `postgres:18.4@sha256:3a82e1f56c8f0f5616a11103ac3d47e632c3938698946a7ad26da0df1334744a`

The variables below are operator-local placeholders. The operator must set
them in the local shell; absolute host paths and the generated PostgreSQL
password are not repository data and must never be committed:

```sh
export PHASE5_SOURCE='<absolute path to the phase5-phase6 worktree>'
export PHASE5_GO_MOD_CACHE='<absolute path to a writable Go module cache>'
export PHASE5_GO_BUILD_CACHE='<absolute path to a writable Go build cache>'
export PHASE5_PG_CONTAINER='phase5-foundation-gate-pg'
export PHASE5_GO_IMAGE='golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651'
export PHASE5_PG_IMAGE='postgres:18.4@sha256:3a82e1f56c8f0f5616a11103ac3d47e632c3938698946a7ad26da0df1334744a'
read -r -s PHASE5_PG_PASSWORD
export PHASE5_PG_PASSWORD
export PHASE5_TEST_DATABASE_URL="postgres://happylearn:${PHASE5_PG_PASSWORD}@host.docker.internal:54330/happylearn?sslmode=disable"
```

A fresh PostgreSQL 18.4 container was initialized with a loopback-only host
port, explicit database/user, and a bounded health check:

```sh
docker run --detach --rm \
  --name "$PHASE5_PG_CONTAINER" \
  --publish 127.0.0.1:54330:5432 \
  --env POSTGRES_USER=happylearn \
  --env POSTGRES_PASSWORD="$PHASE5_PG_PASSWORD" \
  --env POSTGRES_DB=happylearn \
  --health-cmd 'pg_isready -U happylearn -d happylearn' \
  --health-interval 1s \
  --health-timeout 5s \
  --health-retries 30 \
  "$PHASE5_PG_IMAGE"

PHASE5_PG_HEALTH=''
for PHASE5_PG_ATTEMPT in $(seq 1 60); do
  PHASE5_PG_HEALTH="$(docker inspect \
    --format '{{.State.Health.Status}}' "$PHASE5_PG_CONTAINER")"
  [ "$PHASE5_PG_HEALTH" = healthy ] && break
  [ "$PHASE5_PG_HEALTH" = unhealthy ] && break
  sleep 1
done
test "$PHASE5_PG_HEALTH" = healthy
unset PHASE5_PG_ATTEMPT PHASE5_PG_HEALTH
```

The source was mounted read-only. Only the operator-local Go module and build
caches were writable. The successful non-race gate used this complete command:

```sh
docker run --rm \
  --mount "type=bind,source=$PHASE5_SOURCE,target=/src,readonly" \
  --mount "type=bind,source=$PHASE5_GO_MOD_CACHE,target=/go/pkg/mod" \
  --mount "type=bind,source=$PHASE5_GO_BUILD_CACHE,target=/root/.cache/go-build" \
  --workdir /src \
  --env GOENV=off \
  --env GOFLAGS= \
  --env HAPPYLEARN_TEST_DATABASE_URL="$PHASE5_TEST_DATABASE_URL" \
  "$PHASE5_GO_IMAGE" \
  go test -p 1 \
    ./internal/operations ./internal/audit ./internal/app \
    ./internal/notifications ./internal/aiqa ./internal/processing \
    ./cmd/server ./cmd/worker -count=1
```

The successful race gate used the same pinned images, fresh database, mounts,
and environment:

```sh
docker run --rm \
  --mount "type=bind,source=$PHASE5_SOURCE,target=/src,readonly" \
  --mount "type=bind,source=$PHASE5_GO_MOD_CACHE,target=/go/pkg/mod" \
  --mount "type=bind,source=$PHASE5_GO_BUILD_CACHE,target=/root/.cache/go-build" \
  --workdir /src \
  --env GOENV=off \
  --env GOFLAGS= \
  --env HAPPYLEARN_TEST_DATABASE_URL="$PHASE5_TEST_DATABASE_URL" \
  "$PHASE5_GO_IMAGE" \
  go test -race -p 1 \
    ./internal/operations ./internal/audit ./internal/app \
    ./internal/notifications ./internal/aiqa ./internal/processing \
    ./cmd/server ./cmd/worker -count=1
```

The disposable database can be removed after both gates:

```sh
docker stop "$PHASE5_PG_CONTAINER"
unset PHASE5_PG_PASSWORD PHASE5_TEST_DATABASE_URL
```

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

- Markdown fenced-code structure — PASS; all 10 fence lines are balanced.
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
