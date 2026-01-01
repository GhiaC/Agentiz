package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"agentize/model"
	"github.com/sashabaranov/go-openai"
)

// LLMConfig holds configuration for LLM client
type LLMConfig struct {
	APIKey  string
	BaseURL string
	Model   string
}

// LLMHandler handles LLM interactions and tool execution
type LLMHandler struct {
	client *openai.Client
	config LLMConfig
}

// NewLLMHandler creates a new LLM handler
func NewLLMHandler(config LLMConfig) (*LLMHandler, error) {
	openaiConfig := openai.DefaultConfig(config.APIKey)
	if config.BaseURL != "" {
		openaiConfig.BaseURL = config.BaseURL
	}

	client := openai.NewClientWithConfig(openaiConfig)

	return &LLMHandler{
		client: client,
		config: config,
	}, nil
}

// ProcessMessage processes a user message through LLM with context
// Returns the response and token usage
func (h *LLMHandler) ProcessMessage(
	ctx context.Context,
	systemPrompt string,
	messages []openai.ChatCompletionMessage,
	tools []model.Tool,
) (string, []openai.ToolCall, int, error) {
	// Convert model.Tool to openai.Tool
	openaiTools := make([]openai.Tool, 0, len(tools))
	for _, tool := range tools {
		if tool.Status != model.ToolStatusActive {
			continue
		}

		// Convert tool definition to OpenAI format
		openaiTool := openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema, // Use InputSchema instead of Parameters
			},
		}
		openaiTools = append(openaiTools, openaiTool)
	}

	// Build request messages
	reqMessages := make([]openai.ChatCompletionMessage, 0, len(messages)+1)
	if systemPrompt != "" {
		reqMessages = append(reqMessages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		})
	}
	reqMessages = append(reqMessages, messages...)

	// Make request
	modelName := h.config.Model
	if modelName == "" {
		modelName = "gpt-4"
	}

	resp, err := h.client.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model:    modelName,
			Messages: reqMessages,
			Tools:    openaiTools,
		},
	)
	if err != nil {
		return "", nil, 0, fmt.Errorf("LLM request failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", nil, resp.Usage.TotalTokens, fmt.Errorf("no choices in LLM response")
	}

	choice := resp.Choices[0]
	tokenUsage := resp.Usage.TotalTokens

	// Check if tool calls were made
	if choice.FinishReason == openai.FinishReasonToolCalls {
		return "", choice.Message.ToolCalls, tokenUsage, nil
	}

	// Return text response
	return choice.Message.Content, nil, tokenUsage, nil
}

// ExecuteTool executes a tool call and returns the result
// This is a generic executor - actual tool execution logic should be provided by the caller
type ToolExecutor func(toolName string, args map[string]interface{}) (string, error)

// ProcessWithTools processes a message and handles tool calls recursively
func (h *LLMHandler) ProcessWithTools(
	ctx context.Context,
	systemPrompt string,
	initialMessages []openai.ChatCompletionMessage,
	tools []model.Tool,
	executor ToolExecutor,
	maxIterations int,
) (string, int, error) {
	messages := make([]openai.ChatCompletionMessage, len(initialMessages))
	copy(messages, initialMessages)

	totalTokens := 0
	iterations := 0

	for iterations < maxIterations {
		iterations++

		// Process message through LLM
		response, toolCalls, tokens, err := h.ProcessMessage(ctx, systemPrompt, messages, tools)
		totalTokens += tokens

		if err != nil {
			return "", totalTokens, fmt.Errorf("LLM processing failed: %w", err)
		}

		// If no tool calls, return the response
		if len(toolCalls) == 0 {
			// Add assistant response to messages
			messages = append(messages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: response,
			})
			return response, totalTokens, nil
		}

		// Handle tool calls
		toolResults := make([]openai.ChatCompletionMessage, 0, len(toolCalls))
		for _, toolCall := range toolCalls {
			// Parse arguments
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				// If unmarshal fails, try to execute with empty args
				args = make(map[string]interface{})
			}

			// Execute tool
			result, err := executor(toolCall.Function.Name, args)
			if err != nil {
				result = fmt.Sprintf("Error executing tool %s: %v", toolCall.Function.Name, err)
			}

			// Add tool result to messages
			toolResults = append(toolResults, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    result,
				Name:       toolCall.Function.Name,
				ToolCallID: toolCall.ID,
			})
		}

		// Add assistant message with tool calls
		messages = append(messages, openai.ChatCompletionMessage{
			Role:      openai.ChatMessageRoleAssistant,
			ToolCalls: toolCalls,
		})

		// Add tool results
		messages = append(messages, toolResults...)
	}

	return "", totalTokens, fmt.Errorf("max iterations (%d) reached", maxIterations)
}

