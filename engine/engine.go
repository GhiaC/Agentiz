package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/config"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

//go:embed engine.md
var basePrompt string

// Global scheduler once to ensure scheduler starts only once per session store
var schedulerOnce sync.Once
var schedulerOnceMap = make(map[store.SessionStore]*sync.Once)
var schedulerOnceMapMu sync.Mutex

// LLMConfig holds configuration for LLM client
type LLMConfig struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client // Optional: custom HTTP client (e.g., for proxy support)

	// Tool result truncation settings
	MaxToolResultLength int    // Max chars before truncating (default: 250)
	CollectResultModel  string // LLM model for collect_result tool (default: same as Model)
}

// ToolExecutor executes a tool call and returns the result
type ToolExecutor func(toolName string, args map[string]interface{}) (string, error)

// Engine orchestrates session management, tool execution, and LLM interaction.
// It intentionally exposes only the operations that are consumed by InfraAgent.
// Engine uses SessionStore for all state management, including conversation history.
type Engine struct {
	Repo      *fsrepo.NodeRepository
	Sessions  store.SessionStore
	Functions *model.FunctionRegistry
	Executor  ToolExecutor
	// LLM client and configuration
	llmClient *openai.Client
	llmConfig LLMConfig
	// Database readiness flag
	dbReady   bool
	dbReadyMu sync.RWMutex
	// Scheduler for session summarization
	scheduler   *SessionScheduler
	schedulerMu sync.RWMutex
}

// Init initializes the engine by loading the root node and verifying Sessions store is ready.
// This must be called before ProcessMessage to ensure the database is fully loaded.
func (e *Engine) Init() error {
	e.dbReadyMu.Lock()
	defer e.dbReadyMu.Unlock()

	// Try to load root node to verify repository is ready
	_, err := e.Repo.LoadNode("root")
	if err != nil {
		e.dbReady = false
		return fmt.Errorf("failed to initialize engine: repository not ready - %w", err)
	}

	// Verify Sessions store is ready by testing a basic operation
	if e.Sessions == nil {
		e.dbReady = false
		return fmt.Errorf("failed to initialize engine: Sessions store is nil")
	}

	// Test Sessions store by performing a List operation (should not fail even with empty result)
	_, err = e.Sessions.List("__init_test__")
	if err != nil {
		e.dbReady = false
		return fmt.Errorf("failed to initialize engine: Sessions store not ready - %w", err)
	}

	e.dbReady = true
	log.Log.Infof("[Engine] ✅ Database initialized and ready (Repo + Sessions)")
	return nil
}

// IsDBReady returns whether the database is ready
func (e *Engine) IsDBReady() bool {
	e.dbReadyMu.RLock()
	defer e.dbReadyMu.RUnlock()
	return e.dbReady
}

// UseFunctionRegistry configures the registry that will be used for executing tools.
func (e *Engine) UseFunctionRegistry(registry *model.FunctionRegistry) {
	if registry == nil {
		registry = model.NewFunctionRegistry()
	}
	e.Functions = registry
}

// UseLLMConfig configures the LLM client for the engine
// It also automatically starts the scheduler if enabled
func (e *Engine) UseLLMConfig(config LLMConfig) error {
	openaiConfig := openai.DefaultConfig(config.APIKey)
	if config.BaseURL != "" {
		openaiConfig.BaseURL = config.BaseURL
	}
	// Use custom HTTP client if provided (e.g., for proxy support)
	if config.HTTPClient != nil {
		openaiConfig.HTTPClient = config.HTTPClient
	}

	client := openai.NewClientWithConfig(openaiConfig)
	e.llmClient = client
	e.llmConfig = config

	// Automatically start scheduler if LLM is configured and scheduler is not already running
	// Use sync.Once per session store to ensure scheduler starts only once
	if config.APIKey != "" && e.Sessions != nil {
		schedulerOnceMapMu.Lock()
		once, exists := schedulerOnceMap[e.Sessions]
		if !exists {
			once = &sync.Once{}
			schedulerOnceMap[e.Sessions] = once
		}
		schedulerOnceMapMu.Unlock()

		once.Do(func() {
			// Start scheduler in background
			ctx := context.Background()
			e.schedulerMu.Lock()
			if e.scheduler == nil {
				if err := e.startScheduler(ctx, client); err != nil {
					log.Log.Warnf("[Engine] ⚠️  Failed to start scheduler: %v", err)
				} else {
					e.schedulerMu.Unlock()
					return
				}
			}
			e.schedulerMu.Unlock()
		})
	}

	return nil
}

