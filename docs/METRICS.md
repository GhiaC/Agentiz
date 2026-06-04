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
| `planning_runs_total` | counter | `status` |
| `planning_steps_total` | counter | `status` |

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
```

## Where it is instrumented

| Activity | Code |
|----------|------|
| Core message lifecycle + moderation + queue | `core/core.go` |
| Core LLM loop | `core/llm.go` (`processWithTools`) |
| Core tools + agent routing/escalation | `core/tools.go` |
| Vision LLM | `core/vision.go` |
| Plan execution | `core/planning.go` |
| Agent message lifecycle + LLM + tools + queue | `engine/user_agent.go` |
| Backup LLM chain | `engine/backup_chain.go` |
| Summarization scheduler | `engine/schedules.go` |
| Knowledge file opens | `engine/file_tools.go` |
| HTTP route | `routes.go` (`/agentize/metrics`) |
