package llminterface

import "context"

// Message represents a single chat message (provider-agnostic).
type Message struct {
	Role       string     // "system", "user", "assistant", "tool"
	Content    string     // text content
	ToolCallID string     // for tool result messages
	ToolCalls  []ToolCall // for assistant messages requesting tool calls
}

// ToolCall represents a tool invocation requested by the model.
type ToolCall struct {
	ID        string // unique call ID
	Name      string // function name
	Arguments string // JSON-encoded arguments
}

// Tool describes a callable tool/function definition.
type Tool struct {
	Name        string      // function name
	Description string      // human-readable description
	Parameters  interface{} // JSON Schema object describing the parameters
}

// Response represents an LLM completion response.
type Response struct {
	Content   string     // text content (empty when tool calls are present)
	ToolCalls []ToolCall // tool calls requested by the model
	Usage     Usage      // token usage statistics
}

// Usage holds token usage statistics.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Provider is the generic LLM provider interface.
// Any external client (Workers AI, Anthropic, local models, etc.)
// can implement this single method to plug into the Agentize engine.
type Provider interface {
	ChatCompletion(ctx context.Context, model string, messages []Message, tools []Tool) (*Response, error)
}

// ProviderFunc adapts a plain function into a Provider.
// This follows the Go convention (like http.HandlerFunc) for convenience:
//
//	provider := llminterface.ProviderFunc(func(ctx context.Context, model string, msgs []Message, tools []Tool) (*Response, error) {
//	    // your implementation
//	})
type ProviderFunc func(ctx context.Context, model string, messages []Message, tools []Tool) (*Response, error)

// ChatCompletion implements the Provider interface.
func (f ProviderFunc) ChatCompletion(ctx context.Context, model string, messages []Message, tools []Tool) (*Response, error) {
	return f(ctx, model, messages, tools)
}
