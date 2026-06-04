package core

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/llmutils"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/planning"
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

	// orchestrator is the planning orchestrator; when set, execute_plan tool and Planning prompt are available.
	orchestrator *planning.Orchestrator
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

// UsePlanning enables the planning layer in Core. When set, the Core agent gets execute_plan (and optionally get_plan_status, cancel_plan)
// plus the Planning decision-making prompt so it can run and control multi-step tasks.
func (ch *CoreHandler) UsePlanning(o *planning.Orchestrator) {
	ch.orchestrator = o
	if ch.llmClient != nil {
		o.SetLLMClient(ch.llmClient, ch.config.CoreModel)
	}
	coreToolNoOp := func(map[string]interface{}) (string, error) { return "", nil }
	ch.coreTools.MustRegister("execute_plan", "Execute plan", coreToolNoOp)
	ch.coreTools.MustRegister("get_plan_status", "Get plan status", coreToolNoOp)
	ch.coreTools.MustRegister("cancel_plan", "Cancel plan", coreToolNoOp)
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

	if ch.orchestrator != nil {
		ch.orchestrator.SetLLMClient(ch.llmClient, ch.config.CoreModel)
	}

	return nil
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
		metrics.MessageQueued("core")
		return "⏳ Processing previous request... Please wait. 📋 Your message was queued and will be answered in order.", nil
	}
	userMu := ch.getUserMutex(userID)
	userMu.Lock()
	defer userMu.Unlock()
	ch.userProgress.SetInProgress(userID, true)
	defer ch.userProgress.SetInProgress(userID, false)

	metrics.MessageStart("core")
	start := time.Now()
	response, err := ch.processOneMessageCore(ctx, userID, userMessage, contentType)
	metrics.MessageDone("core", metrics.Status(err), time.Since(start))
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
			metrics.Moderation("banned")
			return banMessage, nil
		}
		ctx = model.WithUserID(ctx, userID)
		shouldBan, banMessage, err := ch.userModeration.ProcessNonsenseCheck(ctx, userID, userMessage)
		if err != nil {
			metrics.Moderation("error")
			log.Log.Warnf("[CoreHandler] ⚠️  Failed to process nonsense check, proceeding anyway | UserID: %s | Error: %v", userID, err)
		} else {
			isNonsense = banMessage != "" || shouldBan
			if shouldBan {
				metrics.Moderation("banned")
				return banMessage, nil
			}
			if banMessage != "" {
				metrics.Moderation("nonsense")
				return banMessage, nil
			}
			metrics.Moderation("ok")
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
