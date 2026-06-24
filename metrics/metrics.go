// Package metrics provides Prometheus instrumentation for the Agentize framework.
//
// It is meticulous by design: every metered activity — message processing (Core
// and per-agent), LLM calls (by purpose), tool calls, agent routing/escalation,
// backup-LLM fallbacks, the session-summarization scheduler, planning, knowledge
// file opens and moderation — is recorded here. All collectors live on a
// dedicated registry (see registry below) together with the Go runtime/process
// collectors, so the exposed handler reports a known, bounded set of series
// rather than the global default registry. Set
// AGENTIZE_METRICS_DEFAULT_REGISTRY=1 to expose the global default registry.
//
// The host application exposes these via Agentize.RegisterRoutes (which mounts
// /agentize/metrics) or by mounting metrics.Handler() on its own router.
package metrics

import (
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "agentize"

// registry is a dedicated Prometheus registry holding only Agentize's own
// collectors plus the Go runtime and process collectors. Using our own registry
// (instead of the global default) means the /agentize/metrics endpoint exposes a
// known, bounded set of series and never leaks collectors that other imported
// libraries may have registered on the global default registry. Set
// AGENTIZE_METRICS_DEFAULT_REGISTRY=1 to expose the full global default registry
// instead (opt-in).
var registry = prometheus.NewRegistry()

// factory registers every agentize_* collector (here and in summarization.go)
// onto the dedicated registry above instead of the global default.
var factory = promauto.With(registry)

func init() {
	// Keep Go runtime + process metrics (go_goroutines,
	// process_resident_memory_bytes, …) available on the dedicated registry so
	// the Grafana runtime panels keep working without exposing the whole global
	// default registry.
	registry.MustRegister(collectors.NewGoCollector())
	registry.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// Status returns "ok" or "error" depending on err — a convenience for callers.
func Status(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

var (
	msgLatencyBuckets   = []float64{0.1, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60, 120, 300}
	llmLatencyBuckets   = []float64{0.1, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60, 120}
	schedBuckets        = []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600}
	scanBuckets         = prometheus.ExponentialBuckets(1, 2, 12) // 1 → ~2048 sessions
	fileLatencyBuckets  = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5}
	storeLatencyBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}
)

// ---------------------------------------------------------------------------
// Message processing (Core router + per-agent Engine)
// ---------------------------------------------------------------------------

var (
	messages = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "message", Name: "processed_total",
		Help: "Messages processed by layer (core|agent) and status (ok|error).",
	}, []string{"layer", "status"})

	messageDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "message", Name: "duration_seconds",
		Help: "End-to-end message processing time by layer.", Buckets: msgLatencyBuckets,
	}, []string{"layer"})

	messagesInProgress = factory.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "message", Name: "in_progress",
		Help: "Messages currently being processed by layer.",
	}, []string{"layer"})

	messagesQueued = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "message", Name: "queued_total",
		Help: "Messages queued because the user/session was busy, by layer.",
	}, []string{"layer"})
)

// MessageStart marks the beginning of processing for a layer (core|agent).
func MessageStart(layer string) { messagesInProgress.WithLabelValues(layer).Inc() }

// MessageDone marks completion: decrements in-progress and records count + duration.
func MessageDone(layer, status string, dur time.Duration) {
	messagesInProgress.WithLabelValues(layer).Dec()
	messages.WithLabelValues(layer, status).Inc()
	messageDuration.WithLabelValues(layer).Observe(dur.Seconds())
}

// MessageQueued records a queued (deferred) message.
func MessageQueued(layer string) { messagesQueued.WithLabelValues(layer).Inc() }

// ---------------------------------------------------------------------------
// LLM calls
// ---------------------------------------------------------------------------

var (
	llmCalls = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "llm", Name: "calls_total",
		Help: "LLM calls by purpose (core|agent|vision|summary|moderation|backup), model and status.",
	}, []string{"purpose", "model", "status"})

	llmDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "llm", Name: "call_duration_seconds",
		Help: "LLM call latency by purpose and model.", Buckets: llmLatencyBuckets,
	}, []string{"purpose", "model"})

	llmTokens = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "llm", Name: "tokens_total",
		Help: "LLM token usage by purpose, model and type (input|output|cached).",
	}, []string{"purpose", "model", "type"})
)

// LLMCall records one LLM call with its token breakdown.
func LLMCall(purpose, model, status string, dur time.Duration, prompt, completion, cached int) {
	if model == "" {
		model = "unknown"
	}
	llmCalls.WithLabelValues(purpose, model, status).Inc()
	if dur > 0 {
		llmDuration.WithLabelValues(purpose, model).Observe(dur.Seconds())
	}
	// prompt tokens include cached; split so input excludes cached for clarity.
	input := prompt - cached
	if input > 0 {
		llmTokens.WithLabelValues(purpose, model, "input").Add(float64(input))
	}
	if completion > 0 {
		llmTokens.WithLabelValues(purpose, model, "output").Add(float64(completion))
	}
	if cached > 0 {
		llmTokens.WithLabelValues(purpose, model, "cached").Add(float64(cached))
	}
}

