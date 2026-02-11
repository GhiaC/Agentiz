package engine

import (
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// formatToolCallsContent builds display text for messages that only have tool calls (no content).
// Example: "[Tool Calls: get_weather]" or "[Tool Calls: get_weather, search]"
func formatToolCallsContent(toolCalls []openai.ToolCall) string {
	if len(toolCalls) == 0 {
		return "[Tool Calls: 0]"
	}
	names := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.Function.Name != "" {
			names = append(names, tc.Function.Name)
		}
	}
	if len(names) == 0 {
		return "[Tool Calls: (no name)]"
	}
	return "[Tool Calls: " + strings.Join(names, ", ") + "]"
}

// ToolCallStore is the interface for persisting tool calls.
// Implemented by store.MongoDBStore and store.SQLiteStore.
type ToolCallStore interface {
	PutToolCall(*model.ToolCall) error
	UpdateToolCallResponse(toolID string, response string) error
}

// ToolCallPersister provides tool call persistence functionality.
// Use NewToolCallPersister to create an instance.
type ToolCallPersister struct {
	store  ToolCallStore
	logger string // prefix for log messages
}

// NewToolCallPersister creates a new ToolCallPersister if the session store supports it.
// Returns nil if the store doesn't implement ToolCallStore (logs a warning).
func NewToolCallPersister(sessionStore model.SessionStore, logPrefix string) *ToolCallPersister {
	if sessionStore == nil {
		log.Log.Warnf("[%s] session store is nil; tool calls will not be saved to DB", logPrefix)
		return nil
	}
	tcStore, ok := sessionStore.(ToolCallStore)
	if !ok {
		log.Log.Warnf("[%s] session store (type=%T) does not implement ToolCallStore; tool calls will not be saved to DB", logPrefix, sessionStore)
		return nil
	}
	log.Log.Debugf("[%s] ToolCallPersister created successfully for store type=%T", logPrefix, sessionStore)
	return &ToolCallPersister{
		store:  tcStore,
		logger: logPrefix,
	}
}

// Save persists a tool call to the database and returns the generated ToolID.
// Returns empty string if save fails (error is logged).
func (p *ToolCallPersister) Save(
	session *model.Session,
	messageID string,
	toolCall openai.ToolCall,
) string {
	if p == nil || p.store == nil {
		return ""
	}

	now := time.Now()
	toolID := session.GenerateToolID()
	tc := &model.ToolCall{
		ToolID:       toolID,
		ToolCallID:   toolCall.ID,
		MessageID:    messageID,
		SessionID:    session.SessionID,
		UserID:       session.UserID,
		AgentType:    session.AgentType,
		FunctionName: toolCall.Function.Name,
		Arguments:    toolCall.Function.Arguments,
		Response:     "",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := p.store.PutToolCall(tc); err != nil {
		log.Log.Warnf("[%s] ⚠️  Failed to save tool call | ToolID: %s | ToolCallID: %s | Error: %v",
			p.logger, toolID, toolCall.ID, err)
		return ""
	}

	log.Log.Infof("[%s] 🔧 Tool call saved | ToolID: %s | ToolCallID: %s | Function: %s",
		p.logger, toolID, toolCall.ID, toolCall.Function.Name)
	return toolID
}

// SaveWithAgentType persists a tool call with an explicit agent type (for CoreHandler).
func (p *ToolCallPersister) SaveWithAgentType(
	session *model.Session,
	messageID string,
	toolCall openai.ToolCall,
	agentType model.AgentType,
) string {
	if p == nil || p.store == nil {
		return ""
	}

	now := time.Now()
	toolID := session.GenerateToolID()
	tc := &model.ToolCall{
		ToolID:       toolID,
		ToolCallID:   toolCall.ID,
		MessageID:    messageID,
		SessionID:    session.SessionID,
		UserID:       session.UserID,
		AgentType:    agentType,
		FunctionName: toolCall.Function.Name,
		Arguments:    toolCall.Function.Arguments,
		Response:     "",
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := p.store.PutToolCall(tc); err != nil {
		log.Log.Warnf("[%s] ⚠️  Failed to save tool call | ToolID: %s | ToolCallID: %s | Error: %v",
			p.logger, toolID, toolCall.ID, err)
		return ""
	}

	log.Log.Infof("[%s] 🔧 Tool call saved | ToolID: %s | ToolCallID: %s | Function: %s",
		p.logger, toolID, toolCall.ID, toolCall.Function.Name)
	return toolID
}

// Update updates the response for a tool call by ToolID.
// Does nothing if toolID is empty.
func (p *ToolCallPersister) Update(toolID, response string) {
	if p == nil || p.store == nil || toolID == "" {
		return
	}

	if err := p.store.UpdateToolCallResponse(toolID, response); err != nil {
		log.Log.Warnf("[%s] ⚠️  Failed to update tool call response | ToolID: %s | Error: %v",
			p.logger, toolID, err)
	} else {
		log.Log.Infof("[%s] ✅ Tool call response updated | ToolID: %s", p.logger, toolID)
	}
}

// IsAvailable returns true if the persister can save tool calls.
func (p *ToolCallPersister) IsAvailable() bool {
	return p != nil && p.store != nil
}
