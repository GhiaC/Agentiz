# Agentize — Metrics & Monitoring

Agentize ships **built-in Prometheus instrumentation** covering every metered
activity: message processing, LLM calls, tool calls, agent routing/escalation,
backup-LLM fallbacks, the summarization scheduler, planning, knowledge file opens
and moderation.

## Exposing the endpoint

Metrics live on the default Prometheus registry. `RegisterRoutes` mounts them at:

```
GET /agentize/metrics
```

If you don't use the Agentize router, mount the handler yourself:

```go
import "github.com/ghiac/agentize/metrics"

router.GET("/metrics", metrics.GinHandler())   // gin
// or any net/http mux:
mux.Handle("/metrics", metrics.Handler())
```

Because it uses the default registry, the endpoint also exposes Go runtime and
process metrics (`go_goroutines`, `process_resident_memory_bytes`, …).

## Scrape config

```yaml
  - job_name: 'agentize'
    metrics_path: /agentize/metrics
    static_configs:
      - targets: ['127.0.0.1:8080']   # your app's HTTP port
```

A ready-made Grafana dashboard is in [`grafana/agentize-dashboard.json`](./grafana/agentize-dashboard.json).

## Metric catalogue (prefix `agentize_`)

### Messages (Core router + per-agent Engine)
| Metric | Type | Labels |
|--------|------|--------|
| `message_processed_total` | counter | `layer` (core/agent), `status` (ok/error) |
| `message_duration_seconds` | histogram | `layer` |
| `message_in_progress` | gauge | `layer` |
| `message_queued_total` | counter | `layer` |

### LLM calls
| Metric | Type | Labels |
|--------|------|--------|
| `llm_calls_total` | counter | `purpose` (core/agent/vision/summary/moderation/backup), `model`, `status` |
| `llm_call_duration_seconds` | histogram | `purpose`, `model` |
| `llm_tokens_total` | counter | `purpose`, `model`, `type` (input/output/cached) |

### Tools & agent routing
| Metric | Type | Labels |
|--------|------|--------|
| `tool_calls_total` | counter | `layer` (core/agent), `tool`, `status` |
| `tool_call_duration_seconds` | histogram | `layer`, `tool` |
| `agent_routing_total` | counter | `agent`, `status` |
| `agent_escalations_total` | counter | `agent` |

### Core system prompt (assembly cache + size budget)
| Metric | Type | Labels |
|--------|------|--------|
| `system_prompt_cache_total` | counter | `result` (hit/miss/stale) — per-user assembled-prompt cache lookups in `generateSystemPrompt` |
| `system_prompt_sections_dropped_total` | counter | `section` — optional prompt sections dropped because the assembly hit `MaxSystemPromptSize` |

A growing `stale` rate means summarization/invalidations outpace the TTL; any
non-zero `sections_dropped_total` means some users' context (e.g. session
summaries or the user-files catalog) is being cut — consider raising
`CoreHandlerConfig.MaxSystemPromptSize`.

### Routing trace (Core decision/forward DAG)
| Metric | Type | Labels |
|--------|------|--------|
| `route_trace_recorded_total` | counter | `status` (ok/error) — one per Core-processed message; `error` = failed message or failed persist |
| `route_trace_nodes` | histogram | — (nodes per DAG: decisions + tool calls + forwards + response) |

See [ROUTING_DAG.md](./ROUTING_DAG.md) for the trace this comes from and the
`/agentize/debug/routes` visualization.

### Backup LLM chain
| Metric | Type | Labels |
|--------|------|--------|
| `backup_llm_total` | counter | `provider`, `model`, `status` |

### Session-summarization scheduler (background worker)
| Metric | Type | Labels |
|--------|------|--------|
| `scheduler_runs_total` | counter | `status` (ok/error/interrupted) |
| `scheduler_run_duration_seconds` | histogram | — |
| `scheduler_sessions_scanned` | histogram | — |
| `scheduler_summaries_total` | counter | `status` |
| `scheduler_summary_duration_seconds` | histogram | — |
| `scheduler_running` | gauge | — |

### Knowledge, moderation, planning
| Metric | Type | Labels |
|--------|------|--------|
| `knowledge_file_opens_total` | counter | `status` |
| `moderation_checks_total` | counter | `result` (ok/nonsense/banned/error) |
| `moderation_bans_total` | counter | `reason` (nonsense/offensive/manual) |
| `planning_runs_total` | counter | `status` (ok/error) — one per `execute_plan` call |
| `planning_steps_total` | counter | `status` (ok/error) — one per **executed** step (completed→ok, failed→error; pending/skipped not counted) |

See [PLANNING.md](./PLANNING.md) for the planning subsystem these come from.

### User files (file manager)
| Metric | Type | Labels |
|--------|------|--------|
| `file_operations_total` | counter | `operation` (list/read/grep/save/edit/edit_image/upload), `status` (ok/error) |
| `file_operation_duration_seconds` | histogram | `operation` |
| `file_bytes_total` | counter | `direction` (stored/read) |

These cover the `manage_files` tool actions plus inbound uploads. The per-tool
success/latency is also visible via `tool_calls_total{tool="manage_files"}`; the
`file_*` metrics add the per-**operation** breakdown and byte volume.

### Image editing
| Metric | Type | Labels |
|--------|------|--------|
| `image_edits_total` | counter | `model`, `status` (ok/error) |
| `image_edit_duration_seconds` | histogram | `model` |
| `image_edit_bytes_total` | counter | `direction` (input/output) |

