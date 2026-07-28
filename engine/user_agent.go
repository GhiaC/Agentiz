package engine

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/config"
	"github.com/ghiac/agentize/filestore"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

//go:embed user_agent.md
var basePrompt string

// Global scheduler once to ensure scheduler starts only once per session store
var schedulerOnce sync.Once
var schedulerOnceMap = make(map[store.Store]*sync.Once)
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

	// BackupProviders is a chain of backup LLM providers tried in order BEFORE the
	// default OpenAI client. Each entry pairs a Provider with a Model name.
	// On error or empty response from one provider, the next is tried.
	// After all backups fail, falls back to the default OpenAI client transparently.
	BackupProviders []BackupLLM

	// BackupDisabled if true, skips all backup providers and goes straight to the default LLM.
	BackupDisabled bool

	// SchedulerDisableLogs if true, SessionScheduler does not emit any logs (overrides config from env)
	SchedulerDisableLogs bool
	// SummaryModel overrides the scheduler summarization model (from config/env) when non-empty
	SummaryModel string
}

// ToolExecutor executes a tool call and returns the result
type ToolExecutor func(toolName string, args map[string]interface{}) (string, error)

// Engine orchestrates session management, tool execution, and LLM interaction.
// It intentionally exposes only the operations that are consumed by InfraAgent.
// Engine uses SessionStore for all state management, including conversation history.
type Engine struct {
	Repo      *fsrepo.NodeRepository
	Sessions  store.Store
	Functions *model.FunctionRegistry
	Executor  ToolExecutor
	// Files is the pluggable byte storage for user files. When nil, the
	// manage_files tool and user-file recording are disabled.
	Files filestore.FileStore
	// ImageEditor, when set, performs real image edits for the manage_files
	// edit_image action. Pluggable so the app can wire any image model.
	ImageEditor ImageEditorFunc
	// LLM client and configuration
	llmClient *openai.Client
	llmConfig LLMConfig
	// Database readiness flag
	dbReady   bool
	dbReadyMu sync.RWMutex
	// Scheduler for session summarization
	scheduler   *SessionScheduler
	schedulerMu sync.RWMutex
	// Persistent scheduler for general recurring agent tasks.
	taskScheduler   *TaskScheduler
	taskSchedulerMu sync.RWMutex

	// Per-session mutex for serializing message processing
	// Ensures only one message is processed at a time per session to prevent
	// race conditions on sequence number generation and session updates
	sessionMutexes   map[string]*sync.Mutex
	sessionMutexesMu sync.RWMutex

	// Per-session progress + queue: check before locking so we can return immediately
	// when already in progress and queue the message instead of blocking
	sessionProgress *ProgressGuard

	// Backup LLM chain (initialized from LLMConfig.BackupProviders)
	backups *BackupChain

	// Callback for billing/usage metering (optional, set by application)
	Callback Callback

	// ToolApprovalManager, when set, requires an explicit human approval before
	// every tool invocation. The manager is UI-agnostic; review.Manager is the
	// standard durable implementation.
	ToolApprovalManager ToolApprovalManager

	// userModelOverrides maps userID -> model id, set at runtime via SetUserModel.
	// A non-empty override takes precedence over llmConfig.Model for that user's
	// requests, enabling per-user runtime model switching without reconfiguring the
	// engine. Empty/absent → engine default model. See user_model.go.
	userModelOverrides   map[string]string
	userModelOverridesMu sync.RWMutex
}

// SetToolApprovalManager enables approval gating for every tool call. Passing
// nil disables the gate.
func (e *Engine) SetToolApprovalManager(manager ToolApprovalManager) {
	e.ToolApprovalManager = manager
}

// Init initializes the engine by loading the root node and verifying Sessions store is ready.
// This must be called before ProcessMessage to ensure the database is fully loaded.
func (e *Engine) Init() error {
	e.dbReadyMu.Lock()
	defer e.dbReadyMu.Unlock()

	// Initialize session mutexes map if nil
	if e.sessionMutexes == nil {
		e.sessionMutexes = make(map[string]*sync.Mutex)
	}
	if e.sessionProgress == nil {
		e.sessionProgress = NewProgressGuard()
	}

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

// getSessionMutex returns or creates a mutex for a specific session
// This ensures only one message is processed at a time per session
func (e *Engine) getSessionMutex(sessionID string) *sync.Mutex {
	// First try with read lock (fast path for existing mutexes)
	e.sessionMutexesMu.RLock()
	if e.sessionMutexes != nil {
		if mu, exists := e.sessionMutexes[sessionID]; exists {
			e.sessionMutexesMu.RUnlock()
			return mu
		}
	}
	e.sessionMutexesMu.RUnlock()

	// Need to create new mutex, acquire write lock
	e.sessionMutexesMu.Lock()
	defer e.sessionMutexesMu.Unlock()

	// Double-check after acquiring write lock
	if e.sessionMutexes == nil {
		e.sessionMutexes = make(map[string]*sync.Mutex)
	}

	if mu, exists := e.sessionMutexes[sessionID]; exists {
		return mu
	}

	mu := &sync.Mutex{}
	e.sessionMutexes[sessionID] = mu
	return mu
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
	e.RegisterManageFilesTool()
	e.RegisterTextTools()
	e.RegisterTaskSchedulerTool()
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
	e.InitializeTaskScheduler()
	if e.taskScheduler != nil {
		e.taskScheduler.Start(context.Background())
	}

	// Initialize backup chain from configured providers
	// Note: BackupDisabled only affects Engine's direct LLM calls (callLLM)
	// Scheduler ALWAYS uses backup chain for cost-efficient summarization
	e.backups = NewBackupChain(config.BackupProviders)

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
			// Start scheduler in background goroutine to avoid blocking initialization
			go func() {
				ctx := context.Background()
				e.schedulerMu.Lock()
				if e.scheduler == nil {
					e.schedulerMu.Unlock()
					if err := e.startScheduler(ctx, client); err != nil {
						log.Log.Warnf("[Engine] ⚠️  Failed to start scheduler: %v", err)
					}
				} else {
					e.schedulerMu.Unlock()
				}
			}()
		})
	}

	return nil
}

