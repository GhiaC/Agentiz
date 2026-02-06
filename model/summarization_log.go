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

	// PromptSent is the full prompt sent to the LLM
	PromptSent string

	// ResponseReceived is the response received from the LLM
	ResponseReceived string

	// ModelUsed is the LLM model used for summarization
	ModelUsed string

	// Token usage information
	PromptTokens     int // Tokens used in the prompt
	CompletionTokens int // Tokens used in the completion
	TotalTokens      int // Total tokens used

	// Status indicates if the summarization was successful
	Status string // "success" or "failed"

	// ErrorMessage contains error details if status is "failed"
	ErrorMessage string

	// Metadata
	CreatedAt time.Time
}

// NewSummarizationLog creates a new summarization log entry
func NewSummarizationLog(sessionID string, userID string) *SummarizationLog {
	return &SummarizationLog{
		LogID:     generateSummarizationLogID(sessionID, time.Now()),
		SessionID: sessionID,
		UserID:    userID,
		Status:    "pending",
		CreatedAt: time.Now(),
	}
}

// generateSummarizationLogID generates a unique log ID
func generateSummarizationLogID(sessionID string, timestamp time.Time) string {
	date := timestamp.Format("060102150405") // YYMMDDHHMMSS
	return fmt.Sprintf("summ-%s-%s-%s", date, sessionID[:min(8, len(sessionID))], randomString(4))
}
