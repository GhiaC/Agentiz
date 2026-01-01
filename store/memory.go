package store

import (
	"fmt"
	"sync"
	"time"

	"agentize/model"
)

// MemoryStore is an in-memory implementation of SessionStore
type MemoryStore struct {
	sessions map[string]*model.Session
	mu       sync.RWMutex
}

// NewMemoryStore creates a new in-memory session store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]*model.Session),
	}
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

// SessionStore defines the interface for session storage
type SessionStore interface {
	Get(sessionID string) (*model.Session, error)
	Put(session *model.Session) error
	Delete(sessionID string) error
	List(userID string) ([]*model.Session, error)
}
