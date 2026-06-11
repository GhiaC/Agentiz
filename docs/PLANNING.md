# Agentize — Planning (DAG plan orchestration)

> How the optional **planning** subsystem turns a single user goal into a small DAG of
> steps, runs them in dependency order, and exposes the result. File:line references
> point at real code so this stays verifiable.

Planning is **opt-in**. When disabled (the default) none of this is wired and the
`execute_plan` tool is not even offered to the Core LLM.

## 1. What it is & when to use it

The Core LLM is dispatch-only and a worker agent answers in one shot. Planning fills the
gap for **multi-step work with explicit dependencies** — e.g. *search → summarize*, or
*run three agents in parallel → compare*. The Core LLM decides to call `execute_plan`;
an `Orchestrator` then asks a `Planner` to design a `Plan` (a DAG of `Step`s) and a
`Runner` to execute it.

**Use it** for ordered/parallel multi-step tasks. **Don't** for a single question or
work a single agent already owns — see the LLM-facing checklist in
[`planning/prompt.md`](../planning/prompt.md) (the `execute_plan` tool description) and
the plan schema in [`planning/planner_prompt.md`](../planning/planner_prompt.md).

## 2. Architecture

```mermaid
flowchart TB
    Core["core/ — execute_plan tool"] -->|Execute(input)| Orch
    subgraph planning
      Orch["Orchestrator (planning.go)"]
      Planner["Planner — LLMPlanner | ChainPlanner"]
      Runner["Runner — LocalRunner | TemporalRunner"]
      Store["PlanStore — MemoryStore (+optional Persister)"]
      Obs["Observer(s) — lifecycle events"]
    end
    Orch -->|CreatePlan| Planner
    Orch -->|Run| Runner
    Orch --> Store
    Runner --> Obs
    Runner -->|llm_call| LLM["LLM client"]
    Runner -->|tool_call| Tools["ToolExecutor → Core tools"]
    Runner -->|agent_delegate| AG["worker agent"]
```

`Orchestrator.Execute` ([planning/planning.go:75](../planning/planning.go)) is the entry:

1. `planner.CreatePlan(ctx, input)` → a `*Plan`.
2. Assign a sequential ID (`plan_{userID}_{seq}`) and `store.Save`.
3. Merge orchestrator + per-call observers, build run options (observer, store, LLM
   client, tool executor).
4. `runner.Run(ctx, plan)` → `*PlanResult`.

`LocalRunner.Run` topologically sorts the steps ([planning/dag.go](../planning/dag.go)),
then loops: pick `ReadySteps` (deps satisfied) → `RunStep` → update store + notify
observers, until all steps finish or one fails.

## 3. Step types

A `Step` ([planning/types.go:77](../planning/types.go)) has an `id`, `type`, `name`,
`config`, and `depends_on`. The `LocalRunner` executes these types:

| Type | Executed? | What it does | Key `config` |
|------|-----------|--------------|--------------|
| `llm_call` | ✅ | One LLM turn (reason/summarize/compose) | `prompt`, `model` |
| `tool_call` | ✅ | Run a registered tool via the `ToolExecutor` | `tool_name`, `tool_args` |
| `agent_delegate` | ✅ | Hand off to a worker agent | `agent_type`, `agent_input` |
| `parallel` | ✅ | Run independent `sub_steps` concurrently | `sub_steps` |
| `conditional` | ✅ | Evaluate `condition`; skip the steps of the non-selected branch | `condition`, `branches` |
| `collect` | ✅ | Fan-in: aggregate the outputs of its dependencies (optionally LLM-synthesize) | `depends_on`, optional `prompt` |
| `human_review` | ✅ | Gate on a human decision (pluggable; auto-approves when no reviewer is wired) | `depends_on` |

### Conditional, collect & human_review semantics

- **`conditional`** evaluates its `Condition{field, operator, value}` to a boolean and
  selects branch key `"true"`/`"false"`. Steps listed under the *other* branch key in
  `branches` are marked **skipped**; `PropagateSkips`
  ([dag.go](../planning/dag.go)) then cascades the skip to anything that depended only on
  them. `field` reads the plan input (`""`/`"input"`) or a named step's output; operators
  include `eq`/`ne`/`contains`/`not_contains`/`empty`/`not_empty`/`gt`/`lt`. **Contract:**
  a branch's steps should `depends_on` the conditional and each branch list should be
  complete.
- **`collect`** is the join counterpart to `parallel`: it concatenates the (non-skipped)
  outputs of its `depends_on` steps; with a `prompt` and an LLM it synthesizes them
  instead. A skipped dependency is a *resolved* dependency (it just contributes nothing),
  so a collect after a conditional runs with whichever branch survived.
- **`human_review`** submits its dependencies' output (or the plan input) to a
  `ReviewFunc` wired with `WithLocalReviewer`; rejection fails the step. With no reviewer
  it auto-approves and passes the content through, so review-gated plans still run.

## 4. Per-step reliability: timeout & retries (opt-in)

`StepConfig` carries `Timeout` and `MaxRetries` ([planning/types.go:53](../planning/types.go)).
`RunStep` ([planning/local_runner.go:246](../planning/local_runner.go)) honors both:

- **`Timeout > 0`** wraps the step in `context.WithTimeout` (the plan-level `WithTimeout`
  still applies via the parent context).
- **`MaxRetries > 0`** retries a failed step up to that many times, with a fixed
  `stepRetryBackoff` (250ms) between attempts and an early exit if the context ends.

