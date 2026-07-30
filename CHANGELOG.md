# Changelog

All notable changes to Agentize are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project aims
to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

API, security & observability hardening (improvement roadmap
[chapter 04](docs/improvements/04-api-security-observability.md)).

### Breaking changes

- Removed the planning DAG subsystem and its public surface:
  `planning/`, `execute_plan`, `get_plan_status`, `cancel_plan`,
  `UsePlanning`, `ProcessMessageWithPlanning`, the Plans dashboard, and planning
  metrics.
- Agentize-created engines now require an explicit human approval before every
  tool execution by default. Set `Options.DisableToolApprovals` only when a
  trusted host supplies an equivalent gate.

### Fixed
- **`collect_result` no longer fails with "result not found in session".**
  Oversized tool results were stored on a freshly-fetched session clone inside
  `saveToolResult` and then clobbered by `ProcessMessage`'s single
  `Put(session)` (the store returns a copy on every `Get`), so the stored result
  vanished before `collect_result` could read it. The result is now written onto
  the live session the loop persists. Affected every tool whose output exceeded
  `MaxToolResultLength`.

### Added
- **Browser debug console and screenshot delivery.** The protected debugger now
  has a Browser tab with recent job state and bounded per-resource load
  metadata. The sidecar captures a latest-step PNG and exposes an owner-scoped
  `browser_use` `screenshot` action; Agentize stores it as a generated user
  file. `ProcessMessageWithGeneratedFiles` lets host chat integrations attach
  screenshots and other files generated during the same turn. Debug artifacts
  strip request/response headers, cookies, POST data, and bodies before
  remaining at rest.
- **Deterministic Core workflows.** `execute_workflow` accepts an exact Core-tool
  DAG, calls no planner LLM, persists every task transition in `workflow_runs`,
  and requests approval for each immediate task. The admin dashboard exposes
  workflow list/detail pages.
- **Scheduled workflow state machines.** `create_workflow_schedule` approves the
  complete fixed DAG once; later scheduled runs execute without a planner LLM
  or per-task approval and cannot dispatch or switch agents.
- **Dedicated schedule memory.** Every prompt/workflow schedule now provisions
  its own titled/tagged session. Prompt schedules bind to one fixed worker;
  `max_runs=1` provides one-shot execution.
- **Durable per-tool approval gate.** `engine.ToolApprovalManager` raises a
  generic `tool_call` review with the exact tool and arguments, waits for the
  shared resolution path, and invokes the executor only after approval.
  Rejections and approval errors fail closed. `CoreHandler.SetToolApprovalManager`
  propagates the gate to Core tools and all current/future worker agents.
- **`inspect_result` tool — deterministic inspection of buffered tool output.**
  When a tool result exceeds `MaxToolResultLength` it is buffered under a
  `result_id`; `inspect_result` pulls back parts of it without an LLM via
  `stats`, `head`/`tail` (default 30 lines), `slice` (line range), `grep`
  (regex with `ignore_case`/`invert`/`context`/`max_matches`), `unique`,
  `sort` (`desc`/`numeric`), and `count` (matches of a pattern like `grep -c`,
  or per-line frequency like `sort | uniq -c`). Output is capped
  (`maxInspectResultChars`) and truncated on a rune boundary. Complements
  `collect_result` (LLM-backed semantic extraction). Both are now exposed
  automatically by `GetTools`, registered by `Engine.RegisterTextTools()`, and
  documented in `engine/user_agent.md` and the truncation notice itself.

### Security
- **`change_session` cross-user session takeover fixed.** The core `change_session`
  tool accepted a model-supplied `session_id`, verified only its agent type, and
  set it as the caller's active session — with no ownership check. Because
  session ids are formatted `{userID}-{agentType}-s{seq}` (guessable), an agent
  acting for one user could switch into another user's session, loading the
  victim's history into its context and routing the caller's messages into the
  victim's session. `changeSessionTool` now rejects sessions whose `UserID`
  differs from the caller (reported as "not found" to avoid existence leaks).
  Defense in depth: `setActiveSessionID` refuses to persist a foreign session,
  and `getOrCreateActiveSession` discards a stored active-session pointer that is
  not owned by the user instead of processing against it.
- **File ownership check tightened.** `getOwnedFileMeta` previously skipped the
  ownership comparison entirely when the caller's user id was empty
  (`userID != "" && meta.UserID != userID`), so a request arriving without a
  user id could read any file by id. It now requires exact `meta.UserID == userID`
  (both empty in single-tenant/no-auth setups, so legitimate reads are unaffected).
- **Per-user isolation of buffered tool output.** `collect_result` and
  `inspect_result` retrieve results only through `getOwnedToolResult`, which
  binds access to the caller's own `__user_id__`/`__session_id__` (injected by
  the engine, not model-controllable) and rejects any `result_id` whose owning
  session or user does not match. A model can no longer read, search, or
  summarize another user's buffered output by passing a foreign `result_id`.
  `CollectResultByID` (which trusts the id-embedded session) is deprecated for
  model-facing dispatch.
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
