package model

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

// Context key for user ID
type userIDKey struct{}

// WithUserID adds user_id to context
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey{}, userID)
}

// GetUserIDFromContext retrieves user_id from context
func GetUserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDKey{}).(string)
	return userID, ok
}

// AgentType represents the type of agent that owns a session
type AgentType string

const (
	AgentTypeCore AgentType = "core"
	AgentTypeHigh AgentType = "high"
	AgentTypeLow  AgentType = "low"
	AgentTypeUser AgentType = "user"
)

// Session represents a user session in the agent system
// All fields are flattened for simple database storage and loading
type Session struct {
	// ==================== Identifiers ====================
	UserID    string
	SessionID string
	AgentType AgentType // core, high, low, user
	Model     string    // LLM model name (e.g., "gpt-4o", "gpt-4o-mini")

	// ==================== Messages (flattened from ConversationState) ====================
	// Msgs contains the active conversation messages
	Msgs []openai.ChatCompletionMessage

	// ArchivedMsgs contains messages that have been summarized and moved out of active conversation
	// (Previously was both SummarizedMessages and ExMsgs - now unified)
	ArchivedMsgs []openai.ChatCompletionMessage

	// ==================== Runtime State (not persisted to database) ====================
	// InProgress indicates if a message is currently being processed
	InProgress bool `bson:"-" json:"-"`

	// Queue holds messages waiting to be processed
	Queue []openai.ChatCompletionMessage `bson:"-" json:"-"`

	// ==================== Knowledge/Tools ====================
	// NodeDigests stores lightweight information about visited nodes
	NodeDigests []NodeDigest

	// ToolResults stores tool execution results by unique ID (for large results)
	ToolResults map[string]string

	// ==================== Timestamps ====================
	CreatedAt    time.Time
	UpdatedAt    time.Time // Also serves as LastActivity
	SummarizedAt time.Time // When the session was last summarized

	// ==================== Summarization ====================
	Tags    []string // User-defined or auto-generated tags for categorization
	Title   string   // Session title (auto-generated or user-set)
	Summary string   // LLM-generated summary of the conversation

	// ==================== Sequences ====================
	MessageSeq          int // Sequence counter for messages
	ToolSeq             int // Sequence counter for tool calls
	OpenedFileSeq       int // Sequence counter for opened files
	SummarizationLogSeq int // Sequence counter for summarization logs

	// ==================== Internal (not persisted) ====================
	seqMu sync.Mutex `bson:"-" json:"-"` // Mutex for thread-safe sequence operations
}

// NodeDigest is a lightweight representation of a node (for memory efficiency)
type NodeDigest struct {
	Path     string
	ID       string
	Title    string
	Hash     string
	LoadedAt time.Time
	Excerpt  string // First 100 chars of content
}

// NewSessionWithID creates a new session with a pre-generated session ID
// This is the preferred method when you have the session ID already (e.g., from store.GetNextSessionSeq)
func NewSessionWithID(userID string, sessionID string, agentType AgentType) *Session {
	now := time.Now()
	return &Session{
		UserID:              userID,
		SessionID:           sessionID,
		AgentType:           agentType,
		Model:               "",
		Msgs:                []openai.ChatCompletionMessage{},
		ArchivedMsgs:        []openai.ChatCompletionMessage{},
		InProgress:          false,
		Queue:               []openai.ChatCompletionMessage{},
		NodeDigests:         []NodeDigest{},
		ToolResults:         make(map[string]string),
		CreatedAt:           now,
		UpdatedAt:           now,
		Tags:                []string{},
		Title:               "",
		Summary:             "",
		MessageSeq:          0,
		ToolSeq:             0,
		OpenedFileSeq:       0,
		SummarizationLogSeq: 0,
	}
}

// NewSessionForUser creates a new session for a user with proper sequential ID
// Format: {UserID}-{AgentType}-s{SeqCounter}
// This method uses User.NextSessionSeq for sequence generation
// Note: user must not be nil - caller should check before calling
func NewSessionForUser(user *User, agentType AgentType) *Session {
	if user == nil {
		panic("NewSessionForUser: user cannot be nil")
	}

	seq := user.NextSessionSeq(agentType)
	sessionID := GenerateSessionID(user.UserID, agentType, seq)
	return NewSessionWithID(user.UserID, sessionID, agentType)
}

