package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	_ "modernc.org/sqlite"
)

// execWrite executes a write statement, retrying with exponential backoff when
// SQLite reports lock/busy contention. The busy_timeout pragma handles most
// contention in-engine; this guards the rare cases that still surface.
func (s *SQLiteStore) execWrite(query string, args ...interface{}) (sql.Result, error) {
	backoff := 50 * time.Millisecond
	var res sql.Result
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		res, err = s.db.Exec(query, args...)
		if err == nil || !isSQLiteBusy(err) {
			return res, err
		}
		log.Log.Warnf("store: sqlite busy (attempt %d), retrying in %s: %v", attempt+1, backoff, err)
		time.Sleep(backoff)
		backoff *= 2
	}
	return res, err
}

// isSQLiteBusy reports whether err is a lock-contention error worth retrying.
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "SQLITE_BUSY")
}

// SQLiteStore is a SQLite implementation of SessionStore
// It stores sessions in a SQLite database with JSON serialization
type SQLiteStore struct {
	db     *sql.DB
	mu     sync.RWMutex
	path   string
	quotas Quotas

	// UserNodes tracks visited nodes for each user (user-level, not session-level)
	userNodes sync.Map
	// userLock maps userID → *sync.Mutex. Entries are created with LoadOrStore
	// and never deleted (the per-user mutex footprint is tiny), so a lock can
	// never be removed between lookup and use.
	userLock sync.Map
}

// NewSQLiteStore creates a new SQLite session store
// If dbPath is empty, it uses ":memory:" for in-memory database
// For file-based storage, use a path like "./data/sessions.db"
// The function automatically creates the directory if it doesn't exist
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dbPath == "" {
		dbPath = ":memory:"
	}

	// For file-based storage (not in-memory), ensure directory exists
	if dbPath != ":memory:" {
		dir := filepath.Dir(dbPath)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create directory for database: %w", err)
			}
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Explicit pool sizing. An in-memory database exists per connection, so the
	// pool MUST be a single connection or different conns would see different
	// databases. For file databases WAL allows concurrent readers alongside a
	// writer, so a small pool is safe and avoids fd churn.
	if dbPath == ":memory:" {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	} else {
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(4)
		db.SetConnMaxLifetime(time.Hour)
	}

	store := &SQLiteStore{
		db:   db,
		path: dbPath,
	}

	// WAL mode (readers don't block the writer) and a busy timeout so writes
	// under contention wait instead of failing with "database is locked".
	// journal_mode is a no-op for in-memory databases.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to apply pragmas: %w", err)
	}

	// Create tables and run versioned migrations (fail fast, never silent).
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// SetQuotas configures storage quotas (zero values = unlimited). See Quotas.
func (s *SQLiteStore) SetQuotas(q Quotas) { s.quotas = q }

// Path returns the SQLite database path (":memory:" for in-memory stores). Used
// by the debug dashboard to report where session/file metadata is persisted.
func (s *SQLiteStore) Path() string {
	return s.path
}

// BackendInfo describes this backend for diagnostics and the debug dashboard.
func (s *SQLiteStore) BackendInfo() debuger.BackendInfo {
	return debuger.BackendInfo{Type: "SQLite", Location: sqlitePathLabel(s.path)}
}

// initSchema creates the necessary tables
func (s *SQLiteStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		session_id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		agent_type TEXT NOT NULL,
		session_seq INTEGER NOT NULL DEFAULT 0,
		data TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	
	CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions(updated_at);
	CREATE INDEX IF NOT EXISTS idx_sessions_user_agent ON sessions(user_id, agent_type);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_user_core ON sessions(user_id, agent_type) WHERE agent_type = 'core';
	
	CREATE TABLE IF NOT EXISTS users (
		user_id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	
	CREATE TABLE IF NOT EXISTS messages (
		message_id TEXT PRIMARY KEY,
		seq_id INTEGER DEFAULT 0,
		user_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		model TEXT,
		agent_type TEXT DEFAULT '',
		content_type TEXT DEFAULT '',
		prompt_tokens INTEGER DEFAULT 0,
		completion_tokens INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		request_model TEXT,
		max_tokens INTEGER,
		temperature REAL,
		has_tool_calls INTEGER DEFAULT 0,
		finish_reason TEXT,
		is_nonsense INTEGER DEFAULT 0,
		created_at INTEGER NOT NULL
	);
	
	CREATE INDEX IF NOT EXISTS idx_messages_user_id ON messages(user_id);
	CREATE INDEX IF NOT EXISTS idx_messages_session_id ON messages(session_id);
	CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at);
	
	CREATE TABLE IF NOT EXISTS opened_files (
		file_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		file_path TEXT NOT NULL,
		file_name TEXT,
		opened_at INTEGER NOT NULL,
		closed_at INTEGER,
		is_open INTEGER DEFAULT 1
	);
	
	CREATE INDEX IF NOT EXISTS idx_opened_files_session_id ON opened_files(session_id);
	CREATE INDEX IF NOT EXISTS idx_opened_files_user_id ON opened_files(user_id);
	CREATE INDEX IF NOT EXISTS idx_opened_files_file_path ON opened_files(file_path);
	CREATE INDEX IF NOT EXISTS idx_opened_files_is_open ON opened_files(is_open);

	CREATE TABLE IF NOT EXISTS user_files (
		file_id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		name TEXT,
		mime_type TEXT,
		size INTEGER DEFAULT 0,
		storage_key TEXT NOT NULL,
		source TEXT DEFAULT 'uploaded',
		parent_file_id TEXT DEFAULT '',
		summary TEXT,
		created_at INTEGER NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_user_files_user_id ON user_files(user_id);
	CREATE INDEX IF NOT EXISTS idx_user_files_session_id ON user_files(session_id);
	CREATE INDEX IF NOT EXISTS idx_user_files_created_at ON user_files(created_at);

	CREATE TABLE IF NOT EXISTS tool_calls (
		tool_call_id TEXT PRIMARY KEY,
		tool_id TEXT DEFAULT '',
		message_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		agent_type TEXT DEFAULT '',
		function_name TEXT NOT NULL,
		arguments TEXT NOT NULL,
		response TEXT DEFAULT '',
		response_length INTEGER DEFAULT 0,
		duration_ms INTEGER DEFAULT 0,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	
	CREATE INDEX IF NOT EXISTS idx_tool_calls_message_id ON tool_calls(message_id);
	CREATE INDEX IF NOT EXISTS idx_tool_calls_session_id ON tool_calls(session_id);
	CREATE INDEX IF NOT EXISTS idx_tool_calls_user_id ON tool_calls(user_id);
	CREATE INDEX IF NOT EXISTS idx_tool_calls_created_at ON tool_calls(created_at);
	
	CREATE TABLE IF NOT EXISTS summarization_logs (
		log_id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		session_title TEXT,
		previous_summary TEXT,
		previous_tags TEXT,
		messages_before_count INTEGER DEFAULT 0,
		messages_after_count INTEGER DEFAULT 0,
		archived_messages_count INTEGER DEFAULT 0,
		prompt_sent TEXT NOT NULL,
		response_received TEXT,
		model_used TEXT NOT NULL,
		requested_model TEXT,
		generated_summary TEXT,
		generated_tags TEXT,
		generated_title TEXT,
		prompt_tokens INTEGER DEFAULT 0,
		completion_tokens INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		duration_ms INTEGER DEFAULT 0,
		status TEXT NOT NULL,
		error_message TEXT,
		summarization_type TEXT,
		created_at INTEGER NOT NULL,
		completed_at INTEGER
	);
	
	CREATE INDEX IF NOT EXISTS idx_summarization_logs_session_id ON summarization_logs(session_id);
	CREATE INDEX IF NOT EXISTS idx_summarization_logs_user_id ON summarization_logs(user_id);
	CREATE INDEX IF NOT EXISTS idx_summarization_logs_created_at ON summarization_logs(created_at);
	CREATE INDEX IF NOT EXISTS idx_summarization_logs_status ON summarization_logs(status);
	`

	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}

	// Versioned migrations: each runs once, is recorded in schema_version, and
	// aborts startup on failure instead of silently leaving a partial schema.
	return s.runMigrations()
}

// sqliteMigration is one schema change: applied inside a transaction, recorded
// in schema_version, executed exactly once per database.
type sqliteMigration struct {
	version int
	desc    string
	apply   func(tx *sql.Tx) error
}

// addColumns adds each "name TYPE ..." definition to table when the column does
// not already exist. Genuinely idempotent: fresh databases create these columns
// in the base schema, so the skip is silent at debug level only.
func addColumns(tx *sql.Tx, table string, defs ...string) error {
	existing := make(map[string]bool)
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, def := range defs {
		col := strings.Fields(def)[0]
		if existing[col] {
			log.Log.Debugf("store: migration: %s.%s already exists, skipping", table, col)
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s`, table, def)); err != nil {
			return fmt.Errorf("add column %s.%s: %w", table, col, err)
		}
	}
	return nil
}