// startScheduler starts the session scheduler
func (e *Engine) startScheduler(ctx context.Context, llmClient *openai.Client) error {
	// Load scheduler config from environment
	cfg, err := config.Load()
	if err != nil {
		log.Log.Warnf("[Engine] ⚠️  Failed to load config, using defaults: %v", err)
		cfg = &config.Config{}
	}
	schedulerConfig := cfg.Scheduler
	if !schedulerConfig.Enabled {
		log.Log.Infof("[Engine] ⏸️  Scheduler is disabled via config")
		return nil
	}

	// Create session handler
	sessionHandlerConfig := model.DefaultSessionHandlerConfig()
	sessionHandler := model.NewSessionHandler(e.Sessions, sessionHandlerConfig)

	// Create LLM client wrapper for session handler
	llmClientWrapper := &openAIClientWrapperForSessionHandler{
		Client: llmClient,
	}
	sessionHandler.SetLLMClient(llmClientWrapper)

	// Create scheduler config
	schedulerConfigStruct := DefaultSessionSchedulerConfig()
	if schedulerConfig.CheckInterval > 0 {
		schedulerConfigStruct.CheckInterval = schedulerConfig.CheckInterval
	}
	if schedulerConfig.SummarizedAtThreshold > 0 {
		schedulerConfigStruct.SummarizedAtThreshold = schedulerConfig.SummarizedAtThreshold
	}
	if schedulerConfig.LastActivityThreshold > 0 {
		schedulerConfigStruct.LastActivityThreshold = schedulerConfig.LastActivityThreshold
	}
	if schedulerConfig.MessageThreshold > 0 {
		schedulerConfigStruct.MessageThreshold = schedulerConfig.MessageThreshold
	}
	if schedulerConfig.SummaryModel != "" {
		schedulerConfigStruct.SummaryModel = schedulerConfig.SummaryModel
	}

	// Create and start scheduler
	scheduler := NewSessionScheduler(sessionHandler, llmClient, schedulerConfigStruct)
	e.scheduler = scheduler
	scheduler.Start(ctx)

	log.Log.Infof("[Engine] ✅ Session scheduler started | CheckInterval: %v | SummarizedAtThreshold: %v | LastActivityThreshold: %v | MessageThreshold: %d | SummaryModel: %s",
		schedulerConfigStruct.CheckInterval, schedulerConfigStruct.SummarizedAtThreshold, schedulerConfigStruct.LastActivityThreshold, schedulerConfigStruct.MessageThreshold, schedulerConfigStruct.SummaryModel)

	return nil
}

// openAIClientWrapperForSessionHandler wraps openai.Client to implement model.LLMClient interface
type openAIClientWrapperForSessionHandler struct {
	Client *openai.Client
}

// CreateChatCompletion implements model.LLMClient interface
func (w *openAIClientWrapperForSessionHandler) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return w.Client.CreateChatCompletion(ctx, request)
}

// CreateSession initializes a fresh session anchored at the root node.
func (e *Engine) CreateSession(userID string) (*model.Session, error) {
	session := model.NewSession(userID)

	rootNode, err := e.Repo.LoadNode("root")
	if err != nil {
		return nil, fmt.Errorf("failed to load root node: %w", err)
	}

	session.NodeDigests = []model.NodeDigest{summarizeNode(rootNode)}

	if err := e.Sessions.Put(session); err != nil {
		return nil, fmt.Errorf("failed to persist session: %w", err)
	}

	log.Log.Infof("[Engine] ✅ Created new session | UserID: %s | SessionID: %s", userID, session.SessionID)

	return session, nil
}

// SetProgress sets the progress state for a session
func (e *Engine) SetProgress(sessionID string, inProgress bool) error {
	state := e.getConversationState(sessionID)
	state.InProgress = inProgress
	return e.setConversationState(sessionID, state)
}

