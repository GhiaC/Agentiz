package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/ghiac/agentize/model"
)

// DBStore is a simple wrapper around SQLiteStore with read cache
// It uses in-memory cache for frequently accessed data (sessions, users) while persisting to SQLite
type DBStore struct {
	// SQLite backend - all data is persisted in database
	sqliteStore *SQLiteStore

	// Read cache for sessions (simple LRU-like behavior)
	sessionsCache map[string]*model.Session
	sessionsMu    sync.RWMutex

	// Read cache for users
	usersCache map[string]*model.User
	usersMu    sync.RWMutex

	// UserNodes tracks visited nodes for each user (user-level, not session-level)
	// This stays in-memory for performance as it's frequently accessed
	userNodes sync.Map
	userLock  map[string]*sync.Mutex
	nodesMu   sync.RWMutex // Protects userLock map
}

// UserNodes represents visited nodes for a user
type UserNodes struct {
	VisitedNodes map[string]*model.NodeDigest // Map of node path -> NodeDigest
	LastActivity time.Time                    // Last time user visited any node
}

// NewDBStore creates a new DBStore with SQLite backend
// Uses default path: ./data/sessions.db
func NewDBStore() (*DBStore, error) {
	return NewDBStoreWithPath("./data/sessions.db")
}

// NewDBStoreWithPath creates a new DBStore with custom database path
func NewDBStoreWithPath(dbPath string) (*DBStore, error) {
	sqliteStore, err := NewSQLiteStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize SQLite store: %w", err)
	}

	return &DBStore{
		sqliteStore:   sqliteStore,
		sessionsCache: make(map[string]*model.Session),
		usersCache:    make(map[string]*model.User),
		userLock:      make(map[string]*sync.Mutex),
	}, nil
}

// Close closes the database connection
func (s *DBStore) Close() error {
	if s.sqliteStore != nil {
		return s.sqliteStore.Close()
	}
	return nil
}

