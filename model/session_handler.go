package model

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/sashabaranov/go-openai"
)

// SessionStore defines the interface for session storage (pluggable)
type SessionStore interface {
	Get(sessionID string) (*Session, error)
	Put(session *Session) error
	Delete(sessionID string) error
	List(userID string) ([]*Session, error)
}

// LLMClient defines the interface for LLM operations (for summarization)
type LLMClient interface {
	CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error)
}

// SessionHandlerConfig holds configuration for SessionHandler
type SessionHandlerConfig struct {
	AutoSummarizeThreshold int    // Number of messages before auto-summarize (default: 20)
	SummaryModel           string // LLM model for summarization (default: gpt-4o-mini)
	SummaryMaxTokens       int    // Max tokens for summary (default: 200)
}

// DefaultSessionHandlerConfig returns default configuration
func DefaultSessionHandlerConfig() SessionHandlerConfig {
	return SessionHandlerConfig{
		AutoSummarizeThreshold: 20,
		SummaryModel:           "gpt-4o-mini",
		SummaryMaxTokens:       200,
	}
}

// SessionHandler manages sessions for users across different agent types
type SessionHandler struct {
	store     SessionStore // Pluggable storage backend
	llmClient LLMClient    // For summarization
	config    SessionHandlerConfig

	// In-memory index for quick lookups
	userIndex map[string][]string // userID -> []sessionID
	mu        sync.RWMutex
}

// GetStore returns the underlying SessionStore for direct access
func (sh *SessionHandler) GetStore() SessionStore {
	return sh.store
}

// NewSessionHandler creates a new SessionHandler with the given store
func NewSessionHandler(store SessionStore, config SessionHandlerConfig) *SessionHandler {
	if config.AutoSummarizeThreshold <= 0 {
		config.AutoSummarizeThreshold = 20
	}
	if config.SummaryModel == "" {
		config.SummaryModel = "gpt-4o-mini"
	}
	if config.SummaryMaxTokens <= 0 {
		config.SummaryMaxTokens = 200
	}

	return &SessionHandler{
		store:     store,
		config:    config,
		userIndex: make(map[string][]string),
	}
}

// SetLLMClient sets the LLM client for summarization
func (sh *SessionHandler) SetLLMClient(client LLMClient) {
	sh.llmClient = client
}

// GetLLMClient returns the current LLM client
func (sh *SessionHandler) GetLLMClient() LLMClient {
	return sh.llmClient
}

// CreateSession creates a new session for a user with the specified agent type
func (sh *SessionHandler) CreateSession(userID string, agentType AgentType) (*Session, error) {
	session := NewSessionWithType(userID, agentType)

	if err := sh.store.Put(session); err != nil {
		return nil, fmt.Errorf("failed to store session: %w", err)
	}

	// Update index
	sh.mu.Lock()
	sh.userIndex[userID] = append(sh.userIndex[userID], session.SessionID)
	sh.mu.Unlock()

	// Log session creation
	log.Log.Infof("[SessionHandler] ✅ Created new session | UserID: %s | SessionID: %s | AgentType: %s", userID, session.SessionID, agentType)

	// Log total sessions for this user
	allSessions, _ := sh.store.List(userID)
	log.Log.Infof("[SessionHandler] 📊 Total sessions for user %s: %d", userID, len(allSessions))

	return session, nil
}

// GetSession retrieves a session by ID
func (sh *SessionHandler) GetSession(sessionID string) (*Session, error) {
	session, err := sh.store.Get(sessionID)
	if err != nil {
		log.Log.Warnf("[SessionHandler] ⚠️  Session not found | SessionID: %s | Error: %v", sessionID, err)
		return nil, err
	}
	log.Log.Infof("[SessionHandler] 🔍 Retrieved session | SessionID: %s | UserID: %s | AgentType: %s | Title: %s",
		sessionID, session.UserID, session.AgentType, getSessionTitle(session))
	return session, nil
}

// getSessionTitle returns the session title or "Untitled"
func getSessionTitle(s *Session) string {
	if s.Title != "" {
		return s.Title
	}
	return "Untitled"
}