// OpenFile opens a node by path and adds it to the session's opened nodes.
// Returns the node content if successfully opened, or an error if the path doesn't exist.
func (e *Engine) OpenFile(sessionID string, path string) (string, error) {
	// Get session
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return "", fmt.Errorf("session not found: %w", err)
	}

	// Check if already opened
	alreadyOpened := false
	for _, digest := range session.NodeDigests {
		if digest.Path == path {
			alreadyOpened = true
			// Already opened, return content
			node, err := e.Repo.LoadNode(path)
			if err != nil {
				return "", fmt.Errorf("failed to load node: %w", err)
			}

			// Check if file is recorded as open in database, if not, record it
			if sqliteStore, ok := e.Sessions.(interface {
				GetCurrentlyOpenedFilesBySession(string) ([]*model.OpenedFile, error)
				AddOpenedFile(*model.OpenedFile) error
			}); ok {
				openedFiles, err := sqliteStore.GetCurrentlyOpenedFilesBySession(sessionID)
				if err != nil {
					log.Log.Warnf("[Engine] ⚠️  Failed to get opened files | SessionID: %s | Error: %v", sessionID, err)
				} else {
					// Check if file is already recorded
					isRecorded := false
					for _, f := range openedFiles {
						if f.FilePath == path && f.IsOpen {
							isRecorded = true
							break
						}
					}

					// Record file if not found in database
					if !isRecorded {
						fileName := path
						if node.Title != "" {
							fileName = node.Title
						}
						openedFile := model.NewOpenedFile(sessionID, session.UserID, path, fileName)
						if err := sqliteStore.AddOpenedFile(openedFile); err != nil {
							log.Log.Warnf("[Engine] ⚠️  Failed to record opened file | SessionID: %s | Path: %s | Error: %v", sessionID, path, err)
						}
					}
				}
			}

			return node.Content, nil
		}
	}

	// Load the node
	node, err := e.Repo.LoadNode(path)
	if err != nil {
		return "", fmt.Errorf("file not found: %s", path)
	}

	// Add to session's opened nodes
	session.NodeDigests = append(session.NodeDigests, summarizeNode(node))

	// Persist session
	if err := e.Sessions.Put(session); err != nil {
		return "", fmt.Errorf("failed to update session: %w", err)
	}

	// Record opened file in database (only if not already opened)
	if !alreadyOpened {
		if sqliteStore, ok := e.Sessions.(interface {
			AddOpenedFile(*model.OpenedFile) error
		}); ok {
			fileName := path
			if node.Title != "" {
				fileName = node.Title
			}
			openedFile := model.NewOpenedFile(sessionID, session.UserID, path, fileName)
			if err := sqliteStore.AddOpenedFile(openedFile); err != nil {
				log.Log.Warnf("[Engine] ⚠️  Failed to record opened file | SessionID: %s | Path: %s | Error: %v", sessionID, path, err)
			} else {
				log.Log.Infof("[Engine] 📂 File opened recorded | SessionID: %s | Path: %s | FileID: %s", sessionID, path, openedFile.FileID)
			}
		}
	}

	return node.Content, nil
}

// CloseFile removes a node from the session's opened nodes.
// Returns an error if the path is not opened or is the root node.
func (e *Engine) CloseFile(sessionID string, path string) error {
	// Prevent closing root
	if path == "root" {
		return fmt.Errorf("cannot close root node")
	}

	// Get session
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return fmt.Errorf("session not found: %w", err)
	}

	// Find and remove the node
	found := false
	newDigests := make([]model.NodeDigest, 0, len(session.NodeDigests))
	for _, digest := range session.NodeDigests {
		if digest.Path == path {
			found = true
			continue
		}
		newDigests = append(newDigests, digest)
	}

	if !found {
		return fmt.Errorf("file not opened: %s", path)
	}

	session.NodeDigests = newDigests

	// Persist session
	if err := e.Sessions.Put(session); err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	// Record closed file in database
	if sqliteStore, ok := e.Sessions.(interface {
		CloseOpenedFile(string, string) error
	}); ok {
		if err := sqliteStore.CloseOpenedFile(sessionID, path); err != nil {
			log.Log.Warnf("[Engine] ⚠️  Failed to record closed file | SessionID: %s | Path: %s | Error: %v", sessionID, path, err)
		} else {
			log.Log.Infof("[Engine] 📂 File closed recorded | SessionID: %s | Path: %s", sessionID, path)
		}
	}

	return nil
}

