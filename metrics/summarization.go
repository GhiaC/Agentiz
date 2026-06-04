package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Summarization-specific metrics. These complement the generic scheduler_* metrics
// with detail about each summarization: type, outcome, how many messages were
// archived vs retained (rolling window), input size and resulting summary growth.

var (
	summMsgBuckets  = prometheus.ExponentialBuckets(1, 2, 12) // 1 → ~2048 messages
	summCharBuckets = []float64{50, 100, 200, 400, 600, 800, 1200, 2000, 4000}

	summarizationRuns = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "runs_total",
		Help: "Summarizations by type (first|subsequent|immediate) and status (ok|failed|offensive|empty).",
	}, []string{"type", "status"})

	summarizationArchived = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "messages_archived",
		Help: "Messages moved to the archive per summarization (rolling window evictions).", Buckets: summMsgBuckets,
	})

	summarizationRetained = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "messages_retained",
		Help: "Messages kept active (rolling window) after summarization.", Buckets: summMsgBuckets,
	})

	summarizationInput = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "input_messages",
		Help: "Messages fed into the summarizer per run.", Buckets: summMsgBuckets,
	})

	summarizationChars = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "summary_chars",
		Help: "Resulting summary length in characters (tracks append-style growth).", Buckets: summCharBuckets,
	})

	summarizationGrowth = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "summary_growth_chars",
		Help:    "Change in summary length vs the previous summary (append delta; negative = compaction).",
		Buckets: []float64{-400, -200, -50, 0, 25, 50, 100, 200, 400, 800},
	})

	summarizationOffensive = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "offensive_total",
		Help: "Summarizations that detected offensive content (triggering a ban).",
	})

	summarizationTokens = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "tokens_total",
		Help: "Tokens consumed by summarization by type (prompt|completion).",
	}, []string{"type"})
)

// SummarizationResult records a completed (or failed) summarization run.
//
//	typ        : "first" | "subsequent" | "immediate"
//	status     : "ok" | "failed" | "offensive" | "empty"
//	input      : number of messages summarized
//	archived   : messages evicted to the archive this run
//	retained   : messages kept in the active rolling window
//	prevChars  : previous summary length (chars)
//	newChars   : resulting summary length (chars)
//	promptTok  : prompt tokens used (0 if unknown)
//	complTok   : completion tokens used (0 if unknown)
func SummarizationResult(typ, status string, input, archived, retained, prevChars, newChars, promptTok, complTok int) {
	summarizationRuns.WithLabelValues(typ, status).Inc()
	if status == "offensive" {
		summarizationOffensive.Inc()
	}
	if input > 0 {
		summarizationInput.Observe(float64(input))
	}
	if status == "ok" {
		summarizationArchived.Observe(float64(archived))
		summarizationRetained.Observe(float64(retained))
		if newChars > 0 {
			summarizationChars.Observe(float64(newChars))
		}
		summarizationGrowth.Observe(float64(newChars - prevChars))
	}
	if promptTok > 0 {
		summarizationTokens.WithLabelValues("prompt").Add(float64(promptTok))
	}
	if complTok > 0 {
		summarizationTokens.WithLabelValues("completion").Add(float64(complTok))
	}
}
