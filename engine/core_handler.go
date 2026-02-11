package engine

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

	"github.com/ghiac/agentize/llmutils"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

//go:embed core_controller.md
var coreControllerPrompt string

// CoreHandlerConfig holds configuration for the CoreHandler
type CoreHandlerConfig struct {
	// LLM configuration for the Core's decision-making
	CoreLLMConfig LLMConfig

	// Model configurations for UserAgents
	UserAgentHighModel string // e.g., "openai/gpt-5-nano" or "gpt-4o"
	UserAgentLowModel  string // e.g., "openai/gpt-5-nano"

	// CoreModel is the model used for Core's orchestration decisions
	CoreModel string // e.g., "openai/gpt-5-nano"

	// Session configuration
	AutoSummarizeThreshold int // Default: 20 messages

	// WebSearchDisabled disables web_search and web_search_deepresearch tools
	WebSearchDisabled bool
}

// DefaultCoreHandlerConfig returns default configuration
func DefaultCoreHandlerConfig() CoreHandlerConfig {
	return CoreHandlerConfig{
		UserAgentHighModel:     "openai/gpt-5-nano",
		UserAgentLowModel:      "openai/gpt-5-nano",
		CoreModel:              "openai/gpt-5-nano",
		AutoSummarizeThreshold: 5,
		WebSearchDisabled:      true, // Web search disabled by default
	}
}

// CoreHandler is the main orchestrator that manages user conversations
// and delegates tasks to specialized UserAgents
type CoreHandler struct {
	// Session management for all agent types
	sessionHandler *model.SessionHandler

	// UserAgents (Engine instances)
	userAgentHigh *Engine
	userAgentLow  *Engine

	// LLM client for Core's orchestration decisions
	llmClient *openai.Client
	llmConfig LLMConfig

	// Vision LLM client (separate from main LLM for cost optimization on image processing)
	visionLLMClient *openai.Client
	visionLLMConfig *LLMConfig

	// Core's own sessions per user (for orchestration context)
	coreSessions   map[string]*model.Session
	coreSessionsMu sync.RWMutex

	// Per-user mutex for serializing message processing
	// Ensures only one message is processed at a time per user to prevent
	// race conditions on session creation and sequence number generation
	userMutexes   map[string]*sync.Mutex
	userMutexesMu sync.RWMutex

	// Per-user progress + queue: check before locking so we can return immediately
	// when already in progress and queue the message instead of blocking
	userProgress *ProgressGuard

	// Configuration
	config CoreHandlerConfig

	// Function registry for Core's tools
	coreTools *model.FunctionRegistry

	// User moderation helper
	userModeration *UserModeration

	// Backup LLM chain (initialized from LLMConfig.BackupProviders)
	backups *backupChain

	// Callback for billing/usage metering (optional, set by application)
	Callback Callback
}

// NewCoreHandler creates a new CoreHandler with the given UserAgents
func NewCoreHandler(
	sessionHandler *model.SessionHandler,
	userAgentHigh *Engine,
	userAgentLow *Engine,
	config CoreHandlerConfig,
) *CoreHandler {
	ch := &CoreHandler{
		sessionHandler: sessionHandler,
		userAgentHigh:  userAgentHigh,
		userAgentLow:   userAgentLow,
		config:         config,
		coreSessions:   make(map[string]*model.Session),
		userMutexes:    make(map[string]*sync.Mutex),
		userProgress:   NewProgressGuard(),
		coreTools:      model.NewFunctionRegistry(),
	}

	// Register Core's tools
	ch.registerCoreTools()

	return ch
}

// getUserMutex returns or creates a mutex for a specific user
// This ensures only one message is processed at a time per user
func (ch *CoreHandler) getUserMutex(userID string) *sync.Mutex {
	// First try with read lock (fast path for existing mutexes)
	ch.userMutexesMu.RLock()
	if mu, exists := ch.userMutexes[userID]; exists {
		ch.userMutexesMu.RUnlock()
		return mu
	}
	ch.userMutexesMu.RUnlock()

	// Need to create new mutex, acquire write lock
	ch.userMutexesMu.Lock()
	defer ch.userMutexesMu.Unlock()

	// Double-check after acquiring write lock
	if mu, exists := ch.userMutexes[userID]; exists {
		return mu
	}

	mu := &sync.Mutex{}
	ch.userMutexes[userID] = mu
	return mu
}

// SetCallback sets the billing/usage callback on the CoreHandler and propagates it to child engines.
func (ch *CoreHandler) SetCallback(cb Callback) {
	ch.Callback = cb
	if ch.userAgentHigh != nil {
		ch.userAgentHigh.Callback = cb
	}
	if ch.userAgentLow != nil {
		ch.userAgentLow.Callback = cb
	}
}

// UseLLMConfig configures the LLM client for the Core's orchestration
func (ch *CoreHandler) UseLLMConfig(config LLMConfig) error {
	openaiConfig := openai.DefaultConfig(config.APIKey)
	if config.BaseURL != "" {
		openaiConfig.BaseURL = config.BaseURL
	}
	if config.HTTPClient != nil {
		openaiConfig.HTTPClient = config.HTTPClient
	}

	ch.llmClient = openai.NewClientWithConfig(openaiConfig)
	ch.llmConfig = config

	// Initialize backup chain from configured providers (nil if disabled or empty)
	if config.BackupDisabled {
		ch.backups = nil
	} else {
		ch.backups = newBackupChain(config.BackupProviders)
	}

	// Initialize user moderation helper
	ch.userModeration = NewUserModeration(
		IsNonsenseMessageFast,
		func(ctx context.Context, msg string) (bool, error) {
			return llmutils.IsNonsenseMessageLLM(ctx, ch.llmClient, ch.llmConfig.Model, msg)
		},
		ch.getOrCreateUser,
		ch.saveUser,
	)

	return nil
}

