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
		AutoSummarizeThreshold: 20,
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

	if ch.llmClient == nil {
		return "", fmt.Errorf("LLM client not configured. Call UseLLMConfig first")
	}

	// Get or create Core session for this user
	coreSession := ch.getOrCreateCoreSession(userID)

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

	// Build messages for LLM call
	messages := ch.buildMessages(systemPrompts, coreSession.ConversationState.Msgs)

	// Get Core's tools
	tools := ch.getCoreToolsForLLM()

	// Make LLM call
	response, err := ch.processWithTools(ctx, messages, tools, userID)
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

	return response, nil
}

// getOrCreateCoreSession gets or creates a Core session for a user
func (ch *CoreHandler) getOrCreateCoreSession(userID string) *model.Session {
	ch.coreSessionsMu.RLock()
	session, exists := ch.coreSessions[userID]
	ch.coreSessionsMu.RUnlock()

	if exists {
		// Get user's total sessions count from SessionHandler
		userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
		log.Log.Infof("[CoreHandler] 🔄 Using existing Core session | UserID: %s | SessionID: %s | User sessions: %d | Total Core sessions: %d",
			userID, session.SessionID, len(userSessions), len(ch.coreSessions))
		return session
	}

	ch.coreSessionsMu.Lock()
	defer ch.coreSessionsMu.Unlock()

	// Double-check after acquiring write lock
	if session, exists = ch.coreSessions[userID]; exists {
		// Get user's total sessions count from SessionHandler
		userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
		log.Log.Infof("[CoreHandler] 🔄 Using existing Core session (after lock) | UserID: %s | SessionID: %s | User sessions: %d | Total Core sessions: %d",
			userID, session.SessionID, len(userSessions), len(ch.coreSessions))
		return session
	}

	session = model.NewSessionWithType(userID, model.AgentTypeCore)
	ch.coreSessions[userID] = session

	// Get user's total sessions count from SessionHandler
	userSessions, _ := ch.sessionHandler.ListUserSessions(userID)
	log.Log.Infof("[CoreHandler] ✨ Created new Core session | UserID: %s | SessionID: %s", userID, session.SessionID)
	log.Log.Infof("[CoreHandler] 📊 User sessions: %d | Total Core sessions in memory: %d", len(userSessions), len(ch.coreSessions))

	return session
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
							"description": "The session ID to use for this conversation",
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
							"description": "The session ID to use for this conversation",
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
	}
}

// processWithTools handles the LLM call and tool execution loop
func (ch *CoreHandler) processWithTools(
	ctx context.Context,
	messages []openai.ChatCompletionMessage,
	tools []openai.Tool,
	userID string,
) (string, error) {
	maxIterations := 10
	currentMessages := messages

	for i := 0; i < maxIterations; i++ {
		resp, err := ch.llmClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model:    ch.llmConfig.Model,
			Messages: currentMessages,
			Tools:    tools,
		})
		if err != nil {
			return "", fmt.Errorf("LLM request failed: %w", err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response from LLM")
		}

		choice := resp.Choices[0]

		// If no tool calls, return the response
		if len(choice.Message.ToolCalls) == 0 {
			return choice.Message.Content, nil
		}

		// Add assistant message with tool calls
		currentMessages = append(currentMessages, choice.Message)

		// Execute each tool call
		for _, toolCall := range choice.Message.ToolCalls {
			result, err := ch.executeCoreTool(ctx, userID, toolCall)
			if err != nil {
				result = fmt.Sprintf("Error executing tool: %v", err)
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
