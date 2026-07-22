# Phase 3 Q&A and notification operations

This runbook covers the private teacher/student Q&A timeline, Q&A attachments, the notification outbox, and the 15-second browser polling client. Commands use sample identifiers or `psql` variables; replace them only with values from the authorized incident or maintenance ticket. Never paste question text, note text, attachment names, object keys, cookies, or tokens into tickets or logs.

## Limits and supported files

- Question titles: 1–160 Unicode characters; message and private-note bodies: 1–20,000 Unicode characters.
- Each message permits at most 20 attachments and 100 MiB total.
- Accepted attachments are JPEG, PNG, WebP, GIF (20 MiB each), PDF (50 MiB), DOCX/XLSX/PPTX (30 MiB), and plain text/Markdown (10 MiB).
- Messages and teacher notes are append-only. Status changes do not rewrite the timeline.
- A student can access only their own thread, notifications, and bound Q&A file versions. A missing UUID and another student's UUID both return `404`.

## Disposable acceptance

Keep the AIStor license outside the repository. The harness generates synthetic teacher/student credentials and harmless image/PDF fixtures, creates a unique internal network and volumes, runs one processing worker and all Phase 1–3 browser suites, and deletes only its prefixed resources.

```bash
export HAPPYLEARN_AISTOR_LICENSE_FILE=/absolute/path/to/local/minio.license
test -r "$HAPPYLEARN_AISTOR_LICENSE_FILE"
make e2e-phase3
```

On failure, inspect `test-results/phase3/containers.log`. The harness strips connection strings and credential-like fields and removes traces, screenshots, and videos before CI artifact upload.

## Stuck attachment processing

Use only IDs supplied through the authorized support workflow. Do not query by display name.

```bash
export QA_FILE_VERSION_ID=11111111-1111-4111-8111-111111111111
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -v version_id="$QA_FILE_VERSION_ID" -c \
  "SELECT processing_state,failure_category,created_at FROM file_versions WHERE id=:'version_id';"
docker compose -p happylearn-dev -f deploy/compose.dev.yml ps worker
docker compose -p happylearn-dev -f deploy/compose.dev.yml logs --since 10m --no-log-prefix worker
unset QA_FILE_VERSION_ID
```

Logs expose stable categories only. If the worker is unhealthy, check PostgreSQL/object-store reachability, `/work` tmpfs capacity, ClamAV definition age, and the installed `clamscan`, `soffice`, `pdfinfo`, and `ffprobe` commands. Restarting the single worker is safe; do not edit processing rows or object keys by hand.

## Unbound Q&A attachment retention

A completed Q&A upload that is never bound to a submitted message is retained for 24 hours. Once bound, the file version becomes part of the append-only Q&A history and the orphan cleanup path cannot claim it. Run the existing maintenance cleanup at least hourly so abandoned uploads do not accumulate in the 4 GB deployment budget:

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml run --rm \
  --entrypoint /app/happylearn-maintenance worker cleanup-files --limit 100
```

Schedule the command with the deployment's trusted scheduler; do not run overlapping copies. Object deletion failures retain the database lease for bounded retry, while a binding that wins the row lock prevents cleanup. Monitor only aggregate `file.cleanup_scheduled` and `file.cleanup_completed` audit counts—never object keys or attachment names.

## Outbox leases, retries, and deduplication

Inspect counts and stable error categories, not payload bodies:

```bash
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -c \
  "SELECT kind,published_at IS NOT NULL AS finished,last_error_category,count(*) FROM outbox_events GROUP BY 1,2,3 ORDER BY 1,2,3;"
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -c \
  "SELECT count(*) AS expired_leases FROM outbox_events WHERE published_at IS NULL AND lease_until<=clock_timestamp();"
```

The application leases at most 50 events for 30 seconds, retries with bounded exponential delay, and terminates after 10 attempts. Restart the application to release work only by lease expiry; never clear `lease_owner`, change `attempts`, or replay payload SQL manually. `last_error_category` is one of `payload_invalid`, `kind_unsupported`, `audience_invalid`, `delivery_failed`, `lease_lost`, or `max_attempts`.

Verify deduplication for a supplied recipient and safe dedupe key without reading notification content:

```bash
export RECIPIENT_ID=11111111-1111-4111-8111-111111111111
export DEDUPE_KEY=lesson-published:22222222-2222-4222-8222-222222222222
docker compose -p happylearn-dev -f deploy/compose.dev.yml exec -T postgres \
  psql -U happylearn -d happylearn -v recipient_id="$RECIPIENT_ID" -v dedupe_key="$DEDUPE_KEY" -c \
  "SELECT count(*) AS copies FROM notifications WHERE recipient_user_id=:'recipient_id' AND dedupe_key=:'dedupe_key';"
unset RECIPIENT_ID DEDUPE_KEY
```

The expected result is `0` before delivery or `1` after delivery, never more than one.

## Polling diagnosis

The authenticated console requests `GET /api/v1/notifications/unread-count` every 15 seconds while visible, wakes immediately on focus, and stops timers and in-flight requests on logout, user change, hidden tabs, and unmount. In browser developer tools:

1. Filter Network requests to `unread-count`; verify one request per interval, not parallel bursts.
2. Hide the tab for at least 20 seconds and verify requests stop; refocus and verify one immediate request.
3. Confirm responses have `Cache-Control: no-store` and a request ID.
4. A `401` must stop polling and return the user to authentication. Repeated `5xx` responses require server/outbox diagnosis; do not shorten the interval.

## Privacy-safe support procedure

Ask for the response request ID, approximate timestamp, actor role, and the affected thread/file/notification UUID. Do not ask for screenshots containing messages, private notes, filenames, cookies, or student lists. Correlate only safe audit target IDs and action categories. Never copy `qa_messages.body`, `qa_teacher_notes.body`, upload display names, object keys, notification payloads, or another student's identifier into logs or support systems. Reproduce only with synthetic accounts in the disposable harness.

## Disable a compromised student

Use the teacher console when available. It revokes all sessions and immediately denies Q&A, notification, and attachment access. For an authorized API operation, keep the identifier and session secret outside shell history:

```bash
export STUDENT_ID=11111111-1111-4111-8111-111111111111
# Use the authenticated teacher console to set this exact student's status to disabled.
# Then verify a pre-existing student session receives 401 from /api/v1/auth/me.
unset STUDENT_ID
```

Do not delete the user, thread, notes, messages, notifications, or file objects. Preserve them for recovery and audit.

## Backup and restore

Q&A rows reference original objects in AIStor. Back up PostgreSQL and the AIStor data volume in the same maintenance window and restore the exact pair. A database-only restore can leave attachment metadata pointing at missing objects; an object-only restore can expose no data because authorization remains database-bound. After restore, run the notification dedupe count and sample only authorized Q&A status/file access by UUID.

## Roll back to Phase 2

1. Stop new writes by entering a documented maintenance window.
2. Back up PostgreSQL and AIStor together.
3. Deploy the last Phase 2 application and worker images without running down migrations.
4. Leave migrations `00009`–`00012`, Q&A tables, notification/outbox rows, and all Q&A file objects in place. Phase 2 ignores them.
5. Keep the AIStor lifecycle and bucket data unchanged. Do not delete Q&A originals or previews.
6. Restore Phase 3 by deploying the Phase 3 images against the preserved database/object-store pair and rerun `make e2e-phase3` in a disposable environment.

Down-migrating or deleting Phase 3 data is not part of rollback and requires a separate destructive-change review.
