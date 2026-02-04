package store

import (
	"os"
	"testing"

	"github.com/ghiac/agentize/model"
)

func TestSQLiteStore_CoreSessionUniqueness(t *testing.T) {
	tmpFile := "/tmp/agentize_test_core.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

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
	coreSession, err := store.GetCoreSession(userID)
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

func TestSQLiteStore_GetCoreSession(t *testing.T) {
	tmpFile := "/tmp/agentize_test_getcore.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	userID := "user123"

	// No Core session exists yet
	coreSession, err := store.GetCoreSession(userID)
	if err != nil {
		t.Fatalf("GetCoreSession should not return error when no session exists: %v", err)
	}
	if coreSession != nil {
		t.Error("Core session should be nil when none exists")
	}

	// Create Core session
	session := model.NewSessionWithType(userID, model.AgentTypeCore)
	session.Title = "My Core Session"

	if err := store.PutCoreSession(session); err != nil {
		t.Fatalf("Failed to put core session: %v", err)
	}

	// Get Core session
	coreSession, err = store.GetCoreSession(userID)
	if err != nil {
		t.Fatalf("Failed to get core session: %v", err)
	}

	if coreSession == nil {
		t.Fatal("Core session should exist")
	}

	if coreSession.Title != "My Core Session" {
		t.Errorf("Expected 'My Core Session', got '%s'", coreSession.Title)
	}

	if coreSession.AgentType != model.AgentTypeCore {
		t.Errorf("Expected AgentTypeCore, got %s", coreSession.AgentType)
	}
}

func TestSQLiteStore_PutCoreSession_ReplacesExisting(t *testing.T) {
	tmpFile := "/tmp/agentize_test_replacecore.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	userID := "user123"

	// Create first Core session
	session1 := model.NewSessionWithType(userID, model.AgentTypeCore)
	session1.SessionID = "session1"
	session1.Title = "First Core"

	if err := store.PutCoreSession(session1); err != nil {
		t.Fatalf("Failed to put first core session: %v", err)
	}

	// Create second Core session (should replace first)
	session2 := model.NewSessionWithType(userID, model.AgentTypeCore)
	session2.SessionID = "session2"
	session2.Title = "Second Core"

	if err := store.PutCoreSession(session2); err != nil {
		t.Fatalf("Failed to put second core session: %v", err)
	}

	// Verify only second session exists
	coreSession, err := store.GetCoreSession(userID)
	if err != nil {
		t.Fatalf("Failed to get core session: %v", err)
	}

	if coreSession.SessionID != "session2" {
		t.Errorf("Expected session2, got %s", coreSession.SessionID)
	}

	if coreSession.Title != "Second Core" {
		t.Errorf("Expected 'Second Core', got '%s'", coreSession.Title)
	}

	// Verify first session is deleted
	_, err = store.Get("session1")
	if err == nil {
		t.Error("First core session should have been deleted")
	}
}

func TestSQLiteStore_CoreAndOtherSessions(t *testing.T) {
	tmpFile := "/tmp/agentize_test_mixed.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	userID := "user123"

	// Create Core session
	coreSession := model.NewSessionWithType(userID, model.AgentTypeCore)
	coreSession.SessionID = "core-session-1"
	coreSession.Title = "Core Session"
	if err := store.Put(coreSession); err != nil {
		t.Fatalf("Failed to put core session: %v", err)
	}

	// Create High session
	highSession := model.NewSessionWithType(userID, model.AgentTypeHigh)
	highSession.SessionID = "high-session-1"
	highSession.Title = "High Session"
	if err := store.Put(highSession); err != nil {
		t.Fatalf("Failed to put high session: %v", err)
	}

	// Create Low session
	lowSession := model.NewSessionWithType(userID, model.AgentTypeLow)
	lowSession.SessionID = "low-session-1"
	lowSession.Title = "Low Session"
	if err := store.Put(lowSession); err != nil {
		t.Fatalf("Failed to put low session: %v", err)
	}

	// List all sessions
	sessions, err := store.List(userID)
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}

	if len(sessions) != 3 {
		t.Errorf("Expected 3 sessions, got %d", len(sessions))
	}

	// Verify Core session count
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

func TestSQLiteStore_PutCoreSession_WithExistingSessionID(t *testing.T) {
	tmpFile := "/tmp/agentize_test_existing_session_id.db"
	defer os.Remove(tmpFile)

	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	userID := "user123"

	// First, create a UserAgent session with a specific session_id
	userAgentSession := model.NewSessionWithType(userID, model.AgentTypeLow)
	userAgentSession.SessionID = "test-session-id-123"
	userAgentSession.Title = "UserAgent Session"

	err = store.Put(userAgentSession)
	if err != nil {
		t.Fatalf("Failed to store UserAgent session: %v", err)
	}

	// Now try to create a Core session with the same session_id
	// This should work because we use INSERT OR REPLACE
	coreSession := model.NewSessionWithType(userID, model.AgentTypeCore)
	coreSession.SessionID = "test-session-id-123" // Same session_id
	coreSession.Title = "Core Session"

	err = store.PutCoreSession(coreSession)
	if err != nil {
		t.Fatalf("Failed to store Core session with existing session_id: %v", err)
	}

	// Verify Core session exists
	retrieved, err := store.GetCoreSession(userID)
	if err != nil {
		t.Fatalf("Failed to retrieve Core session: %v", err)
	}
	if retrieved == nil {
		t.Fatal("Core session should exist")
	}
	if retrieved.SessionID != "test-session-id-123" {
		t.Errorf("Expected session_id 'test-session-id-123', got '%s'", retrieved.SessionID)
	}
	if retrieved.AgentType != model.AgentTypeCore {
		t.Errorf("Expected AgentType Core, got %s", retrieved.AgentType)
	}

	// Verify the session with this ID is now a Core session (it was replaced)
	retrievedByID, err := store.Get("test-session-id-123")
	if err != nil {
		t.Fatalf("Session should exist: %v", err)
	}
	if retrievedByID.AgentType != model.AgentTypeCore {
		t.Errorf("Session should be Core type, got %s", retrievedByID.AgentType)
	}

	// Verify only one session exists for this user
	allSessions, err := store.List(userID)
	if err != nil {
		t.Fatalf("Failed to list sessions: %v", err)
	}
	if len(allSessions) != 1 {
		t.Errorf("Expected 1 session, got %d", len(allSessions))
	}
}
