# Alert Webhook C0 Hardening Design

## Scope

Harden commit `4b3c377` without changing the public eleven-field webhook
payload, the 1/5/30-minute schedule, the disabled/no-backlog behavior, the
admin test API, retention, logging, or `internal/platform/safelog`.

## Lease fencing

`Complete` must fence against authoritative database state in the same
delivery update. In addition to identity, owner, and token, it requires
`claim_expires_at > finished_at`. Equality is expired. A rejected completion
returns `ErrConflict` before later attempts can be cancelled.

## Migration 25

Migration 25 upgrades existing version-24 data before installing constraints:

- copy `alert_webhook_events.alert_id` into every related delivery;
- force `opened` snapshots to `state='open'`;
- force `upgraded` snapshots to `severity='critical'` and repair states other
  than `open` or `acknowledged` to `open`;
- force `resolved` snapshots to `state='resolved'`.

It then adds a unique event identity `(id, alert_id)`, replaces the delivery's
single-column event foreign key with `(event_id, alert_id)`, and adds a
transition/snapshot check. This prevents poison rows from entering the claim
path. Down migration restores the version-24 foreign key and indexes without
undoing safe data normalization.

## Claim query and indexes

The claim query is a package constant used by both production and the real
PostgreSQL `EXPLAIN (ANALYZE, BUFFERS)` test. Due time is the exact expression:

```sql
CASE
  WHEN delivery_state='pending' THEN scheduled_at
  WHEN delivery_state='claimed' THEN claim_expires_at
END
```

Migration 25 indexes that expression first for pending/claimed webhook rows.
Separate event-scoped partial indexes serve active-claim and succeeded-event
anti-lookups. The claim remains one ordered candidate CTE with
`FOR UPDATE SKIP LOCKED`, preserving fairness and multi-replica safety.

## Legacy delivery isolation

Legacy replay reads include `event_id IS NULL`, matching the partial unique
index used by insertion. A legacy row and event delivery may therefore share
alert, attempt, and destination without ambiguous replay or conflict checks.

## Sender construction and safe errors

`WebhookSenderConfig` no longer exposes a `Doer`. The HTTP doer interface and
the constructor accepting it are package-private test seams. The exported
constructor always creates the pinned `safehttp` client.

All invalid configuration, URL normalization, and resolver failures return the
stable sentinel `ErrInvalid` without wrapping raw URL, hostname, IP, resolver,
authorization, or timeout details.

## Verification

Tests must first reproduce each defect, then pass after the smallest fix:

- live, exact-expiry, expired, and re-owned lease completion;
- version-24 data repair, direct invalid insert/update rejection, composite FK,
  valid acknowledged critical upgrade, claimability, and DownTo24;
- real PostgreSQL legacy/event coexistence;
- marker-bearing configuration/resolver errors and absence of public `Doer`;
- actual claim SQL plan over at least 12,000 historical rows;
- affected ordinary and race tests, full `go vet`, gofmt, and diff checks.