// ProcessMessage routes a user message through the LLM workflow and tool executor.
func (e *Engine) ProcessMessage(
	ctx context.Context,
	sessionID string,
	userMessage string,
) (string, int, error) {
	log.Log.Infof("[Engine] 🚀 Processing message | SessionID: %s | Message length: %d chars", sessionID, len(userMessage))

	// Check if database is ready
	if !e.IsDBReady() {
		return "", 0, errors.New("database is not ready. Call Init() first to ensure database is fully loaded")
	}

	if e.llmClient == nil {
		return "", 0, errors.New("LLM client is not configured. Call UseLLMConfig first")
	}

	// Get conversation state from session
	convState := e.getConversationState(sessionID)

	// Get session for logging
	session, err := e.Sessions.Get(sessionID)
	if err == nil {
		log.Log.Infof("[Engine] 🔍 Retrieved session | SessionID: %s | UserID: %s | Messages in history: %d",
			sessionID, session.UserID, len(convState.Msgs))
	} else {
		log.Log.Warnf("[Engine] ⚠️  Session not found | SessionID: %s | Error: %v", sessionID, err)
	}

	// Check if already in progress
	if convState.InProgress {
		if err := e.queueMessage(sessionID, userMessage); err != nil {
			return "", 0, fmt.Errorf("failed to update session: %w", err)
		}
		return "در حال پردازش درخواست قبلی... لطفا صبر کنید.", 0, nil
	}

	// Set progress flag
	convState.InProgress = true
	if err := e.setConversationState(sessionID, convState); err != nil {
		return "", 0, fmt.Errorf("failed to update session: %w", err)
	}

	defer func() {
		// Get fresh state to preserve messages added during processing
		freshState := e.getConversationState(sessionID)
		freshState.InProgress = false
		e.setConversationState(sessionID, freshState)
	}()

	// Clean up old function calls if last activity was more than 2 hours ago
	if convState.LastActivity.Before(time.Now().Add(-2 * time.Hour)) {
		if err := e.removeFunctionCalls(sessionID); err != nil {
			return "", 0, fmt.Errorf("failed to clean up function calls: %w", err)
		}
		convState = e.getConversationState(sessionID)
	}

	// Add user message
	if len(userMessage) > 1 {
		// Get session to access userID
		session, err := e.Sessions.Get(sessionID)
		if err != nil {
			return "", 0, fmt.Errorf("failed to get session: %w", err)
		}
		if err := e.appendMessages(sessionID, []openai.ChatCompletionMessage{
			{
				Role:    openai.ChatMessageRoleUser,
				Content: userMessage,
			},
		}); err != nil {
			return "", 0, fmt.Errorf("failed to add user message: %w", err)
		}

		// Save user message to database
		userMsg := model.NewUserMessage(session.UserID, sessionID, userMessage)
		// Set model from session if available
		if session.Model != "" {
			userMsg.Model = session.Model
		}
		if sqliteStore, ok := e.Sessions.(interface {
			PutMessage(*model.Message) error
		}); ok {
			if err := sqliteStore.PutMessage(userMsg); err != nil {
				log.Log.Warnf("[Engine] ⚠️  Failed to save user message | SessionID: %s | Error: %v", sessionID, err)
			}
		}
	}

	response, tokens, err := e.processChatRequest(ctx, sessionID)
	if err != nil {
		log.Log.Errorf("[Engine] ❌ LLM processing failed | SessionID: %s | Error: %v", sessionID, err)
		return "", tokens, fmt.Errorf("LLM processing failed: %w", err)
	}

	log.Log.Infof("[Engine] ✅ Message processed successfully | SessionID: %s | Response length: %d chars | Tokens: %d",
		sessionID, len(response), tokens)

	return response, tokens, nil
}

func summarizeNode(node *model.Node) model.NodeDigest {
	excerpt := node.Content
	if len(excerpt) > 100 {
		excerpt = excerpt[:100] + "..."
	}
	return model.NodeDigest{
		Path:     node.Path,
		ID:       node.ID,
		Title:    node.Title,
		Hash:     node.Hash,
		LoadedAt: node.LoadedAt,
		Excerpt:  excerpt,
	}
}

// GetSystemPrompts returns an array of system prompts in the following order:
// 1. Base prompt (engine.md) - Architecture overview and instructions
// 2. File index - List of all knowledge files with metadata
// 3. Opened files - Content of currently opened nodes
//
// The order is deterministic to enable AI prompt caching.
func (e *Engine) GetSystemPrompts(session *model.Session) []string {
	var prompts []string

	// 1. Base prompt (engine.md)
	if basePrompt != "" {
		prompts = append(prompts, basePrompt)
	}

	// 2. File index - all files with metadata
	fileIndex := e.buildFileIndex(session)
	if fileIndex != "" {
		prompts = append(prompts, fileIndex)
	}

	// 3. Opened files content
	openedPrompts := e.getOpenedNodePrompts(session)
	prompts = append(prompts, openedPrompts...)

	return prompts
}

// buildFileIndex generates a compact file index for LLM context.
// Format: Path | Description | Summary | IsOpen | Length
func (e *Engine) buildFileIndex(session *model.Session) string {
	// Build set of opened paths for quick lookup
	openedPaths := make(map[string]bool)
	for _, digest := range session.NodeDigests {
		openedPaths[digest.Path] = true
	}

	// Collect all nodes recursively
	var entries []string
	e.collectFileIndexEntries("root", openedPaths, &entries)

	if len(entries) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# File Index\n\n")
	sb.WriteString("| Path | Description | Summary | Open | Len |\n")
	sb.WriteString("|------|-------------|---------|------|-----|\n")
	for _, entry := range entries {
		sb.WriteString(entry)
		sb.WriteString("\n")
	}

	return sb.String()
}

