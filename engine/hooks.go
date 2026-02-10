package engine

import (
	"context"
	"time"
)

// ==================== Status Updates (per-request, via context) ====================

// StatusPhase represents a processing stage
type StatusPhase string

const (
	StatusReceived      StatusPhase = "received"       // message received
	StatusAnalyzing     StatusPhase = "analyzing"      // checking nonsense, building prompts
	StatusRouting       StatusPhase = "routing"        // Core deciding high/low agent
	StatusThinking      StatusPhase = "thinking"       // waiting for LLM response
	StatusToolExecuting StatusPhase = "tool_executing" // executing a tool
	StatusToolDone      StatusPhase = "tool_done"      // tool finished
	StatusAgentCalling  StatusPhase = "agent_calling"  // delegating to user agent
	StatusAgentDone     StatusPhase = "agent_done"     // user agent finished
	StatusCompleted     StatusPhase = "completed"      // processing done
	StatusError         StatusPhase = "error"          // error occurred
	StatusCustom        StatusPhase = "custom"         // LLM-generated custom status via update_status tool
)

// StatusUpdate carries real-time progress information
type StatusUpdate struct {
	UserID    string
	SessionID string
	Phase     StatusPhase
	Detail    string                 // human-readable detail: tool name, model name, etc.
	Metadata  map[string]interface{} // extensible
}

// StatusFunc is a per-request callback for real-time status updates.
// It is passed via context so each request (e.g., each Telegram message) gets its own.
type StatusFunc func(status *StatusUpdate)

// Context helpers for StatusFunc
type statusCtxKey struct{}

// WithStatusFunc attaches a StatusFunc to the context.
func WithStatusFunc(ctx context.Context, fn StatusFunc) context.Context {
	return context.WithValue(ctx, statusCtxKey{}, fn)
}

// notifyStatus is the internal helper called throughout the engine.
// Safe to call even if no StatusFunc is set (no-op).
func notifyStatus(ctx context.Context, userID, sessionID string, phase StatusPhase, detail string) {
	if fn, ok := ctx.Value(statusCtxKey{}).(StatusFunc); ok && fn != nil {
		fn(&StatusUpdate{
			UserID:    userID,
			SessionID: sessionID,
			Phase:     phase,
			Detail:    detail,
		})
	}
}

// ==================== Usage Callback (global, on struct) ====================

// UsageEvent represents a metered action for billing/tracking
type UsageEvent struct {
	UserID    string
	SessionID string
	EventType EventType
	Name      string // tool name, model name, or agent type
	Tokens    int    // token count (for LLM calls)
	Duration  time.Duration
	Error     error
	Metadata  map[string]interface{}
}

// EventType classifies the kind of metered action
type EventType string

const (
	EventToolCall     EventType = "tool_call"
	EventLLMCall      EventType = "llm_call"
	EventAgentRouting EventType = "agent_routing"
)

// Callback is the global hook interface for billing and usage metering.
// Set once on CoreHandler/Engine at initialization.
type Callback interface {
	// BeforeAction is called before a tool/LLM/agent call.
	// Return non-nil error to BLOCK the action (e.g., limit exceeded).
	BeforeAction(ctx context.Context, event *UsageEvent) error

	// AfterAction is called after completion for recording usage.
	AfterAction(ctx context.Context, event *UsageEvent)
}
