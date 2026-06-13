package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghiac/agentize/model"
)

// Migrations are recorded in schema_version, applied exactly once, and stable
// across reopen of the same database file.
func TestSQLiteStore_VersionedMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "migrate.db")

	st, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	v1, err := st.SchemaVersion()
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	want := sqliteMigrations[len(sqliteMigrations)-1].version
	if v1 != want {
		t.Errorf("schema version after open = %d, want %d", v1, want)
	}
	s := model.NewSessionWithType("user-1", model.AgentTypeLow)
	if err := st.Put(s); err != nil {
		t.Fatalf("Put: %v", err)
	}
	st.Close()

	// Reopen: migrations must not re-run or fail, data must survive.
	st2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	v2, err := st2.SchemaVersion()
	if err != nil || v2 != want {
		t.Errorf("schema version after reopen = %d (%v), want %d", v2, err, want)
	}
	if _, err := st2.Get(s.SessionID); err != nil {
		t.Errorf("session lost across reopen: %v", err)
	}
}

// A SQLite backup is itself a valid database containing the data.
func TestSQLiteStore_BackupIsRestorable(t *testing.T) {
	st, err := NewSQLiteStore(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	s := model.NewSessionWithType("user-1", model.AgentTypeLow)
	if err := st.Put(s); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var buf bytes.Buffer
	if err := st.Backup(&buf); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	restored := filepath.Join(t.TempDir(), "restored.db")
	if err := writeFile(restored, buf.Bytes()); err != nil {
		t.Fatalf("write restored db: %v", err)
	}
	st2, err := NewSQLiteStore(restored)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer st2.Close()
	got, err := st2.Get(s.SessionID)
	if err != nil || got.UserID != "user-1" {
		t.Errorf("restored backup missing session: %v / %+v", err, got)
	}
}

// Verify finds orphaned rows after a session row is removed out from under its
// children.
func TestSQLiteStore_VerifyFindsOrphans(t *testing.T) {
	st, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	s := model.NewSessionWithType("user-1", model.AgentTypeLow)
	if err := st.Put(s); err != nil {
		t.Fatalf("Put: %v", err)
	}
	m := model.NewUserMessage(s.SessionID+"-m0001", 1, "user-1", s.SessionID, "hi", model.ContentTypeText)
	if err := st.PutMessage(m); err != nil {
		t.Fatalf("PutMessage: %v", err)
	}

	// Remove the session directly; the message becomes an orphan.
	if err := st.Delete(s.SessionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	issues, err := st.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	found := false
	for _, issue := range issues {
		if issue.Type == "orphaned_message" && issue.ID == m.MessageID {
			found = true
		}
	}
	if !found {
		t.Errorf("Verify did not report the orphaned message; got %+v", issues)
	}
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
