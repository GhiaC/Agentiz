package agentize

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/llmutils"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// OpenAIClientWrapperForSessionHandler wraps openai.Client to implement model.LLMClient interface
type OpenAIClientWrapperForSessionHandler struct {
	Client *openai.Client
}

// CreateChatCompletion implements model.LLMClient interface
func (w *OpenAIClientWrapperForSessionHandler) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return w.Client.CreateChatCompletion(ctx, request)
}

// GetSchedulerMessageThreshold returns the message threshold from the engine's scheduler if available
func (ag *Agentize) GetSchedulerMessageThreshold() int {
	// Try to get from scheduler first
	ag.schedulerMu.RLock()
	if ag.scheduler != nil {
		threshold := ag.scheduler.GetMessageThreshold()
		ag.schedulerMu.RUnlock()
		return threshold
	}
	ag.schedulerMu.RUnlock()

	// Try engine scheduler
	if ag.engine != nil {
		return ag.engine.GetSchedulerMessageThreshold()
	}

	// Fallback: try environment variable
	if thresholdStr := os.Getenv("AGENTIZE_SCHEDULER_MESSAGE_THRESHOLD"); thresholdStr != "" {
		if threshold, err := strconv.Atoi(thresholdStr); err == nil && threshold > 0 {
			return threshold
		}
	}
	// Final fallback to default
	return 5
}

// StartScheduler starts the session scheduler if enabled
// It reads configuration from environment variables or uses defaults
// This should be called after UseLLMConfig to ensure LLM client is configured
func (ag *Agentize) StartScheduler(ctx context.Context) error {
	// Get LLM config from engine
	llmConfig := ag.engine.GetLLMConfig()
	if llmConfig.APIKey == "" {
		return fmt.Errorf("LLM client is not configured. Call UseLLMConfig first")
	}

	// Create a new LLM client with HTTP client wrapper that adds user_id header from context
	var baseHTTPClient *http.Client
	if llmConfig.HTTPClient != nil {
		baseHTTPClient = llmConfig.HTTPClient
	}
	llmClient := llmutils.NewOpenAIClientWithUserIDHeader(llmConfig.APIKey, llmConfig.BaseURL, baseHTTPClient)

	// Get session store from engine
	sessionStore := ag.engine.Sessions
	if sessionStore == nil {
		return fmt.Errorf("session store is not available")
	}

	// Create session handler from session store
	sessionHandlerConfig := model.DefaultSessionHandlerConfig()
	sessionHandler := model.NewSessionHandler(sessionStore, sessionHandlerConfig)

	// Set LLM client for session handler
	llmClientWrapper := &OpenAIClientWrapperForSessionHandler{Client: llmClient}
	sessionHandler.SetLLMClient(llmClientWrapper)

	// Load scheduler config from environment or use defaults
	schedulerConfig := loadSchedulerConfig()

	// Check if scheduler is enabled
	if enabled := os.Getenv("AGENTIZE_SCHEDULER_ENABLED"); enabled == "false" {
		log.Log.Infof("[Agentize] ⏸️  Scheduler is disabled via AGENTIZE_SCHEDULER_ENABLED=false")
		return nil
	}

	// Create scheduler
	scheduler := engine.NewSessionScheduler(sessionHandler, llmClient, schedulerConfig)

	ag.schedulerMu.Lock()
	ag.scheduler = scheduler
	ag.schedulerMu.Unlock()

	// Start scheduler
	scheduler.Start(ctx)

	log.Log.Infof("[Agentize] ✅ Session scheduler started | CheckInterval: %v | FirstThreshold: %d msgs | SubsequentThreshold: %d msgs + %v | SummaryModel: %s",
		schedulerConfig.CheckInterval, schedulerConfig.FirstSummarizationThreshold,
		schedulerConfig.SubsequentMessageThreshold, schedulerConfig.SubsequentTimeThreshold,
		schedulerConfig.SummaryModel)

	return nil
}

// loadSchedulerConfig loads scheduler configuration from environment variables
func loadSchedulerConfig() engine.SessionSchedulerConfig {
	config := engine.DefaultSessionSchedulerConfig()

	if v := os.Getenv("AGENTIZE_SCHEDULER_CHECK_INTERVAL_MINUTES"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil {
			config.CheckInterval = time.Duration(minutes) * time.Minute
		}
	}
	if v := os.Getenv("AGENTIZE_SCHEDULER_SUBSEQUENT_TIME_THRESHOLD_MINUTES"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil {
			config.SubsequentTimeThreshold = time.Duration(minutes) * time.Minute
		}
	}
	if v := os.Getenv("AGENTIZE_SCHEDULER_LAST_ACTIVITY_THRESHOLD_MINUTES"); v != "" {
		if minutes, err := strconv.Atoi(v); err == nil {
			config.LastActivityThreshold = time.Duration(minutes) * time.Minute
		}
	}
	if v := os.Getenv("AGENTIZE_SCHEDULER_FIRST_THRESHOLD"); v != "" {
		if threshold, err := strconv.Atoi(v); err == nil {
			config.FirstSummarizationThreshold = threshold
		}
	}
	if v := os.Getenv("AGENTIZE_SCHEDULER_SUBSEQUENT_MESSAGE_THRESHOLD"); v != "" {
		if threshold, err := strconv.Atoi(v); err == nil {
			config.SubsequentMessageThreshold = threshold
		}
	}
	if v := os.Getenv("AGENTIZE_SCHEDULER_SUMMARY_MODEL"); v != "" {
		config.SummaryModel = v
	}

	return config
}

// StopScheduler stops the session scheduler gracefully
func (ag *Agentize) StopScheduler() {
	ag.schedulerMu.Lock()
	scheduler := ag.scheduler
	ag.schedulerMu.Unlock()

	if scheduler != nil {
		scheduler.Stop()
		log.Log.Infof("[Agentize] 🛑 Session scheduler stopped")
	}
}

// GetScheduler returns the current scheduler instance
func (ag *Agentize) GetScheduler() *engine.SessionScheduler {
	ag.schedulerMu.RLock()
	defer ag.schedulerMu.RUnlock()
	return ag.scheduler
}

// GetSchedulerConfig returns the full scheduler configuration if available
// Returns nil if scheduler is not initialized
func (ag *Agentize) GetSchedulerConfig() *engine.SessionSchedulerConfig {
	// Try Agentize's scheduler first
	ag.schedulerMu.RLock()
	if ag.scheduler != nil {
		config := ag.scheduler.GetConfig()
		ag.schedulerMu.RUnlock()
		return &config
	}
	ag.schedulerMu.RUnlock()

	// Try engine's scheduler
	if ag.engine != nil {
		return ag.engine.GetSchedulerConfig()
	}

	return nil
}
