package core

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/llmutils"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

//go:embed core_controller.md
var coreControllerPrompt string

// CoreHandlerConfig holds configuration for the CoreHandler.
type CoreHandlerConfig struct {
	CoreLLMConfig engine.LLMConfig

	// CoreModel is the model used for Core's orchestration decisions.
	CoreModel string

	// Session configuration
	AutoSummarizeThreshold int

	// WebSearchDisabled disables web_search and web_search_deepresearch tools.
	WebSearchDisabled bool
}

// DefaultCoreHandlerConfig returns default configuration.
func DefaultCoreHandlerConfig() CoreHandlerConfig {
	return CoreHandlerConfig{
		CoreModel:              "openai/gpt-5-nano",
		AutoSummarizeThreshold: 5,
		WebSearchDisabled:      true,
	}
}

// CoreHandler is the main orchestrator that manages user conversations
// and delegates tasks to specialized agents via AgentManager.
type CoreHandler struct {
	sessionHandler *model.SessionHandler
	agents         *agentmanager.AgentManager

	llmClient *openai.Client
	llmConfig engine.LLMConfig

	visionLLMClient *openai.Client
	visionLLMConfig *engine.LLMConfig

	coreSessions   map[string]*model.Session
	coreSessionsMu sync.RWMutex

	userMutexes   map[string]*sync.Mutex
	userMutexesMu sync.RWMutex

	userProgress *engine.ProgressGuard

	config CoreHandlerConfig

	coreTools *model.FunctionRegistry

	userModeration *engine.UserModeration

	backups *engine.BackupChain

	Callback engine.Callback
}

// NewCoreHandler creates a new CoreHandler with the given AgentManager.
func NewCoreHandler(
	sessionHandler *model.SessionHandler,
	agents *agentmanager.AgentManager,
	config CoreHandlerConfig,
) *CoreHandler {
	ch := &CoreHandler{
		sessionHandler: sessionHandler,
		agents:         agents,
		config:         config,
		coreSessions:   make(map[string]*model.Session),
		userMutexes:    make(map[string]*sync.Mutex),
		userProgress:   engine.NewProgressGuard(),
		coreTools:      model.NewFunctionRegistry(),
	}

	ch.registerCoreTools()

	return ch
}

func (ch *CoreHandler) getUserMutex(userID string) *sync.Mutex {
	ch.userMutexesMu.RLock()
	if mu, exists := ch.userMutexes[userID]; exists {
		ch.userMutexesMu.RUnlock()
		return mu
	}
	ch.userMutexesMu.RUnlock()

	ch.userMutexesMu.Lock()
	defer ch.userMutexesMu.Unlock()

	if mu, exists := ch.userMutexes[userID]; exists {
		return mu
	}

	mu := &sync.Mutex{}
	ch.userMutexes[userID] = mu
	return mu
}

// SetCallback sets the billing/usage callback on the CoreHandler and propagates it to all agents.
func (ch *CoreHandler) SetCallback(cb engine.Callback) {
	ch.Callback = cb
	ch.agents.SetCallback(cb)
}

// UseLLMConfig configures the LLM client for the Core's orchestration.
func (ch *CoreHandler) UseLLMConfig(config engine.LLMConfig) error {
	openaiConfig := openai.DefaultConfig(config.APIKey)
	if config.BaseURL != "" {
		openaiConfig.BaseURL = config.BaseURL
	}
	if config.HTTPClient != nil {
		openaiConfig.HTTPClient = config.HTTPClient
	}

	ch.llmClient = openai.NewClientWithConfig(openaiConfig)
	ch.llmConfig = config

	if config.BackupDisabled {
		ch.backups = nil
	} else {
		ch.backups = engine.NewBackupChain(config.BackupProviders)
	}

	ch.userModeration = engine.NewUserModeration(
		engine.IsNonsenseMessageFast,
		func(ctx context.Context, msg string) (bool, error) {
			return llmutils.IsNonsenseMessageLLM(ctx, ch.llmClient, ch.llmConfig.Model, msg)
		},
		ch.getOrCreateUser,
		ch.saveUser,
	)

	return nil
}

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
	if err == nil && resp.Usage.TotalTokens > 0 {
		cacheTokens := 0
		if resp.Usage.PromptTokensDetails != nil {
			cacheTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		log.Log.Infof("[CoreHandler] 📊 TOKEN USAGE >> Model: %s | prompt=%d | completion=%d | total=%d | cache=%d",
			modelName, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, cacheTokens)
	}
	return resp, err
}

// SetHTTPClient sets a custom HTTP client (e.g., for proxy support).
func (ch *CoreHandler) SetHTTPClient(client *http.Client) {
	if ch.llmConfig.HTTPClient == nil {
		ch.llmConfig.HTTPClient = client
	}
}