// ---------------------------------------------------------------------------
// Tool calls + agent routing
// ---------------------------------------------------------------------------

var (
	toolCalls = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "tool", Name: "calls_total",
		Help: "Tool calls by layer (core|agent), tool name and status.",
	}, []string{"layer", "tool", "status"})

	toolDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "tool", Name: "call_duration_seconds",
		Help: "Tool call latency by layer and tool.", Buckets: llmLatencyBuckets,
	}, []string{"layer", "tool"})

	agentRouting = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "agent", Name: "routing_total",
		Help: "Agent routing (delegation) calls by target agent and status.",
	}, []string{"agent", "status"})

	agentEscalations = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "agent", Name: "escalations_total",
		Help: "Agent escalations to a higher cost tier, by originating agent.",
	}, []string{"agent"})
)

// ToolCall records one tool execution.
func ToolCall(layer, tool, status string, dur time.Duration) {
	if tool == "" {
		tool = "unknown"
	}
	toolCalls.WithLabelValues(layer, tool, status).Inc()
	if dur > 0 {
		toolDuration.WithLabelValues(layer, tool).Observe(dur.Seconds())
	}
}

// AgentRouting records a delegation to a worker agent.
func AgentRouting(agent, status string) {
	if agent == "" {
		agent = "unknown"
	}
	agentRouting.WithLabelValues(agent, status).Inc()
}

// AgentEscalation records an escalation to a higher-tier agent.
func AgentEscalation(agent string) {
	if agent == "" {
		agent = "unknown"
	}
	agentEscalations.WithLabelValues(agent).Inc()
}

// ---------------------------------------------------------------------------
// Core system prompt (assembly cache + size budget)
// ---------------------------------------------------------------------------

var (
	systemPromptCacheLookups = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "system_prompt", Name: "cache_total",
		Help: "Core system-prompt cache lookups by result: hit (served from cache), " +
			"miss (no entry), stale (entry expired or invalidated by summarization).",
	}, []string{"result"})

	systemPromptSections = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "system_prompt", Name: "sections_dropped_total",
		Help: "Optional system-prompt sections dropped because the assembled prompt hit MaxSystemPromptSize, by section.",
	}, []string{"section"})
)

// SystemPromptCache records one cache lookup result: "hit", "miss" or "stale".
func SystemPromptCache(result string) {
	systemPromptCacheLookups.WithLabelValues(result).Inc()
}

// SystemPromptSectionDropped records an optional prompt section skipped due to the size budget.
func SystemPromptSectionDropped(section string) {
	systemPromptSections.WithLabelValues(section).Inc()
}

// ---------------------------------------------------------------------------
// Routing trace (Core decision/forward DAG)
// ---------------------------------------------------------------------------

var (
	routeTraces = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "route_trace", Name: "recorded_total",
		Help: "Core routing-decision DAGs recorded, by status (ok|error). " +
			"\"error\" covers both a failed message and a failed trace persist.",
	}, []string{"status"})

	routeTraceNodes = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "route_trace", Name: "nodes",
		Help:    "Number of nodes (decisions + tool calls + forwards + response) per routing DAG.",
		Buckets: []float64{1, 2, 3, 4, 5, 7, 10, 15, 20, 30, 50},
	})
)

// RouteTrace records one Core routing DAG: its terminal status and node count.
func RouteTrace(status string, nodes int) {
	if status == "" {
		status = "unknown"
	}
	routeTraces.WithLabelValues(status).Inc()
	if nodes > 0 {
		routeTraceNodes.Observe(float64(nodes))
	}
}

// ---------------------------------------------------------------------------
// Backup LLM chain
// ---------------------------------------------------------------------------

var backupLLM = factory.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace, Subsystem: "backup", Name: "llm_total",
	Help: "Backup-LLM provider attempts by provider, model and status (ok|error).",
}, []string{"provider", "model", "status"})

// BackupLLM records one backup provider attempt.
func BackupLLM(provider, model, status string) {
	if provider == "" {
		provider = "unknown"
	}
	if model == "" {
		model = "unknown"
	}
	backupLLM.WithLabelValues(provider, model, status).Inc()
}

// ---------------------------------------------------------------------------
// Session-summarization scheduler (background worker)
// ---------------------------------------------------------------------------

