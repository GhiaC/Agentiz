package engine

import (
	"context"
	_ "embed"
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
}

// DefaultCoreHandlerConfig returns default configuration
func DefaultCoreHandlerConfig() CoreHandlerConfig {
	return CoreHandlerConfig{
		//UserAgentHighModel:     "gpt-4o", FOR TESTING
		UserAgentHighModel:     "gpt-4o-mini",
		UserAgentLowModel:      "gpt-4o-mini",
		AutoSummarizeThreshold: 5,
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

	// Core's own sessions per user (for orchestration context)
	coreSessions   map[string]*model.Session
	coreSessionsMu sync.RWMutex

	// Configuration
	config CoreHandlerConfig

	// Function registry for Core's tools
	coreTools *model.FunctionRegistry

	// User moderation helper
	userModeration *UserModeration
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
	userMsg := model.NewUserMessage(userID, coreSession.SessionID, userMessage)
	// Set model from session if available
	if coreSession.Model != "" {
		userMsg.Model = coreSession.Model
	}
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

	// 2. Sessions summary prompt
	sessionsPrompt, err := ch.sessionHandler.GetSessionsPrompt(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get sessions prompt: %w", err)
	}
	prompts = append(prompts, sessionsPrompt)

	return prompts, nil
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
	return []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "call_user_agent_high",
				Description: "Send a message to the high-intelligence UserAgent for complex tasks",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"session_id": map[string]interface{}{
							"type":        "string",
							"description": "The session ID to use for this conversation. This is the value inside the square brackets [XXX] from the sessions list, NOT the title in quotes. Example: if you see '[1802460620-260205-ahov] \"Some Title\"', use '1802460620-260205-ahov' as session_id.",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "The message to send to the UserAgent",
						},
					},
					"required": []string{"session_id", "message"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "call_user_agent_low",
				Description: "Send a message to the cost-effective UserAgent for simple tasks",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"session_id": map[string]interface{}{
							"type":        "string",
							"description": "The session ID to use for this conversation. This is the value inside the square brackets [XXX] from the sessions list, NOT the title in quotes. Example: if you see '[1802460620-260205-ahov] \"Some Title\"', use '1802460620-260205-ahov' as session_id.",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "The message to send to the UserAgent",
						},
					},
					"required": []string{"session_id", "message"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "create_session",
				Description: "Create a new session for a UserAgent",
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
				Name:        "summarize_session",
				Description: "Trigger summarization of a session to archive old messages",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"session_id": map[string]interface{}{
							"type":        "string",
							"description": "The session ID to summarize",
						},
					},
					"required": []string{"session_id"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "list_sessions",
				Description: "Get a refreshed list of all sessions for the current user",
				Parameters: map[string]interface{}{
					"type":       "object",
					"properties": map[string]interface{}{},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "update_session_metadata",
				Description: "Update the title and tags of a session",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"session_id": map[string]interface{}{
							"type":        "string",
							"description": "The session ID to update",
						},
						"title": map[string]interface{}{
							"type":        "string",
							"description": "New title for the session",
						},
						"tags": map[string]interface{}{
							"type":        "array",
							"items":       map[string]interface{}{"type": "string"},
							"description": "Tags to apply to the session",
						},
					},
					"required": []string{"session_id"},
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "ban_user",
				Description: "Ban a user for a specified duration. Use this when a user repeatedly sends nonsense messages or violates rules. Duration is in hours (0 means permanent ban). Note: Once banned, the user's messages will not be processed, so this action should be used carefully.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"user_id": map[string]interface{}{
							"type":        "string",
							"description": "The user ID to ban",
						},
						"duration_hours": map[string]interface{}{
							"type":        "number",
							"description": "Ban duration in hours (0 for permanent ban)",
						},
						"message": map[string]interface{}{
							"type":        "string",
							"description": "Optional custom ban message to show to the user",
						},
					},
					"required": []string{"user_id", "duration_hours"},
				},
			},
		},
		{
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
		},
	}
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
		request := openai.ChatCompletionRequest{
			Model:    modelName,
			Messages: currentMessages,
			Tools:    tools,
		}
		resp, err := ch.llmClient.CreateChatCompletion(ctx, request)
		if err != nil {
			return "", formatLLMError(err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response from LLM")
		}

		choice := resp.Choices[0]

		// Save message to database
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

	case "summarize_session":
		return ch.summarizeSessionTool(ctx, args)

	case "list_sessions":
		return ch.listSessionsTool(userID)

	case "update_session_metadata":
		return ch.updateSessionMetadataTool(args)

	case "ban_user":
		return ch.banUserTool(ctx, args)

	case "web_search":
		return ch.webSearchTool(ctx, userID, args)

	default:
		return "", fmt.Errorf("unknown tool: %s", toolCall.Function.Name)
	}
}