// ProcessMessage is the main entry point for user messages.
func (ch *CoreHandler) ProcessMessage(
	ctx context.Context,
	userID string,
	userMessage string,
) (string, error) {
	return ch.ProcessMessageWithContentType(ctx, userID, userMessage, model.ContentTypeText)
}

// ProcessMessageWithContentType is like ProcessMessage but stores the user message with the given content type.
func (ch *CoreHandler) ProcessMessageWithContentType(
	ctx context.Context,
	userID string,
	userMessage string,
	contentType model.ContentType,
) (string, error) {
	if ch.userProgress.TryQueue(userID, userMessage) {
		return "⏳ Processing previous request... Please wait. 📋 Your message was queued and will be answered in order.", nil
	}
	userMu := ch.getUserMutex(userID)
	userMu.Lock()
	defer userMu.Unlock()
	ch.userProgress.SetInProgress(userID, true)
	defer ch.userProgress.SetInProgress(userID, false)

	response, err := ch.processOneMessageCore(ctx, userID, userMessage, contentType)
	if err != nil {
		return "", err
	}
	return response, nil
}

func (ch *CoreHandler) processOneMessageCore(
	ctx context.Context,
	userID string,
	userMessage string,
	contentType model.ContentType,
) (string, error) {
	engine.NotifyStatus(ctx, userID, "", engine.StatusReceived, "")

	userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
	ch.coreSessionsMu.RLock()
	totalCoreSessions := len(ch.coreSessions)
	ch.coreSessionsMu.RUnlock()

	log.Log.Infof("[CoreHandler] 🚀 Processing new message | UserID: %s | Message length: %d chars | User sessions: %d | Total Core sessions: %d",
		userID, len(userMessage), len(userSessions), totalCoreSessions)

	if !ch.agents.IsReady() {
		return "", fmt.Errorf("database is not ready. Call Init() on agent engines first")
	}
	if ch.llmClient == nil {
		return "", fmt.Errorf("LLM client not configured. Call UseLLMConfig first")
	}

	engine.NotifyStatus(ctx, userID, "", engine.StatusAnalyzing, "")

	var isNonsense bool
	if ch.userModeration != nil {
		if isBanned, banMessage := ch.userModeration.CheckBanStatus(userID); isBanned {
			return banMessage, nil
		}
		ctx = model.WithUserID(ctx, userID)
		shouldBan, banMessage, err := ch.userModeration.ProcessNonsenseCheck(ctx, userID, userMessage)
		if err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to process nonsense check, proceeding anyway | UserID: %s | Error: %v", userID, err)
		} else {
			isNonsense = banMessage != "" || shouldBan
			if shouldBan {
				return banMessage, nil
			}
			if banMessage != "" {
				return banMessage, nil
			}
		}
	}

	coreSession, err := ch.getOrCreateCoreSession(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get or create core session: %w", err)
	}
	systemPrompts, err := ch.buildSystemPrompts(userID)
	if err != nil {
		return "", fmt.Errorf("failed to build system prompts: %w", err)
	}

	coreSession.Msgs = append(
		coreSession.Msgs,
		openai.ChatCompletionMessage{Role: openai.ChatMessageRoleUser, Content: userMessage},
	)
	userMsgID, userSeqID := coreSession.GenerateMessageIDWithSeq()
	userMsg := model.NewUserMessage(userMsgID, userSeqID, userID, coreSession.SessionID, userMessage, contentType)
	userMsg.IsNonsense = isNonsense
	ch.saveMessage(userMsg)
	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}

	messages := ch.buildMessages(systemPrompts, coreSession.Msgs)
	tools := ch.getCoreToolsForLLM()
	ctx = model.WithUserID(ctx, userID)
	engine.NotifyStatus(ctx, userID, coreSession.SessionID, engine.StatusRouting, "")

	response, err := ch.processWithTools(ctx, messages, tools, userID, coreSession)
	if err != nil {
		return "", fmt.Errorf("failed to process message: %w", err)
	}

	coreSession.Msgs = append(
		coreSession.Msgs,
		openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: response},
	)
	coreSession.UpdatedAt = time.Now()
	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}
	engine.NotifyStatus(ctx, userID, coreSession.SessionID, engine.StatusCompleted, "")
	return response, nil
}

