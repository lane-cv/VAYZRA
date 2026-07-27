# HappyLearn Phase 6 Production Release and Acceptance Design

Date: 2026-07-28
Status: Approved for implementation planning

## 1. Purpose and delivery boundary

Phase 6 converts the Phase 1–5 application into a reproducible single-host
production release. It adds hardened production Compose and Caddy
configuration, immutable-image release metadata, owner-only secrets, host
preflight checks, maintenance-window deployment, migration, smoke checks,
automatic image rollback, destructive-restore safeguards, and full local
production-grade acceptance.

The user selected a two-step delivery:

1. Complete and validate all repository-owned production assets now.
2. Perform real Ubuntu server, domain, TLS, firewall, reboot, observation, and
   final recovery acceptance after server access is provided.

The repository-ready gate may produce a `v1.0.0-rc.1` candidate. The project
must not claim final Phase 6 completion or create `v1.0.0` until the real-server
gate passes.

## 2. Confirmed deployment decisions

- Target: Ubuntu 24.04, 2 CPU, 4 GB RAM, one host, Docker Engine and Compose.
- Release style: planned maintenance window, not blue/green or Kubernetes.
- Caddy is the only public container and opens only TCP 80 and 443.
- PostgreSQL, Redis, AIStor, app internals, metrics, and administration ports
  remain private or loopback-only.
- Every release requires a successful verified pre-release backup.
- Database migrations use expand-and-contract compatibility.
- Failed application rollout switches to the previously verified immutable
  image set; it does not automatically run destructive down migrations.
- Destructive restore is a separate, explicit operator workflow.

## 3. Production topology

The production Compose project name is `happylearn-prod`. The file set is
stable and every command uses the same project name and files.

### 3.1 Long-running services

- `caddy`: public TLS termination, maintenance response, reverse proxy,
  response headers, upload limits, and sanitized access logs.
- `app`: public business API and Vue static assets on the private network.
- `worker`: file processing, notification outbox, cleanup, and AI execution.
- `postgres`: durable business and operational data.
- `redis`: ephemeral coordination only.
- `minio`: AIStor object storage with versioning and private console/API.

### 3.2 One-shot services

- `migrate`: validates current schema compatibility and applies forward
  migrations exactly once.
- `backup`: creates or verifies Phase 5 recovery points.
- `restore`: restores only to an explicitly named, empty target.
- `acceptance`: runs internal readiness and safe smoke checks.

The backup service runs only while the worker is drained and stopped. The
configured and live resource gates treat that mutual exclusion as a required
invariant.

### 3.3 Networks and ports

- `edge`: Caddy's external network.
- `private`: an internal Docker network shared by Caddy, app, worker, and data
  services.
- Host ports 80 and 443 map only to Caddy.
- The internal metrics/host-ingestion listener may bind one loopback-only host
  port. It is not available on a non-loopback interface.
- PostgreSQL, Redis, AIStor API, and AIStor console have no production host
  port mapping.

Contract tests inspect the merged Compose model and reject additional public
ports, non-internal data networks, privileged containers, Docker socket mounts,
or secret values embedded in environment blocks.

## 4. Images and release metadata

Application, worker, migration, backup, Caddy, PostgreSQL, Redis, and AIStor
images are referenced by immutable digest in the resolved production
configuration. Floating `latest` references are forbidden.

Each release manifest contains:

- semantic version;
- Git commit SHA;
- build time;
- app and worker image digests;
- migration image digest;
- minimum and maximum compatible schema versions;
- Compose and Caddy configuration hashes;
- Phase 5 backup/restore evidence ID;
- creation actor and timestamp.

The active and previous manifests are stored in an owner-only release state
directory. A manifest contains identifiers and hashes only, not environment
values or secret paths.

The app health response includes semantic version, commit, and schema version
only on the internal readiness endpoint. The public live endpoint remains
minimal.

## 5. Secret and filesystem model

### 5.1 Configuration

An owner-only production environment file contains non-secret deployment
values such as domain, timezone, image references, and resource limits. Secret
material uses Docker Compose secrets backed by owner-only files:

- PostgreSQL password;
- Redis authentication material when enabled;
- AIStor access and secret keys;
- AIStor license;
- login-throttle secret;
- AI master key and key version metadata;
- initial admin password input;
- internal metrics bearer token;
- host-ingestion HMAC key;
- backup repository credentials;
- optional S3 repository credentials;
- optional webhook URL and authorization material;
- Caddy DNS provider credentials if a DNS challenge is later selected.

Preflight accepts secret values only through files. It verifies regular-file
type, ownership, no group/world write, maximum expected size, and non-empty
content without printing content.

### 5.2 Runtime filesystems

- Application and worker roots are read-only.
- Temporary paths are size-bounded tmpfs with `noexec`, `nosuid`, and
  service-specific UID/GID.
- Every container drops all capabilities unless Caddy requires a narrowly
  documented bind capability.
- `no-new-privileges` is enabled.
- Persistent volumes are dedicated by responsibility.
- Backup repositories use a host path with owner-only permissions so an
  operator can copy or inspect repository metadata during disaster recovery.
- Container logs rotate at 10 MiB with five files.

## 6. Caddy behavior

Caddy:

- obtains and renews TLS certificates for the configured domain;
- redirects HTTP to HTTPS;
- proxies only approved application paths;
- returns 404 for `/internal/*`;
- applies HSTS after successful real-domain validation;
- sets content-type, frame, referrer, permissions, and CSP headers compatible
  with the existing Markdown, KaTeX, file preview, and restricted external
  video contracts;
- applies explicit request-body limits by route;
- uses bounded upstream timeouts while preserving AI SSE;
- serves a static maintenance response when the release state is not `normal`;
- logs method, sanitized path without query string, status, duration, response
  size, request ID, and remote network prefix;
- never logs cookies, authorization, signed query parameters, request bodies,
  or upstream secret headers.

The local production harness uses Caddy's local TLS issuer and a disposable
hostname. The real server gate uses the public issuer after DNS is verified.

## 7. Host preflight

The read-only preflight command fails before any production mutation unless all
of the following pass:

- Ubuntu version is supported.
- Docker Engine and Compose meet the pinned minimum versions.
- Exactly the expected deployment user and project directory own the files.
- CPU is at least 2 cores and memory is at least 4 GiB.
- Root, Docker, application data, and backup filesystems meet configured free
  space thresholds.
- System time and `Asia/Shanghai` timezone behavior are valid.
- Required ports are either unused or owned by the current production Caddy.
- Domain A/AAAA resolution points to the intended host during the real-server
  stage.
- Every secret file passes section 5 checks.
- The production Compose and Caddy configurations validate.
- Every referenced image contains a digest and can be pulled.
- Release manifest hashes match the checked-out configuration.
- The previous release manifest, if present, remains available.
- A verified local Phase 5 recovery point no more than 24 hours old exists.

The command emits only pass/fail categories and safe remediation text.

## 8. Maintenance-window release protocol

The release command requires an explicit semantic version and release manifest.
It obtains an exclusive host file lock and then:

1. Runs host preflight.
2. Creates and verifies a `pre_release` backup.
3. Confirms the backup ID and manifest hash in release state.
4. Enters Phase 5 `release` operational mode.
5. Switches Caddy to the maintenance response.
6. Stops new public writes and new worker claims.
7. Waits up to ten minutes for active work to drain.
8. Pulls every immutable image from the approved release manifest.
9. Runs schema compatibility validation.
10. Runs the one-shot migration service.
11. Starts the new app and worker without opening public traffic.
12. Waits for application, worker, PostgreSQL, Redis, and AIStor health.
13. Runs safe internal smoke checks:
    - schema version;
    - admin login challenge availability without authenticating;
    - one database read;
    - one Redis round trip;
    - one authenticated AIStor list operation;
    - static asset delivery;
    - internal metrics authentication and forbidden public access.
14. Marks the new manifest active and the former active manifest previous.
15. Returns operational mode to `normal`.
16. Removes the maintenance response.
17. Records the release result and sanitized evidence.

