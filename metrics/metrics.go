// Package metrics provides Prometheus instrumentation for the Agentize framework.
//
// It is meticulous by design: every metered activity — message processing (Core
// and per-agent), LLM calls (by purpose), tool calls, agent routing/escalation,
// backup-LLM fallbacks, the session-summarization scheduler, planning, knowledge
// file opens and moderation — is recorded here. All collectors live on the
// default Prometheus registry, so the exposed handler also includes Go
// runtime/process metrics.
//
// The host application exposes these via Agentize.RegisterRoutes (which mounts
// /agentize/metrics) or by mounting metrics.Handler() on its own router.
package metrics

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "agentize"

// Status returns "ok" or "error" depending on err — a convenience for callers.
func Status(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

var (
	msgLatencyBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60, 120, 300}
	llmLatencyBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 4, 8, 15, 30, 60, 120}
	schedBuckets      = []float64{0.5, 1, 2, 5, 10, 30, 60, 120, 300, 600}
	scanBuckets       = prometheus.ExponentialBuckets(1, 2, 12) // 1 → ~2048 sessions
)

// ---------------------------------------------------------------------------
// Message processing (Core router + per-agent Engine)
// ---------------------------------------------------------------------------

var (
	messages = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "message", Name: "processed_total",
		Help: "Messages processed by layer (core|agent) and status (ok|error).",
	}, []string{"layer", "status"})

	messageDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "message", Name: "duration_seconds",
		Help: "End-to-end message processing time by layer.", Buckets: msgLatencyBuckets,
	}, []string{"layer"})

	messagesInProgress = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "message", Name: "in_progress",
		Help: "Messages currently being processed by layer.",
	}, []string{"layer"})

	messagesQueued = promauto.NewCounterVec(prometheus.CounterOpts{
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
	llmCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "llm", Name: "calls_total",
		Help: "LLM calls by purpose (core|agent|vision|summary|moderation|backup), model and status.",
	}, []string{"purpose", "model", "status"})

	llmDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "llm", Name: "call_duration_seconds",
		Help: "LLM call latency by purpose and model.", Buckets: llmLatencyBuckets,
	}, []string{"purpose", "model"})

	llmTokens = promauto.NewCounterVec(prometheus.CounterOpts{
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
	toolCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "tool", Name: "calls_total",
		Help: "Tool calls by layer (core|agent), tool name and status.",
	}, []string{"layer", "tool", "status"})

	toolDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "tool", Name: "call_duration_seconds",
		Help: "Tool call latency by layer and tool.", Buckets: llmLatencyBuckets,
	}, []string{"layer", "tool"})

	agentRouting = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "agent", Name: "routing_total",
		Help: "Agent routing (delegation) calls by target agent and status.",
	}, []string{"agent", "status"})

	agentEscalations = promauto.NewCounterVec(prometheus.CounterOpts{
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
// Backup LLM chain
// ---------------------------------------------------------------------------

var backupLLM = promauto.NewCounterVec(prometheus.CounterOpts{
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
	schedRuns = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "runs_total",
		Help: "Scheduler check cycles by status (ok|error).",
	}, []string{"status"})

	schedRunDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "run_duration_seconds",
		Help: "Duration of one scheduler check cycle.", Buckets: schedBuckets,
	})

	schedScanned = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "sessions_scanned",
		Help: "Sessions scanned per scheduler cycle.", Buckets: scanBuckets,
	})

	schedSummaries = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "summaries_total",
		Help: "Per-session summarizations by status (ok|error).",
	}, []string{"status"})

	schedSummaryDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "scheduler", Name: "summary_duration_seconds",
		Help: "Duration of one session summarization.", Buckets: schedBuckets,
	})

	schedRunning = promauto.NewGauge(prometheus.GaugeOpts{
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
	knowledgeOpens = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "knowledge", Name: "file_opens_total",
		Help: "Knowledge node/file opens by status (ok|error).",
	}, []string{"status"})

	moderationChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "moderation", Name: "checks_total",
		Help: "Moderation checks by result (ok|nonsense|banned|error).",
	}, []string{"result"})

	planRuns = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "planning", Name: "runs_total",
		Help: "Plan executions by status (ok|error).",
	}, []string{"status"})

	planSteps = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "planning", Name: "steps_total",
		Help: "Plan steps executed by status (ok|error).",
	}, []string{"status"})
)

// KnowledgeOpen records a knowledge file open.
func KnowledgeOpen(status string) { knowledgeOpens.WithLabelValues(status).Inc() }

// Moderation records a moderation check result.
func Moderation(result string) { moderationChecks.WithLabelValues(result).Inc() }

// PlanRun records a plan execution outcome.
func PlanRun(status string) { planRuns.WithLabelValues(status).Inc() }

// PlanStep records a plan step outcome.
func PlanStep(status string) { planSteps.WithLabelValues(status).Inc() }

// ---------------------------------------------------------------------------
// HTTP exposition
// ---------------------------------------------------------------------------

// Handler returns the standard Prometheus HTTP handler (default registry).
func Handler() http.Handler { return promhttp.Handler() }

// GinHandler adapts the Prometheus handler to a gin.HandlerFunc.
func GinHandler() gin.HandlerFunc {
	h := promhttp.Handler()
	return func(c *gin.Context) { h.ServeHTTP(c.Writer, c.Request) }
}
