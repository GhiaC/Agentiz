package store

import (
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/model"
)

// Cache sizes for the DBStore read caches. Bounded LRUs so a long-running
// server's cache cannot grow without limit.
const (
	sessionsCacheSize = 10000
	usersCacheSize    = 10000
)

// DBStore is a simple wrapper around SQLiteStore with read cache
// It uses bounded in-memory LRU caches for frequently accessed data
// (sessions, users) while persisting to SQLite
type DBStore struct {
	// SQLite backend - all data is persisted in database
	sqliteStore *SQLiteStore

	// Bounded read caches (thread-safe LRUs).
	sessionsCache *lru.Cache[string, *model.Session]
	usersCache    *lru.Cache[string, *model.User]

	// UserNodes tracks visited nodes for each user (user-level, not session-level)
	// This stays in-memory for performance as it's frequently accessed
	userNodes sync.Map
	// userLock maps userID → *sync.Mutex; created with LoadOrStore, never deleted.
	userLock sync.Map
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

	sessionsCache, err := lru.New[string, *model.Session](sessionsCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create sessions cache: %w", err)
	}
	usersCache, err := lru.New[string, *model.User](usersCacheSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create users cache: %w", err)
	}

	return &DBStore{
		sqliteStore:   sqliteStore,
		sessionsCache: sessionsCache,
		usersCache:    usersCache,
	}, nil
}

// Close closes the database connection
func (s *DBStore) Close() error {
	if s.sqliteStore != nil {
		return s.sqliteStore.Close()
	}
	return nil
}

// Path returns the underlying SQLite database path. Used by the debug dashboard
// to report where session/file metadata is persisted.
func (s *DBStore) Path() string {
	if s.sqliteStore != nil {
		return s.sqliteStore.Path()
	}
	return ""
}

// BackendInfo describes this backend for diagnostics and the debug dashboard.
// DBStore is a read-cached SQLite store, so it reports its SQLite backend.
func (s *DBStore) BackendInfo() debuger.BackendInfo {
	if s.sqliteStore != nil {
		return s.sqliteStore.BackendInfo()
	}
	return debuger.BackendInfo{Type: "SQLite"}
}

