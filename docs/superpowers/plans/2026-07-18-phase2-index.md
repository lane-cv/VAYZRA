# HappyLearn Phase 2 Execution Index

Execute these plans in order:

1. `docs/superpowers/plans/2026-07-18-phase2-catalog-publishing.md` — fixed catalog, drafts, immutable publication, audience authorization, search, and learning state.
2. `docs/superpowers/plans/2026-07-18-phase2-secure-files.md` — private MinIO, resumable multipart uploads, file bindings, preview/download policy, Range delivery, and access logs.
3. `docs/superpowers/plans/2026-07-18-phase2-processing-files.md` — durable processing jobs, isolated scan/Office conversion/video probing, file center, replacement, rollback, and publication readiness.
4. `docs/superpowers/plans/2026-07-18-phase2-console-acceptance.md` — teacher editor, student learning space, Markdown/LaTeX safety, browser acceptance, Docker hardening, and runbooks.

The four plans implement `docs/superpowers/specs/2026-07-18-phase2-teaching-design.md`. Do not start Phase 3 until the final gate in the console/acceptance plan passes.

## Spec Coverage Map

| Approved requirement | Owning plan/task |
|---|---|
| Fixed five-level empty catalog, ordering, archive | Catalog Tasks 1–2 |
| Markdown drafts, optimistic autosave, immutable publication | Catalog Task 2; Console Tasks 1–2 |
| All/selected-student visibility and non-enumeration | Catalog Tasks 1–3; acceptance |
| Authorized title/body/attachment-name search and progress | Catalog Task 3; Secure Files Task 3; Console Task 3 |
| Private object storage and resumable 500 MiB upload | Secure Files Tasks 1–2 |
| Preview/download policy, Range video, access logs | Secure Files Task 3 |
| Scan, Office preview, video probe, no transcode | Processing Tasks 1–2 |
| Replacement, 30-day rollback, safe cleanup, file center | Processing Task 3; Console Task 2 |
| Arbitrary HTTPS video URL with restricted iframe | Catalog Task 2; Console Task 3 |
| Desktop/mobile teacher and student workflows | Console Tasks 1–3 |
| Full tests, security review, Docker/resource validation | Console Task 4 and every gate |