Both default to off (`0`) — a step is attempted exactly once with no extra timeout, so
existing plans are unaffected. Retries re-execute the step handler, so only set
`MaxRetries` on **idempotent** steps.

## 5. Lifecycle & status

`Plan.Status` and `Step.Status` move through:

```
pending → running → completed | failed | skipped(step) / cancelled(plan)
```

A step that fails sets `Step.Error` and fails the whole plan (no partial-completion mode
today). `Plan.Error`/`Plan.Output` capture the final outcome; `StepResult` records
`Output`, `Duration`, and `TokensUsed` per step.

**Cancellation** (`Cancel`, [planning/local_runner.go](../planning/local_runner.go)) is
currently **mark-only**: it sets `PlanCancelled` in the store but does not interrupt an
in-flight LLM/tool call. Prefer a per-step `Timeout` to bound runaway steps.

## 6. Runners & planners

| Runner | Use | Status |
|--------|-----|--------|
| `LocalRunner` ([local_runner.go:69](../planning/local_runner.go)) | In-process; uses the Engine/LLM client + `ToolExecutor` | Primary |
| `TemporalRunner` ([temporal_runner.go](../planning/temporal_runner.go)) | Delegate to Temporal workflows | Needs a host-supplied `TemporalAdapter`; not wired by default |

| Planner | Use |
|---------|-----|
| `LLMPlanner` ([llm_planner.go:48](../planning/llm_planner.go)) | Ask an LLM to emit the plan JSON from the goal + available tools |
| `ChainPlanner` ([chain_planner.go](../planning/chain_planner.go)) | Deterministic LLM-then-tool chain (no planning LLM call) |

`Planner.Replan` exists on the interface but is **not yet invoked** by the Orchestrator.

## 7. Persistence — important

By default the Orchestrator uses an in-memory `MemoryStore`
([planning/planning.go:69](../planning/planning.go)) — **plans do not survive a restart**,
so `/agentize/debug/plans` is empty after a reboot until new plans run.

`MemoryStore` accepts an optional `Persister` via `WithPersister`
([planning/memory_store.go:15](../planning/memory_store.go)) to load/save plans against a
durable backend, but Agentize does not ship a DB-backed `Persister` — wire one (against
your `SessionStore`) and pass it with `planning.WithOrchestratorStore(...)` if you need
durable plan history. `EnsurePlanningSeed`
([agentize.go:429](../agentize.go)) writes a few template plans on first run so the
dashboard isn't empty in a fresh deployment.

## 8. Enabling planning

```go
planner := planning.NewLLMPlanner(coreLLMClient, "openai/gpt-5")
runner  := planning.NewLocalRunner(engine, functionRegistry)
ag.UsePlanning(planner, runner)        // agentize.go:410 → core.UsePlanning (core.go:144)
_ = ag.EnsurePlanningSeed(context.Background())
```

`UsePlanning` registers the `execute_plan`, `get_plan_status`, and `cancel_plan` Core
tools (only when an orchestrator is set — [core/tools.go](../core/tools.go)). The tool
implementations live in [core/planning.go](../core/planning.go):
`executePlanTool` (119), `getPlanStatusTool` (203), `cancelPlanTool` (231). The
single-shot facade `ProcessMessageWithPlanning`
([agentize.go:437](../agentize.go)) runs the orchestrator directly, bypassing the Core
router.

## 9. Metrics

| Metric | Type | Labels |
|--------|------|--------|
| `agentize_planning_runs_total` | counter | `status` (ok\|error) — one per `execute_plan` call |
| `agentize_planning_steps_total` | counter | `status` (ok\|error) — one per **executed** step |

Recorded in `executePlanTool` ([core/planning.go](../core/planning.go)): each step is now
counted with its **real** status (completed → `ok`, failed → `error`); steps that never
ran (pending/skipped) are not counted. See [METRICS.md](./METRICS.md).

```promql
# Plan success rate
sum(rate(agentize_planning_runs_total{status="ok"}[15m]))
  / clamp_min(sum(rate(agentize_planning_runs_total[15m])), 1)

# Step failure rate
sum(rate(agentize_planning_steps_total{status="error"}[15m]))
  / clamp_min(sum(rate(agentize_planning_steps_total[15m])), 1)
```

## 10. Debug dashboard (`/agentize/debug/plans`)

Served by [debuger/pages/plans.go](../debuger/pages/plans.go) from the `PlanStore`
(`List`/`Get`) — **not** the `DebugStore` DB (so it reflects whatever the in-memory store
holds; see §7).

- **List**: Plan ID, User, Session, Status (badge), step count, Created/Updated.
- **Detail**: plan metadata (input, output, error, timestamps), the planning system
  prompts, and a **Steps** table: ID, Type, Name, Status, **Depends On** (the DAG edges),
  Started, Duration, **Tokens**, and **Result / Error** (the step's failure reason in red
  when it failed, otherwise an output preview).

## 11. Current limitations (roadmap)

- In-memory plan store by default — no durable history without a host `Persister` (§7).
- `Cancel` is mark-only and does not interrupt running steps (§5).
- `conditional` inside a `parallel` sub-step is unsupported (it mutates shared plan state).
- `Planner.Replan` and `TemporalRunner` exist but are not reachable from the default wiring.
- No per-step-type or duration metrics yet — only run/step counters (§9).

---

*Keep this in sync with `planning/` when step types, the runner, the store contract, or
the debug page change.*
