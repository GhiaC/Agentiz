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
	UserAgentHighModel string // e.g., "gpt-5.2" or "gpt-4o"
	UserAgentLowModel  string // e.g., "gpt-4o-mini"

	// Session configuration
	AutoSummarizeThreshold int // Default: 20 messages

	// WebSearchDisabled disables web_search and web_search_deepresearch tools
	WebSearchDisabled bool
}

// DefaultCoreHandlerConfig returns default configuration
func DefaultCoreHandlerConfig() CoreHandlerConfig {
	return CoreHandlerConfig{
		//UserAgentHighModel:     "gpt-4o", FOR TESTING
		UserAgentHighModel:     "gpt-4o-mini",
		UserAgentLowModel:      "gpt-4o-mini",
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

	// Configuration
	config CoreHandlerConfig

	// Function registry for Core's tools
	coreTools *model.FunctionRegistry

	// User moderation helper
	userModeration *UserModeration

	// Backup LLM chain (initialized from LLMConfig.BackupProviders)
	backups *backupChain
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
		coreTools:      model.NewFunctionRegistry(),
	}

	// Register Core's tools
	ch.registerCoreTools()

	return ch
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

// ProcessMessage is the main entry point for user messages
// It routes through the Core's orchestration logic
func (ch *CoreHandler) ProcessMessage(
	ctx context.Context,
	userID string,
	userMessage string,
) (string, error) {
	// Get user's total sessions count before processing
	userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
	ch.coreSessionsMu.RLock()
	totalCoreSessions := len(ch.coreSessions)
	ch.coreSessionsMu.RUnlock()

	log.Log.Infof("[CoreHandler] 🚀 Processing new message | UserID: %s | Message length: %d chars | User sessions: %d | Total Core sessions: %d",
		userID, len(userMessage), len(userSessions), totalCoreSessions)

	// Check if database is ready (check both UserAgents)
	if !ch.userAgentHigh.IsDBReady() || !ch.userAgentLow.IsDBReady() {
		return "", fmt.Errorf("database is not ready. Call Init() on UserAgents first to ensure database is fully loaded")
	}

	if ch.llmClient == nil {
		return "", fmt.Errorf("LLM client not configured. Call UseLLMConfig first")
	}

	// Check user ban status and nonsense messages
	var isNonsense bool
	if ch.userModeration != nil {
		// Check if user is banned
		if isBanned, banMessage := ch.userModeration.CheckBanStatus(userID); isBanned {
			return banMessage, nil
		}

		// Add user_id to context for LLM calls
		ctx = model.WithUserID(ctx, userID)

		// Check if message is nonsense and handle auto-ban
		shouldBan, banMessage, err := ch.userModeration.ProcessNonsenseCheck(ctx, userID, userMessage)
		if err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to process nonsense check, proceeding anyway | UserID: %s | Error: %v", userID, err)
		} else {
			// Determine if message is nonsense (if banMessage is not empty, it's nonsense)
			isNonsense = banMessage != "" || shouldBan
			if shouldBan {
				return banMessage, nil
			} else if banMessage != "" {
				// Warning message (no ban yet)
				return banMessage, nil
			}
		}
	}

	// Get or create Core session for this user
	coreSession, err := ch.getOrCreateCoreSession(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get or create core session: %w", err)
	}

	// Build system prompts
	systemPrompts, err := ch.buildSystemPrompts(userID)
	if err != nil {
		return "", fmt.Errorf("failed to build system prompts: %w", err)
	}

	// Add user message to Core's session
	coreSession.ConversationState.Msgs = append(
		coreSession.ConversationState.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: userMessage,
		},
	)

	// Save user message to database
	// Note: User messages don't have a model - the model field stays empty for user messages
	userMsg := model.NewUserMessage(userID, coreSession.SessionID, userMessage, model.ContentTypeText)
	// Set nonsense flag if detected
	userMsg.IsNonsense = isNonsense
	ch.saveMessage(userMsg)

	// Save Core session after adding user message
	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}

	// Build messages for LLM call
	messages := ch.buildMessages(systemPrompts, coreSession.ConversationState.Msgs)

	// Get Core's tools
	tools := ch.getCoreToolsForLLM()

	// Add user_id to context for LLM calls
	ctx = model.WithUserID(ctx, userID)

	// Make LLM call
	response, err := ch.processWithTools(ctx, messages, tools, userID, coreSession)
	if err != nil {
		return "", fmt.Errorf("failed to process message: %w", err)
	}

	// Add assistant response to Core's session
	coreSession.ConversationState.Msgs = append(
		coreSession.ConversationState.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: response,
		},
	)
	coreSession.ConversationState.LastActivity = time.Now()

	// Save Core session after adding assistant response
	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}

	return response, nil
}