// callLLM tries the backup LLM providers in order (if configured), then falls back
// to the default OpenAI client. This is the single entry point for all LLM calls
// in the CoreHandler, ensuring consistent fallback behaviour.
func (ch *CoreHandler) callLLM(ctx context.Context, model string, messages []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionResponse, error) {
	// Try backup providers chain first
	if resp, ok := ch.backups.tryBackup(ctx, messages, tools, "CoreHandler"); ok {
		return resp, nil
	}

	// Default: OpenAI client
	systemPromptLen := 0
	for _, m := range messages {
		if m.Role == openai.ChatMessageRoleSystem {
			systemPromptLen += len(m.Content)
		}
	}
	log.Log.Infof("[CoreHandler] 🔵 DEFAULT LLM >> Using OpenAI | Model: %s | Messages: %d | Tools: %d | system_prompt_len=%d", model, len(messages), len(tools), systemPromptLen)
	request := openai.ChatCompletionRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
	}
	resp, err := ch.llmClient.CreateChatCompletion(ctx, request)
	if err == nil && resp.Usage.TotalTokens > 0 {
		cacheTokens := 0
		if resp.Usage.PromptTokensDetails != nil {
			cacheTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		log.Log.Infof("[CoreHandler] 📊 TOKEN USAGE >> Model: %s | prompt=%d | completion=%d | total=%d | cache=%d (ورودی=prompt، خروجی=completion، مجموع=total، کش‌پرامپت=cache)",
			model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, cacheTokens)
	}
	return resp, err
}

// SetHTTPClient sets a custom HTTP client (e.g., for proxy support)
func (ch *CoreHandler) SetHTTPClient(client *http.Client) {
	if ch.llmConfig.HTTPClient == nil {
		ch.llmConfig.HTTPClient = client
	}
}

// ProcessMessage is the main entry point for user messages.
// It checks in-progress (without locking) and queues if busy; otherwise holds
// per-user mutex and processes, then drains the queue.
func (ch *CoreHandler) ProcessMessage(
	ctx context.Context,
	userID string,
	userMessage string,
) (string, error) {
	if ch.userProgress.TryQueue(userID, userMessage) {
		return "⏳ در حال پردازش درخواست قبلی هستم... لطفا صبر کنید. 📋 پیام شما صف شد و به‌ترتیب پاسخ داده می‌شود.", nil
	}
	userMu := ch.getUserMutex(userID)
	userMu.Lock()
	defer userMu.Unlock()
	ch.userProgress.SetInProgress(userID, true)
	defer ch.userProgress.SetInProgress(userID, false)

	response, err := ch.processOneMessageCore(ctx, userID, userMessage)
	if err != nil {
		return "", err
	}
	// Queued messages are merged inside processWithTools after the first tool response (one combined answer)
	return response, nil
}

// processOneMessageCore does one full Core message flow (no mutex; caller must hold user mutex and set progress).
func (ch *CoreHandler) processOneMessageCore(
	ctx context.Context,
	userID string,
	userMessage string,
) (string, error) {
	notifyStatus(ctx, userID, "", StatusReceived, "")

	userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
	ch.coreSessionsMu.RLock()
	totalCoreSessions := len(ch.coreSessions)
	ch.coreSessionsMu.RUnlock()

	log.Log.Infof("[CoreHandler] 🚀 Processing new message | UserID: %s | Message length: %d chars | User sessions: %d | Total Core sessions: %d",
		userID, len(userMessage), len(userSessions), totalCoreSessions)

	if !ch.userAgentHigh.IsDBReady() || !ch.userAgentLow.IsDBReady() {
		return "", fmt.Errorf("database is not ready. Call Init() on UserAgents first to ensure database is fully loaded")
	}
	if ch.llmClient == nil {
		return "", fmt.Errorf("LLM client not configured. Call UseLLMConfig first")
	}

	notifyStatus(ctx, userID, "", StatusAnalyzing, "")

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
	userMsg := model.NewUserMessage(userMsgID, userSeqID, userID, coreSession.SessionID, userMessage, model.ContentTypeText)
	userMsg.IsNonsense = isNonsense
	ch.saveMessage(userMsg)
	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}

	messages := ch.buildMessages(systemPrompts, coreSession.Msgs)
	tools := ch.getCoreToolsForLLM()
	ctx = model.WithUserID(ctx, userID)
	notifyStatus(ctx, userID, coreSession.SessionID, StatusRouting, "")

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
	notifyStatus(ctx, userID, coreSession.SessionID, StatusCompleted, "")
	return response, nil
}

// getOrCreateCoreSession gets or creates a Core session for a user
// It uses SessionHandler to ensure persistence in the database
// NOTE: This uses the same pattern as getOrCreateActiveSession - first checks User's ActiveSessionID
func (ch *CoreHandler) getOrCreateCoreSession(userID string) (*model.Session, error) {
	// First check in-memory cache
	ch.coreSessionsMu.RLock()
	session, exists := ch.coreSessions[userID]
	ch.coreSessionsMu.RUnlock()

	if exists {
		// Verify session still exists in database and refresh if needed
		dbSession, err := ch.sessionHandler.GetSession(session.SessionID)
		if err == nil && dbSession != nil {
			// Update cache with fresh data from database
			ch.coreSessionsMu.Lock()
			ch.coreSessions[userID] = dbSession
			ch.coreSessionsMu.Unlock()

			log.Log.Infof("[CoreHandler] 🔄 Using cached Core session | UserID: %s | SessionID: %s",
				userID, dbSession.SessionID)
			return dbSession, nil
		}
		// Session not found in DB, will create new one below
	}

	ch.coreSessionsMu.Lock()
	defer ch.coreSessionsMu.Unlock()

	// Double-check after acquiring write lock
	if session, exists = ch.coreSessions[userID]; exists {
		dbSession, err := ch.sessionHandler.GetSession(session.SessionID)
		if err == nil && dbSession != nil {
			ch.coreSessions[userID] = dbSession
			log.Log.Infof("[CoreHandler] 🔄 Using cached Core session (after lock) | UserID: %s | SessionID: %s",
				userID, dbSession.SessionID)
			return dbSession, nil
		}
	}

	// Check User's ActiveSessionID for Core type first
	activeSessionID := ch.getActiveSessionID(userID, model.AgentTypeCore)
	if activeSessionID != "" {
		activeSession, err := ch.sessionHandler.GetSession(activeSessionID)
		if err == nil && activeSession != nil {
			ch.coreSessions[userID] = activeSession
			log.Log.Infof("[CoreHandler] 🔄 Using active Core session from User | UserID: %s | SessionID: %s",
				userID, activeSession.SessionID)
			return activeSession, nil
		}
		// Active session reference is stale, will create new below
		log.Log.Warnf("[CoreHandler] ⚠️  Active Core session no longer exists | UserID: %s | OldSessionID: %s",
			userID, activeSessionID)
	}

	// Fallback: Try to get existing Core session from database (for migration from old data)
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		GetCoreSession(string) (*model.Session, error)
	}); ok {
		existingCore, err := sqliteStore.GetCoreSession(userID)
		if err == nil && existingCore != nil {
			ch.coreSessions[userID] = existingCore
			// Also set as active session for future lookups
			_ = ch.setActiveSessionID(userID, model.AgentTypeCore, existingCore.SessionID)
			log.Log.Infof("[CoreHandler] 🔄 Loaded Core session from database (migration) | UserID: %s | SessionID: %s",
				userID, existingCore.SessionID)
			return existingCore, nil
		}
	} else {
		// Fallback: search through all sessions for Core type
		allSessions, err := ch.sessionHandler.ListUserSessions(userID)
		if err == nil {
			for _, s := range allSessions {
				if s.AgentType == model.AgentTypeCore {
					ch.coreSessions[userID] = s
					// Also set as active session for future lookups
					_ = ch.setActiveSessionID(userID, model.AgentTypeCore, s.SessionID)
					log.Log.Infof("[CoreHandler] 🔄 Found Core session from list (migration) | UserID: %s | SessionID: %s",
						userID, s.SessionID)
					return s, nil
				}
			}
		}
	}

	// Create new Core session with proper sequential ID
	// NOTE: createSessionForUser already sets the new session as ActiveSession
	session, err := ch.createSessionForUser(userID, model.AgentTypeCore)
	if err != nil {
		return nil, fmt.Errorf("failed to create core session: %w", err)
	}

	ch.coreSessions[userID] = session

	log.Log.Infof("[CoreHandler] ✨ Created new Core session | UserID: %s | SessionID: %s", userID, session.SessionID)

	return session, nil
}

