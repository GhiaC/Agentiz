package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/ghiac/agentize/model"
)

// MemoryStore is an in-memory implementation of SessionStore
// It also manages visited nodes for each user (similar to Memory in engine/memory.go)
type MemoryStore struct {
	sessions map[string]*model.Session
	mu       sync.RWMutex

	// UserNodes tracks visited nodes for each user (user-level, not session-level)
	userNodes sync.Map
	userLock  map[string]*sync.Mutex
	nodesMu   sync.RWMutex // Protects userLock map
}

// UserNodes represents visited nodes for a user
type UserNodes struct {
	VisitedNodes map[string]*model.NodeDigest // Map of node path -> NodeDigest
	LastActivity time.Time
}

// NewMemoryStore creates a new in-memory session store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]*model.Session),
		userLock: make(map[string]*sync.Mutex),
	}
}

// getOrCreateLock gets or creates a mutex for a userID
func (s *MemoryStore) getOrCreateLock(userID string) *sync.Mutex {
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
func (s *MemoryStore) Get(sessionID string) (*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	session, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}

	return session, nil
}

// Put stores or updates a session
func (s *MemoryStore) Put(session *model.Session) error {
	if session == nil {
		return fmt.Errorf("session cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	session.UpdatedAt = time.Now()
	s.sessions[session.SessionID] = session

	return nil
}

// Delete removes a session
func (s *MemoryStore) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, sessionID)
	return nil
}

// List returns all sessions for a user
func (s *MemoryStore) List(userID string) ([]*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var sessions []*model.Session
	for _, session := range s.sessions {
		if session.UserID == userID {
			sessions = append(sessions, session)
		}
	}

	return sessions, nil
}

// AddVisitedNode adds a visited node for a user
// This tracks nodes at user level, across all sessions
func (s *MemoryStore) AddVisitedNode(userID string, nodeDigest *model.NodeDigest) {
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
func (s *MemoryStore) GetVisitedNodes(userID string) map[string]*model.NodeDigest {
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
func (s *MemoryStore) GetVisitedNodePaths(userID string) []string {
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
func (s *MemoryStore) HasVisitedNode(userID string, nodePath string) bool {
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
func (s *MemoryStore) ClearVisitedNodes(userID string) {
	lock := s.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	s.userNodes.Delete(userID)
}

// SessionStore is an alias for model.SessionStore for backward compatibility
type SessionStore = model.SessionStore

// GetAllSessions returns all sessions grouped by userID
func (s *MemoryStore) GetAllSessions() (map[string][]*model.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessionsByUser := make(map[string][]*model.Session)
	for _, session := range s.sessions {
		sessionsByUser[session.UserID] = append(sessionsByUser[session.UserID], session)
	}

	return sessionsByUser, nil
}

// Ensure MemoryStore implements model.SessionStore
var _ model.SessionStore = (*MemoryStore)(nil)
