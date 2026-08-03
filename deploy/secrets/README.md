# Production secret files

Create this directory outside the Git checkout (normally
`/etc/happylearn/secrets`) as root, mode `0711`. Every value file must be a
regular, non-symlink file owned by its consuming container UID, mode `0600`,
and must contain only the value plus at most one terminal newline. The
directory's execute-only traversal does not permit listing or reading another
service's files. Never commit real or example secret values.

| Filename | Consumers | Maximum | Rotation owner |
|---|---|---:|---|
| `postgres-password` | PostgreSQL UID 999 | 4 KiB | database operator |
| `redis-password` | Redis UID 999 | 4 KiB | application operator |
| `minio-access-key`, `minio-secret-key` | AIStor UID 1000 | 4 KiB each | storage operator |
| `aistor-license` | AIStor UID 1000 (may instead be root:0 mode `0440`) | provider file size | storage operator |
| `app-database-url`, `app-redis-url`, `app-login-throttle`, `app-minio-access-key`, `app-minio-secret-key`, `app-ai-master-key` | app/migrate/acceptance UID 10001 | 8 KiB URL / 4 KiB value | application operator |
| `worker-database-url`, `worker-redis-url`, `worker-login-throttle`, `worker-minio-access-key`, `worker-minio-secret-key`, `worker-ai-master-key` | worker UID 10002 | 8 KiB URL / 4 KiB value | application operator |
| `metrics-bearer`, `host-metrics-hmac` | app UID 10001 | 8 KiB | operations operator |
| `backup-database-password`, `backup-local-repository`, `backup-password`, `backup-age-identity` | backup/restore UID 10003 | 4 KiB / 8 KiB / 4 KiB / 64 KiB | backup operator |

Create values with the project-approved password/key generator into a
temporary owner-only directory, validate permissions with `stat`, then move
the complete file atomically into place. Record rotation in the operations
change ticket. Do not pass values on command lines, in environment files, or
through shell history.

Compose file-backed secrets cannot portably remap UID/GID. Shared logical
values therefore have one owner-only copy per service UID. Rotate all copies
in one maintenance-window change and verify their hashes without printing the
values.

Create the backup repository directory and
`HAPPYLEARN_RELEASE_STATE_PATH/backup-workflows` as `10003:0` mode `0700`.
Keep `HAPPYLEARN_RELEASE_STATE_PATH` itself owned by the release coordinator
and mode `0700`; release manifests and state never share the writable backup
workflow subdirectory.

Create `HAPPYLEARN_RELEASE_STATE_PATH/release-input` as `10001:10001` mode
`0700`. The release coordinator atomically places only the candidate manifest
there as `10001:10001` mode `0600`; migrate and acceptance mount this one
subdirectory read-only.
