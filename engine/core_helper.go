package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/sashabaranov/go-openai"
)

// IsNonsenseMessageFast uses fast heuristics to detect nonsense messages (no LLM cost)
func IsNonsenseMessageFast(message string) bool {
	trimmed := strings.TrimSpace(message)

	// Very short messages
	if len(trimmed) < 3 {
		return true
	}

	// Count different character types
	hasLetter := false
	hasDigit := false
	hasSpace := false
	specialCharCount := 0
	emojiCount := 0
	repeatedCharCount := 0

	var lastChar rune
	repeatCount := 0

	for i, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= 'آ' && r <= 'ی') {
			hasLetter = true
		} else if r >= '0' && r <= '9' {
			hasDigit = true
		} else if r == ' ' || r == '\t' || r == '\n' {
			hasSpace = true
		} else {
			// Check for emoji or special characters
			if r > 127 {
				emojiCount++
			} else {
				specialCharCount++
			}
		}

		// Check for repeated characters
		if i > 0 && r == lastChar {
			repeatCount++
		} else {
			if repeatCount > 3 {
				repeatedCharCount += repeatCount
			}
			repeatCount = 1
		}
		lastChar = r
	}

	if repeatCount > 3 {
		repeatedCharCount += repeatCount
	}

	// Heuristic rules (fast, no LLM cost)

	// 1. Only special characters or emojis (no letters/numbers)
	if !hasLetter && !hasDigit && (specialCharCount > len(trimmed)/2 || emojiCount > len(trimmed)/2) {
		return true
	}

	// 2. Too many repeated characters (e.g., "aaaaaa", "111111")
	if repeatedCharCount > len(trimmed)/2 {
		return true
	}

	// 3. Too many special characters relative to text
	if hasLetter && specialCharCount > len(trimmed)/3 {
		return true
	}

	// 4. Very long message with no spaces (likely spam/gibberish)
	if len(trimmed) > 50 && !hasSpace {
		return true
	}

	// 5. Only numbers (unless it's a short number which might be valid)
	if !hasLetter && hasDigit && len(trimmed) > 10 {
		return true
	}

	// 6. Pattern detection: same character repeated many times
	if len(trimmed) > 5 {
		charFreq := make(map[rune]int)
		for _, r := range trimmed {
			charFreq[r]++
		}
		for _, count := range charFreq {
			if count > len(trimmed)*2/3 {
				return true // One character dominates
			}
		}
	}

	return false
}

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