func execAll(tx *sql.Tx, stmts ...string) error {
	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("%q: %w", stmt, err)
		}
	}
	return nil
}

// sqliteMigrations is the ordered, append-only migration list. NEVER reorder or
// edit an applied entry — append a new version instead.
var sqliteMigrations = []sqliteMigration{
	{1, "messages.is_nonsense", func(tx *sql.Tx) error {
		return addColumns(tx, "messages", `is_nonsense INTEGER DEFAULT 0`)
	}},
	{2, "summarization_logs detail columns + status index", func(tx *sql.Tx) error {
		if err := addColumns(tx, "summarization_logs",
			`session_title TEXT`, `previous_summary TEXT`, `previous_tags TEXT`,
			`messages_before_count INTEGER DEFAULT 0`, `messages_after_count INTEGER DEFAULT 0`,
			`archived_messages_count INTEGER DEFAULT 0`, `requested_model TEXT`,
			`generated_summary TEXT`, `generated_tags TEXT`, `generated_title TEXT`,
			`duration_ms INTEGER DEFAULT 0`, `summarization_type TEXT`, `completed_at INTEGER`,
		); err != nil {
			return err
		}
		return execAll(tx, `CREATE INDEX IF NOT EXISTS idx_summarization_logs_status ON summarization_logs(status)`)
	}},
	{3, "messages/tool_calls type + telemetry columns", func(tx *sql.Tx) error {
		if err := addColumns(tx, "messages", `agent_type TEXT DEFAULT ''`, `content_type TEXT DEFAULT ''`); err != nil {
			return err
		}
		return addColumns(tx, "tool_calls",
			`agent_type TEXT DEFAULT ''`, `response_length INTEGER DEFAULT 0`,
			`duration_ms INTEGER DEFAULT 0`, `tool_id TEXT DEFAULT ''`,
			`status TEXT DEFAULT 'pending'`, `error TEXT DEFAULT ''`,
		)
	}},
	{4, "messages.seq_id", func(tx *sql.Tx) error {
		return addColumns(tx, "messages", `seq_id INTEGER DEFAULT 0`)
	}},
	{5, "sessions.session_seq + (user_id, agent_type) index", func(tx *sql.Tx) error {
		if err := addColumns(tx, "sessions", `session_seq INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		return execAll(tx, `CREATE INDEX IF NOT EXISTS idx_sessions_user_agent ON sessions(user_id, agent_type)`)
	}},
	{6, "tool_calls.display_label", func(tx *sql.Tx) error {
		return addColumns(tx, "tool_calls", `display_label TEXT DEFAULT ''`)
	}},
	{7, "user_files.parent_file_id", func(tx *sql.Tx) error {
		return addColumns(tx, "user_files", `parent_file_id TEXT DEFAULT ''`)
	}},
	{8, "route_traces table", func(tx *sql.Tx) error {
		return execAll(tx, `
		CREATE TABLE IF NOT EXISTS route_traces (
			trace_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			status TEXT DEFAULT '',
			data TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
			`CREATE INDEX IF NOT EXISTS idx_route_traces_session_id ON route_traces(session_id)`,
			`CREATE INDEX IF NOT EXISTS idx_route_traces_user_id ON route_traces(user_id)`,
			`CREATE INDEX IF NOT EXISTS idx_route_traces_created_at ON route_traces(created_at)`,
		)
	}},
	{9, "opened_files (session_id, is_open) compound index", func(tx *sql.Tx) error {
		return execAll(tx, `CREATE INDEX IF NOT EXISTS idx_opened_files_session_open ON opened_files(session_id, is_open)`)
	}},
	{10, "messages (session_id, seq_id) index for pagination", func(tx *sql.Tx) error {
		return execAll(tx, `CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, seq_id)`)
	}},
}

// runMigrations applies every migration newer than the recorded schema version.
// Each migration runs in its own transaction and is recorded on success, so a
// failure leaves the database at a known version and aborts startup loudly.
func (s *SQLiteStore) runMigrations() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		description TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_version table: %w", err)
	}

	var current sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for _, m := range sqliteMigrations {
		if current.Valid && int64(m.version) <= current.Int64 {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("migration %d (%s): begin: %w", m.version, m.desc, err)
		}
		if err := m.apply(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s) failed: %w", m.version, m.desc, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO schema_version (version, description, applied_at) VALUES (?, ?, ?)`,
			m.version, m.desc, time.Now().Unix(),
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s): record: %w", m.version, m.desc, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d (%s): commit: %w", m.version, m.desc, err)
		}
		log.Log.Infof("store: applied migration %d: %s", m.version, m.desc)
	}
	return nil
}