// getOrCreateLock gets or creates a mutex for a userID
func (s *DBStore) getOrCreateLock(userID string) *sync.Mutex {
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
// First checks cache, then falls back to database
func (s *DBStore) Get(sessionID string) (*model.Session, error) {
	// Check cache first
	s.sessionsMu.RLock()
	if session, ok := s.sessionsCache[sessionID]; ok {
		s.sessionsMu.RUnlock()
		// Return a copy to prevent external modification
		sessionCopy := *session
		return &sessionCopy, nil
	}
	s.sessionsMu.RUnlock()

	// Not in cache, get from database
	session, err := s.sqliteStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// Add to cache
	s.sessionsMu.Lock()
	sessionCopy := *session
	s.sessionsCache[sessionID] = &sessionCopy
	s.sessionsMu.Unlock()

	return session, nil
}

// Put stores or updates a session
// Updates both cache and database (write-through)
func (s *DBStore) Put(session *model.Session) error {
	// Update database first
	if err := s.sqliteStore.Put(session); err != nil {
		return err
	}

	// Update cache
	s.sessionsMu.Lock()
	sessionCopy := *session
	s.sessionsCache[session.SessionID] = &sessionCopy
	s.sessionsMu.Unlock()

	return nil
}

// Delete removes a session
// Removes from both cache and database
func (s *DBStore) Delete(sessionID string) error {
	// Delete from database
	if err := s.sqliteStore.Delete(sessionID); err != nil {
		return err
	}

	// Remove from cache
	s.sessionsMu.Lock()
	delete(s.sessionsCache, sessionID)
	s.sessionsMu.Unlock()

	return nil
}

// List returns all sessions for a user (delegates to SQLiteStore)
func (s *DBStore) List(userID string) ([]*model.Session, error) {
	return s.sqliteStore.List(userID)
}

// GetAllSessions returns all sessions grouped by userID (delegates to SQLiteStore)
func (s *DBStore) GetAllSessions() (map[string][]*model.Session, error) {
	return s.sqliteStore.GetAllSessions()
}

// AddVisitedNode adds a visited node for a user
// This tracks nodes at user level, across all sessions (in-memory only for performance)
func (s *DBStore) AddVisitedNode(userID string, nodeDigest *model.NodeDigest) {
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
func (s *DBStore) GetVisitedNodes(userID string) map[string]*model.NodeDigest {
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
func (s *DBStore) GetVisitedNodePaths(userID string) []string {
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
func (s *DBStore) HasVisitedNode(userID string, nodePath string) bool {
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
func (s *DBStore) ClearVisitedNodes(userID string) {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	s.userNodes.Delete(userID)
}

// GetUser retrieves a user by ID
// First checks cache, then falls back to database
func (s *DBStore) GetUser(userID string) (*model.User, error) {
	// Check cache first
	s.usersMu.RLock()
	if user, ok := s.usersCache[userID]; ok {
		s.usersMu.RUnlock()
		// Return a copy to prevent external modification
		userCopy := *user
		return &userCopy, nil
	}
	s.usersMu.RUnlock()

	// Not in cache, get from database
	user, err := s.sqliteStore.GetUser(userID)
	if err != nil {
		return nil, err
	}

	// Add to cache if found
	if user != nil {
		s.usersMu.Lock()
		userCopy := *user
		s.usersCache[userID] = &userCopy
		s.usersMu.Unlock()
	}

	return user, nil
}

// PutUser stores or updates a user
// Updates both cache and database (write-through)
func (s *DBStore) PutUser(user *model.User) error {
	// Update database first
	if err := s.sqliteStore.PutUser(user); err != nil {
		return err
	}

	// Update cache
	s.usersMu.Lock()
	userCopy := *user
	s.usersCache[user.UserID] = &userCopy
	s.usersMu.Unlock()

	return nil
}

// GetOrCreateUser gets an existing user or creates a new one (delegates to SQLiteStore)
func (s *DBStore) GetOrCreateUser(userID string) (*model.User, error) {
	return s.sqliteStore.GetOrCreateUser(userID)
}

// PutMessage stores a message (delegates to SQLiteStore)
func (s *DBStore) PutMessage(message *model.Message) error {
	return s.sqliteStore.PutMessage(message)
}

// GetMessagesBySession returns all messages for a session (delegates to SQLiteStore)
func (s *DBStore) GetMessagesBySession(sessionID string) ([]*model.Message, error) {
	return s.sqliteStore.GetMessagesBySession(sessionID)
}

// GetMessagesByUser returns all messages for a user (delegates to SQLiteStore)
func (s *DBStore) GetMessagesByUser(userID string) ([]*model.Message, error) {
	return s.sqliteStore.GetMessagesByUser(userID)
}

// AddOpenedFile records that a file was opened in a session (delegates to SQLiteStore)
func (s *DBStore) AddOpenedFile(openedFile *model.OpenedFile) error {
	return s.sqliteStore.AddOpenedFile(openedFile)
}

// CloseOpenedFile marks a file as closed (delegates to SQLiteStore)
func (s *DBStore) CloseOpenedFile(sessionID string, filePath string) error {
	return s.sqliteStore.CloseOpenedFile(sessionID, filePath)
}

// GetOpenedFilesBySession returns all opened files for a session (delegates to SQLiteStore)
func (s *DBStore) GetOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	return s.sqliteStore.GetOpenedFilesBySession(sessionID)
}

// GetCurrentlyOpenedFilesBySession returns only currently open files for a session (delegates to SQLiteStore)
func (s *DBStore) GetCurrentlyOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	return s.sqliteStore.GetCurrentlyOpenedFilesBySession(sessionID)
}

// SessionStore is an alias for model.SessionStore for backward compatibility
type SessionStore = model.SessionStore

// Ensure DBStore implements model.SessionStore
var _ model.SessionStore = (*DBStore)(nil)
