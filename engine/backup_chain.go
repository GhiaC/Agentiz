package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	llminterface "github.com/ghiac/agentize/llm-interface"
	"github.com/ghiac/agentize/log"
	"github.com/sashabaranov/go-openai"
)

// BackupLLM represents a backup LLM provider paired with a specific model.
// Multiple BackupLLM entries form a chain: tried in order, first success wins.
type BackupLLM struct {
	Provider llminterface.Provider
	Model    string // model name to pass to the provider (e.g. "@cf/openai/gpt-oss-120b")
	Name     string // human-readable name for logging (e.g. "cf-oss-120b")
}

// backupChain manages a chain of backup LLM providers with per-provider cooldowns.
// It is the single implementation used by both Engine and CoreHandler to avoid duplication.
type backupChain struct {
	providers  []BackupLLM
	cooldowns  map[string]time.Time
	cooldownMu sync.Mutex
}

// newBackupChain creates a backupChain from the given providers.
// Returns nil if providers is empty (caller should check for nil before calling tryBackup).
func newBackupChain(providers []BackupLLM) *backupChain {
	if len(providers) == 0 {
		return nil
	}
	return &backupChain{
		providers: providers,
		cooldowns: make(map[string]time.Time),
	}
}

// tryBackup iterates through backup providers in order and returns the first successful response.
// logPrefix is used for log messages (e.g. "Engine" or "CoreHandler").
// Returns (response, true) on success, or (zero, false) if all providers failed/skipped.
func (bc *backupChain) tryBackup(ctx context.Context, messages []openai.ChatCompletionMessage, tools []openai.Tool, logPrefix string) (openai.ChatCompletionResponse, bool) {
	if bc == nil || len(bc.providers) == 0 {
		return openai.ChatCompletionResponse{}, false
	}

	// Convert messages/tools once (shared across all providers)
	ifcMsgs := llminterface.FromOpenAIMessages(messages)
	ifcTools := llminterface.FromOpenAITools(tools)

	// Compute prompt stats once for logging
	promptChars := 0
	systemPromptLen := 0
	for _, m := range ifcMsgs {
		promptChars += len(m.Content) + len(m.ToolCallID)
		if m.Role == "system" {
			systemPromptLen += len(m.Content)
		}
		for _, tc := range m.ToolCalls {
			promptChars += len(tc.Arguments)
		}
	}

	for i, backup := range bc.providers {
		name := backup.Name
		if name == "" {
			name = fmt.Sprintf("backup-%d", i)
		}

		// Check per-provider cooldown
		bc.cooldownMu.Lock()
		cooldownUntil, hasCooldown := bc.cooldowns[name]
		inCooldown := hasCooldown && time.Now().Before(cooldownUntil)
		bc.cooldownMu.Unlock()

		if inCooldown {
			log.Log.Infof("[%s] ⏸️ BACKUP LLM >> Skipping %s (cooldown until %s)",
				logPrefix, name, cooldownUntil.Format(time.RFC3339))
			continue
		}

		log.Log.Infof("[%s] 🔄 BACKUP LLM >> Trying %s | Model: %s | Messages: %d | Tools: %d | Prompt ~%d chars | system_prompt_len=%d",
			logPrefix, name, backup.Model, len(ifcMsgs), len(ifcTools), promptChars, systemPromptLen)

		resp, err := backup.Provider.ChatCompletion(ctx, backup.Model, ifcMsgs, ifcTools)
		if err == nil && (resp.Content != "" || len(resp.ToolCalls) > 0) {
			// Success
			log.Log.Infof("[%s] ✅ BACKUP LLM >> Success | %s | Model: %s | Response: %d chars | ToolCalls: %d | Tokens: prompt=%d completion=%d total=%d",
				logPrefix, name, backup.Model, len(resp.Content), len(resp.ToolCalls),
				resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
			return llminterface.ToOpenAIResponse(resp), true
		}

		// Failed or empty: set per-provider cooldown and continue to next
		bc.cooldownMu.Lock()
		bc.cooldowns[name] = time.Now().Add(backupCooldownDuration)
		bc.cooldownMu.Unlock()

		if err != nil {
			log.Log.Warnf("[%s] ❌ BACKUP LLM >> %s failed | Model: %s | Error: %v | Messages: %d | Tools: %d",
				logPrefix, name, backup.Model, err, len(ifcMsgs), len(ifcTools))
			if cause := errors.Unwrap(err); cause != nil {
				log.Log.Warnf("[%s] ❌ BACKUP LLM >> Cause: %v", logPrefix, cause)
			}
		} else {
			reason := "API returned success but content and tool_calls are both empty"
			if resp.Usage.CompletionTokens == 0 {
				reason = "model produced 0 completion tokens (content filter, max_tokens, or empty API response)"
			}
			log.Log.Warnf("[%s] ❌ BACKUP LLM >> %s empty response | Model: %s | Tokens: prompt=%d completion=%d total=%d | Reason: %s",
				logPrefix, name, backup.Model,
				resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, reason)
		}

		log.Log.Warnf("[%s] ⏸️ BACKUP LLM >> %s disabled for %s", logPrefix, name, backupCooldownDuration)
	}

	// All providers failed or were in cooldown
	return openai.ChatCompletionResponse{}, false
}
