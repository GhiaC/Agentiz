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