// saveCoreSession saves the Core session to the database
func (ch *CoreHandler) saveCoreSession(session *model.Session) error {
	// Update in-memory cache
	ch.coreSessionsMu.Lock()
	ch.coreSessions[session.UserID] = session
	ch.coreSessionsMu.Unlock()

	// Save to database through SessionHandler
	store := ch.sessionHandler.GetStore()
	if err := store.Put(session); err != nil {
		return fmt.Errorf("failed to save core session: %w", err)
	}

	return nil
}

// buildSystemPrompts builds the array of system prompts for the Core
func (ch *CoreHandler) buildSystemPrompts(userID string) ([]string, error) {
	prompts := []string{}

	// 1. Core Controller base prompt
	prompts = append(prompts, coreControllerPrompt)

	// 2. UserAgent registered tools prompt — tells Core exactly what tools are available
	toolsPrompt := ch.buildUserAgentToolsPrompt()
	if toolsPrompt != "" {
		prompts = append(prompts, toolsPrompt)
	}

	// 3. Session context - Summary and tags from previous conversations (if summarized)
	// This provides context from archived messages that are no longer in the active conversation
	ch.coreSessionsMu.RLock()
	coreSession := ch.coreSessions[userID]
	ch.coreSessionsMu.RUnlock()
	if coreSession != nil {
		sessionContext := ch.buildCoreSessionContext(coreSession)
		if sessionContext != "" {
			prompts = append(prompts, sessionContext)
		}
	}

	// 4. Active sessions prompt (shows current active session for each agent type)
	activePrompt := ch.buildActiveSessionsPrompt(userID)
	if activePrompt != "" {
		prompts = append(prompts, activePrompt)
	}

	// 5. Sessions list prompt (for change_session)
	sessionsPrompt, err := ch.sessionHandler.GetSessionsPrompt(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sessions prompt: %w", err)
	}
	prompts = append(prompts, sessionsPrompt)

	return prompts, nil
}

// buildUserAgentToolsPrompt generates a system prompt listing all tools registered
// in the UserAgent engines. This helps the Core LLM understand what capabilities
// the agents have so it can route requests more accurately.
func (ch *CoreHandler) buildUserAgentToolsPrompt() string {
	// Collect unique tool names from both engines
	toolSet := make(map[string]bool)
	if ch.userAgentHigh != nil && ch.userAgentHigh.Functions != nil {
		for _, name := range ch.userAgentHigh.Functions.GetAllRegistered() {
			toolSet[name] = true
		}
	}
	if ch.userAgentLow != nil && ch.userAgentLow.Functions != nil {
		for _, name := range ch.userAgentLow.Functions.GetAllRegistered() {
			toolSet[name] = true
		}
	}

	if len(toolSet) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Registered UserAgent Tools\n\n")
	sb.WriteString("The following tools are currently registered and available to UserAgents.\n")
	sb.WriteString("When a user's request requires any of these tools, delegate to the appropriate agent.\n\n")
	for name := range toolSet {
		sb.WriteString(fmt.Sprintf("- `%s`\n", name))
	}
	return sb.String()
}

// buildCoreSessionContext builds session context with summary and tags for the Core
// This is used to provide context from archived/summarized messages
// Note: ExMsgs is only for debug purposes and is NOT included in the LLM context
func (ch *CoreHandler) buildCoreSessionContext(session *model.Session) string {
	// Only include context if session has been summarized (has summary or tags)
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

// buildActiveSessionsPrompt generates a prompt showing current active sessions
func (ch *CoreHandler) buildActiveSessionsPrompt(userID string) string {
	user, err := ch.getOrCreateUser(userID)
	if err != nil || user == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("## Current Active Sessions\n\n")
	sb.WriteString("These are the currently active sessions. Messages are automatically sent to these sessions.\n\n")

	hasActiveSessions := false

	// Check each agent type
	agentTypes := []struct {
		agentType model.AgentType
		name      string
	}{
		{model.AgentTypeHigh, "UserAgent-High"},
		{model.AgentTypeLow, "UserAgent-Low"},
	}

	for _, at := range agentTypes {
		sessionID := user.GetActiveSessionID(at.agentType)
		if sessionID == "" {
			sb.WriteString(fmt.Sprintf("- **%s**: No active session (will be created automatically on first message)\n", at.name))
			continue
		}

		// Get session details
		session, err := ch.sessionHandler.GetSession(sessionID)
		if err != nil || session == nil {
			sb.WriteString(fmt.Sprintf("- **%s**: No active session (previous session was deleted)\n", at.name))
			continue
		}

		hasActiveSessions = true
		title := session.Title
		if title == "" {
			title = "Untitled"
		}
		msgCount := len(session.Msgs)
		sb.WriteString(fmt.Sprintf("- **%s**: [%s] \"%s\" (%d messages)\n", at.name, sessionID, title, msgCount))
	}

	if !hasActiveSessions {
		return ""
	}

	sb.WriteString("\nUse `create_session` to start a new topic, or `change_session` to switch to a different session.\n")

	return sb.String()
}

// buildMessages builds the message array for the LLM call
// Note: Only uses Summary, Tags, and Msgs from sessions. ExMsgs is only for debug purposes and is not used here.
func (ch *CoreHandler) buildMessages(systemPrompts []string, conversationMsgs []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	messages := []openai.ChatCompletionMessage{}

	// Add system prompts
	for _, prompt := range systemPrompts {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: prompt,
		})
	}

	// Add conversation history
	messages = append(messages, conversationMsgs...)

	return messages
}