// collectFileIndexEntries recursively collects file index entries
func (e *Engine) collectFileIndexEntries(path string, openedPaths map[string]bool, entries *[]string) {
	node, err := e.Repo.LoadNode(path)
	if err != nil {
		return
	}

	// Build entry: | Path | Description | Summary | Open | Len |
	isOpen := "no"
	if openedPaths[path] {
		isOpen = "yes"
	}

	// Truncate description and summary for compact display
	desc := truncateString(node.Description, 50)
	summary := truncateString(node.Summary, 80)
	contentLen := len(node.Content)

	entry := fmt.Sprintf("| %s | %s | %s | %s | %d |", path, desc, summary, isOpen, contentLen)
	*entries = append(*entries, entry)

	// Recurse into children
	children, err := e.Repo.GetChildren(path)
	if err != nil {
		return
	}

	for _, childPath := range children {
		e.collectFileIndexEntries(childPath, openedPaths, entries)
	}
}

// truncateString truncates a string to maxLen and adds ellipsis if needed
func truncateString(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "|", "/") // Escape pipe for markdown table
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// getOpenedNodePrompts returns prompts for opened nodes in deterministic order
func (e *Engine) getOpenedNodePrompts(session *model.Session) []string {
	if len(session.NodeDigests) == 0 {
		return nil
	}

	// Extract node paths from NodeDigests
	nodePaths := make([]string, 0, len(session.NodeDigests))
	for _, digest := range session.NodeDigests {
		nodePaths = append(nodePaths, digest.Path)
	}

	// Sort paths in tree order (by depth, then lexicographically)
	// This ensures consistent ordering for AI prompt caching
	sort.Slice(nodePaths, func(i, j int) bool {
		depthI := strings.Count(nodePaths[i], "/")
		depthJ := strings.Count(nodePaths[j], "/")
		if depthI != depthJ {
			return depthI < depthJ
		}
		return nodePaths[i] < nodePaths[j]
	})

	// Build prompts array - one per node
	var prompts []string
	for _, path := range nodePaths {
		node, err := e.Repo.LoadNode(path)
		if err != nil {
			continue // Skip nodes that can't be loaded
		}

		// Add node content if available
		if node.Content != "" {
			// Always include path as header, optionally with title
			var header string
			if node.Title != "" {
				header = fmt.Sprintf("# %s\n**Path:** `%s`\n\n", node.Title, path)
			} else {
				header = fmt.Sprintf("**Path:** `%s`\n\n", path)
			}

			prompts = append(prompts, header+node.Content)
		}
	}

	return prompts
}

// GetTools returns tools calculated from the session's opened nodes
// TEMPORARY: For testing and v1, returns ALL registered tools without needing to open nodes
func (e *Engine) GetTools(session *model.Session) []openai.Tool {
	// TEMPORARY: Load all tools from all nodes for testing/v1
	// TODO: Revert to session-based tool loading after testing
	registry := model.NewToolRegistry(model.MergeStrategyOverride)

	allTools, err := e.Repo.LoadAllTools()
	if err == nil {
		registry.AddTools(allTools)
	} else {
		// Fallback to original behavior if loading all tools fails
		for _, digest := range session.NodeDigests {
			node, err := e.Repo.LoadNode(digest.Path)
			if err != nil {
				continue // Skip nodes that can't be loaded
			}
			registry.AddTools(node.Tools)
		}
	}

	// Convert to openai.Tool format
	accumulatedTools := registry.GetTools()
	tools := make([]openai.Tool, 0, len(accumulatedTools))
	for _, tool := range accumulatedTools {
		if tool.Status != model.ToolStatusActive {
			continue
		}
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return tools
}

// getConversationStateFromSession retrieves conversation state from a session object
func getConversationStateFromSession(session *model.Session) *model.ConversationState {
	if session.ConversationState == nil {
		session.ConversationState = model.NewConversationState()
	}
	return session.ConversationState
}

// setConversationStateToSession stores conversation state in a session object
func setConversationStateToSession(session *model.Session, state *model.ConversationState) {
	state.LastActivity = time.Now()
	session.ConversationState = state
}

// getConversationState retrieves conversation state from session
func (e *Engine) getConversationState(sessionID string) *model.ConversationState {
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return model.NewConversationState()
	}
	return getConversationStateFromSession(session)
}

// setConversationState stores conversation state in session
func (e *Engine) setConversationState(sessionID string, state *model.ConversationState) error {
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return err
	}
	setConversationStateToSession(session, state)
	return e.Sessions.Put(session)
}

// getMessages retrieves messages from session
func (e *Engine) getMessages(sessionID string) []openai.ChatCompletionMessage {
	state := e.getConversationState(sessionID)
	messages := make([]openai.ChatCompletionMessage, len(state.Msgs))
	copy(messages, state.Msgs)
	return messages
}