// getOrCreateCoreSession gets or creates a Core session for a user.
func (ch *CoreHandler) getOrCreateCoreSession(userID string) (*model.Session, error) {
	ch.coreSessionsMu.RLock()
	session, exists := ch.coreSessions[userID]
	ch.coreSessionsMu.RUnlock()

	if exists {
		dbSession, err := ch.sessionHandler.GetSession(session.SessionID)
		if err == nil && dbSession != nil {
			ch.coreSessionsMu.Lock()
			ch.coreSessions[userID] = dbSession
			ch.coreSessionsMu.Unlock()
			return dbSession, nil
		}
	}

	ch.coreSessionsMu.Lock()
	defer ch.coreSessionsMu.Unlock()

	if session, exists = ch.coreSessions[userID]; exists {
		dbSession, err := ch.sessionHandler.GetSession(session.SessionID)
		if err == nil && dbSession != nil {
			ch.coreSessions[userID] = dbSession
			return dbSession, nil
		}
	}

	activeSessionID := ch.getActiveSessionID(userID, model.AgentTypeCore)
	if activeSessionID != "" {
		activeSession, err := ch.sessionHandler.GetSession(activeSessionID)
		if err == nil && activeSession != nil {
			ch.coreSessions[userID] = activeSession
			return activeSession, nil
		}
		log.Log.Warnf("[CoreHandler] ⚠️  Active Core session no longer exists | UserID: %s | OldSessionID: %s",
			userID, activeSessionID)
	}

	// Fallback: Try to get existing Core session from database
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		GetCoreSession(string) (*model.Session, error)
	}); ok {
		existingCore, err := sqliteStore.GetCoreSession(userID)
		if err == nil && existingCore != nil {
			ch.coreSessions[userID] = existingCore
			_ = ch.setActiveSessionID(userID, model.AgentTypeCore, existingCore.SessionID)
			return existingCore, nil
		}
	} else {
		allSessions, err := ch.sessionHandler.ListUserSessions(userID)
		if err == nil {
			for _, s := range allSessions {
				if s.AgentType == model.AgentTypeCore {
					ch.coreSessions[userID] = s
					_ = ch.setActiveSessionID(userID, model.AgentTypeCore, s.SessionID)
					return s, nil
				}
			}
		}
	}

	session, err := ch.createSessionForUser(userID, model.AgentTypeCore)
	if err != nil {
		return nil, fmt.Errorf("failed to create core session: %w", err)
	}

	ch.coreSessions[userID] = session
	log.Log.Infof("[CoreHandler] ✨ Created new Core session | UserID: %s | SessionID: %s", userID, session.SessionID)

	return session, nil
}

func (ch *CoreHandler) saveCoreSession(session *model.Session) error {
	ch.coreSessionsMu.Lock()
	ch.coreSessions[session.UserID] = session
	ch.coreSessionsMu.Unlock()

	store := ch.sessionHandler.GetStore()
	if err := store.Put(session); err != nil {
		return fmt.Errorf("failed to save core session: %w", err)
	}
	return nil
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

	return prompts, nil
}

func (ch *CoreHandler) getSessionFunc() agentmanager.SessionGetter {
	return func(sessionID string) (*model.Session, error) {
		return ch.sessionHandler.GetSession(sessionID)
	}
}

func (ch *CoreHandler) getActiveSessionIDFunc() agentmanager.ActiveSessionIDGetter {
	return func(userID string, agentType model.AgentType) string {
		return ch.getActiveSessionID(userID, agentType)
	}
}

func (ch *CoreHandler) buildCoreSessionContext(session *model.Session) string {
	if session.Summary == "" && len(session.Tags) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Core Session Context\n\n")
	sb.WriteString("This is a continuation of a previous conversation. Here is the context from earlier messages:\n\n")

	if session.Summary != "" {
		sb.WriteString("## Summary of Previous Conversation\n")
		sb.WriteString(session.Summary)
		sb.WriteString("\n\n")
	}

	if len(session.Tags) > 0 {
		sb.WriteString("## Topics Discussed\n")
		sb.WriteString(strings.Join(session.Tags, ", "))
		sb.WriteString("\n")
	}

	return sb.String()
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

// getCoreToolsForLLM returns the tools in OpenAI format using dynamic agent tools.
func (ch *CoreHandler) getCoreToolsForLLM() []openai.Tool {
	// Dynamic call_agent_{name} tools from AgentManager
	tools := ch.agents.BuildCallTools()

	// Session management tools with dynamic agent names
	tools = append(tools, ch.agents.BuildSessionManagementTools()...)

	// Static tools
	tools = append(tools,
		openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_sessions",
				Description: "Get a list of all sessions for the current user. Use to find sessions for change_session.",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "ban_user",
				Description: "Ban the current user for a specified duration. Use this when a user repeatedly sends nonsense messages or violates rules.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"duration_hours": map[string]interface{}{
							"type":        "number",
							"description": "Ban duration in hours (0 for permanent ban)",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "Optional custom ban message to show to the user",
						},
					},
					"required": []string{"duration_hours"},
				},
			},
		},
		openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "update_status",
				Description: "Send a real-time status/progress update to the user. " +
					"Use before long operations to inform the user what you are doing.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{
							"type":        "string",
							"description": "Status message to show the user (in Persian)",
						},
					},
					"required": []string{"message"},
				},
			},
		},
	)

	if !ch.config.WebSearchDisabled {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "web_search",
				Description: "Search the web for up-to-date information.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query",
						},
					},
					"required": []string{"query"},
				},
			},
		})
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "web_search_deepresearch",
				Description: "Web search using Tongyi DeepResearch model for deep-research style results.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query",
						},
					},
					"required": []string{"query"},
				},
			},
		})
	}

	return tools
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
			return "", engine.FormatLLMError(err)
		}

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