const backupCooldownDuration = 1 * time.Second

// callLLM tries the backup LLM providers in order (if configured and not disabled), then falls back
// to the default OpenAI client. This is the single entry point for all LLM calls
// in the Engine, ensuring consistent fallback behaviour.
func (e *Engine) callLLM(ctx context.Context, model string, messages []openai.ChatCompletionMessage, tools []openai.Tool) (openai.ChatCompletionResponse, error) {
	// Try backup providers chain first (only if not disabled)
	if !e.llmConfig.BackupDisabled {
		if resp, ok := e.backups.TryBackup(ctx, messages, tools, "Engine"); ok {
			return resp, nil
		}
	}

	// Default: OpenAI client
	systemPromptLen := 0
	for _, m := range messages {
		if m.Role == openai.ChatMessageRoleSystem {
			systemPromptLen += len(m.Content)
		}
	}
	log.Log.Infof("[Engine] 🔵 DEFAULT LLM >> Using OpenAI | Model: %s | Messages: %d | Tools: %d | system_prompt_len=%d", model, len(messages), len(tools), systemPromptLen)
	request := openai.ChatCompletionRequest{
		Model:    model,
		Messages: messages,
		Tools:    tools,
	}
	resp, err := e.llmClient.CreateChatCompletion(ctx, request)
	if err != nil {
		LogLLMError("Engine", model, err)
	} else if resp.Usage.TotalTokens > 0 {
		cacheTokens := 0
		if resp.Usage.PromptTokensDetails != nil {
			cacheTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		log.Log.Infof("[Engine] 📊 TOKEN USAGE >> Model: %s | prompt=%d | completion=%d | total=%d | cache=%d (input=prompt, output=completion, total=total, cache=cache)",
			model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, cacheTokens)
	}
	return resp, err
}

// startScheduler starts the session scheduler
func (e *Engine) startScheduler(ctx context.Context, llmClient *openai.Client) error {
	// Load scheduler config from environment
	cfg, err := config.Load()
	var schedulerConfig config.SchedulerConfig
	if err != nil {
		log.Log.Warnf("[Engine] ⚠️  Failed to load config, using defaults: %v", err)
		// Use default config (enabled by default)
		schedulerConfig = config.SchedulerConfig{
			Enabled:                     true, // Enabled by default
			CheckInterval:               5 * time.Minute,
			FirstSummarizationThreshold: 5,
			SubsequentMessageThreshold:  25,
			SubsequentTimeThreshold:     1 * time.Hour,
			LastActivityThreshold:       1 * time.Hour,
			SummaryModel:                "openai/gpt-5-nano",
			DisableLogs:                 e.llmConfig.SchedulerDisableLogs,
		}
	} else {
		schedulerConfig = cfg.Scheduler
		// Scheduler is enabled by default, only disable if explicitly set to false
		if !schedulerConfig.Enabled {
			log.Log.Infof("[Engine] ⏸️  Scheduler is disabled via config")
			return nil
		}
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
	if schedulerConfig.FirstSummarizationThreshold > 0 {
		schedulerConfigStruct.FirstSummarizationThreshold = schedulerConfig.FirstSummarizationThreshold
	}
	if schedulerConfig.SubsequentMessageThreshold > 0 {
		schedulerConfigStruct.SubsequentMessageThreshold = schedulerConfig.SubsequentMessageThreshold
	}
	if schedulerConfig.SubsequentTimeThreshold > 0 {
		schedulerConfigStruct.SubsequentTimeThreshold = schedulerConfig.SubsequentTimeThreshold
	}
	if schedulerConfig.LastActivityThreshold > 0 {
		schedulerConfigStruct.LastActivityThreshold = schedulerConfig.LastActivityThreshold
	}
	if schedulerConfig.SummaryModel != "" {
		schedulerConfigStruct.SummaryModel = schedulerConfig.SummaryModel
	}
	if e.llmConfig.SummaryModel != "" {
		schedulerConfigStruct.SummaryModel = e.llmConfig.SummaryModel
	}
	// DisableLogs: from config (env) or from LLMConfig (programmatic, e.g. TradeAgent yaml)
	schedulerConfigStruct.DisableLogs = schedulerConfig.DisableLogs || e.llmConfig.SchedulerDisableLogs

	// Create and start scheduler
	scheduler := NewSessionScheduler(sessionHandler, llmClient, schedulerConfigStruct)

	// Set backup chain for scheduler - always enabled for scheduler (ignores BackupDisabled)
	// This allows scheduler to use cheaper models (OSS 120B) for summarization
	if e.backups != nil {
		scheduler.SetBackupChain(e.backups)
		log.Log.Infof("[Engine] 🔗 Scheduler using backup chain with %d providers", len(e.backups.providers))
	}

	e.schedulerMu.Lock()
	e.scheduler = scheduler
	e.schedulerMu.Unlock()
	// Start scheduler in background goroutine to avoid blocking initialization
	go scheduler.Start(ctx)

	log.Log.Infof("[Engine] ✅ Session scheduler started | CheckInterval: %v | FirstThreshold: %d msgs | SubsequentThreshold: %d msgs + %v | SummaryModel: %s",
		schedulerConfigStruct.CheckInterval, schedulerConfigStruct.FirstSummarizationThreshold, schedulerConfigStruct.SubsequentMessageThreshold, schedulerConfigStruct.SubsequentTimeThreshold, schedulerConfigStruct.SummaryModel)

	return nil
}

// GetSchedulerMessageThreshold returns the message threshold from the scheduler if available
func (e *Engine) GetSchedulerMessageThreshold() int {
	e.schedulerMu.RLock()
	defer e.schedulerMu.RUnlock()
	if e.scheduler != nil {
		return e.scheduler.GetMessageThreshold()
	}
	// Fallback to default (don't call config.Load() to avoid potential issues)
	return 5
}

// GetSchedulerConfig returns the full scheduler configuration if available
// Returns nil if scheduler is not initialized
func (e *Engine) GetSchedulerConfig() *SessionSchedulerConfig {
	e.schedulerMu.RLock()
	defer e.schedulerMu.RUnlock()
	if e.scheduler != nil {
		config := e.scheduler.GetConfig()
		return &config
	}
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
// Uses store.GetNextSessionSeq for proper sequential ID generation
func (e *Engine) CreateSession(userID string) (*model.Session, error) {
	// Get next sequence number from store (default to AgentTypeLow for Engine sessions)
	agentType := model.AgentTypeLow
	seq, err := e.Sessions.GetNextSessionSeq(userID, agentType)
	if err != nil {
		return nil, fmt.Errorf("failed to get next session seq: %w", err)
	}

	// Create session with sequence-based ID
	sessionID := model.GenerateSessionID(userID, agentType, seq)
	session := model.NewSessionWithID(userID, sessionID, agentType)

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
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return err
	}
	session.InProgress = inProgress
	return e.Sessions.Put(session)
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
			{
				openedFiles, err := e.Sessions.GetCurrentlyOpenedFilesBySession(sessionID)
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
						openedFile := model.NewOpenedFile(session, path, fileName)
						if err := e.Sessions.AddOpenedFile(openedFile); err != nil {
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
		fileName := path
		if node.Title != "" {
			fileName = node.Title
		}
		openedFile := model.NewOpenedFile(session, path, fileName)
		if err := e.Sessions.AddOpenedFile(openedFile); err != nil {
			log.Log.Warnf("[Engine] ⚠️  Failed to record opened file | SessionID: %s | Path: %s | Error: %v", sessionID, path, err)
		} else {
			log.Log.Infof("[Engine] 📂 File opened recorded | SessionID: %s | Path: %s | FileID: %s", sessionID, path, openedFile.FileID)
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
	if err := e.Sessions.CloseOpenedFile(sessionID, path); err != nil {
		log.Log.Warnf("[Engine] ⚠️  Failed to record closed file | SessionID: %s | Path: %s | Error: %v", sessionID, path, err)
	} else {
		log.Log.Infof("[Engine] 📂 File closed recorded | SessionID: %s | Path: %s", sessionID, path)
	}

	return nil
}

// ProcessMessage routes a user message through the LLM workflow and tool executor.
// It checks in-progress (without locking) and queues if busy; otherwise holds
// per-session mutex and processes, then drains the queue.
func (e *Engine) ProcessMessage(
	ctx context.Context,
	sessionID string,
	userMessage string,
) (string, int, error) {
	// Defensive: ensure progress guard is initialized (e.g. if Init() was not called)
	e.dbReadyMu.Lock()
	if e.sessionProgress == nil {
		e.sessionProgress = NewProgressGuard()
	}
	e.dbReadyMu.Unlock()
	// Check if already processing - queue if busy
	if e.sessionProgress.TryQueue(sessionID, userMessage) {
		metrics.MessageQueued("agent")
		return "⏳ Processing previous request... Please wait. 📋 Your message was queued and will be answered in order.", 0, nil
	}

	// Lock session mutex
	sessionMu := e.getSessionMutex(sessionID)
	sessionMu.Lock()
	defer sessionMu.Unlock()

	return e.processMessageLocked(ctx, sessionID, userMessage)
}

// ProcessScheduledMessage runs a background scheduled prompt on the owning
// session and waits for any foreground turn instead of returning the normal
// "queued" acknowledgement. This guarantees the scheduler captures the actual
// task output and can pass that output to its conclusion model.
func (e *Engine) ProcessScheduledMessage(
	ctx context.Context,
	sessionID string,
	userMessage string,
) (string, int, error) {
	e.dbReadyMu.Lock()
	if e.sessionProgress == nil {
		e.sessionProgress = NewProgressGuard()
	}
	e.dbReadyMu.Unlock()

	sessionMu := e.getSessionMutex(sessionID)
	sessionMu.Lock()
	defer sessionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	return e.processMessageLocked(ctx, sessionID, userMessage)
}

// processMessageLocked processes one message and drains the foreground queue.
// Caller must hold the per-session mutex.
func (e *Engine) processMessageLocked(
	ctx context.Context,
	sessionID string,
	userMessage string,
) (string, int, error) {
	e.sessionProgress.SetInProgress(sessionID, true)
	defer e.sessionProgress.SetInProgress(sessionID, false)

	log.Log.Infof("[Engine] 🚀 ProcessMessage | SessionID: %s | MsgLen: %d", sessionID, len(userMessage))

	// Validate prerequisites
	if !e.IsDBReady() {
		return "", 0, errors.New("database is not ready. Call Init() first")
	}
	if e.llmClient == nil {
		return "", 0, errors.New("LLM client is not configured. Call UseLLMConfig first")
	}

	// Get session
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get session: %w", err)
	}

	log.Log.Infof("[Engine] 🔍 Session loaded | SessionID: %s | UserID: %s | Messages: %d",
		sessionID, session.UserID, len(session.Msgs))

	// Clean old function calls if session is stale (> 2 hours)
	if session.UpdatedAt.Before(time.Now().Add(-2 * time.Hour)) {
		if err := e.removeFunctionCalls(sessionID); err != nil {
			log.Log.Warnf("[Engine] ⚠️  Failed to clean function calls | Error: %v", err)
		}
	}

	// Process the message
	metrics.MessageStart("agent")
	procStart := time.Now()
	response, tokens, err := e.processOneMessageBody(ctx, sessionID, userMessage)
	metrics.MessageDone("agent", metrics.Status(err), time.Since(procStart))
	if err != nil {
		log.Log.Errorf("[Engine] ❌ Processing failed | SessionID: %s | Error: %v", sessionID, err)
		return "", tokens, err
	}

	// Process any queued messages
	for _, m := range e.sessionProgress.DrainQueue(sessionID) {
		if _, _, qErr := e.processOneMessageBody(ctx, sessionID, m); qErr != nil {
			log.Log.Warnf("[Engine] ⚠️  Queued message failed | Error: %v", qErr)
		}
	}

	log.Log.Infof("[Engine] ✅ Done | SessionID: %s | ResponseLen: %d | Tokens: %d",
		sessionID, len(response), tokens)

	return response, tokens, nil
}

// processOneMessageBody appends the user message to session and runs the chat request.
// Caller must hold session mutex.
func (e *Engine) processOneMessageBody(ctx context.Context, sessionID string, userMessage string) (string, int, error) {
	// Add user message to session if not empty
	if len(userMessage) > 0 {
		session, err := e.Sessions.Get(sessionID)
		if err != nil {
			return "", 0, fmt.Errorf("failed to get session: %w", err)
		}

		// Append user message
		session.Msgs = append(session.Msgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: userMessage,
		})
		session.UpdatedAt = time.Now()

		// Generate the user-message ID BEFORE persisting the session, so the
		// bumped MessageSeq is included in the Put below. Otherwise the next
		// reload (processChatRequest) re-reads the pre-increment seq and the
		// assistant message gets the SAME MessageID, whose upsert/INSERT OR
		// REPLACE then overwrites this user-message row.
		userMsgID, userSeqID := session.GenerateMessageIDWithSeq()
		userMsg := model.NewUserMessage(userMsgID, userSeqID, session.UserID, sessionID, userMessage, model.ContentTypeText)

		// Save session
		if err := e.Sessions.Put(session); err != nil {
			return "", 0, fmt.Errorf("failed to save session: %w", err)
		}

		// Save user message to messages table
		if err := e.Sessions.PutMessage(userMsg); err != nil {
			log.Log.Warnf("[Engine] ⚠️  Failed to save user message | Error: %v", err)
		}
	}

	return e.processChatRequest(ctx, sessionID)
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
// 2. Session context - Summary and tags from previous conversations (if summarized)
// 3. File index - List of all knowledge files with metadata
// 4. Opened files - Content of currently opened nodes
//
// The order is deterministic to enable AI prompt caching.
func (e *Engine) GetSystemPrompts(session *model.Session) []string {
	var prompts []string

	// 1. Base prompt (engine.md)
	if basePrompt != "" {
		prompts = append(prompts, basePrompt)
	}

	// 2. Session context - Summary and tags from previous conversations
	// This provides context from archived messages that are no longer in the active conversation
	sessionContext := e.buildSessionContext(session)
	if sessionContext != "" {
		prompts = append(prompts, sessionContext)
	}

	// 3. File index - all files with metadata
	fileIndex := e.buildFileIndex(session)
	if fileIndex != "" {
		prompts = append(prompts, fileIndex)
	}

	// 4. Opened files content
	openedPrompts := e.getOpenedNodePrompts(session)
	prompts = append(prompts, openedPrompts...)

	return prompts
}

// buildSessionContext generates a context prompt from session summary and tags
// This is used to provide context from archived/summarized messages
func (e *Engine) buildSessionContext(session *model.Session) string {
	// Only include context if session has been summarized (has summary or tags)
	if session.Summary == "" && len(session.Tags) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Session Context\n\n")
	sb.WriteString("This is a continuation of a previous conversation. Here is the context from earlier messages:\n\n")

	if session.Summary != "" {
		sb.WriteString("## Summary of Previous Conversation\n")
		sb.WriteString(session.Summary)
		sb.WriteString("\n\n")
	}

	if len(session.Tags) > 0 {
		sb.WriteString("## Topics Discussed\n")
		sb.WriteString("Tags: ")
		sb.WriteString(strings.Join(session.Tags, ", "))
		sb.WriteString("\n")
	}

	return sb.String()
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

func truncateString(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "|", "/")
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
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
	// TODO(TD-2): revert to session-based tool loading after v1 testing (tracked in CHANGELOG.md → Tracked technical debt).
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

	// Expose the file-manager tool to the agent when a file store is configured.
	// This makes manage_files available without editing every node's tools.json.
	if e.Files != nil {
		tools = append(tools, ManageFilesToolDefinition())
	}

	// Expose the result-inspection tools whenever a session store is configured.
	// They let the agent pull back specific parts of oversized tool results that
	// were buffered on its session (see processToolResult / text_tools.go).
	if e.Sessions != nil {
		tools = append(tools, CollectResultToolDefinition(), InspectResultToolDefinition())
	}
	// Persistent recurring tasks are a built-in capability, so the LLM receives
	// this schema without requiring a repository tools.json entry.
	if e.GetTaskScheduler() != nil {
		tools = append(tools, TaskSchedulerToolDefinition())
	}
	return tools
}

// removeFunctionCalls removes function/tool call messages
func (e *Engine) removeFunctionCalls(sessionID string) error {
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return err
	}
	msgs := []openai.ChatCompletionMessage{}
	for _, msg := range session.Msgs {
		if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 || msg.FunctionCall != nil {
			continue
		}
		if msg.Role == openai.ChatMessageRoleAssistant || msg.Role == openai.ChatMessageRoleUser {
			msgs = append(msgs, msg)
		}
	}
	session.Msgs = msgs
	session.UpdatedAt = time.Now()
	return e.Sessions.Put(session)
}

// generateResultID generates a unique ID for tool results
// Format: r_<fullSessionID>_<unixTimestamp>
func generateResultID(sessionID string) string {
	return fmt.Sprintf("r_%s_%d", sessionID, time.Now().Unix())
}

// parseResultID extracts the full sessionID from a resultID.
// The timestamp is always the last underscore-separated segment, so everything
// between the leading "r_" and the final "_<timestamp>" is the sessionID
// (which itself may contain underscores).
func parseResultID(resultID string) (sessionID string, ok bool) {
	// New format: r_<sessionID>_<timestamp>
	if strings.HasPrefix(resultID, "r_") {
		lastUnderscore := strings.LastIndex(resultID, "_")
		// "r_" is at index 0-1, so the sessionID starts at index 2.
		// lastUnderscore must be beyond the "r_" prefix and the sessionID.
		if lastUnderscore > 2 {
			return resultID[2:lastUnderscore], true
		}
	}

	// Old format: result_<sessionID>_<timestamp>_<random>
	if strings.HasPrefix(resultID, "result_") {
		parts := strings.Split(resultID, "_")
		if len(parts) >= 4 {
			return parts[1], true
		}
	}

	return "", false
}

func randomString(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[rand.IntN(len(charset))]
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

// processToolResult checks whether result exceeds the max length and, if so,
// stores the full result ON THE PASSED SESSION (under a generated result ID) and
// returns a short message pointing the model at collect_result.
//
// It MUST store on the session object the caller already holds — not on a fresh
// clone fetched from the store — because the store returns a copy on every Get
// (DBStore.Get → Clone). The surrounding ProcessMessage loop persists the
// session once, after all tool calls, via a single Sessions.Put(session). A
// self-contained Get→modify→Put here would write a different clone that the
// loop's later Put(session) then overwrites, dropping ToolResults — which is
// exactly why collect_result used to fail with "result not found in session".
func (e *Engine) processToolResult(session *model.Session, result string) string {
	maxLen := e.llmConfig.MaxToolResultLength
	if maxLen <= 0 {
		maxLen = 250 // Default
	}

	if len(result) <= maxLen {
		return result
	}

	if session.ToolResults == nil {
		session.ToolResults = make(map[string]string)
	}
	resultID := generateResultID(session.SessionID)
	session.ToolResults[resultID] = result

	return fmt.Sprintf("Tool result exceeds %d characters (exact: %d characters). "+
		"The full output is buffered privately for you under result_id=\"%s\" (only you can access it). Retrieve what you need with:\n"+
		"- `inspect_result` (no LLM, fast): action=stats to size it, then head/tail (default 30 lines), slice (start/end line range), grep (regex, with ignore_case/invert/context/max_matches), unique, sort (desc/numeric), or count (matches of a query, or per-line frequency).\n"+
		"- `collect_result` (LLM extraction): pass a 'query' describing the specific information you need.",
		maxLen, len(result), resultID)
}

// CollectResultByID uses a separate LLM to extract specific information from a stored tool result
// It extracts sessionID from the resultID automatically.
//
// Deprecated (security): this trusts the sessionID embedded in a caller-supplied
// resultID, so it must NOT be used to dispatch a model's collect_result call —
// a model could pass a foreign result_id to read another user's buffer. The
// model-facing tool is registered via RegisterTextTools and enforces per-user
// ownership through getOwnedToolResult. Retained only for internal/host callers
// that already trust the resultID.
func (e *Engine) CollectResultByID(ctx context.Context, resultID string, query string) (string, error) {
	// Extract sessionID from resultID
	sessionID, ok := parseResultID(resultID)
	if !ok {
		return "", fmt.Errorf("invalid result_id format: '%s'", resultID)
	}
	return e.CollectResult(ctx, sessionID, resultID, query)
}

// CollectResult uses a separate LLM to extract specific information from a stored
// tool result. It trusts the caller-supplied sessionID (caller-controlled, not
// model-controlled). For the model-facing tool path use collectResultFunction,
// which enforces per-user ownership via getOwnedToolResult before extraction.
func (e *Engine) CollectResult(ctx context.Context, sessionID string, resultID string, query string) (string, error) {
	// Get the stored result
	fullResult, ok := e.GetToolResult(sessionID, resultID)
	if !ok {
		return "", fmt.Errorf("result with ID '%s' not found in session '%s'", resultID, sessionID)
	}

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

	return e.extractFromResult(ctx, fullResult, query)
}

// extractFromResult runs the helper-LLM extraction over an already-retrieved
// (ownership-checked) result. Callers are responsible for access control and
// for putting the owning userID on ctx (for metering).
func (e *Engine) extractFromResult(ctx context.Context, fullResult string, query string) (string, error) {
	// Determine which model to use
	modelName := e.llmConfig.CollectResultModel
	if modelName == "" {
		modelName = e.llmConfig.Model
	}
	if modelName == "" {
		modelName = "openai/gpt-5-nano"
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

	// Make LLM call (tries backup provider first, then falls back to OpenAI)
	msgs := []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: userPrompt},
	}
	resp, err := e.callLLM(ctx, modelName, msgs, nil)

	if err != nil {
		return "", FormatLLMError(err)
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

// FormatLLMError formats OpenAI API errors with detailed information
func FormatLLMError(err error) error {
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

// LogLLMError emits a single, detailed, greppable diagnostic line for any LLM
// call failure. Every line is tagged with the keyword LLMFAIL so the full
// picture of a failed request can be pulled with a single filter:
//
//	docker logs tradeagent 2>&1 | grep LLMFAIL
//
// component is the caller (e.g. "CoreHandler", "Engine", "Scheduler"); model is
// the requested model name.
func LogLLMError(component, model string, err error) {
	if err == nil {
		return
	}

	// RequestError: go-openai returns this when the HTTP response body cannot be
	// decoded as JSON (e.g. an empty / non-JSON body behind a 5xx gateway error).
	// This is the "unexpected end of JSON input" case — usually a transient
	// upstream/proxy failure where LiteLLM returned 503 with an empty body.
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) {
		body := strings.TrimSpace(string(reqErr.Body))
		underlying := ""
		if reqErr.Err != nil {
			underlying = reqErr.Err.Error()
		}
		log.Log.Errorf("[LLMFAIL] ❌ component=%s | kind=request_error | model=%s | http_status=%d (%s) | underlying=%q | body_len=%d | body_empty=%t | body=%q",
			component, model, reqErr.HTTPStatusCode, reqErr.HTTPStatus, underlying, len(reqErr.Body), len(body) == 0, body)
		return
	}

	// APIError: a well-formed OpenAI/LiteLLM error JSON (has a real message).
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		log.Log.Errorf("[LLMFAIL] ❌ component=%s | kind=api_error | model=%s | http_status=%d (%s) | type=%q | code=%v | message=%q",
			component, model, apiErr.HTTPStatusCode, apiErr.HTTPStatus, apiErr.Type, apiErr.Code, apiErr.Message)
		return
	}

	// Context cancellation / deadline (client-side timeout or user abort).
	if errors.Is(err, context.DeadlineExceeded) {
		log.Log.Errorf("[LLMFAIL] ❌ component=%s | kind=timeout | model=%s | err=%v", component, model, err)
		return
	}
	if errors.Is(err, context.Canceled) {
		log.Log.Errorf("[LLMFAIL] ❌ component=%s | kind=canceled | model=%s | err=%v", component, model, err)
		return
	}

	// Anything else: network refused, DNS failure, proxy down, etc.
	log.Log.Errorf("[LLMFAIL] ❌ component=%s | kind=other | model=%s | go_type=%T | err=%v", component, model, err, err)
}

// sanitizeOrphanedToolResults removes tool-result messages (role: "tool") whose
// ToolCallID does not match any tool call in the preceding assistant messages.
// This can happen when an older rolling-window split archived the assistant
// message that issued the call but kept the result, producing a history that
// LLMs reject with "No tool call found for function call output with call_id X".
func sanitizeOrphanedToolResults(msgs []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	// Collect every tool-call ID that appears in an assistant message.
	calledIDs := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			if tc.ID != "" {
				calledIDs[tc.ID] = true
			}
		}
	}

	out := make([]openai.ChatCompletionMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == openai.ChatMessageRoleTool && !calledIDs[m.ToolCallID] {
			log.Log.Warnf("[Engine] ⚠️  Dropping orphaned tool-result message | ToolCallID: %s", m.ToolCallID)
			continue
		}
		out = append(out, m)
	}
	return out
}

// processChatRequest processes an LLM chat request with support for tool calls.
// SIMPLIFIED: Uses a single session object and local messages list throughout the loop.
// Only saves to DB at key points (after tool calls, and at the end).
func (e *Engine) processChatRequest(
	ctx context.Context,
	sessionID string,
) (string, int, error) {
	const maxIterations = 10
	totalTokenUsage := 0

	// Load session once at the start
	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get session: %w", err)
	}

	// Get system prompts and tools (these don't change during the loop)
	systemPrompts := e.GetSystemPrompts(session)
	openaiTools := e.GetTools(session)

	// Set model: a per-user runtime override (SetUserModel) wins over the engine
	// default, enabling live per-user model switching without reconfiguring the
	// engine. Empty override → engine model → hard default. session.Model is kept in
	// sync with the effective model for display/logging/usage records.
	modelName := e.llmConfig.Model
	if ov := e.UserModelOverride(session.UserID); ov != "" {
		modelName = ov
	}
	if modelName == "" {
		modelName = "openai/gpt-5-nano"
	}
	if session.Model != modelName {
		session.Model = modelName
	}

	// Ensure user_id is in context
	if session.UserID != "" {
		ctx = model.WithUserID(ctx, session.UserID)
	}

	// Work with a local copy of messages - this is the single source of truth for this request.
	// Sanitize first to drop any orphaned tool-result messages left by an older rolling-window
	// split that landed between an assistant tool-call and its result.
	localMsgs := sanitizeOrphanedToolResults(session.Msgs)

	// pendingImages holds transient multimodal image messages injected by
	// manage_files "read" on an image. They are sent to the LLM for the rest of
	// this turn but never persisted to session.Msgs (avoids base64 history bloat).
	var pendingImages []openai.ChatCompletionMessage

	for i := 0; i < maxIterations; i++ {
		// Build request messages: system prompts + local messages + open images
		reqMessages := make([]openai.ChatCompletionMessage, 0, len(systemPrompts)+len(localMsgs)+len(pendingImages))
		for _, prompt := range systemPrompts {
			if prompt != "" {
				reqMessages = append(reqMessages, openai.ChatCompletionMessage{
					Role:    openai.ChatMessageRoleSystem,
					Content: prompt,
				})
			}
		}
		reqMessages = append(reqMessages, localMsgs...)
		reqMessages = append(reqMessages, pendingImages...)

		log.Log.Infof("[Engine] LLM request | iteration=%d/%d | messages=%d | tools=%d",
			i+1, maxIterations, len(reqMessages), len(openaiTools))

		NotifyStatus(ctx, session.UserID, sessionID, StatusThinking, "")

		// BeforeAction: check quota/credit before LLM call (block without consuming tokens)
		if e.Callback != nil {
			if cbErr := e.Callback.BeforeAction(ctx, &UsageEvent{
				UserID:    session.UserID,
				SessionID: sessionID,
				EventType: EventLLMCall,
				Name:      EventNameLLMCall,
				Model:     modelName,
			}); cbErr != nil {
				return cbErr.Error(), totalTokenUsage, nil
			}
		}

		// Call LLM
		llmStart := time.Now()
		resp, err := e.callLLM(ctx, modelName, reqMessages, openaiTools)
		llmDuration := time.Since(llmStart)
		if err != nil {
			metrics.LLMCall("agent", modelName, "error", llmDuration, 0, 0, 0)
			return "", totalTokenUsage, FormatLLMError(err)
		}

		agentCached := 0
		if resp.Usage.PromptTokensDetails != nil {
			agentCached = resp.Usage.PromptTokensDetails.CachedTokens
		}
		metrics.LLMCall("agent", modelName, "ok", llmDuration, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, agentCached)

		if len(resp.Choices) == 0 {
			return "", totalTokenUsage, fmt.Errorf("no choices in LLM response")
		}

		choice := resp.Choices[0]
		totalTokenUsage += resp.Usage.TotalTokens

		// Record usage callback
		if e.Callback != nil {
			ev := &UsageEvent{
				UserID:       session.UserID,
				SessionID:    sessionID,
				EventType:    EventLLMCall,
				Name:         EventNameLLMCall,
				Tokens:       resp.Usage.TotalTokens,
				InputTokens:  resp.Usage.PromptTokens,
				OutputTokens: resp.Usage.CompletionTokens,
				Model:        modelName,
				Duration:     llmDuration,
			}
			if resp.Usage.PromptTokensDetails != nil {
				ev.CachedInputTokens = resp.Usage.PromptTokensDetails.CachedTokens
			}
			e.Callback.AfterAction(ctx, ev)
		}

		// Save LLM message to DB
		request := openai.ChatCompletionRequest{Model: modelName, Messages: reqMessages, Tools: openaiTools}
		messageID := e.saveMessage(session, request, resp, choice)

		// Handle tool calls
		if choice.FinishReason == openai.FinishReasonToolCalls {
			if e.Executor == nil {
				return "", totalTokenUsage, fmt.Errorf("tool calls received but no executor provided")
			}

			// Add assistant message with tool calls to local messages
			localMsgs = append(localMsgs, openai.ChatCompletionMessage{
				Role:      openai.ChatMessageRoleAssistant,
				ToolCalls: choice.Message.ToolCalls,
			})

			// Execute each tool and add results to local messages
			for _, toolCall := range choice.Message.ToolCalls {
				result, inject := e.executeTool(ctx, session, messageID, toolCall)
				localMsgs = append(localMsgs, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    result,
					Name:       toolCall.Function.Name,
					ToolCallID: toolCall.ID,
				})
				if inject != nil {
					pendingImages = append(pendingImages, inject.message())
				}
			}

			// Save session with updated messages after tool execution
			session.Msgs = localMsgs
			session.UpdatedAt = time.Now()
			if err := e.Sessions.Put(session); err != nil {
				log.Log.Warnf("[Engine] ⚠️  Failed to save session after tools | SessionID: %s | Error: %v", sessionID, err)
			}

			// Continue loop to process tool results
			continue
		}

		// Text response - we're done
		textResponse := choice.Message.Content
		localMsgs = append(localMsgs, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleAssistant,
			Content: textResponse,
		})

		// Save final session state
		session.Msgs = localMsgs
		session.UpdatedAt = time.Now()
		if err := e.Sessions.Put(session); err != nil {
			log.Log.Warnf("[Engine] ⚠️  Failed to save session | SessionID: %s | Error: %v", sessionID, err)
		}

		return textResponse, totalTokenUsage, nil
	}

	return "", totalTokenUsage, fmt.Errorf("max iterations (%d) reached without final response", maxIterations)
}

