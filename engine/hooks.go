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
	// SendAsNewMessage: when true, the receiver should send a new message instead of editing the status message.
	SendAsNewMessage bool
}

// NotifyOption modifies a StatusUpdate before it is passed to the StatusFunc.
type NotifyOption func(*StatusUpdate)

// OptSendAsNewMessage sets SendAsNewMessage on the StatusUpdate so the client sends a new message instead of editing.
func OptSendAsNewMessage() NotifyOption {
	return func(s *StatusUpdate) { s.SendAsNewMessage = true }
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
// Optional opts are applied to the StatusUpdate before passing to the callback.
func notifyStatus(ctx context.Context, userID, sessionID string, phase StatusPhase, detail string, opts ...NotifyOption) {
	if fn, ok := ctx.Value(statusCtxKey{}).(StatusFunc); ok && fn != nil {
		su := &StatusUpdate{
			UserID:    userID,
			SessionID: sessionID,
			Phase:     phase,
			Detail:    detail,
		}
		for _, opt := range opts {
			opt(su)
		}
		fn(su)
	}
}

// ==================== Usage Callback (global, on struct) ====================

// UsageEvent represents a metered action for billing/tracking
type UsageEvent struct {
	UserID    string
	SessionID string
	EventType EventType
	Name      string // for LLM: use EventNameLLMCall; for tool_call: tool name; for agent_routing: agent type (e.g. high, low)
	Tokens    int    // token count (for LLM calls) - deprecated, use Input/Output/Cached
	// Detailed token counts for LLM calls (Credit+Usage billing)
	InputTokens       int
	OutputTokens      int
	CachedInputTokens int
	Model             string
	Duration          time.Duration
	Error             error
	Metadata          map[string]interface{}
}

// EventType classifies the kind of metered action
type EventType string

const (
	EventToolCall     EventType = "tool_call"
	EventLLMCall      EventType = "llm_call"
	EventAgentRouting EventType = "agent_routing"
)

// EventNameLLMCall is the fixed Name for UsageEvent when EventType is EventLLMCall. Use Model for the actual model id.
const EventNameLLMCall = "llm_call"

// Callback is the global hook interface for billing and usage metering.
// Set once on CoreHandler/Engine at initialization.
type Callback interface {
	// BeforeAction is called before a tool/LLM/agent call.
	// Return non-nil error to BLOCK the action (e.g., limit exceeded).
	BeforeAction(ctx context.Context, event *UsageEvent) error

	// AfterAction is called after completion for recording usage.
	AfterAction(ctx context.Context, event *UsageEvent)
}

// FormatBlockedActionResult returns the string to use as the result when BeforeAction blocks.
// The callback error message is returned as-is so the app (e.g. Billing) can use a consistent template.
func FormatBlockedActionResult(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