// SchemaVersion returns the highest applied migration version (0 = base schema
// only). For diagnostics and the debug dashboard.
func (s *SQLiteStore) SchemaVersion() (int, error) {
	var current sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&current); err != nil {
		return 0, err
	}
	return int(current.Int64), nil
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// getOrCreateLock returns the per-user mutex, creating it atomically on first
// use. Entries are never deleted, so the returned lock is always valid.
func (s *SQLiteStore) getOrCreateLock(userID string) *sync.Mutex {
	v, _ := s.userLock.LoadOrStore(userID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// Get retrieves a session by ID
func (s *SQLiteStore) Get(sessionID string) (*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data string
	var createdAt, updatedAt int64

	err := s.db.QueryRow(
		"SELECT data, created_at, updated_at FROM sessions WHERE session_id = ?",
		sessionID,
	).Scan(&data, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query session: %w", err)
	}

	session := &model.Session{}
	if err := json.Unmarshal([]byte(data), session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Restore timestamps
	session.CreatedAt = time.Unix(createdAt, 0)
	session.UpdatedAt = time.Unix(updatedAt, 0)

	// Restore MessageSeq from database to ensure it's correct
	// Use MAX(seq_id) to get the highest sequence number, not COUNT(*) which doesn't reflect actual sequences
	maxSeqID := s.getMaxSeqIDForSession(sessionID)
	if maxSeqID > session.MessageSeq {
		// Ensure MessageSeq is at least as high as the highest seq_id in the database
		session.MessageSeq = maxSeqID
	}

	// Restore ToolSeq from tool_calls so we never reuse a tool ID (ensures new tool calls are stored with unique IDs)
	maxToolSeq := s.getMaxToolSeqForSession(sessionID)
	if maxToolSeq > session.ToolSeq {
		session.ToolSeq = maxToolSeq
	}

	return session, nil
}

// getMaxSeqIDForSession returns the maximum seq_id for a session.
// Used to restore MessageSeq counter correctly from database.
func (s *SQLiteStore) getMaxSeqIDForSession(sessionID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var maxSeqID sql.NullInt64
	err := s.db.QueryRow(
		"SELECT MAX(seq_id) FROM messages WHERE session_id = ?",
		sessionID,
	).Scan(&maxSeqID)
	if err != nil || !maxSeqID.Valid {
		return 0
	}
	return int(maxSeqID.Int64)
}

// getMaxToolSeqForSession returns the maximum tool sequence number for a session from tool_calls table.
func (s *SQLiteStore) getMaxToolSeqForSession(sessionID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows, err := s.db.Query("SELECT tool_id FROM tool_calls WHERE session_id = ?", sessionID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	maxSeq := 0
	for rows.Next() {
		var toolID string
		if err := rows.Scan(&toolID); err != nil {
			continue
		}
		if seq := parseToolSeqFromToolID(toolID); seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq
}

// Put stores or updates a session
// For Core sessions, this ensures only one Core session exists per user
func (s *SQLiteStore) Put(session *model.Session) error {
	if err := validateSession(session); err != nil {
		return err
	}

	// Ensure user exists when storing a session (otherwise user is never created on first session)
	if _, err := s.GetOrCreateUser(session.UserID); err != nil {
		return fmt.Errorf("ensure user for session: %w", err)
	}

	// For Core sessions, use PutCoreSession to ensure uniqueness
	if session.AgentType == model.AgentTypeCore {
		return s.PutCoreSession(session)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session.UpdatedAt = time.Now()

	// Serialize session to JSON
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	createdAt := session.CreatedAt.Unix()
	updatedAt := session.UpdatedAt.Unix()

	// Extract session_seq from session_id (format: userID-agentType-s0001)
	sessionSeq := extractSessionSeq(session.SessionID)

	// Use INSERT OR REPLACE for upsert behavior
	_, err = s.execWrite(
		`INSERT OR REPLACE INTO sessions (session_id, user_id, agent_type, session_seq, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.SessionID,
		session.UserID,
		string(session.AgentType),
		sessionSeq,
		string(data),
		createdAt,
		updatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store session: %w", err)
	}

	return nil
}

// extractSessionSeq extracts the sequence number from a session ID
// Format: userID-agentType-s0001 -> 1
// Returns 0 if the format is not recognized
func extractSessionSeq(sessionID string) int {
	// Find the last occurrence of "-s" and extract the number after it
	idx := strings.LastIndex(sessionID, "-s")
	if idx == -1 || idx+2 >= len(sessionID) {
		return 0
	}
	seqStr := sessionID[idx+2:]
	seq, err := strconv.Atoi(seqStr)
	if err != nil {
		return 0
	}
	return seq
}

// Delete removes a session
func (s *SQLiteStore) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.execWrite("DELETE FROM sessions WHERE session_id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

	auditDeletion("session", sessionID, "")
	return nil
}

// DeleteUserData deletes all sessions, messages, tool calls, summarization logs,
// and opened files for a user. Resets user's ActiveSessionIDs and SessionSeqs,
// and unbans the user (clears BanUntil, BanMessage, NonsenseCount).
func (s *SQLiteStore) DeleteUserData(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Explicit serializable isolation: nothing (e.g. concurrent session
	// creation) may interleave with the delete and leave orphans behind.
	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete in order (child tables first, then sessions, then update user)
	if _, err := tx.Exec("DELETE FROM messages WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("failed to delete messages: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM tool_calls WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("failed to delete tool_calls: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM summarization_logs WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("failed to delete summarization_logs: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM opened_files WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("failed to delete opened_files: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM user_files WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("failed to delete user_files: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM route_traces WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("failed to delete route_traces: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("failed to delete sessions: %w", err)
	}

	// Reset user's ActiveSessionIDs and SessionSeqs
	var data string
	var createdAt, updatedAt int64
	scanErr := tx.QueryRow(
		"SELECT data, created_at, updated_at FROM users WHERE user_id = ?",
		userID,
	).Scan(&data, &createdAt, &updatedAt)
	if scanErr == nil {
		user := &model.User{}
		if json.Unmarshal([]byte(data), user) == nil {
			user.ActiveSessionIDs = make(map[model.AgentType]string)
			user.SessionSeqs = make(map[model.AgentType]int)
			user.Unban() // remove ban so user is no longer banned after full data delete
			if userData, err := json.Marshal(user); err == nil {
				now := user.UpdatedAt.Unix() // Unban() already set UpdatedAt
				_, _ = tx.Exec(
					`INSERT OR REPLACE INTO users (user_id, data, created_at, updated_at) VALUES (?, ?, ?, ?)`,
					userID, string(userData), createdAt, now,
				)
			}
		}
	}
	// Ignore "user not found" - user might not exist yet

	if err := tx.Commit(); err != nil {
		return err
	}
	auditDeletion("user_data", userID, userID)
	return nil
}

// List returns all sessions for a user
func (s *SQLiteStore) List(userID string) ([]*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		"SELECT data, created_at, updated_at FROM sessions WHERE user_id = ? ORDER BY updated_at DESC",
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []*model.Session
	for rows.Next() {
		var data string
		var createdAt, updatedAt int64

		if err := rows.Scan(&data, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		session := &model.Session{}
		if err := json.Unmarshal([]byte(data), session); err != nil {
			return nil, fmt.Errorf("failed to unmarshal session: %w", err)
		}

		// Restore timestamps
		session.CreatedAt = time.Unix(createdAt, 0)
		session.UpdatedAt = time.Unix(updatedAt, 0)

		sessions = append(sessions, session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	return sessions, nil
}

// GetNextSessionSeq returns the next session sequence number for a user and agent type
// Uses MAX(session_seq) to avoid duplicate IDs when sessions are deleted
func (s *SQLiteStore) GetNextSessionSeq(userID string, agentType model.AgentType) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var maxSeq sql.NullInt64
	err := s.db.QueryRow(
		"SELECT MAX(session_seq) FROM sessions WHERE user_id = ? AND agent_type = ?",
		userID, string(agentType),
	).Scan(&maxSeq)
	if err != nil {
		return 0, fmt.Errorf("failed to get max session seq: %w", err)
	}

	if maxSeq.Valid {
		return int(maxSeq.Int64) + 1, nil
	}
	return 1, nil
}

// GetAllSessions returns all sessions grouped by userID
func (s *SQLiteStore) GetAllSessions() (map[string][]*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		"SELECT data, created_at, updated_at FROM sessions ORDER BY updated_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query all sessions: %w", err)
	}
	defer rows.Close()

	sessionsByUser := make(map[string][]*model.Session)
	for rows.Next() {
		var data string
		var createdAt, updatedAt int64

		if err := rows.Scan(&data, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan session: %w", err)
		}

		session := &model.Session{}
		if err := json.Unmarshal([]byte(data), session); err != nil {
			return nil, fmt.Errorf("failed to unmarshal session: %w", err)
		}

		// Restore timestamps
		session.CreatedAt = time.Unix(createdAt, 0)
		session.UpdatedAt = time.Unix(updatedAt, 0)

		sessionsByUser[session.UserID] = append(sessionsByUser[session.UserID], session)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating sessions: %w", err)
	}

	return sessionsByUser, nil
}

// GetCoreSession returns the Core session for a user
// For each user, there should be only one Core session
// If no Core session exists, it returns nil without error
func (s *SQLiteStore) GetCoreSession(userID string) (*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data string
	var createdAt, updatedAt int64

	err := s.db.QueryRow(
		"SELECT data, created_at, updated_at FROM sessions WHERE user_id = ? AND agent_type = ? LIMIT 1",
		userID,
		string(model.AgentTypeCore),
	).Scan(&data, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, nil // No Core session found, return nil without error
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query core session: %w", err)
	}

	session := &model.Session{}
	if err := json.Unmarshal([]byte(data), session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	// Restore timestamps
	session.CreatedAt = time.Unix(createdAt, 0)
	session.UpdatedAt = time.Unix(updatedAt, 0)

	// Restore seq counters from persisted rows so core-session ID generation never
	// reuses an ID. Mirrors Get() and the MongoDB backend for cross-store parity.
	if maxSeqID := s.getMaxSeqIDForSession(session.SessionID); maxSeqID > session.MessageSeq {
		session.MessageSeq = maxSeqID
	}
	if maxToolSeq := s.getMaxToolSeqForSession(session.SessionID); maxToolSeq > session.ToolSeq {
		session.ToolSeq = maxToolSeq
	}

	return session, nil
}

// PutCoreSession stores or updates a Core session for a user
// This ensures only one Core session exists per user by deleting any existing Core sessions first
func (s *SQLiteStore) PutCoreSession(session *model.Session) error {
	if err := validateSession(session); err != nil {
		return err
	}
	if session.AgentType != model.AgentTypeCore {
		return fmt.Errorf("session must be of type Core")
	}

	// Ensure user exists when storing a session
	if _, err := s.GetOrCreateUser(session.UserID); err != nil {
		return fmt.Errorf("ensure user for core session: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Delete any existing Core sessions for this user
	_, err := s.db.Exec(
		"DELETE FROM sessions WHERE user_id = ? AND agent_type = ?",
		session.UserID,
		string(model.AgentTypeCore),
	)
	if err != nil {
		return fmt.Errorf("failed to delete existing core sessions: %w", err)
	}

	// Now store the new Core session
	session.UpdatedAt = time.Now()

	// Serialize session to JSON
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	createdAt := session.CreatedAt.Unix()
	updatedAt := session.UpdatedAt.Unix()

	// Extract session_seq from session_id
	sessionSeq := extractSessionSeq(session.SessionID)

	// Use INSERT OR REPLACE to handle case where session_id might already exist
	// (e.g., from a previous session with different agent_type)
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO sessions (session_id, user_id, agent_type, session_seq, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		session.SessionID,
		session.UserID,
		string(session.AgentType),
		sessionSeq,
		string(data),
		createdAt,
		updatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store core session: %w", err)
	}

	return nil
}

// AddVisitedNode adds a visited node for a user
// This tracks nodes at user level, across all sessions
func (s *SQLiteStore) AddVisitedNode(userID string, nodeDigest *model.NodeDigest) {
	if nodeDigest == nil {
		return
	}

	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if userNodes, ok := s.userNodes.Load(userID); ok {
		un := userNodes.(*UserNodes)
		if un.VisitedNodes == nil {
			un.VisitedNodes = make(map[string]*model.NodeDigest)
		}
		un.VisitedNodes[nodeDigest.Path] = nodeDigest
		un.LastActivity = time.Now()
		s.userNodes.Store(userID, un)
	} else {
		un := &UserNodes{
			VisitedNodes: map[string]*model.NodeDigest{
				nodeDigest.Path: nodeDigest,
			},
			LastActivity: time.Now(),
		}
		s.userNodes.Store(userID, un)
	}
}

// GetVisitedNodes returns all visited nodes for a user
func (s *SQLiteStore) GetVisitedNodes(userID string) map[string]*model.NodeDigest {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if userNodes, ok := s.userNodes.Load(userID); ok {
		un := userNodes.(*UserNodes)
		// Return a copy to prevent external modification
		result := make(map[string]*model.NodeDigest)
		for k, v := range un.VisitedNodes {
			// Create a copy of NodeDigest
			digestCopy := *v
			result[k] = &digestCopy
		}
		return result
	}
	return make(map[string]*model.NodeDigest)
}

// GetVisitedNodePaths returns a list of visited node paths for a user
func (s *SQLiteStore) GetVisitedNodePaths(userID string) []string {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if userNodes, ok := s.userNodes.Load(userID); ok {
		un := userNodes.(*UserNodes)
		paths := make([]string, 0, len(un.VisitedNodes))
		for path := range un.VisitedNodes {
			paths = append(paths, path)
		}
		return paths
	}
	return []string{}
}

// HasVisitedNode checks if a user has visited a specific node
func (s *SQLiteStore) HasVisitedNode(userID string, nodePath string) bool {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if userNodes, ok := s.userNodes.Load(userID); ok {
		un := userNodes.(*UserNodes)
		_, exists := un.VisitedNodes[nodePath]
		return exists
	}
	return false
}

// ClearVisitedNodes clears all visited nodes for a user
func (s *SQLiteStore) ClearVisitedNodes(userID string) {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	s.userNodes.Delete(userID)
}

// NewSQLiteStoreFromFile creates a new SQLite session store from a file path
// This is a convenience function that creates the store and handles errors
// Example: store, err := NewSQLiteStoreFromFile("./data/sessions.db")
func NewSQLiteStoreFromFile(dbPath string) (model.SessionStore, error) {
	return NewSQLiteStore(dbPath)
}

// GetUser retrieves a user by ID
func (s *SQLiteStore) GetUser(userID string) (*model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data string
	var createdAt, updatedAt int64

	err := s.db.QueryRow(
		"SELECT data, created_at, updated_at FROM users WHERE user_id = ?",
		userID,
	).Scan(&data, &createdAt, &updatedAt)

	if err == sql.ErrNoRows {
		return nil, nil // User not found, return nil without error
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user: %w", err)
	}

	user := &model.User{}
	if err := json.Unmarshal([]byte(data), user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user: %w", err)
	}

	// Restore timestamps
	user.CreatedAt = time.Unix(createdAt, 0)
	user.UpdatedAt = time.Unix(updatedAt, 0)

	// Initialize ActiveSessionIDs if nil (backward compatibility for old users)
	if user.ActiveSessionIDs == nil {
		user.ActiveSessionIDs = make(map[model.AgentType]string)
	}

	// Initialize SessionSeqs if nil (backward compatibility for old users)
	if user.SessionSeqs == nil {
		user.SessionSeqs = make(map[model.AgentType]int)
	}

	return user, nil
}

// PutUser stores or updates a user
func (s *SQLiteStore) PutUser(user *model.User) error {
	if err := validateUser(user); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	user.UpdatedAt = time.Now()

	// Serialize user to JSON
	data, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user: %w", err)
	}

	createdAt := user.CreatedAt.Unix()
	updatedAt := user.UpdatedAt.Unix()

	// Use INSERT OR REPLACE for upsert behavior
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO users (user_id, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?)`,
		user.UserID,
		string(data),
		createdAt,
		updatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store user: %w", err)
	}

	return nil
}

// GetOrCreateUser gets an existing user or creates a new one
func (s *SQLiteStore) GetOrCreateUser(userID string) (*model.User, error) {
	user, err := s.GetUser(userID)
	if err != nil {
		return nil, err
	}
	if user != nil {
		needsSave := false

		// Compute SessionSeqs from existing sessions if empty (backward compatibility)
		if len(user.SessionSeqs) == 0 {
			if err := s.computeSessionSeqs(user); err == nil && len(user.SessionSeqs) > 0 {
				needsSave = true
			}
		}

		// Compute ActiveSessionIDs from existing sessions if empty (backward compatibility)
		if len(user.ActiveSessionIDs) == 0 {
			if err := s.computeActiveSessionIDs(user); err == nil && len(user.ActiveSessionIDs) > 0 {
				needsSave = true
			}
		}

		// Save user if any backward compatibility computation was done. The
		// backfill is logged so a buggy computation can be traced instead of
		// silently poisoning the record.
		if needsSave {
			log.Log.Infof("store: backfilled SessionSeqs/ActiveSessionIDs for user %s (seqs=%v, active=%v)",
				user.UserID, user.SessionSeqs, user.ActiveSessionIDs)
			if err := s.PutUser(user); err != nil {
				log.Log.Warnf("store: failed to persist backfilled user %s: %v", user.UserID, err)
			}
		}

		return user, nil
	}

	// Create new user
	user = model.NewUser(userID)
	if err := s.PutUser(user); err != nil {
		return nil, err
	}

	return user, nil
}

// computeSessionSeqs computes SessionSeqs from existing sessions for backward compatibility
// This is called when a user has no SessionSeqs (old user migrating to new format)
// Uses MAX(session_seq) to handle cases where sessions have been deleted
func (s *SQLiteStore) computeSessionSeqs(user *model.User) error {
	if user == nil {
		return nil
	}

	// Get max session_seq for each agent type
	rows, err := s.db.Query(
		`SELECT agent_type, MAX(session_seq) FROM sessions WHERE user_id = ? GROUP BY agent_type`,
		user.UserID,
	)
	if err != nil {
		return fmt.Errorf("failed to query max session seqs: %w", err)
	}
	defer rows.Close()

	// Update user's SessionSeqs
	if user.SessionSeqs == nil {
		user.SessionSeqs = make(map[model.AgentType]int)
	}

	for rows.Next() {
		var agentType string
		var maxSeq sql.NullInt64
		if err := rows.Scan(&agentType, &maxSeq); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}
		if maxSeq.Valid && agentType != "" {
			user.SessionSeqs[model.AgentType(agentType)] = int(maxSeq.Int64)
		}
	}

	return rows.Err()
}

// computeActiveSessionIDs computes ActiveSessionIDs from existing sessions for backward compatibility
// This is called when a user has no ActiveSessionIDs (old user migrating to new format)
// For each agent type, it selects the most recently updated session as the active session
func (s *SQLiteStore) computeActiveSessionIDs(user *model.User) error {
	if user == nil {
		return nil
	}

	// Get all sessions for this user
	sessions, err := s.List(user.UserID)
	if err != nil {
		return err
	}

	// Find the most recent session for each agent type
	latestByType := make(map[model.AgentType]*model.Session)
	for _, session := range sessions {
		if session.AgentType == "" {
			continue
		}
		existing := latestByType[session.AgentType]
		if existing == nil || session.UpdatedAt.After(existing.UpdatedAt) {
			latestByType[session.AgentType] = session
		}
	}

	// Update user's ActiveSessionIDs
	if user.ActiveSessionIDs == nil {
		user.ActiveSessionIDs = make(map[model.AgentType]string)
	}
	for agentType, session := range latestByType {
		user.ActiveSessionIDs[agentType] = session.SessionID
	}

	return nil
}

// checkMessageQuota enforces MaxMessagesPerSession (when configured) for n new
// messages in sessionID. Callers must hold s.mu.
func (s *SQLiteStore) checkMessageQuota(sessionID string, n int) error {
	if s.quotas.MaxMessagesPerSession <= 0 {
		return nil
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		return fmt.Errorf("failed to count messages for quota: %w", err)
	}
	if count+n > s.quotas.MaxMessagesPerSession {
		return fmt.Errorf("%w: session %s has %d messages (max %d)",
			ErrQuotaExceeded, sessionID, count, s.quotas.MaxMessagesPerSession)
	}
	return nil
}

// messageInsertArgs flattens a message into the columns of the messages table.
func messageInsertArgs(message *model.Message) []interface{} {
	hasToolCalls := 0
	if message.HasToolCalls {
		hasToolCalls = 1
	}
	isNonsense := 0
	if message.IsNonsense {
		isNonsense = 1
	}
	return []interface{}{
		message.MessageID,
		message.SeqID,
		message.UserID,
		message.SessionID,
		message.Role,
		message.Content,
		message.Model,
		string(message.AgentType),
		string(message.ContentType),
		message.PromptTokens,
		message.CompletionTokens,
		message.TotalTokens,
		message.RequestModel,
		message.MaxTokens,
		message.Temperature,
		hasToolCalls,
		message.FinishReason,
		isNonsense,
		message.CreatedAt.Unix(),
	}
}

const messageInsertSQL = `INSERT OR REPLACE INTO messages (
		message_id, seq_id, user_id, session_id, role, content, model,
		agent_type, content_type,
		prompt_tokens, completion_tokens, total_tokens,
		request_model, max_tokens, temperature, has_tool_calls, finish_reason, is_nonsense, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// PutMessage stores a message in the database
func (s *SQLiteStore) PutMessage(message *model.Message) error {
	if err := validateMessage(message); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.checkMessageQuota(message.SessionID, 1); err != nil {
		return err
	}

	if _, err := s.execWrite(messageInsertSQL, messageInsertArgs(message)...); err != nil {
		return fmt.Errorf("failed to store message: %w", err)
	}
	return nil
}

// PutMessages stores a batch of messages in one transaction (a single fsync
// instead of O(N) round trips). All-or-nothing: any invalid message or write
// error rolls the whole batch back.
func (s *SQLiteStore) PutMessages(messages []*model.Message) error {
	if len(messages) == 0 {
		return nil
	}
	for _, m := range messages {
		if err := validateMessage(m); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.quotas.MaxMessagesPerSession > 0 {
		perSession := make(map[string]int)
		for _, m := range messages {
			perSession[m.SessionID]++
		}
		for sessionID, n := range perSession {
			if err := s.checkMessageQuota(sessionID, n); err != nil {
				return err
			}
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(messageInsertSQL)
	if err != nil {
		return fmt.Errorf("failed to prepare insert: %w", err)
	}
	defer stmt.Close()

	for _, m := range messages {
		if _, err := stmt.Exec(messageInsertArgs(m)...); err != nil {
			return fmt.Errorf("failed to store message %s: %w", m.MessageID, err)
		}
	}
	return tx.Commit()
}

const messageSelectColumns = `message_id, seq_id, user_id, session_id, role, content, model,
			agent_type, content_type,
			prompt_tokens, completion_tokens, total_tokens,
			request_model, max_tokens, temperature, has_tool_calls, finish_reason, is_nonsense, created_at`

// scanMessages decodes message rows produced by a messageSelectColumns query.
func scanMessages(rows *sql.Rows) ([]*model.Message, error) {
	var messages []*model.Message
	for rows.Next() {
		msg := &model.Message{}
		var createdAt int64
		var hasToolCallsInt int
		var isNonsenseInt int
		var agentType, contentType string

		err := rows.Scan(
			&msg.MessageID,
			&msg.SeqID,
			&msg.UserID,
			&msg.SessionID,
			&msg.Role,
			&msg.Content,
			&msg.Model,
			&agentType,
			&contentType,
			&msg.PromptTokens,
			&msg.CompletionTokens,
			&msg.TotalTokens,
			&msg.RequestModel,
			&msg.MaxTokens,
			&msg.Temperature,
			&hasToolCallsInt,
			&msg.FinishReason,
			&isNonsenseInt,
			&createdAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		msg.AgentType = model.AgentType(agentType)
		msg.ContentType = model.ContentType(contentType)
		msg.HasToolCalls = hasToolCallsInt != 0
		msg.IsNonsense = isNonsenseInt != 0
		msg.CreatedAt = time.Unix(createdAt, 0)
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating messages: %w", err)
	}
	return messages, nil
}

// messagesPageSize is the internal page size GetMessagesBySession uses so a
// huge session never sits in one unbounded result set on the database side.
const messagesPageSize = 1000

// GetMessagesBySessionPage returns one page of a session's messages, newest
// first (the same ordering as GetMessagesBySession). limit <= 0 defaults to
// messagesPageSize; offset < 0 is treated as 0.
func (s *SQLiteStore) GetMessagesBySessionPage(sessionID string, limit, offset int) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getMessagesBySessionPageLocked(sessionID, limit, offset)
}

func (s *SQLiteStore) getMessagesBySessionPageLocked(sessionID string, limit, offset int) ([]*model.Message, error) {
	if limit <= 0 {
		limit = messagesPageSize
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(
		`SELECT `+messageSelectColumns+`
		FROM messages WHERE session_id = ? ORDER BY created_at DESC, seq_id DESC LIMIT ? OFFSET ?`,
		sessionID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows)
}

// GetMessagesBySession returns all messages for a session, newest first. It is
// implemented via pages internally; prefer GetMessagesBySessionPage for
// sessions that can grow without bound.
func (s *SQLiteStore) GetMessagesBySession(sessionID string) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var all []*model.Message
	for offset := 0; ; offset += messagesPageSize {
		page, err := s.getMessagesBySessionPageLocked(sessionID, messagesPageSize, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < messagesPageSize {
			return all, nil
		}
	}
}

// GetMessagesByUser returns all messages for a user
func (s *SQLiteStore) GetMessagesByUser(userID string) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT `+messageSelectColumns+`
		FROM messages WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows)
}

// AddOpenedFile records that a file was opened in a session
func (s *SQLiteStore) AddOpenedFile(openedFile *model.OpenedFile) error {
	if err := validateOpenedFile(openedFile); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	openedAt := openedFile.OpenedAt.Unix()
	var closedAt int64
	if !openedFile.ClosedAt.IsZero() {
		closedAt = openedFile.ClosedAt.Unix()
	}

	isOpen := 0
	if openedFile.IsOpen {
		isOpen = 1
	}

	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO opened_files (
			file_id, session_id, user_id, file_path, file_name, opened_at, closed_at, is_open
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		openedFile.FileID,
		openedFile.SessionID,
		openedFile.UserID,
		openedFile.FilePath,
		openedFile.FileName,
		openedAt,
		closedAt,
		isOpen,
	)

	if err != nil {
		return fmt.Errorf("failed to store opened file: %w", err)
	}

	return nil
}

// CloseOpenedFile marks a file as closed
func (s *SQLiteStore) CloseOpenedFile(sessionID string, filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	closedAt := time.Now().Unix()

	_, err := s.db.Exec(
		`UPDATE opened_files 
		 SET is_open = 0, closed_at = ? 
		 WHERE session_id = ? AND file_path = ? AND is_open = 1`,
		closedAt,
		sessionID,
		filePath,
	)

	if err != nil {
		return fmt.Errorf("failed to close opened file: %w", err)
	}

	return nil
}

// GetOpenedFilesBySession returns all opened files for a session
func (s *SQLiteStore) GetOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT file_id, session_id, user_id, file_path, file_name, opened_at, closed_at, is_open
		FROM opened_files WHERE session_id = ? ORDER BY opened_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query opened files: %w", err)
	}
	defer rows.Close()

	var files []*model.OpenedFile
	for rows.Next() {
		f := &model.OpenedFile{}
		var openedAt, closedAt int64
		var isOpenInt int

		err := rows.Scan(
			&f.FileID,
			&f.SessionID,
			&f.UserID,
			&f.FilePath,
			&f.FileName,
			&openedAt,
			&closedAt,
			&isOpenInt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan opened file: %w", err)
		}

		f.OpenedAt = time.Unix(openedAt, 0)
		if closedAt > 0 {
			f.ClosedAt = time.Unix(closedAt, 0)
		}
		f.IsOpen = isOpenInt != 0
		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating opened files: %w", err)
	}

	return files, nil
}

// GetCurrentlyOpenedFilesBySession returns only currently open files for a session
func (s *SQLiteStore) GetCurrentlyOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT file_id, session_id, user_id, file_path, file_name, opened_at, closed_at, is_open
		FROM opened_files WHERE session_id = ? AND is_open = 1 ORDER BY opened_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query opened files: %w", err)
	}
	defer rows.Close()

	var files []*model.OpenedFile
	for rows.Next() {
		f := &model.OpenedFile{}
		var openedAt, closedAt int64
		var isOpenInt int

		err := rows.Scan(
			&f.FileID,
			&f.SessionID,
			&f.UserID,
			&f.FilePath,
			&f.FileName,
			&openedAt,
			&closedAt,
			&isOpenInt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan opened file: %w", err)
		}

		f.OpenedAt = time.Unix(openedAt, 0)
		if closedAt > 0 {
			f.ClosedAt = time.Unix(closedAt, 0)
		}
		f.IsOpen = isOpenInt != 0
		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating opened files: %w", err)
	}

	return files, nil
}

// GetAllUsers returns all users
func (s *SQLiteStore) GetAllUsers() ([]*model.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		"SELECT data, created_at, updated_at FROM users ORDER BY created_at DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query users: %w", err)
	}
	defer rows.Close()

	var users []*model.User
	for rows.Next() {
		var data string
		var createdAt, updatedAt int64

		if err := rows.Scan(&data, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}

		user := &model.User{}
		if err := json.Unmarshal([]byte(data), user); err != nil {
			return nil, fmt.Errorf("failed to unmarshal user: %w", err)
		}

		// Restore timestamps
		user.CreatedAt = time.Unix(createdAt, 0)
		user.UpdatedAt = time.Unix(updatedAt, 0)

		users = append(users, user)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating users: %w", err)
	}

	return users, nil
}

// GetAllMessages returns all messages
func (s *SQLiteStore) GetAllMessages() ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT ` + messageSelectColumns + `
		FROM messages ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows)
}

// GetAllOpenedFiles returns all opened files
func (s *SQLiteStore) GetAllOpenedFiles() ([]*model.OpenedFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT file_id, session_id, user_id, file_path, file_name, opened_at, closed_at, is_open
		FROM opened_files ORDER BY opened_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query opened files: %w", err)
	}
	defer rows.Close()

	var files []*model.OpenedFile
	for rows.Next() {
		f := &model.OpenedFile{}
		var openedAt, closedAt int64
		var isOpenInt int

		err := rows.Scan(
			&f.FileID,
			&f.SessionID,
			&f.UserID,
			&f.FilePath,
			&f.FileName,
			&openedAt,
			&closedAt,
			&isOpenInt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan opened file: %w", err)
		}

		f.OpenedAt = time.Unix(openedAt, 0)
		if closedAt > 0 {
			f.ClosedAt = time.Unix(closedAt, 0)
		}
		f.IsOpen = isOpenInt != 0
		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating opened files: %w", err)
	}

	return files, nil
}

// GetOpenedFilesByUser returns opened files for a user sorted by OpenedAt (newest first).
func (s *SQLiteStore) GetOpenedFilesByUser(userID string) ([]*model.OpenedFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT file_id, session_id, user_id, file_path, file_name, opened_at, closed_at, is_open
		FROM opened_files WHERE user_id = ? ORDER BY opened_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query opened files by user: %w", err)
	}
	defer rows.Close()

	var files []*model.OpenedFile
	for rows.Next() {
		f := &model.OpenedFile{}
		var openedAt, closedAt int64
		var isOpenInt int

		err := rows.Scan(
			&f.FileID,
			&f.SessionID,
			&f.UserID,
			&f.FilePath,
			&f.FileName,
			&openedAt,
			&closedAt,
			&isOpenInt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan opened file: %w", err)
		}

		f.OpenedAt = time.Unix(openedAt, 0)
		if closedAt > 0 {
			f.ClosedAt = time.Unix(closedAt, 0)
		}
		f.IsOpen = isOpenInt != 0
		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating opened files: %w", err)
	}

	return files, nil
}

// PutUserFile inserts or updates a user file record.
func (s *SQLiteStore) PutUserFile(f *model.UserFile) error {
	if err := validateUserFile(f); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.checkUserFileQuota(f); err != nil {
		return err
	}

	_, err := s.execWrite(
		`INSERT OR REPLACE INTO user_files (
			file_id, user_id, session_id, name, mime_type, size, storage_key, source, parent_file_id, summary, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.FileID,
		f.UserID,
		f.SessionID,
		f.Name,
		f.MIMEType,
		f.Size,
		f.StorageKey,
		string(f.Source),
		f.ParentFileID,
		f.Summary,
		f.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("failed to store user file: %w", err)
	}
	return nil
}

// scanUserFile scans a single user_files row into a model.UserFile.
func scanUserFile(scanner interface {
	Scan(dest ...interface{}) error
}) (*model.UserFile, error) {
	f := &model.UserFile{}
	var source string
	var createdAt int64
	err := scanner.Scan(
		&f.FileID,
		&f.UserID,
		&f.SessionID,
		&f.Name,
		&f.MIMEType,
		&f.Size,
		&f.StorageKey,
		&source,
		&f.ParentFileID,
		&f.Summary,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}
	f.Source = model.FileSource(source)
	f.CreatedAt = time.Unix(createdAt, 0)
	return f, nil
}

const userFileColumns = `file_id, user_id, session_id, name, mime_type, size, storage_key, source, parent_file_id, summary, created_at`

// GetUserFile returns a single user file by ID, or nil if not found.
func (s *SQLiteStore) GetUserFile(fileID string) (*model.UserFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(
		`SELECT `+userFileColumns+` FROM user_files WHERE file_id = ?`,
		fileID,
	)
	f, err := scanUserFile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query user file: %w", err)
	}
	return f, nil
}

// queryUserFiles runs a user_files query and scans all rows.
func (s *SQLiteStore) queryUserFiles(query string, args ...interface{}) ([]*model.UserFile, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query user files: %w", err)
	}
	defer rows.Close()

	var files []*model.UserFile
	for rows.Next() {
		f, err := scanUserFile(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user file: %w", err)
		}
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating user files: %w", err)
	}
	return files, nil
}

// GetUserFilesByUser returns all files for a user, newest first.
func (s *SQLiteStore) GetUserFilesByUser(userID string) ([]*model.UserFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryUserFiles(
		`SELECT `+userFileColumns+` FROM user_files WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
}

// GetUserFilesBySession returns all files for a session, newest first.
func (s *SQLiteStore) GetUserFilesBySession(sessionID string) ([]*model.UserFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryUserFiles(
		`SELECT `+userFileColumns+` FROM user_files WHERE session_id = ? ORDER BY created_at DESC`,
		sessionID,
	)
}

// GetAllUserFiles returns all user files, newest first.
func (s *SQLiteStore) GetAllUserFiles() ([]*model.UserFile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.queryUserFiles(
		`SELECT ` + userFileColumns + ` FROM user_files ORDER BY created_at DESC`,
	)
}

// checkUserFileQuota enforces per-user file-count and byte quotas (when
// configured) for a new/updated file record. Callers must hold s.mu. Updates to
// an existing file_id don't count as a new file.
func (s *SQLiteStore) checkUserFileQuota(f *model.UserFile) error {
	if s.quotas.MaxUserFilesPerUser <= 0 && s.quotas.MaxFileBytesPerUser <= 0 {
		return nil
	}
	var count int
	var bytes sql.NullInt64
	var existing int
	err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(size), 0),
			COALESCE(SUM(CASE WHEN file_id = ? THEN 1 ELSE 0 END), 0)
		 FROM user_files WHERE user_id = ?`,
		f.FileID, f.UserID,
	).Scan(&count, &bytes, &existing)
	if err != nil {
		return fmt.Errorf("failed to count user files for quota: %w", err)
	}
	if s.quotas.MaxUserFilesPerUser > 0 && existing == 0 && count+1 > s.quotas.MaxUserFilesPerUser {
		return fmt.Errorf("%w: user %s has %d files (max %d)",
			ErrQuotaExceeded, f.UserID, count, s.quotas.MaxUserFilesPerUser)
	}
	if s.quotas.MaxFileBytesPerUser > 0 && bytes.Int64+f.Size > s.quotas.MaxFileBytesPerUser {
		return fmt.Errorf("%w: user %s would exceed %d bytes of files",
			ErrQuotaExceeded, f.UserID, s.quotas.MaxFileBytesPerUser)
	}
	return nil
}

// DeleteUserFile removes a user file metadata record by ID.
func (s *SQLiteStore) DeleteUserFile(fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.execWrite("DELETE FROM user_files WHERE file_id = ?", fileID); err != nil {
		return fmt.Errorf("failed to delete user file: %w", err)
	}
	auditDeletion("user_file", fileID, "")
	return nil
}

// GetSession is an alias for Get to match DebugStore interface
func (s *SQLiteStore) GetSession(sessionID string) (*model.Session, error) {
	return s.Get(sessionID)
}

// PutToolCall stores a tool call in the database
func (s *SQLiteStore) PutToolCall(toolCall *model.ToolCall) error {
	if err := validateToolCall(toolCall); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	createdAt := toolCall.CreatedAt.Unix()
	updatedAt := toolCall.UpdatedAt.Unix()

	status := toolCall.Status
	if status == "" {
		status = model.ToolCallStatusPending
	}
	displayLabel := toolCall.DisplayLabel
	// Use INSERT OR REPLACE for upsert behavior
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO tool_calls (
			tool_call_id, tool_id, message_id, session_id, user_id, agent_type, function_name, display_label, arguments, response, response_length, duration_ms, status, error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		toolCall.ToolCallID,
		toolCall.ToolID,
		toolCall.MessageID,
		toolCall.SessionID,
		toolCall.UserID,
		string(toolCall.AgentType),
		toolCall.FunctionName,
		displayLabel,
		toolCall.Arguments,
		toolCall.Response,
		toolCall.ResponseLength,
		toolCall.DurationMs,
		status,
		toolCall.Error,
		createdAt,
		updatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store tool call: %w", err)
	}

	return nil
}

// UpdateToolCallResponse updates the response for a tool call by ToolID and calculates duration.
// When execErr != nil, sets status='failed' and error=execErr.Error().
func (s *SQLiteStore) UpdateToolCallResponse(toolID string, response string, execErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	updatedAt := now.Unix()
	responseLength := utf8.RuneCountInString(response)

	status := model.ToolCallStatusSuccess
	errorMsg := ""
	if execErr != nil {
		status = model.ToolCallStatusFailed
		errorMsg = execErr.Error()
	}

	// Get created_at to calculate duration (look up by tool_id)
	var createdAtUnix int64
	err := s.db.QueryRow(
		"SELECT created_at FROM tool_calls WHERE tool_id = ?",
		toolID,
	).Scan(&createdAtUnix)

	var durationMs int64
	if err == nil {
		createdAt := time.Unix(createdAtUnix, 0)
		durationMs = now.Sub(createdAt).Milliseconds()
	}

	_, err = s.db.Exec(
		`UPDATE tool_calls 
		 SET response = ?, response_length = ?, duration_ms = ?, status = ?, error = ?, updated_at = ? 
		 WHERE tool_id = ?`,
		response,
		responseLength,
		durationMs,
		status,
		errorMsg,
		updatedAt,
		toolID,
	)

	if err != nil {
		return fmt.Errorf("failed to update tool call response: %w", err)
	}

	return nil
}

// GetToolCallsBySession returns all tool calls for a session
func (s *SQLiteStore) GetToolCallsBySession(sessionID string) ([]*model.ToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT tool_call_id, tool_id, message_id, session_id, user_id, agent_type, function_name, COALESCE(display_label,'') as display_label, arguments, response, response_length, duration_ms, status, error, created_at, updated_at
		FROM tool_calls WHERE session_id = ? ORDER BY created_at DESC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query tool calls: %w", err)
	}
	defer rows.Close()

	var toolCalls []*model.ToolCall
	for rows.Next() {
		tc := &model.ToolCall{}
		var createdAt, updatedAt int64
		var agentType string

		err := rows.Scan(
			&tc.ToolCallID,
			&tc.ToolID,
			&tc.MessageID,
			&tc.SessionID,
			&tc.UserID,
			&agentType,
			&tc.FunctionName,
			&tc.DisplayLabel,
			&tc.Arguments,
			&tc.Response,
			&tc.ResponseLength,
			&tc.DurationMs,
			&tc.Status,
			&tc.Error,
			&createdAt,
			&updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan tool call: %w", err)
		}

		tc.AgentType = model.AgentType(agentType)
		tc.CreatedAt = time.Unix(createdAt, 0)
		tc.UpdatedAt = time.Unix(updatedAt, 0)
		toolCalls = append(toolCalls, tc)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tool calls: %w", err)
	}

	return toolCalls, nil
}

// GetAllToolCalls returns all tool calls
func (s *SQLiteStore) GetAllToolCalls() ([]*model.ToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT tool_call_id, tool_id, message_id, session_id, user_id, agent_type, function_name, COALESCE(display_label,'') as display_label, arguments, response, response_length, duration_ms, status, error, created_at, updated_at
		FROM tool_calls ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query tool calls: %w", err)
	}
	defer rows.Close()

	var toolCalls []*model.ToolCall
	for rows.Next() {
		tc := &model.ToolCall{}
		var createdAt, updatedAt int64
		var agentType string

		err := rows.Scan(
			&tc.ToolCallID,
			&tc.ToolID,
			&tc.MessageID,
			&tc.SessionID,
			&tc.UserID,
			&agentType,
			&tc.FunctionName,
			&tc.DisplayLabel,
			&tc.Arguments,
			&tc.Response,
			&tc.ResponseLength,
			&tc.DurationMs,
			&tc.Status,
			&tc.Error,
			&createdAt,
			&updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan tool call: %w", err)
		}

		tc.AgentType = model.AgentType(agentType)
		tc.CreatedAt = time.Unix(createdAt, 0)
		tc.UpdatedAt = time.Unix(updatedAt, 0)
		toolCalls = append(toolCalls, tc)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tool calls: %w", err)
	}

	return toolCalls, nil
}

