# HappyLearn Phase 3 Execution Index

Execute these plans in order:

1. `docs/superpowers/plans/2026-07-22-phase3-qa-core.md` — schema, immutable timelines, student isolation, teacher queue, state transitions, idempotency, notes, and audit.
2. `docs/superpowers/plans/2026-07-22-phase3-files-notifications.md` — QA upload policies, immutable message attachments, controlled delivery, notification storage, polling, and durable lesson-publication outbox consumption.
3. `docs/superpowers/plans/2026-07-22-phase3-console-acceptance.md` — student and teacher question consoles, notification center, polling lifecycle, browser acceptance, CI, and runbooks.

The three plans implement `docs/superpowers/specs/2026-07-22-phase3-teacher-qa-notifications-design.md`. Do not start Phase 4 until the final gate in the console/acceptance plan passes.

## Spec Coverage Map

| Approved requirement | Owning plan/task |
|---|---|
| One question per private thread and immutable timeline | QA Core Tasks 1–2 |
| SQL-level student isolation and uniform `404` | QA Core Tasks 2–3 |
| Teacher queue, reply, state machine, reopen, notes | QA Core Tasks 3–4 |
| Idempotency, optimistic versioning, transactional audit | QA Core Tasks 2–4 |
| QA upload limits, scanning, ownership, immutable bindings | Files/Notifications Tasks 1–2 |
| Controlled preview/download and access logs | Files/Notifications Task 2 |
| Notification list/count/read and 15-second polling API | Files/Notifications Task 3; Console Task 3 |
| Durable, idempotent lesson-publication notifications | Files/Notifications Task 4 |
| Student and teacher responsive workflows | Console Tasks 1–2 |
| Cross-student E2E, CI, Docker, and runbooks | Console Task 4 |

## Phase 3 Completion Gate

1. Execute every plan task in order with its RED/GREEN evidence.
2. Run the complete verification list from the console/acceptance plan.
3. Review the final Phase 3 diff for spec compliance, security/privacy, operations, and test adequacy.
4. Fix every Critical or Important finding and rerun the complete gate.
5. Keep the repository clean before beginning the Phase 4 design.
