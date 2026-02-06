package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// SessionSchedulerConfig holds configuration for the session scheduler
type SessionSchedulerConfig struct {
	// CheckInterval is how often to check sessions (default: 5 minutes)
	CheckInterval time.Duration

	// SummarizedAtThreshold is how old SummarizedAt should be to trigger summarization (default: 1 hour)
	SummarizedAtThreshold time.Duration

	// LastActivityThreshold is how recent LastActivity should be to consider session active (default: 1 hour)
	LastActivityThreshold time.Duration

	// MessageThreshold is the minimum number of messages to trigger summarization (default: 20)
	MessageThreshold int

	// SummaryModel is the LLM model to use for summarization (default: gpt-4o-mini)
	SummaryModel string
}

// DefaultSessionSchedulerConfig returns default configuration
func DefaultSessionSchedulerConfig() SessionSchedulerConfig {
	return SessionSchedulerConfig{
		CheckInterval:         5 * time.Minute,
		SummarizedAtThreshold: 1 * time.Hour,
		LastActivityThreshold: 1 * time.Hour,
		MessageThreshold:      20,
		SummaryModel:          "gpt-4o-mini",
	}
}

// SessionScheduler periodically checks and summarizes sessions
type SessionScheduler struct {
	sessionHandler *model.SessionHandler
	llmClient      *openai.Client
	config         SessionSchedulerConfig
	stopChan       chan struct{}
	running        bool
	mu             sync.Mutex
}

// NewSessionScheduler creates a new session scheduler
func NewSessionScheduler(
	sessionHandler *model.SessionHandler,
	llmClient *openai.Client,
	config SessionSchedulerConfig,
) *SessionScheduler {
	return &SessionScheduler{
		sessionHandler: sessionHandler,
		llmClient:      llmClient,
		config:         config,
		stopChan:       make(chan struct{}),
		running:        false,
	}
}

// Start starts the scheduler in a background goroutine
func (ss *SessionScheduler) Start(ctx context.Context) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if ss.running {
		log.Log.Warnf("[SessionScheduler] ⚠️  Scheduler is already running")
		return
	}

	ss.running = true
	ss.stopChan = make(chan struct{}) // Recreate stopChan in case it was closed
	log.Log.Infof("[SessionScheduler] 🚀 Starting session scheduler | CheckInterval: %v | SummarizedAtThreshold: %v | LastActivityThreshold: %v | MessageThreshold: %d",
		ss.config.CheckInterval, ss.config.SummarizedAtThreshold, ss.config.LastActivityThreshold, ss.config.MessageThreshold)

	go ss.run(ctx)
}

// Stop stops the scheduler gracefully
func (ss *SessionScheduler) Stop() {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if !ss.running {
		return
	}

	log.Log.Infof("[SessionScheduler] 🛑 Stopping session scheduler")
	close(ss.stopChan)
	ss.running = false
}

// run runs the scheduler loop
func (ss *SessionScheduler) run(ctx context.Context) {
	ticker := time.NewTicker(ss.config.CheckInterval)
	defer ticker.Stop()

	// Run immediately on start
	ss.checkAndSummarizeSessions(ctx)

	for {
		select {
		case <-ticker.C:
			ss.checkAndSummarizeSessions(ctx)
		case <-ss.stopChan:
			log.Log.Infof("[SessionScheduler] ✅ Scheduler stopped")
			return
		case <-ctx.Done():
			log.Log.Infof("[SessionScheduler] ✅ Scheduler stopped (context cancelled)")
			return
		}
	}
}

// checkAndSummarizeSessions checks all sessions and summarizes eligible ones
func (ss *SessionScheduler) checkAndSummarizeSessions(ctx context.Context) {
	log.Log.Infof("[SessionScheduler] 🔍 Checking sessions for summarization...")

	// Get all sessions from store
	store := ss.sessionHandler.GetStore()
	debugStore, ok := store.(interface {
		GetAllSessions() (map[string][]*model.Session, error)
	})
	if !ok {
		log.Log.Errorf("[SessionScheduler] ❌ Store does not implement GetAllSessions")
		return
	}

	sessionsByUser, err := debugStore.GetAllSessions()
	if err != nil {
		log.Log.Errorf("[SessionScheduler] ❌ Failed to get all sessions: %v", err)
		return
	}

	totalSessions := 0
	eligibleSessions := 0
	summarizedSessions := 0

	now := time.Now()

	// Iterate through all sessions
	for userID, sessions := range sessionsByUser {
		totalSessions += len(sessions)
		for _, session := range sessions {
			if ss.isEligibleForSummarization(session, now) {
				eligibleSessions++
				if err := ss.summarizeSession(ctx, session); err != nil {
					log.Log.Errorf("[SessionScheduler] ❌ Failed to summarize session %s: %v", session.SessionID, err)
				} else {
					summarizedSessions++
					log.Log.Infof("[SessionScheduler] ✅ Summarized session %s (UserID: %s)", session.SessionID, userID)
				}
			}
		}
	}

	log.Log.Infof("[SessionScheduler] 📊 Summary check completed | Total: %d | Eligible: %d | Summarized: %d",
		totalSessions, eligibleSessions, summarizedSessions)
}

