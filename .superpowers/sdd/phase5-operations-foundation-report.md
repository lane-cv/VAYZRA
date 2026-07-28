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

The host login shell did not expose a `go` executable, so both Go gates were
executed with the locally cached official `golang:1.26.5-bookworm` image, a
read-only source mount, and a fresh temporary PostgreSQL 18.4 instance exposed
to the test container at `host.docker.internal:54330`. The package lists,
environment, serialization flag, race flag, and cache-bypass count match the
planned gates.

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
   Notification, AI, processing, and upload-cleanup claims acquire the
   transaction-level shared advisory lock and recheck durable mode inside the
   same transaction as the claim. Upload cleanup holds one outer shared gate
   through object settlement and uses a package-private identity marker to
   avoid self-blocking behind a queued exclusive waiter.

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

## Known non-blocking advisory

Vite retains the existing informational chunk-size advisory: the main
JavaScript chunk is 710.62 kB before gzip (235.81 kB gzip), above the default
500 kB warning threshold. The build exits 0 and this foundation task does not
change the established bundling strategy.