// appendMessages adds messages to session
func (e *Engine) appendMessages(sessionID string, messages []openai.ChatCompletionMessage) error {
	state := e.getConversationState(sessionID)
	state.Msgs = append(state.Msgs, messages...)
	return e.setConversationState(sessionID, state)
}

// queueMessage adds a message to the queue
func (e *Engine) queueMessage(sessionID string, text string) error {
	state := e.getConversationState(sessionID)
	state.Queue = append(state.Queue, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: text,
	})
	return e.setConversationState(sessionID, state)
}

// processQueuedMessages moves queued messages to session messages
// This should be called after tool processing to ensure queued requests
// are added to session at the earliest opportunity
func (e *Engine) processQueuedMessages(sessionID string) error {
	state := e.getConversationState(sessionID)
	if len(state.Queue) == 0 {
		return nil
	}

	// Move queued messages to session messages
	log.Log.Infof("Processing %d queued messages for session %s", len(state.Queue), sessionID)
	state.Msgs = append(state.Msgs, state.Queue...)
	state.Queue = []openai.ChatCompletionMessage{} // Clear the queue

	return e.setConversationState(sessionID, state)
}

// removeFunctionCalls removes function/tool call messages
func (e *Engine) removeFunctionCalls(sessionID string) error {
	state := e.getConversationState(sessionID)
	msgs := []openai.ChatCompletionMessage{}
	for _, msg := range state.Msgs {
		if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 || msg.FunctionCall != nil {
			continue
		}
		if msg.Role == openai.ChatMessageRoleAssistant || msg.Role == openai.ChatMessageRoleUser {
			msgs = append(msgs, msg)
		}
	}
	state.Msgs = msgs
	return e.setConversationState(sessionID, state)
}

// generateResultID generates a unique ID for tool results
// Format: result_SESSION-ID_TIMESTAMP_RANDOM
func generateResultID(sessionID string) string {
	return fmt.Sprintf("result_%s_%d_%s", sessionID, time.Now().UnixNano(), randomString(6))
}

// parseResultID extracts sessionID from resultID
// Returns sessionID and the original resultID
func parseResultID(resultID string) (sessionID string, ok bool) {
	// Format: result_SESSION-ID_TIMESTAMP_RANDOM
	parts := strings.Split(resultID, "_")
	if len(parts) < 4 || parts[0] != "result" {
		return "", false
	}
	// SessionID is the second part
	return parts[1], true
}

// randomString generates a random string of given length
func randomString(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		time.Sleep(1 * time.Nanosecond) // Ensure different values
	}
	return string(b)
}

// truncateForLog truncates a string for logging purposes
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// saveToolResult stores a tool result and returns the result ID
func (e *Engine) saveToolResult(sessionID string, result string) string {
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return ""
	}
	if session.ToolResults == nil {
		session.ToolResults = make(map[string]string)
	}
	resultID := generateResultID(sessionID)
	session.ToolResults[resultID] = result
	e.Sessions.Put(session)
	return resultID
}

// GetToolResult retrieves a stored tool result by ID
func (e *Engine) GetToolResult(sessionID string, resultID string) (string, bool) {
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return "", false
	}
	if session.ToolResults == nil {
		return "", false
	}
	result, ok := session.ToolResults[resultID]
	return result, ok
}

// processToolResult checks if result exceeds max length and returns truncated message if needed
func (e *Engine) processToolResult(sessionID string, result string) string {
	maxLen := e.llmConfig.MaxToolResultLength
	if maxLen <= 0 {
		maxLen = 250 // Default
	}

	if len(result) <= maxLen {
		return result
	}

	// Store full result and return truncated message
	resultID := e.saveToolResult(sessionID, result)
	return fmt.Sprintf("Tool result exceeds %d characters (exact: %d characters). To retrieve specific information from this result, use the `collect_result` tool with result_id=\"%s\" and specify what information you need.",
		maxLen, len(result), resultID)
}

// CollectResultByID uses a separate LLM to extract specific information from a stored tool result
// It extracts sessionID from the resultID automatically
func (e *Engine) CollectResultByID(ctx context.Context, resultID string, query string) (string, error) {
	// Extract sessionID from resultID
	sessionID, ok := parseResultID(resultID)
	if !ok {
		return "", fmt.Errorf("invalid result_id format: '%s'", resultID)
	}
	return e.CollectResult(ctx, sessionID, resultID, query)
}

