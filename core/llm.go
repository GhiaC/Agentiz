package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// callLLM tries the backup LLM providers in order, then falls back to the default client.
func (ch *CoreHandler) callLLM(ctx context.Context, modelName string, messages []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionResponse, error) {
	if resp, ok := ch.backups.TryBackup(ctx, messages, tools, "CoreHandler"); ok {
		return resp, nil
	}

	systemPromptLen := 0
	for _, m := range messages {
		if m.Role == openai.ChatMessageRoleSystem {
			systemPromptLen += len(m.Content)
		}
	}
	log.Log.Infof("[CoreHandler] 🔵 DEFAULT LLM >> Using OpenAI | Model: %s | Messages: %d | Tools: %d | system_prompt_len=%d", modelName, len(messages), len(tools), systemPromptLen)
	request := openai.ChatCompletionRequest{
		Model:    modelName,
		Messages: messages,
		Tools:    tools,
	}
	resp, err := ch.llmClient.CreateChatCompletion(ctx, request)
	if err != nil {
		engine.LogLLMError("CoreHandler", modelName, err)
	} else if resp.Usage.TotalTokens > 0 {
		cacheTokens := 0
		if resp.Usage.PromptTokensDetails != nil {
			cacheTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		log.Log.Infof("[CoreHandler] 📊 TOKEN USAGE >> Model: %s | prompt=%d | completion=%d | total=%d | cache=%d",
			modelName, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, cacheTokens)
	}
	return resp, err
}

// generateSystemPrompt returns the per-user system-prompt array, served from a
// short-lived cache (config.SystemPromptCacheTTL, default 10m) so the hot routing
// path does not rebuild every section on every message. It rebuilds when the entry
// expires, when the user's Core session has been (re)summarized since the entry was
// built (its memory changed), or after invalidateSystemPrompt is called (session
// create/change, background summarization). coreSession is the freshly loaded Core
// session and may be nil.
func (ch *CoreHandler) generateSystemPrompt(userID string, coreSession *model.Session) ([]string, error) {
	ttl := ch.config.SystemPromptCacheTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	ch.systemPromptCacheMu.RLock()
	entry, ok := ch.systemPromptCache[userID]
	ch.systemPromptCacheMu.RUnlock()

	if ok && time.Since(entry.builtAt) < ttl && !memorySummarizedSince(coreSession, entry.builtAt) {
		metrics.SystemPromptCache("hit")
		return entry.prompts, nil
	}
	if ok {
		metrics.SystemPromptCache("stale")
	} else {
		metrics.SystemPromptCache("miss")
	}

	prompts, err := ch.buildSystemPrompts(userID)
	if err != nil {
		return nil, err
	}

	ch.systemPromptCacheMu.Lock()
	ch.systemPromptCache[userID] = cachedSystemPrompt{prompts: prompts, builtAt: time.Now()}
	ch.systemPromptCacheMu.Unlock()

	return prompts, nil
}

// memorySummarizedSince reports whether the Core session was summarized after t.
// When it was, sections 4–5 (the user's memory) are stale and the cached prompt
// must be rebuilt even inside the TTL window.
func memorySummarizedSince(coreSession *model.Session, t time.Time) bool {
	return coreSession != nil && coreSession.SummarizedAt.After(t)
}

// invalidateSystemPrompt drops the cached system-prompt array for a user so the
// next message rebuilds it. Safe to call when nothing is cached.
func (ch *CoreHandler) invalidateSystemPrompt(userID string) {
	ch.systemPromptCacheMu.Lock()
	delete(ch.systemPromptCache, userID)
	ch.systemPromptCacheMu.Unlock()
}

// InvalidateSystemPromptCache forces the Core to rebuild a user's cached
// system-prompt array on their next message. Wire this to the session scheduler's
// OnSessionSummarized hook (SessionSchedulerConfig) so a background summarization
// is reflected in the Core's memory immediately rather than only when the TTL
// expires.
func (ch *CoreHandler) InvalidateSystemPromptCache(userID string) {
	ch.invalidateSystemPrompt(userID)
}

// buildSystemPrompts builds the array of system prompts for the Core. Prefer
// generateSystemPrompt on the hot path; this is the uncached builder it wraps.
//
// It is a thin projection of assembleSections (the single source of truth, see
// prompt_sections.go): keep every section that survived the size budget, in
// order. SystemPromptSectionsFor returns the same assembly with full metadata
// for the debug UI, so the live array and the debug view never drift.
func (ch *CoreHandler) buildSystemPrompts(userID string) ([]string, error) {
	sections, err := ch.assembleSections(userID, ch.currentCoreSession(userID))
	if err != nil {
		return nil, err
	}
	prompts := make([]string, 0, len(sections))
	for _, s := range sections {
		if s.Included {
			prompts = append(prompts, s.Content)
		}
	}
	return prompts, nil
}

// userFileLister is the subset of the store used to read a user's files. The
// concrete store (store.Store) implements it; the narrow SessionStore interface
// does not expose it, so the Core type-asserts as it does elsewhere (see
// getOrCreateCoreSession). When the store lacks the method, the section is skipped.
type userFileLister interface {
	GetUserFilesByUser(userID string) ([]*model.UserFile, error)
}

// buildUserFilesPrompt builds the "User Files" system-prompt section: a compact
// table of the files the user has uploaded or been given, so the Core can pass a
// file's ID and name to a worker agent when delegating a file-related task (the
// agent reads the bytes on demand via its own file tool). Returns "" when the
// store does not track user files or the user has none.
func (ch *CoreHandler) buildUserFilesPrompt(userID string) string {
	lister, ok := ch.sessionHandler.GetStore().(userFileLister)
	if !ok {
		return ""
	}
	files, err := lister.GetUserFilesByUser(userID)
	if err != nil || len(files) == 0 {
		return ""
	}

	maxFiles := ch.config.maxUserFilesInPrompt()
	shown := files
	truncated := 0
	if len(shown) > maxFiles {
		truncated = len(shown) - maxFiles
		shown = shown[:maxFiles]
	}

	var sb strings.Builder
	sb.WriteString("# User Files\n\n")
	sb.WriteString("Files the user uploaded or was given. When a request involves one of these files, ")
	sb.WriteString("delegate to an agent and include the file's ID and name in your message so the agent can read it with its file tool. ")
	sb.WriteString("Do not paste file contents yourself.\n\n")
	sb.WriteString("| File ID | Name | Type | Size | Source |\n")
	sb.WriteString("|---------|------|------|------|--------|\n")
	for _, f := range shown {
		name := f.Name
		if f.Summary != "" {
			name = fmt.Sprintf("%s (%s)", f.Name, f.Summary)
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %s | %s | %s | %s |\n",
			f.FileID, name, f.MIMEType, humanizeBytes(f.Size), f.Source))
	}
	if truncated > 0 {
		sb.WriteString(fmt.Sprintf("\n(+%d more not shown)\n", truncated))
	}
	return sb.String()
}

