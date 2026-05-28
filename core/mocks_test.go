package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// MockSessionStore implements model.SessionStore, engine.ToolCallStore, and the
// optional methods used by CoreHandler via type assertion (PutMessage, GetOrCreateUser,
// PutUser, GetCoreSession). Used for CoreHandler tests.
type MockSessionStore struct {
	mu sync.RWMutex

	sessions    map[string]*model.Session
	users       map[string]*model.User
	messages    []*model.Message
	toolCalls   map[string]*model.ToolCall
	sessionSeqs map[string]int // key: userID|agentType
}

// NewMockSessionStore returns a new MockSessionStore ready for tests.
func NewMockSessionStore() *MockSessionStore {
	return &MockSessionStore{
		sessions:    make(map[string]*model.Session),
		users:       make(map[string]*model.User),
		messages:    nil,
		toolCalls:   make(map[string]*model.ToolCall),
		sessionSeqs: make(map[string]int),
	}
}

// SessionStore
func (m *MockSessionStore) Get(sessionID string) (*model.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (m *MockSessionStore) Put(session *model.Session) error {
	if session == nil {
		return errors.New("session cannot be nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.SessionID] = session
	return nil
}

func (m *MockSessionStore) Delete(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, sessionID)
	return nil
}

func (m *MockSessionStore) List(userID string) ([]*model.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*model.Session
	for _, s := range m.sessions {
		if s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *MockSessionStore) GetNextSessionSeq(userID string, agentType model.AgentType) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := userID + "|" + string(agentType)
	n := m.sessionSeqs[key]
	m.sessionSeqs[key] = n + 1
	return n + 1, nil
}

// GetCoreSession (used by CoreHandler via type assertion)
func (m *MockSessionStore) GetCoreSession(userID string) (*model.Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s.UserID == userID && s.AgentType == model.AgentTypeCore {
			return s, nil
		}
	}
	return nil, nil
}

// PutMessage (used by CoreHandler via type assertion)
func (m *MockSessionStore) PutMessage(msg *model.Message) error {
	if msg == nil {
		return errors.New("message cannot be nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, msg)
	return nil
}

// GetOrCreateUser (used by CoreHandler via type assertion)
func (m *MockSessionStore) GetOrCreateUser(userID string) (*model.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[userID]; ok {
		return u, nil
	}
	u := model.NewUser(userID)
	m.users[userID] = u
	return u, nil
}

// PutUser (used by CoreHandler via type assertion)
func (m *MockSessionStore) PutUser(user *model.User) error {
	if user == nil {
		return errors.New("user cannot be nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[user.UserID] = user
	return nil
}

// ToolCallStore
func (m *MockSessionStore) PutToolCall(tc *model.ToolCall) error {
	if tc == nil {
		return errors.New("tool call cannot be nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolCalls[tc.ToolID] = tc
	return nil
}

func (m *MockSessionStore) UpdateToolCallResponse(toolID, response string, execErr error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	tc, ok := m.toolCalls[toolID]
	if !ok {
		return nil
	}
	tc.Response = response
	tc.UpdatedAt = time.Now()
	if execErr != nil {
		tc.Status = "error"
		tc.Error = execErr.Error()
	} else {
		tc.Status = "completed"
	}
	return nil
}

// Test helpers
func (m *MockSessionStore) Sessions() map[string]*model.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]*model.Session)
	for k, v := range m.sessions {
		out[k] = v
	}
	return out
}

func (m *MockSessionStore) Users() map[string]*model.User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]*model.User)
	for k, v := range m.users {
		out[k] = v
	}
	return out
}

func (m *MockSessionStore) MessageCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.messages)
}

func (m *MockSessionStore) ToolCallCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.toolCalls)
}

// MockLLMTransport is an http.RoundTripper that returns predefined chat completion responses.
// Use it with openai.DefaultConfig(apiKey) and config.HTTPClient = &http.Client{Transport: mock}.
type MockLLMTransport struct {
	mu        sync.Mutex
	Responses []openai.ChatCompletionResponse
	Index     int
}

func (t *MockLLMTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	var resp openai.ChatCompletionResponse
	if t.Index < len(t.Responses) {
		resp = t.Responses[t.Index]
		t.Index++
	} else {
		resp = openai.ChatCompletionResponse{
			Choices: []openai.ChatCompletionChoice{{
				Message: openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: ""},
			}},
		}
	}
	t.mu.Unlock()

	body, _ := json.Marshal(resp)
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

// AddResponse appends a response to the mock. Safe for concurrent use.
func (t *MockLLMTransport) AddResponse(r openai.ChatCompletionResponse) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Responses = append(t.Responses, r)
}

// Reset sets the response index to 0 so the same responses are replayed.
func (t *MockLLMTransport) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Index = 0
}
