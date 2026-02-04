package model

import (
	"time"

	"github.com/sashabaranov/go-openai"
)

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

	// Agent type identifier (core, high, low)
	AgentType AgentType
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
		AgentType:          "",
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