// getCoreToolsForLLM returns the tools in OpenAI format
func (ch *CoreHandler) getCoreToolsForLLM() []openai.Tool {
	tools := []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "call_user_agent_high",
				Description: "Send a message to the high-intelligence UserAgent for complex tasks. Session is managed automatically.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{
							"type":        "string",
							"description": "The message to send to the UserAgent",
						},
					},
					"required": []string{"message"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "call_user_agent_low",
				Description: "Send a message to the cost-effective UserAgent for simple tasks. Session is managed automatically.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"message": map[string]interface{}{
							"type":        "string",
							"description": "The message to send to the UserAgent",
						},
					},
					"required": []string{"message"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "create_session",
				Description: "Create a new session for a UserAgent and make it the active session. Use when starting a new topic or conversation.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_type": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"high", "low"},
							"description": "The type of UserAgent to create the session for",
						},
						"title": map[string]interface{}{
							"type":        "string",
							"description": "Optional title for the session",
						},
					},
					"required": []string{"agent_type"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "change_session",
				Description: "Switch to a different existing session. Use when user wants to continue a previous conversation.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"agent_type": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"high", "low"},
							"description": "The type of UserAgent",
						},
						"session_id": map[string]interface{}{
							"type":        "string",
							"description": "The session ID to switch to (from list_sessions)",
						},
					},
					"required": []string{"agent_type", "session_id"},
				},
			},
		},
		{
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
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "ban_user",
				Description: "Ban the current user for a specified duration. Use this when a user repeatedly sends nonsense messages or violates rules. Duration is in hours (0 means permanent ban). Note: Once banned, the user's messages will not be processed, so this action should be used carefully.",
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
	}

	// update_status tool: let Core LLM send contextual status updates
	tools = append(tools, openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: "update_status",
			// TODO write shorter Description
			Description: "Send a real-time status/progress update to the user. " +
				"Use before long operations to inform the user what you are doing. " +
				"You can also send partial results or intermediate findings. " +
				"The message is shown as a temporary status that gets replaced by the final response.",
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
	})

	// Add web search tools only if not disabled
	if !ch.config.WebSearchDisabled {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "web_search",
				Description: "Search the web for up-to-date information. Use this when you need current information, recent news, real-time data, or information that may have changed recently. The search will return results with citations to sources. Input: query (string, required) - The search query to find information on the web.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query to find information on the web",
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
				Description: "Same as web_search but uses Tongyi DeepResearch model (alibaba/tongyi-deepresearch-30b-a3b). Use for testing or when you want deep-research style search results. Input: query (string, required) - The search query to find information on the web.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"query": map[string]interface{}{
							"type":        "string",
							"description": "The search query to find information on the web",
						},
					},
					"required": []string{"query"},
				},
			},
		})
	}

	return tools
}

// processWithTools handles the LLM call and tool execution loop
func (ch *CoreHandler) processWithTools(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
	userID string,
	coreSession *model.Session,
) (string, error) {
	maxIterations := 10
	currentMessages := messages

	// Update Core session model if different from stored model (only once before the loop)
	modelName := ch.llmConfig.Model
	if modelName == "" {
		modelName = "openai/gpt-5-nano"
	}
	if coreSession != nil && coreSession.Model != modelName {
		coreSession.Model = modelName
		// Save session to persist model name
		if err := ch.saveCoreSession(coreSession); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to update Core session model | SessionID: %s | Error: %v", coreSession.SessionID, err)
		}
	}

	// Ensure user_id is in context
	if _, ok := model.GetUserIDFromContext(ctx); !ok && userID != "" {
		ctx = model.WithUserID(ctx, userID)
	}

	sessionID := ""
	if coreSession != nil {
		sessionID = coreSession.SessionID
	}

	for i := 0; i < maxIterations; i++ {
		// Notify status: thinking (model name only in metadata, not exposed to user)
		notifyStatus(ctx, userID, sessionID, StatusThinking, "")

		// Make LLM call (tries backup provider first, then falls back to OpenAI)
		llmStart := time.Now()
		resp, err := ch.callLLM(ctx, modelName, currentMessages, tools)
		llmDuration := time.Since(llmStart)
		if err != nil {
			return "", formatLLMError(err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response from LLM")
		}

		choice := resp.Choices[0]

		// Callback: record LLM usage
		if ch.Callback != nil {
			ch.Callback.AfterAction(ctx, &UsageEvent{
				UserID:    userID,
				SessionID: sessionID,
				EventType: EventLLMCall,
				Name:      modelName,
				Tokens:    resp.Usage.TotalTokens,
				Duration:  llmDuration,
			})
		}

		// Save message to database
		request := openai.ChatCompletionRequest{
			Model:    modelName,
			Messages: currentMessages,
			Tools:    tools,
		}
		messageID := ch.saveCoreMessage(userID, request, resp, choice)

		// If no tool calls: merge any queued messages and do one more LLM turn so all get one combined answer
		if len(choice.Message.ToolCalls) == 0 {
			queued := ch.userProgress.DrainQueue(userID)
			if len(queued) > 0 && coreSession != nil {
				for _, text := range queued {
					currentMessages = append(currentMessages, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: text,
					})
					coreSession.Msgs = append(coreSession.Msgs, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: text,
					})
					userMsgID, userSeqID := coreSession.GenerateMessageIDWithSeq()
					userMsg := model.NewUserMessage(userMsgID, userSeqID, userID, coreSession.SessionID, text, model.ContentTypeText)
					ch.saveMessage(userMsg)
				}
				_ = ch.saveCoreSession(coreSession)
				// Continue loop to make one more LLM call with merged messages (no tools for this turn)
				currentMessages = append(currentMessages, choice.Message)
				continue
			}
			return choice.Message.Content, nil
		}

		// Add assistant message with tool calls
		currentMessages = append(currentMessages, choice.Message)

		// Execute each tool call; after the first tool response, merge all queued user messages so they get one combined answer
		queuedMerged := false
		for _, toolCall := range choice.Message.ToolCalls {
			// Save tool call to database (before execution); toolID is used for update later
			var toolID string
			if coreSession != nil {
				toolID = ch.saveToolCall(coreSession, messageID, toolCall)
			}

			// Notify status: tool executing
			notifyStatus(ctx, userID, sessionID, StatusToolExecuting, toolCall.Function.Name)

			// Callback: check limit before tool execution
			if ch.Callback != nil {
				if cbErr := ch.Callback.BeforeAction(ctx, &UsageEvent{
					UserID:    userID,
					SessionID: sessionID,
					EventType: EventToolCall,
					Name:      toolCall.Function.Name,
				}); cbErr != nil {
					result := fmt.Sprintf("Tool %s blocked: %v", toolCall.Function.Name, cbErr)
					if coreSession != nil {
						ch.updateToolCallResponse(toolID, result)
					}
					currentMessages = append(currentMessages, openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    result,
						ToolCallID: toolCall.ID,
					})
					continue
				}
			}

			toolStart := time.Now()
			result, err := ch.executeCoreTool(ctx, userID, toolCall)
			toolDuration := time.Since(toolStart)
			if err != nil {
				result = fmt.Sprintf("Error executing tool: %v", err)
			}

			// Callback: record tool usage
			if ch.Callback != nil {
				e := &UsageEvent{
					UserID:    userID,
					SessionID: sessionID,
					EventType: EventToolCall,
					Name:      toolCall.Function.Name,
					Duration:  toolDuration,
					Error:     err,
				}
				ch.Callback.AfterAction(ctx, e)
			}

			// Notify status: tool done
			notifyStatus(ctx, userID, sessionID, StatusToolDone, toolCall.Function.Name)

			// Update tool call response in database
			if coreSession != nil {
				ch.updateToolCallResponse(toolID, result)
			}

			// Add tool result
			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    result,
				ToolCallID: toolCall.ID,
			})

			// After first tool response: merge all queued user messages into this turn so they get one combined answer
			if !queuedMerged {
				queuedMerged = true
				for _, text := range ch.userProgress.DrainQueue(userID) {
					currentMessages = append(currentMessages, openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: text,
					})
					if coreSession != nil {
						coreSession.Msgs = append(coreSession.Msgs, openai.ChatCompletionMessage{
							Role:    openai.ChatMessageRoleUser,
							Content: text,
						})
						userMsgID, userSeqID := coreSession.GenerateMessageIDWithSeq()
						userMsg := model.NewUserMessage(userMsgID, userSeqID, userID, coreSession.SessionID, text, model.ContentTypeText)
						ch.saveMessage(userMsg)
					}
				}
				if coreSession != nil {
					_ = ch.saveCoreSession(coreSession)
				}
			}
		}
	}

	return "", fmt.Errorf("max iterations reached without final response")
}

