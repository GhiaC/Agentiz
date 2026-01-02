package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentize/fsrepo"
	"agentize/model"
	"agentize/store"
)

// Engine is the main agent engine
type Engine struct {
	repo             *fsrepo.NodeRepository
	sessionStore     store.SessionStore
	toolStrategy     model.MergeStrategy
	llmHandler       *LLMHandler             // Optional LLM handler
	functionRegistry *model.FunctionRegistry // Registry for tool functions
}

// NewEngine creates a new engine instance
func NewEngine(repo *fsrepo.NodeRepository, sessionStore store.SessionStore, toolStrategy model.MergeStrategy) *Engine {
	if toolStrategy == "" {
		toolStrategy = model.MergeStrategyOverride
	}
	return &Engine{
		repo:             repo,
		sessionStore:     sessionStore,
		toolStrategy:     toolStrategy,
		llmHandler:       nil,                         // LLM handler is optional
		functionRegistry: model.NewFunctionRegistry(), // Initialize function registry
	}
}

// NewEngineWithFunctions creates a new engine instance with a pre-configured function registry
func NewEngineWithFunctions(repo *fsrepo.NodeRepository, sessionStore store.SessionStore, toolStrategy model.MergeStrategy, functionRegistry *model.FunctionRegistry) *Engine {
	if toolStrategy == "" {
		toolStrategy = model.MergeStrategyOverride
	}
	if functionRegistry == nil {
		functionRegistry = model.NewFunctionRegistry()
	}
	return &Engine{
		repo:             repo,
		sessionStore:     sessionStore,
		toolStrategy:     toolStrategy,
		llmHandler:       nil,
		functionRegistry: functionRegistry,
	}
}

// SetFunctionRegistry sets the function registry for the engine
func (e *Engine) SetFunctionRegistry(registry *model.FunctionRegistry) {
	e.functionRegistry = registry
}

// GetFunctionRegistry returns the function registry
func (e *Engine) GetFunctionRegistry() *model.FunctionRegistry {
	return e.functionRegistry
}

// SetLLMHandler sets the LLM handler for the engine
func (e *Engine) SetLLMHandler(handler *LLMHandler) {
	e.llmHandler = handler
}

// StartSession creates a new session for a user starting at root
func (e *Engine) StartSession(userID string) (*model.Session, error) {
	session := model.NewSession(userID)

	// Load root node
	rootNode, err := e.repo.LoadNode("root")
	if err != nil {
		return nil, fmt.Errorf("failed to load root node: %w", err)
	}

	// Initialize session with root node
	session.CurrentNodePath = "root"
	session.OpenedFiles = []string{"root/node.md", "root/node.yaml", "root/tools.json"}

	// Accumulate tools from root
	registry := model.NewToolRegistry(e.toolStrategy)
	if err := registry.AddTools(rootNode.Tools); err != nil {
		return nil, fmt.Errorf("failed to add root tools: %w", err)
	}
	session.AccumulatedTools = registry.GetTools()

	// Add node digest
	session.NodeDigests = []model.NodeDigest{
		createNodeDigest(rootNode),
	}

	// Save session
	if err := e.sessionStore.Put(session); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	return session, nil
}

// GetContext returns the current context for a session
func (e *Engine) GetContext(sessionID string) (*Context, error) {
	session, err := e.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// Load current node
	currentNode, err := e.repo.LoadNode(session.CurrentNodePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load current node: %w", err)
	}

	return &Context{
		Session:          session,
		CurrentNode:      currentNode,
		AccumulatedTools: session.AccumulatedTools,
	}, nil
}

