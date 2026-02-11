package store

import (
	"os"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

func TestSQLiteStore_BasicOperations(t *testing.T) {
	// Use a temporary file for testing
	tmpFile := "/tmp/agentize_test.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	// Create a test session
	session := model.NewSessionWithType("user123", model.AgentTypeCore)
	session.Title = "Test Session"
	session.Tags = []string{"test", "demo"}

	// Test Put
	if err := store.Put(session); err != nil {
		t.Fatalf("Failed to put session: %v", err)
	}

	// Test Get
	retrieved, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if retrieved.SessionID != session.SessionID {
		t.Errorf("SessionID mismatch: got %s, want %s", retrieved.SessionID, session.SessionID)
	}
	if retrieved.UserID != session.UserID {
		t.Errorf("UserID mismatch: got %s, want %s", retrieved.UserID, session.UserID)
	}
	if retrieved.Title != session.Title {
		t.Errorf("Title mismatch: got %s, want %s", retrieved.Title, session.Title)
	}
	if retrieved.AgentType != session.AgentType {
		t.Errorf("AgentType mismatch: got %s, want %s", retrieved.AgentType, session.AgentType)
	}

	// Test List
	sessions, err := store.List("user123")
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("Expected 1 session, got %d", len(sessions))
	}

	// Test Update
	session.Title = "Updated Title"
	if err := store.Put(session); err != nil {
		t.Fatalf("Failed to update session: %v", err)
	}

	retrieved, err = store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Failed to get updated session: %v", err)
	}
	if retrieved.Title != "Updated Title" {
		t.Errorf("Title not updated: got %s, want Updated Title", retrieved.Title)
	}

	// Test Delete
	if err := store.Delete(session.SessionID); err != nil {
		t.Fatalf("Failed to delete session: %v", err)
	}

	_, err = store.Get(session.SessionID)
	if err == nil {
		t.Error("Expected error when getting deleted session, got nil")
	}
}

func TestSQLiteStore_ConversationState(t *testing.T) {
	tmpFile := "/tmp/agentize_test_conv.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	session := model.NewSessionWithType("user123", model.AgentTypeCore)

	// Add messages to session
	session.Msgs = []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleUser,
			Content: "Hello",
		},
		{
			Role:    openai.ChatMessageRoleAssistant,
			Content: "Hi there!",
		},
	}

	if err := store.Put(session); err != nil {
		t.Fatalf("Failed to put session: %v", err)
	}

	retrieved, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if len(retrieved.Msgs) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(retrieved.Msgs))
	}
	if retrieved.Msgs[0].Content != "Hello" {
		t.Errorf("First message content mismatch: got %s, want Hello", retrieved.Msgs[0].Content)
	}
}

func TestSQLiteStore_NodeDigests(t *testing.T) {
	tmpFile := "/tmp/agentize_test_nodes.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	session := model.NewSessionWithType("user123", model.AgentTypeCore)
	session.NodeDigests = []model.NodeDigest{
		{
			Path:     "root/node1",
			ID:       "node1",
			Title:    "Node 1",
			Hash:     "hash1",
			LoadedAt: time.Now(),
			Excerpt:  "Excerpt 1",
		},
	}

	if err := store.Put(session); err != nil {
		t.Fatalf("Failed to put session: %v", err)
	}

	retrieved, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if len(retrieved.NodeDigests) != 1 {
		t.Errorf("Expected 1 node digest, got %d", len(retrieved.NodeDigests))
	}
	if retrieved.NodeDigests[0].Path != "root/node1" {
		t.Errorf("Node path mismatch: got %s, want root/node1", retrieved.NodeDigests[0].Path)
	}
}

func TestSQLiteStore_ToolResults(t *testing.T) {
	tmpFile := "/tmp/agentize_test_tools.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	session := model.NewSessionWithType("user123", model.AgentTypeCore)
	session.ToolResults = map[string]string{
		"result1": "Tool result 1",
		"result2": "Tool result 2",
	}

	if err := store.Put(session); err != nil {
		t.Fatalf("Failed to put session: %v", err)
	}

	retrieved, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if len(retrieved.ToolResults) != 2 {
		t.Errorf("Expected 2 tool results, got %d", len(retrieved.ToolResults))
	}
	if retrieved.ToolResults["result1"] != "Tool result 1" {
		t.Errorf("Tool result mismatch: got %s, want Tool result 1", retrieved.ToolResults["result1"])
	}
}

func TestSQLiteStore_MultipleUsers(t *testing.T) {
	tmpFile := "/tmp/agentize_test_multi.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	// Create sessions for different users
	session1 := model.NewSessionWithType("user1", model.AgentTypeCore)
	session2 := model.NewSessionWithType("user2", model.AgentTypeCore)
	session3 := model.NewSessionWithType("user1", model.AgentTypeHigh)

	if err := store.Put(session1); err != nil {
		t.Fatalf("Failed to put session1: %v", err)
	}
	if err := store.Put(session2); err != nil {
		t.Fatalf("Failed to put session2: %v", err)
	}
	if err := store.Put(session3); err != nil {
		t.Fatalf("Failed to put session3: %v", err)
	}

	// List sessions for user1
	sessions, err := store.List("user1")
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("Expected 2 sessions for user1, got %d", len(sessions))
	}

	// List sessions for user2
	sessions, err = store.List("user2")
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("Expected 1 session for user2, got %d", len(sessions))
	}
}
