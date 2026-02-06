package llmutils

import (
	"context"
	"fmt"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// IsNonsenseMessageLLM uses LLM to verify if a message is nonsense (expensive, use sparingly)
func IsNonsenseMessageLLM(ctx context.Context, llmClient *openai.Client, model string, message string) (bool, error) {
	if llmClient == nil {
		return false, fmt.Errorf("LLM client not configured")
	}

	// Use LLM to detect nonsense messages
	systemPrompt := `You are a message quality checker. Determine if a user message is nonsense, spam, or meaningless.

A message is considered nonsense if it:
- Contains only random characters, symbols, or gibberish
- Is completely unrelated to any meaningful conversation
- Contains only emojis or symbols without text
- Is clearly spam or trolling
- Makes no sense in any context

A message is NOT nonsense if it:
- Contains actual questions or requests
- Has meaningful content, even if short
- Is part of a conversation
- Contains code, technical terms, or specific topics

Respond with only "YES" if the message is nonsense, or "NO" if it's meaningful.`

	resp, err := llmClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: message},
		},
		MaxTokens:   10,
		Temperature: 0.1,
	})

	if err != nil {
		return false, err
	}

	if len(resp.Choices) == 0 {
		return false, fmt.Errorf("no response from LLM")
	}

	response := strings.TrimSpace(strings.ToUpper(resp.Choices[0].Message.Content))
	return response == "YES" || strings.HasPrefix(response, "YES"), nil
}