// executeCoreTool executes a Core tool and returns the result string.
func (ch *CoreHandler) executeCoreTool(
	ctx context.Context,
	userID, sessionID string,
	coreSession *model.Session,
	messageID string,
	toolCall openai.ToolCall,
) string {
	persister := ch.getToolCallPersister()
	var toolID string
	if coreSession != nil {
		toolID = persister.SaveWithAgentType(coreSession, messageID, toolCall, model.AgentTypeCore)
	}

	toolDetail := ch.coreTools.GetDisplayName(toolCall.Function.Name)
	if toolDetail == "" {
		toolDetail = toolCall.Function.Name
	}
	engine.NotifyStatus(ctx, userID, sessionID, engine.StatusToolExecuting, toolDetail)

	if ch.Callback != nil {
		if cbErr := ch.Callback.BeforeAction(ctx, &engine.UsageEvent{
			UserID:    userID,
			SessionID: sessionID,
			EventType: engine.EventToolCall,
			Name:      toolCall.Function.Name,
		}); cbErr != nil {
			result := engine.FormatBlockedActionResult(cbErr)
			persister.Update(toolID, result, cbErr)
			return result
		}
	}

	toolStart := time.Now()
	result, err := ch.runCoreToolImpl(ctx, userID, sessionID, toolCall)
	toolDuration := time.Since(toolStart)
	if err != nil {
		result = fmt.Sprintf("Error executing tool: %v", err)
	}

	if ch.Callback != nil {
		ch.Callback.AfterAction(ctx, &engine.UsageEvent{
			UserID:    userID,
			SessionID: sessionID,
			EventType: engine.EventToolCall,
			Name:      toolCall.Function.Name,
			Duration:  toolDuration,
			Error:     err,
		})
	}

	engine.NotifyStatus(ctx, userID, sessionID, engine.StatusToolDone, toolDetail)
	persister.Update(toolID, result, err)

	return result
}

// runCoreToolImpl runs the Core tool logic (switch on tool name).
func (ch *CoreHandler) runCoreToolImpl(
	ctx context.Context,
	userID, sessionID string,
	toolCall openai.ToolCall,
) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("failed to parse tool arguments: %w", err)
	}

	toolName := toolCall.Function.Name

	// Dynamic agent dispatch: call_agent_{name}
	if strings.HasPrefix(toolName, "call_agent_") {
		agentName := strings.TrimPrefix(toolName, "call_agent_")
		agent, ok := ch.agents.Get(agentName)
		if !ok {
			return "", fmt.Errorf("unknown agent: %s", agentName)
		}

		engine.NotifyStatus(ctx, userID, "", engine.StatusAgentCalling, agentName)
		if ch.Callback != nil {
			if cbErr := ch.Callback.BeforeAction(ctx, &engine.UsageEvent{
				UserID: userID, EventType: engine.EventAgentRouting, Name: agentName,
			}); cbErr != nil {
				return engine.FormatBlockedActionResult(cbErr), nil
			}
		}

		result, err := ch.callAgent(ctx, userID, args, agent)

		// Escalation: if this is not the highest-tier agent and result starts with "ESCALATE:"
		if err == nil && strings.HasPrefix(strings.TrimSpace(result), "ESCALATE:") {
			if ch.Callback != nil {
				ch.Callback.AfterAction(ctx, &engine.UsageEvent{
					UserID: userID, SessionID: sessionID, EventType: engine.EventAgentRouting, Name: agentName,
				})
			}
			// Find a higher-tier agent
			higherAgent := ch.findHigherTierAgent(agent.Config.CostTier)
			if higherAgent != nil {
				engine.NotifyStatus(ctx, userID, "", engine.StatusAgentCalling, higherAgent.Config.Name+" (escalated)")
				result, err = ch.callAgent(ctx, userID, args, higherAgent)
				if ch.Callback != nil {
					ch.Callback.AfterAction(ctx, &engine.UsageEvent{
						UserID: userID, SessionID: sessionID, EventType: engine.EventAgentRouting, Name: higherAgent.Config.Name, Error: err,
					})
				}
				engine.NotifyStatus(ctx, userID, "", engine.StatusAgentDone, higherAgent.Config.Name)
				return result, err
			}
		}

		if ch.Callback != nil {
			ch.Callback.AfterAction(ctx, &engine.UsageEvent{
				UserID: userID, SessionID: sessionID, EventType: engine.EventAgentRouting, Name: agentName, Error: err,
			})
		}
		engine.NotifyStatus(ctx, userID, "", engine.StatusAgentDone, agentName)
		return result, err
	}

	switch toolName {
	case "update_status":
		message, _ := args["message"].(string)
		if message != "" {
			engine.NotifyStatus(ctx, userID, "", engine.StatusCustom, message)
		}
		return "status updated", nil

	case "create_session":
		return ch.createSessionTool(ctx, userID, args)

	case "change_session":
		return ch.changeSessionTool(ctx, userID, args)

	case "list_sessions":
		return ch.listSessionsTool(userID)

	case "ban_user":
		return ch.banUserTool(ctx, userID, args)

	case "web_search":
		return ch.webSearchWithModelTool(ctx, userID, args, "")
	case "web_search_deepresearch":
		return ch.webSearchWithModelTool(ctx, userID, args, engine.SearchModelTongyiDeepResearch)

	default:
		return "", fmt.Errorf("unknown tool: %s", toolName)
	}
}

