package model

import (
	"context"
	"fmt"
	"strings"
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
)

// Session represents a user session in the agent system
type Session struct {
	// UserID identifies the user
	UserID string

	// SessionID is a unique identifier for this session
	SessionID string

	// ConversationState stores conversation/interaction data
	ConversationState *ConversationState

	// NodeDigests stores lightweight information about visited nodes
	NodeDigests []NodeDigest

	// ToolResults stores tool execution results by unique ID (for large results)
	ToolResults map[string]string

	// Metadata
	CreatedAt time.Time
	UpdatedAt time.Time

	// Session organization and summarization fields
	Tags    []string // User-defined or auto-generated tags for categorization
	Title   string   // Session title (auto-generated or user-set)
	Summary string   // LLM-generated summary of the conversation

	// Summarization state
	SummarizedAt       time.Time                      // When the session was last summarized
	SummarizedMessages []openai.ChatCompletionMessage // Archived messages that have been summarized
	ExMsgs             []openai.ChatCompletionMessage // Exported messages (moved from Msgs after summarization, only for debug)

	// Agent type identifier (core, high, low)
	AgentType AgentType

	// Model name used in this session (e.g., "gpt-4o", "gpt-4o-mini")
	Model string
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

// NewSession creates a new session for a user
func NewSession(userID string) *Session {
	now := time.Now()
	return &Session{
		UserID:             userID,
		SessionID:          generateSessionID(userID),
		ConversationState:  NewConversationState(),
		NodeDigests:        []NodeDigest{},
		ToolResults:        make(map[string]string),
		CreatedAt:          now,
		UpdatedAt:          now,
		Tags:               []string{},
		Title:              "",
		Summary:            "",
		SummarizedMessages: []openai.ChatCompletionMessage{},
		ExMsgs:             []openai.ChatCompletionMessage{},
		AgentType:          "",
		Model:              "",
	}
}

// NewSessionWithType creates a new session for a user with a specific agent type
func NewSessionWithType(userID string, agentType AgentType) *Session {
	session := NewSession(userID)
	session.AgentType = agentType
	return session
}

// generateSessionID generates a unique session ID
// Format: {userID}-{YYMMDD}-{random4}
func generateSessionID(userID string) string {
	date := time.Now().Format("060102") // YYMMDD
	return userID + "-" + date + "-" + randomString(4)
}

func randomString(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	nano := time.Now().UnixNano()
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[(nano+int64(i*7))%int64(len(charset))]
	}
	return string(b)
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
		model = "gpt-4o-mini"
	}

	// Ensure user_id is in context
	ctx = WithUserID(ctx, s.UserID)

	// Get conversation text from messages
	// Note: Only use Summary, Tags, and Msgs for usage. ExMsgs is only for debug purposes.
	var conversationText string
	allMessages := append(s.SummarizedMessages, s.ConversationState.Msgs...)
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
