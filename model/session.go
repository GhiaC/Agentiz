package model

import (
	"time"
)

// Session represents a user session in the agent system
type Session struct {
	// UserID identifies the user
	UserID string

	// SessionID is a unique identifier for this session
	SessionID string

	// CurrentNodePath is the path of the current node (e.g., "root", "root/next")
	CurrentNodePath string

	// OpenedFiles tracks which files have been loaded (for debugging/tracing)
	OpenedFiles []string

	// AccumulatedTools are all tools aggregated from root to current node
	AccumulatedTools []Tool

	// Memory stores conversation/interaction data
	Memory map[string]interface{}

	// NodeDigests stores lightweight information about visited nodes
	NodeDigests []NodeDigest

	// Metadata
	CreatedAt time.Time
	UpdatedAt time.Time
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
		UserID:           userID,
		SessionID:        generateSessionID(),
		CurrentNodePath:  "root",
		OpenedFiles:      []string{},
		AccumulatedTools: []Tool{},
		Memory:           make(map[string]interface{}),
		NodeDigests:      []NodeDigest{},
		CreatedAt:        now,
		UpdatedAt:        now,
	}
}

// generateSessionID generates a unique session ID
func generateSessionID() string {
	// Simple implementation - in production use uuid or similar
	return time.Now().Format("20060102150405") + "-" + randomString(8)
}

func randomString(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = charset[i%len(charset)]
	}
	return string(b)
}
