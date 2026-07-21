# Phase 2 file processing and recovery runbook

This runbook covers the private upload, processing, preview, replacement, and retention path. Commands assume the fixed development Compose project `happylearn-dev`; substitute the explicitly approved production project name and never expose PostgreSQL, Redis, S3, the AIStor console, or worker health ports publicly.

## Required configuration

The app and worker require `HAPPYLEARN_DATABASE_URL`, `HAPPYLEARN_REDIS_URL`, `HAPPYLEARN_LOGIN_THROTTLE_SECRET`, `HAPPYLEARN_PUBLIC_ORIGIN`, `HAPPYLEARN_MINIO_ENDPOINT`, `HAPPYLEARN_MINIO_ACCESS_KEY`, `HAPPYLEARN_MINIO_SECRET_KEY`, `HAPPYLEARN_MINIO_ORIGINALS_BUCKET`, and `HAPPYLEARN_MINIO_PREVIEWS_BUCKET`. Set `HAPPYLEARN_MINIO_USE_TLS=true` when the internal endpoint uses TLS. Compose additionally requires `HAPPYLEARN_AISTOR_LICENSE_FILE`, a readable host path to the AIStor Free license; never place license contents in environment variables, images, logs, or Git.

The API and worker idempotently create the two configured buckets, verify that neither has a public bucket policy, and install an incomplete-multipart lifecycle rule. Startup and readiness fail closed if any check fails. Processing concurrency is one worker replica and one leased job at a time; do not scale the worker on the 2-core/4-GB target.

## Accepted and rejected inputs

| Input | Accepted behavior | Rejection behavior |
| --- | --- | --- |
| PDF, image, DOCX/XLSX/PPTX | Virus scan plus bounded same-origin preview | Type mismatch, malformed conversion, encrypted content, or unsafe archive fails closed |
| MP4 with supported browser media | Virus scan, metadata probe, original-object Range playback | Invalid container/codec metadata fails processing |
| Other video containers/codecs | May be retained only when safe and offered with download policy | Preview policy is refused without a trusted browser-playable original or ready derived preview |
| Macro-enabled Office | Never publishable | Rejected with a stable non-content failure category |
| ZIP/archive or archive bomb | Never publishable | Rejected before conversion/extraction |
| Antivirus detection (including the disposable EICAR probe) | Never publishable | Rejected; no preview or download capability is issued |
| Declared MIME/content mismatch | Never publishable | Rejected after trusted type detection |

The global upload limit is 500 MiB and browser parts are 8 MiB with at most two concurrent requests. “Preview only” removes the normal download endpoint but is not DRM; users can still capture rendered content.

## Interrupted upload recovery

Open upload sessions remain resumable for 24 hours. The browser stores only the opaque session ID in IndexedDB, re-queries completed part numbers after reload, and uploads missing parts. Ask the teacher to select the same local file (same name, size, and modification time) and choose **继续上传**. Do not delete multipart objects during a transient request failure.

If a session is expired or cancelled, start a new upload. The bounded cleanup runner aborts expired multipart uploads after the retention window. Verify counts without exposing object keys:

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -c "SELECT state,count(*) FROM upload_sessions GROUP BY state ORDER BY state;"
```

## Processing diagnosis and retry

Check container health and stable categories first; logs intentionally omit filenames, object keys, hashes, and contents:

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml ps
docker compose -p happylearn-dev -f deploy/compose.dev.yml logs --since 30m --no-log-prefix worker
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T worker \
  sh -c 'clamscan --version && soffice --version && pdfinfo -v && ffprobe -version'
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -c "SELECT state,attempts,last_failure_category,count(*) FROM file_processing_jobs GROUP BY state,attempts,last_failure_category ORDER BY state,attempts;"
```

A `running` lease past `lease_until` is reclaimed automatically, up to four attempts. A terminal `failed` version can be retried from **文件中心** only when the server classifies it as retryable; rejected malware, macro, archive, and type-mismatch inputs must never be manually reset in SQL. Rebuild the worker at least weekly so ClamAV definitions remain within the seven-day readiness limit. For converter/scanner failures, confirm `/work` is tmpfs, has free space, and all four commands above execute as UID 10002 before using the UI retry action.

## Replacement, rollback, and cleanup

Replacement creates a new immutable version and updates draft references only. Current publications continue to reference their frozen file version. Use **文件中心 → 替换** after the new upload reaches `ready`; use **回滚草稿引用** to select a retained older version. Publish a new lesson revision only after preview and audience checks pass.

Unreferenced versions are retained for at least 30 days from losing their last effective reference. Run the bounded cleanup command; it rechecks draft and published references under database locks and leaves metadata intact if object deletion fails:

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml run --rm --no-deps \
  --entrypoint /app/happylearn-maintenance worker cleanup-files --limit 100
```

Never delete S3 objects or file rows by hand. A preview-only file may still have both original and derived objects; cleanup owns both.

## Private-network verification

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml config | sed -n '/ports:/,/^[^ ]/p'
docker network inspect happylearn-dev_happylearn
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T app \
  curl --fail --silent http://127.0.0.1:8080/api/v1/health/ready
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T worker \
  curl --fail --silent http://127.0.0.1:8081/ready
```

On production, only the reverse proxy may publish 80/443. The Compose application, worker, PostgreSQL, Redis, S3 API, and S3 console must have no host-port mapping. Bucket policy checks must report private; do not use presigned public URLs.

## Backup and Phase 1 rollback

Quiesce publishing, uploads, the worker, and cleanup before taking a coordinated PostgreSQL dump and object-volume snapshot. Restore both from the same recovery point, then run readiness checks before resuming writes. A database-only restore can leave immutable revisions pointing at missing objects; an object-only restore can retain unreachable data.

To roll application behavior back to the Phase 1 image, stop the Phase 2 app and worker, keep PostgreSQL and the `minio_data` volume intact, and start the approved Phase 1 app image against the same database only if its migration compatibility has been verified. Do not run `down --volumes`, downgrade migrations, delete Phase 2 tables, or erase AIStor data. Keep the Phase 2 worker stopped while Phase 1 is active. Roll forward by restoring the Phase 2 app/worker images and verifying both readiness endpoints; retained objects and immutable revisions remain available.
