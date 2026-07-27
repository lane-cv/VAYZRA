# Phase 6 Production Stack Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a digest-pinned, secret-file-driven, resource-bounded production Compose and Caddy stack whose public surface, filesystem policy, readiness metadata, and merged configuration are enforced by mutation-tested contracts.

**Architecture:** Keep Caddy as the only public service and place every application and data service on one internal network. Compose consumes immutable image references and owner-only secret files; Go processes resolve supported `_FILE` variables before validation. Public liveness stays minimal, while the private listener exposes authenticated release and schema readiness.

**Tech Stack:** Docker Engine and Compose, Caddy, Go, PostgreSQL 18.4, Redis, AIStor, Bash, jq, yq.

---

## File structure

- Create `deploy/compose.prod.yml`, `deploy/compose.prod.local.yml`.
- Create `deploy/Caddyfile`, `deploy/Caddyfile.maintenance`,
  `deploy/Caddyfile.local`.
- Create `deploy/maintenance.html`, `deploy/production.env.example`,
  `deploy/secrets/README.md`.
- Modify `internal/platform/secretfile/secretfile.go`,
  `internal/platform/secretfile/secretfile_test.go`.
- Modify `internal/platform/config/config.go`,
  `internal/platform/config/config_test.go`.
- Create `internal/platform/buildinfo/buildinfo.go`,
  `internal/platform/buildinfo/buildinfo_test.go`.
- Modify `internal/operations/internal_http.go`,
  `internal/operations/internal_http_test.go`,
  `internal/app/app_test.go`, `cmd/server/main.go`, `cmd/worker/main.go`,
  `cmd/backup/main.go`, `cmd/backup/main_test.go`.
- Create `scripts/phase6-production_contract_test.sh`,
  `scripts/phase6-production_contract_mutation_test.sh`.
- Modify `Makefile`, `package.json`, `.gitignore`.

### Task 1: Define production configuration contracts

- [ ] **Step 1: Write the failing merged-Compose contract**

Create `scripts/phase6-production_contract_test.sh`. It must render the model
with a temporary, owner-only environment and secret directory, then assert:

```text
project name: happylearn-prod
long-running services: caddy app worker postgres redis minio
one-shot services: migrate backup restore acceptance
networks: edge private
private.internal: true
published ports: caddy 80/tcp and 443/tcp only
optional internal listener: 127.0.0.1 only
privileged containers: none
host network: none
Docker socket mounts: none
floating latest tags: none
every image value: name@sha256:<64 lowercase hexadecimal characters>
every long-running service: healthcheck, restart policy, resource limit
app and worker: read_only, non-root, cap_drop ALL, no-new-privileges
logs: json-file, max-size 10m, max-file 5
secret values inside environment blocks: none
```

Use `docker compose -p happylearn-prod --env-file "$env_file" -f
deploy/compose.prod.yml config --format json` and parse the JSON with `jq`.
The fixture image digests are 64 zero-to-nine hexadecimal characters and are
never pulled.

The contract supports
`HAPPYLEARN_PHASE6_CONTRACT_SCOPE=all|compose|caddy`; `all` is the default.
The focused scopes keep the vertical slices independently green without
skipping either category in the aggregate gate.

- [ ] **Step 2: Write the failing Caddy contract**

The same script must validate all three active Caddy configurations:

- `/internal/*` returns a local 404 and is never proxied;
- HTTP redirects to HTTPS;
- maintenance mode serves `maintenance.html` with status 503 and
  `Retry-After`;
- normal mode proxies only the application routes;
- request body limits are explicit for upload and default routes;
- SSE routes disable response buffering and keep a longer upstream timeout;
- query strings, cookies, authorization, request bodies, and upstream secret
  headers are absent from the access-log format;
- security headers include content type, frame ancestors, referrer,
  permissions, and the project CSP;
- public HSTS is present only in `Caddyfile`, not the local-TLS file.

- [ ] **Step 3: Run and verify RED**

```bash
bash scripts/phase6-production_contract_test.sh
```

Expected: FAIL because the production files do not exist.

- [ ] **Step 4: Add mutation coverage**

Create `scripts/phase6-production_contract_mutation_test.sh`. For each mutation,
copy the production files to a temporary directory, make exactly one unsafe
change, and require the main contract to fail:

```text
publish PostgreSQL
remove private network internal flag
replace one digest with latest
mount the Docker socket
remove app read_only
remove cap_drop
add a literal password environment value
increase resource totals above the host ceiling
proxy /internal/*
log the request URI including query
remove the upload limit
remove maintenance Retry-After
```

The script must preserve the first unexpected result and delete only its
validated temporary directory.

