package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
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

// Default max runes for the initial web search message shown to the user.
const webSearchInitialMaxRunes = 1024

// FormatWebSearchInitialMessage builds a short, user-facing "initial result" message from a full search result.
// If maxRunes <= 0, webSearchInitialMaxRunes is used. Truncation prefers word/sentence boundaries and appends "…" when truncated.
func FormatWebSearchInitialMessage(result string, maxRunes int) string {
	const header = "🔍 نتیجه اولیه جستجو\n\n"
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return header + "—"
	}
	if maxRunes <= 0 {
		maxRunes = webSearchInitialMaxRunes
	}
	runes := []rune(trimmed)
	if len(runes) <= maxRunes {
		return header + trimmed
	}
	// Prefer cut at last space before limit; otherwise hard cut
	cut := maxRunes
	for i := cut - 1; i >= 0 && i < len(runes); i-- {
		if runes[i] == ' ' || runes[i] == '\n' || runes[i] == '.' || runes[i] == '۔' {
			cut = i + 1
			break
		}
	}
	return header + string(runes[:cut]) + "…"
}

// Search model names for web search capability
const (
	DefaultSearchModel            = "openai/gpt-4o-mini-search-preview"
	SearchModelTongyiDeepResearch = "alibaba/tongyi-deepresearch-30b-a3b"
)

// PerformWebSearch performs a web search using the default search-enabled model.
func PerformWebSearch(
	ctx context.Context,
	llmClient *openai.Client,
	llmConfig LLMConfig,
	query string,
	userID string,
) (string, error) {
	return PerformWebSearchWithModel(ctx, llmClient, llmConfig, query, userID, DefaultSearchModel)
}

// PerformWebSearchWithModel performs a web search using the given search-enabled model.
// Models: gpt-4o-search-preview, gpt-4o-mini-search-preview, or alibaba/tongyi-deepresearch-30b-a3b (etc.)
func PerformWebSearchWithModel(
	ctx context.Context,
	llmClient *openai.Client,
	llmConfig LLMConfig,
	query string,
	userID string,
	searchModel string,
) (string, error) {
	if searchModel == "" {
		searchModel = DefaultSearchModel
	}
	// Ensure userID is in context
	if userID != "" {
		ctx = model.WithUserID(ctx, userID)
	}

	log.Log.Infof("[WebSearch] 🔍 Performing web search | UserID: %s | Query: %s | Model: %s", userID, query, searchModel)

	request := openai.ChatCompletionRequest{
		Model: searchModel,
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: query,
			},
		},
	}

	resp, err := llmClient.CreateChatCompletion(ctx, request)
	if err != nil {
		log.Log.Errorf("[WebSearch] ❌ Web search failed | UserID: %s | Error: %v", userID, err)
		return "", fmt.Errorf("web search failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from web search")
	}

	result := resp.Choices[0].Message.Content
	log.Log.Infof("[WebSearch] ✅ Web search completed | UserID: %s | Result length: %d chars", userID, len(result))
	return result, nil
}