// ListUserSessions returns all sessions for a user
func (sh *SessionHandler) ListUserSessions(userID string) ([]*Session, error) {
	sessions, err := sh.store.List(userID)
	if err != nil {
		log.Log.Errorf("[SessionHandler] ❌ Failed to list sessions | UserID: %s | Error: %v", userID, err)
		return nil, err
	}

	log.Log.Infof("[SessionHandler] 📋 Listing sessions | UserID: %s | Total: %d", userID, len(sessions))

	// Group by agent type for better visibility
	byType := make(map[AgentType]int)
	totalActiveMessages := 0
	totalArchivedMessages := 0

	for _, s := range sessions {
		byType[s.AgentType]++
		activeMsgs := len(s.ConversationState.Msgs)
		archivedMsgs := len(s.SummarizedMessages)
		totalActiveMessages += activeMsgs
		totalArchivedMessages += archivedMsgs

		// Log detailed session info
		title := s.Title
		if title == "" {
			title = "Untitled"
		}
		timeAgo := formatTimeAgo(s.UpdatedAt)

		log.Log.Infof("[SessionHandler]   ├─ [%s] %s | Title: \"%s\" | Active: %d msgs | Archived: %d msgs | Last: %s",
			s.SessionID, agentTypeDisplayName(s.AgentType), title, activeMsgs, archivedMsgs, timeAgo)
	}

	// Summary by type
	for agentType, count := range byType {
		log.Log.Infof("[SessionHandler]   └─ %s sessions: %d", agentTypeDisplayName(agentType), count)
	}

	// Overall summary
	log.Log.Infof("[SessionHandler] 📊 Sessions Summary | Total: %d | Active messages: %d | Archived messages: %d",
		len(sessions), totalActiveMessages, totalArchivedMessages)

	return sessions, nil
}

// ListUserSessionsByType returns sessions for a user filtered by agent type
func (sh *SessionHandler) ListUserSessionsByType(userID string, agentType AgentType) ([]*Session, error) {
	allSessions, err := sh.store.List(userID)
	if err != nil {
		return nil, err
	}

	var filtered []*Session
	for _, s := range allSessions {
		if s.AgentType == agentType {
			filtered = append(filtered, s)
		}
	}

	log.Log.Infof("[SessionHandler] 🔎 Filtered sessions | UserID: %s | AgentType: %s | Found: %d (out of %d total)",
		userID, agentType, len(filtered), len(allSessions))

	return filtered, nil
}

// DeleteSession removes a session
func (sh *SessionHandler) DeleteSession(sessionID string) error {
	session, err := sh.store.Get(sessionID)
	if err != nil {
		return err
	}

	// Remove from index
	sh.mu.Lock()
	if sessions, ok := sh.userIndex[session.UserID]; ok {
		for i, sid := range sessions {
			if sid == sessionID {
				sh.userIndex[session.UserID] = append(sessions[:i], sessions[i+1:]...)
				break
			}
		}
	}
	sh.mu.Unlock()

	return sh.store.Delete(sessionID)
}

// UpdateSessionMetadata updates the title, tags, and summary of a session
func (sh *SessionHandler) UpdateSessionMetadata(sessionID string, title string, tags []string, summary string) error {
	session, err := sh.store.Get(sessionID)
	if err != nil {
		return err
	}

	if title != "" {
		session.Title = title
	}
	if tags != nil {
		session.Tags = tags
	}
	if summary != "" {
		session.Summary = summary
	}
	session.UpdatedAt = time.Now()

	return sh.store.Put(session)
}

// AddMessage adds a message to a session and checks for auto-summarization
func (sh *SessionHandler) AddMessage(ctx context.Context, sessionID string, msg openai.ChatCompletionMessage) error {
	session, err := sh.store.Get(sessionID)
	if err != nil {
		return err
	}

	session.ConversationState.Msgs = append(session.ConversationState.Msgs, msg)
	session.ConversationState.LastActivity = time.Now()
	session.UpdatedAt = time.Now()

	if err := sh.store.Put(session); err != nil {
		return err
	}

	// Check for auto-summarization
	if len(session.ConversationState.Msgs) >= sh.config.AutoSummarizeThreshold {
		go func() {
			if err := sh.SummarizeSession(ctx, sessionID); err != nil {
				// Log error but don't block
				fmt.Printf("auto-summarization failed for session %s: %v\n", sessionID, err)
			}
		}()
	}

	return nil
}

