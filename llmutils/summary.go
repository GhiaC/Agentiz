package llmutils

import (
	"context"
	"fmt"

	"github.com/sashabaranov/go-openai"
)

// LLMClient defines the interface for LLM operations
// This allows for easy mocking and testing
type LLMClient interface {
	CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

// SummaryConfig holds configuration for summary generation
type SummaryConfig struct {
	Model     string // LLM model to use (default: gpt-4o-mini)
	MaxTokens int    // Max tokens for response (default: 100)
}

// DefaultSummaryConfig returns default configuration
func DefaultSummaryConfig() SummaryConfig {
	return SummaryConfig{
		Model:     "gpt-4o-mini",
		MaxTokens: 100,
	}
}

// GenerateSummary uses LLM to generate a concise English summary for content.
// The summary describes the specific content of the document so users can understand what's inside.
func GenerateSummary(ctx context.Context, client LLMClient, content string, config SummaryConfig) (string, error) {
	if client == nil {
		return "", fmt.Errorf("LLM client is nil")
	}

	// Apply defaults
	if config.Model == "" {
		config.Model = "gpt-4o-mini"
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = 100
	}

	// Build prompt for content-focused summary
	systemPrompt := `You are a technical documentation summarizer.
Generate a concise English summary (1-2 sentences) that describes what this document contains.

Requirements:
- Focus on SPECIFIC content, not generic descriptions
- Mention specific tools, commands, APIs, or features described in the document
- The summary should help someone understand what they will find inside
- Be concrete: instead of "monitoring tools" say "Prometheus queries for CPU and memory metrics"
- Maximum 150 characters
- Do not use generic words like "overview", "management", "operations" unless absolutely necessary

Example good summary: "Describes kubectl commands for debugging pods: logs, exec, describe, and port-forward with examples"
Example bad summary: "Kubernetes pod management and monitoring overview"
`

	userPrompt := fmt.Sprintf("Summarize what this document contains:\n\n%s", content)

	// Make LLM call
	resp, err := client.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model: config.Model,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
				{Role: openai.ChatMessageRoleUser, Content: userPrompt},
			},
			MaxTokens: config.MaxTokens,
		},
	)

	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return resp.Choices[0].Message.Content, nil
}

// ConversationSummaryConfig holds configuration for conversation summarization
type ConversationSummaryConfig struct {
	Model     string // LLM model to use (default: gpt-4o-mini)
	MaxTokens int    // Max tokens for response (default: 200)
}

// DefaultConversationSummaryConfig returns default configuration for conversation summaries
func DefaultConversationSummaryConfig() ConversationSummaryConfig {
	return ConversationSummaryConfig{
		Model:     "gpt-4o-mini",
		MaxTokens: 200,
	}
}

// GenerateConversationSummary uses LLM to generate a summary of a conversation.
// The conversation should be formatted as a string with role labels (e.g., "user: ...\nassistant: ...")
func GenerateConversationSummary(ctx context.Context, client LLMClient, conversation string, config ConversationSummaryConfig) (string, error) {
	if client == nil {
		return "", fmt.Errorf("LLM client is nil")
	}

	// Apply defaults
	if config.Model == "" {
		config.Model = "gpt-4o-mini"
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = 200
	}

	systemPrompt := `You are a conversation summarizer.
Generate a concise summary (2-3 sentences) that captures the main topics and outcomes of this conversation.

Requirements:
- Focus on key topics discussed and any decisions or conclusions reached
- Be specific about what was accomplished or discussed
- Maximum 200 characters
- Use present or past tense appropriately
- Do not include greetings or filler content

Example: "Debugged Kubernetes pod restart issue. Found memory limits too low. Applied fix and verified pod stability."
`

	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: config.Model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Summarize this conversation:\n\n" + conversation},
		},
		MaxTokens: config.MaxTokens,
	})

	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return resp.Choices[0].Message.Content, nil
}

// GenerateSessionTitle uses LLM to generate a short title for a conversation session.
func GenerateSessionTitle(ctx context.Context, client LLMClient, conversation string, model string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("LLM client is nil")
	}

	if model == "" {
		model = "gpt-4o-mini"
	}

	systemPrompt := `Generate a short title (3-5 words) for this conversation.
The title should capture the main topic or purpose.
Return only the title, no quotes or extra text.

Example outputs:
- Kubernetes Pod Debugging
- API Authentication Design
- Database Migration Planning
- Quick Q&A Session`

	resp, err := client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Generate a title for this conversation:\n\n" + conversation},
		},
		MaxTokens: 20,
	})

	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return resp.Choices[0].Message.Content, nil
}

// FormatMessagesForSummary converts OpenAI messages to a readable format for summarization
func FormatMessagesForSummary(msgs []openai.ChatCompletionMessage) string {
	var result string
	for _, msg := range msgs {
		// Skip tool-related messages
		if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 {
			continue
		}

		content := msg.Content
		if content == "" {
			continue
		}

		// Truncate long messages
		if len(content) > 500 {
			content = content[:500] + "..."
		}

		result += fmt.Sprintf("%s: %s\n", msg.Role, content)
	}
	return result
}