// callAgent sends a message to an agent's Engine.
func (ch *CoreHandler) callAgent(
	ctx context.Context,
	userID string,
	args map[string]interface{},
	agent *agentmanager.RegisteredAgent,
) (string, error) {
	message, ok := args["message"].(string)
	if !ok || message == "" {
		return "", fmt.Errorf("message is required")
	}

	sessionID, err := ch.getOrCreateActiveSession(userID, agent.Config.AgentType)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Failed to get/create active session | UserID: %s | Agent: %s | Error: %v",
			userID, agent.Config.Name, err)
		return "", fmt.Errorf("failed to get active session: %w", err)
	}

	log.Log.Infof("[CoreHandler] 🎯 Calling agent | Agent: %s | SessionID: %s | UserID: %s | Message length: %d chars",
		agent.Config.Name, sessionID, userID, len(message))

	response, _, err := agent.Engine.ProcessMessage(ctx, sessionID, message)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Agent processing failed | Agent: %s | SessionID: %s | Error: %v", agent.Config.Name, sessionID, err)
		return "", fmt.Errorf("agent %s error: %w", agent.Config.Name, err)
	}

	log.Log.Infof("[CoreHandler] ✅ Agent response received | Agent: %s | SessionID: %s | Response length: %d chars",
		agent.Config.Name, sessionID, len(response))

	return response, nil
}

// findHigherTierAgent returns an agent with a higher cost tier, or nil if none found.
func (ch *CoreHandler) findHigherTierAgent(currentTier agentmanager.CostTier) *agentmanager.RegisteredAgent {
	var targetTier agentmanager.CostTier
	switch currentTier {
	case agentmanager.CostTierLow:
		targetTier = agentmanager.CostTierMedium
	case agentmanager.CostTierMedium:
		targetTier = agentmanager.CostTierHigh
	default:
		return nil
	}

	agents := ch.agents.GetByTier(targetTier)
	if len(agents) > 0 {
		return agents[0]
	}

	// If medium not found, try high
	if targetTier == agentmanager.CostTierMedium {
		agents = ch.agents.GetByTier(agentmanager.CostTierHigh)
		if len(agents) > 0 {
			return agents[0]
		}
	}
	return nil
}

// createSessionTool creates a new session for a dynamic agent.
func (ch *CoreHandler) createSessionTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	agentName, ok := args["agent_name"].(string)
	if !ok || agentName == "" {
		return "", fmt.Errorf("agent_name is required")
	}

	agent, ok := ch.agents.Get(agentName)
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", agentName)
	}

	log.Log.Infof("[CoreHandler] 🛠️  createSessionTool | UserID: %s | Agent: %s", userID, agentName)

	session, err := ch.createSessionForUser(userID, agent.Config.AgentType)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	if title, ok := args["title"].(string); ok && title != "" {
		session.Title = title
		ch.sessionHandler.UpdateSessionMetadata(session.SessionID, title, nil, "")
	}

	if err := ch.setActiveSessionID(userID, agent.Config.AgentType, session.SessionID); err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to set active session | UserID: %s | Agent: %s | Error: %v", userID, agentName, err)
	}

	return fmt.Sprintf("Created new session and set as active (agent: %s)", agentName), nil
}

