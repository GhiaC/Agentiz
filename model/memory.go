package model

import (
	"sync"
	"time"

	"github.com/sashabaranov/go-openai"
)

// ConversationState represents the memory structure for a user/session
type ConversationState struct {
	Msgs         []openai.ChatCompletionMessage
	ToolID       string
	ToolsMsg     *openai.ChatCompletionMessage
	InProgress   bool
	Queue        []openai.ChatCompletionMessage
	LastActivity time.Time
}

// NewConversationState creates a new conversation state
func NewConversationState() *ConversationState {
	return &ConversationState{
		Msgs:         []openai.ChatCompletionMessage{},
		LastActivity: time.Now(),
	}
}

// Memory manages conversation history for multiple users/sessions
type Memory struct {
	usersMemory sync.Map
	userLock    map[string]*sync.Mutex
	mu          sync.RWMutex // Protects userLock map
}

// NewMemory creates a new memory manager
func NewMemory() *Memory {
	return &Memory{
		usersMemory: sync.Map{},
		userLock:    make(map[string]*sync.Mutex),
	}
}

// getOrCreateLock gets or creates a mutex for a userID
func (m *Memory) getOrCreateLock(userID string) *sync.Mutex {
	m.mu.RLock()
	lock, exists := m.userLock[userID]
	m.mu.RUnlock()

	if exists {
		return lock
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double check after acquiring write lock
	if lock, exists := m.userLock[userID]; exists {
		return lock
	}

	lock = &sync.Mutex{}
	m.userLock[userID] = lock
	return lock
}

// Append adds messages to a user's memory
func (m *Memory) Append(userID string, msg []openai.ChatCompletionMessage) {
	lock := m.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if mem, ok := m.usersMemory.Load(userID); ok {
		userMem := mem.(*ConversationState)
		userMem.Msgs = append(userMem.Msgs, msg...)
		userMem.LastActivity = time.Now()
		m.usersMemory.Store(userID, userMem)
	} else {
		userMem := &ConversationState{
			Msgs:         msg,
			LastActivity: time.Now(),
		}
		m.usersMemory.Store(userID, userMem)
	}
}

// InWaiting checks if a user is waiting for a tool response
func (m *Memory) InWaiting(userID string) bool {
	lock := m.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if mem, ok := m.usersMemory.Load(userID); ok {
		userMem := mem.(*ConversationState)
		return userMem.ToolID != ""
	}
	return false
}

// GetMemory retrieves a user's memory
func (m *Memory) GetMemory(userID string) *ConversationState {
	lock := m.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if mem, ok := m.usersMemory.Load(userID); ok {
		return mem.(*ConversationState)
	}
	return &ConversationState{
		Msgs: make([]openai.ChatCompletionMessage, 0),
	}
}

// ClearTool clears the tool state for a user
func (m *Memory) ClearTool(userID string) {
	m.SetTool(userID, "", nil)
}

// SetTool sets the tool state for a user
func (m *Memory) SetTool(userID string, toolID string, toolMsg *openai.ChatCompletionMessage) {
	lock := m.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if mem, ok := m.usersMemory.Load(userID); ok {
		userMem := mem.(*ConversationState)
		userMem.LastActivity = time.Now()
		userMem.ToolID = toolID
		userMem.ToolsMsg = toolMsg
		m.usersMemory.Store(userID, userMem)
	}
}

// Queue adds a message to the queue for a user
func (m *Memory) Queue(userID string, text string) {
	lock := m.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if mem, ok := m.usersMemory.Load(userID); ok {
		userMem := mem.(*ConversationState)
		userMem.LastActivity = time.Now()
		userMem.Queue = append(userMem.Queue, openai.ChatCompletionMessage{
			Role:    openai.ChatMessageRoleUser,
			Content: text,
		})
		m.usersMemory.Store(userID, userMem)
	}
}

// SetProgress sets the progress state for a user
func (m *Memory) SetProgress(userID string, inProgress bool) {
	lock := m.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if mem, ok := m.usersMemory.Load(userID); ok {
		userMem := mem.(*ConversationState)
		userMem.InProgress = inProgress
		userMem.LastActivity = time.Now()
		m.usersMemory.Store(userID, userMem)
	}
}

// ResetQueue clears the queue for a user
func (m *Memory) ResetQueue(userID string) {
	lock := m.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if mem, ok := m.usersMemory.Load(userID); ok {
		userMem := mem.(*ConversationState)
		userMem.Queue = []openai.ChatCompletionMessage{}
		userMem.LastActivity = time.Now()
		m.usersMemory.Store(userID, userMem)
	}
}

// RemoveFuncs removes function/tool call messages from a user's memory
func (m *Memory) RemoveFuncs(userID string) {
	lock := m.getOrCreateLock(userID)
	lock.Lock()
	defer lock.Unlock()

	if mem, ok := m.usersMemory.Load(userID); ok {
		msgs := []openai.ChatCompletionMessage{}
		for _, msg := range mem.(*ConversationState).Msgs {
			if msg.ToolCallID != "" || len(msg.ToolCalls) > 0 ||
				msg.FunctionCall != nil {
				continue
			}
			if msg.Role == openai.ChatMessageRoleAssistant ||
				msg.Role == openai.ChatMessageRoleUser {
				msgs = append(msgs, msg)
			}
		}
		userMem := mem.(*ConversationState)
		userMem.Msgs = msgs
		userMem.LastActivity = time.Now()
		m.usersMemory.Store(userID, userMem)
	}
}

// MemoryProvider methods - these require sessionID to be passed
// GetMessagesForSession returns all messages for a specific session
func (m *Memory) GetMessagesForSession(sessionID string) []openai.ChatCompletionMessage {
	userMem := m.GetMemory(sessionID)
	messages := make([]openai.ChatCompletionMessage, len(userMem.Msgs))
	copy(messages, userMem.Msgs)
	return messages
}

// AddAssistantMessageForSession adds an assistant message for a specific session
func (m *Memory) AddAssistantMessageForSession(sessionID string, content string) {
	m.Append(sessionID, []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleAssistant,
			Content: content,
		},
	})
}

// AddAssistantWithToolCallsForSession adds an assistant message with tool calls for a specific session
func (m *Memory) AddAssistantWithToolCallsForSession(sessionID string, toolCalls []openai.ToolCall) {
	m.Append(sessionID, []openai.ChatCompletionMessage{
		{
			Role:      openai.ChatMessageRoleAssistant,
			ToolCalls: toolCalls,
		},
	})
}

// AddToolResultForSession adds a tool result for a specific session
func (m *Memory) AddToolResultForSession(sessionID string, toolCallID, functionName, result string) {
	m.Append(sessionID, []openai.ChatCompletionMessage{
		{
			Role:       openai.ChatMessageRoleTool,
			Content:    result,
			Name:       functionName,
			ToolCallID: toolCallID,
		},
	})
}