// callUserAgent sends a message to a UserAgent
func (ch *CoreHandler) callUserAgent(
	ctx context.Context,
	userID string, // userID - reserved for future use
	args map[string]interface{},
	agent *Engine,
	agentType model.AgentType,
) (string, error) {
	sessionID, ok := args["session_id"].(string)
	if !ok || sessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}

	message, ok := args["message"].(string)
	if !ok || message == "" {
		return "", fmt.Errorf("message is required")
	}

	log.Log.Infof("[CoreHandler] 🎯 Selecting session for UserAgent | SessionID: %s | AgentType: %s | UserID: %s",
		sessionID, agentType, userID)

	// Verify session exists and belongs to the right agent type
	session, err := ch.sessionHandler.GetSession(sessionID)
	if err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Session not found | SessionID: %s | Error: %v", sessionID, err)
		return "", fmt.Errorf("session not found: %s", sessionID)
	}
	if session.AgentType != agentType {
		log.Log.Warnf("[CoreHandler] ⚠️  Session type mismatch | SessionID: %s | Expected: %s | Got: %s | Creating new session",
			sessionID, agentType, session.AgentType)

		// Create a new session of the correct type
		newSession, err := ch.sessionHandler.CreateSession(userID, agentType)
		if err != nil {
			log.Log.Errorf("[CoreHandler] ❌ Failed to create new session | UserID: %s | AgentType: %s | Error: %v",
				userID, agentType, err)
			return "", fmt.Errorf("failed to create %s session: %w", agentType, err)
		}

		// Update sessionID in args for future use
		sessionID = newSession.SessionID
		args["session_id"] = sessionID

		log.Log.Infof("[CoreHandler] ✅ Created new session for escalation | SessionID: %s | AgentType: %s | UserID: %s",
			sessionID, agentType, userID)

		// Get the new session
		session = newSession
	}

	log.Log.Infof("[CoreHandler] ✅ Session selected | SessionID: %s | AgentType: %s | Title: %s | Message length: %d chars",
		sessionID, agentType, getSessionTitleForLog(session), len(message))

	// List all available sessions for comparison
	allSessions, _ := ch.sessionHandler.ListUserSessions(userID)
	log.Log.Infof("[CoreHandler] 📊 Available sessions for user: %d | Selected: %s", len(allSessions), sessionID)

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

	// List all sessions after creation
	allSessions, _ := ch.sessionHandler.ListUserSessions(userID)
	log.Log.Infof("[CoreHandler] ✅ Session created successfully | SessionID: %s | Total user sessions: %d", session.SessionID, len(allSessions))

	return fmt.Sprintf("Created session: %s (type: %s)", session.SessionID, agentType), nil
}

// summarizeSessionTool triggers session summarization
func (ch *CoreHandler) summarizeSessionTool(ctx context.Context, args map[string]interface{}) (string, error) {
	sessionID, ok := args["session_id"].(string)
	if !ok || sessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}

	if err := ch.sessionHandler.SummarizeSession(ctx, sessionID); err != nil {
		return "", fmt.Errorf("failed to summarize session: %w", err)
	}

	return fmt.Sprintf("Session %s summarized successfully", sessionID), nil
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

// updateSessionMetadataTool updates session metadata
func (ch *CoreHandler) updateSessionMetadataTool(args map[string]interface{}) (string, error) {
	sessionID, ok := args["session_id"].(string)
	if !ok || sessionID == "" {
		return "", fmt.Errorf("session_id is required")
	}

	title, _ := args["title"].(string)

	var tags []string
	if tagsInterface, ok := args["tags"].([]interface{}); ok {
		for _, t := range tagsInterface {
			if tagStr, ok := t.(string); ok {
				tags = append(tags, tagStr)
			}
		}
	}

	if err := ch.sessionHandler.UpdateSessionMetadata(sessionID, title, tags, ""); err != nil {
		return "", fmt.Errorf("failed to update session: %w", err)
	}

	return fmt.Sprintf("Session %s updated", sessionID), nil
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

// banUserTool bans a user for a specified duration
func (ch *CoreHandler) banUserTool(_ context.Context, args map[string]interface{}) (string, error) {
	userID, ok := args["user_id"].(string)
	if !ok || userID == "" {
		return "", fmt.Errorf("user_id is required")
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

// webSearchTool performs a web search using OpenAI's web search capability
func (ch *CoreHandler) webSearchTool(ctx context.Context, userID string, args map[string]interface{}) (string, error) {
	query, ok := args["query"].(string)
	if !ok || query == "" {
		return "", fmt.Errorf("query is required")
	}

	// Ensure userID is in context
	if userID != "" {
		ctx = model.WithUserID(ctx, userID)
	}

	// Use the helper function to perform web search
	result, err := PerformWebSearch(ctx, ch.llmClient, ch.llmConfig, query, userID)
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