// SummarizeSession generates a summary of the conversation and archives messages
func (sh *SessionHandler) SummarizeSession(ctx context.Context, sessionID string) error {
	if sh.llmClient == nil {
		return fmt.Errorf("LLM client not configured")
	}

	session, err := sh.store.Get(sessionID)
	if err != nil {
		return err
	}

	// Skip if no messages to summarize
	if len(session.ConversationState.Msgs) == 0 {
		return nil
	}

	// Format messages for summarization
	conversationText := formatMessagesForSummary(session.ConversationState.Msgs)

	// Add user_id to context for LLM calls
	if session.UserID != "" {
		ctx = WithUserID(ctx, session.UserID)
	}

	// Create log entry before making the request
	summLog := NewSummarizationLog(sessionID, session.UserID)
	summLog.ModelUsed = sh.config.SummaryModel
	summLog.Status = "pending"
	// PromptSent will be set in generateConversationSummary with full prompt

	// Try to save log if store supports it
	if debugStore, ok := sh.store.(interface {
		PutSummarizationLog(log *SummarizationLog) error
	}); ok {
		if err := debugStore.PutSummarizationLog(summLog); err != nil {
			log.Log.Warnf("[SessionHandler] ⚠️  Failed to save summarization log: %v", err)
		} else {
			log.Log.Infof("[SessionHandler] ✅ Saved summarization log (pending) | LogID: %s | SessionID: %s", summLog.LogID, sessionID)
		}
	} else {
		log.Log.Warnf("[SessionHandler] ⚠️  Store does not implement PutSummarizationLog, skipping log")
	}

	// Generate summary using LLM
	summary, err := sh.generateConversationSummary(ctx, conversationText, summLog)
	if err != nil {
		// Update log with error
		summLog.Status = "failed"
		summLog.ErrorMessage = err.Error()
		if debugStore, ok := sh.store.(interface {
			PutSummarizationLog(log *SummarizationLog) error
		}); ok {
			_ = debugStore.PutSummarizationLog(summLog)
		}
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	// Generate title if not set
	if session.Title == "" {
		title, err := sh.generateSessionTitle(ctx, conversationText)
		if err == nil {
			session.Title = title
		}
	}

	// Archive messages and update session
	session.SummarizedMessages = append(session.SummarizedMessages, session.ConversationState.Msgs...)
	session.ConversationState.Msgs = []openai.ChatCompletionMessage{}
	session.Summary = summary
	session.SummarizedAt = time.Now()
	session.UpdatedAt = time.Now()

	return sh.store.Put(session)
}

// GetSessionsPrompt generates a formatted prompt showing all user sessions
// This is used by CoreHandler to understand the user's session history
// Note: Only uses Summary, Tags, and Msgs from sessions. ExMsgs is only for debug purposes and is not used here.
func (sh *SessionHandler) GetSessionsPrompt(userID string) (string, error) {
	sessions, err := sh.store.List(userID)
	if err != nil {
		return "", err
	}

	if len(sessions) == 0 {
		return "## Active Sessions\n\nNo active sessions for this user.", nil
	}

	// Group sessions by agent type
	byType := make(map[AgentType][]*Session)
	for _, s := range sessions {
		agentType := s.AgentType
		if agentType == "" {
			agentType = "unknown"
		}
		byType[agentType] = append(byType[agentType], s)
	}

	// Sort sessions within each group by UpdatedAt (most recent first)
	for _, typeSessions := range byType {
		sort.Slice(typeSessions, func(i, j int) bool {
			return typeSessions[i].UpdatedAt.After(typeSessions[j].UpdatedAt)
		})
	}

	var sb strings.Builder
	sb.WriteString("## Active Sessions\n\n")

	// Order: high, low, core, others
	typeOrder := []AgentType{AgentTypeHigh, AgentTypeLow, AgentTypeCore}
	processedTypes := make(map[AgentType]bool)

	for _, agentType := range typeOrder {
		if typeSessions, ok := byType[agentType]; ok {
			sb.WriteString(fmt.Sprintf("### %s Sessions:\n", agentTypeDisplayName(agentType)))
			for i, s := range typeSessions {
				sh.formatSessionEntry(&sb, i+1, s)
			}
			sb.WriteString("\n")
			processedTypes[agentType] = true
		}
	}

	// Process any remaining types
	for agentType, typeSessions := range byType {
		if processedTypes[agentType] {
			continue
		}
		sb.WriteString(fmt.Sprintf("### %s Sessions:\n", agentTypeDisplayName(agentType)))
		for i, s := range typeSessions {
			sh.formatSessionEntry(&sb, i+1, s)
		}
		sb.WriteString("\n")
	}

	return sb.String(), nil
}

// formatSessionEntry formats a single session entry for the prompt
func (sh *SessionHandler) formatSessionEntry(sb *strings.Builder, index int, s *Session) {
	title := s.Title
	if title == "" {
		title = "Untitled"
	}

	// Calculate time ago
	timeAgo := formatTimeAgo(s.UpdatedAt)

	sb.WriteString(fmt.Sprintf("%d. [%s] \"%s\" - Last: %s\n", index, s.SessionID, title, timeAgo))

	if s.Summary != "" {
		sb.WriteString(fmt.Sprintf("   Summary: %s\n", s.Summary))
	}

	if len(s.Tags) > 0 {
		sb.WriteString(fmt.Sprintf("   Tags: %s\n", strings.Join(s.Tags, ", ")))
	}

	// Show message count
	msgCount := len(s.ConversationState.Msgs)
	archivedCount := len(s.SummarizedMessages)
	sb.WriteString(fmt.Sprintf("   Messages: %d active, %d archived\n", msgCount, archivedCount))
}

// generateConversationSummary uses LLM to generate a summary of the conversation
func (sh *SessionHandler) generateConversationSummary(ctx context.Context, conversationText string, summLog *SummarizationLog) (string, error) {
	systemPrompt := `You are a conversation summarizer.
Generate a concise summary (2-3 sentences) that captures the main topics and outcomes of this conversation.

Requirements:
- Focus on key topics discussed and any decisions or conclusions reached
- Be specific about what was accomplished or discussed
- Maximum 200 characters
- Use present or past tense appropriately

Example: "Debugged Kubernetes pod restart issue. Found memory limits too low. Applied fix and verified pod stability."
`

	fullPrompt := systemPrompt + "\n\nSummarize this conversation:\n\n" + conversationText
	summLog.PromptSent = fullPrompt

	resp, err := sh.llmClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: sh.config.SummaryModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Summarize this conversation:\n\n" + conversationText},
		},
		MaxTokens: sh.config.SummaryMaxTokens,
	})

	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
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
	if debugStore, ok := sh.store.(interface {
		PutSummarizationLog(log *SummarizationLog) error
	}); ok {
		if err := debugStore.PutSummarizationLog(summLog); err != nil {
			log.Log.Warnf("[SessionHandler] ⚠️  Failed to update summarization log: %v", err)
		} else {
			log.Log.Infof("[SessionHandler] ✅ Updated summarization log (success) | LogID: %s | SessionID: %s | Tokens: %d", summLog.LogID, summLog.SessionID, summLog.TotalTokens)
		}
	}

	return summary, nil
}

