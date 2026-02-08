package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ghiac/agentize/model"
	_ "modernc.org/sqlite"
)

// SQLiteStore is a SQLite implementation of SessionStore
// It stores sessions in a SQLite database with JSON serialization
type SQLiteStore struct {
	db   *sql.DB
	mu   sync.RWMutex
	path string

	// UserNodes tracks visited nodes for each user (user-level, not session-level)
	userNodes sync.Map
	userLock  map[string]*sync.Mutex
	nodesMu   sync.RWMutex // Protects userLock map
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

	store := &SQLiteStore{
		db:       db,
		path:     dbPath,
		userLock: make(map[string]*sync.Mutex),
	}

	// Create tables
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
}

// initSchema creates the necessary tables
func (s *SQLiteStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		session_id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		agent_type TEXT NOT NULL,
		data TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	
	CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
	CREATE INDEX IF NOT EXISTS idx_sessions_updated_at ON sessions(updated_at);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_user_core ON sessions(user_id, agent_type) WHERE agent_type = 'core';
	
	CREATE TABLE IF NOT EXISTS users (
		user_id TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	
	CREATE TABLE IF NOT EXISTS messages (
		message_id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		model TEXT,
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
	
	CREATE TABLE IF NOT EXISTS tool_calls (
		tool_call_id TEXT PRIMARY KEY,
		message_id TEXT NOT NULL,
		session_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		function_name TEXT NOT NULL,
		arguments TEXT NOT NULL,
		response TEXT DEFAULT '',
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

	// Migration: Add is_nonsense column if it doesn't exist (for existing databases)
	// SQLite doesn't support IF NOT EXISTS for ALTER TABLE ADD COLUMN, so we ignore errors
	_ = s.migrateAddIsNonsenseColumn()

	// Migration: Add new columns to summarization_logs table for existing databases
	_ = s.migrateSummarizationLogsColumns()

	return nil
}

// migrateAddIsNonsenseColumn adds is_nonsense column to messages table if it doesn't exist
func (s *SQLiteStore) migrateAddIsNonsenseColumn() error {
	_, _ = s.db.Exec(`ALTER TABLE messages ADD COLUMN is_nonsense INTEGER DEFAULT 0`)
	// Ignore error if column already exists
	return nil
}

// migrateSummarizationLogsColumns adds new columns to summarization_logs table for existing databases
func (s *SQLiteStore) migrateSummarizationLogsColumns() error {
	// Add new columns - ignore errors if columns already exist
	columns := []string{
		`ALTER TABLE summarization_logs ADD COLUMN session_title TEXT`,
		`ALTER TABLE summarization_logs ADD COLUMN previous_summary TEXT`,
		`ALTER TABLE summarization_logs ADD COLUMN previous_tags TEXT`,
		`ALTER TABLE summarization_logs ADD COLUMN messages_before_count INTEGER DEFAULT 0`,
		`ALTER TABLE summarization_logs ADD COLUMN messages_after_count INTEGER DEFAULT 0`,
		`ALTER TABLE summarization_logs ADD COLUMN archived_messages_count INTEGER DEFAULT 0`,
		`ALTER TABLE summarization_logs ADD COLUMN requested_model TEXT`,
		`ALTER TABLE summarization_logs ADD COLUMN generated_summary TEXT`,
		`ALTER TABLE summarization_logs ADD COLUMN generated_tags TEXT`,
		`ALTER TABLE summarization_logs ADD COLUMN generated_title TEXT`,
		`ALTER TABLE summarization_logs ADD COLUMN duration_ms INTEGER DEFAULT 0`,
		`ALTER TABLE summarization_logs ADD COLUMN summarization_type TEXT`,
		`ALTER TABLE summarization_logs ADD COLUMN completed_at INTEGER`,
	}

	for _, col := range columns {
		_, _ = s.db.Exec(col)
	}

	// Add index for status
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_summarization_logs_status ON summarization_logs(status)`)

	return nil
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// getOrCreateLock gets or creates a mutex for a userID
func (s *SQLiteStore) getOrCreateLock(userID string) *sync.Mutex {
	s.nodesMu.RLock()
	lock, exists := s.userLock[userID]
	s.nodesMu.RUnlock()

	if exists {
		return lock
	}

	s.nodesMu.Lock()
	defer s.nodesMu.Unlock()

	// Double check after acquiring write lock
	if lock, exists := s.userLock[userID]; exists {
		return lock
	}

	lock = &sync.Mutex{}
	s.userLock[userID] = lock
	return lock
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

	return session, nil
}

// Put stores or updates a session
// For Core sessions, this ensures only one Core session exists per user
func (s *SQLiteStore) Put(session *model.Session) error {
	if session == nil {
		return fmt.Errorf("session cannot be nil")
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

	// Use INSERT OR REPLACE for upsert behavior
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO sessions (session_id, user_id, agent_type, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		session.SessionID,
		session.UserID,
		string(session.AgentType),
		string(data),
		createdAt,
		updatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store session: %w", err)
	}

	return nil
}

// Delete removes a session
func (s *SQLiteStore) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM sessions WHERE session_id = ?", sessionID)
	if err != nil {
		return fmt.Errorf("failed to delete session: %w", err)
	}

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

	return session, nil
}

// PutCoreSession stores or updates a Core session for a user
// This ensures only one Core session exists per user by deleting any existing Core sessions first
func (s *SQLiteStore) PutCoreSession(session *model.Session) error {
	if session == nil {
		return fmt.Errorf("session cannot be nil")
	}
	if session.AgentType != model.AgentTypeCore {
		return fmt.Errorf("session must be of type Core")
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

	// Use INSERT OR REPLACE to handle case where session_id might already exist
	// (e.g., from a previous session with different agent_type)
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO sessions (session_id, user_id, agent_type, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		session.SessionID,
		session.UserID,
		string(session.AgentType),
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

	return user, nil
}

// PutUser stores or updates a user
func (s *SQLiteStore) PutUser(user *model.User) error {
	if user == nil {
		return fmt.Errorf("user cannot be nil")
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
		return user, nil
	}

	// Create new user
	user = model.NewUser(userID)
	if err := s.PutUser(user); err != nil {
		return nil, err
	}

	return user, nil
}

// PutMessage stores a message in the database
func (s *SQLiteStore) PutMessage(message *model.Message) error {
	if message == nil {
		return fmt.Errorf("message cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	createdAt := message.CreatedAt.Unix()

	// Convert bool to int for SQLite
	hasToolCalls := 0
	if message.HasToolCalls {
		hasToolCalls = 1
	}
	isNonsense := 0
	if message.IsNonsense {
		isNonsense = 1
	}

	// Use INSERT OR REPLACE for upsert behavior
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO messages (
			message_id, user_id, session_id, role, content, model,
			prompt_tokens, completion_tokens, total_tokens,
			request_model, max_tokens, temperature, has_tool_calls, finish_reason, is_nonsense, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		message.MessageID,
		message.UserID,
		message.SessionID,
		message.Role,
		message.Content,
		message.Model,
		message.PromptTokens,
		message.CompletionTokens,
		message.TotalTokens,
		message.RequestModel,
		message.MaxTokens,
		message.Temperature,
		hasToolCalls,
		message.FinishReason,
		isNonsense,
		createdAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store message: %w", err)
	}

	return nil
}

// GetMessagesBySession returns all messages for a session
func (s *SQLiteStore) GetMessagesBySession(sessionID string) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT message_id, user_id, session_id, role, content, model,
			prompt_tokens, completion_tokens, total_tokens,
			request_model, max_tokens, temperature, has_tool_calls, finish_reason, is_nonsense, created_at
		FROM messages WHERE session_id = ? ORDER BY created_at ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []*model.Message
	for rows.Next() {
		msg := &model.Message{}
		var createdAt int64
		var hasToolCallsInt int
		var isNonsenseInt int

		err := rows.Scan(
			&msg.MessageID,
			&msg.UserID,
			&msg.SessionID,
			&msg.Role,
			&msg.Content,
			&msg.Model,
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

// GetMessagesByUser returns all messages for a user
func (s *SQLiteStore) GetMessagesByUser(userID string) ([]*model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(
		`SELECT message_id, user_id, session_id, role, content, model,
			prompt_tokens, completion_tokens, total_tokens,
			request_model, max_tokens, temperature, has_tool_calls, finish_reason, is_nonsense, created_at
		FROM messages WHERE user_id = ? ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []*model.Message
	for rows.Next() {
		msg := &model.Message{}
		var createdAt int64
		var hasToolCallsInt int
		var isNonsenseInt int

		err := rows.Scan(
			&msg.MessageID,
			&msg.UserID,
			&msg.SessionID,
			&msg.Role,
			&msg.Content,
			&msg.Model,
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

// AddOpenedFile records that a file was opened in a session
func (s *SQLiteStore) AddOpenedFile(openedFile *model.OpenedFile) error {
	if openedFile == nil {
		return fmt.Errorf("openedFile cannot be nil")
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
		`SELECT message_id, user_id, session_id, role, content, model,
			prompt_tokens, completion_tokens, total_tokens,
			request_model, max_tokens, temperature, has_tool_calls, finish_reason, is_nonsense, created_at
		FROM messages ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query messages: %w", err)
	}
	defer rows.Close()

	var messages []*model.Message
	for rows.Next() {
		msg := &model.Message{}
		var createdAt int64
		var hasToolCallsInt int
		var isNonsenseInt int

		err := rows.Scan(
			&msg.MessageID,
			&msg.UserID,
			&msg.SessionID,
			&msg.Role,
			&msg.Content,
			&msg.Model,
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

// GetSession is an alias for Get to match DebugStore interface
func (s *SQLiteStore) GetSession(sessionID string) (*model.Session, error) {
	return s.Get(sessionID)
}

// PutToolCall stores a tool call in the database
func (s *SQLiteStore) PutToolCall(toolCall *model.ToolCall) error {
	if toolCall == nil {
		return fmt.Errorf("toolCall cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	createdAt := toolCall.CreatedAt.Unix()
	updatedAt := toolCall.UpdatedAt.Unix()

	// Use INSERT OR REPLACE for upsert behavior
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO tool_calls (
			tool_call_id, message_id, session_id, user_id, function_name, arguments, response, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		toolCall.ToolCallID,
		toolCall.MessageID,
		toolCall.SessionID,
		toolCall.UserID,
		toolCall.FunctionName,
		toolCall.Arguments,
		toolCall.Response,
		createdAt,
		updatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to store tool call: %w", err)
	}

	return nil
}

// UpdateToolCallResponse updates the response for a tool call
func (s *SQLiteStore) UpdateToolCallResponse(toolCallID string, response string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	updatedAt := time.Now().Unix()

	_, err := s.db.Exec(
		`UPDATE tool_calls 
		 SET response = ?, updated_at = ? 
		 WHERE tool_call_id = ?`,
		response,
		updatedAt,
		toolCallID,
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
		`SELECT tool_call_id, message_id, session_id, user_id, function_name, arguments, response, created_at, updated_at
		FROM tool_calls WHERE session_id = ? ORDER BY created_at ASC`,
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

		err := rows.Scan(
			&tc.ToolCallID,
			&tc.MessageID,
			&tc.SessionID,
			&tc.UserID,
			&tc.FunctionName,
			&tc.Arguments,
			&tc.Response,
			&createdAt,
			&updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan tool call: %w", err)
		}

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
		`SELECT tool_call_id, message_id, session_id, user_id, function_name, arguments, response, created_at, updated_at
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

		err := rows.Scan(
			&tc.ToolCallID,
			&tc.MessageID,
			&tc.SessionID,
			&tc.UserID,
			&tc.FunctionName,
			&tc.Arguments,
			&tc.Response,
			&createdAt,
			&updatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan tool call: %w", err)
		}

		tc.CreatedAt = time.Unix(createdAt, 0)
		tc.UpdatedAt = time.Unix(updatedAt, 0)
		toolCalls = append(toolCalls, tc)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tool calls: %w", err)
	}

	return toolCalls, nil
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

// Ensure SQLiteStore implements model.SessionStore
var _ model.SessionStore = (*SQLiteStore)(nil)

// Ensure SQLiteStore implements debuger.DebugStore
// This is verified at compile time in agentize.go where debuger package is imported