Every state transition is durable and idempotent. Re-running after interruption
continues from the last proven step or fails safe when operator judgement is
required.

## 9. Rollback

Rollback is automatic only for a failure after migration has started and before
public traffic is reopened, and only when the previous manifest declares
compatibility with the new schema version.

The rollback path:

1. Keeps Caddy in maintenance mode.
2. Captures bounded sanitized service status and log tails.
3. Stops the failed app and worker.
4. Starts the previous image digests.
5. verifies dependency and application readiness;
6. runs the same safe smoke checks;
7. marks the release `rolled_back`;
8. returns operational mode to `normal`;
9. reopens traffic.

No automatic rollback:

- runs a down migration;
- restores the pre-release database;
- deletes new rows or objects;
- removes the failed image or diagnostic evidence;
- changes DNS or TLS configuration.

If the previous image is not schema-compatible or does not become healthy, the
system remains in an explicit `failed_safe` maintenance state. The command
prints the safe runbook path and trace ID.

## 10. Migration compatibility

- Forward migrations are additive within a release: new nullable columns,
  tables, indexes, and dual-read/dual-write transitions.
- Destructive column or table removal occurs no earlier than the next
  production release after all running code no longer depends on it.
- The migration service holds a PostgreSQL advisory lock.
- The release manifest declares its schema compatibility interval.
- Preflight rejects an app/worker image set whose intervals do not overlap.
- Migration failure rolls back its own database transaction where PostgreSQL
  permits transactional DDL and leaves traffic in maintenance.

## 11. Destructive restore

Restore is never part of automatic image rollback. The operator invokes a
separate command with:

- exact target project name;
- exact verified backup UUID;
- explicit destructive flag;
- typed confirmation containing the target project and backup UUID.

The command rejects the production target while Caddy is serving traffic. It
requires maintenance mode, a second current recovery point, empty replacement
volumes, and successful repository integrity verification.

Restore occurs into new volumes. The restored stack passes the Phase 5 restore
checks, all restored sessions are revoked, and only then may the operator switch
the production volume mapping. Original volumes remain detached and
recoverable until a separately approved retention action.

## 12. Host timers and service units

Repository-owned systemd templates provide:

- one-minute host metrics sampling;
- one-minute backup dispatch; the Phase 5 database schedule decides whether the
  default 03:00 Asia/Shanghai daily run or a queued manual run is due, using the
  local date as its idempotency key;
- hourly retry of queued manual or degraded remote backup work;
- daily retention cleanup;
- quarterly restore-verification reminder and runner;
- Docker/Compose service startup after host reboot.

Installation is an explicit operator command that renders templates with the
approved absolute project path. It shows the target files before writing during
the real-server stage. Repository tests verify unit hardening, absolute paths,
project-name consistency, time-zone semantics, and secret-free command lines.

## 13. Local production acceptance

The repository-ready gate creates a unique disposable production project with:

- production Compose and Caddy files;
- local TLS issuer and disposable hostname;
- owner-only generated test secrets;
- real PostgreSQL, Redis, and licensed AIStor;
- app, worker, migration, backup, and acceptance images;
- isolated volumes, networks, ports, and artifacts.

It executes:

1. Clean installation and first admin bootstrap.
2. Every Phase 1–5 browser group on desktop Chromium.
3. The established mobile acceptance paths.
4. Cross-student and admin-route authorization probes.
5. Teaching publication and secure file preview/download behavior.
6. Teacher Q&A, notifications, AI streaming, quota, and usage accounting.
7. Operations dashboard, settings, alert, audit, backup, and restore evidence.
8. Local encrypted backup and empty-environment recovery.
9. Release of a second image set.
10. Injected readiness failure and automatic previous-image rollback.
11. Container restart and host-project restart behavior.
12. Caddy TLS, security header, upload limit, SSE, internal-route denial, and
    query-log sanitization.
13. Dependency, image, and reachable-code vulnerability checks.
14. Thirty-minute steady-state and one live resource capture during the
    heaviest allowed workload.