// changeSessionTool switches to an existing session.
func (ch *CoreHandler) changeSessionTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	agentName, ok := args["agent_name"].(string)
	if !ok || agentName == "" {
		return "", fmt.Errorf("agent_name is required")
	}

	sessionID, ok := args["session_id"].(string)
	if !ok || sessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}

	agent, ok := ch.agents.Get(agentName)
	if !ok {
		return "", fmt.Errorf("unknown agent: %s", agentName)
	}

	session, err := ch.sessionHandler.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	if session.AgentType != agent.Config.AgentType {
		return "", fmt.Errorf("session %s is not a %s session (it's a %s session)", sessionID, agentName, session.AgentType)
	}

	if err := ch.setActiveSessionID(userID, agent.Config.AgentType, sessionID); err != nil {
		return "", fmt.Errorf("failed to set active session: %w", err)
	}

	title := session.Title
	if title == "" {
		title = "Untitled"
	}

	return fmt.Sprintf("Switched to session: %s (agent: %s)", title, agentName), nil
}

func (ch *CoreHandler) listSessionsTool(userID string) (string, error) {
	_, err := ch.sessionHandler.ListUserSessions(userID)
	if err != nil {
		return "", err
	}
	return ch.sessionHandler.GetSessionsPrompt(userID)
}

func (ch *CoreHandler) banUserTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	if userID == "" {
		return "", fmt.Errorf("user_id is required but not available in context")
	}

	durationHours, ok := args["duration_hours"].(float64)
	if !ok {
		return "", fmt.Errorf("duration_hours is required and must be a number")
	}

	message, _ := args["message"].(string)
	if message == "" {
		if durationHours == 0 {
			message = "You have been permanently restricted."
		} else {
			message = fmt.Sprintf("You have been restricted for %.0f hours.", durationHours)
		}
	}

	user, err := ch.getOrCreateUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get user: %w", err)
	}

	var banDuration time.Duration
	if durationHours > 0 {
		banDuration = time.Duration(durationHours) * time.Hour
	}

	user.Ban(banDuration, message)
	if err := ch.saveUser(user); err != nil {
		return "", fmt.Errorf("failed to save user ban: %w", err)
	}

	log.Log.Infof("[CoreHandler] 🚫 User banned | UserID: %s | Duration: %v", userID, banDuration)
	return fmt.Sprintf("User %s has been banned. Duration: %v", userID, banDuration), nil
}

func (ch *CoreHandler) webSearchWithModelTool(ctx context.Context, userID string, args map[string]interface{}, searchModel string) (string, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "", fmt.Errorf("query is required")
	}
	result, err := engine.PerformWebSearchWithModel(ctx, ch.llmClient, ch.llmConfig, query, userID, searchModel)
	if err != nil {
		return "", fmt.Errorf("web search failed: %w", err)
	}
	if result != "" {
		initialMessage := engine.FormatWebSearchInitialMessage(result, 0)
		engine.NotifyStatus(ctx, userID, "", engine.StatusCustom, initialMessage, engine.OptSendAsNewMessage())
	}
	return result, nil
}

// registerCoreTools registers display names for status/UI.
func (ch *CoreHandler) registerCoreTools() {
	coreToolNoOp := func(args map[string]interface{}) (string, error) { return "", nil }

	// Dynamic agent tools
	for _, agent := range ch.agents.GetAll() {
		toolName := "call_agent_" + agent.Config.Name
		ch.coreTools.MustRegister(toolName, agent.Config.DisplayName, coreToolNoOp)
	}

	ch.coreTools.MustRegister("update_status", "به‌روزرسانی وضعیت", coreToolNoOp)
	ch.coreTools.MustRegister("create_session", "ایجاد نشست", coreToolNoOp)
	ch.coreTools.MustRegister("change_session", "تغییر نشست", coreToolNoOp)
	ch.coreTools.MustRegister("list_sessions", "لیست نشست‌ها", coreToolNoOp)
	ch.coreTools.MustRegister("ban_user", "مسدود کاربر", coreToolNoOp)
	ch.coreTools.MustRegister("web_search", "جستجوی وب", coreToolNoOp)
	ch.coreTools.MustRegister("web_search_deepresearch", "جستجوی وب (عمیق)", coreToolNoOp)
}

// saveCoreMessage saves a message from CoreHandler to the database.
func (ch *CoreHandler) saveCoreMessage(
	userID string,
	request openai.ChatCompletionRequest,
	response openai.ChatCompletionResponse,
	choice openai.ChatCompletionChoice,
) string {
	coreSession, err := ch.getOrCreateCoreSession(userID)
	if err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to get core session for message save | UserID: %s | Error: %v", userID, err)
		return ""
	}

	content := choice.Message.Content
	if content == "" && len(choice.Message.ToolCalls) > 0 {
		content = engine.FormatToolCallsContent(choice.Message.ToolCalls)
	}

	messageID, seqID := coreSession.GenerateMessageIDWithSeq()
	msg := model.NewMessage(
		messageID,
		seqID,
		userID,
		coreSession.SessionID,
		openai.ChatMessageRoleAssistant,
		content,
		model.AgentTypeCore,
		model.ContentTypeText,
		request,
		response,
		choice,
	)

	ch.saveMessage(msg)
	return msg.MessageID
}

