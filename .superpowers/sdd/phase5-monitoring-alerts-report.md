# Phase 5 Monitoring and Alerts Gate Report

Date: 2026-07-28

## Reviewed range

- Base: `8c90e06`
- Monitoring implementation tip: `3a75364`
- Reviewed implementation range: `8c90e06..3a75364`
- Task 9 also includes the gate remediations and this report in the final
  `test(operations): close monitoring and alerts gate` commit.

## Outcome

The complete monitoring gate passes after closing four Important production
defects across three remediation areas found during independent review:

1. every internal-listener response, including 404 and 503, now carries
   `Cache-Control: no-store`;
2. the development Compose deployment now publishes the internal listener only
   on `127.0.0.1:9090`; a networkless least-capability init service copies the
   metrics/HMAC sources into an app-only volume as uid/gid 10001, mode 0400
   files;
3. metadata retention now runs immediately and daily in production, reads the
   current configured sample-retention period on every run, and preserves
   delivery history while an alert is open or acknowledged.

## Fresh complete gate

### Backend

- Ordinary gate — PASS, exit 0, 44.30s:

  ```text
  go test -p 1 \
    ./internal/operations ./internal/platform/safehttp \
    ./internal/platform/secretfile ./internal/platform/safelog \
    ./internal/backup ./internal/aiqa ./internal/platform/config \
    ./internal/app ./cmd/server ./cmd/host-sampler ./cmd/worker \
    ./cmd/backup ./cmd/backup-retention ./cmd/maintenance -count=1
  ```

  Package times were: operations 20.524s, safehttp 0.730s, secretfile
  0.545s, safelog 0.585s, backup 5.250s, aiqa 5.646s, config 0.550s,
  app 0.562s, server 1.014s, host-sampler 0.377s, worker 0.595s, backup
  0.629s, backup-retention 0.597s, and maintenance 0.600s.

- Race gate — PASS, exit 0, no race reports, 56.96s:

  ```text
  go test -race -p 1 \
    ./internal/operations ./internal/platform/safehttp \
    ./internal/platform/safelog ./internal/aiqa ./internal/backup \
    ./internal/app ./cmd/server ./cmd/host-sampler -count=1
  ```

  Package times were: operations 24.908s, safehttp 1.755s, safelog 1.690s,
  aiqa 9.317s, backup 7.924s, app 1.620s, server 2.111s, and host-sampler
  1.431s.

- A focused post-remediation gate — PASS, exit 0, 3.89s:
  `go test -p 1 ./internal/operations ./cmd/server -run
  'Retention|InternalMetrics|InternalHostSamples' -count=1`;
  operations 1.157s and server 0.525s.
- Affected-package vet — PASS, exit 0, 0.41s. It covered all packages in the
  ordinary gate above.
- `gofmt -l` on every changed Go file — PASS with no output.

The Go gates used the dedicated PostgreSQL 18.4 test database published only
on loopback port 55440 by
`vayzra-monitoring-gate-pg-3a75364`. Sandboxed attempts could not open loopback
sockets and are not counted as product failures or passing gates; every reported
PASS is from the authorized isolated rerun.

### Frontend

- `pnpm test` — PASS; 64 files and 448 tests, Vitest 8.37s, wall 9.57s.
- `pnpm typecheck` — PASS; wall 2.69s.
- `pnpm lint` — PASS with `--max-warnings=0`; wall 5.21s.
- `pnpm build` — PASS; 268 modules, wall 2.79s.

The build retains the existing non-blocking chunk advisory: the main JavaScript
chunk is 752.25 kB before gzip and 247.61 kB after gzip.

### Shell, Compose, and repository checks

- `bash scripts/host-metrics_contract_test.sh` — PASS, exit 0, 6.5s. The fresh
  authorized run printed:

  ```text
  host metrics uid contract: PASS uid=10004 owner=10003 mode=0700 df=ok
  host metrics contract: PASS
  ```

  The contract renders both overridden and development-default Compose secret
  sources; validates the loopback-only 9090 publication, init copy,
  ownership/mode check, named volume, dependency, and container `_FILE` paths;
  and rejects deletion mutations for the port, either environment variable,
  either source mount, copy, owner, mode, app volume, or dependency.

- `bash scripts/ci-compose_contract_test.sh` — PASS, exit 0, 1.03s. The
  contract validates the exact eleven-service set, merged CI networking, host-port
  policy, and logging-driver/max-size/max-file mutations for every service.
- `bash scripts/ci-compose_contract_mutation_test.sh` — PASS, exit 0.
- `bash -n` for the collector, host contract, CI Compose contract, and CI
  Compose mutation contract scripts — PASS.
- `git diff --check` and `git diff --check 8c90e06..HEAD` — PASS.