// getOrCreateLock returns the per-user mutex, creating it atomically on first
// use. Entries are never deleted, so the returned lock is always valid.
func (s *DBStore) getOrCreateLock(userID string) *sync.Mutex {
	v, _ := s.userLock.LoadOrStore(userID, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// Get retrieves a session by ID
// First checks cache, then falls back to database
func (s *DBStore) Get(sessionID string) (*model.Session, error) {
	// Check cache first
	if session, ok := s.sessionsCache.Get(sessionID); ok {
		// Return a copy to prevent external modification (using Clone to avoid copylocks)
		return session.Clone(), nil
	}

	// Not in cache, get from database
	session, err := s.sqliteStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// Add to cache
	s.sessionsCache.Add(sessionID, session.Clone())

	return session, nil
}

// Put stores or updates a session
// Updates both cache and database (write-through)
func (s *DBStore) Put(session *model.Session) error {
	// Update database first
	if err := s.sqliteStore.Put(session); err != nil {
		return err
	}

	// Update cache (using Clone to avoid copylocks)
	s.sessionsCache.Add(session.SessionID, session.Clone())

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
	s.sessionsCache.Remove(sessionID)

	return nil
}

// GetCoreSession returns the user's core session, or (nil, nil) if none.
// Delegates to SQLiteStore and warms the session cache on hit.
func (s *DBStore) GetCoreSession(userID string) (*model.Session, error) {
	session, err := s.sqliteStore.GetCoreSession(userID)
	if err != nil || session == nil {
		return session, err
	}
	s.sessionsCache.Add(session.SessionID, session.Clone())
	return session, nil
}

// PutCoreSession upserts the user's single core session (write-through).
func (s *DBStore) PutCoreSession(session *model.Session) error {
	if err := s.sqliteStore.PutCoreSession(session); err != nil {
		return err
	}
	s.sessionsCache.Add(session.SessionID, session.Clone())
	return nil
}

// List returns all sessions for a user (delegates to SQLiteStore)
func (s *DBStore) List(userID string) ([]*model.Session, error) {
	return s.sqliteStore.List(userID)
}

// GetNextSessionSeq returns the next session sequence number for a user and agent type
// Delegates to SQLiteStore
func (s *DBStore) GetNextSessionSeq(userID string, agentType model.AgentType) (int, error) {
	return s.sqliteStore.GetNextSessionSeq(userID, agentType)
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
	if user, ok := s.usersCache.Get(userID); ok {
		// Return a copy to prevent external modification
		userCopy := *user
		return &userCopy, nil
	}

	// Not in cache, get from database
	user, err := s.sqliteStore.GetUser(userID)
	if err != nil {
		return nil, err
	}

	// Add to cache if found
	if user != nil {
		userCopy := *user
		s.usersCache.Add(userID, &userCopy)
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
	userCopy := *user
	s.usersCache.Add(user.UserID, &userCopy)

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

// PutMessages stores a batch of messages in one transaction (delegates to SQLiteStore)
func (s *DBStore) PutMessages(messages []*model.Message) error {
	return s.sqliteStore.PutMessages(messages)
}

// GetMessagesBySession returns all messages for a session (delegates to SQLiteStore)
func (s *DBStore) GetMessagesBySession(sessionID string) ([]*model.Message, error) {
	return s.sqliteStore.GetMessagesBySession(sessionID)
}

// GetMessagesBySessionPage returns one page of a session's messages (delegates to SQLiteStore)
func (s *DBStore) GetMessagesBySessionPage(sessionID string, limit, offset int) ([]*model.Message, error) {
	return s.sqliteStore.GetMessagesBySessionPage(sessionID, limit, offset)
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

// GetAllUsers returns all users (delegates to SQLiteStore)
func (s *DBStore) GetAllUsers() ([]*model.User, error) {
	return s.sqliteStore.GetAllUsers()
}

// GetAllMessages returns all messages (delegates to SQLiteStore)
func (s *DBStore) GetAllMessages() ([]*model.Message, error) {
	return s.sqliteStore.GetAllMessages()
}

// GetAllOpenedFiles returns all opened files (delegates to SQLiteStore)
func (s *DBStore) GetAllOpenedFiles() ([]*model.OpenedFile, error) {
	return s.sqliteStore.GetAllOpenedFiles()
}

// GetOpenedFilesByUser returns opened files for a user (delegates to SQLiteStore)
func (s *DBStore) GetOpenedFilesByUser(userID string) ([]*model.OpenedFile, error) {
	return s.sqliteStore.GetOpenedFilesByUser(userID)
}

// PutUserFile stores a user file record (delegates to SQLiteStore)
func (s *DBStore) PutUserFile(f *model.UserFile) error {
	return s.sqliteStore.PutUserFile(f)
}

// GetUserFile returns a user file by ID (delegates to SQLiteStore)
func (s *DBStore) GetUserFile(fileID string) (*model.UserFile, error) {
	return s.sqliteStore.GetUserFile(fileID)
}

// GetUserFilesByUser returns all files for a user (delegates to SQLiteStore)
func (s *DBStore) GetUserFilesByUser(userID string) ([]*model.UserFile, error) {
	return s.sqliteStore.GetUserFilesByUser(userID)
}

// GetUserFilesBySession returns all files for a session (delegates to SQLiteStore)
func (s *DBStore) GetUserFilesBySession(sessionID string) ([]*model.UserFile, error) {
	return s.sqliteStore.GetUserFilesBySession(sessionID)
}

// GetAllUserFiles returns all user files (delegates to SQLiteStore)
func (s *DBStore) GetAllUserFiles() ([]*model.UserFile, error) {
	return s.sqliteStore.GetAllUserFiles()
}

// DeleteUserFile removes a user file record (delegates to SQLiteStore)
func (s *DBStore) DeleteUserFile(fileID string) error {
	return s.sqliteStore.DeleteUserFile(fileID)
}

// GetSession is an alias for Get to match DebugStore interface
func (s *DBStore) GetSession(sessionID string) (*model.Session, error) {
	return s.Get(sessionID)
}

// GetAllToolCalls returns all tool calls (delegates to SQLiteStore)
func (s *DBStore) GetAllToolCalls() ([]*model.ToolCall, error) {
	return s.sqliteStore.GetAllToolCalls()
}

// GetToolCallsBySession returns all tool calls for a session (delegates to SQLiteStore)
func (s *DBStore) GetToolCallsBySession(sessionID string) ([]*model.ToolCall, error) {
	return s.sqliteStore.GetToolCallsBySession(sessionID)
}

// GetToolCallByID returns a tool call by its ID (delegates to SQLiteStore)
func (s *DBStore) GetToolCallByID(toolCallID string) (*model.ToolCall, error) {
	return s.sqliteStore.GetToolCallByID(toolCallID)
}

// GetToolCallByToolID returns a tool call by its ToolID (sequential ID) (delegates to SQLiteStore)
func (s *DBStore) GetToolCallByToolID(toolID string) (*model.ToolCall, error) {
	return s.sqliteStore.GetToolCallByToolID(toolID)
}

// PutSummarizationLog stores a summarization log (delegates to SQLiteStore)
func (s *DBStore) PutSummarizationLog(log *model.SummarizationLog) error {
	return s.sqliteStore.PutSummarizationLog(log)
}

// GetSummarizationLogsBySession returns summarization logs for a session (delegates to SQLiteStore)
func (s *DBStore) GetSummarizationLogsBySession(sessionID string) ([]*model.SummarizationLog, error) {
	return s.sqliteStore.GetSummarizationLogsBySession(sessionID)
}

// GetAllSummarizationLogs returns all summarization logs (delegates to SQLiteStore)
func (s *DBStore) GetAllSummarizationLogs() ([]*model.SummarizationLog, error) {
	return s.sqliteStore.GetAllSummarizationLogs()
}

// PutRouteTrace stores a Core routing-decision DAG (delegates to SQLiteStore)
func (s *DBStore) PutRouteTrace(trace *model.RouteTrace) error {
	return s.sqliteStore.PutRouteTrace(trace)
}

// GetRouteTraceByID returns a route trace by ID (delegates to SQLiteStore)
func (s *DBStore) GetRouteTraceByID(traceID string) (*model.RouteTrace, error) {
	return s.sqliteStore.GetRouteTraceByID(traceID)
}

// GetRouteTracesBySession returns route traces for a session (delegates to SQLiteStore)
func (s *DBStore) GetRouteTracesBySession(sessionID string) ([]*model.RouteTrace, error) {
	return s.sqliteStore.GetRouteTracesBySession(sessionID)
}

// GetRouteTracesByUser returns route traces for a user (delegates to SQLiteStore)
func (s *DBStore) GetRouteTracesByUser(userID string) ([]*model.RouteTrace, error) {
	return s.sqliteStore.GetRouteTracesByUser(userID)
}

// GetAllRouteTraces returns all route traces (delegates to SQLiteStore)
func (s *DBStore) GetAllRouteTraces() ([]*model.RouteTrace, error) {
	return s.sqliteStore.GetAllRouteTraces()
}

// PutToolCall stores a tool call (delegates to SQLiteStore)
func (s *DBStore) PutToolCall(toolCall *model.ToolCall) error {
	return s.sqliteStore.PutToolCall(toolCall)
}

// UpdateToolCallResponse updates the response for a tool call (delegates to SQLiteStore)
func (s *DBStore) UpdateToolCallResponse(toolID string, response string, execErr error) error {
	return s.sqliteStore.UpdateToolCallResponse(toolID, response, execErr)
}

// DeleteUserData deletes all sessions, messages, tool calls, summarization logs,
// and opened files for a user (delegates to SQLiteStore and clears caches)
func (s *DBStore) DeleteUserData(userID string) error {
	// Get sessions before delete to clear cache
	sessions, _ := s.sqliteStore.List(userID)

	if err := s.sqliteStore.DeleteUserData(userID); err != nil {
		return err
	}

	// Clear session cache for deleted sessions
	for _, sess := range sessions {
		s.sessionsCache.Remove(sess.SessionID)
	}

	// Clear user cache
	s.usersCache.Remove(userID)

	return nil
}

// SessionStore is an alias for model.SessionStore for backward compatibility
type SessionStore = model.SessionStore

// Ensure DBStore implements model.SessionStore and debuger.DebugStore
var (
	_ model.SessionStore = (*DBStore)(nil)
	_ debuger.DebugStore = (*DBStore)(nil)
)