// isEligibleForSummarization checks if a session is eligible for summarization
func (ss *SessionScheduler) isEligibleForSummarization(session *model.Session, now time.Time) bool {
	// Check if session has messages
	if session.ConversationState == nil || len(session.ConversationState.Msgs) == 0 {
		return false
	}

	// Check message threshold
	if len(session.ConversationState.Msgs) < ss.config.MessageThreshold {
		return false
	}

	// Check if SummarizedAt is old (or never set)
	summarizedAtOld := session.SummarizedAt.IsZero() || now.Sub(session.SummarizedAt) >= ss.config.SummarizedAtThreshold
	if !summarizedAtOld {
		return false
	}

	// Check if LastActivity is recent (session is being used)
	if session.ConversationState.LastActivity.IsZero() {
		return false
	}
	lastActivityRecent := now.Sub(session.ConversationState.LastActivity) <= ss.config.LastActivityThreshold
	if !lastActivityRecent {
		return false
	}

	return true
}

// summarizeSession summarizes a session and moves messages to ExMsgs
func (ss *SessionScheduler) summarizeSession(ctx context.Context, session *model.Session) error {
	log.Log.Infof("[SessionScheduler] 📝 Summarizing session %s | Messages: %d", session.SessionID, len(session.ConversationState.Msgs))

	// Ensure user_id is in context
	ctx = model.WithUserID(ctx, session.UserID)

	// Create LLM client wrapper for openai.Client
	llmClientWrapper := &OpenAIClientWrapper{
		Client: ss.llmClient,
	}

	// Set LLM client in session handler temporarily
	originalLLMClient := ss.sessionHandler.GetLLMClient()
	ss.sessionHandler.SetLLMClient(llmClientWrapper)
	defer ss.sessionHandler.SetLLMClient(originalLLMClient)

	// Populate fields (Title, Summary, Tags) if not already set
	if session.Title == "" || session.Summary == "" || len(session.Tags) == 0 {
		if err := session.PopulateFields(ctx, llmClientWrapper, ss.config.SummaryModel); err != nil {
			log.Log.Warnf("[SessionScheduler] ⚠️  Failed to populate fields for session %s: %v", session.SessionID, err)
			// Continue anyway
		}
	}

	// Generate summary if not already set
	if session.Summary == "" {
		conversationText := formatMessagesForSummary(session.ConversationState.Msgs)
		summary, err := ss.generateSummary(ctx, conversationText)
		if err != nil {
			log.Log.Warnf("[SessionScheduler] ⚠️  Failed to generate summary for session %s: %v", session.SessionID, err)
		} else {
			session.Summary = summary
		}
	}

	// Move current Msgs to ExMsgs (append, not replace)
	session.ExMsgs = append(session.ExMsgs, session.ConversationState.Msgs...)

	// Clear Msgs
	session.ConversationState.Msgs = []openai.ChatCompletionMessage{}

	// Update timestamps
	session.SummarizedAt = time.Now()
	session.UpdatedAt = time.Now()

	// Save session
	store := ss.sessionHandler.GetStore()
	if err := store.Put(session); err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}

	log.Log.Infof("[SessionScheduler] ✅ Session %s summarized | ExMsgs: %d | Summary: %s",
		session.SessionID, len(session.ExMsgs), session.Summary)

	return nil
}

// generateSummary generates a summary for the conversation
func (ss *SessionScheduler) generateSummary(ctx context.Context, conversationText string) (string, error) {
	systemPrompt := `You are a conversation summarizer.
Generate a concise summary (2-3 sentences) that captures the main topics and outcomes of this conversation.

Requirements:
- Focus on key topics discussed and any decisions or conclusions reached
- Be specific about what was accomplished or discussed
- Maximum 200 characters
- Use present or past tense appropriately
- Do not include greetings or filler content

Example: "Debugged Kubernetes pod restart issue. Found memory limits too low. Applied fix and verified pod stability."
`

	request := openai.ChatCompletionRequest{
		Model: ss.config.SummaryModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Summarize this conversation:\n\n" + conversationText},
		},
		MaxTokens: 200,
	}

	resp, err := ss.llmClient.CreateChatCompletion(ctx, request)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return resp.Choices[0].Message.Content, nil
}

// formatMessagesForSummary converts messages to a readable format for summarization
func formatMessagesForSummary(msgs []openai.ChatCompletionMessage) string {
	var result string
	for _, msg := range msgs {
		// Skip tool-related messages
		if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 {
			continue
		}

		content := msg.Content
		if content == "" {
			continue
		}

		// Truncate long messages
		if len(content) > 300 {
			content = content[:300] + "..."
		}

		result += fmt.Sprintf("%s: %s\n", msg.Role, content)
	}
	return result
}

// OpenAIClientWrapper wraps openai.Client to implement model.LLMClient interface
type OpenAIClientWrapper struct {
	Client *openai.Client
}

// CreateChatCompletion implements model.LLMClient interface
func (w *OpenAIClientWrapper) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return w.Client.CreateChatCompletion(ctx, request)
}