// Advance moves the session to the next node if allowed
func (e *Engine) Advance(sessionID string) (*model.Session, error) {
	session, err := e.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	// Check if advance is allowed
	currentNode, err := e.repo.LoadNode(session.CurrentNodePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load current node: %w", err)
	}

	if !currentNode.CanUserAccessNextSimple(session.UserID) {
		return nil, fmt.Errorf("advance not allowed for user %s at node: %s", session.UserID, session.CurrentNodePath)
	}

	// Get children nodes
	children, err := e.repo.GetChildren(session.CurrentNodePath)
	if err != nil || len(children) == 0 {
		return nil, fmt.Errorf("no child nodes available for: %s", session.CurrentNodePath)
	}

	// For sequential mode, take the first child
	// TODO: Support other routing modes (parallel, conditional, etc.)
	nextPath := children[0]

	// Load next node
	nextNode, err := e.repo.LoadNode(nextPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load next node: %w", err)
	}

	// Update session
	session.CurrentNodePath = nextPath
	session.OpenedFiles = append(session.OpenedFiles,
		nextPath+"/node.md",
		nextPath+"/node.yaml",
		nextPath+"/tools.json",
	)

	// Accumulate tools
	registry := model.NewToolRegistry(e.toolStrategy)
	// Add all tools from root to current (including new ones)
	for _, digest := range session.NodeDigests {
		node, err := e.repo.LoadNode(digest.Path)
		if err == nil {
			_ = registry.AddTools(node.Tools)
		}
	}
	// Add new node's tools
	_ = registry.AddTools(nextNode.Tools)
	session.AccumulatedTools = registry.GetTools()

	// Add node digest
	session.NodeDigests = append(session.NodeDigests, createNodeDigest(nextNode))

	// Save session
	if err := e.sessionStore.Put(session); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	return session, nil
}

// Step processes user input and returns agent output
// If LLM handler is set, it uses LLM; otherwise uses rule-based logic
func (e *Engine) Step(sessionID string, userInput string) (*StepOutput, error) {
	session, err := e.sessionStore.Get(sessionID)
	if err != nil {
		return nil, err
	}

	currentNode, err := e.repo.LoadNode(session.CurrentNodePath)
	if err != nil {
		return nil, fmt.Errorf("failed to load current node: %w", err)
	}

	output := &StepOutput{
		Action:      "respond",
		Message:     "",
		ToolCall:    nil,
		CurrentNode: session.CurrentNodePath,
		OpenedFiles: session.OpenedFiles,
	}

	// Rule-based decision making
	// Check if user wants to advance
	if e.shouldAdvance(userInput, currentNode, session.UserID) {
		children, err := e.repo.GetChildren(session.CurrentNodePath)
		if err == nil && len(children) > 0 {
			output.Action = "advance"
			output.Message = fmt.Sprintf("Advancing to next node: %s", children[0])
			return output, nil
		}
	}

	// If LLM handler is available, use it
	if e.llmHandler != nil {
		return e.stepWithLLM(sessionID, userInput, currentNode, session)
	}

	// Fallback to rule-based response
	output.Message = fmt.Sprintf("Processing input at node: %s", currentNode.Title)
	if currentNode.Content != "" {
		output.Message += "\n" + currentNode.Content[:min(100, len(currentNode.Content))]
	}

	return output, nil
}

// stepWithLLM processes user input using LLM
func (e *Engine) stepWithLLM(sessionID string, userInput string, currentNode *model.Node, session *model.Session) (*StepOutput, error) {
	// Get or create conversation memory
	var memory *ConversationMemory
	if memData, ok := session.Memory["conversation"].(*ConversationMemory); ok {
		memory = memData
	} else {
		memory = NewConversationMemory()
		session.Memory["conversation"] = memory
	}

	// Add user message
	memory.AddUserMessage(userInput)

	// Build system prompt from current node
	systemPrompt := currentNode.Content
	if currentNode.Description != "" {
		systemPrompt = currentNode.Description + "\n\n" + systemPrompt
	}

	// Get active tools
	activeTools := make([]model.Tool, 0)
	for _, tool := range session.AccumulatedTools {
		if tool.Status == model.ToolStatusActive {
			activeTools = append(activeTools, tool)
		}
	}

	// Process with LLM
	ctx := context.Background()
	response, tokens, err := e.llmHandler.ProcessWithTools(
		ctx,
		systemPrompt,
		memory.GetMessages(),
		activeTools,
		e.createToolExecutor(sessionID),
		10, // max iterations
	)

	if err != nil {
		return nil, fmt.Errorf("LLM processing failed: %w", err)
	}

	// Add assistant response to memory
	memory.AddAssistantMessage(response)

	// Save session
	session.UpdatedAt = time.Now() // Update timestamp
	if err := e.sessionStore.Put(session); err != nil {
		return nil, fmt.Errorf("failed to save session: %w", err)
	}

	return &StepOutput{
		Action:      "respond",
		Message:     response,
		ToolCall:    nil,
		CurrentNode: session.CurrentNodePath,
		OpenedFiles: session.OpenedFiles,
		Debug: map[string]interface{}{
			"tokens": tokens,
		},
	}, nil
}