// humanizeBytes formats a byte count as a short human-readable string (B, KB, MB…).
func humanizeBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (ch *CoreHandler) buildMessages(systemPrompts []string, conversationMsgs []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	messages := []openai.ChatCompletionMessage{}
	for _, prompt := range systemPrompts {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: prompt,
		})
	}
	messages = append(messages, conversationMsgs...)
	return messages
}

// processWithTools handles the LLM call and tool execution loop.
func (ch *CoreHandler) processWithTools(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
	userID string,
	coreSession *model.Session,
) (string, error) {
	maxIterations := ch.config.maxLLMIterations()
	currentMessages := messages

	modelName := ch.llmConfig.Model
	if modelName == "" {
		modelName = "openai/gpt-5-nano"
	}

	if coreSession != nil && coreSession.Model != modelName {
		coreSession.Model = modelName
		if err := ch.saveCoreSession(coreSession); err != nil {
			// Non-fatal: the model name is metadata, but a failing store is worth surfacing.
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to persist session model change | SessionID: %s | Error: %v", coreSession.SessionID, err)
		}
	}

	if _, ok := model.GetUserIDFromContext(ctx); !ok && userID != "" {
		ctx = model.WithUserID(ctx, userID)
	}

	sessionID := ""
	if coreSession != nil {
		sessionID = coreSession.SessionID
	}

	// Route-trace recorder for this message (nil-safe when tracing is disabled).
	rec := routeRecorderFrom(ctx)

	for i := 0; i < maxIterations; i++ {
		log.Log.Infof("[CoreHandler] 🔄 processWithTools iteration %d/%d | UserID: %s | Messages: %d",
			i+1, maxIterations, userID, len(currentMessages))

		engine.NotifyStatus(ctx, userID, sessionID, engine.StatusThinking, "")

		if ch.Callback != nil {
			if cbErr := ch.Callback.BeforeAction(ctx, &engine.UsageEvent{
				UserID:    userID,
				SessionID: sessionID,
				EventType: engine.EventLLMCall,
				Name:      engine.EventNameLLMCall,
				Model:     modelName,
			}); cbErr != nil {
				return cbErr.Error(), nil
			}
		}

		llmStart := time.Now()
		resp, err := ch.callLLM(ctx, modelName, currentMessages, tools)
		llmDuration := time.Since(llmStart)
		if err != nil {
			metrics.LLMCall("core", modelName, "error", llmDuration, 0, 0, 0)
			return "", engine.FormatLLMError(err)
		}

		cachedTokens := 0
		if resp.Usage.PromptTokensDetails != nil {
			cachedTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		metrics.LLMCall("core", modelName, "ok", llmDuration, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, cachedTokens)

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response from LLM")
		}

		choice := resp.Choices[0]

		if ch.Callback != nil {
			ev := &engine.UsageEvent{
				UserID:       userID,
				SessionID:    sessionID,
				EventType:    engine.EventLLMCall,
				Name:         engine.EventNameLLMCall,
				Tokens:       resp.Usage.TotalTokens,
				InputTokens:  resp.Usage.PromptTokens,
				OutputTokens: resp.Usage.CompletionTokens,
				Model:        modelName,
				Duration:     llmDuration,
			}
			if resp.Usage.PromptTokensDetails != nil {
				ev.CachedInputTokens = resp.Usage.PromptTokensDetails.CachedTokens
			}
			ch.Callback.AfterAction(ctx, ev)
		}

		request := openai.ChatCompletionRequest{Model: modelName, Messages: currentMessages, Tools: tools}
		messageID := ch.saveCoreMessage(userID, request, resp, choice)

		log.Log.Infof("[CoreHandler] 📊 LLM response | Iteration: %d | FinishReason: %s | ToolCalls: %d | ContentLen: %d",
			i+1, choice.FinishReason, len(choice.Message.ToolCalls), len(choice.Message.Content))

		// Record this LLM turn as a decision node on the routing DAG.
		rec.Decision(
			fmt.Sprintf("Decision %d", i+1),
			modelName,
			resp.Usage.TotalTokens,
			llmDuration.Milliseconds(),
			model.RouteStatusOK,
			fmt.Sprintf("finish_reason=%s · tool_calls=%d", choice.FinishReason, len(choice.Message.ToolCalls)),
		)

		if len(choice.Message.ToolCalls) == 0 {
			rec.Response(choice.Message.Content, false, model.RouteStatusOK)
			return choice.Message.Content, nil
		}

		currentMessages = append(currentMessages, choice.Message)

		// dispatched holds the answer of an agent that Core routed to.
		var dispatched string
		var didDispatch bool

		for _, toolCall := range choice.Message.ToolCalls {
			isAgentCall := strings.HasPrefix(toolCall.Function.Name, "call_agent_")

			// Once the Core has dispatched this turn, a second call_agent_* would
			// run another full worker agent (real LLM cost + latency) only for the
			// Core to discard its answer — it returns the FIRST agent's answer
			// verbatim (see below). So skip the redundant dispatch, recording it on
			// the DAG for visibility. Non-agent tools still run even after a
			// dispatch: they may carry side effects the user wants (status updates,
			// session changes, bans).
			if isAgentCall && didDispatch {
				result := ch.skipRedundantAgentCall(ctx, toolCall)
				currentMessages = append(currentMessages, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    result,
					ToolCallID: toolCall.ID,
				})
				continue
			}

			result := ch.executeCoreTool(ctx, userID, sessionID, coreSession, messageID, toolCall)

			log.Log.Infof("[CoreHandler] 🔧 Tool executed | Name: %s | ResultLen: %d",
				toolCall.Function.Name, len(result))

			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    result,
				ToolCallID: toolCall.ID,
			})

			// Core only dispatches: when it routes to an agent, that agent's
			// answer goes straight back to the user. The result does NOT
			// re-enter Core's LLM, so no closed graph is formed. If deeper /
			// longer planning is needed it must be done by the high-tier agent,
			// not by Core looping back on itself.
			if isAgentCall && !didDispatch {
				dispatched = result
				didDispatch = true
			}
		}

		if didDispatch {
			// The dispatched agent's answer is returned verbatim — record it as the
			// terminal response, linked from the agent node it came from.
			rec.Response(dispatched, true, model.RouteStatusOK)
			log.Log.Infof("[CoreHandler] ↩️  Returning agent answer directly (Core dispatch only) | UserID: %s | ResultLen: %d",
				userID, len(dispatched))
			return dispatched, nil
		}
	}

	return "", fmt.Errorf("max iterations (%d) reached without final response", maxIterations)
}
