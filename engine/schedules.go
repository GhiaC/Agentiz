package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
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

	// CleanerInterval is how often to run the cleaner goroutine to remove duplicate messages (default: 30 minutes)
	CleanerInterval time.Duration

	// DisableLogs if true, SessionScheduler does not emit any logs
	DisableLogs bool
}

// DefaultSessionSchedulerConfig returns default configuration
func DefaultSessionSchedulerConfig() SessionSchedulerConfig {
	return SessionSchedulerConfig{
		CheckInterval:         5 * time.Minute,
		SummarizedAtThreshold: 1 * time.Hour,
		LastActivityThreshold: 1 * time.Hour,
		MessageThreshold:      5, // Reduced from 20 to trigger summarization more frequently
		SummaryModel:          "gpt-4o-mini",
		CleanerInterval:       30 * time.Minute,
		DisableLogs:           true,
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
		if !ss.config.DisableLogs {
			log.Log.Warnf("[SessionScheduler] ⚠️  Scheduler is already running")
		}
		return
	}

	ss.running = true
	ss.stopChan = make(chan struct{}) // Recreate stopChan in case it was closed
	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🚀 Starting session scheduler | CheckInterval: %v | SummarizedAtThreshold: %v | LastActivityThreshold: %v | MessageThreshold: %d | CleanerInterval: %v",
			ss.config.CheckInterval, ss.config.SummarizedAtThreshold, ss.config.LastActivityThreshold, ss.config.MessageThreshold, ss.config.CleanerInterval)
	}

	go ss.run(ctx)
	go ss.runCleaner(ctx)
}

// Stop stops the scheduler gracefully
func (ss *SessionScheduler) Stop() {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if !ss.running {
		return
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🛑 Stopping session scheduler")
	}
	close(ss.stopChan)
	ss.running = false
}

// GetMessageThreshold returns the message threshold from scheduler config
func (ss *SessionScheduler) GetMessageThreshold() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.config.MessageThreshold
}

// run runs the scheduler loop
func (ss *SessionScheduler) run(ctx context.Context) {
	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🔍 Starting initial session check (checking all sessions immediately)...")
	}
	ss.checkAndSummarizeSessions(ctx)
	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] ✅ Initial session check completed, starting periodic checks...")
	}

	ticker := time.NewTicker(ss.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ss.checkAndSummarizeSessions(ctx)
		case <-ss.stopChan:
			if !ss.config.DisableLogs {
				log.Log.Infof("[SessionScheduler] ✅ Scheduler stopped")
			}
			return
		case <-ctx.Done():
			if !ss.config.DisableLogs {
				log.Log.Infof("[SessionScheduler] ✅ Scheduler stopped (context cancelled)")
			}
			return
		}
	}
}