// createToolExecutor creates a tool executor for the session
func (e *Engine) createToolExecutor(sessionID string) ToolExecutor {
	return func(toolName string, args map[string]interface{}) (string, error) {
		// Check if function is registered
		if e.functionRegistry == nil {
			return "", fmt.Errorf("function registry not initialized")
		}

		// Execute the tool function
		return e.functionRegistry.Execute(toolName, args)
	}
}

// shouldAdvance determines if the session should advance based on user input and auth
func (e *Engine) shouldAdvance(userInput string, node *model.Node, userID string) bool {
	if !node.CanUserAccessNextSimple(userID) {
		return false
	}

	// For now, we don't have advance condition in Auth, so we'll check user input
	// Simple keyword matching for MVP - check for common advance keywords
	lowerInput := strings.ToLower(userInput)
	advanceKeywords := []string{"next", "continue", "proceed", "advance", "go"}
	
	for _, keyword := range advanceKeywords {
		if strings.Contains(lowerInput, keyword) {
			return true
		}
	}
	
	return false
}

// Context represents the current context of a session
type Context struct {
	Session          *model.Session
	CurrentNode      *model.Node
	AccumulatedTools []model.Tool // Only active and temporary disabled tools (hidden excluded)
}

// GetActiveTools returns only active tools (excluding disabled and hidden)
func (c *Context) GetActiveTools() []model.Tool {
	activeTools := make([]model.Tool, 0)
	for _, tool := range c.AccumulatedTools {
		if tool.Status == model.ToolStatusActive {
			activeTools = append(activeTools, tool)
		}
	}
	return activeTools
}

// GetDisabledTools returns only temporarily disabled tools
func (c *Context) GetDisabledTools() []model.Tool {
	disabledTools := make([]model.Tool, 0)
	for _, tool := range c.AccumulatedTools {
		if tool.Status == model.ToolStatusTemporaryDisabled {
			disabledTools = append(disabledTools, tool)
		}
	}
	return disabledTools
}

// CanUseTool checks if a tool can be used and returns an error if not
func (c *Context) CanUseTool(toolName string) error {
	for _, tool := range c.AccumulatedTools {
		if tool.Name == toolName {
			return tool.CanUse()
		}
	}
	return &model.ToolNotFoundError{ToolName: toolName}
}

// StepOutput represents the output of a step operation
type StepOutput struct {
	Action      string                 `json:"action"` // "respond", "advance", "tool_call"
	Message     string                 `json:"message"`
	ToolCall    *ToolCall              `json:"tool_call,omitempty"`
	CurrentNode string                 `json:"current_node"`
	OpenedFiles []string               `json:"opened_files"`
	Debug       map[string]interface{} `json:"debug,omitempty"`
}

// ToolCall represents a tool invocation
type ToolCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

// Helper functions
func createNodeDigest(node *model.Node) model.NodeDigest {
	excerpt := node.Content
	if len(excerpt) > 100 {
		excerpt = excerpt[:100] + "..."
	}
	return model.NodeDigest{
		Path:     node.Path,
		ID:       node.ID,
		Title:    node.Title,
		Hash:     node.Hash,
		LoadedAt: node.LoadedAt,
		Excerpt:  excerpt,
	}
}

// GetSessionStore returns the session store
func (e *Engine) GetSessionStore() store.SessionStore {
	return e.sessionStore
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
