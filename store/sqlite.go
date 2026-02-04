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
	`

	_, err := s.db.Exec(schema)
	return err
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

// Ensure SQLiteStore implements model.SessionStore
var _ model.SessionStore = (*SQLiteStore)(nil)