// generateSessionTitle uses LLM to generate a title for the session
func (sh *SessionHandler) generateSessionTitle(ctx context.Context, conversationText string) (string, error) {
	systemPrompt := `Generate a short title (3-5 words) for this conversation.
The title should capture the main topic or purpose.
Return only the title, no quotes or extra text.

Example outputs:
- Kubernetes Pod Debugging
- API Authentication Design
- Database Migration Planning`

	resp, err := sh.llmClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: sh.config.SummaryModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Generate a title for this conversation:\n\n" + conversationText},
		},
		MaxTokens: 20,
	})

	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// Helper functions

// formatMessagesForSummary converts messages to a readable format for summarization
func formatMessagesForSummary(msgs []openai.ChatCompletionMessage) string {
	var sb strings.Builder
	for _, msg := range msgs {
		role := msg.Role
		content := msg.Content

		// Skip tool-related messages for summary
		if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 {
			continue
		}

		if content == "" {
			continue
		}

		// Truncate long messages
		if len(content) > 500 {
			content = content[:500] + "..."
		}

		sb.WriteString(fmt.Sprintf("%s: %s\n", role, content))
	}
	return sb.String()
}

// formatTimeAgo formats a time as a human-readable "time ago" string
func formatTimeAgo(t time.Time) string {
	duration := time.Since(t)

	switch {
	case duration < time.Minute:
		return "just now"
	case duration < time.Hour:
		mins := int(duration.Minutes())
		if mins == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", mins)
	case duration < 24*time.Hour:
		hours := int(duration.Hours())
		if hours == 1 {
			return "1h ago"
		}
		return fmt.Sprintf("%dh ago", hours)
	default:
		days := int(duration.Hours() / 24)
		if days == 1 {
			return "1d ago"
		}
		return fmt.Sprintf("%dd ago", days)
	}
}

// agentTypeDisplayName returns a human-readable name for the agent type
func agentTypeDisplayName(agentType AgentType) string {
	switch agentType {
	case AgentTypeHigh:
		return "UserAgent-High"
	case AgentTypeLow:
		return "UserAgent-Low"
	case AgentTypeCore:
		return "Core"
	default:
		return string(agentType)
	}
}