func (ch *CoreHandler) saveMessage(msg *model.Message) {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		PutMessage(*model.Message) error
	}); ok {
		if err := sqliteStore.PutMessage(msg); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to save message | MessageID: %s | Error: %v", msg.MessageID, err)
		}
	}
}

func (ch *CoreHandler) getToolCallPersister() *engine.ToolCallPersister {
	return engine.NewToolCallPersister(ch.sessionHandler.GetStore(), "CoreHandler")
}

// GetSessionHandler returns the session handler.
func (ch *CoreHandler) GetSessionHandler() *model.SessionHandler {
	return ch.sessionHandler
}

// GetAgents returns the AgentManager.
func (ch *CoreHandler) GetAgents() *agentmanager.AgentManager {
	return ch.agents
}

// GetCoreTools returns the function registry for Core tools.
func (ch *CoreHandler) GetCoreTools() *model.FunctionRegistry {
	return ch.coreTools
}

func (ch *CoreHandler) getOrCreateUser(userID string) (*model.User, error) {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		GetOrCreateUser(string) (*model.User, error)
	}); ok {
		return sqliteStore.GetOrCreateUser(userID)
	}
	return nil, nil
}

func (ch *CoreHandler) saveUser(user *model.User) error {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		PutUser(*model.User) error
	}); ok {
		return sqliteStore.PutUser(user)
	}
	return fmt.Errorf("store does not support user management")
}

func (ch *CoreHandler) createSessionForUser(userID string, agentType model.AgentType) (*model.Session, error) {
	user, err := ch.getOrCreateUser(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	if user == nil {
		return ch.sessionHandler.CreateSession(userID, agentType)
	}

	session, err := ch.sessionHandler.CreateSessionForUser(user, agentType)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	if err := ch.saveUser(user); err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to save user after session creation | UserID: %s | Error: %v", userID, err)
	}

	return session, nil
}

func (ch *CoreHandler) getActiveSessionID(userID string, agentType model.AgentType) string {
	user, err := ch.getOrCreateUser(userID)
	if err != nil || user == nil {
		return ""
	}
	return user.GetActiveSessionID(agentType)
}

func (ch *CoreHandler) setActiveSessionID(userID string, agentType model.AgentType, sessionID string) error {
	if sessionID != "" {
		session, err := ch.sessionHandler.GetSession(sessionID)
		if err != nil || session == nil {
			return fmt.Errorf("session not found in database: %s", sessionID)
		}
	}

	user, err := ch.getOrCreateUser(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return fmt.Errorf("user not found and could not be created")
	}

	user.SetActiveSessionID(agentType, sessionID)
	if err := ch.saveUser(user); err != nil {
		return fmt.Errorf("failed to save user: %w", err)
	}

	log.Log.Infof("[CoreHandler] 📌 Active session set | UserID: %s | AgentType: %s | SessionID: %s",
		userID, agentType, sessionID)
	return nil
}

func (ch *CoreHandler) getOrCreateActiveSession(userID string, agentType model.AgentType) (string, error) {
	sessionID := ch.getActiveSessionID(userID, agentType)
	if sessionID != "" {
		session, err := ch.sessionHandler.GetSession(sessionID)
		if err == nil && session != nil {
			return sessionID, nil
		}
		log.Log.Warnf("[CoreHandler] ⚠️  Active session no longer exists, creating new | UserID: %s | AgentType: %s",
			userID, agentType)
	}

	session, err := ch.createSessionForUser(userID, agentType)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	log.Log.Infof("[CoreHandler] ✨ Auto-created active session | UserID: %s | AgentType: %s | SessionID: %s",
		userID, agentType, session.SessionID)
	return session.SessionID, nil
}

// ============================================================================
// Vision LLM Support
// ============================================================================

// UseVisionLLMConfig configures a separate LLM client for image processing.
func (ch *CoreHandler) UseVisionLLMConfig(config engine.LLMConfig) error {
	openaiConfig := openai.DefaultConfig(config.APIKey)
	if config.BaseURL != "" {
		openaiConfig.BaseURL = config.BaseURL
	}
	if config.HTTPClient != nil {
		openaiConfig.HTTPClient = config.HTTPClient
	}

	ch.visionLLMClient = openai.NewClientWithConfig(openaiConfig)
	ch.visionLLMConfig = &config

	log.Log.Infof("[CoreHandler] ✅ Vision LLM configured | Model: %s | BaseURL: %s", config.Model, config.BaseURL)
	return nil
}

