# Agentize Improvement Roadmap

This directory is the project's engineering backlog, chapter by chapter. It has two parts:

- **Audit chapters (01–06)** — a full-codebase review (concrete weaknesses with `file:line`
  references + fixes). **All audit findings are implemented** (each chapter carries a status
  note); chapter 06 is the forward roadmap those fixes unlocked.
- **Forward backlog (07–14)** — detailed, task-by-task specs for the work that remains from
  chapter 06's later phases. Each is written to be executed one chapter at a time.

## Audit chapters (01–06) — ✅ implemented

| Chapter | Scope | Status |
|---|---|---|
| [01 — Core Agent](01-core-agent.md) | `core/`, `agentmanager/`, system prompt, sessions, tools, vision | ✅ C1–C12 done |
| [03 — Storage & Data](03-storage-data.md) | `store/`, `filestore/`, `fsrepo/`, `model/`, `config/` | ✅ S1–S21 done (S9 deferred → ch 14) |
| [04 — API, Security & Observability](04-api-security-observability.md) | `routes.go`, `debuger/`, `metrics/`, `imageedit/`, `log/` | ✅ A1–A9 done |
| [05 — Testing, CI & Docs](05-testing-ci-docs.md) | tests, Makefile, scripts, `docs/` | ✅ T1–T9 done |
| [06 — Future Steps](06-future-steps.md) | product/feature roadmap (phases) | Phases 1–2 + much of 4 done; remainder → 07–14 |

## Forward backlog (07–14) — 🔜 to implement

Detailed specs for everything still outstanding from chapter 06. Each chapter has: current
state, goals, a grounded design with code sketches, a numbered **task list** (with file targets,
tests, and acceptance criteria), and a suggested order of work.

| Chapter | Scope | Size | 06 phase |
|---|---|---|---|
| [07 — Observability & ops finishing](07-observability-ops-finish.md) | log correlation IDs; Verify/PoolStats/Backup on the dashboard | S | 2.2, 2.3 |
| [08 — Performance & horizontal readiness](08-performance-scaling.md) | benchmark suite; hot-path; single-process constraint + lock seam | S–M | 3.3, 3.4 |
| [09 — Memory: Summaries array + token budget](09-memory-summaries-context.md) | `Summary` → `Summaries[]` + DB migration; one context budget | **L** | 3.1, 3.2 |
| [10 — Human-in-the-loop approvals](10-human-in-the-loop.md) | **generic, UI-agnostic, durable** approval mechanism (Telegram + dashboard + API) | M–L | 4.4 |
| [11 — Streaming responses](11-streaming-responses.md) | `ProcessMessageStream` deltas + mid-conversation cancel | **L** | 4.2, 4.6 |
| [12 — Retrieval over user files](12-file-retrieval.md) | per-user embedding search (sqlite-vec / Mongo / brute-force) | **L** | 4.3 |
| [13 — Audio transcription input](13-audio-input.md) | voice-note → transcript via a pluggable provider | M | 4.7 |
| [14 — Release engineering & sync tooling](14-release-sync-tooling.md) | semver/tagging; Go sync tool; public-readiness; S9 | S–M | 5.1–5.3, S9 |

## How to read the chapters

- **Audit chapters (01–06):** Current state → Weaknesses (severity + `file:line`) → Proposed
  solutions → Order of work → Implementation status.
- **Backlog chapters (07–14):** Current state → Goals → Design (code sketches) → **Tasks**
  (numbered, file-targeted) → Acceptance → Order of work → Caveats.

## Suggested execution order (backlog)

```
07 (obs/ops, quick) ─► 08 (benchmarks/baseline) ─► 09 (memory migration — coordinate w/ darkoob)
10 (human-in-the-loop) ── independent; high priority for the darkoob Telegram bot
11 (streaming) ─► (enables mid-conversation cancel)
12 (retrieval), 13 (audio) ── independent feature adds
14 (release/sync) ── lowest urgency; do the sync de-risk when convenient
```

Rules carried from chapter 06's "what NOT to do": don't add a new datastore (use sqlite-vec /
Mongo), don't adopt Temporal preemptively (the opt-in runner already exists), and coordinate any
schema change (ch 09, ch 10's review table) with the darkoob sync.

## Status of the original cross-cutting priorities (P0 → P3) — ✅ all done

| Priority | Item | Chapter | Status |
|---|---|---|---|
| **P0** | Auth for `/agentize/*` + protect `delete-data` / raw download | 04 | ✅ |
| **P0** | CI pipeline (`go vet`, `go test -race`, coverage) | 05 | ✅ |
| **P1** | Session TOCTOU; prompt size cap; pre-LLM quota check | 01 | ✅ |
| **P1** | Store hardening (migrations, schema versioning, validation, pagination) | 03 | ✅ |
| **P1** | Audit logging for destructive operations | 03, 04 | ✅ |
| **P2** | Cache/pool/staleness metrics; configurable limits; LRU caches | 01, 03, 04 | ✅ (pool gauges → ch 07) |
| **P2** | README rewrite + `SECURITY.md` + `OPERATIONS.md`; English auth docs | 05 | ✅ |
