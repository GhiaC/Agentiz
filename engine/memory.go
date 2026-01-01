package engine

import (
	"github.com/sashabaranov/go-openai"
)

// ConversationMemory manages conversation history for a session
type ConversationMemory struct {
	messages []openai.ChatCompletionMessage
}

// NewConversationMemory creates a new conversation memory
func NewConversationMemory() *ConversationMemory {
	return &ConversationMemory{
		messages: make([]openai.ChatCompletionMessage, 0),
	}
}

// AddUserMessage adds a user message to the conversation
func (m *ConversationMemory) AddUserMessage(content string) {
	m.messages = append(m.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: content,
	})
}

// AddAssistantMessage adds an assistant message to the conversation
func (m *ConversationMemory) AddAssistantMessage(content string) {
	m.messages = append(m.messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: content,
	})
}

// AddToolResult adds a tool result to the conversation
func (m *ConversationMemory) AddToolResult(toolCallID, functionName, result string) {
	m.messages = append(m.messages, openai.ChatCompletionMessage{
		Role:       openai.ChatMessageRoleTool,
		Content:    result,
		Name:       functionName,
		ToolCallID: toolCallID,
	})
}

// AddAssistantWithToolCalls adds an assistant message with tool calls
func (m *ConversationMemory) AddAssistantWithToolCalls(toolCalls []openai.ToolCall) {
	m.messages = append(m.messages, openai.ChatCompletionMessage{
		Role:      openai.ChatMessageRoleAssistant,
		ToolCalls: toolCalls,
	})
}

// GetMessages returns all messages
func (m *ConversationMemory) GetMessages() []openai.ChatCompletionMessage {
	return m.messages
}

// Clear clears all messages
func (m *ConversationMemory) Clear() {
	m.messages = make([]openai.ChatCompletionMessage, 0)
}

// GetLastMessages returns the last N messages
func (m *ConversationMemory) GetLastMessages(n int) []openai.ChatCompletionMessage {
	if n >= len(m.messages) {
		return m.messages
	}
	return m.messages[len(m.messages)-n:]
}