// executeCoreTool executes a Core tool and returns the result
func (ch *CoreHandler) executeCoreTool(
	ctx context.Context,
	userID string,
	toolCall openai.ToolCall,
) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("failed to parse tool arguments: %w", err)
	}

	switch toolCall.Function.Name {
	case "call_user_agent_high":
		notifyStatus(ctx, userID, "", StatusAgentCalling, "high")
		if ch.Callback != nil {
			if cbErr := ch.Callback.BeforeAction(ctx, &UsageEvent{
				UserID: userID, EventType: EventAgentRouting, Name: "high",
			}); cbErr != nil {
				return fmt.Sprintf("Agent high blocked: %v", cbErr), nil
			}
		}
		result, err := ch.callUserAgent(ctx, userID, args, ch.userAgentHigh, model.AgentTypeHigh)
		if ch.Callback != nil {
			ch.Callback.AfterAction(ctx, &UsageEvent{
				UserID: userID, EventType: EventAgentRouting, Name: "high", Error: err,
			})
		}
		notifyStatus(ctx, userID, "", StatusAgentDone, "high")
		return result, err

	case "call_user_agent_low":
		notifyStatus(ctx, userID, "", StatusAgentCalling, "low")
		if ch.Callback != nil {
			if cbErr := ch.Callback.BeforeAction(ctx, &UsageEvent{
				UserID: userID, EventType: EventAgentRouting, Name: "low",
			}); cbErr != nil {
				return fmt.Sprintf("Agent low blocked: %v", cbErr), nil
			}
		}
		result, err := ch.callUserAgent(ctx, userID, args, ch.userAgentLow, model.AgentTypeLow)
		if err != nil {
			return "", err
		}
		// Check for escalation
		if strings.HasPrefix(strings.TrimSpace(result), "ESCALATE:") {
			if ch.Callback != nil {
				ch.Callback.AfterAction(ctx, &UsageEvent{
					UserID: userID, EventType: EventAgentRouting, Name: "low",
				})
			}
			notifyStatus(ctx, userID, "", StatusAgentCalling, "high (escalated)")
			// Auto-escalate to high model
			result, err = ch.callUserAgent(ctx, userID, args, ch.userAgentHigh, model.AgentTypeHigh)
			if ch.Callback != nil {
				ch.Callback.AfterAction(ctx, &UsageEvent{
					UserID: userID, EventType: EventAgentRouting, Name: "high", Error: err,
				})
			}
			notifyStatus(ctx, userID, "", StatusAgentDone, "high")
			return result, err
		}
		if ch.Callback != nil {
			ch.Callback.AfterAction(ctx, &UsageEvent{
				UserID: userID, EventType: EventAgentRouting, Name: "low",
			})
		}
		notifyStatus(ctx, userID, "", StatusAgentDone, "low")
		return result, nil

	case "update_status":
		message, _ := args["message"].(string)
		if message != "" {
			notifyStatus(ctx, userID, "", StatusCustom, message)
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
		return ch.webSearchWithModelTool(ctx, userID, args, SearchModelTongyiDeepResearch)

	default:
		return "", fmt.Errorf("unknown tool: %s", toolCall.Function.Name)
	}
}