var (
	schedRuns = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "runs_total",
		Help: "Scheduler check cycles by status (ok|error).",
	}, []string{"status"})

	schedRunDuration = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "run_duration_seconds",
		Help: "Duration of one scheduler check cycle.", Buckets: schedBuckets,
	})

	schedScanned = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "sessions_scanned",
		Help: "Sessions scanned per scheduler cycle.", Buckets: scanBuckets,
	})

	schedSummaries = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "summaries_total",
		Help: "Per-session summarizations by status (ok|error).",
	}, []string{"status"})

	schedSummaryDuration = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "summary_duration_seconds",
		Help: "Duration of one session summarization.", Buckets: schedBuckets,
	})

	schedRunning = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "running",
		Help: "1 while a scheduler cycle is executing, else 0.",
	})
)

// SchedulerRun records one full scheduler cycle.
func SchedulerRun(status string, dur time.Duration, scanned, summarized int) {
	schedRuns.WithLabelValues(status).Inc()
	schedRunDuration.Observe(dur.Seconds())
	schedScanned.Observe(float64(scanned))
}

// SchedulerRunning toggles the running gauge around a cycle.
func SchedulerRunning(running bool) {
	if running {
		schedRunning.Set(1)
	} else {
		schedRunning.Set(0)
	}
}

// SchedulerSummary records one per-session summarization.
func SchedulerSummary(status string, dur time.Duration) {
	schedSummaries.WithLabelValues(status).Inc()
	schedSummaryDuration.Observe(dur.Seconds())
}

// ---------------------------------------------------------------------------
// Knowledge, moderation, planning
// ---------------------------------------------------------------------------

var (
	knowledgeOpens = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "file_opens_total",
		Help: "Knowledge node/file opens by status (ok|error).",
	}, []string{"status"})

	moderationChecks = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "moderation", Name: "checks_total",
		Help: "Moderation checks by result (ok|nonsense|banned|error).",
	}, []string{"result"})

	bans = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "moderation", Name: "bans_total",
		Help: "User bans applied by reason (nonsense|offensive|manual).",
	}, []string{"reason"})

	planRuns = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "planning", Name: "runs_total",
		Help: "Plan executions by status (ok|error).",
	}, []string{"status"})

	planSteps = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "planning", Name: "steps_total",
		Help: "Plan steps executed by status (ok|error).",
	}, []string{"status"})
)

// KnowledgeOpen records a knowledge file open.
func KnowledgeOpen(status string) { knowledgeOpens.WithLabelValues(status).Inc() }

// Moderation records a moderation check result.
func Moderation(result string) { moderationChecks.WithLabelValues(result).Inc() }

// Ban records a user ban with its reason (nonsense|offensive).
func Ban(reason string) {
	if reason == "" {
		reason = "unknown"
	}
	bans.WithLabelValues(reason).Inc()
}

// PlanRun records a plan execution outcome.
func PlanRun(status string) { planRuns.WithLabelValues(status).Inc() }

// PlanStep records a plan step outcome.
func PlanStep(status string) { planSteps.WithLabelValues(status).Inc() }

// ---------------------------------------------------------------------------
// User files (file manager)
// ---------------------------------------------------------------------------

var (
	fileOps = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "file", Name: "operations_total",
		Help: "User-file operations by operation (list|read|grep|save|edit|edit_image|upload) and status (ok|error).",
	}, []string{"operation", "status"})

	fileOpDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "file", Name: "operation_duration_seconds",
		Help: "User-file operation latency by operation.", Buckets: fileLatencyBuckets,
	}, []string{"operation"})

	fileBytes = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "file", Name: "bytes_total",
		Help: "User-file bytes moved by direction (stored|read).",
	}, []string{"direction"})
)

// FileOp records one user-file operation and its latency.
func FileOp(operation, status string, dur time.Duration) {
	if operation == "" {
		operation = "unknown"
	}
	fileOps.WithLabelValues(operation, status).Inc()
	if dur > 0 {
		fileOpDuration.WithLabelValues(operation).Observe(dur.Seconds())
	}
}

// FileBytes records bytes stored or read for user files (direction: stored|read).
func FileBytes(direction string, n int64) {
	if n > 0 {
		fileBytes.WithLabelValues(direction).Add(float64(n))
	}
}

// ---------------------------------------------------------------------------
// Image editing (e.g. OpenRouter Gemini image edits)
// ---------------------------------------------------------------------------

var (
	imageEdits = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "image", Name: "edits_total",
		Help: "Image edit attempts by model and status (ok|error).",
	}, []string{"model", "status"})

	imageEditDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "image", Name: "edit_duration_seconds",
		Help: "Image edit latency by model.", Buckets: llmLatencyBuckets,
	}, []string{"model"})

	imageEditBytes = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "image", Name: "edit_bytes_total",
		Help: "Image edit bytes by direction (input|output).",
	}, []string{"direction"})
)