- [ ] **Step 5: Commit the RED contracts**

```bash
git add scripts/phase6-production_contract_test.sh \
  scripts/phase6-production_contract_mutation_test.sh
git commit -m "test(phase6): define production stack contracts"
```

### Task 2: Resolve secret files before configuration validation

- [ ] **Step 1: Write failing secret-file tests**

Extend `internal/platform/secretfile/secretfile_test.go` for:

```go
func TestResolveReturnsDirectValue(t *testing.T)
func TestResolveReadsTrimmedRegularFile(t *testing.T)
func TestResolveRejectsDirectAndFileTogether(t *testing.T)
func TestResolveRejectsSymlink(t *testing.T)
func TestResolveRejectsEmptyFile(t *testing.T)
func TestResolveRejectsOversizedFile(t *testing.T)
func TestResolveRejectsGroupOrWorldWritableFile(t *testing.T)
func TestResolveDoesNotIncludeValueOrPathInError(t *testing.T)
```

The public API is:

```go
type Lookup func(string) (string, bool)

func Resolve(lookup Lookup, name string, maxBytes int64) (string, error)
```

`Resolve` checks `NAME` and `NAME_FILE`, requires mutual exclusion, opens with
`O_NOFOLLOW`, verifies a regular file owned by the current effective user or
root, rejects group/world write, reads at most `maxBytes+1`, trims one terminal
line ending, and returns category-only errors.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/platform/secretfile
```

Expected: FAIL because the package is absent.

- [ ] **Step 3: Implement the resolver**

Use `unix.Open` plus `unix.Fstat` so validation applies to the opened file
descriptor rather than a race-prone path lookup. Keep the default maximum at
64 KiB in the caller; use smaller explicit limits for tokens and passwords.

- [ ] **Step 4: Add config integration tests**

Extend `internal/platform/config/config_test.go` to cover `_FILE` support for
the application and worker settings:

```text
HAPPYLEARN_DATABASE_URL
HAPPYLEARN_REDIS_URL
HAPPYLEARN_MINIO_ACCESS_KEY
HAPPYLEARN_MINIO_SECRET_KEY
HAPPYLEARN_LOGIN_THROTTLE_SECRET
HAPPYLEARN_AI_MASTER_KEY
HAPPYLEARN_METRICS_BEARER_SECRET
HAPPYLEARN_HOST_METRICS_HMAC_SECRET
HAPPYLEARN_WEBHOOK_URL
HAPPYLEARN_WEBHOOK_AUTHORIZATION
```

For each item prove development direct value, file value, mutual exclusion,
empty file, and redacted error behavior. Production mode rejects direct secret
values and requires the corresponding `_FILE` variable. Non-secret settings
such as public origin, timezone, image references, log level, and limits remain
ordinary environment values.

Extend `cmd/backup/main_test.go` with the same production-only file rule for
`HAPPYLEARN_BACKUP_PASSWORD` and `HAPPYLEARN_BACKUP_AGE_IDENTITY`. The backup
command uses `secretfile.Resolve` directly and does not add backup secrets to
the application `config.Config`.

- [ ] **Step 5: Integrate and verify**

Modify `config.Load` to resolve supported secrets first and pass the resolved
values into existing parsers. Do not add secret values to `Config.String`,
logs, health responses, metrics, or validation errors.

```bash
go test ./internal/platform/secretfile ./internal/platform/config
go test ./cmd/server ./cmd/worker ./cmd/backup
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/secretfile internal/platform/config \
  cmd/server cmd/worker cmd/backup
git commit -m "feat(phase6): load production secrets from files"
```

### Task 3: Add private release readiness metadata

- [ ] **Step 1: Write failing build-info tests**

Create `internal/platform/buildinfo/buildinfo_test.go`:

```go
func TestParseRequiresSemanticVersionAndCommit(t *testing.T)
func TestParseRejectsInvalidSchemaInterval(t *testing.T)
func TestPublicLiveOmitsBuildAndSchema(t *testing.T)
func TestInternalReadyIncludesSafeReleaseMetadata(t *testing.T)
func TestInternalReadyRejectsMissingBearerToken(t *testing.T)
```

Use:

```go
type Info struct {
    Version          string `json:"version"`
    Commit           string `json:"commit"`
    BuiltAt          string `json:"builtAt"`
    MinSchemaVersion int64  `json:"minSchemaVersion"`
    MaxSchemaVersion int64  `json:"maxSchemaVersion"`
}
```

Build values arrive through linker flags. Parsing rejects an empty version,
non-semantic version, non-hex commit, invalid RFC3339 timestamp, negative
schema bounds, or `min > max`.

- [ ] **Step 2: Run and verify RED**

```bash
go test ./internal/platform/buildinfo ./internal/operations \
  ./internal/app ./cmd/server