// GetToolCallByID returns a tool call by its ID
func (s *SQLiteStore) GetToolCallByID(toolCallID string) (*model.ToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(
		`SELECT tool_call_id, tool_id, message_id, session_id, user_id, agent_type, function_name, COALESCE(display_label,'') as display_label, arguments, response, response_length, duration_ms, status, error, created_at, updated_at
		FROM tool_calls WHERE tool_call_id = ?`,
		toolCallID,
	)

	tc := &model.ToolCall{}
	var createdAt, updatedAt int64
	var agentType string

	err := row.Scan(
		&tc.ToolCallID,
		&tc.ToolID,
		&tc.MessageID,
		&tc.SessionID,
		&tc.UserID,
		&agentType,
		&tc.FunctionName,
		&tc.DisplayLabel,
		&tc.Arguments,
		&tc.Response,
		&tc.ResponseLength,
		&tc.DurationMs,
		&tc.Status,
		&tc.Error,
		&createdAt,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil // optional lookup: not found is not an error (contract parity)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get tool call: %w", err)
	}

	tc.AgentType = model.AgentType(agentType)
	tc.CreatedAt = time.Unix(createdAt, 0)
	tc.UpdatedAt = time.Unix(updatedAt, 0)

	return tc, nil
}

// GetToolCallByToolID returns a tool call by its ToolID (sequential ID)
func (s *SQLiteStore) GetToolCallByToolID(toolID string) (*model.ToolCall, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(
		`SELECT tool_call_id, tool_id, message_id, session_id, user_id, agent_type, function_name, COALESCE(display_label,'') as display_label, arguments, response, response_length, duration_ms, status, error, created_at, updated_at
		FROM tool_calls WHERE tool_id = ?`,
		toolID,
	)

	tc := &model.ToolCall{}
	var createdAt, updatedAt int64
	var agentType string

	err := row.Scan(
		&tc.ToolCallID,
		&tc.ToolID,
		&tc.MessageID,
		&tc.SessionID,
		&tc.UserID,
		&agentType,
		&tc.FunctionName,
		&tc.DisplayLabel,
		&tc.Arguments,
		&tc.Response,
		&tc.ResponseLength,
		&tc.DurationMs,
		&tc.Status,
		&tc.Error,
		&createdAt,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil // optional lookup: not found is not an error (contract parity)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get tool call by tool ID: %w", err)
	}

	tc.AgentType = model.AgentType(agentType)
	tc.CreatedAt = time.Unix(createdAt, 0)
	tc.UpdatedAt = time.Unix(updatedAt, 0)

	return tc, nil
}