// callUserAgent sends a message to a UserAgent
// Session is automatically managed - uses active session or creates new one
func (ch *CoreHandler) callUserAgent(
	ctx context.Context,
	userID string,
	args map[string]interface{},
	agent *Engine,
	agentType model.AgentType,
) (string, error) {
	message, ok := args["message"].(string)
	if !ok || message == "" {
		return "", fmt.Errorf("message is required")
	}

	// Get or create active session for this agent type
	sessionID, err := ch.getOrCreateActiveSession(userID, agentType)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Failed to get/create active session | UserID: %s | AgentType: %s | Error: %v",
			userID, agentType, err)
		return "", fmt.Errorf("failed to get active session: %w", err)
	}

	log.Log.Infof("[CoreHandler] 🎯 Using active session | SessionID: %s | AgentType: %s | UserID: %s | Message length: %d chars",
		sessionID, agentType, userID, len(message))

	// Process message through the UserAgent
	response, _, err := agent.ProcessMessage(ctx, sessionID, message)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ UserAgent processing failed | SessionID: %s | Error: %v", sessionID, err)
		return "", fmt.Errorf("UserAgent error: %w", err)
	}

	log.Log.Infof("[CoreHandler] ✅ UserAgent response received | SessionID: %s | Response length: %d chars",
		sessionID, len(response))

	return response, nil
}

// getSessionTitleForLog returns session title or "Untitled"
func getSessionTitleForLog(s *model.Session) string {
	if s.Title != "" {
		return s.Title
	}
	return "Untitled"
}

// createSessionTool creates a new session
func (ch *CoreHandler) createSessionTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	agentTypeStr, ok := args["agent_type"].(string)
	if !ok || agentTypeStr == "" {
		return "", fmt.Errorf("agent_type is required")
	}

	var agentType model.AgentType
	switch agentTypeStr {
	case "high":
		agentType = model.AgentTypeHigh
	case "low":
		agentType = model.AgentTypeLow
	default:
		return "", fmt.Errorf("invalid agent_type: %s", agentTypeStr)
	}

	log.Log.Infof("[CoreHandler] 🛠️  createSessionTool called | UserID: %s | AgentType: %s", userID, agentType)

	session, err := ch.createSessionForUser(userID, agentType)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Failed to create session | UserID: %s | AgentType: %s | Error: %v", userID, agentType, err)
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	// Set title if provided
	if title, ok := args["title"].(string); ok && title != "" {
		session.Title = title
		ch.sessionHandler.UpdateSessionMetadata(session.SessionID, title, nil, "")
		log.Log.Infof("[CoreHandler] 📝 Set session title | SessionID: %s | Title: %s", session.SessionID, title)
	}

	// Set as active session automatically
	if err := ch.setActiveSessionID(userID, agentType, session.SessionID); err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to set active session | UserID: %s | AgentType: %s | Error: %v", userID, agentType, err)
	}

	log.Log.Infof("[CoreHandler] ✅ Session created and set as active | SessionID: %s | AgentType: %s", session.SessionID, agentType)

	return fmt.Sprintf("Created new session and set as active (type: %s)", agentType), nil
}

// changeSessionTool switches to an existing session
func (ch *CoreHandler) changeSessionTool(_ context.Context, userID string, args map[string]interface{}) (string, error) {
	agentTypeStr, ok := args["agent_type"].(string)
	if !ok || agentTypeStr == "" {
		return "", fmt.Errorf("agent_type is required")
	}

	sessionID, ok := args["session_id"].(string)
	if !ok || sessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}

	var agentType model.AgentType
	switch agentTypeStr {
	case "high":
		agentType = model.AgentTypeHigh
	case "low":
		agentType = model.AgentTypeLow
	default:
		return "", fmt.Errorf("invalid agent_type: %s", agentTypeStr)
	}

	log.Log.Infof("[CoreHandler] 🛠️  changeSessionTool called | UserID: %s | AgentType: %s | SessionID: %s", userID, agentType, sessionID)

	// Verify session exists and belongs to the correct agent type
	session, err := ch.sessionHandler.GetSession(sessionID)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Session not found | SessionID: %s | Error: %v", sessionID, err)
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	if session.AgentType != agentType {
		return "", fmt.Errorf("session %s is not a %s session (it's a %s session)", sessionID, agentType, session.AgentType)
	}

	// Set as active session
	if err := ch.setActiveSessionID(userID, agentType, sessionID); err != nil {
		return "", fmt.Errorf("failed to set active session: %w", err)
	}

	title := session.Title
	if title == "" {
		title = "Untitled"
	}

	log.Log.Infof("[CoreHandler] ✅ Session changed | UserID: %s | AgentType: %s | SessionID: %s | Title: %s", userID, agentType, sessionID, title)

	return fmt.Sprintf("Switched to session: %s (%s)", title, agentType), nil
}

// listSessionsTool returns the sessions summary
func (ch *CoreHandler) listSessionsTool(userID string) (string, error) {
	log.Log.Infof("[CoreHandler] 🛠️  listSessionsTool called | UserID: %s", userID)
	sessions, err := ch.sessionHandler.ListUserSessions(userID)
	if err != nil {
		return "", err
	}
	log.Log.Infof("[CoreHandler] 📋 Returning %d sessions for user %s", len(sessions), userID)
	return ch.sessionHandler.GetSessionsPrompt(userID)
}

// registerCoreTools registers the Core's internal tools
func (ch *CoreHandler) registerCoreTools() {
	// These are used internally by processWithTools
	// The actual tool functions are handled in executeCoreTool
}

// GetSessionHandler returns the session handler for external access
func (ch *CoreHandler) GetSessionHandler() *model.SessionHandler {
	return ch.sessionHandler
}

// GetUserAgentHigh returns the high-intelligence UserAgent
func (ch *CoreHandler) GetUserAgentHigh() *Engine {
	return ch.userAgentHigh
}

// GetUserAgentLow returns the cost-effective UserAgent
func (ch *CoreHandler) GetUserAgentLow() *Engine {
	return ch.userAgentLow
}

// getOrCreateUser gets or creates a user from the store
func (ch *CoreHandler) getOrCreateUser(userID string) (*model.User, error) {
	store := ch.sessionHandler.GetStore()

	// Try to cast to SQLiteStore to access user methods
	if sqliteStore, ok := store.(interface {
		GetOrCreateUser(string) (*model.User, error)
	}); ok {
		return sqliteStore.GetOrCreateUser(userID)
	}

	// If store doesn't support user management, return nil
	return nil, nil
}

// saveUser saves a user to the store
func (ch *CoreHandler) saveUser(user *model.User) error {
	store := ch.sessionHandler.GetStore()

	// Try to cast to SQLiteStore to access user methods
	if sqliteStore, ok := store.(interface {
		PutUser(*model.User) error
	}); ok {
		return sqliteStore.PutUser(user)
	}

	return fmt.Errorf("store does not support user management")
}