Emitted by the built-in OpenRouter image editor (`imageedit/`) on every
`edit_image` call — see [the image-edit flow](#image-editing-flow) below.

### Summarization (detail beyond `scheduler_*`)
| Metric | Type | Labels |
|--------|------|--------|
| `summarization_runs_total` | counter | `type` (first/subsequent/immediate), `status` (ok/failed/offensive/empty) |
| `summarization_input_messages` | histogram | — (messages fed to the summarizer) |
| `summarization_messages_archived` | histogram | — (evicted from the rolling window) |
| `summarization_messages_retained` | histogram | — (kept active in the rolling window) |
| `summarization_summary_chars` | histogram | — (resulting summary length) |
| `summarization_summary_growth_chars` | histogram | — (delta vs previous; <0 = compaction) |
| `summarization_tokens_total` | counter | `type` (prompt/completion) |
| `summarization_offensive_total` | counter | — |

Dashboard: [`grafana/agentize-summarization-dashboard.json`](./grafana/agentize-summarization-dashboard.json).

#### Summarization behavior (since the rolling-window change)
- **Rolling window:** summarization no longer empties the active conversation. The
  most recent `SchedulerConfig.RetainRecentMessages` messages (default **10**) stay
  in `Msgs`; only older messages move to `ArchivedMsgs`. Messages rotate out one
  window at a time instead of the session going suddenly blank.
- **Append-style summary:** the summary is *merged*, not replaced. The model keeps
  every previously captured specific, adds only new specifics, and updates a fact
  only when the new conversation corrects it (soft cap ~800 chars with compaction).

## Example PromQL

```promql
# Message throughput by layer
sum by (layer) (rate(agentize_message_processed_total[5m])) * 60

# p95 Core message latency
histogram_quantile(0.95, sum by (le) (rate(agentize_message_duration_seconds_bucket{layer="core"}[10m])))

# Token burn per model
sum by (model, type) (rate(agentize_llm_tokens_total[5m]))

# Backup-LLM fallback rate (how often the primary failed over)
sum by (provider) (rate(agentize_backup_llm_total{status="ok"}[15m]))

# Scheduler: summaries produced per cycle, and cycle duration p95
sum(rate(agentize_scheduler_summaries_total{status="ok"}[1h]))
histogram_quantile(0.95, rate(agentize_scheduler_run_duration_seconds_bucket[1h]))

# Agent escalation ratio
sum(rate(agentize_agent_escalations_total[30m])) / sum(rate(agentize_agent_routing_total[30m]))

# Routing DAGs recorded per minute, and median DAG size (decision complexity)
sum by (status) (rate(agentize_route_trace_recorded_total[5m])) * 60
histogram_quantile(0.5, sum by (le) (rate(agentize_route_trace_nodes_bucket[1h])))

# File operations by action (/min), and p95 latency
sum by (operation, status) (rate(agentize_file_operations_total[5m])) * 60
histogram_quantile(0.95, sum by (le, operation) (rate(agentize_file_operation_duration_seconds_bucket[10m])))

# File bytes moved (stored vs read, bytes/s)
sum by (direction) (rate(agentize_file_bytes_total[5m]))

# Bans applied by reason (/h)
sum by (reason) (rate(agentize_moderation_bans_total[1h])) * 3600

# Image edits: rate by model & status, and p95 latency
sum by (model, status) (rate(agentize_image_edits_total[15m])) * 60
histogram_quantile(0.95, sum by (le, model) (rate(agentize_image_edit_duration_seconds_bucket[1h])))

# Image edit failure ratio
sum(rate(agentize_image_edits_total{status="error"}[1h]))
  / clamp_min(sum(rate(agentize_image_edits_total[1h])), 1)
```

## Image editing flow

A user sends an image, then asks to change it — the model edits it and stores the
result as a **new** file (the original is kept).

```
ProcessMessageWithImage ─ records the upload as a UserFile (FileSourceUploaded)
        │                   [metrics: file_operations_total{operation="upload"}]
        ▼
agent: manage_files action=list → finds the file_id (scoped to the user)
        │
        ▼
agent: manage_files action=edit_image (file_id, instruction)
        │   Engine.EditImageFile → ImageEditor(bytes, mime, instruction)
        │   └─ OpenRouter editor (imageedit/): POST /chat/completions with
        │      modalities=["image","text"]; reads message.images[0] data URL
        │      [metrics: image_edits_total{model,status}, image_edit_duration_seconds,
        │       image_edit_bytes_total{direction}; logs: "[imageedit] …"]
        ▼
   saves edited bytes as a NEW UserFile (FileSourceGenerated, ParentFileID=original)
        [metrics: file_operations_total{operation="edit_image"}, file_bytes_total{direction="stored"};
         logs: "[Engine] ✅ edit_image saved …"]
```

**Enabling it** (host wiring):

```go
// 1) record images the user sends as files
coreHandler.SetFileRecorder(ag.RecordUserFile)
// 2) enable editing via OpenRouter (default model: google/gemini-2.5-flash-image-preview)
ag.UseOpenRouterImageEditor(imageedit.OpenRouterConfig{APIKey: openRouterKey})
```

Without (2) the `edit_image` action returns "image editing is not configured"; without
(1) inbound images are not recorded as files (the `save` action still works for generated
files).

## Where it is instrumented

| Activity | Code |
|----------|------|
| Core message lifecycle + moderation + queue | `core/core.go` |
| Core LLM loop | `core/llm.go` (`processWithTools`) |
| Core tools + agent routing/escalation | `core/tools.go` |
| Routing-decision DAG (record + persist) | `core/route_trace.go`, `core/core.go` |
| Vision LLM | `core/vision.go` |
| Plan execution | `core/planning.go` |
| Agent message lifecycle + LLM + tools + queue | `engine/user_agent.go` |
| Backup LLM chain | `engine/backup_chain.go` |
| Summarization scheduler | `engine/schedules.go` |
| Knowledge file opens | `engine/file_tools.go` |
| HTTP route | `routes.go` (`/agentize/metrics`) |
