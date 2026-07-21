# HappyLearn local development and Ubuntu container runbook

This phase produces a same-origin Go/API and Vue-console image plus a serial file-processing worker. It is intended for the user's Ubuntu 24.04 Docker host behind a TLS-terminating reverse proxy. PostgreSQL, Redis, AIStor S3, and the worker health endpoint remain private to the Docker network. This runbook does not deploy to a remote server.

## Shell requirement

All commands in this runbook are Bash commands. On Windows, run them from WSL2 or Git Bash; native PowerShell is not supported by these snippets.
## Prerequisites

- Docker Engine with the Compose plugin (Ubuntu 24.04 or Docker Desktop for local work)
- Go `1.26.5`, Node `24.18.0`, and pnpm `11.9.0`
- `openssl` for generating local-only secrets

Use the fixed Compose project name below so network and cleanup targets stay narrow. The object store requires an AIStor Free license file; keep it outside the repository and point Compose to it before starting the stack:

```bash
export HAPPYLEARN_AISTOR_LICENSE_FILE="$PWD/.secrets/minio.license"
test -r "$HAPPYLEARN_AISTOR_LICENSE_FILE"
docker compose -p happylearn-dev -f deploy/compose.dev.yml up -d --build
docker compose -p happylearn-dev -f deploy/compose.dev.yml ps
```

Wait until all services report `healthy`. The application is available at `127.0.0.1:8080`; PostgreSQL, Redis, and the AIStor S3/console development ports bind only to loopback. The worker health endpoint is private to the Compose network and is not published on the host. Production deployment must remove every database, Redis, S3, and S3-console host mapping.

The worker runs as UID/GID `10002`, with a read-only root filesystem, no Linux capabilities, and a 1024 MiB tmpfs work directory. It has no published port and its network has no public route. Each uploaded version is processed serially through type validation, ClamAV scanning, bounded Office conversion or video probing, and private preview storage. Processing fails closed when the baked-in daily ClamAV definitions are missing or older than seven days. Rebuild and redeploy `Dockerfile.worker` at least weekly (and immediately for urgent signature releases):

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml build --pull worker
docker compose -p happylearn-dev -f deploy/compose.dev.yml up -d worker
docker compose -p happylearn-dev -f deploy/compose.dev.yml logs --no-log-prefix worker
```

Worker logs intentionally contain stable error categories rather than uploaded names, object keys, or file contents. A readiness failure means PostgreSQL, object storage, tmpfs, or one of `clamscan`, `soffice`, `pdfinfo`, and `ffprobe` is unavailable.

Soft-deleted, unreferenced file versions remain recoverable for 30 days. Run bounded cleanup from the same hardened image; it rechecks draft and published references under database locks and retains metadata whenever object deletion fails:

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml run --rm --no-deps \
  --entrypoint /app/happylearn-maintenance worker cleanup-files --limit 100
```

## Local configuration, migration, and bootstrap

Create a private local secret directory and environment file. The values below are development-only placeholders; do not reuse them outside local testing.

```bash
install -d -m 0700 .secrets
umask 077
openssl rand -base64 48 > .secrets/login-throttle-secret
cat > .env <<EOF
HAPPYLEARN_ENV=development
HAPPYLEARN_LISTEN=:8080
HAPPYLEARN_DATABASE_URL=postgres://happylearn:happylearn_dev@127.0.0.1:54329/happylearn?sslmode=disable
HAPPYLEARN_REDIS_URL=redis://127.0.0.1:56379/0
HAPPYLEARN_LOGIN_THROTTLE_SECRET=$(cat .secrets/login-throttle-secret)
HAPPYLEARN_PUBLIC_ORIGIN=http://127.0.0.1:8080
HAPPYLEARN_TRUSTED_PROXY_CIDRS=
EOF
chmod 600 .env .secrets/login-throttle-secret
```

Build the console before starting the Go server; server startup applies the embedded migrations. Create the sole teacher once using a password file with owner-only permissions.

```bash
pnpm install --frozen-lockfile
pnpm build
set -a; . ./.env; set +a
go run ./cmd/server
# In a second terminal, after the server has applied migrations:
set -a; . ./.env; set +a
read -rs -p 'Development teacher password: ' HAPPYLEARN_BOOTSTRAP_PASSWORD; echo
printf '%s' "$HAPPYLEARN_BOOTSTRAP_PASSWORD" > .secrets/admin-password
unset HAPPYLEARN_BOOTSTRAP_PASSWORD
chmod 600 .secrets/admin-password
go run ./cmd/admin create-teacher --username admin --display-name '教师' --password-file .secrets/admin-password
shred -u .secrets/admin-password
```

