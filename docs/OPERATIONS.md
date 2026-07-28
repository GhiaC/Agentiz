# Operations

Running Agentize-backed services in production: health, backup/restore, tuning,
alerting, graceful shutdown, and upgrades. See [METRICS.md](METRICS.md) for the
metric catalogue and [SECURITY.md](SECURITY.md) for the threat model.

## Health

- `GET /agentize/health` — always-open liveness probe; returns status, node
  count, and version. Wire it to your orchestrator's liveness/readiness checks.
- `agentize_message_in_progress` and `agentize_scheduler_running` show live
  activity; the Go/process collectors (`go_goroutines`,
  `process_resident_memory_bytes`) ship on the same `/agentize/metrics` endpoint.

## Backup & restore

Both backends implement `store.Maintainer`. The store handed to your app is a
transparent metered wrapper that **forwards** `Maintainer`, so:

```go
import "github.com/ghiac/agentize/store"

if mt, ok := ag.GetSessionStore().(store.Maintainer); ok {
	f, _ := os.Create("backup.db")
	defer f.Close()
	if err := mt.Backup(f); err != nil { /* handle */ }
}
```

| Backend | Backup | Restore |
|---|---|---|
| **SQLite** | `Maintainer.Backup(w)` streams a consistent snapshot via `VACUUM INTO` (safe on a live DB). | Stop the service, replace the DB file with the snapshot, restart. |
| **MongoDB** | `Backup` returns `store.ErrBackupUnsupported` — use `mongodump` against the cluster. | `mongorestore`. |

**Integrity check.** `Maintainer.Verify()` scans for orphaned rows (messages,
tool calls, opened files, user files, route traces whose parent is gone) and
returns `[]store.Issue`. Run it after a restore or on a schedule. Pair with the
`agentize_store_deletions_total` metric to watch destructive activity.

## Graceful shutdown

Drain in this order so nothing is lost mid-write:

```go
ag.StopScheduler()                 // stop the background summarizer
_ = store.Close()                  // close the DB handle / Mongo client you opened
```

(`store` here is the value you got from `store.Open`. Closing releases the
SQLite handle or the MongoDB connection pool.)

## Scheduler tuning

The summarization scheduler runs in-process and is configured by env vars (see
the table in [README.md](../README.md) and [METRICS.md](METRICS.md)). Watch:

- `agentize_scheduler_run_duration_seconds` — cycle cost; if it approaches the
  check interval, raise `AGENTIZE_SCHEDULER_CHECK_INTERVAL_MINUTES`.
- `agentize_summary_age_seconds` — how stale summaries get before refresh; a
  rising p95 means thresholds are too high or the scheduler is starved.
- `agentize_scheduler_summaries_total{status="error"}` — failing summarizations.

## Alerting

PromQL building blocks live in [METRICS.md](METRICS.md). Suggested alerts:

| Alert | Expression sketch | Why |
|---|---|---|
| Core message error rate | `rate(agentize_message_processed_total{layer="core",status="error"}[5m])` high | User-facing failures |
| Backup-LLM fallback | `rate(agentize_backup_llm_total{status="ok"}[15m])` > 0 | Primary LLM is failing over |
| Store latency p95 | `histogram_quantile(0.95, sum by (le,operation,backend) (rate(agentize_store_query_duration_seconds_bucket[10m])))` | DB slowness |
| Summary staleness | `histogram_quantile(0.95, rate(agentize_summary_age_seconds_bucket[1h]))` | Summarizer falling behind |
| Rejected deletes | `rate(agentize_audit_actions_total{status="rejected"}[15m])` > 0 | Possible probing of the delete endpoint |
| Sections dropped | `rate(agentize_system_prompt_sections_dropped_total[15m])` > 0 | Prompt over budget — raise `MaxSystemPromptSize` |

Import the dashboards in [grafana/](grafana/) (`agentize-dashboard.json` and
`agentize-summarization-dashboard.json`); the former includes a **Storage &
Audit** row covering store latency, summary age, audit actions, and deletions.

## Human-in-the-loop reviews

Every tool invocation is gated by a durable "approve / reject" request when a
`ToolApprovalManager` is configured. Agentize-created engines enable this by
default; Core + worker deployments should call
`ch.SetToolApprovalManager(ag.ReviewManager())`. The execution goroutine waits
without invoking the tool and resumes only after approval. Rejection and
approval-system errors fail closed. Hosts can also raise a durable
`ReviewRequest` (see [REVIEWS.md](REVIEWS.md)). Pending reviews are listed,
approved, and rejected at **`/agentize/debug/reviews`** (admin-gated; resolutions
are audit-logged as `agentize_audit_actions_total{action="resolve_review"}`), and
the same `Resolve` call backs a Telegram bot or any HTTP frontend. Watch
`agentize_reviews_pending` for a growing backlog of unresolved approvals.

Deterministic workflow runs are visible at `/agentize/debug/workflows`; their
schedule links and task outputs are persisted in `workflow_runs`. Immediate
workflows can wait on per-task reviews. A scheduled workflow is approved once
at creation and then executes its unchanged DAG without per-run approvals.
Schedule detail pages link to each workflow run and to the schedule's dedicated
memory session.

The workflow runner is process-local. State transitions are durable, but an
activity interrupted by a process crash is not automatically replayed; inspect
stale `running` workflows before manually retrying side-effecting work.

## Scaling

- **SQLite** is single-node (WAL + retry). It suits one process; for horizontal
  scale or HA, use the **MongoDB** backend (`store.Open` with `Backend: "mongodb"`)
  — switching is a one-line config change because both satisfy `store.Store`.
- The raw-file-download rate limit is **per process** (in-memory token bucket). If
  you run multiple replicas, also rate-limit at the ingress.
- Metrics use a dedicated registry; scrape each replica separately and aggregate
  in Prometheus.

## Logging

`AGENTIZE_LOG_LEVEL` (`debug|info|warn|error`, default `info`) and
`AGENTIZE_LOG_FORMAT` (`text` default, or `json` for ingestion). `[AUDIT]` lines
record destructive actions with user + client IP — route them to your retention
store. Avoid `debug` in production (verbose, may surface request detail).

## Upgrades & the darkoob sync

Agentize is mirrored into the darkoob platform
(`darkoob-platform/services/infra_agent/pkg/agentize`) by
`scripts/sync-to-darkoob.sh`, which copies changed files and rewrites the import
path to the destination module.

```bash
# Always preview first — lists operations without writing anything.
./scripts/sync-to-darkoob.sh --dry-run

# Then apply.
./scripts/sync-to-darkoob.sh
```

Before syncing: run `make test-race` (or push and let CI run it), review
[CHANGELOG.md](../CHANGELOG.md) for deprecations, and confirm the destination
`go.mod` import path matches the script's expectation. The CI pipeline syntax-
checks the sync script (`bash -n`) so a regression in it is caught before a real
sync.