// ImageEdit records one image-edit attempt: count + latency by model, plus the
// input and output image byte volume.
func ImageEdit(model, status string, dur time.Duration, inBytes, outBytes int64) {
	if model == "" {
		model = "unknown"
	}
	imageEdits.WithLabelValues(model, status).Inc()
	if dur > 0 {
		imageEditDuration.WithLabelValues(model).Observe(dur.Seconds())
	}
	if inBytes > 0 {
		imageEditBytes.WithLabelValues("input").Add(float64(inBytes))
	}
	if outBytes > 0 {
		imageEditBytes.WithLabelValues("output").Add(float64(outBytes))
	}
}

// ---------------------------------------------------------------------------
// Store (persistence layer)
// ---------------------------------------------------------------------------

var (
	storeDeletions = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "store", Name: "deletions_total",
		Help: "Destructive store operations by entity (session|user_data|user_file). Pairs with the store audit log.",
	}, []string{"entity"})

	storeQueryDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "store", Name: "query_duration_seconds",
		Help:    "Store operation latency by operation (store method name) and backend (sqlite|mongodb).",
		Buckets: storeLatencyBuckets,
	}, []string{"operation", "backend"})
)

// RecordStoreDeletion counts one destructive store operation (audit trail).
func RecordStoreDeletion(entity string) {
	if entity == "" {
		entity = "unknown"
	}
	storeDeletions.WithLabelValues(entity).Inc()
}

var (
	reviewsTotal = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "reviews", Name: "total",
		Help: "Human-in-the-loop review decisions by kind (plan_step|tool_call|payment|custom) and decision (approved|rejected|expired|canceled).",
	}, []string{"kind", "decision"})

	reviewsPending = factory.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "reviews", Name: "pending",
		Help: "Number of pending (unresolved) human-in-the-loop reviews.",
	})
)

// RecordReview counts one resolved review by kind and decision (terminal status).
func RecordReview(kind, decision string) {
	if kind == "" {
		kind = "unknown"
	}
	if decision == "" {
		decision = "unknown"
	}
	reviewsTotal.WithLabelValues(kind, decision).Inc()
}

// SetPendingReviews sets the pending-reviews gauge (restart-safe: derive it from
// the store's pending count rather than tracking a drifting delta).
func SetPendingReviews(n int) {
	reviewsPending.Set(float64(n))
}

// StoreQuery records the latency of one store operation. operation is the store
// method name (e.g. "Get", "PutMessage"); backend is "sqlite" or "mongodb".
func StoreQuery(operation, backend string, dur time.Duration) {
	if operation == "" {
		operation = "unknown"
	}
	if backend == "" {
		backend = "unknown"
	}
	storeQueryDuration.WithLabelValues(operation, backend).Observe(dur.Seconds())
}

// ---------------------------------------------------------------------------
// Audit (destructive admin actions)
// ---------------------------------------------------------------------------

var auditActions = factory.NewCounterVec(prometheus.CounterOpts{
	Namespace: namespace, Subsystem: "audit", Name: "actions_total",
	Help: "Audited admin actions by action (e.g. delete_user_data) and status (ok|error|rejected).",
}, []string{"action", "status"})

// AuditAction records one audited admin action. It pairs with an [AUDIT] log
// line at the call site, giving both a queryable counter and a forensic record.
func AuditAction(action, status string) {
	if action == "" {
		action = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	auditActions.WithLabelValues(action, status).Inc()
}

// ---------------------------------------------------------------------------
// HTTP exposition
// ---------------------------------------------------------------------------

// Registry returns the dedicated Agentize Prometheus registry (agentize_* plus
// the Go runtime/process collectors). Hosts building their own handler can
// gather from this instead of the global default registry.
func Registry() *prometheus.Registry { return registry }

// metricsHandler serves the dedicated Agentize registry by default. Set
// AGENTIZE_METRICS_DEFAULT_REGISTRY=1 to expose the full global default registry
// instead (every collector any imported package registered there).
func metricsHandler() http.Handler {
	if os.Getenv("AGENTIZE_METRICS_DEFAULT_REGISTRY") == "1" {
		return promhttp.Handler()
	}
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{Registry: registry})
}

// Handler returns the Prometheus HTTP handler for the Agentize metrics.
func Handler() http.Handler { return metricsHandler() }

// GinHandler adapts the Agentize metrics handler to a gin.HandlerFunc.
func GinHandler() gin.HandlerFunc {
	h := metricsHandler()
	return func(c *gin.Context) { h.ServeHTTP(c.Writer, c.Request) }
}
