package llminterface

import (
	"encoding/json"
	"fmt"

	openai "github.com/sashabaranov/go-openai"
)

// ---------------------------------------------------------------------------
// openai → llminterface  (used before calling a backup Provider)
// ---------------------------------------------------------------------------

// FromOpenAIMessages converts OpenAI ChatCompletionMessages to provider-agnostic Messages.
func FromOpenAIMessages(msgs []openai.ChatCompletionMessage) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		msg := Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
		out = append(out, msg)
	}
	return out
}

// FromOpenAITools converts OpenAI Tool definitions to provider-agnostic Tools.
func FromOpenAITools(tools []openai.Tool) []Tool {
	out := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if t.Function == nil {
			continue
		}
		out = append(out, Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// llminterface → openai  (used after a backup Provider returns a Response)
// ---------------------------------------------------------------------------

// ToOpenAIResponse converts a provider-agnostic Response back to an OpenAI
// ChatCompletionResponse so the engine can process it without changes.
func ToOpenAIResponse(r *Response) openai.ChatCompletionResponse {
	msg := openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: r.Content,
	}

	finishReason := openai.FinishReasonStop

	for _, tc := range r.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{
			ID:   tc.ID,
			Type: openai.ToolTypeFunction,
			Function: openai.FunctionCall{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		})
		finishReason = openai.FinishReasonToolCalls
	}

	return openai.ChatCompletionResponse{
		Model: r.Model,
		Choices: []openai.ChatCompletionChoice{
			{
				Index:        0,
				Message:      msg,
				FinishReason: finishReason,
			},
		},
		Usage: openai.Usage{
			PromptTokens:     r.Usage.PromptTokens,
			CompletionTokens: r.Usage.CompletionTokens,
			TotalTokens:      r.Usage.TotalTokens,
		},
	}
}

// ---------------------------------------------------------------------------
// Helpers for Provider implementations (e.g. backup adapter in TradeAgent)
// ---------------------------------------------------------------------------

// ToolCallArgumentsToJSON marshals a map[string]interface{} to a JSON string.
// Useful for Provider implementations that receive arguments as a map.
func ToolCallArgumentsToJSON(args map[string]interface{}) string {
	if args == nil {
		return "{}"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(b)
}

// ToolCallArgumentsFromJSON parses a JSON string into map[string]interface{}.
// Useful for Provider implementations that need arguments as a map.
func ToolCallArgumentsFromJSON(jsonStr string) (map[string]interface{}, error) {
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, err
	}
	return result, nil
}