```

Expected: FAIL because build info and the internal endpoint are absent.

- [ ] **Step 3: Implement build info and readiness**

Add `GET /internal/readiness` to the Phase 5 operations internal router only;
do not register it in `app.New`. Require the existing metrics bearer token,
query the current migration version, and return:

```json
{
  "status": "ready",
  "version": "0.0.0-phase6.test",
  "commit": "0123456789abcdef",
  "schemaVersion": 21,
  "minSchemaVersion": 21,
  "maxSchemaVersion": 22
}
```

Omit build time and dependency addresses from the response. Return 503 if the
database is unreachable or the schema lies outside the declared interval.
Keep public `/health/live` as `{"status":"ok"}`.

- [ ] **Step 4: Wire linker values**

Add variables to `cmd/server` and `cmd/worker`, pass them to
`buildinfo.Parse`, and fail startup before binding when metadata is invalid in
production mode. Development and test mode use the explicit version
`0.0.0-dev`, commit `0000000`, and a test schema interval.

- [ ] **Step 5: Verify**

```bash
go test ./internal/platform/buildinfo ./internal/operations \
  ./internal/app ./cmd/server ./cmd/worker
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/buildinfo internal/operations internal/app \
  cmd/server cmd/worker
git commit -m "feat(phase6): expose private release readiness"
```

### Task 4: Implement the hardened Compose topology

- [ ] **Step 1: Create safe examples**

`deploy/production.env.example` contains names and non-secret examples only:

```dotenv
COMPOSE_PROJECT_NAME=happylearn-prod
HAPPYLEARN_DOMAIN=learn.example.invalid
HAPPYLEARN_TIMEZONE=Asia/Shanghai
HAPPYLEARN_APP_IMAGE=registry.example.invalid/happylearn/app@sha256:0000000000000000000000000000000000000000000000000000000000000000
HAPPYLEARN_WORKER_IMAGE=registry.example.invalid/happylearn/worker@sha256:0000000000000000000000000000000000000000000000000000000000000000
HAPPYLEARN_MIGRATE_IMAGE=registry.example.invalid/happylearn/migrate@sha256:0000000000000000000000000000000000000000000000000000000000000000
HAPPYLEARN_BACKUP_IMAGE=registry.example.invalid/happylearn/backup@sha256:0000000000000000000000000000000000000000000000000000000000000000
HAPPYLEARN_CADDY_IMAGE=caddy@sha256:0000000000000000000000000000000000000000000000000000000000000000
HAPPYLEARN_POSTGRES_IMAGE=postgres@sha256:0000000000000000000000000000000000000000000000000000000000000000
HAPPYLEARN_REDIS_IMAGE=redis@sha256:0000000000000000000000000000000000000000000000000000000000000000
HAPPYLEARN_MINIO_IMAGE=quay.io/minio/aistor/minio@sha256:0000000000000000000000000000000000000000000000000000000000000000
HAPPYLEARN_BACKUP_HOST_PATH=/srv/happylearn/backups
HAPPYLEARN_RELEASE_STATE_PATH=/srv/happylearn/releases
```

`deploy/secrets/README.md` lists exact filenames, consuming services, maximum
sizes, owner/mode `0600`, creation procedure, rotation owner, and the rule that
example secret values never enter Git.

- [ ] **Step 2: Create the production stack**

Implement the service graph from the approved design. Use required-variable
expansion for every image and deployment path. Give long-running services
these hard limits:

| Service | Memory | CPU |
|---|---:|---:|
| PostgreSQL | 448 MiB | 0.25 |
| Redis | 96 MiB | 0.10 |
| AIStor | 448 MiB | 0.25 |
| app | 320 MiB | 0.25 |
| worker | 1664 MiB | 0.90 |
| Caddy | 96 MiB | 0.10 |
| **Steady total** | **3072 MiB** | **1.85** |

The one-shot backup limit is 384 MiB and 0.15 CPU. Compose profiles and the
host release script must stop the worker before backup, so the backup peak is
1792 MiB and 1.10 CPU. This leaves at least 1 GiB memory and 0.15 CPU outside
container hard limits on the 2 CPU/4 GiB target.

Use dedicated volumes for database, object data, Redis, Caddy data/config, and
application state. Bind the backup and release-state directories only into the
one-shot services that require them. Do not mount the Docker socket.

- [ ] **Step 3: Apply runtime hardening**

For every applicable service set:

```yaml
read_only: true
security_opt:
  - no-new-privileges:true
cap_drop:
  - ALL