// PutSummarizationLog stores a summarization log entry in the database
func (s *SQLiteStore) PutSummarizationLog(log *model.SummarizationLog) error {
	if log == nil {
		return fmt.Errorf("summarization log cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	createdAt := log.CreatedAt.Unix()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
		log.CreatedAt = time.Now()
	}

	var completedAt *int64
	if !log.CompletedAt.IsZero() {
		ts := log.CompletedAt.Unix()
		completedAt = &ts
	}

	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO summarization_logs (
			log_id, session_id, user_id, session_title, previous_summary, previous_tags,
			messages_before_count, messages_after_count, archived_messages_count,
			prompt_sent, response_received, model_used, requested_model,
			generated_summary, generated_tags, generated_title,
			prompt_tokens, completion_tokens, total_tokens, duration_ms,
			status, error_message, summarization_type, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.LogID,
		log.SessionID,
		log.UserID,
		log.SessionTitle,
		log.PreviousSummary,
		log.PreviousTags,
		log.MessagesBeforeCount,
		log.MessagesAfterCount,
		log.ArchivedMessagesCount,
		log.PromptSent,
		log.ResponseReceived,
		log.ModelUsed,
		log.RequestedModel,
		log.GeneratedSummary,
		log.GeneratedTags,
		log.GeneratedTitle,
		log.PromptTokens,
		log.CompletionTokens,
		log.TotalTokens,
		log.DurationMs,
		log.Status,
		log.ErrorMessage,
		log.SummarizationType,
		createdAt,
		completedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store summarization log: %w", err)
	}

	return nil
}

// GetSummarizationLogsBySession returns all summarization logs for a session
func (s *SQLiteStore) GetSummarizationLogsBySession(sessionID string) ([]*model.SummarizationLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT log_id, session_id, user_id, session_title, previous_summary, previous_tags,
			messages_before_count, messages_after_count, archived_messages_count,
			prompt_sent, response_received, model_used, requested_model,
			generated_summary, generated_tags, generated_title,
			prompt_tokens, completion_tokens, total_tokens, duration_ms,
			status, error_message, summarization_type, created_at, completed_at
		FROM summarization_logs WHERE session_id = ? ORDER BY created_at DESC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query summarization logs: %w", err)
	}
	defer rows.Close()

	return s.scanSummarizationLogs(rows)
}