// checkAndSummarizeSessions checks all sessions and summarizes eligible ones
func (ss *SessionScheduler) checkAndSummarizeSessions(ctx context.Context) {
	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 🔍 Checking sessions for summarization...")
	}

	// Get all sessions from store
	sessionStore := ss.sessionHandler.GetStore()
	debugStore, ok := sessionStore.(store.DebugStore)
	if !ok {
		if !ss.config.DisableLogs {
			log.Log.Errorf("[SessionScheduler] ❌ Store does not implement DebugStore interface")
		}
		return
	}

	sessionsByUser, err := debugStore.GetAllSessions()
	if err != nil {
		if !ss.config.DisableLogs {
			log.Log.Errorf("[SessionScheduler] ❌ Failed to get all sessions: %v", err)
		}
		return
	}

	totalSessions := 0
	eligibleSessions := 0
	summarizedSessions := 0
	sessionsWithMessages := 0
	sessionsWithoutMessages := 0
	alreadySummarizedSessions := 0
	sessionsNotEligible := 0
	totalUsers := len(sessionsByUser)
	totalMessages := 0

	now := time.Now()

	// Iterate through all sessions
	for userID, sessions := range sessionsByUser {
		totalSessions += len(sessions)
		for _, session := range sessions {
			msgCount := 0
			if session.ConversationState != nil {
				msgCount = len(session.ConversationState.Msgs)
				totalMessages += msgCount
			}

			// Track sessions with/without messages
			if msgCount > 0 {
				sessionsWithMessages++
			} else {
				sessionsWithoutMessages++
			}

			// Track already summarized sessions
			if !session.SummarizedAt.IsZero() {
				alreadySummarizedSessions++
			}

			isEligible := ss.isEligibleForSummarization(session, now)
			if !isEligible && msgCount > 0 {
				sessionsNotEligible++
				// Log why session is not eligible (only for sessions with messages)
				reasons := []string{}
				if session.ConversationState == nil || msgCount == 0 {
					reasons = append(reasons, "no messages")
				}
				if msgCount < ss.config.MessageThreshold {
					reasons = append(reasons, fmt.Sprintf("only %d messages (need %d)", msgCount, ss.config.MessageThreshold))
				}
				if !session.SummarizedAt.IsZero() {
					summarizedAge := now.Sub(session.SummarizedAt)
					if summarizedAge < ss.config.SummarizedAtThreshold {
						reasons = append(reasons, fmt.Sprintf("summarized %v ago (need %v)", summarizedAge, ss.config.SummarizedAtThreshold))
					}
					if session.ConversationState != nil && !session.ConversationState.LastActivity.IsZero() {
						lastActivityAge := now.Sub(session.ConversationState.LastActivity)
						if lastActivityAge > ss.config.LastActivityThreshold {
							reasons = append(reasons, fmt.Sprintf("last activity %v ago (need within %v)", lastActivityAge, ss.config.LastActivityThreshold))
						}
					}
				}
				if len(reasons) > 0 && !ss.config.DisableLogs {
					log.Log.Debugf("[SessionScheduler] ⏭️  Session %s not eligible: %s | Messages: %d", session.SessionID, strings.Join(reasons, ", "), msgCount)
				}
			}
			if isEligible {
				eligibleSessions++
				if !ss.config.DisableLogs {
					log.Log.Infof("[SessionScheduler] 🎯 Session eligible for summarization | SessionID: %s | UserID: %s | Messages: %d", session.SessionID, userID, msgCount)
				}
				if err := ss.summarizeSession(ctx, session); err != nil {
					if !ss.config.DisableLogs {
						log.Log.Errorf("[SessionScheduler] ❌ Failed to summarize session %s: %v", session.SessionID, err)
					}
				} else {
					summarizedSessions++
					if !ss.config.DisableLogs {
						log.Log.Infof("[SessionScheduler] ✅ Summarized session %s (UserID: %s)", session.SessionID, userID)
					}
				}
				if !ss.config.DisableLogs {
					log.Log.Infof("[SessionScheduler] ⏸️  Sleeping 10 seconds before next summarization...")
				}
				time.Sleep(10 * time.Second)
			}
		}
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 📊 Summary check completed | Total: %d | Users: %d | Messages: %d | WithMsgs: %d | NoMsgs: %d | AlreadySummarized: %d | NotEligible: %d | Eligible: %d | Summarized: %d | Threshold: %d msgs, %v old, %v activity",
			totalSessions, totalUsers, totalMessages, sessionsWithMessages, sessionsWithoutMessages, alreadySummarizedSessions, sessionsNotEligible, eligibleSessions, summarizedSessions,
			ss.config.MessageThreshold, ss.config.SummarizedAtThreshold, ss.config.LastActivityThreshold)
	}
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

	// If SummarizedAt is empty (never summarized), summarize it regardless of LastActivity
	if session.SummarizedAt.IsZero() {
		return true
	}

	// For sessions that have been summarized before, check if SummarizedAt is old
	summarizedAtOld := now.Sub(session.SummarizedAt) >= ss.config.SummarizedAtThreshold
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
	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] 📝 Summarizing session %s | Messages: %d", session.SessionID, len(session.ConversationState.Msgs))
	}

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
	summaryWasEmpty := session.Summary == ""
	if session.Title == "" || session.Summary == "" || len(session.Tags) == 0 {
		if err := session.PopulateFields(ctx, llmClientWrapper, ss.config.SummaryModel); err != nil {
			if !ss.config.DisableLogs {
				log.Log.Warnf("[SessionScheduler] ⚠️  Failed to populate fields for session %s: %v", session.SessionID, err)
			}
			// Continue anyway
		}
	}

	// Always call generateSummary to save the summarization log, even if summary is already set
	// This ensures we track all summarization attempts in the database
	conversationText := formatMessagesForSummary(session.ConversationState.Msgs)
	summary, err := ss.generateSummary(ctx, session.SessionID, session.UserID, conversationText)
	if err != nil {
		if !ss.config.DisableLogs {
			log.Log.Warnf("[SessionScheduler] ⚠️  Failed to generate summary for session %s: %v", session.SessionID, err)
		}
		// Only set summary if it was empty and we got a new one
		if summaryWasEmpty && summary != "" {
			session.Summary = summary
		}
	} else {
		// Only update summary if it was empty before
		if summaryWasEmpty {
			session.Summary = summary
		}
	}

	// Create a backup of Msgs before moving (for rollback in case of save failure)
	msgsBackup := make([]openai.ChatCompletionMessage, len(session.ConversationState.Msgs))
	copy(msgsBackup, session.ConversationState.Msgs)

	// Move current Msgs to ExMsgs (append, not replace)
	// Create a copy to ensure safe transfer
	msgsToMove := make([]openai.ChatCompletionMessage, len(session.ConversationState.Msgs))
	copy(msgsToMove, session.ConversationState.Msgs)
	session.ExMsgs = append(session.ExMsgs, msgsToMove...)

	// Clear Msgs
	session.ConversationState.Msgs = []openai.ChatCompletionMessage{}

	// Update timestamps
	session.SummarizedAt = time.Now()
	session.UpdatedAt = time.Now()

	// Save session - if this fails, we'll rollback the changes
	store := ss.sessionHandler.GetStore()
	if err := store.Put(session); err != nil {
		// Rollback: restore Msgs and remove from ExMsgs
		session.ConversationState.Msgs = msgsBackup
		// Remove the messages we just added to ExMsgs
		if len(session.ExMsgs) >= len(msgsToMove) {
			session.ExMsgs = session.ExMsgs[:len(session.ExMsgs)-len(msgsToMove)]
		}
		return fmt.Errorf("failed to save session: %w", err)
	}

	// After successful save, ensure Msgs is empty (defensive check)
	if len(session.ConversationState.Msgs) > 0 {
		if !ss.config.DisableLogs {
			log.Log.Warnf("[SessionScheduler] ⚠️  Msgs not empty after save, clearing... | SessionID: %s | Msgs count: %d", session.SessionID, len(session.ConversationState.Msgs))
		}
		session.ConversationState.Msgs = []openai.ChatCompletionMessage{}
		// Save again to ensure consistency
		if err := store.Put(session); err != nil {
			if !ss.config.DisableLogs {
				log.Log.Errorf("[SessionScheduler] ❌ Failed to save session after clearing Msgs: %v", err)
			}
			return fmt.Errorf("failed to save session after clearing Msgs: %w", err)
		}
	}

	if !ss.config.DisableLogs {
		log.Log.Infof("[SessionScheduler] ✅ Session %s summarized | ExMsgs: %d | Summary: %s",
			session.SessionID, len(session.ExMsgs), session.Summary)
	}

	return nil
}

