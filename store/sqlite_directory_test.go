package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghiac/agentize/model"
)

func TestSQLiteStore_AutoCreateDirectory(t *testing.T) {
	// Use a nested directory path that doesn't exist
	tmpDir := "/tmp/agentize_test_dir"
	tmpFile := filepath.Join(tmpDir, "nested", "sessions.db")
	defer os.RemoveAll(tmpDir)

	// Create store - should automatically create directories
	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	// Verify directory was created
	if _, err := os.Stat(filepath.Dir(tmpFile)); os.IsNotExist(err) {
		t.Errorf("Directory was not created: %s", filepath.Dir(tmpFile))
	}

	// Verify database file exists
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Errorf("Database file was not created: %s", tmpFile)
	}

	// Test that we can actually use the store
	session := model.NewSessionWithType("user123", model.AgentTypeCore)
	if err := store.Put(session); err != nil {
		t.Fatalf("Failed to put session: %v", err)
	}

	retrieved, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatalf("Failed to get session: %v", err)
	}

	if retrieved.SessionID != session.SessionID {
		t.Errorf("SessionID mismatch: got %s, want %s", retrieved.SessionID, session.SessionID)
	}
}

func TestSQLiteStore_ExistingDirectory(t *testing.T) {
	// Use a directory that already exists
	tmpDir := "/tmp/agentize_test_existing"
	tmpFile := filepath.Join(tmpDir, "sessions.db")
	defer os.RemoveAll(tmpDir)

	// Create directory first
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		t.Fatalf("Failed to create directory: %v", err)
	}

	// Create store - should work fine with existing directory
	store, err := NewSQLiteStore(tmpFile)
	if err != nil {
		t.Fatalf("Failed to create SQLiteStore: %v", err)
	}
	defer store.Close()

	// Test that we can use the store
	session := model.NewSessionWithType("user123", model.AgentTypeCore)
	if err := store.Put(session); err != nil {
		t.Fatalf("Failed to put session: %v", err)
	}
}