// GetAllSummarizationLogs returns all summarization logs
func (s *SQLiteStore) GetAllSummarizationLogs() ([]*model.SummarizationLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT log_id, session_id, user_id, session_title, previous_summary, previous_tags,
			messages_before_count, messages_after_count, archived_messages_count,
			prompt_sent, response_received, model_used, requested_model,
			generated_summary, generated_tags, generated_title,
			prompt_tokens, completion_tokens, total_tokens, duration_ms,
			status, error_message, summarization_type, created_at, completed_at
		FROM summarization_logs ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query summarization logs: %w", err)
	}
	defer rows.Close()

	return s.scanSummarizationLogs(rows)
}

// scanSummarizationLogs scans rows into SummarizationLog objects
func (s *SQLiteStore) scanSummarizationLogs(rows *sql.Rows) ([]*model.SummarizationLog, error) {
	var logs []*model.SummarizationLog
	for rows.Next() {
		log := &model.SummarizationLog{}
		var createdAt int64
		var completedAt sql.NullInt64
		var sessionTitle, previousSummary, previousTags sql.NullString
		var requestedModel, generatedSummary, generatedTags, generatedTitle sql.NullString
		var summarizationType sql.NullString

		err := rows.Scan(
			&log.LogID,
			&log.SessionID,
			&log.UserID,
			&sessionTitle,
			&previousSummary,
			&previousTags,
			&log.MessagesBeforeCount,
			&log.MessagesAfterCount,
			&log.ArchivedMessagesCount,
			&log.PromptSent,
			&log.ResponseReceived,
			&log.ModelUsed,
			&requestedModel,
			&generatedSummary,
			&generatedTags,
			&generatedTitle,
			&log.PromptTokens,
			&log.CompletionTokens,
			&log.TotalTokens,
			&log.DurationMs,
			&log.Status,
			&log.ErrorMessage,
			&summarizationType,
			&createdAt,
			&completedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan summarization log: %w", err)
		}

		log.CreatedAt = time.Unix(createdAt, 0)
		if completedAt.Valid {
			log.CompletedAt = time.Unix(completedAt.Int64, 0)
		}
		if sessionTitle.Valid {
			log.SessionTitle = sessionTitle.String
		}
		if previousSummary.Valid {
			log.PreviousSummary = previousSummary.String
		}
		if previousTags.Valid {
			log.PreviousTags = previousTags.String
		}
		if requestedModel.Valid {
			log.RequestedModel = requestedModel.String
		}
		if generatedSummary.Valid {
			log.GeneratedSummary = generatedSummary.String
		}
		if generatedTags.Valid {
			log.GeneratedTags = generatedTags.String
		}
		if generatedTitle.Valid {
			log.GeneratedTitle = generatedTitle.String
		}
		if summarizationType.Valid {
			log.SummarizationType = summarizationType.String
		}

		logs = append(logs, log)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating summarization logs: %w", err)
	}

	return logs, nil
}