// saveMessage saves a message to the database and returns the messageID
func (e *Engine) saveMessage(
	session *model.Session,
	request openai.ChatCompletionRequest,
	response openai.ChatCompletionResponse,
	choice openai.ChatCompletionChoice,
) string {
	// Get user message content
	content := choice.Message.Content
	if content == "" && len(choice.Message.ToolCalls) > 0 {
		content = FormatToolCallsContent(choice.Message.ToolCalls)
	}

	// Create message record
	messageID, seqID := session.GenerateMessageIDWithSeq()
	msg := model.NewMessage(
		messageID,
		seqID,
		session.UserID,
		session.SessionID,
		openai.ChatMessageRoleAssistant,
		content,
		model.AgentTypeLow,
		model.ContentTypeText,
		request,
		response,
		choice,
	)

	// Save to database
	if err := e.Sessions.PutMessage(msg); err != nil {
		log.Log.Warnf("[Engine] ⚠️  Failed to save message | SessionID: %s | Error: %v", session.SessionID, err)
	} else {
		log.Log.Infof("[Engine] 💾 Message saved | MessageID: %s | Model: %s | Tokens: %d", msg.MessageID, msg.Model, msg.TotalTokens)
	}
	return msg.MessageID
}

// toolActionMetadata surfaces a tool's "action" argument (when present) as
// usage-event metadata, so a host can price/limit by sub-action — e.g. the
// manage_files edit_image action vs a cheap list/read.
func toolActionMetadata(args map[string]interface{}) map[string]interface{} {
	if a, ok := args["action"].(string); ok && a != "" {
		return map[string]interface{}{"action": a}
	}
	return nil
}