// NewSessionWithType creates a new session for a user with a specific agent type
// This is a convenience function for tests and simple use cases
// For production, prefer using SessionHandler.CreateSession or NewSessionWithID
func NewSessionWithType(userID string, agentType AgentType) *Session {
	// Use seq=1 for simple initialization (tests, local dev)
	sessionID := GenerateSessionID(userID, agentType, 1)
	return NewSessionWithID(userID, sessionID, agentType)
}

// GenerateSessionID generates a session ID with the new format
// Format: {UserID}-{AgentType}-s{SeqCounter}
// Example: user123-core-s0001, user123-low-s0002
func GenerateSessionID(userID string, agentType AgentType, seq int) string {
	agentShort := agentTypeShortCode(agentType)
	return fmt.Sprintf("%s-%s-s%04d", userID, agentShort, seq)
}

// agentTypeShortCode returns short code for agent type
func agentTypeShortCode(agentType AgentType) string {
	switch agentType {
	case AgentTypeCore:
		return "core"
	case AgentTypeHigh:
		return "high"
	case AgentTypeLow:
		return "low"
	default:
		return "unk"
	}
}

// NextMessageSeq increments and returns the next message sequence number
// Thread-safe via mutex
func (s *Session) NextMessageSeq() int {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.MessageSeq++
	return s.MessageSeq
}

// GenerateMessageID generates a unique message ID for this session
// Format: {SessionID}-{SeqID}
// Thread-safe via mutex
func (s *Session) GenerateMessageID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.MessageSeq++
	return fmt.Sprintf("%s-m%04d", s.SessionID, s.MessageSeq)
}

// GenerateMessageIDWithSeq generates a unique message ID and returns both the ID and sequence number
// Format: {SessionID}-m{SeqID}
// Returns: (messageID, seqID)
// Thread-safe via mutex
func (s *Session) GenerateMessageIDWithSeq() (string, int) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.MessageSeq++
	messageID := fmt.Sprintf("%s-m%04d", s.SessionID, s.MessageSeq)
	return messageID, s.MessageSeq
}

// NextToolSeq increments and returns the next tool sequence number
// Thread-safe via mutex
func (s *Session) NextToolSeq() int {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.ToolSeq++
	return s.ToolSeq
}

// GenerateToolID generates a unique tool ID for this session
// Format: {SessionID}-t{SeqID}
// Thread-safe via mutex
func (s *Session) GenerateToolID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.ToolSeq++
	return fmt.Sprintf("%s-t%04d", s.SessionID, s.ToolSeq)
}

// GenerateToolIDWithSeq generates a unique tool ID and returns both the ID and sequence number
// Format: {SessionID}-t{SeqID}
// Returns: (toolID, seqID)
// Thread-safe via mutex
func (s *Session) GenerateToolIDWithSeq() (string, int) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.ToolSeq++
	toolID := fmt.Sprintf("%s-t%04d", s.SessionID, s.ToolSeq)
	return toolID, s.ToolSeq
}

// GenerateFileID generates a unique file ID for this session
// Format: {SessionID}-f{SeqID}
// Thread-safe via mutex
func (s *Session) GenerateFileID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.OpenedFileSeq++
	return fmt.Sprintf("%s-f%04d", s.SessionID, s.OpenedFileSeq)
}

// GenerateFileIDWithSeq generates a unique file ID and returns both the ID and sequence number
// Format: {SessionID}-f{SeqID}
// Returns: (fileID, seqID)
// Thread-safe via mutex
func (s *Session) GenerateFileIDWithSeq() (string, int) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.OpenedFileSeq++
	fileID := fmt.Sprintf("%s-f%04d", s.SessionID, s.OpenedFileSeq)
	return fileID, s.OpenedFileSeq
}

// GenerateSummarizationLogID generates a unique summarization log ID for this session
// Format: {SessionID}-l{SeqID}
// Thread-safe via mutex
func (s *Session) GenerateSummarizationLogID() string {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.SummarizationLogSeq++
	return fmt.Sprintf("%s-l%04d", s.SessionID, s.SummarizationLogSeq)
}

// GenerateSummarizationLogIDWithSeq generates a unique summarization log ID and returns both
// Format: {SessionID}-l{SeqID}
// Returns: (logID, seqID)
// Thread-safe via mutex
func (s *Session) GenerateSummarizationLogIDWithSeq() (string, int) {
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	s.SummarizationLogSeq++
	logID := fmt.Sprintf("%s-l%04d", s.SessionID, s.SummarizationLogSeq)
	return logID, s.SummarizationLogSeq
}

