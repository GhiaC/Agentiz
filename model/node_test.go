package model

import "testing"

func TestToolRegistry(t *testing.T) {
	registry := NewToolRegistry(MergeStrategyOverride)

	tool1 := Tool{
		Name:        "test_tool",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}

	tool2 := Tool{
		Name:        "test_tool",
		Description: "Overridden tool",
		InputSchema: map[string]interface{}{},
	}

	// Add first tool
	if err := registry.AddTool(tool1); err != nil {
		t.Fatalf("Failed to add tool: %v", err)
	}

	// Add second tool with same name (should override)
	if err := registry.AddTool(tool2); err != nil {
		t.Fatalf("Failed to add tool: %v", err)
	}

	tools := registry.GetTools()
	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool, got %d", len(tools))
	}

	if tools[0].Description != "Overridden tool" {
		t.Errorf("Expected overridden description, got %s", tools[0].Description)
	}
}

func TestToolRegistryErrorStrategy(t *testing.T) {
	registry := NewToolRegistry(MergeStrategyError)

	tool1 := Tool{
		Name:        "test_tool",
		Description: "Test tool",
		InputSchema: map[string]interface{}{},
	}

	tool2 := Tool{
		Name:        "test_tool",
		Description: "Duplicate tool",
		InputSchema: map[string]interface{}{},
	}

	// Add first tool
	if err := registry.AddTool(tool1); err != nil {
		t.Fatalf("Failed to add tool: %v", err)
	}

	// Add second tool with same name (should error)
	err := registry.AddTool(tool2)
	if err == nil {
		t.Fatal("Expected error for duplicate tool name")
	}

	if _, ok := err.(*ToolConflictError); !ok {
		t.Errorf("Expected ToolConflictError, got %T", err)
	}
}