// ProcessMessageWithImage handles messages that include an image.
func (ch *CoreHandler) ProcessMessageWithImage(
	ctx context.Context,
	userID string,
	userMessage string,
	imageData []byte,
	imageMimeType string,
) (string, error) {
	userMu := ch.getUserMutex(userID)
	userMu.Lock()
	defer userMu.Unlock()

	log.Log.Infof("[CoreHandler] 🖼️  Processing image message | UserID: %s | Message length: %d chars | Image size: %d bytes | MimeType: %s",
		userID, len(userMessage), len(imageData), imageMimeType)

	if !ch.agents.IsReady() {
		return "", fmt.Errorf("database is not ready. Call Init() on agent engines first")
	}

	llmClient := ch.visionLLMClient
	llmModel := ""
	if ch.visionLLMConfig != nil {
		llmModel = ch.visionLLMConfig.Model
	}

	if llmClient == nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Vision LLM not configured, falling back to main LLM")
		llmClient = ch.llmClient
		llmModel = ch.llmConfig.Model
	}

	if llmClient == nil {
		return "", fmt.Errorf("LLM client not configured. Call UseLLMConfig first")
	}

	if llmModel == "" {
		llmModel = "openai/gpt-5-nano"
	}

	if ch.userModeration != nil {
		if isBanned, banMessage := ch.userModeration.CheckBanStatus(userID); isBanned {
			return banMessage, nil
		}
	}

	coreSession, err := ch.getOrCreateCoreSession(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get or create core session: %w", err)
	}

	base64Image := base64.StdEncoding.EncodeToString(imageData)
	dataURL := fmt.Sprintf("data:%s;base64,%s", imageMimeType, base64Image)

	userMsg := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser,
		MultiContent: []openai.ChatMessagePart{
			{
				Type: openai.ChatMessagePartTypeText,
				Text: userMessage,
			},
			{
				Type: openai.ChatMessagePartTypeImageURL,
				ImageURL: &openai.ChatMessageImageURL{
					URL:    dataURL,
					Detail: openai.ImageURLDetailAuto,
				},
			},
		},
	}

	historyContent := userMessage
	if historyContent == "" {
		historyContent = "(User sent an image)"
	} else {
		historyContent = fmt.Sprintf("(User sent an image) %s", userMessage)
	}
	coreSession.Msgs = append(
		coreSession.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: historyContent,
		},
	)

	coreSession.Model = llmModel

	imageMsgID, imageSeqID := coreSession.GenerateMessageIDWithSeq()
	userMsgRecord := model.NewUserMessage(imageMsgID, imageSeqID, userID, coreSession.SessionID, historyContent, model.ContentTypeImage)
	ch.saveMessage(userMsgRecord)

	systemPrompts, err := ch.buildSystemPrompts(userID)
	if err != nil {
		return "", fmt.Errorf("failed to build system prompts: %w", err)
	}

	messages := []openai.ChatCompletionMessage{}
	for _, prompt := range systemPrompts {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: prompt,
		})
	}

	historyMsgs := coreSession.Msgs
	if len(historyMsgs) > 1 {
		messages = append(messages, historyMsgs[:len(historyMsgs)-1]...)
	}

	messages = append(messages, userMsg)

	ctx = model.WithUserID(ctx, userID)

	log.Log.Infof("[CoreHandler] 🔵 VISION LLM >> Model: %s | Messages: %d | Image included", llmModel, len(messages))

	request := openai.ChatCompletionRequest{
		Model:    llmModel,
		Messages: messages,
	}

	resp, err := llmClient.CreateChatCompletion(ctx, request)
	if err != nil {
		return "", fmt.Errorf("vision LLM call failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from vision LLM")
	}

	response := resp.Choices[0].Message.Content

	if resp.Usage.TotalTokens > 0 {
		log.Log.Infof("[CoreHandler] 📊 VISION TOKEN USAGE >> Model: %s | prompt=%d | completion=%d | total=%d",
			llmModel, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
	}

	coreSession.Msgs = append(
		coreSession.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: response,
		},
	)
	coreSession.UpdatedAt = time.Now()

	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}

	assistantMsgID, assistantSeqID := coreSession.GenerateMessageIDWithSeq()
	assistantMsg := model.NewMessage(
		assistantMsgID,
		assistantSeqID,
		userID,
		coreSession.SessionID,
		openai.ChatMessageRoleAssistant,
		response,
		model.AgentTypeCore,
		model.ContentTypeImage,
		request,
		resp,
		resp.Choices[0],
	)
	ch.saveMessage(assistantMsg)

	log.Log.Infof("[CoreHandler] ✅ Image message processed | UserID: %s | Response length: %d chars | Model: %s", userID, len(response), llmModel)

	return response, nil
}

// HasVisionLLM returns true if a Vision LLM is configured.
func (ch *CoreHandler) HasVisionLLM() bool {
	return ch.visionLLMClient != nil && ch.visionLLMConfig != nil
}
