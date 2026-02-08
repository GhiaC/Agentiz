package model

import (
	"fmt"
	"time"
)

// SummarizationLog represents a log entry for a summarization request
type SummarizationLog struct {
	// LogID is a unique identifier for this log entry
	LogID string

	// SessionID identifies the session being summarized
	SessionID string

	// UserID identifies the user who owns the session
	UserID string

	// Session context before summarization
	SessionTitle          string // Session title at summarization time
	PreviousSummary       string // Previous summary before this summarization
	PreviousTags          string // Previous tags (comma-separated) before this summarization
	MessagesBeforeCount   int    // Number of messages before summarization
	MessagesAfterCount    int    // Number of messages after summarization (should be 0)
	ArchivedMessagesCount int    // Number of archived messages after this summarization

	// LLM request/response details
	PromptSent       string // The full prompt sent to the LLM
	ResponseReceived string // The response received from the LLM
	ModelUsed        string // The LLM model used for summarization
	RequestedModel   string // The model that was requested (may differ from actual)

	// Generated content
	GeneratedSummary string // The new summary generated
	GeneratedTags    string // The new tags generated (comma-separated)
	GeneratedTitle   string // The title generated (if session had no title)

	// Token usage information
	PromptTokens     int // Tokens used in the prompt
	CompletionTokens int // Tokens used in the completion
	TotalTokens      int // Total tokens used

	// Timing information
	DurationMs int64 // Time taken for summarization in milliseconds

	// Status indicates if the summarization was successful
	Status string // "success", "failed", "pending"

	// ErrorMessage contains error details if status is "failed"
	ErrorMessage string

	// SummarizationType indicates what triggered the summarization
	SummarizationType string // "first", "subsequent", "immediate"

	// Metadata
	CreatedAt   time.Time
	CompletedAt time.Time
}

// NewSummarizationLog creates a new summarization log entry
func NewSummarizationLog(sessionID string, userID string) *SummarizationLog {
	now := time.Now()
	return &SummarizationLog{
		LogID:     generateSummarizationLogID(sessionID, now),
		SessionID: sessionID,
		UserID:    userID,
		Status:    "pending",
		CreatedAt: now,
	}
}

// MarkCompleted marks the log as completed and calculates duration
func (log *SummarizationLog) MarkCompleted(status string) {
	log.Status = status
	log.CompletedAt = time.Now()
	log.DurationMs = log.CompletedAt.Sub(log.CreatedAt).Milliseconds()
}

// generateSummarizationLogID generates a unique log ID
func generateSummarizationLogID(sessionID string, timestamp time.Time) string {
	date := timestamp.Format("060102150405") // YYMMDDHHMMSS
	return fmt.Sprintf("summ-%s-%s-%s", date, sessionID[:min(8, len(sessionID))], randomString(4))
}