// ============================================================================
// Route traces (Core decision/forward DAGs)
// ============================================================================

// PutRouteTrace upserts a route trace keyed by TraceID. The full DAG is stored
// as JSON in the data column.
func (s *SQLiteStore) PutRouteTrace(trace *model.RouteTrace) error {
	if trace == nil {
		return fmt.Errorf("route trace cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	createdAt := trace.CreatedAt.Unix()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
		trace.CreatedAt = time.Unix(createdAt, 0)
	}

	data, err := json.Marshal(trace)
	if err != nil {
		return fmt.Errorf("failed to marshal route trace: %w", err)
	}

	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO route_traces (trace_id, session_id, user_id, status, data, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		trace.TraceID, trace.SessionID, trace.UserID, trace.Status, string(data), createdAt,
	)
	if err != nil {
		return fmt.Errorf("failed to store route trace: %w", err)
	}
	return nil
}

// GetRouteTraceByID returns the trace by id, or (nil, nil) when not found.
func (s *SQLiteStore) GetRouteTraceByID(traceID string) (*model.RouteTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var data string
	err := s.db.QueryRow(`SELECT data FROM route_traces WHERE trace_id = ?`, traceID).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query route trace: %w", err)
	}
	trace := &model.RouteTrace{}
	if err := json.Unmarshal([]byte(data), trace); err != nil {
		return nil, fmt.Errorf("failed to unmarshal route trace: %w", err)
	}
	return trace, nil
}