The running server exposes `GET /api/v1/health/live` and `GET /api/v1/health/ready` on the same `http://127.0.0.1:8080` origin as the Vue application.

## Test and acceptance commands

Run all unit, integration, static-web, frontend, type, build, vulnerability, and Compose validation checks:

```bash
make verify
```

Complete Phase 2 acceptance requires the AIStor Free license path and uses unique containers, an internal network, and dedicated data/fixture/runner volumes. It includes the Phase 1 authentication scenarios, generates all file fixtures inside Linux, uses only disposable credentials, stores failure evidence under `test-results/phase2`, and removes only its own resources in a trap:

```bash
HAPPYLEARN_AISTOR_LICENSE_FILE="$PWD/.secrets/minio.license" make e2e
```

For manual diagnosis only, start the server as above and use new test-only passwords (never production values):

```bash
export E2E_ADMIN_PASSWORD='replace-with-a-local-test-password'
export E2E_STUDENT_PASSWORD='replace-with-a-local-temporary-password'
export E2E_STUDENT_NEW_PASSWORD='replace-with-a-local-changed-password'
pnpm exec playwright install chromium
E2E_FIXTURE_DIR=/absolute/path/to/disposable/generated-fixtures \
  pnpm exec playwright test tests/e2e/teaching.spec.ts tests/e2e/files.spec.ts tests/e2e/learning.spec.ts
unset E2E_ADMIN_PASSWORD E2E_STUDENT_PASSWORD E2E_STUDENT_NEW_PASSWORD
```

## Container image and read-only smoke test

Build the phase image after a clean frontend build is available to the Docker build stages:

```bash
docker build -t happylearn:phase2 .
docker build -f Dockerfile.worker -t happylearn-worker:phase2 .
```

For a local container smoke test, create a separate owner-only file using the Compose-network hostnames, then run the image with a read-only root filesystem. Replace `CHANGE_ME_LOCAL_ONLY` with a generated local secret, never a real production secret.

```bash
umask 077
cat > .env.container <<'EOF'
HAPPYLEARN_ENV=development
HAPPYLEARN_LISTEN=:8080
HAPPYLEARN_DATABASE_URL=postgres://happylearn:happylearn_dev@postgres:5432/happylearn?sslmode=disable
HAPPYLEARN_REDIS_URL=redis://redis:6379/0
HAPPYLEARN_LOGIN_THROTTLE_SECRET=CHANGE_ME_LOCAL_ONLY_AT_LEAST_32_BYTES_LONG
HAPPYLEARN_PUBLIC_ORIGIN=http://127.0.0.1:8080
EOF
chmod 600 .env.container
docker run --rm --name happylearn-phase2 --read-only --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --user 10001:10001 --network happylearn-dev_happylearn --env-file .env.container -p 8080:8080 happylearn:phase2
# In another terminal:
curl --fail http://127.0.0.1:8080/api/v1/health/live
curl --fail http://127.0.0.1:8080/api/v1/health/ready
docker inspect -f '{{.Config.User}}' happylearn-phase2
```

## Backup and restore

Back up the named development database before testing migrations or restores:

```bash
mkdir -p backups
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  pg_dump -U happylearn -Fc happylearn > backups/happylearn-dev-$(date +%F).dump
```

The database dump is not a complete Phase 2 backup. Snapshot the `minio_data` volume in the same maintenance window and retain the exact database/object-store pair; immutable revision rows refer to object keys. See [phase2-files.md](phase2-files.md) for recovery and rollback procedures.

**Destructive development-only restore:** this replaces data in the `happylearn` database of the explicitly named `happylearn-dev` Compose project. Confirm the backup file and project name before running it.

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  pg_restore -U happylearn --clean --if-exists --no-owner -d happylearn < backups/happylearn-dev-YYYY-MM-DD.dump
```

## Shutdown and destructive cleanup

Stop the local stack without deleting data:

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml down
```

**Destructive development-only cleanup:** this removes only the volumes attached to the named `happylearn-dev` Compose project; it does not target other Docker projects, images, or host paths.

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml down --volumes --remove-orphans
```

## CI branch protection

The repository workflow verifies pushes to `master` and `main`. Branch protection is an external repository setting and must be configured in the hosting provider; this repository does not claim that setting is enabled.
