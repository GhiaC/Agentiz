package core

import (
	"context"
	"fmt"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/planning"
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

// buildSystemPrompts builds the array of system prompts for the Core.
func (ch *CoreHandler) buildSystemPrompts(userID string) ([]string, error) {
	prompts := []string{coreControllerPrompt}

	// Agent descriptions (replaces hardcoded UserAgent table)
	prompts = append(prompts, ch.agents.BuildAgentsDescriptionPrompt())

	// Agent tools (replaces buildUserAgentToolsPrompt)
	prompts = append(prompts, ch.agents.BuildAgentToolsPrompt())

	// Core's own session context (Summary + Tags)
	ch.coreSessionsMu.RLock()
	coreSession := ch.coreSessions[userID]
	ch.coreSessionsMu.RUnlock()
	if coreSession != nil {
		sessionContext := ch.buildCoreSessionContext(coreSession)
		if sessionContext != "" {
			prompts = append(prompts, sessionContext)
		}
	}

	// All agents' session contexts (Summary + Tags from each agent's active session)
	allContexts := ch.agents.BuildAllSessionContextsPrompt(
		ch.getSessionFunc(),
		ch.getActiveSessionIDFunc(),
		userID,
	)
	if allContexts != "" {
		prompts = append(prompts, allContexts)
	}

	// Active sessions prompt (which session is active per agent)
	activePrompt := ch.agents.BuildActiveSessionsPrompt(
		ch.getSessionFunc(),
		ch.getActiveSessionIDFunc(),
		userID,
	)
	if activePrompt != "" {
		prompts = append(prompts, activePrompt)
	}

	// Sessions list prompt (for change_session)
	sessionsPrompt, err := ch.sessionHandler.GetSessionsPrompt(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sessions prompt: %w", err)
	}
	prompts = append(prompts, sessionsPrompt)

	// Planning: when enabled, inject decision-making prompt so Core can choose execute_plan for multi-step tasks
	if ch.orchestrator != nil {
		prompts = append(prompts, planning.CorePrompt())
	}

	return prompts, nil
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
	const maxIterations = 10
	currentMessages := messages

	modelName := ch.llmConfig.Model
	if modelName == "" {
		modelName = "openai/gpt-5-nano"
	}

	if coreSession != nil && coreSession.Model != modelName {
		coreSession.Model = modelName
		_ = ch.saveCoreSession(coreSession)
	}

	if _, ok := model.GetUserIDFromContext(ctx); !ok && userID != "" {
		ctx = model.WithUserID(ctx, userID)
	}

	sessionID := ""
	if coreSession != nil {
		sessionID = coreSession.SessionID
	}

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

		if len(choice.Message.ToolCalls) == 0 {
			return choice.Message.Content, nil
		}

		currentMessages = append(currentMessages, choice.Message)

		for _, toolCall := range choice.Message.ToolCalls {
			result := ch.executeCoreTool(ctx, userID, sessionID, coreSession, messageID, toolCall)

			log.Log.Infof("[CoreHandler] 🔧 Tool executed | Name: %s | ResultLen: %d",
				toolCall.Function.Name, len(result))

			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    result,
				ToolCallID: toolCall.ID,
			})
		}
	}

	return "", fmt.Errorf("max iterations (%d) reached without final response", maxIterations)
}
