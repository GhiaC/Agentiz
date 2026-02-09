package model

import (
	"fmt"
	"time"

	"github.com/sashabaranov/go-openai"
)

// ContentType represents the type of content in the message
type ContentType string

const (
	ContentTypeText  ContentType = "text"
	ContentTypeAudio ContentType = "audio"
	ContentTypeImage ContentType = "image"
)

// Message represents a stored message with LLM usage information
type Message struct {
	// MessageID is a unique identifier for this message
	MessageID string

	// AgentType indicates which type of agent created this message (core, low, high)
	AgentType AgentType

	// ContentType indicates the type of content (text, audio, image)
	ContentType ContentType

	// UserID identifies the user who sent/received this message
	UserID string

	// SessionID identifies the session this message belongs to
	SessionID string

	// Role is the message role (user, assistant, system, tool)
	Role string

	// Content is the message content
	Content string

	// Model is the LLM model used for this message
	Model string

	// Token usage information
	PromptTokens     int // Tokens used in the prompt
	CompletionTokens int // Tokens used in the completion
	TotalTokens      int // Total tokens used

	// Request information
	RequestModel string  // Model requested (may differ from actual model used)
	MaxTokens    int     // Max tokens requested
	Temperature  float64 // Temperature used
	HasToolCalls bool    // Whether this message had tool calls

	// Response information
	FinishReason string // Finish reason from LLM (stop, tool_calls, length, etc.)

	// Nonsense detection
	IsNonsense bool // Whether this message was detected as nonsense

	// Metadata
	CreatedAt time.Time
}

// NewMessage creates a new message from an OpenAI response
func NewMessage(
	userID string,
	sessionID string,
	role string,
	content string,
	agentType AgentType,
	contentType ContentType,
	request openai.ChatCompletionRequest,
	response openai.ChatCompletionResponse,
	choice openai.ChatCompletionChoice,
) *Message {
	now := time.Now()
	var temperature float64
	if request.Temperature > 0 {
		temperature = float64(request.Temperature)
	}

	msg := &Message{
		MessageID:        generateMessageID(userID, sessionID, now),
		AgentType:        agentType,
		ContentType:      contentType,
		UserID:           userID,
		SessionID:        sessionID,
		Role:             role,
		Content:          content,
		Model:            response.Model,
		RequestModel:     request.Model,
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
		MaxTokens:        request.MaxTokens,
		Temperature:      temperature,
		HasToolCalls:     len(choice.Message.ToolCalls) > 0,
		FinishReason:     string(choice.FinishReason),
		CreatedAt:        now,
	}

	return msg
}

// NewUserMessage creates a message for a user input (no LLM response)
func NewUserMessage(userID string, sessionID string, content string, contentType ContentType) *Message {
	now := time.Now()
	return &Message{
		MessageID:   generateMessageID(userID, sessionID, now),
		AgentType:   AgentTypeUser,
		ContentType: contentType,
		UserID:      userID,
		SessionID:   sessionID,
		Role:        openai.ChatMessageRoleUser,
		Content:     content,
		CreatedAt:   now,
	}
}

// generateMessageID generates a unique message ID
func generateMessageID(userID string, sessionID string, timestamp time.Time) string {
	// Format: {timestamp}-{userID}-{sessionID}-{random4}
	// Include nanoseconds to ensure uniqueness even for messages created in the same second
	date := timestamp.Format("060102150405")  // YYMMDDHHMMSS
	nanos := timestamp.Nanosecond() % 1000000 // Last 6 digits of nanoseconds (millisecond precision)
	return fmt.Sprintf("%s%06d-%s-%s-%s", date, nanos, userID[:min(8, len(userID))], sessionID[:min(8, len(sessionID))], randomString(4))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
