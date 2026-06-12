# The Core Agent — the brain of Agentize

> The Core agent (`core/`) is the orchestrator that every user message hits first.
> This document explains what it is, the state it owns, and — in detail — how it
> assembles the **array of system prompts** that drive its routing LLM. File:line
> references point at real code so this stays verifiable. For the end-to-end
> message journey across the whole framework, see [ARCHITECTURE.md](./ARCHITECTURE.md).

## 1. What the Core agent is

`CoreHandler` ([core/core.go:49](../core/core.go)) is a **per-user, dispatch-only
router**. It does not answer most requests itself; it decides *which* specialized
worker agent (low / high / custom tiers in `agentmanager/`) should handle a message
and forwards it. The chosen agent's reply is returned to the user **verbatim** — it
does not loop back into Core's LLM (see ARCHITECTURE §4, "dispatch-only").

What makes the Core the "brain":

- It is the only component that sees **all** of a user's sessions at once. Every
  worker agent only knows its own session; the Core holds the bird's-eye view.
- It owns a dedicated **Core session** per user (`AgentType = core`) whose summary,
  tags and history become long-term memory injected into every future decision.
- Its behavior is almost entirely **prompt-driven**: routing quality is a function
  of the system-prompt array it builds on each turn (§4). Changing Core behavior
  usually means changing that array, not the control flow.

## 2. Key state owned by the Core

`CoreHandler` fields ([core/core.go:49-83](../core/core.go)):

| Field | Purpose |
|-------|---------|
| `sessionHandler` | Access to all sessions + the underlying `store.Store`. |
| `agents` | The `AgentManager`: registered worker agents, their tiers, tools, capabilities. |
| `llmClient` / `llmConfig` | The Core orchestration LLM (default `openai/gpt-5-nano`). |
| `visionLLMClient` | Optional cheaper LLM for image input ([core/vision.go](../core/vision.go)). |
| `coreSessions` | `map[userID]*Session` — in-memory cache of each user's Core session. |
| `userMutexes` / `userProgress` | Per-user serialization + queueing of messages. |
| `coreTools` | `FunctionRegistry` of Core's own tools (web_search, sessions, ban, plan…). |
| `orchestrator` | Optional planning layer; when set, adds `execute_plan` + a Planning prompt. |
| `fileRecorder` | Optional hook to persist files the user sends (wired to `RecordUserFile`). |

The Core session is fetched/created per message by `getOrCreateCoreSession`
([core/session.go:13](../core/session.go)), which prefers the in-memory cache, then
the store's `GetCoreSession`, then creates a fresh one.

## 3. Lifecycle of one message (Core view)

`ProcessMessage` → `processOneMessageCore` ([core/core.go:233](../core/core.go)):

1. Per-user mutex + progress guard (busy → queue) ([core/core.go:213-221](../core/core.go)).
2. Status `Received → Analyzing`; moderation (ban + nonsense) ([core/core.go:256-282](../core/core.go)).
3. Get/create the **Core session**, append the user message, persist it
   ([core/core.go:284-316](../core/core.go)).
4. **Build the system prompts** via `buildSystemPrompts(userID)`
   ([core/core.go:299](../core/core.go) → [core/llm.go:50](../core/llm.go)). ← §4.
5. Assemble messages = system prompts (as `system` role) + conversation, fetch Core
   tools, run the tool loop `processWithTools` ([core/llm.go:118](../core/llm.go)).
6. Append the final answer to the Core session, persist, status `Completed`.

A `RouteTrace` (the routing DAG) is recorded alongside for the debug dashboard
([core/route_trace.go](../core/route_trace.go)).

## 4. The system-prompt array (the heart)

The Core does **not** use a single system prompt. `buildSystemPrompts(userID)`
([core/llm.go:50-103](../core/llm.go)) returns a `[]string`; `buildMessages`
([core/llm.go:105](../core/llm.go)) then emits **one `system` message per entry**,
in order, before the conversation messages. The array, in build order:

| # | Section | Source | Varies by | Stability |
|---|---------|--------|-----------|-----------|
| 1 | **Core Controller** (rules, hard constraints, decision flow) | `core_controller.md`, embedded ([core/core.go:21](../core/core.go)) | nothing | **static** |
| 2 | **Available Agents** (table: name, desc, cost tier, capabilities, knowledge) | `agents.BuildAgentsDescriptionPrompt()` ([agentmanager/prompt.go:29](../agentmanager/prompt.go)) | registered agents | static per deployment |
| 3 | **Registered Agent Tools** (union of all agents' tool names) | `agents.BuildAgentToolsPrompt()` ([agentmanager/prompt.go:89](../agentmanager/prompt.go)) | registered agents | static per deployment |
| 4 | **Core Session Context** (Summary + Tags of *this user's* Core session) | `buildCoreSessionContext()` ([core/session.go:109](../core/session.go)) | user, summarization | **dynamic** |
| 5 | **Agent Session Contexts** (Summary + Tags of each agent's active session) | `agents.BuildAllSessionContextsPrompt()` ([agentmanager/prompt.go:335](../agentmanager/prompt.go)) | user, summarization | **dynamic** |
| 6 | **User Files** (compact table of the user's uploaded/generated files) | `buildUserFilesPrompt()` ([core/llm.go](../core/llm.go)) | user, file uploads | **dynamic** |
| 7 | **Current Active Sessions** (which session is active per agent) | `agents.BuildActiveSessionsPrompt()` ([agentmanager/prompt.go:230](../agentmanager/prompt.go)) | user, session changes | **dynamic** |
| 8 | **Sessions list** (for `change_session`) | `sessionHandler.GetSessionsPrompt(userID)` ([model/session_handler.go:474](../model/session_handler.go)) | user, session changes | **dynamic** |
| 9 | **Planning** (decision rules for `execute_plan`) | `planning.CorePrompt()` ([planning/core_prompt.go](../planning/core_prompt.go)) | planning enabled | static, optional |

Two important properties:

- **Static-first ordering is deliberate.** Sections 1–3 (and 9) are byte-stable
  across messages for a given deployment, so provider-side **prompt caching**
  (OpenAI/Anthropic) can cache that prefix; the per-user dynamic sections (4–8) come
  later and change without invalidating the cached prefix. `callLLM`
  ([core/llm.go:18](../core/llm.go)) logs `cache=` tokens so you can confirm hits.
- **Sections 4–8 are the Core's memory of the user**, and assembling them is the
  hottest read path in the Core. The array is **memoized per user** by
  `generateSystemPrompt` ([core/llm.go](../core/llm.go)) for
  `CoreHandlerConfig.SystemPromptCacheTTL` (default **10 minutes**), wrapping the
  uncached `buildSystemPrompts` builder. The cache is rebuilt when it expires, when
  the user's Core session was re-summarized since it was built
  (`memorySummarizedSince`), or after `invalidateSystemPrompt` is called — which
  happens on `create_session` / `change_session` ([core/tools.go](../core/tools.go))
  and via the exported `InvalidateSystemPromptCache` (wired to summarization, §5).
  Cache lookups are observable via `agentize_system_prompt_cache_total{result=hit|miss|stale}`.
- **Size budget.** `buildSystemPrompts` assembles sections through a `promptBudget`
  capped at `CoreHandlerConfig.MaxSystemPromptSize` (default **120000 chars**).
  Sections 1–3 (controller, agent descriptions, agent tools) are required; the
  per-user sections are optional and dropped — logged and counted in
  `agentize_system_prompt_sections_dropped_total{section}` — when they would push
  the total past the cap, so one user's huge history cannot inflate every message's
  token cost.

### Section 6 — User Files (handing files to a worker agent)

`buildUserFilesPrompt` ([core/llm.go](../core/llm.go)) type-asserts the store to
`GetUserFilesByUser(userID)` and renders a compact table (File ID, Name, Type, Size,
Source) of the user's uploaded/generated files, capped at
`CoreHandlerConfig.MaxUserFilesInPrompt` (default 50). It is **metadata-only** — no extra LLM calls — and includes a file's existing
`Summary` when present. The Core is instructed (in `core_controller.md`) to pass a
file's **ID and name** in the `call_agent_*` message rather than pasting bytes; the
worker agent then reads it on demand via its own file tool. Files are user-scoped, so
any of the user's agents can resolve the ID.

### How a section is built (example: Core Session Context)

`buildCoreSessionContext` ([core/session.go:109](../core/session.go)) returns empty
when the session has neither a summary nor tags; otherwise it emits:

```
# Core Session Context
This is a continuation of a previous conversation. ...
## Summary of Previous Conversation
<session.Summary>
## Topics Discussed
<tag, tag, ...>
```

The agent-side equivalent, `BuildSessionContextPrompt`
([agentmanager/prompt.go:287](../agentmanager/prompt.go)), does the same per agent
and is aggregated by `BuildAllSessionContextsPrompt`. **All of these read a single
`session.Summary` string** — the field that summarization maintains (§5).

## 5. Memory: how summaries get into the prompt

The Core's long-term memory of a user is the **`Summary` + `Tags`** on each session,
produced in the background by the `SessionScheduler`
([engine/schedules.go](../engine/schedules.go)). Today the model holds a single
summary string per session:

- `Session.Summary string` ([model/session.go:77](../model/session.go)).
- Summarization is **incremental/merge-style**: `summarizeSession`
  ([engine/schedules.go:639](../engine/schedules.go)) feeds the *previous* summary +
  new messages to the LLM and replaces `Summary` with a full merged result
  (prompt: `DefaultSummarizationPrompts` [engine/schedules.go:104](../engine/schedules.go)).
  Raw messages are not lost — older ones move to `ArchivedMsgs` while a rolling
  window of the most recent stays in `Msgs` (`splitRollingWindow`
  [engine/schedules.go:617](../engine/schedules.go)).
- Eligibility: first summary after `FirstSummarizationThreshold` (5) messages, then
  subsequent ones gated by message count + time
  ([engine/schedules.go:553](../engine/schedules.go)).

So the data flow is: **messages (detail) → scheduler → `Summary` (snapshot) →
sections 4–5 of the Core's system-prompt array → routing decision.**

**Keeping the Core's cached prompt fresh.** Because summarization runs in the
background and changes sections 4–5, the scheduler exposes
`SessionSchedulerConfig.OnSessionSummarized(userID, sessionID)`
([engine/schedules.go](../engine/schedules.go)), fired after a successful summary.
A host running the multi-agent Core should wire it to
`CoreHandler.InvalidateSystemPromptCache(userID)` so the next message rebuilds that
user's memory immediately instead of waiting out the 10-minute TTL. (The Core also
self-heals for its *own* Core session via `memorySummarizedSince`, but the hook is
what covers the worker agents' sessions.)

> **Roadmap (Stage 2).** `Summary` is a single merged string today. A planned change
> turns it into an **append-only array of snapshots** (`Summaries []string`): each
> summarization appends one delta snapshot instead of overwriting, and the Core
> renders the whole timeline in sections 4–5. That touches the `Session` model and
> both stores (a DB migration), so it is sequenced after the Stage 1 cache/files/UI
> work described here.

## 6. Where the Core surfaces in the debug UI

- **Sessions / Users**: each user's sessions (including the `core` session) are
  listed with title, summary, tags, model on the user detail page
  ([debuger/pages/users.go:386](../debuger/pages/users.go)).
- **Route traces**: the per-message routing DAG at `/agentize/debug/routes`.
- **Summarization logs**: every summarization run is logged with before/after state.
- **System Info**: backend + counts panel on the dashboard ([systeminfo.go](../systeminfo.go)).

- **Core Agent (Brain) panel**: the user detail page shows a dedicated card with the
  memory the Core operates on for that user — its Core session, model, summary, tags,
  last-summarized time, message count, and known-document count
  (`renderCoreBrainCard` [debuger/pages/users.go](../debuger/pages/users.go)).

## 7. Extending the Core — where changes land

| You want to change… | Touch |
|----------------------|-------|
| Core's hard rules / routing policy | `core/core_controller.md` (embedded prompt) |
| Which prompt sections exist & their order | `buildSystemPrompts` ([core/llm.go:50](../core/llm.go)) |
| How a user's memory renders into the prompt | `buildCoreSessionContext` ([core/session.go:109](../core/session.go)) + `agentmanager/prompt.go` builders |
| How memory is produced | `summarizeSession` + `DefaultSummarizationPrompts` ([engine/schedules.go](../engine/schedules.go)) |
| The memory shape itself | `Session` struct ([model/session.go:39](../model/session.go)) + `store/` (SQLite/Mongo persistence) |
| Files the Core knows about | `store.GetUserFilesByUser` (via type-assertion on `GetStore()`, as in [core/session.go:52](../core/session.go)) |
| Core "brain" debug view | `RenderUserDetail` ([debuger/pages/users.go:169](../debuger/pages/users.go)) |

### Wiring the prompt cache invalidation (host responsibility)

The per-user prompt cache (§4) is invalidated automatically on
`create_session` / `change_session`, and self-heals when the user's *own* Core
session is re-summarized. To also reflect **worker-agent** summarization immediately,
the host wires the scheduler hook to the Core — one line where it constructs both:

```go
schedulerConfig.OnSessionSummarized = func(userID, _ string) {
    coreHandler.InvalidateSystemPromptCache(userID)
}
```

Without this, worker-agent summary changes still appear, but only once the 10-minute
TTL expires. Two invariants worth preserving:

- Keep the static prefix (sections 1–3) byte-identical regardless of cache state, so
  provider-side prompt caching keeps working independently of the app cache.
- Any new dynamic section added to `buildSystemPrompts` is covered by the cache for
  free, but if it can change *without* a summarization or session event, add an
  `invalidateSystemPrompt(userID)` call at its mutation point — as the Core's image
  upload path does ([core/vision.go](../core/vision.go)) so a just-received file
  appears in the User Files section on the same message rather than after the TTL.

---

*Generated as a code-grounded overview of the Core agent. When the prompt array or
the summarization/memory model changes, update §4–§5 and the file:line anchors.*