// GetRouteTracesBySession returns all traces for a session, newest first.
func (s *SQLiteStore) GetRouteTracesBySession(sessionID string) ([]*model.RouteTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT data FROM route_traces WHERE session_id = ? ORDER BY created_at DESC, trace_id DESC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query route traces: %w", err)
	}
	defer rows.Close()
	return scanRouteTraces(rows)
}

// GetRouteTracesByUser returns all traces for a user, newest first.
func (s *SQLiteStore) GetRouteTracesByUser(userID string) ([]*model.RouteTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT data FROM route_traces WHERE user_id = ? ORDER BY created_at DESC, trace_id DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query route traces: %w", err)
	}
	defer rows.Close()
	return scanRouteTraces(rows)
}

// GetAllRouteTraces returns all traces across all sessions, newest first.
func (s *SQLiteStore) GetAllRouteTraces() ([]*model.RouteTrace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`SELECT data FROM route_traces ORDER BY created_at DESC, trace_id DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to query route traces: %w", err)
	}
	defer rows.Close()
	return scanRouteTraces(rows)
}

// scanRouteTraces decodes the JSON data column of each row into a RouteTrace.
func scanRouteTraces(rows *sql.Rows) ([]*model.RouteTrace, error) {
	var traces []*model.RouteTrace
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("failed to scan route trace: %w", err)
		}
		trace := &model.RouteTrace{}
		if err := json.Unmarshal([]byte(data), trace); err != nil {
			return nil, fmt.Errorf("failed to unmarshal route trace: %w", err)
		}
		traces = append(traces, trace)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating route traces: %w", err)
	}
	return traces, nil
}

// Ensure SQLiteStore implements model.SessionStore and debuger.DebugStore
var (
	_ model.SessionStore = (*SQLiteStore)(nil)
	_ debuger.DebugStore = (*SQLiteStore)(nil)
)