// getOrCreateCoreSession gets or creates a Core session for a user
// It uses SessionHandler to ensure persistence in the database
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

			userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
			log.Log.Infof("[CoreHandler] 🔄 Using existing Core session | UserID: %s | SessionID: %s | User sessions: %d",
				userID, dbSession.SessionID, len(userSessions))
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
			userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
			log.Log.Infof("[CoreHandler] 🔄 Using existing Core session (after lock) | UserID: %s | SessionID: %s | User sessions: %d",
				userID, dbSession.SessionID, len(userSessions))
			return dbSession, nil
		}
	}

	// Try to get existing Core session from database
	// Check if store has GetCoreSession method (e.g., SQLiteStore)
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		GetCoreSession(string) (*model.Session, error)
	}); ok {
		existingCore, err := sqliteStore.GetCoreSession(userID)
		if err == nil && existingCore != nil {
			ch.coreSessions[userID] = existingCore
			userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
			log.Log.Infof("[CoreHandler] 🔄 Loaded Core session from database | UserID: %s | SessionID: %s | User sessions: %d",
				userID, existingCore.SessionID, len(userSessions))
			return existingCore, nil
		}
	} else {
		// Fallback: search through all sessions for Core type
		allSessions, err := ch.sessionHandler.ListUserSessions(userID)
		if err == nil {
			for _, s := range allSessions {
				if s.AgentType == model.AgentTypeCore {
					ch.coreSessions[userID] = s
					log.Log.Infof("[CoreHandler] 🔄 Found Core session from list | UserID: %s | SessionID: %s",
						userID, s.SessionID)
					return s, nil
				}
			}
		}
	}

	// Create new Core session through SessionHandler (which will persist it)
	session, err := ch.sessionHandler.CreateSession(userID, model.AgentTypeCore)
	if err != nil {
		return nil, fmt.Errorf("failed to create core session: %w", err)
	}

	ch.coreSessions[userID] = session

	userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
	log.Log.Infof("[CoreHandler] ✨ Created new Core session | UserID: %s | SessionID: %s", userID, session.SessionID)
	log.Log.Infof("[CoreHandler] 📊 User sessions: %d", len(userSessions))

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

	// 2. Session context - Summary and tags from previous conversations (if summarized)
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

	// 3. Active sessions prompt (shows current active session for each agent type)
	activePrompt := ch.buildActiveSessionsPrompt(userID)
	if activePrompt != "" {
		prompts = append(prompts, activePrompt)
	}

	// 4. Sessions list prompt (for change_session)
	sessionsPrompt, err := ch.sessionHandler.GetSessionsPrompt(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sessions prompt: %w", err)
	}
	prompts = append(prompts, sessionsPrompt)

	return prompts, nil
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
		msgCount := len(session.ConversationState.Msgs)
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

	// Add web search tools only if not disabled
	if !ch.config.WebSearchDisabled {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "web_search",
				Description: "Search the web for up-to-date information. Use this when you need current information, recent news, real-time data, or information that may have changed recently. The search will return results with citations to sources.",
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
				Description: "Same as web_search but uses Tongyi DeepResearch model (alibaba/tongyi-deepresearch-30b-a3b). Use for testing or when you want deep-research style search results.",
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
		modelName = "gpt-4o-mini"
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

	for i := 0; i < maxIterations; i++ {
		// Make LLM call (tries backup provider first, then falls back to OpenAI)
		resp, err := ch.callLLM(ctx, modelName, currentMessages, tools)
		if err != nil {
			return "", formatLLMError(err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response from LLM")
		}

		choice := resp.Choices[0]

		// Save message to database
		request := openai.ChatCompletionRequest{
			Model:    modelName,
			Messages: currentMessages,
			Tools:    tools,
		}
		messageID := ch.saveCoreMessage(userID, request, resp, choice)

		// If no tool calls, return the response
		if len(choice.Message.ToolCalls) == 0 {
			return choice.Message.Content, nil
		}

		// Add assistant message with tool calls
		currentMessages = append(currentMessages, choice.Message)

		// Execute each tool call
		for _, toolCall := range choice.Message.ToolCalls {
			// Save tool call to database (before execution)
			if coreSession != nil {
				ch.saveToolCall(userID, coreSession.SessionID, messageID, toolCall)
			}

			result, err := ch.executeCoreTool(ctx, userID, toolCall)
			if err != nil {
				result = fmt.Sprintf("Error executing tool: %v", err)
			}

			// Update tool call response in database
			if coreSession != nil {
				ch.updateToolCallResponse(toolCall.ID, result)
			}

			// Add tool result
			currentMessages = append(currentMessages, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    result,
				ToolCallID: toolCall.ID,
			})
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
		return ch.callUserAgent(ctx, userID, args, ch.userAgentHigh, model.AgentTypeHigh)

	case "call_user_agent_low":
		result, err := ch.callUserAgent(ctx, userID, args, ch.userAgentLow, model.AgentTypeLow)
		if err != nil {
			return "", err
		}
		// Check for escalation
		if strings.HasPrefix(strings.TrimSpace(result), "ESCALATE:") {
			// Auto-escalate to high model
			return ch.callUserAgent(ctx, userID, args, ch.userAgentHigh, model.AgentTypeHigh)
		}
		return result, nil

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

	session, err := ch.sessionHandler.CreateSession(userID, agentType)
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
// Persists to database via User model
func (ch *CoreHandler) setActiveSessionID(userID string, agentType model.AgentType, sessionID string) error {
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
func (ch *CoreHandler) getOrCreateActiveSession(userID string, agentType model.AgentType) (string, error) {
	// Check if active session exists
	sessionID := ch.getActiveSessionID(userID, agentType)
	if sessionID != "" {
		// Verify session still exists in database
		session, err := ch.sessionHandler.GetSession(sessionID)
		if err == nil && session != nil {
			return sessionID, nil
		}
		// Session was deleted, clear the reference
		log.Log.Warnf("[CoreHandler] ⚠️  Active session no longer exists, creating new | UserID: %s | AgentType: %s | OldSessionID: %s",
			userID, agentType, sessionID)
	}

	// Create new session
	session, err := ch.sessionHandler.CreateSession(userID, agentType)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	// Set as active session
	if err := ch.setActiveSessionID(userID, agentType, session.SessionID); err != nil {
		return "", fmt.Errorf("failed to set active session: %w", err)
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
	msg := model.NewMessage(
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

// saveToolCall saves a tool call to the database
func (ch *CoreHandler) saveToolCall(userID string, sessionID string, messageID string, toolCall openai.ToolCall) {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		PutToolCall(*model.ToolCall) error
	}); ok {
		now := time.Now()
		tc := &model.ToolCall{
			ToolCallID:   toolCall.ID,
			MessageID:    messageID,
			SessionID:    sessionID,
			UserID:       userID,
			AgentType:    model.AgentTypeCore,
			FunctionName: toolCall.Function.Name,
			Arguments:    toolCall.Function.Arguments,
			Response:     "", // Will be updated after execution
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := sqliteStore.PutToolCall(tc); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to save tool call | ToolCallID: %s | Error: %v", toolCall.ID, err)
		} else {
			log.Log.Infof("[CoreHandler] 🔧 Tool call saved | ToolCallID: %s | Function: %s", toolCall.ID, toolCall.Function.Name)
		}
	}
}

// updateToolCallResponse updates the response for a tool call
func (ch *CoreHandler) updateToolCallResponse(toolCallID string, response string) {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		UpdateToolCallResponse(string, string) error
	}); ok {
		if err := sqliteStore.UpdateToolCallResponse(toolCallID, response); err != nil {
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to update tool call response | ToolCallID: %s | Error: %v", toolCallID, err)
		} else {
			log.Log.Infof("[CoreHandler] ✅ Tool call response updated | ToolCallID: %s", toolCallID)
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
func (ch *CoreHandler) ProcessMessageWithImage(
	ctx context.Context,
	userID string,
	userMessage string,
	imageData []byte,
	imageMimeType string,
) (string, error) {
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
		llmModel = "gpt-4o-mini" // Default fallback
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
	coreSession.ConversationState.Msgs = append(
		coreSession.ConversationState.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: historyContent,
		},
	)

	// Update session model to vision model for proper tracking
	coreSession.Model = llmModel

	// Save user message to database
	// Note: User messages don't have a model - the model field stays empty for user messages
	userMsgRecord := model.NewUserMessage(userID, coreSession.SessionID, historyContent, model.ContentTypeImage)
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
	historyMsgs := coreSession.ConversationState.Msgs
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
	coreSession.ConversationState.Msgs = append(
		coreSession.ConversationState.Msgs,
		openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: response,
		},
	)
	coreSession.ConversationState.LastActivity = time.Now()

	// Save session
	if err := ch.saveCoreSession(coreSession); err != nil {
		return "", fmt.Errorf("failed to save core session: %w", err)
	}

	// Save assistant message to database
	assistantMsg := model.NewMessage(
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