// createSessionForUser creates a new session with proper sequential ID
// Format: {UserID}-{AgentType}-s{SeqCounter}
// This ensures unique, incrementing session IDs per user and agent type
func (ch *CoreHandler) createSessionForUser(userID string, agentType model.AgentType) (*model.Session, error) {
	// Get or create user to access/increment session sequence
	user, err := ch.getOrCreateUser(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	if user == nil {
		// Fallback to old behavior if user management not supported
		return ch.sessionHandler.CreateSession(userID, agentType)
	}

	// Create session with proper sequential ID
	session, err := ch.sessionHandler.CreateSessionForUser(user, agentType)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	// Save user with updated session sequence counter
	if err := ch.saveUser(user); err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to save user after session creation | UserID: %s | Error: %v", userID, err)
	}

	return session, nil
}

// getActiveSessionID returns the active session ID for a user and agent type
// Returns empty string if no active session exists
func (ch *CoreHandler) getActiveSessionID(userID string, agentType model.AgentType) string {
	user, err := ch.getOrCreateUser(userID)
	if err != nil || user == nil {
		return ""
	}
	return user.GetActiveSessionID(agentType)
}

// setActiveSessionID sets the active session ID for a user and agent type
// Persists to database via User model.
// IMPORTANT: Only sets active session if the session exists in the database.
func (ch *CoreHandler) setActiveSessionID(userID string, agentType model.AgentType, sessionID string) error {
	// Validate that the session exists in the database before setting it as active
	if sessionID != "" {
		session, err := ch.sessionHandler.GetSession(sessionID)
		if err != nil || session == nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Cannot set active session - session not found | UserID: %s | AgentType: %s | SessionID: %s",
				userID, agentType, sessionID)
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

// getOrCreateActiveSession gets active session or creates one if not exists
// Returns the session ID (either existing or newly created)
// NOTE: This is the primary entry point for getting the correct session for LLM calls
func (ch *CoreHandler) getOrCreateActiveSession(userID string, agentType model.AgentType) (string, error) {
	// Check if active session exists in user record
	sessionID := ch.getActiveSessionID(userID, agentType)
	if sessionID != "" {
		// Verify session still exists in database
		session, err := ch.sessionHandler.GetSession(sessionID)
		if err == nil && session != nil {
			log.Log.Infof("[CoreHandler] 🔄 Using existing active session | UserID: %s | AgentType: %s | SessionID: %s",
				userID, agentType, sessionID)
			return sessionID, nil
		}
		// Session was deleted, clear the reference and create new
		log.Log.Warnf("[CoreHandler] ⚠️  Active session no longer exists, creating new | UserID: %s | AgentType: %s | OldSessionID: %s",
			userID, agentType, sessionID)
	}

	// Create new session with proper sequential ID
	// NOTE: createSessionForUser already sets the new session as ActiveSession via CreateSessionForUser
	session, err := ch.createSessionForUser(userID, agentType)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	log.Log.Infof("[CoreHandler] ✨ Auto-created active session | UserID: %s | AgentType: %s | SessionID: %s",
		userID, agentType, session.SessionID)
	return session.SessionID, nil
}

// banUserTool bans the current user for a specified duration
// userID is passed directly from executeCoreTool (from the current conversation context)
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
			message = "شما به صورت دائمی محدود شده‌اید."
		} else {
			message = fmt.Sprintf("شما به مدت %.0f ساعت محدود شده‌اید.", durationHours)
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

// webSearchWithModelTool performs a web search; if searchModel is empty, uses the default.
func (ch *CoreHandler) webSearchWithModelTool(ctx context.Context, userID string, args map[string]interface{}, searchModel string) (string, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "", fmt.Errorf("query is required")
	}
	result, err := PerformWebSearchWithModel(ctx, ch.llmClient, ch.llmConfig, query, userID, searchModel)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Web search failed | UserID: %s | Query: %s | Error: %v", userID, query, err)
		return "", fmt.Errorf("web search failed: %w", err)
	}
	log.Log.Infof("[CoreHandler] ✅ Web search completed | UserID: %s | Query: %s | Result length: %d chars", userID, query, len(result))
	return result, nil
}

// saveCoreMessage saves a message from CoreHandler to the database
// Returns the messageID of the saved message
func (ch *CoreHandler) saveCoreMessage(
	userID string,
	request openai.ChatCompletionRequest,
	response openai.ChatCompletionResponse,
	choice openai.ChatCompletionChoice,
) string {
	// Get Core session to get sessionID
	coreSession, err := ch.getOrCreateCoreSession(userID)
	if err != nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Failed to get core session for message save | UserID: %s | Error: %v", userID, err)
		return ""
	}

	// Get message content
	content := choice.Message.Content
	if content == "" && len(choice.Message.ToolCalls) > 0 {
		content = fmt.Sprintf("[Tool Calls: %d]", len(choice.Message.ToolCalls))
	}

	// Create message record
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

// saveMessage saves a message to the database
func (ch *CoreHandler) saveMessage(msg *model.Message) {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		PutMessage(*model.Message) error
	}); ok {
		if err := sqliteStore.PutMessage(msg); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to save message | MessageID: %s | Error: %v", msg.MessageID, err)
		} else {
			log.Log.Infof("[CoreHandler] 💾 Message saved | MessageID: %s | Model: %s | Tokens: %d", msg.MessageID, msg.Model, msg.TotalTokens)
		}
	}
}

// saveToolCall saves a tool call to the database and returns the ToolID (for use in updateToolCallResponse).
func (ch *CoreHandler) saveToolCall(session *model.Session, messageID string, toolCall openai.ToolCall) string {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		PutToolCall(*model.ToolCall) error
	}); ok {
		now := time.Now()
		// Generate sequential ToolID for this session
		toolID := session.GenerateToolID()
		tc := &model.ToolCall{
			ToolID:       toolID,
			ToolCallID:   toolCall.ID,
			MessageID:    messageID,
			SessionID:    session.SessionID,
			UserID:       session.UserID,
			AgentType:    model.AgentTypeCore,
			FunctionName: toolCall.Function.Name,
			Arguments:    toolCall.Function.Arguments,
			Response:     "", // Will be updated after execution
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := sqliteStore.PutToolCall(tc); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to save tool call | ToolID: %s | ToolCallID: %s | Error: %v", toolID, toolCall.ID, err)
		} else {
			log.Log.Infof("[CoreHandler] 🔧 Tool call saved | ToolID: %s | ToolCallID: %s | Function: %s", toolID, toolCall.ID, toolCall.Function.Name)
		}
		return toolID
	}
	return ""
}

