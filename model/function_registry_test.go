package model

import (
	"errors"
	"testing"
)

func TestFunctionRegistry(t *testing.T) {
	registry := NewFunctionRegistry()

	// Test Register
	err := registry.Register("test_tool", "", func(args map[string]interface{}) (string, error) {
		return "success", nil
	})
	if err != nil {
		t.Fatalf("Failed to register function: %v", err)
	}

	// Test duplicate registration
	err = registry.Register("test_tool", "", func(args map[string]interface{}) (string, error) {
		return "duplicate", nil
	})
	if err == nil {
		t.Error("Expected error for duplicate registration")
	}

	// Test Get
	fn, ok := registry.Get("test_tool")
	if !ok {
		t.Fatal("Function should be found")
	}
	result, err := fn(nil)
	if err != nil {
		t.Errorf("Function execution failed: %v", err)
	}
	if result != "success" {
		t.Errorf("Expected 'success', got '%s'", result)
	}

	// Test Has
	if !registry.Has("test_tool") {
		t.Error("Has should return true for registered tool")
	}
	if registry.Has("nonexistent") {
		t.Error("Has should return false for unregistered tool")
	}

	// Test Execute
	result, err = registry.Execute("test_tool", map[string]interface{}{"key": "value"})
	if err != nil {
		t.Errorf("Execute failed: %v", err)
	}
	if result != "success" {
		t.Errorf("Expected 'success', got '%s'", result)
	}

	// Test Execute with nonexistent tool
	_, err = registry.Execute("nonexistent", nil)
	if err == nil {
		t.Fatal("Expected error for nonexistent tool")
	}
	var notFoundErr *FunctionNotFoundError
	if !errors.As(err, &notFoundErr) {
		t.Errorf("Expected FunctionNotFoundError, got %T", err)
	}
}

func TestFunctionRegistryBatch(t *testing.T) {
	registry := NewFunctionRegistry()

	registrations := map[string]ToolFunction{
		"tool1": func(args map[string]interface{}) (string, error) {
			return "result1", nil
		},
		"tool2": func(args map[string]interface{}) (string, error) {
			return "result2", nil
		},
	}

	err := registry.RegisterBatch(registrations)
	if err != nil {
		t.Fatalf("Failed to register batch: %v", err)
	}

	if !registry.Has("tool1") || !registry.Has("tool2") {
		t.Error("Batch registration failed")
	}
}

func TestFunctionRegistryValidation(t *testing.T) {
	funcRegistry := NewFunctionRegistry()
	toolRegistry := NewToolRegistry(MergeStrategyOverride)

	// Register some functions
	funcRegistry.MustRegister("tool1", "", func(args map[string]interface{}) (string, error) {
		return "ok", nil
	})
	funcRegistry.MustRegister("tool2", "", func(args map[string]interface{}) (string, error) {
		return "ok", nil
	})

	// Add tools to registry
	tool1 := Tool{
		Name:        "tool1",
		Description: "Tool 1",
		InputSchema: map[string]interface{}{},
		Status:      ToolStatusActive,
	}
	tool2 := Tool{
		Name:        "tool2",
		Description: "Tool 2",
		InputSchema: map[string]interface{}{},
		Status:      ToolStatusActive,
	}
	tool3 := Tool{
		Name:        "tool3",
		Description: "Tool 3",
		InputSchema: map[string]interface{}{},
		Status:      ToolStatusActive,
	}

	toolRegistry.AddTool(tool1)
	toolRegistry.AddTool(tool2)
	toolRegistry.AddTool(tool3)

	// Validate - should find missing tool3
	missing := funcRegistry.ValidateTools(toolRegistry)
	if len(missing) != 1 || missing[0] != "tool3" {
		t.Errorf("Expected missing tool3, got: %v", missing)
	}

	// Test ValidateAllTools
	err := funcRegistry.ValidateAllTools(toolRegistry)
	if err == nil {
		t.Fatal("Expected error for missing functions")
	}
	var missingErr *MissingFunctionsError
	if !errors.As(err, &missingErr) {
		t.Errorf("Expected MissingFunctionsError, got %T", err)
	}
	if len(missingErr.MissingTools) != 1 || missingErr.MissingTools[0] != "tool3" {
		t.Errorf("Expected missing tool3, got: %v", missingErr.MissingTools)
	}

	// Register missing function and validate again
	funcRegistry.MustRegister("tool3", "", func(args map[string]interface{}) (string, error) {
		return "ok", nil
	})

	err = funcRegistry.ValidateAllTools(toolRegistry)
	if err != nil {
		t.Errorf("Expected no error after registering all functions, got: %v", err)
	}
}