15. Complete cleanup and proof that no project container, network, temporary
    volume, image, secret, or unsanitized artifact remains.

## 14. Real-server acceptance

After the user provides the server and domain, the final gate additionally
requires:

- read-only host inventory and preflight;
- explicit approval before installation changes;
- DNS and public TLS validation;
- firewall proof that only approved public ports are reachable;
- first production backup and restore into an isolated empty project;
- measured production recovery completes within the four-hour RTO;
- server reboot and automatic healthy recovery;
- desktop and mobile acceptance over the real domain;
- CPU, memory, disk, restart, and latency observation under representative
  workload;
- backup timer and alert delivery observation;
- rollback rehearsal within the maintenance window;
- a post-release observation period with no unresolved Critical alert.

Only after these checks pass may `v1.0.0` be created and Phase 6 be marked
complete.

## 15. Failure handling and evidence

- Shell commands use strict mode, explicit paths, exact project names, bounded
  timeouts, and traps that preserve the last durable state.
- Destructive targets are validated before mutation and never derive from broad
  globs or unresolved variables.
- Diagnostics include Compose status, health, bounded log tails, release step,
  trace ID, and configuration hashes.
- Diagnostics exclude environment dumps, Docker inspect payloads, secret paths
  whose names reveal credentials, database row bodies, query strings, cookies,
  authorization, object keys, and file contents.
- Uploadable CI artifacts are confined to the canonical `test-results` root and
  pass the existing fail-closed sanitizer.

## 16. Testing strategy

### 16.1 Contracts

- Production Compose topology, networks, ports, users, read-only roots,
  capabilities, secrets, health checks, logging, resources, and digests.
- Caddy public routes, internal denial, TLS, headers, body limits, SSE, and
  sanitized logging.
- Release-manifest schema and digest enforcement.
- Systemd project paths, project names, schedules, and secret-free commands.
- Shell syntax, mutation tests, interruption traps, and cleanup symmetry.

### 16.2 Release failure matrix

- missing or unsafe secret file;
- insufficient disk;
- invalid or unavailable image digest;
- failed pre-release backup;
- drain timeout;
- migration lock conflict;
- migration failure;
- app readiness failure;
- worker readiness failure;
- AIStor restart failure;
- smoke-check failure;
- signal interruption at every durable step;
- previous-image success;
- previous-image incompatibility;
- previous-image readiness failure.

### 16.3 Security and recovery

- public port scan and private-service non-enumeration;
- HTTP security baseline;
- secret and PII scans across logs, metrics, configs, artifacts, and image
  history;
- container image and dependency scanning;
- destructive restore target and confirmation rejection tests;
- wrong-key, tampered-repository, missing-object, and session-resurrection
  restore tests;
- authorization and CSRF regressions after restore and rollback.

### 16.4 Resources

The production configuration must leave host headroom rather than allocating
all 4 GiB to containers. App, worker, database, Redis, AIStor, and Caddy steady
limits plus Docker overhead are validated. Backup runs only while worker work is
drained and the worker is stopped. Both configured arithmetic and live capture
must prove that the mutually exclusive peak remains within 2 CPU and 4 GB.

## 17. Completion gates

### 17.1 Repository-ready gate

The repository portion of Phase 6 is complete when:

1. All production configuration, scripts, commands, templates, and runbooks are
   implemented.
2. Phase 5 remains green.
3. Backend, frontend, contract, security, recovery, and vulnerability gates
   pass.
4. The disposable local production acceptance, release, injected failure,
   rollback, backup, and empty restore pass.
5. Desktop, mobile, resource, restart, and cleanup evidence pass.
6. Complete-diff review has no open Critical or Important finding.
7. The repository is clean and the release-candidate evidence is recorded.

At this point `v1.0.0-rc.1` may be prepared, subject to explicit release
authorization.

### 17.2 Final Phase 6 gate

Final completion additionally requires every real-server check in section 14.
Until then, project status is “Phase 6 repository production-ready; real-server
acceptance pending.” The final `v1.0.0` tag is not created early.
