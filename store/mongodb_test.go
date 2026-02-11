package store

import (
	"context"
	"os"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// TestMongoDBStore_BasicOperations tests basic CRUD operations
// Note: This test requires a running MongoDB instance
// Set MONGODB_URI environment variable to override default connection string
func TestMongoDBStore_BasicOperations(t *testing.T) {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}

	config := MongoDBStoreConfig{
		URI:        uri,
		Database:   "agentize_test",
		Collection: "sessions_test",
	}

	mongoStore, err := NewMongoDBStore(config)
	if err != nil {
		t.Skipf("Skipping test: MongoDB not available: %v", err)
	}
	defer mongoStore.Close()

	var store model.SessionStore = mongoStore

	// Clean up test data
	ctx := context.Background()
	mongoStore.collection.DeleteMany(ctx, map[string]interface{}{})

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

func TestMongoDBStore_CoreSessionUniqueness(t *testing.T) {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}

	config := MongoDBStoreConfig{
		URI:        uri,
		Database:   "agentize_test",
		Collection: "sessions_core_test",
	}

	mongoStore, err := NewMongoDBStore(config)
	if err != nil {
		t.Skipf("Skipping test: MongoDB not available: %v", err)
	}
	defer mongoStore.Close()

	var store model.SessionStore = mongoStore

	// Clean up test data
	ctx := context.Background()
	mongoStore.collection.DeleteMany(ctx, map[string]interface{}{})

	userID := "user123"

	// Create first Core session
	session1 := model.NewSessionWithType(userID, model.AgentTypeCore)
	session1.Title = "First Core Session"

	if err := store.Put(session1); err != nil {
		t.Fatalf("Failed to put first core session: %v", err)
	}

	// Try to create second Core session for same user
	session2 := model.NewSessionWithType(userID, model.AgentTypeCore)
	session2.Title = "Second Core Session"

	if err := store.Put(session2); err != nil {
		t.Fatalf("Failed to put second core session: %v", err)
	}

	// Get Core session - should return the second one (latest)
	coreSession, err := mongoStore.GetCoreSession(userID)
	if err != nil {
		t.Fatalf("Failed to get core session: %v", err)
	}

	if coreSession == nil {
		t.Fatal("Core session should exist")
	}

	if coreSession.Title != "Second Core Session" {
		t.Errorf("Expected 'Second Core Session', got '%s'", coreSession.Title)
	}

	// Verify only one Core session exists
	sessions, err := store.List(userID)
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}

	coreCount := 0
	for _, s := range sessions {
		if s.AgentType == model.AgentTypeCore {
			coreCount++
		}
	}

	if coreCount != 1 {
		t.Errorf("Expected 1 Core session, got %d", coreCount)
	}
}

func TestMongoDBStore_ConversationState(t *testing.T) {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}

	config := MongoDBStoreConfig{
		URI:        uri,
		Database:   "agentize_test",
		Collection: "sessions_conv_test",
	}

	mongoStore, err := NewMongoDBStore(config)
	if err != nil {
		t.Skipf("Skipping test: MongoDB not available: %v", err)
	}
	defer mongoStore.Close()

	var store model.SessionStore = mongoStore

	// Clean up test data
	ctx := context.Background()
	mongoStore.collection.DeleteMany(ctx, map[string]interface{}{})

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

func TestMongoDBStore_MultipleUsers(t *testing.T) {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		uri = "mongodb://localhost:27017"
	}

	config := MongoDBStoreConfig{
		URI:        uri,
		Database:   "agentize_test",
		Collection: "sessions_multi_test",
	}

	mongoStore, err := NewMongoDBStore(config)
	if err != nil {
		t.Skipf("Skipping test: MongoDB not available: %v", err)
	}
	defer mongoStore.Close()

	var store model.SessionStore = mongoStore

	// Clean up test data
	ctx := context.Background()
	mongoStore.collection.DeleteMany(ctx, map[string]interface{}{})

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