// generateSummary generates a summary for the conversation
func (ss *SessionScheduler) generateSummary(ctx context.Context, sessionID string, userID string, conversationText string) (string, error) {
	if !ss.config.DisableLogs {
		fmt.Printf("[SessionScheduler] 🔍 generateSummary called | SessionID: %s | UserID: %s | ConversationText length: %d\n",
			sessionID, userID, len(conversationText))
	}

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

	userPrompt := "Summarize this conversation:\n\n" + conversationText
	fullPrompt := systemPrompt + "\n\n" + userPrompt

	request := openai.ChatCompletionRequest{
		Model: ss.config.SummaryModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt},
		},
		MaxTokens: 200,
	}

	// Create log entry before making the request
	summLog := model.NewSummarizationLog(sessionID, userID)
	summLog.PromptSent = fullPrompt
	summLog.ModelUsed = ss.config.SummaryModel
	summLog.Status = "pending"

	if !ss.config.DisableLogs {
		fmt.Printf("[SessionScheduler] 🔍 Created summarization log | LogID: %s | SessionID: %s | UserID: %s | Status: %s\n",
			summLog.LogID, summLog.SessionID, summLog.UserID, summLog.Status)
	}

	// Validate required fields
	if !ss.config.DisableLogs {
		if summLog.PromptSent == "" {
			log.Log.Warnf("[SessionScheduler] ⚠️  PromptSent is empty for log | LogID: %s", summLog.LogID)
		}
		if summLog.ModelUsed == "" {
			log.Log.Warnf("[SessionScheduler] ⚠️  ModelUsed is empty for log | LogID: %s", summLog.LogID)
		}
		if summLog.LogID == "" {
			log.Log.Errorf("[SessionScheduler] ❌ LogID is empty!")
		}
		if summLog.SessionID == "" {
			log.Log.Errorf("[SessionScheduler] ❌ SessionID is empty!")
		}
		if summLog.UserID == "" {
			log.Log.Errorf("[SessionScheduler] ❌ UserID is empty!")
		}
	}

	// Get store to save log
	sessionStore := ss.sessionHandler.GetStore()
	if !ss.config.DisableLogs {
		fmt.Printf("[SessionScheduler] 🔍 Store type: %T | LogID: %s\n", sessionStore, summLog.LogID)
	}
	debugStore, ok := sessionStore.(store.DebugStore)
	if !ok {
		if !ss.config.DisableLogs {
			fmt.Printf("[SessionScheduler] ⚠️  Store does not implement DebugStore interface, skipping summarization log | Store type: %T | LogID: %s\n", sessionStore, summLog.LogID)
			log.Log.Warnf("[SessionScheduler] ⚠️  Store does not implement DebugStore interface, skipping summarization log | Store type: %T | LogID: %s", sessionStore, summLog.LogID)
		}
	} else {
		if !ss.config.DisableLogs {
			fmt.Printf("[SessionScheduler] ✅ Store implements DebugStore, attempting to save summarization log | LogID: %s | SessionID: %s | Status: %s | PromptSent length: %d\n",
				summLog.LogID, sessionID, summLog.Status, len(summLog.PromptSent))
			log.Log.Infof("[SessionScheduler] 🔍 Store implements DebugStore, attempting to save summarization log | LogID: %s | SessionID: %s | Status: %s | PromptSent length: %d", summLog.LogID, sessionID, summLog.Status, len(summLog.PromptSent))
		}
		if err := debugStore.PutSummarizationLog(summLog); err != nil {
			if !ss.config.DisableLogs {
				fmt.Printf("[SessionScheduler] ❌ Failed to save summarization log: %v | LogID: %s | SessionID: %s\n", err, summLog.LogID, sessionID)
				log.Log.Errorf("[SessionScheduler] ❌ Failed to save summarization log: %v | LogID: %s | SessionID: %s | Error details: %+v", err, summLog.LogID, sessionID, err)
			}
		} else if !ss.config.DisableLogs {
			fmt.Printf("[SessionScheduler] ✅ Saved summarization log (pending) | LogID: %s | SessionID: %s\n", summLog.LogID, sessionID)
			log.Log.Infof("[SessionScheduler] ✅ Saved summarization log (pending) | LogID: %s | SessionID: %s", summLog.LogID, sessionID)
		}
	}

	systemPromptLen := 0
	for _, m := range request.Messages {
		if m.Role == openai.ChatMessageRoleSystem {
			systemPromptLen += len(m.Content)
		}
	}
	log.Log.Infof("[SessionScheduler] 🔵 LLM >> Model: %s | Messages: %d | system_prompt_len=%d (summarization)", request.Model, len(request.Messages), systemPromptLen)

	resp, err := ss.llmClient.CreateChatCompletion(ctx, request)
	if err != nil {
		// Update log with error
		summLog.Status = "failed"
		summLog.ErrorMessage = err.Error()
		if ok {
			if updateErr := debugStore.PutSummarizationLog(summLog); updateErr != nil {
				log.Log.Errorf("[SessionScheduler] ❌ Failed to update summarization log (failed status): %v | LogID: %s | SessionID: %s", updateErr, summLog.LogID, sessionID)
			} else {
				log.Log.Infof("[SessionScheduler] ✅ Updated summarization log (failed) | LogID: %s | SessionID: %s", summLog.LogID, sessionID)
			}
		}
		return "", err
	}
	if resp.Usage.TotalTokens > 0 {
		cacheTokens := 0
		if resp.Usage.PromptTokensDetails != nil {
			cacheTokens = resp.Usage.PromptTokensDetails.CachedTokens
		}
		log.Log.Infof("[SessionScheduler] 📊 TOKEN USAGE >> Model: %s | prompt=%d | completion=%d | total=%d | cache=%d (summarization)",
			request.Model, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens, cacheTokens)
	}

	if len(resp.Choices) == 0 {
		err := fmt.Errorf("no response from LLM")
		summLog.Status = "failed"
		summLog.ErrorMessage = err.Error()
		if ok {
			if updateErr := debugStore.PutSummarizationLog(summLog); updateErr != nil {
				log.Log.Errorf("[SessionScheduler] ❌ Failed to update summarization log (no response): %v | LogID: %s | SessionID: %s", updateErr, summLog.LogID, sessionID)
			} else {
				log.Log.Infof("[SessionScheduler] ✅ Updated summarization log (no response) | LogID: %s | SessionID: %s", summLog.LogID, sessionID)
			}
		}
		return "", err
	}

	summary := resp.Choices[0].Message.Content

	// Update log with success response
	summLog.Status = "success"
	summLog.ResponseReceived = summary
	if resp.Usage.PromptTokens > 0 {
		summLog.PromptTokens = resp.Usage.PromptTokens
	}
	if resp.Usage.CompletionTokens > 0 {
		summLog.CompletionTokens = resp.Usage.CompletionTokens
	}
	if resp.Usage.TotalTokens > 0 {
		summLog.TotalTokens = resp.Usage.TotalTokens
	}
	if ok {
		fmt.Printf("[SessionScheduler] 🔍 Attempting to update summarization log (success) | LogID: %s | SessionID: %s | Tokens: %d\n",
			summLog.LogID, sessionID, summLog.TotalTokens)
		log.Log.Infof("[SessionScheduler] 🔍 Attempting to update summarization log (success) | LogID: %s | SessionID: %s | Tokens: %d", summLog.LogID, sessionID, summLog.TotalTokens)
		if err := debugStore.PutSummarizationLog(summLog); err != nil {
			fmt.Printf("[SessionScheduler] ❌ Failed to update summarization log: %v | LogID: %s | SessionID: %s\n", err, summLog.LogID, sessionID)
			log.Log.Errorf("[SessionScheduler] ❌ Failed to update summarization log: %v | LogID: %s | SessionID: %s", err, summLog.LogID, sessionID)
		} else {
			fmt.Printf("[SessionScheduler] ✅ Updated summarization log (success) | LogID: %s | SessionID: %s | Tokens: %d\n",
				summLog.LogID, sessionID, summLog.TotalTokens)
			log.Log.Infof("[SessionScheduler] ✅ Updated summarization log (success) | LogID: %s | SessionID: %s | Tokens: %d", summLog.LogID, sessionID, summLog.TotalTokens)
		}
	} else {
		fmt.Printf("[SessionScheduler] ⚠️  Store does not implement DebugStore interface, cannot update log | LogID: %s\n", summLog.LogID)
		log.Log.Warnf("[SessionScheduler] ⚠️  Store does not implement DebugStore interface, cannot update log | LogID: %s", summLog.LogID)
	}

	return summary, nil
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