```

Use size-bounded `tmpfs` mounts with `noexec,nosuid,nodev`, named non-root
users, init processes, health checks, bounded stop grace periods, and the
required log rotation. Give Caddy only the narrowly required networking
capability when its chosen image user cannot bind 80/443.

- [ ] **Step 4: Add local overrides**

`deploy/compose.prod.local.yml` changes only:

- local image tags built by the acceptance harness;
- loopback high ports for Caddy;
- local license and generated secret paths;
- disposable bind/volume paths;
- `deploy/Caddyfile.local`;
- local-TLS disposable hostname.

It must not relax private networks, container users, read-only roots,
capabilities, secret files, health checks, logging, or resource limits.

- [ ] **Step 5: Run the Compose contract**

```bash
HAPPYLEARN_PHASE6_CONTRACT_SCOPE=compose \
  bash scripts/phase6-production_contract_test.sh
```

Expected: the complete Compose topology passes before Caddy policy is added.

- [ ] **Step 6: Commit**

```bash
git add deploy/compose.prod.yml deploy/compose.prod.local.yml \
  deploy/production.env.example deploy/secrets/README.md
git commit -m "feat(phase6): add hardened production compose stack"
```

### Task 5: Implement normal, maintenance, and local Caddy policies

- [ ] **Step 1: Create the maintenance asset**

`deploy/maintenance.html` is self-contained, contains no external resource,
script, form, user identifier, or release detail, and tells users to retry
later. Keep it below 16 KiB.

- [ ] **Step 2: Implement normal public policy**

`deploy/Caddyfile` must:

- serve the configured domain and redirect HTTP to HTTPS;
- terminate `/internal/*` with 404 before all proxy matchers;
- enforce the approved CSP and other security headers;
- cap ordinary request bodies at 2 MiB and upload-part routes at 9 MiB; the
  latter admits the application's exact 8 MiB part plus protocol framing while
  the application remains the authoritative size/hash validator;
- preserve AI SSE without buffering;
- proxy only to `app:8080`;
- enable HSTS after public-domain validation;
- emit JSON access logs containing method, sanitized path, status, duration,
  response size, request ID, and masked remote network prefix;
- exclude URI query, headers, cookies, request/response bodies, and upstream
  secret material.

- [ ] **Step 3: Implement maintenance policy**

`deploy/Caddyfile.maintenance` preserves TLS and the same security/logging
policy, serves `maintenance.html` with 503 and a bounded `Retry-After`, and
never proxies to app. Health probes for Caddy itself remain available only
inside the Compose network.

- [ ] **Step 4: Implement local TLS policy**

`deploy/Caddyfile.local` mirrors normal behavior with `tls internal`, no HSTS,
and the harness-provided disposable hostname. The local root certificate is
exported only into the disposable browser trust store.

- [ ] **Step 5: Run Caddy validation and contracts**

```bash
bash scripts/phase6-production_contract_test.sh
bash scripts/phase6-production_contract_mutation_test.sh
```

Expected: the contract creates its owner-only fixture, invokes Caddy
`validate --config /etc/caddy/Caddyfile` with the image entrypoint intact, and
all normal, maintenance, local-TLS, and mutation cases pass.

- [ ] **Step 6: Commit**

```bash
git add deploy/Caddyfile deploy/Caddyfile.maintenance \
  deploy/Caddyfile.local deploy/maintenance.html
git commit -m "feat(phase6): add production edge policies"
```

### Task 6: Integrate verification targets and review the slice

- [ ] **Step 1: Add stable targets**

Add:

```make
phase6-production-contracts:
	bash scripts/phase6-production_contract_test.sh
	bash scripts/phase6-production_contract_mutation_test.sh
```

Expose the Make target through `package.json` and include it in the repository
contract aggregate.

- [ ] **Step 2: Ignore only generated local state**

Add exact entries for rendered production environment files, generated secret
files, local certificate material, and release-state fixtures. Do not ignore
the example environment, secret manifest, Caddy files, or contracts.

- [ ] **Step 3: Run the focused gate**

```bash
go test ./internal/platform/... ./internal/app/... ./cmd/server ./cmd/worker
make phase6-production-contracts
git diff --check
```

Expected: PASS.

- [ ] **Step 4: Review against the Phase 6 design**

Inspect the merged Compose model, not only YAML source. Confirm the service
list, public port set, internal network, digest references, secrets, filesystem
policy, resource arithmetic, health checks, log policy, Caddy route ordering,
maintenance isolation, and internal readiness boundary.

- [ ] **Step 5: Commit review fixes**

```bash
git add deploy internal/platform internal/app cmd scripts Makefile \
  package.json .gitignore
git commit -m "chore(phase6): harden production stack contracts"
```