// executeTool executes a single tool and returns the result string plus an
// optional image to inject into the conversation (e.g. when manage_files reads
// an image so a vision model can see it). inject is nil for ordinary tools.
// SIMPLIFIED: Does not modify session messages - caller is responsible for that.
func (e *Engine) executeTool(
	ctx context.Context,
	session *model.Session,
	messageID string,
	toolCall openai.ToolCall,
) (string, *injectedImage) {
	sessionID := session.SessionID

	log.Log.Infof("[Engine] 🔧 executeTool | Function=%s | SessionID=%s", toolCall.Function.Name, sessionID)

	// Save tool call to DB
	persister := NewToolCallPersister(e.Sessions, "Engine")
	toolID := ""
	if persister != nil {
		toolID = persister.Save(session, messageID, toolCall)
	}

	// Parse args
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
		args = make(map[string]interface{})
	}
	args["__user_id__"] = session.UserID
	args["__session_id__"] = sessionID

	toolDetail := toolCall.Function.Name
	if e.Functions != nil {
		if d := e.Functions.GetDisplayName(toolCall.Function.Name); d != "" {
			toolDetail = d
		}
	}
	// Check callback before execution. Expose a tool's "action" arg (e.g.
	// manage_files edit_image) so the host can pre-block expensive media actions.
	if e.Callback != nil {
		if cbErr := e.Callback.BeforeAction(ctx, &UsageEvent{
			UserID:    session.UserID,
			SessionID: sessionID,
			EventType: EventToolCall,
			Name:      toolCall.Function.Name,
			Metadata:  toolActionMetadata(args),
		}); cbErr != nil {
			result := FormatBlockedActionResult(cbErr)
			if persister != nil {
				persister.Update(toolID, result, cbErr)
			}
			return result, nil
		}
	}

	// Human approval is the final gate before execution. It deliberately runs
	// after cheap programmatic guards (quota/policy callback), so users are not
	// asked to approve work that would be blocked anyway.
	approvalRefID := toolID
	if approvalRefID == "" {
		approvalRefID = toolCall.ID
	}
	if e.ToolApprovalManager != nil {
		NotifyStatus(ctx, session.UserID, sessionID, StatusToolApproval, toolDetail)
	}
	_, approvalErr := AwaitToolApproval(ctx, e.ToolApprovalManager, ToolApprovalRequest{
		RefID:       approvalRefID,
		UserID:      session.UserID,
		SessionID:   sessionID,
		AgentType:   session.AgentType,
		ToolName:    toolCall.Function.Name,
		DisplayName: toolDetail,
		Arguments:   toolCall.Function.Arguments,
	})
	if approvalErr != nil {
		result := fmt.Sprintf("Tool %s was not executed: %v", toolCall.Function.Name, approvalErr)
		NotifyStatus(ctx, session.UserID, sessionID, StatusToolRejected, toolDetail)
		if persister != nil {
			persister.Update(toolID, result, approvalErr)
		}
		return result, nil
	}

	NotifyStatus(ctx, session.UserID, sessionID, StatusToolExecuting, toolDetail)

	// Execute tool
	toolStart := time.Now()
	result, err := e.Executor(toolCall.Function.Name, args)
	toolDuration := time.Since(toolStart)
	metrics.ToolCall("agent", toolCall.Function.Name, metrics.Status(err), toolDuration)

	if err != nil {
		result = fmt.Sprintf("Error executing tool %s: %v", toolCall.Function.Name, err)
		log.Log.Warnf("[Engine] Tool error | name=%s | error=%v", toolCall.Function.Name, err)
	} else {
		log.Log.Infof("[Engine] Tool result | name=%s | len=%d", toolCall.Function.Name, len(result))
	}

	// Callback after execution. A tool may hand back model/token usage via the
	// shared args map (e.g. manage_files edit_image → the image-model cost); when
	// present, attach it so the host meters the real cost, not a zero-cost tool.
	if e.Callback != nil {
		ev := &UsageEvent{
			UserID:    session.UserID,
			SessionID: sessionID,
			EventType: EventToolCall,
			Name:      toolCall.Function.Name,
			Duration:  toolDuration,
			Error:     err,
			Metadata:  toolActionMetadata(args),
		}
		if u, ok := args[usageArgKey].(*model.ImageEditResult); ok && u != nil {
			ev.Model = u.Model
			ev.InputTokens = u.InputTokens
			ev.OutputTokens = u.OutputTokens
			ev.Tokens = u.InputTokens + u.OutputTokens
			if ev.Metadata == nil {
				ev.Metadata = map[string]interface{}{}
			}
			ev.Metadata["media"] = "image"
		}
		e.Callback.AfterAction(ctx, ev)
	}

	NotifyStatus(ctx, session.UserID, sessionID, StatusToolDone, toolDetail)

	// Process result (truncate if needed). Pass the live session so an oversized
	// result is stored on it and persisted by the caller's single Put(session);
	// see processToolResult for why a clone must not be used here.
	// collect_result / inspect_result already return bounded output derived from
	// an existing buffer; re-buffering would just create a truncation loop.
	var processedResult string
	switch toolCall.Function.Name {
	case "collect_result", "inspect_result":
		processedResult = result
	default:
		processedResult = e.processToolResult(session, result)
	}

	// Update persister with result
	if persister != nil {
		persister.Update(toolID, processedResult, err)
	}

	// A tool (manage_files read on an image) may hand back an image to inject
	// into the conversation via the shared args map.
	var inject *injectedImage
	if v, ok := args[injectImageArgKey]; ok {
		inject, _ = v.(*injectedImage)
	}

	return processedResult, inject
}

// Deprecated: use executeTool instead. Kept for backward compatibility; planned
// for removal (see CHANGELOG.md → Deprecated).
func (e *Engine) executeOneToolCall(
	ctx context.Context,
	session *model.Session,
	messageID, sessionID string,
	toolCall openai.ToolCall,
) openai.ChatCompletionMessage {
	result, _ := e.executeTool(ctx, session, messageID, toolCall)
	return openai.ChatCompletionMessage{
		Role:       openai.ChatMessageRoleTool,
		Content:    result,
		Name:       toolCall.Function.Name,
		ToolCallID: toolCall.ID,
	}
}