// CollectResult uses a separate LLM to extract specific information from a stored tool result
func (e *Engine) CollectResult(ctx context.Context, sessionID string, resultID string, query string) (string, error) {
	// Get the stored result
	fullResult, ok := e.GetToolResult(sessionID, resultID)
	if !ok {
		return "", fmt.Errorf("result with ID '%s' not found in session '%s'", resultID, sessionID)
	}

	// Determine which model to use
	modelName := e.llmConfig.CollectResultModel
	if modelName == "" {
		modelName = e.llmConfig.Model
	}
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}

	// Determine max response length
	maxLen := e.llmConfig.MaxToolResultLength
	if maxLen <= 0 {
		maxLen = 250 // Default
	}

	// Build a simple prompt for extraction
	systemPrompt := fmt.Sprintf(`You are a helpful assistant that extracts specific information from data.
Given a large data output and a user query, extract only the relevant information that answers the query.
Be concise and direct in your response. Only return the extracted information, no explanations.
Your response must not exceed %d characters.`, maxLen)

	userPrompt := fmt.Sprintf(`Data:
	%s

	Query: %s

	Extract the relevant information from the data that answers the query:`, fullResult, query)

	// Get userID from session and add to context for LLM call
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get session: %w", err)
	}
	if session.UserID != "" {
		ctx = model.WithUserID(ctx, session.UserID)
	} else {
		log.Log.Warnf("[Engine] ⚠️  Session has no UserID | SessionID: %s", sessionID)
	}

	// Make LLM call
	resp, err := e.llmClient.CreateChatCompletion(
		ctx,
		openai.ChatCompletionRequest{
			Model: modelName,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
				{Role: openai.ChatMessageRoleUser, Content: userPrompt},
			},
		},
	)

	if err != nil {
		return "", formatLLMError(err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return resp.Choices[0].Message.Content, nil
}

// GetLLMClient returns the LLM client for external use (e.g., by llmutils)
func (e *Engine) GetLLMClient() *openai.Client {
	return e.llmClient
}

// GetLLMConfig returns the LLM configuration
func (e *Engine) GetLLMConfig() LLMConfig {
	return e.llmConfig
}

// formatLLMError formats OpenAI API errors with detailed information
func formatLLMError(err error) error {
	if err == nil {
		return nil
	}

	// Check if it's an OpenAI APIError
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		// Format error with status code and message
		if apiErr.Message != "" {
			return fmt.Errorf("LLM request failed: error, status code: %d, message: %s", apiErr.HTTPStatusCode, apiErr.Message)
		}
		return fmt.Errorf("LLM request failed: error, status code: %d", apiErr.HTTPStatusCode)
	}

	// For other errors, return as-is with prefix
	return fmt.Errorf("LLM request failed: %w", err)
}