func TestFunctionRegistryWithDisabledTools(t *testing.T) {
	funcRegistry := NewFunctionRegistry()
	toolRegistry := NewToolRegistry(MergeStrategyOverride)

	// Register function for active tool
	funcRegistry.MustRegister("active_tool", "", func(args map[string]interface{}) (string, error) {
		return "ok", nil
	})

	// Add active tool
	activeTool := Tool{
		Name:        "active_tool",
		Description: "Active tool",
		InputSchema: map[string]interface{}{},
		Status:      ToolStatusActive,
	}
	toolRegistry.AddTool(activeTool)

	// Add disabled tool (should not require function)
	disabledTool := Tool{
		Name:          "disabled_tool",
		Description:   "Disabled tool",
		InputSchema:   map[string]interface{}{},
		Status:        ToolStatusTemporaryDisabled,
		DisableReason: DisableReasonMaintenance,
	}
	toolRegistry.AddTool(disabledTool)

	// Add hidden tool (should not require function)
	hiddenTool := Tool{
		Name:        "hidden_tool",
		Description: "Hidden tool",
		InputSchema: map[string]interface{}{},
		Status:      ToolStatusHidden,
	}
	toolRegistry.AddTool(hiddenTool)

	// Validate - should not complain about disabled/hidden tools
	missing := funcRegistry.ValidateTools(toolRegistry)
	if len(missing) != 0 {
		t.Errorf("Expected no missing functions (disabled/hidden tools don't need functions), got: %v", missing)
	}

	err := funcRegistry.ValidateAllTools(toolRegistry)
	if err != nil {
		t.Errorf("Expected no error (disabled/hidden tools don't need functions), got: %v", err)
	}
}

func TestFunctionRegistry_DisableToolTemporarily(t *testing.T) {
	registry := NewFunctionRegistry()

	// Register initial function
	fn1 := func(args map[string]interface{}) (string, error) {
		return "working", nil
	}
	if err := registry.Register("test_tool", "", fn1); err != nil {
		t.Fatalf("Failed to register: %v", err)
	}

	// Verify it's working
	result, err := registry.Execute("test_tool", nil)
	if err != nil {
		t.Fatalf("Failed to execute: %v", err)
	}
	if result != "working" {
		t.Errorf("Expected 'working', got %s", result)
	}

	// Disable the tool temporarily
	if err := registry.DisableToolTemporarily(
		"test_tool",
		DisableReasonRateLimit,
		"API quota exceeded. Please try again later.",
	); err != nil {
		t.Fatalf("Failed to disable tool: %v", err)
	}

	// Verify it's disabled - should return ToolDisabledError
	_, err = registry.Execute("test_tool", nil)
	if err == nil {
		t.Fatal("Expected error when executing disabled tool")
	}
	var disabledErr *ToolDisabledError
	if !errors.As(err, &disabledErr) {
		t.Errorf("Expected ToolDisabledError, got %T: %v", err, err)
	}
	if disabledErr.ToolName != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got %s", disabledErr.ToolName)
	}
	if disabledErr.DisableReason != DisableReasonRateLimit {
		t.Errorf("Expected reason 'rate_limit', got %s", disabledErr.DisableReason)
	}
	if disabledErr.ErrorMessage != "API quota exceeded. Please try again later." {
		t.Errorf("Expected error message 'API quota exceeded. Please try again later.', got %s", disabledErr.ErrorMessage)
	}

	// Test disabling a non-existent tool (should still work)
	if err := registry.DisableToolTemporarily(
		"nonexistent_tool",
		DisableReasonMaintenance,
		"Under maintenance",
	); err != nil {
		t.Fatalf("Failed to disable non-existent tool: %v", err)
	}

	// Verify non-existent tool is also disabled
	_, err = registry.Execute("nonexistent_tool", nil)
	if err == nil {
		t.Fatal("Expected error when executing disabled non-existent tool")
	}
	if !errors.As(err, &disabledErr) {
		t.Errorf("Expected ToolDisabledError, got %T", err)
	}
}
