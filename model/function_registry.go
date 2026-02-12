package model

import (
	"fmt"
	"sync"
)

// ToolFunction is the signature for tool execution functions
// It receives a map of arguments and returns a result string and error
type ToolFunction func(args map[string]interface{}) (string, error)

// registeredEntry holds a tool function and its optional display name for UI/status
type registeredEntry struct {
	Fn          ToolFunction
	DisplayName string
}

// FunctionRegistry manages the mapping between tool names and their Go functions
// This registry must be populated at application startup with all available functions
type FunctionRegistry struct {
	mu        sync.RWMutex
	functions map[string]registeredEntry // tool name -> function + display name
}

// NewFunctionRegistry creates a new function registry
func NewFunctionRegistry() *FunctionRegistry {
	return &FunctionRegistry{
		functions: make(map[string]registeredEntry),
	}
}

// Register registers a function for a tool name with an optional display name for UI/status.
// If displayName is empty, toolName is used as the display name.
func (fr *FunctionRegistry) Register(toolName string, displayName string, fn ToolFunction) error {
	if toolName == "" {
		return fmt.Errorf("tool name cannot be empty")
	}
	if fn == nil {
		return fmt.Errorf("function cannot be nil for tool: %s", toolName)
	}
	if displayName == "" {
		displayName = toolName
	}

	fr.mu.Lock()
	defer fr.mu.Unlock()

	if _, exists := fr.functions[toolName]; exists {
		return fmt.Errorf("function already registered for tool: %s", toolName)
	}

	fr.functions[toolName] = registeredEntry{Fn: fn, DisplayName: displayName}
	return nil
}

// RegisterBatch registers multiple functions at once (display name defaults to tool name)
func (fr *FunctionRegistry) RegisterBatch(registrations map[string]ToolFunction) error {
	for toolName, fn := range registrations {
		if err := fr.Register(toolName, toolName, fn); err != nil {
			return fmt.Errorf("failed to register %s: %w", toolName, err)
		}
	}
	return nil
}

// MustRegister registers a function with an optional display name and panics if there's an error
func (fr *FunctionRegistry) MustRegister(toolName string, displayName string, fn ToolFunction) {
	if err := fr.Register(toolName, displayName, fn); err != nil {
		panic(fmt.Sprintf("failed to register tool function %s: %v", toolName, err))
	}
}

// RegisterOrReplace registers a function, replacing any existing registration.
// If displayName is empty and the tool already exists, the existing display name is preserved.
func (fr *FunctionRegistry) RegisterOrReplace(toolName string, displayName string, fn ToolFunction) error {
	if toolName == "" {
		return fmt.Errorf("tool name cannot be empty")
	}
	if fn == nil {
		return fmt.Errorf("function cannot be nil for tool: %s", toolName)
	}

	fr.mu.Lock()
	defer fr.mu.Unlock()

	entry := registeredEntry{Fn: fn, DisplayName: displayName}
	if displayName == "" {
		if existing, ok := fr.functions[toolName]; ok {
			entry.DisplayName = existing.DisplayName
		} else {
			entry.DisplayName = toolName
		}
	}
	fr.functions[toolName] = entry
	return nil
}

// DisableToolTemporarily temporarily disables a tool by replacing its function
// with a disabled version that returns an appropriate error message
// This uses the ToolRegistry's temporary disable mechanism
func (fr *FunctionRegistry) DisableToolTemporarily(toolName string, reason DisableReason, errorMessage string) error {
	if toolName == "" {
		return fmt.Errorf("tool name cannot be empty")
	}

	// Build error message
	msg := fmt.Sprintf("Tool '%s' is temporarily disabled", toolName)
	if reason != DisableReasonNone && reason != "" {
		msg += fmt.Sprintf(" (reason: %s)", reason)
	}
	if errorMessage != "" {
		msg += fmt.Sprintf(" - %s", errorMessage)
	}

	// Create disabled function that returns the error message
	disabledFn := func(args map[string]interface{}) (string, error) {
		return "", &ToolDisabledError{
			ToolName:      toolName,
			DisableReason: reason,
			ErrorMessage:  errorMessage,
		}
	}

	return fr.RegisterOrReplace(toolName, "", disabledFn)
}

// GetDisplayName returns the display name for a tool, or toolName if not set, or empty if not registered
func (fr *FunctionRegistry) GetDisplayName(toolName string) string {
	fr.mu.RLock()
	defer fr.mu.RUnlock()
	entry, ok := fr.functions[toolName]
	if !ok {
		return ""
	}
	if entry.DisplayName != "" {
		return entry.DisplayName
	}
	return toolName
}

// Get retrieves a function for a tool name
func (fr *FunctionRegistry) Get(toolName string) (ToolFunction, bool) {
	fr.mu.RLock()
	defer fr.mu.RUnlock()

	entry, ok := fr.functions[toolName]
	return entry.Fn, ok
}

// Execute executes a tool function by name
func (fr *FunctionRegistry) Execute(toolName string, args map[string]interface{}) (string, error) {
	fn, ok := fr.Get(toolName)
	if !ok {
		return "", &FunctionNotFoundError{ToolName: toolName}
	}

	return fn(args)
}

// Has checks if a function is registered for a tool name
func (fr *FunctionRegistry) Has(toolName string) bool {
	fr.mu.RLock()
	defer fr.mu.RUnlock()

	_, ok := fr.functions[toolName]
	return ok
}

// GetAllRegistered returns all registered tool names
func (fr *FunctionRegistry) GetAllRegistered() []string {
	fr.mu.RLock()
	defer fr.mu.RUnlock()

	names := make([]string, 0, len(fr.functions))
	for name := range fr.functions {
		names = append(names, name)
	}
	return names
}

// ValidateTools checks if all tools in a registry have corresponding functions
// Returns a list of missing tool names
func (fr *FunctionRegistry) ValidateTools(toolRegistry *ToolRegistry) []string {
	fr.mu.RLock()
	defer fr.mu.RUnlock()

	missing := make([]string, 0)
	tools := toolRegistry.GetToolsIncludingHidden()

	for _, tool := range tools {
		// Only validate active tools (disabled/hidden tools might not need functions)
		if tool.Status == ToolStatusActive {
			if _, ok := fr.functions[tool.Name]; !ok {
				missing = append(missing, tool.Name)
			}
		}
	}

	return missing
}

// ValidateAllTools checks if all tools have functions and returns an error if any are missing
func (fr *FunctionRegistry) ValidateAllTools(toolRegistry *ToolRegistry) error {
	missing := fr.ValidateTools(toolRegistry)
	if len(missing) > 0 {
		return &MissingFunctionsError{MissingTools: missing}
	}
	return nil
}

// FunctionNotFoundError is returned when a function is not found for a tool
type FunctionNotFoundError struct {
	ToolName string
}

func (e *FunctionNotFoundError) Error() string {
	return fmt.Sprintf("function not found for tool: %s", e.ToolName)
}

// MissingFunctionsError is returned when tools are missing their functions
type MissingFunctionsError struct {
	MissingTools []string
}

func (e *MissingFunctionsError) Error() string {
	return fmt.Sprintf("missing functions for tools: %v", e.MissingTools)
}