// runCleaner runs the cleaner goroutine to remove duplicate messages
func (ss *SessionScheduler) runCleaner(ctx context.Context) {
	log.Log.Infof("[SessionScheduler] 🧹 Starting cleaner goroutine | CleanerInterval: %v", ss.config.CleanerInterval)

	// Run immediately on start (first run)
	log.Log.Infof("[SessionScheduler] 🧹 Running initial cleanup (first run)...")
	ss.cleanDuplicateMessages(ctx, true)

	ticker := time.NewTicker(ss.config.CleanerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ss.cleanDuplicateMessages(ctx, false)
		case <-ss.stopChan:
			log.Log.Infof("[SessionScheduler] ✅ Cleaner stopped")
			return
		case <-ctx.Done():
			log.Log.Infof("[SessionScheduler] ✅ Cleaner stopped (context cancelled)")
			return
		}
	}
}

// cleanDuplicateMessages removes duplicate messages from Msgs that exist in ExMsgs
// isFirstRun indicates if this is the first run (initial cleanup on startup)
func (ss *SessionScheduler) cleanDuplicateMessages(_ context.Context, isFirstRun bool) {
	if isFirstRun {
		log.Log.Infof("[SessionScheduler] 🧹 Starting initial duplicate message cleanup (first run on startup)...")
	} else {
		log.Log.Infof("[SessionScheduler] 🧹 Starting periodic duplicate message cleanup...")
	}

	// Get all sessions from store
	sessionStore := ss.sessionHandler.GetStore()
	debugStore, ok := sessionStore.(store.DebugStore)
	if !ok {
		log.Log.Errorf("[SessionScheduler] ❌ Store does not implement DebugStore interface")
		return
	}

	sessionsByUser, err := debugStore.GetAllSessions()
	if err != nil {
		log.Log.Errorf("[SessionScheduler] ❌ Failed to get all sessions for cleaning: %v", err)
		return
	}

	totalSessions := 0
	cleanedSessions := 0
	totalRemoved := 0
	sessionsWithBoth := 0
	sessionsWithOnlyMsgs := 0
	sessionsWithOnlyExMsgs := 0

	// Iterate through all sessions
	for userID, sessions := range sessionsByUser {
		totalSessions += len(sessions)
		for _, session := range sessions {
			hasMsgs := session.ConversationState != nil && len(session.ConversationState.Msgs) > 0
			hasExMsgs := len(session.ExMsgs) > 0

			if hasMsgs && hasExMsgs {
				sessionsWithBoth++
			} else if hasMsgs {
				sessionsWithOnlyMsgs++
			} else if hasExMsgs {
				sessionsWithOnlyExMsgs++
			}

			if !hasMsgs {
				continue
			}

			if !hasExMsgs {
				continue
			}

			// Count messages before cleaning
			beforeCount := len(session.ConversationState.Msgs)
			exMsgsCount := len(session.ExMsgs)

			log.Log.Infof("[SessionScheduler] 🔍 Checking session %s (UserID: %s) | Msgs: %d | ExMsgs: %d",
				session.SessionID, userID, beforeCount, exMsgsCount)

			// Remove duplicates
			removedCount := ss.removeDuplicateMessages(session)

			afterCount := len(session.ConversationState.Msgs)

			if removedCount > 0 {
				// Update timestamp
				session.UpdatedAt = time.Now()

				// Save session
				if err := sessionStore.Put(session); err != nil {
					log.Log.Errorf("[SessionScheduler] ❌ Failed to save session after cleaning: %v | SessionID: %s", err, session.SessionID)
					continue
				}

				// Verify the save by reading the session back
				verifySession, err := sessionStore.Get(session.SessionID)
				if err != nil {
					log.Log.Warnf("[SessionScheduler] ⚠️  Failed to verify session after save: %v | SessionID: %s", err, session.SessionID)
				} else if verifySession.ConversationState != nil {
					verifyCount := len(verifySession.ConversationState.Msgs)
					if verifyCount != afterCount {
						log.Log.Errorf("[SessionScheduler] ❌ Verification failed! Expected %d messages after save, got %d | SessionID: %s",
							afterCount, verifyCount, session.SessionID)
					} else {
						log.Log.Debugf("[SessionScheduler] ✅ Verification passed | SessionID: %s | Msgs count: %d", session.SessionID, verifyCount)
					}
				}

				cleanedSessions++
				totalRemoved += removedCount
				log.Log.Infof("[SessionScheduler] ✅ Cleaned session %s (UserID: %s) | Removed: %d messages | Before: %d | After: %d | ExMsgs: %d",
					session.SessionID, userID, removedCount, beforeCount, afterCount, exMsgsCount)
			} else {
				log.Log.Infof("[SessionScheduler] ⏭️  No duplicates found in session %s (UserID: %s) | Msgs: %d | ExMsgs: %d",
					session.SessionID, userID, beforeCount, exMsgsCount)
			}
		}
	}

	if isFirstRun {
		log.Log.Infof("[SessionScheduler] 📊 Initial cleanup completed (first run) | Total sessions: %d | WithBoth: %d | OnlyMsgs: %d | OnlyExMsgs: %d | Cleaned: %d | Total removed: %d messages",
			totalSessions, sessionsWithBoth, sessionsWithOnlyMsgs, sessionsWithOnlyExMsgs, cleanedSessions, totalRemoved)
	} else {
		log.Log.Infof("[SessionScheduler] 📊 Periodic cleanup completed | Total sessions: %d | WithBoth: %d | OnlyMsgs: %d | OnlyExMsgs: %d | Cleaned: %d | Total removed: %d messages",
			totalSessions, sessionsWithBoth, sessionsWithOnlyMsgs, sessionsWithOnlyExMsgs, cleanedSessions, totalRemoved)
	}
}

