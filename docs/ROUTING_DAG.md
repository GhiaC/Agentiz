# Agentize — Routing DAG (Core decision & forward trace)

After the Core processes **each user message**, it records a **routing trace**: a
directed acyclic graph (DAG) of exactly what it decided and where it forwarded the
message. Every LLM turn, every tool call, and every dispatch/escalation to a worker
agent becomes a node, ending at the response sent to the user.

The trace is **persisted to the database** (SQLite and MongoDB, at parity) and
**visualized** on the debug dashboard at `/agentize/debug/routes`.

> Scope: this is the **Core router's** decision/forward graph. A worker agent's own
> internal LLM/tool loop is a separate concern (its tool calls are already persisted
> as `ToolCall` records and shown on the Tool Calls page). The DAG shows how the Core
> got from the user message to an answer — including which agent it handed off to.

## What it captures

```
👤 User message
   └─ 🧠 Decision 1            (one Core LLM turn; model, tokens, finish reason)
        ├─ 🔧 Tool call         (web_search, update_status, create_session, …)
        ├─ 📑 Plan              (execute_plan → planning layer)
        └─ 🧠 Decision 2
             └─ 🤖 Agent dispatch   (call_agent_<name>; the routing decision)
                  └─ ⏫ Escalation   (ESCALATE: → higher-tier agent)
                       └─ 💬 Response to user   (returned verbatim from the agent)
```

Node types (`model.RouteNodeType`):

| Type | Meaning |
|------|---------|
| `user_message` | the root — the message that entered the Core |
| `decision` | one Core LLM turn (carries model, total tokens, duration, finish reason) |
| `tool_call` | a non-routing Core tool (`web_search`, `update_status`, sessions, `ban_user`, …) |
| `agent_dispatch` | a forward to a worker agent via `call_agent_<name>` — the Core's routing decision |
| `escalation` | a forward to a higher cost tier after an `ESCALATE:` reply |
| `plan` | an `execute_plan` invocation handed to the planning layer |
| `response` | the terminal answer to the user |

Each node carries a status (`ok` / `error` / `blocked` / `skipped`), and
dispatch/escalation nodes carry the agent name plus per-agent timing.

### The DAG shape

- Decisions form a **spine**: `user → decision1 → decision2 → …` (each LLM turn feeds
  the previous turn's tool results back in).
- Tool / dispatch / plan nodes **branch off** the decision that invoked them.
- An escalation hangs off the dispatch it escalated from.
- The response hangs off the final decision — or, when the Core dispatched, off the
  **agent node** whose answer it returned verbatim. That edge ("returns") is the
  visual signature of Core's **dispatch-only** rule (see [ARCHITECTURE.md](./ARCHITECTURE.md)).

Because the graph is faithful to what happened, it also surfaces things worth
noticing — e.g. a single Core turn that emits **two** `call_agent_*` calls shows the
first as a real dispatch and the second as a `skipped` dispatch node: Core runs only
the first agent (its answer is returned verbatim) and short-circuits the rest rather
than paying for a worker-agent run whose answer would be discarded.

## Data model

`model/route_trace.go`:

```go
type RouteTrace struct {
    TraceID   string        // {CoreSessionID}-rt{seq}, e.g. user42-core-s0001-rt0001
    SessionID string        // the Core session
    UserID    string
    Message   string        // user message (rune-truncated to 2000)
    Response  string        // final response (rune-truncated)
    Nodes     []RouteNode
    Edges     []RouteEdge
    Status    string        // "ok" | "error"
    Error     string
    TotalTokens int
    DurationMs  int64
    CreatedAt time.Time
}
```

IDs are sequential per Core session (`Session.GenerateRouteTraceID`, backed by a new
`Session.TraceSeq`), matching the existing `-m`/`-t`/`-l` ID conventions.

## How it is built

A `model.RouteTraceBuilder` assembles the DAG incrementally as the message is
processed. It rides in the `context.Context` (like the user id) so the LLM loop and
the tool layer record into the same trace without threading a builder through every
signature. Every builder method is **nil-safe**, so when tracing is disabled the
instrumentation calls are no-ops.

| Where | What it records |
|-------|-----------------|
| `core/core.go` (`processOneMessageCore`) | creates the builder for the Core session, attaches it to the context, and persists on every exit path (`defer`) |
| `core/llm.go` (`processWithTools`) | a `decision` node per LLM turn; the terminal `response` node (direct answer or dispatched) |
| `core/tools.go` (`executeCoreTool`) | a `tool_call` / `plan` node per non-routing tool; a `blocked` node when a callback denies a tool |
| `core/tools.go` (`runCoreToolImpl`) | the `agent_dispatch` and `escalation` nodes, with per-agent timing |
| `core/tools.go` (`skipRedundantAgentCall`) | a `skipped` `agent_dispatch` node for a second `call_agent_*` in a turn already dispatched (no agent run) |
| `core/route_trace.go` | the context plumbing + `persistRouteTrace` (persist + metrics + log) |

Moderation short-circuits (ban / nonsense) return **before** a Core session is built,
so they are not traced here; they remain visible via `moderation_*` metrics and the
message's `IsNonsense` flag.

## Persistence

`store.Store` gains a writer and `debuger.DebugStore` the readers:

```go
PutRouteTrace(*model.RouteTrace) error                       // store.Store
GetRouteTraceByID(id string) (*model.RouteTrace, error)      // (nil, nil) when absent
GetRouteTracesBySession(sessionID string) ([]*RouteTrace, error)  // newest first
GetRouteTracesByUser(userID string) ([]*RouteTrace, error)        // newest first
GetAllRouteTraces() ([]*RouteTrace, error)                        // newest first
```

Both backends store the full DAG as JSON (`route_traces` table / collection) with
`session_id` / `user_id` denormalized for indexed lookup, mirroring how summarization
logs are stored. `DeleteUserData` removes a user's traces. The shared
`store/conformance_test.go` suite asserts SQLite/MongoDB parity for all of the above.

## Visualization

`/agentize/debug/routes` lists recent traces (when, user, session, message, the agent
it routed to, node count, tokens, duration, status). Opening one renders:

- an **interactive ECharts graph** of the DAG (drag/zoom, hover a node for full
  detail, color-coded by node type),
- the user message and final response,
- an ordered **Steps** table (also the no-JS fallback).

All user-derived text is HTML-escaped; the graph payload is embedded in a
non-executable `<script type="application/json">` island that `json.Marshal`
HTML-escapes, so message text cannot break out of the page.

## Metrics

| Metric | Type | Labels |
|--------|------|--------|
| `agentize_route_trace_recorded_total` | counter | `status` (ok/error) |
| `agentize_route_trace_nodes` | histogram | — (nodes per DAG) |

`error` covers both a failed message and a failed trace persist. See [METRICS.md](./METRICS.md).

## Logging

On every recorded trace the Core logs a one-line summary, e.g.:

```
[CoreHandler] 🧭 Route trace recorded | TraceID: user42-core-s0001-rt0003 | Nodes: 6 | Dispatched: low,high | Status: ok | Tokens: 730 | DurationMs: 4210
```

Persistence failures log a warning and are swallowed — tracing never fails a user
request.
