package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Summarization-specific metrics. These complement the generic scheduler_* metrics
// with detail about each summarization: type, outcome, how many messages were
// archived vs retained (rolling window), input size and resulting summary growth.

var (
	summMsgBuckets    = prometheus.ExponentialBuckets(1, 2, 12) // 1 → ~2048 messages
	summCharBuckets   = []float64{50, 100, 200, 400, 600, 800, 1200, 2000, 4000}
	summaryAgeBuckets = []float64{60, 300, 900, 1800, 3600, 7200, 14400, 43200, 86400, 259200} // 1m → 3d

	summarizationRuns = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "runs_total",
		Help: "Summarizations by type (first|subsequent|immediate) and status (ok|failed|offensive|empty).",
	}, []string{"type", "status"})

	summarizationArchived = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "messages_archived",
		Help: "Messages moved to the archive per summarization (rolling window evictions).", Buckets: summMsgBuckets,
	})

	summarizationRetained = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "messages_retained",
		Help: "Messages kept active (rolling window) after summarization.", Buckets: summMsgBuckets,
	})

	summarizationInput = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "input_messages",
		Help: "Messages fed into the summarizer per run.", Buckets: summMsgBuckets,
	})

	summarizationChars = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "summary_chars",
		Help: "Resulting summary length in characters (tracks append-style growth).", Buckets: summCharBuckets,
	})

	summarizationGrowth = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "summary_growth_chars",
		Help:    "Change in summary length vs the previous summary (append delta; negative = compaction).",
		Buckets: []float64{-400, -200, -50, 0, 25, 50, 100, 200, 400, 800},
	})

	summarizationOffensive = factory.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "offensive_total",
		Help: "Summarizations that detected offensive content (triggering a ban).",
	})

	summarizationTokens = factory.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "summarization", Name: "tokens_total",
		Help: "Tokens consumed by summarization by type (prompt|completion).",
	}, []string{"type"})

	summaryAge = factory.NewHistogram(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "summary", Name: "age_seconds",
		Help: "Age of the previous summary when a session is re-summarized (seconds since the last summarization). " +
			"High values mean summaries go stale before the scheduler refreshes them.",
		Buckets: summaryAgeBuckets,
	})
)

// SummaryAge records how stale the previous summary was when a session is
// re-summarized: dur is now - previousSummarizedAt. It is skipped (dur <= 0) for
// a session's first-ever summarization, where there is no previous summary.
func SummaryAge(dur time.Duration) {
	if dur > 0 {
		summaryAge.Observe(dur.Seconds())
	}
}

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
