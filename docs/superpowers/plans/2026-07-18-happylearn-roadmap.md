# HappyLearn Delivery Roadmap

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement each phase plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the approved HappyLearn design as six sequential, independently testable phases.

**Architecture:** A Go + Vue modular monolith is deployed with Docker Compose and backed by PostgreSQL, Redis, and MinIO. Each phase leaves the application runnable and adds one bounded business capability without weakening role or data isolation.

**Tech Stack:** Go 1.26.5, Node.js 24 LTS, pnpm 11, Vue 3, TypeScript, PostgreSQL 18.4, Redis 8.8, MinIO, Caddy, Docker Compose.

## Global Constraints

- Target Ubuntu 24.04 on 2 CPU cores and 4 GB RAM.
- One teacher super-administrator; every student account is teacher-created.
- Student answers and attachments are private to that student and the teacher.
- PostgreSQL stores business records; MinIO stores file bodies; Redis stores ephemeral coordination data only.
- Database, Redis, and MinIO ports are never published to the public network.
- AI keys never reach the browser and never appear in logs.
- Implement with TDD, least privilege, explicit ownership checks, and frequent commits.

---

## Phase Sequence

1. **Foundation, authentication, authorization, and audit**  
   Plan index: `docs/superpowers/plans/2026-07-18-phase1-index.md`  
   Exit: the teacher can sign in, create/disable students, reset passwords, and inspect audit events; students can sign in, complete first-password change, and cannot reach admin APIs.

2. **Teaching catalog and secure files**  
   Plan to be written after Phase 1 acceptance.  
   Exit: the teacher can publish grade/term/subject/chapter/lesson content with private MinIO files and per-file preview/download rules; students can access published content only.

3. **Teacher Q&A and in-app notifications**  
   Plan to be written after Phase 2 acceptance.  
   Exit: a student can open a private teacher thread, exchange messages and attachments, receive notifications, and never access another student's thread.

4. **AI Q&A, compatible gateway, and usage accounting**  
   Plan to be written after Phase 3 acceptance.  
   Exit: the teacher can configure an encrypted OpenAI-compatible provider; students receive streaming answers with idempotent token/cost accounting and quotas.

5. **Dashboards, backup, monitoring, and hardening**  
   Plan to be written after Phase 4 acceptance.  
   Exit: the teacher sees health, storage, AI usage, audit, and backup status; restore and retention procedures are automated and verified.

6. **Production deployment and full acceptance**  
   Plan to be written after Phase 5 acceptance.  
   Exit: the tagged release runs behind Caddy on the Ubuntu server, passes security and recovery checks, and meets the approved acceptance criteria on desktop and mobile.

## Cross-Phase Review Gate

At the end of every phase:

1. Run backend unit, integration, race, static-analysis, and vulnerability checks.
2. Run frontend unit, type, lint, build, and relevant browser tests.
3. Run `docker compose config` and container health checks.
4. Review the phase against `docs/superpowers/specs/2026-07-17-happylearn-design.md`.
5. Commit only after all checks pass and record any deliberately deferred requirement in the next phase plan.
