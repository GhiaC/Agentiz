# Planning

When this section is present, the **execute_plan** tool is available. Use it only for multi-step or dependency-ordered tasks.

> Core itself is dispatch-only and does not iterate on an Agent's reply. So multi-step work has exactly two homes: **`execute_plan`** (an explicit, auditable step graph orchestrated here) or **a single higher-tier Agent** that does the reasoning internally. Pick `execute_plan` when the steps span several tools/agents and order matters; pick `call_agent_{name}` (high tier) when one capable Agent can own the whole task end-to-end.

## What it is

Planning builds an execution **Plan** (a DAG of steps) and runs it step-by-step. Steps can depend on prior steps. Final output is a single text response.

**Step types:** `llm_call` (model call), `tool_call` (tool/API), `agent_delegate` (hand off to an agent), `parallel`, `conditional`, `collect`, `human_review` (if supported). Execution is in-process (local) or via a workflow engine; plans are stored and visible in the Plans debug dashboard.

## When to use execute_plan

- User request is **explicitly multi-step** (e.g. “fetch X, compare with Y, then summarize”).
- **Order matters**: step B must use the result of step A (e.g. search first, then summarize the search result).
- You want **structured, auditable execution** (e.g. visible in Plans debug).

**When NOT to use**

- **Single question or one-off task** → answer directly or use `call_agent_{name}`.
- **Domain-specific agent work** (any capability owned by a specialized agent) → delegate to the right agent via `call_agent_*`, not execute_plan.
- **One agent is enough** and there is no need for a step graph → use `call_agent_{name}` only.

## Tool

| Tool | Input | Output |
|------|--------|--------|
| `execute_plan` | `message` (string): the user’s request or goal | Final text output of the plan run |

## Decision checklist

1. **Multi-step or clear step dependencies?** → `execute_plan`
2. **Simple or single-agent task?** → `call_agent_{name}`
3. **A capability owned by a specific agent?** → Delegate to that agent; do **not** use `execute_plan`