// removeDuplicateMessages removes messages from Msgs that are duplicates of messages in ExMsgs
// Returns the number of messages removed
func (ss *SessionScheduler) removeDuplicateMessages(session *model.Session) int {
	if session.ConversationState == nil || len(session.ConversationState.Msgs) == 0 {
		return 0
	}

	if len(session.ExMsgs) == 0 {
		return 0
	}

	// Create a map of ExMsgs for fast lookup
	exMsgsMap := make(map[string]bool)
	exMsgsKeys := make([]string, 0, len(session.ExMsgs))
	exMsgsDetails := make([]string, 0, len(session.ExMsgs))

	log.Log.Infof("[SessionScheduler] 🔍 Processing ExMsgs for session %s | ExMsgs count: %d", session.SessionID, len(session.ExMsgs))

	for idx, exMsg := range session.ExMsgs {
		key := messageKey(exMsg)
		exMsgsMap[key] = true
		exMsgsKeys = append(exMsgsKeys, key)
		if idx < 3 { // Store details for first 3
			exMsgsDetails = append(exMsgsDetails, fmt.Sprintf("ExMsg[%d]: Role=%s, Content=%s", idx, exMsg.Role, truncateStringForLog(exMsg.Content, 50)))
		}
	}

	log.Log.Infof("[SessionScheduler] ✅ Created ExMsgs map with %d unique keys for session %s | Checking %d messages in Msgs",
		len(exMsgsMap), session.SessionID, len(session.ConversationState.Msgs))

	if len(exMsgsDetails) > 0 {
		log.Log.Infof("[SessionScheduler] 📋 ExMsgs details (first 3): %v", exMsgsDetails)
	}

	// Filter out duplicate messages from Msgs
	cleanedMsgs := make([]openai.ChatCompletionMessage, 0, len(session.ConversationState.Msgs))
	removedCount := 0
	removedKeys := make([]string, 0)
	checkedKeys := make([]string, 0)

	for i, msg := range session.ConversationState.Msgs {
		key := messageKey(msg)
		checkedKeys = append(checkedKeys, key[:min(50, len(key))])

		// Check if this key exists in ExMsgs
		if exMsgsMap[key] {
			// This message exists in ExMsgs, skip it
			removedCount++
			removedKeys = append(removedKeys, fmt.Sprintf("msg[%d]:%s", i, key[:min(50, len(key))]))
			log.Log.Infof("[SessionScheduler] ✅ Found duplicate message at index %d | Role: %s | Content preview: %s | Key: %s",
				i, msg.Role, truncateStringForLog(msg.Content, 50), key[:min(100, len(key))])
			continue
		}

		// Log when we don't find a match (for debugging)
		if i < 3 { // Only log first 3 for performance
			log.Log.Debugf("[SessionScheduler] 🔍 Checking msg[%d] | Role: %s | Content preview: %s | Key not found in ExMsgs",
				i, msg.Role, truncateStringForLog(msg.Content, 50))
		}

		cleanedMsgs = append(cleanedMsgs, msg)
	}

	// Log sample keys for debugging
	if len(checkedKeys) > 0 {
		sampleSize := min(3, len(checkedKeys))
		log.Log.Infof("[SessionScheduler] 📋 Sample Msgs keys (first %d): %v", sampleSize, checkedKeys[:sampleSize])
	}
	if len(exMsgsKeys) > 0 {
		sampleSize := min(3, len(exMsgsKeys))
		log.Log.Infof("[SessionScheduler] 📋 Sample ExMsgs keys (first %d): %v", sampleSize, exMsgsKeys[:sampleSize])
	}

	// Detailed comparison for first few messages when no duplicates found
	if removedCount == 0 && len(session.ConversationState.Msgs) > 0 && len(session.ExMsgs) > 0 {
		log.Log.Infof("[SessionScheduler] 🔍 No duplicates found - performing detailed comparison for session %s:", session.SessionID)

		// Compare first 2 messages from Msgs with first 2 ExMsgs
		for i := 0; i < min(2, len(session.ConversationState.Msgs)); i++ {
			msg := session.ConversationState.Msgs[i]
			msgKey := messageKey(msg)
			log.Log.Infof("[SessionScheduler]   📝 Msg[%d] | Role: %s | Content: %s | Key: %s",
				i, msg.Role, truncateStringForLog(msg.Content, 80), truncateStringForLog(msgKey, 100))

			// Check if any ExMsg matches (check ALL ExMsgs, not just first 5)
			foundMatch := false
			for j, exMsg := range session.ExMsgs {
				exMsgKey := messageKey(exMsg)
				if msgKey == exMsgKey {
					foundMatch = true
					log.Log.Warnf("[SessionScheduler]   ⚠️  DUPLICATE FOUND BUT NOT REMOVED! Msg[%d] matches ExMsg[%d] | Role: %s | Content: %s",
						i, j, msg.Role, truncateStringForLog(msg.Content, 100))
					log.Log.Warnf("[SessionScheduler]   ⚠️  Key: %s", truncateStringForLog(msgKey, 200))
					break
				}

				// Also check content match even if key doesn't match (for debugging)
				if msg.Role == exMsg.Role && msg.Content == exMsg.Content {
					log.Log.Warnf("[SessionScheduler]   ⚠️  Content matches but key doesn't! Msg[%d] vs ExMsg[%d] | MsgKey: %s | ExMsgKey: %s",
						i, j, truncateStringForLog(msgKey, 100), truncateStringForLog(exMsgKey, 100))
				}
			}

			if !foundMatch && i < 2 {
				log.Log.Debugf("[SessionScheduler]   ❌ Msg[%d] not found in ExMsgs", i)
			}
		}
	}

	// Update Msgs if any duplicates were removed
	if removedCount > 0 {
		log.Log.Infof("[SessionScheduler] 🗑️  Removing %d duplicate messages from session %s | Sample removed keys: %v",
			removedCount, session.SessionID, removedKeys[:min(5, len(removedKeys))])
		session.ConversationState.Msgs = cleanedMsgs
		// Ensure we're not keeping a reference to the old slice
		session.ConversationState.Msgs = append([]openai.ChatCompletionMessage(nil), cleanedMsgs...)
	}

	return removedCount
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// truncateStringForLog truncates a string to a maximum length for logging
func truncateStringForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// messageKey creates a unique key for a message to compare duplicates
// Compares Role, Content, Name, ToolCallID, and ToolCalls
// This must be deterministic and consistent for the same message
func messageKey(msg openai.ChatCompletionMessage) string {
	var parts []string

	// Always include role (required field)
	parts = append(parts, "role:"+msg.Role)

	// Content (may be empty for tool calls)
	if msg.Content != "" {
		parts = append(parts, "content:"+msg.Content)
	} else {
		parts = append(parts, "content:")
	}

	// Name (optional, for tool messages)
	if msg.Name != "" {
		parts = append(parts, "name:"+msg.Name)
	}

	// ToolCallID (for tool result messages)
	if msg.ToolCallID != "" {
		parts = append(parts, "toolcallid:"+msg.ToolCallID)
	}

	// Add tool calls if present (must be sorted for consistency)
	if len(msg.ToolCalls) > 0 {
		// Sort tool calls by ID for consistent key generation
		toolCallKeys := make([]string, 0, len(msg.ToolCalls))
		for _, tc := range msg.ToolCalls {
			tcKey := fmt.Sprintf("tc:%s:%s", tc.ID, tc.Type)
			if tc.Function.Name != "" {
				tcKey += fmt.Sprintf(":fn:%s:%s", tc.Function.Name, tc.Function.Arguments)
			}
			toolCallKeys = append(toolCallKeys, tcKey)
		}
		// Sort for consistency
		sort.Strings(toolCallKeys)
		parts = append(parts, "toolcalls:"+strings.Join(toolCallKeys, ","))
	}

	// Add function call if present (legacy)
	if msg.FunctionCall != nil {
		parts = append(parts, fmt.Sprintf("fc:%s:%s", msg.FunctionCall.Name, msg.FunctionCall.Arguments))
	}

	return strings.Join(parts, "|")
}

// OpenAIClientWrapper wraps openai.Client to implement model.LLMClient interface
type OpenAIClientWrapper struct {
	Client *openai.Client
}

// CreateChatCompletion implements model.LLMClient interface
func (w *OpenAIClientWrapper) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return w.Client.CreateChatCompletion(ctx, request)
}