// ==================== Backward Compatibility Methods ====================

// GetConversationState returns a ConversationState-like view of the session
// Deprecated: Access Msgs, InProgress, Queue, UpdatedAt directly on Session
func (s *Session) GetConversationState() *ConversationState {
	return &ConversationState{
		Msgs:         s.Msgs,
		InProgress:   s.InProgress,
		Queue:        s.Queue,
		LastActivity: s.UpdatedAt,
	}
}

// GetExMsgs returns ArchivedMsgs (for backward compatibility with debugger)
// Deprecated: Use ArchivedMsgs directly
func (s *Session) GetExMsgs() []openai.ChatCompletionMessage {
	return s.ArchivedMsgs
}

// GetSummarizedMessages returns ArchivedMsgs (for backward compatibility)
// Deprecated: Use ArchivedMsgs directly
func (s *Session) GetSummarizedMessages() []openai.ChatCompletionMessage {
	return s.ArchivedMsgs
}

// Clone creates a deep copy of the session
// This is safe to use when you need to copy a session without copying the mutex
func (s *Session) Clone() *Session {
	// Create a new session with the same values
	clone := &Session{
		UserID:              s.UserID,
		SessionID:           s.SessionID,
		AgentType:           s.AgentType,
		Model:               s.Model,
		InProgress:          s.InProgress,
		CreatedAt:           s.CreatedAt,
		UpdatedAt:           s.UpdatedAt,
		SummarizedAt:        s.SummarizedAt,
		Title:               s.Title,
		Summary:             s.Summary,
		MessageSeq:          s.MessageSeq,
		ToolSeq:             s.ToolSeq,
		OpenedFileSeq:       s.OpenedFileSeq,
		SummarizationLogSeq: s.SummarizationLogSeq,
		// seqMu is NOT copied - new mutex for the clone
	}

	// Copy slices
	if s.Msgs != nil {
		clone.Msgs = make([]openai.ChatCompletionMessage, len(s.Msgs))
		copy(clone.Msgs, s.Msgs)
	}
	if s.ArchivedMsgs != nil {
		clone.ArchivedMsgs = make([]openai.ChatCompletionMessage, len(s.ArchivedMsgs))
		copy(clone.ArchivedMsgs, s.ArchivedMsgs)
	}
	if s.Queue != nil {
		clone.Queue = make([]openai.ChatCompletionMessage, len(s.Queue))
		copy(clone.Queue, s.Queue)
	}
	if s.NodeDigests != nil {
		clone.NodeDigests = make([]NodeDigest, len(s.NodeDigests))
		copy(clone.NodeDigests, s.NodeDigests)
	}
	if s.Tags != nil {
		clone.Tags = make([]string, len(s.Tags))
		copy(clone.Tags, s.Tags)
	}

	// Copy map
	if s.ToolResults != nil {
		clone.ToolResults = make(map[string]string, len(s.ToolResults))
		for k, v := range s.ToolResults {
			clone.ToolResults[k] = v
		}
	}

	return clone
}

// LLMClientWithUserID wraps LLMClient to add user_id header to all requests
type LLMClientWithUserID struct {
	Client LLMClient
	UserID string
}

// CreateChatCompletion adds user_id header to the request
func (c *LLMClientWithUserID) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	// Add user_id to context if not already present
	if _, exists := GetUserIDFromContext(ctx); !exists {
		ctx = WithUserID(ctx, c.UserID)
	}

	// Create a copy of the request to modify headers
	// Note: openai.ChatCompletionRequest doesn't have a Headers field directly,
	// so we need to wrap the HTTP client. For now, we'll use context.
	// The actual header injection should be done at the HTTP client level.
	return c.Client.CreateChatCompletion(ctx, request)
}