The init-copy permission model also passed a fresh runtime probe under the
dedicated Compose project `vayzra-app-secrets-init-check`. The one-shot
`app-secrets-init` exited 0; a read-only volume probe verified directory
`10001:10001:500`, both files `10001:10001:400`, and byte-for-byte equality
with the two explicit development fixtures. It printed
`app secrets init live contract: PASS`. `compose down --volumes` then removed
the dedicated volume, a subsequent inspect found it absent, and the temporary
license fixture was removed.

## Privacy and operations review

1. **Metrics are aggregate and fixed-label only.** `Sample` validation uses
   fixed source, metric, scope, and unit enums; metric exposition derives only
   fixed label names. Unknown, UUID-like, slash-containing, whitespace, and
   duplicate series are rejected.
2. **Internal routes stay private.** Public handling rejects `/internal` and
   every `/internal/` child. The separate internal listener exposes only exact
   GET `/internal/metrics` and POST `/internal/host-samples` routes. All
   responses are `no-store`.
3. **Host payloads exclude Docker-sensitive fields.** The collector projects
   only allowlisted service state/restart and bounded CPU/memory/disk
   aggregates. Command, environment, mount, image, registry-auth, log,
   container-ID, and project data never reach the signed payload.
4. **Host authentication is replay-safe.** Authentication signs the exact raw
   body with timestamp and nonce, accepts only a ±90-second window, claims the
   nonce before insertion, and uses fixed canonical JSON with a 64 KiB limit.
5. **Webhook configuration is memory-only.** URL and authorization are loaded
   only from `_FILE` configuration, are neither persisted nor logged, and use
   the shared safe HTTP transport with public-address validation, DNS pinning,
   redirect rejection, bounded response size, and timeouts.
6. **Acknowledgement cannot resolve or suppress critical evaluation.**
   Acknowledgement changes only open to acknowledged; critical upgrades and
   deliveries continue, and only three healthy evaluations resolve an alert.
7. **Retention is bounded and immutable-audited.** Each invocation uses the
   single advisory-lock owner, deletes children before parents with one ordered
   1,000-row batch per table, and writes one count-only immutable audit event
   in the same transaction. Open/acknowledged alerts and their deliveries,
   nonterminal backup/restore rows, and all audit rows are preserved.
8. **Production logs are fixed-schema and redacted.** Request logs use escaped
   paths without query, cookies, headers, bodies, or client data. Server,
   worker, backup, backup-retention, maintenance, alert, webhook, and retention
   runtime callbacks use safe fixed-category logging and never pass raw errors.
9. **Container logs rotate everywhere.** All eleven services, including the
   one-shot app-secret initializer, inherit
   `json-file`, `max-size: 10m`, and `max-file: "5"`.

## Independent findings and remediation

The independent baseline review of `8c90e06..3a75364` reported
Critical/Important/Minor `0/6/1`.

| Finding | Resolution |
| --- | --- |
| Internal 404/503 responses lacked `no-store` | Fixed at the internal handler entry and covered for authorization failure, dependency failure, and replay. |
| Development host collector could not reach/configure the app internal listener | Fixed with loopback-only 9090, `_FILE` wiring, tracked development-only sources, a root/networkless/least-capability init-copy into an app-only volume, explicit uid/gid 10001 mode 0400 verification, override/default rendering, and ten deletion mutations. This avoids Docker Compose's documented inability to apply `uid/gid/mode` to file-source secrets. The production branch continues to fail closed while `deploy/compose.prod.yml` remains a Phase 6 deliverable. |
| Retention runner had no production invocation | Fixed with an immediate, non-overlapping daily scheduler, two-minute run timeout, current-settings read, fixed safe categories, idempotent waiting stop, and production lifecycle wiring before pool close. |
| Old deliveries could be deleted for open/acknowledged alerts | Fixed by joining the parent alert and requiring `state='resolved'`; the state-contrast PostgreSQL test preserves open/acknowledged rows and deletes the resolved row. |
| Reviewer interpreted the batch rule as 1,000 total rows per transaction | No code change. Task 8 Step 6 is the more specific rule: one ordered 1,000-row batch **per table per invocation**. The existing child-before-parent transaction and per-table boundary test implement that exact rule. |
| Task 9 command/Compose coverage was incomplete | Closed operationally by the expanded ordinary/vet command set and the exact Compose/host mutation gates recorded above. |
| Minor: safe-log production seam was primarily static | Strengthened for the new retention path with both a sensitive-canary runtime callback test and a production-source seam assertion. Existing command runtime tests and standard-logger import/output bans remain in force. |

Final independent post-remediation follow-up: Critical/Important/Minor
`0/0/0`; no residual findings.

## Resource ownership and cleanup

This gate created only the dedicated PostgreSQL container
`vayzra-monitoring-gate-pg-3a75364` and the task-specific Go build cache at
`/private/tmp/vayzra-monitoring-gocache`. They must be removed only after the
final review and commit. The shared database on port 54329 and unrelated
containers/worktrees were not modified or removed.
