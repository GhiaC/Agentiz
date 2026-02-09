package model

import "time"

// MergeStrategy defines how tools with the same name should be handled
type MergeStrategy string

const (
	// MergeStrategyOverride replaces tools with the same name (lower level wins)
	MergeStrategyOverride MergeStrategy = "override"
	// MergeStrategyAppend keeps all tools, renaming duplicates
	MergeStrategyAppend MergeStrategy = "append"
	// MergeStrategyError returns an error if duplicate names are found
	MergeStrategyError MergeStrategy = "error"
)

// ToolRegistry manages tool aggregation and conflict resolution
type ToolRegistry struct {
	strategy MergeStrategy
	tools    map[string]Tool // name -> tool
}

// NewToolRegistry creates a new tool registry with the specified merge strategy
func NewToolRegistry(strategy MergeStrategy) *ToolRegistry {
	if strategy == "" {
		strategy = MergeStrategyOverride // default
	}
	return &ToolRegistry{
		strategy: strategy,
		tools:    make(map[string]Tool),
	}
}

// AddTools adds tools to the registry, applying the merge strategy
func (tr *ToolRegistry) AddTools(tools []Tool) error {
	for _, tool := range tools {
		if err := tr.AddTool(tool); err != nil {
			return err
		}
	}
	return nil
}

// AddTool adds a single tool to the registry
func (tr *ToolRegistry) AddTool(tool Tool) error {
	existing, exists := tr.tools[tool.Name]

	switch tr.strategy {
	case MergeStrategyOverride:
		// Lower level (later added) wins
		tr.tools[tool.Name] = tool

	case MergeStrategyAppend:
		if exists {
			// Rename the existing tool
			existing.Name = existing.Name + "_prev"
			tr.tools[existing.Name] = existing
		}
		tr.tools[tool.Name] = tool

	case MergeStrategyError:
		if exists {
			return &ToolConflictError{
				ToolName: tool.Name,
				Existing: existing,
				New:      tool,
			}
		}
		tr.tools[tool.Name] = tool

	default:
		// Default to override
		tr.tools[tool.Name] = tool
	}

	return nil
}

// GetTools returns all tools as a slice, excluding hidden tools
func (tr *ToolRegistry) GetTools() []Tool {
	tools := make([]Tool, 0, len(tr.tools))
	for _, tool := range tr.tools {
		// Skip hidden tools
		if tool.Status == ToolStatusHidden {
			continue
		}
		tools = append(tools, tool)
	}
	return tools
}

// GetToolsIncludingHidden returns all tools including hidden ones
func (tr *ToolRegistry) GetToolsIncludingHidden() []Tool {
	tools := make([]Tool, 0, len(tr.tools))
	for _, tool := range tr.tools {
		tools = append(tools, tool)
	}
	return tools
}

// GetTool returns a tool by name (including hidden tools)
func (tr *ToolRegistry) GetTool(name string) (Tool, bool) {
	tool, ok := tr.tools[name]
	return tool, ok
}

// GetActiveTool returns a tool by name only if it's active (not disabled or hidden)
func (tr *ToolRegistry) GetActiveTool(name string) (Tool, bool) {
	tool, ok := tr.tools[name]
	if !ok {
		return tool, false
	}
	// Check if tool is active
	if tool.Status == ToolStatusActive {
		return tool, true
	}
	return tool, false
}

// IsToolUsable checks if a tool can be used (not disabled or hidden)
func (tr *ToolRegistry) IsToolUsable(name string) bool {
	tool, ok := tr.tools[name]
	if !ok {
		return false
	}
	return tool.IsUsable()
}

// CanUseTool checks if a tool can be used and returns an error if not
func (tr *ToolRegistry) CanUseTool(name string) error {
	tool, ok := tr.tools[name]
	if !ok {
		return &ToolNotFoundError{ToolName: name}
	}
	return tool.CanUse()
}

// ToolConflictError is returned when a tool name conflict occurs with MergeStrategyError
type ToolConflictError struct {
	ToolName string
	Existing Tool
	New      Tool
}

func (e *ToolConflictError) Error() string {
	return "tool name conflict: " + e.ToolName
}

// ToolNotFoundError is returned when a tool is not found
type ToolNotFoundError struct {
	ToolName string
}

func (e *ToolNotFoundError) Error() string {
	return "tool not found: " + e.ToolName
}

// ToolDisabledError is returned when trying to use a disabled tool
type ToolDisabledError struct {
	ToolName      string
	DisableReason DisableReason
	ErrorMessage  string
}

func (e *ToolDisabledError) Error() string {
	msg := "tool is disabled: " + e.ToolName
	if e.DisableReason != DisableReasonNone {
		msg += " (reason: " + string(e.DisableReason) + ")"
	}
	if e.ErrorMessage != "" {
		msg += " - " + e.ErrorMessage
	}
	return msg
}

// IsUsable checks if the tool can be used (not disabled or hidden)
func (t *Tool) IsUsable() bool {
	return t.Status == ToolStatusActive
}

// CanUse checks if the tool can be used and returns an error if not
func (t *Tool) CanUse() error {
	if t.Status == ToolStatusHidden {
		return &ToolNotFoundError{ToolName: t.Name}
	}
	if t.Status == ToolStatusTemporaryDisabled {
		return &ToolDisabledError{
			ToolName:      t.Name,
			DisableReason: t.DisableReason,
			ErrorMessage:  t.ErrorMessage,
		}
	}
	if t.Status != ToolStatusActive {
		return &ToolDisabledError{
			ToolName:      t.Name,
			DisableReason: DisableReasonCustom,
			ErrorMessage:  "tool status is: " + string(t.Status),
		}
	}
	return nil
}

// SetTemporaryDisabled marks the tool as temporarily disabled with a reason
func (t *Tool) SetTemporaryDisabled(reason DisableReason, errorMessage string) {
	t.Status = ToolStatusTemporaryDisabled
	t.DisableReason = reason
	t.ErrorMessage = errorMessage
}

// SetHidden marks the tool as hidden
func (t *Tool) SetHidden() {
	t.Status = ToolStatusHidden
	t.DisableReason = DisableReasonNone
	t.ErrorMessage = ""
}

// SetActive marks the tool as active
func (t *Tool) SetActive() {
	t.Status = ToolStatusActive
	t.DisableReason = DisableReasonNone
	t.ErrorMessage = ""
}

// ToolCall represents a tool call execution record
type ToolCall struct {
	// ToolCallID is the unique identifier for this tool call (from OpenAI)
	ToolCallID string

	// MessageID identifies the message that triggered this tool call
	MessageID string

	// SessionID identifies the session this tool call belongs to
	SessionID string

	// UserID identifies the user who triggered this tool call
	UserID string

	// AgentType indicates which type of agent made this tool call (core, low, high)
	AgentType AgentType

	// FunctionName is the name of the function/tool that was called
	FunctionName string

	// Arguments is the JSON string of arguments passed to the tool
	Arguments string

	// Response is the result/response from the tool execution
	Response string

	// Metadata
	CreatedAt time.Time
	UpdatedAt time.Time
}