// PopulateFields uses LLM to populate Title, Summary, and Tags fields of the session
// It requires an LLMClient and a model name
func (s *Session) PopulateFields(ctx context.Context, client LLMClient, model string) error {
	if client == nil {
		return fmt.Errorf("LLM client is required")
	}

	if model == "" {
		model = "openai/gpt-5-nano"
	}

	// Ensure user_id is in context
	ctx = WithUserID(ctx, s.UserID)

	// Get conversation text from messages
	// Uses ArchivedMsgs (previously summarized) + current Msgs
	var conversationText string
	allMessages := append(s.ArchivedMsgs, s.Msgs...)
	if len(allMessages) == 0 {
		return fmt.Errorf("no messages in session to populate fields")
	}

	// Format messages for LLM
	for _, msg := range allMessages {
		// Skip tool-related messages
		if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 {
			continue
		}
		if msg.Content == "" {
			continue
		}
		conversationText += fmt.Sprintf("%s: %s\n", msg.Role, msg.Content)
	}

	// Generate Title
	if s.Title == "" {
		title, err := s.generateTitle(ctx, client, model, conversationText)
		if err != nil {
			return fmt.Errorf("failed to generate title: %w", err)
		}
		s.Title = title
	}

	// Generate Summary
	if s.Summary == "" {
		summary, err := s.generateSummary(ctx, client, model, conversationText)
		if err != nil {
			return fmt.Errorf("failed to generate summary: %w", err)
		}
		s.Summary = summary
	}

	// Generate Tags
	if len(s.Tags) == 0 {
		tags, err := s.generateTags(ctx, client, model, conversationText)
		if err != nil {
			return fmt.Errorf("failed to generate tags: %w", err)
		}
		s.Tags = tags
	}

	// Update UpdatedAt timestamp
	s.UpdatedAt = time.Now()

	return nil
}

// generateTitle generates a title for the session
func (s *Session) generateTitle(ctx context.Context, client LLMClient, model string, conversationText string) (string, error) {
	systemPrompt := `Generate a short title (3-5 words) for this conversation.
The title should capture the main topic or purpose.
Return only the title, no quotes or extra text.

Example outputs:
- Kubernetes Pod Debugging
- API Authentication Design
- Database Migration Planning
- Quick Q&A Session`

	// Truncate conversation if too long
	if len(conversationText) > 300 {
		conversationText = conversationText[:300] + "..."
	}

	request := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Generate a title for this conversation:\n\n" + conversationText},
		},
		MaxTokens: 20,
	}

	// Add user_id to request headers via context
	resp, err := client.CreateChatCompletion(ctx, request)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// generateSummary generates a summary for the session
func (s *Session) generateSummary(ctx context.Context, client LLMClient, model string, conversationText string) (string, error) {
	systemPrompt := `You are a conversation summarizer.
Generate a concise summary (2-3 sentences) that captures the main topics and outcomes of this conversation.

Requirements:
- Focus on key topics discussed and any decisions or conclusions reached
- Be specific about what was accomplished or discussed
- Maximum 200 characters
- Use present or past tense appropriately
- Do not include greetings or filler content

Example: "Debugged Kubernetes pod restart issue. Found memory limits too low. Applied fix and verified pod stability."`

	// Truncate conversation if too long
	if len(conversationText) > 300 {
		conversationText = conversationText[:300] + "..."
	}

	request := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Summarize this conversation:\n\n" + conversationText},
		},
		MaxTokens: 200,
	}

	resp, err := client.CreateChatCompletion(ctx, request)
	if err != nil {
		return "", err
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// TODO Refactor This Function to move better place in llmutils
// generateTags generates tags for the session
func (s *Session) generateTags(ctx context.Context, client LLMClient, model string, conversationText string) ([]string, error) {
	systemPrompt := `You are a conversation tagger.
Generate 2-5 relevant tags for this conversation that help categorize it.

Requirements:
- Tags should be short (1-3 words each)
- Focus on main topics, technologies, or problem domains
- Use lowercase, hyphenated format (e.g., "kubernetes", "api-design", "debugging")
- Return only the tags, comma-separated, no quotes or extra text
- Maximum 5 tags

Example outputs:
- kubernetes, debugging, pods
- api-design, authentication, security
- database, migration, postgresql`

	// Truncate conversation if too long
	if len(conversationText) > 300 {
		conversationText = conversationText[:300] + "..."
	}

	request := openai.ChatCompletionRequest{
		Model: model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: "Generate tags for this conversation:\n\n" + conversationText},
		},
		MaxTokens: 50,
	}

	resp, err := client.CreateChatCompletion(ctx, request)
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from LLM")
	}

	// Parse tags from response
	tagsStr := strings.TrimSpace(resp.Choices[0].Message.Content)
	tagsStr = strings.Trim(tagsStr, "\"'")
	tags := strings.Split(tagsStr, ",")

	// Clean and trim tags
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		tag = strings.ToLower(tag)
		if tag != "" {
			result = append(result, tag)
		}
	}

	return result, nil
}
