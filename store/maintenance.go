package store

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
)

// auditDeletion records every destructive store operation for compliance and
// forensics: a structured log line plus the agentize_store_deletions_total
// metric. entity is the kind of record ("session", "user_data", "user_file"),
// id the deleted entity, userID the owning user when known.
func auditDeletion(entity, id, userID string) {
	log.Log.Infof("store: audit: deleted %s id=%s user=%s at=%s", entity, id, userID, time.Now().Format(time.RFC3339))
	metrics.RecordStoreDeletion(entity)
}

// ---------------------------------------------------------------------------
// SQLiteStore maintenance
// ---------------------------------------------------------------------------

// Backup streams a consistent snapshot of the database to w using VACUUM INTO
// (works on live databases, including in-memory ones). Implements Maintainer.
func (s *SQLiteStore) Backup(w io.Writer) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tmpDir, err := os.MkdirTemp("", "agentize-backup-*")
	if err != nil {
		return fmt.Errorf("store: backup: temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	tmpFile := filepath.Join(tmpDir, "backup.db")

	if _, err := s.db.Exec(`VACUUM INTO ?`, tmpFile); err != nil {
		return fmt.Errorf("store: backup: vacuum into: %w", err)
	}
	f, err := os.Open(tmpFile)
	if err != nil {
		return fmt.Errorf("store: backup: open snapshot: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("store: backup: stream: %w", err)
	}
	return nil
}

// Verify scans for orphaned child rows (messages, tool calls, opened files,
// user files, route traces whose session no longer exists). Implements
// Maintainer. Intended for the debug dashboard and operational checks.
func (s *SQLiteStore) Verify() ([]Issue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	checks := []struct {
		issueType string
		query     string
		detail    string
	}{
		{"orphaned_message",
			`SELECT message_id FROM messages WHERE session_id NOT IN (SELECT session_id FROM sessions)`,
			"message references a session that no longer exists"},
		{"orphaned_tool_call",
			`SELECT tool_call_id FROM tool_calls WHERE session_id NOT IN (SELECT session_id FROM sessions)`,
			"tool call references a session that no longer exists"},
		{"orphaned_opened_file",
			`SELECT file_id FROM opened_files WHERE session_id NOT IN (SELECT session_id FROM sessions)`,
			"opened file references a session that no longer exists"},
		{"orphaned_user_file",
			`SELECT file_id FROM user_files WHERE user_id NOT IN (SELECT user_id FROM users)`,
			"user file references a user that no longer exists"},
		{"orphaned_route_trace",
			`SELECT trace_id FROM route_traces WHERE session_id NOT IN (SELECT session_id FROM sessions)`,
			"route trace references a session that no longer exists"},
	}

	var issues []Issue
	for _, c := range checks {
		rows, err := s.db.Query(c.query)
		if err != nil {
			return nil, fmt.Errorf("store: verify %s: %w", c.issueType, err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			issues = append(issues, Issue{Type: c.issueType, ID: id, Detail: c.detail})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return issues, nil
}

// PoolStats exposes the connection pool statistics for monitoring.
func (s *SQLiteStore) PoolStats() sql.DBStats {
	return s.db.Stats()
}

// ---------------------------------------------------------------------------
// DBStore maintenance (delegates to the underlying SQLiteStore)
// ---------------------------------------------------------------------------

// Backup streams a snapshot of the underlying SQLite database. Implements Maintainer.
func (s *DBStore) Backup(w io.Writer) error { return s.sqliteStore.Backup(w) }

// Verify scans the underlying SQLite database for orphans. Implements Maintainer.
func (s *DBStore) Verify() ([]Issue, error) { return s.sqliteStore.Verify() }

// PoolStats exposes the underlying SQLite pool statistics.
func (s *DBStore) PoolStats() sql.DBStats { return s.sqliteStore.PoolStats() }

// SetQuotas configures storage quotas on the underlying SQLite store.
func (s *DBStore) SetQuotas(q Quotas) { s.sqliteStore.SetQuotas(q) }

// Compile-time guarantee that every backend offers maintenance operations.
var (
	_ Maintainer = (*SQLiteStore)(nil)
	_ Maintainer = (*DBStore)(nil)
	_ Maintainer = (*MongoDBStore)(nil)
)