// updateToolCallResponse updates the response for a tool call by ToolID.
func (ch *CoreHandler) updateToolCallResponse(toolID string, response string) {
	if toolID == "" {
		return
	}
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		UpdateToolCallResponse(string, string) error
	}); ok {
		if err := sqliteStore.UpdateToolCallResponse(toolID, response); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to update tool call response | ToolID: %s | Error: %v", toolID, err)
		} else {
			log.Log.Infof("[CoreHandler] ✅ Tool call response updated | ToolID: %s", toolID)
		}
	}
}

// ============================================================================
// Vision LLM Support (for image processing with cost-optimized model)
// ============================================================================

// UseVisionLLMConfig configures a separate LLM client for image processing
// This allows using a cheaper vision-capable model (e.g., gpt-5-nano) for images
// while keeping the main LLM for text-only orchestration
func (ch *CoreHandler) UseVisionLLMConfig(config LLMConfig) error {
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

// ProcessMessageWithImage handles messages that include an image
// It uses the Vision LLM (if configured) or falls back to the main LLM
// The image is processed directly by the LLM, not sent to UserAgents
// Uses per-user mutex to ensure only one message is processed at a time per user
func (ch *CoreHandler) ProcessMessageWithImage(
	ctx context.Context,
	userID string,
	userMessage string,
	imageData []byte,
	imageMimeType string,
) (string, error) {
	// Acquire per-user mutex to serialize message processing
	// This prevents race conditions on session creation and sequence number generation
	userMu := ch.getUserMutex(userID)
	userMu.Lock()
	defer userMu.Unlock()

	log.Log.Infof("[CoreHandler] 🖼️  Processing image message | UserID: %s | Message length: %d chars | Image size: %d bytes | MimeType: %s",
		userID, len(userMessage), len(imageData), imageMimeType)

	// Check if database is ready
	if !ch.userAgentHigh.IsDBReady() || !ch.userAgentLow.IsDBReady() {
		return "", fmt.Errorf("database is not ready. Call Init() on UserAgents first")
	}

	// Determine which LLM client to use
	llmClient := ch.visionLLMClient
	llmModel := ""
	if ch.visionLLMConfig != nil {
		llmModel = ch.visionLLMConfig.Model
	}

	// Fall back to main LLM if Vision LLM not configured
	if llmClient == nil {
		log.Log.Warnf("[CoreHandler] ⚠️  Vision LLM not configured, falling back to main LLM")
		llmClient = ch.llmClient
		llmModel = ch.llmConfig.Model
	}

	if llmClient == nil {
		return "", fmt.Errorf("LLM client not configured. Call UseLLMConfig first")
	}

	if llmModel == "" {
		llmModel = "openai/gpt-5-nano" // Default fallback
	}

	// Check user ban status
	if ch.userModeration != nil {
		if isBanned, banMessage := ch.userModeration.CheckBanStatus(userID); isBanned {
			return banMessage, nil
		}
	}

	// Get or create Core session
	coreSession, err := ch.getOrCreateCoreSession(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get or create core session: %w", err)
	}

	// Build base64 data URL for image
	base64Image := base64.StdEncoding.EncodeToString(imageData)
	dataURL := fmt.Sprintf("data:%s;base64,%s", imageMimeType, base64Image)

	// Create multimodal message with image
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

	// Add to session (store text representation for history)
	// Use a user-friendly message instead of technical MIME type
	historyContent := userMessage
	if historyContent == "" {
		historyContent = "(کاربر یک تصویر ارسال کرد)"
	} else {
		historyContent = fmt.Sprintf("(کاربر یک تصویر ارسال کرد) %s", userMessage)
	}
	coreSession.Msgs = append(
		coreSession.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: historyContent,
		},
	)

	// Update session model to vision model for proper tracking
	coreSession.Model = llmModel

	// Save user message to database
	// Note: User messages don't have a model - the model field stays empty for user messages
	imageMsgID, imageSeqID := coreSession.GenerateMessageIDWithSeq()
	userMsgRecord := model.NewUserMessage(imageMsgID, imageSeqID, userID, coreSession.SessionID, historyContent, model.ContentTypeImage)
	ch.saveMessage(userMsgRecord)

	// Build system prompts (simplified for vision - no tools needed)
	systemPrompts, err := ch.buildSystemPrompts(userID)
	if err != nil {
		return "", fmt.Errorf("failed to build system prompts: %w", err)
	}

	// Build messages for LLM call
	messages := []openai.ChatCompletionMessage{}

	// Add system prompts
	for _, prompt := range systemPrompts {
		messages = append(messages, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleSystem,
			Content: prompt,
		})
	}

	// Add conversation history (without the current message)
	historyMsgs := coreSession.Msgs
	if len(historyMsgs) > 1 {
		messages = append(messages, historyMsgs[:len(historyMsgs)-1]...)
	}

	// Add the multimodal message (with actual image)
	messages = append(messages, userMsg)

	// Add user_id to context
	ctx = model.WithUserID(ctx, userID)

	// Make LLM call (no tools for vision messages - direct response)
	log.Log.Infof("[CoreHandler] 🔵 VISION LLM >> Model: %s | Messages: %d | Image included", llmModel, len(messages))

	request := openai.ChatCompletionRequest{
		Model:    llmModel,
		Messages: messages,
	}

	resp, err := llmClient.CreateChatCompletion(ctx, request)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Vision LLM call failed | Error: %v", err)
		return "", fmt.Errorf("vision LLM call failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from vision LLM")
	}

	response := resp.Choices[0].Message.Content

	// Log token usage
	if resp.Usage.TotalTokens > 0 {
		log.Log.Infof("[CoreHandler] 📊 VISION TOKEN USAGE >> Model: %s | prompt=%d | completion=%d | total=%d",
			llmModel, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens)
	}

	// Add assistant response to session
	coreSession.Msgs = append(
		coreSession.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: response,
		},
	)
	coreSession.UpdatedAt = time.Now()

	// Save session
	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}

	// Save assistant message to database
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

// HasVisionLLM returns true if a Vision LLM is configured
func (ch *CoreHandler) HasVisionLLM() bool {
	return ch.visionLLMClient != nil && ch.visionLLMConfig != nil
}
