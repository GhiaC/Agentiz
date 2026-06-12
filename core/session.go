package core

import (
	"fmt"
	"strings"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
)

// getOrCreateCoreSession gets or creates a Core session for a user.
//
// The whole check-and-create runs under the write lock: with a read-lock fast
// path two goroutines could both miss, both create a session, and the second
// would silently overwrite the first (TOCTOU). Creation is rare and per-user
// messages are already serialized by getUserMutex, so the simpler single-lock
// form costs nothing on the hot path.
func (ch *CoreHandler) getOrCreateCoreSession(userID string) (*model.Session, error) {
	ch.coreSessionsMu.Lock()
	defer ch.coreSessionsMu.Unlock()

	if session, exists := ch.coreSessions[userID]; exists {
		dbSession, err := ch.sessionHandler.GetSession(session.SessionID)
		if err == nil && dbSession != nil {
			ch.coreSessions[userID] = dbSession
			return dbSession, nil
		}
	}

	activeSessionID := ch.getActiveSessionID(userID, model.AgentTypeCore)
	if activeSessionID != "" {
		activeSession, err := ch.sessionHandler.GetSession(activeSessionID)
		if err == nil && activeSession != nil {
			ch.coreSessions[userID] = activeSession
			return activeSession, nil
		}
		log.Log.Warnf("[CoreHandler] ⚠️  Active Core session no longer exists | UserID: %s | OldSessionID: %s",
			userID, activeSessionID)
	}

	// Fallback: Try to get existing Core session from database
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		GetCoreSession(string) (*model.Session, error)
	}); ok {
		existingCore, err := sqliteStore.GetCoreSession(userID)
		if err == nil && existingCore != nil {
			ch.coreSessions[userID] = existingCore
			_ = ch.setActiveSessionID(userID, model.AgentTypeCore, existingCore.SessionID)
			return existingCore, nil
		}
	} else {
		allSessions, err := ch.sessionHandler.ListUserSessions(userID)
		if err == nil {
			for _, s := range allSessions {
				if s.AgentType == model.AgentTypeCore {
					ch.coreSessions[userID] = s
					_ = ch.setActiveSessionID(userID, model.AgentTypeCore, s.SessionID)
					return s, nil
				}
			}
		}
	}

	session, err := ch.createSessionForUser(userID, model.AgentTypeCore)
	if err != nil {
		return nil, fmt.Errorf("failed to create core session: %w", err)
	}

	ch.coreSessions[userID] = session
	log.Log.Infof("[CoreHandler] ✨ Created new Core session | UserID: %s | SessionID: %s", userID, session.SessionID)

	return session, nil
}

func (ch *CoreHandler) saveCoreSession(session *model.Session) error {
	ch.coreSessionsMu.Lock()
	ch.coreSessions[session.UserID] = session
	ch.coreSessionsMu.Unlock()

	store := ch.sessionHandler.GetStore()
	if err := store.Put(session); err != nil {
		return fmt.Errorf("failed to save core session: %w", err)
	}
	return nil
}

func (ch *CoreHandler) getSessionFunc() agentmanager.SessionGetter {
	return func(sessionID string) (*model.Session, error) {
		return ch.sessionHandler.GetSession(sessionID)
	}
}

func (ch *CoreHandler) getActiveSessionIDFunc() agentmanager.ActiveSessionIDGetter {
	return func(userID string, agentType model.AgentType) string {
		return ch.getActiveSessionID(userID, agentType)
	}
}

func (ch *CoreHandler) buildCoreSessionContext(session *model.Session) string {
	if session.Summary == "" && len(session.Tags) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# Core Session Context\n\n")
	sb.WriteString("This is a continuation of a previous conversation. Here is the context from earlier messages:\n\n")

	if session.Summary != "" {
		sb.WriteString("## Summary of Previous Conversation\n")
		sb.WriteString(session.Summary)
		sb.WriteString("\n\n")
	}

	if len(session.Tags) > 0 {
		sb.WriteString("## Topics Discussed\n")
		sb.WriteString(strings.Join(session.Tags, ", "))
		sb.WriteString("\n")
	}

	return sb.String()
}

func (ch *CoreHandler) getOrCreateUser(userID string) (*model.User, error) {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		GetOrCreateUser(string) (*model.User, error)
	}); ok {
		return sqliteStore.GetOrCreateUser(userID)
	}
	return nil, nil
}

func (ch *CoreHandler) saveUser(user *model.User) error {
	store := ch.sessionHandler.GetStore()
	if sqliteStore, ok := store.(interface {
		PutUser(*model.User) error
	}); ok {
		return sqliteStore.PutUser(user)
	}
	return fmt.Errorf("store does not support user management")
}

func (ch *CoreHandler) createSessionForUser(userID string, agentType model.AgentType) (*model.Session, error) {
	user, err := ch.getOrCreateUser(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	if user == nil {
		return ch.sessionHandler.CreateSession(userID, agentType)
	}

	session, err := ch.sessionHandler.CreateSessionForUser(user, agentType)
	if err != nil {
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	// CreateSessionForUser mutated the user (session sequence, active-session
	// bookkeeping); failing to persist that would leave the new session orphaned
	// from the user record, so surface the error instead of swallowing it.
	if err := ch.saveUser(user); err != nil {
		log.Log.Errorf("[CoreHandler] ❌ Failed to save user after session creation | UserID: %s | Error: %v", userID, err)
		return nil, fmt.Errorf("failed to save user after session creation: %w", err)
	}

	return session, nil
}

func (ch *CoreHandler) getActiveSessionID(userID string, agentType model.AgentType) string {
	user, err := ch.getOrCreateUser(userID)
	if err != nil || user == nil {
		return ""
	}
	return user.GetActiveSessionID(agentType)
}

func (ch *CoreHandler) setActiveSessionID(userID string, agentType model.AgentType, sessionID string) error {
	if sessionID != "" {
		session, err := ch.sessionHandler.GetSession(sessionID)
		if err != nil || session == nil {
			return fmt.Errorf("session not found in database: %s", sessionID)
		}
	}

	user, err := ch.getOrCreateUser(userID)
	if err != nil {
		return fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return fmt.Errorf("user not found and could not be created")
	}

	user.SetActiveSessionID(agentType, sessionID)
	if err := ch.saveUser(user); err != nil {
		return fmt.Errorf("failed to save user: %w", err)
	}

	log.Log.Infof("[CoreHandler] 📌 Active session set | UserID: %s | AgentType: %s | SessionID: %s",
		userID, agentType, sessionID)
	return nil
}

func (ch *CoreHandler) getOrCreateActiveSession(userID string, agentType model.AgentType) (string, error) {
	sessionID := ch.getActiveSessionID(userID, agentType)
	if sessionID != "" {
		session, err := ch.sessionHandler.GetSession(sessionID)
		if err == nil && session != nil {
			return sessionID, nil
		}
		log.Log.Warnf("[CoreHandler] ⚠️  Active session no longer exists, creating new | UserID: %s | AgentType: %s",
			userID, agentType)
	}

	session, err := ch.createSessionForUser(userID, agentType)
	if err != nil {
		return "", fmt.Errorf("failed to create session: %w", err)
	}

	log.Log.Infof("[CoreHandler] ✨ Auto-created active session | UserID: %s | AgentType: %s | SessionID: %s",
		userID, agentType, session.SessionID)
	return session.SessionID, nil
}
