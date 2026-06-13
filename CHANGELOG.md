# Changelog

All notable changes to Agentize are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project aims
to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

API, security & observability hardening (improvement roadmap
[chapter 04](docs/improvements/04-api-security-observability.md)).

### Security
- **Raw user-file downloads are rate limited** to 10 requests/min per IP
  (burst 10), in addition to the existing admin auth, to blunt bulk exfiltration
  by `fileID` enumeration. `GET /agentize/debug/documents/:fileID/raw`.
- **Destructive delete now requires typed confirmation.**
  `POST /agentize/debug/users/:userID/delete-data` rejects (HTTP 400) unless the
  request carries `?confirm=<userID>` matching the path user.
- **Audit trail for user-data deletion.** Every attempt emits an `[AUDIT]` log
  line (user + client IP + outcome) and increments
  `agentize_audit_actions_total{action="delete_user_data",status}`.

### Added
- **Dedicated metrics registry.** `/agentize/metrics` now serves a bounded
  registry containing only `agentize_*` collectors plus the Go runtime/process
  collectors, instead of the global default registry. Set
  `AGENTIZE_METRICS_DEFAULT_REGISTRY=1` to expose the full default registry
  (opt-in). New `metrics.Registry()` accessor.
- **New metrics** (see [docs/METRICS.md](docs/METRICS.md)):
  - `agentize_store_query_duration_seconds{operation,backend}` — per-operation,
    per-backend store latency (transparent wrapper around `store.Store`).
  - `agentize_summary_age_seconds` — staleness of the previous summary when a
    session is re-summarized.
  - `agentize_audit_actions_total{action,status}` — audited admin actions.
- **Configurable logging.** `AGENTIZE_LOG_LEVEL=debug|info|warn|error`
  (default `info`) and `AGENTIZE_LOG_FORMAT=text|json` (default `text`).
- Grafana dashboard: new **"Storage & Audit"** row
  ([docs/grafana/agentize-dashboard.json](docs/grafana/agentize-dashboard.json)).

### Changed
- `metrics.Handler()` / `metrics.GinHandler()` now serve the dedicated registry
  by default (behavioral change for hosts mounting them directly; opt back into
  the global default registry with `AGENTIZE_METRICS_DEFAULT_REGISTRY=1`).

### Removed
- Dead, commented-out "summary repair" block in the summarization scheduler
  (`engine/schedules.go`).

### Deprecated
These remain for backward compatibility and are slated for removal in a future
`0.x` release. They are marked with Go `// Deprecated:` doc comments so `go vet`
and editors surface them at call sites.

| Symbol | Location | Replacement |
|---|---|---|
| `engine.UsageEvent.Tokens` | `engine/hooks.go` | `InputTokens` / `OutputTokens` / `CachedInputTokens` |
| `engine.(*Engine).executeOneToolCall` | `engine/user_agent.go` | `executeTool` |

## Tracked technical debt

Live `TODO`s carried in the code, referenced by a stable ID in the comment
(`TODO(TD-N): ...`) so they are traceable until filed as issues.

| ID | Item | Location |
|---|---|---|
| TD-1 | Move `generateTags` into `llmutils` | `model/session.go` |
| TD-2 | Revert to session-based tool loading after v1 testing | `engine/user_agent.go` (`GetTools`) |

## [0.1.0]

- Initial development version.