// processChatRequest processes an LLM chat request with support for tool calls and memory management.
// It handles the full flow including:
// - Building the request with system prompt and memory
// - Making the OpenAI API call
// - Handling tool calls recursively
// - Managing memory state
//
// Returns the text response and total token usage.
func (e *Engine) processChatRequest(
	ctx context.Context,
	sessionID string,
) (string, int, error) {
	// Get session for system prompt and tools
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get session: %w", err)
	}

	// Get system prompts and tools from session
	systemPrompts := e.GetSystemPrompts(session)
	openaiTools := e.GetTools(session)

	// Set default model
	modelName := e.llmConfig.Model
	if modelName == "" {
		modelName = "gpt-4o-mini"
	}

	// Update session model if different from stored model
	if session.Model != modelName {
		session.Model = modelName
		// Save session to persist model name
		if err := e.Sessions.Put(session); err != nil {
			log.Log.Warnf("[Engine] ⚠️  Failed to update session model | SessionID: %s | Error: %v", sessionID, err)
		}
	}

	// Build request messages with system prompts (one message per node for AI caching)
	reqMessages := make([]openai.ChatCompletionMessage, 0)
	for _, prompt := range systemPrompts {
		if prompt != "" {
			reqMessages = append(reqMessages, openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleSystem,
				Content: prompt,
			})
		}
	}

	// Add memory messages
	reqMessages = append(reqMessages, e.getMessages(sessionID)...)

	// Ensure user_id is in context
	if session.UserID != "" {
		ctx = model.WithUserID(ctx, session.UserID)
	}

	log.Log.Infof("LLM request: system_prompts=%d, tools=%d, messages=%d",
		len(systemPrompts), len(openaiTools), len(reqMessages))

	// Make OpenAI API call
	request := openai.ChatCompletionRequest{
		Model:    modelName,
		Messages: reqMessages,
		Tools:    openaiTools,
	}
	resp, err := e.llmClient.CreateChatCompletion(ctx, request)

	if err != nil {
		return "", 0, formatLLMError(err)
	}

	if len(resp.Choices) == 0 {
		return "", resp.Usage.TotalTokens, fmt.Errorf("no choices in LLM response")
	}

	choice := resp.Choices[0]
	tokenUsage := resp.Usage.TotalTokens

	// Save message to database
	e.saveMessage(session, request, resp, choice)

	// Handle tool calls
	if choice.FinishReason == openai.FinishReasonToolCalls {
		if e.Executor == nil {
			return "", tokenUsage, fmt.Errorf("tool calls received but no executor provided")
		}

		// Add assistant message with tool calls to memory
		e.appendMessages(sessionID, []openai.ChatCompletionMessage{
			{
				Role:      openai.ChatMessageRoleAssistant,
				ToolCalls: choice.Message.ToolCalls,
			},
		})

		// Execute tools and collect results
		toolResults := make([]openai.ChatCompletionMessage, 0, len(choice.Message.ToolCalls))
		for _, toolCall := range choice.Message.ToolCalls {
			// Parse arguments
			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				args = make(map[string]interface{})
			}

			// Inject context into args for tools that need user/session context
			// This allows tools to access the current user/session without exposing it in the AI schema
			args["__user_id__"] = session.UserID
			args["__session_id__"] = session.SessionID

			// Log tool call from LLM
			log.Log.Infof("LLM tool call: name=%s, args=%s", toolCall.Function.Name, toolCall.Function.Arguments)

			// Execute tool
			result, err := e.Executor(toolCall.Function.Name, args)
			if err != nil {
				result = fmt.Sprintf("Error executing tool %s: %v", toolCall.Function.Name, err)
				log.Log.Infof("Tool execution error: name=%s, error=%v", toolCall.Function.Name, err)
			} else {
				log.Log.Infof("Tool execution result: name=%s, result_len=%d, result=%s", toolCall.Function.Name, len(result), truncateForLog(result, 500))
			}

			// Process tool result (truncate if too long)
			// Skip truncation for collect_result to avoid infinite loop
			var processedResult string
			if toolCall.Function.Name == "collect_result" {
				processedResult = result
			} else {
				processedResult = e.processToolResult(sessionID, result)
			}

			// Add tool result to memory
			e.appendMessages(sessionID, []openai.ChatCompletionMessage{
				{
					Role:       openai.ChatMessageRoleTool,
					Content:    processedResult,
					Name:       toolCall.Function.Name,
					ToolCallID: toolCall.ID,
				},
			})

			// Also collect for recursive call
			toolResults = append(toolResults, openai.ChatCompletionMessage{
				Role:       openai.ChatMessageRoleTool,
				Content:    processedResult,
				Name:       toolCall.Function.Name,
				ToolCallID: toolCall.ID,
			})
		}

		// Process queued messages immediately after tool execution
		// This ensures queued requests are added to session at the earliest opportunity
		if err := e.processQueuedMessages(sessionID); err != nil {
			log.Log.Warnf("Failed to process queued messages: %v", err)
		}

		// Recursively call again to process tool results
		recursiveResponse, recursiveTokenUsage, err := e.processChatRequest(ctx, sessionID)
		if err != nil {
			return recursiveResponse, tokenUsage + recursiveTokenUsage, err
		}
		return recursiveResponse, tokenUsage + recursiveTokenUsage, nil
	}

	// Handle text response
	textResponse := choice.Message.Content
	e.appendMessages(sessionID, []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleAssistant,
			Content: textResponse,
		},
	})

	return textResponse, tokenUsage, nil
}

// saveMessage saves a message to the database
func (e *Engine) saveMessage(
	session *model.Session,
	request openai.ChatCompletionRequest,
	response openai.ChatCompletionResponse,
	choice openai.ChatCompletionChoice,
) {
	// Get user message content
	content := choice.Message.Content
	if content == "" && len(choice.Message.ToolCalls) > 0 {
		content = fmt.Sprintf("[Tool Calls: %d]", len(choice.Message.ToolCalls))
	}

	// Create message record
	msg := model.NewMessage(
		session.UserID,
		session.SessionID,
		openai.ChatMessageRoleAssistant,
		content,
		request,
		response,
		choice,
	)

	// Try to save to database if store supports it
	if sqliteStore, ok := e.Sessions.(interface {
		PutMessage(*model.Message) error
	}); ok {
		if err := sqliteStore.PutMessage(msg); err != nil {
			log.Log.Warnf("[Engine] ⚠️  Failed to save message | SessionID: %s | Error: %v", session.SessionID, err)
		} else {
			log.Log.Infof("[Engine] 💾 Message saved | MessageID: %s | Model: %s | Tokens: %d", msg.MessageID, msg.Model, msg.TotalTokens)
		}
	}
}
