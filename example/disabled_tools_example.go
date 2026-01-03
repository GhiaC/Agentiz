package main

import (
	"fmt"
	"log"

	"github.com/ghiac/agentize/model"
)

// This example demonstrates how to work with disabled and hidden tools
func disabledToolsExample() {
	registry := model.NewToolRegistry(model.MergeStrategyOverride)

	// Create an active tool
	activeTool := model.Tool{
		Name:        "search_docs",
		Description: "Search in documentation",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"q": map[string]interface{}{"type": "string"},
			},
		},
		Status: model.ToolStatusActive,
	}

	// Create a temporarily disabled tool
	disabledTool := model.Tool{
		Name:        "analyze_data",
		Description: "Analyze data",
		InputSchema: map[string]interface{}{},
	}
	disabledTool.SetTemporaryDisabled(model.DisableReasonMaintenance, "Service under maintenance until 2024-01-15")

	// Create a hidden tool
	hiddenTool := model.Tool{
		Name:        "internal_debug",
		Description: "Internal debugging tool",
		InputSchema: map[string]interface{}{},
	}
	hiddenTool.SetHidden()

	// Add all tools
	if err := registry.AddTool(activeTool); err != nil {
		log.Fatalf("Failed to add active tool: %v", err)
	}
	if err := registry.AddTool(disabledTool); err != nil {
		log.Fatalf("Failed to add disabled tool: %v", err)
	}
	if err := registry.AddTool(hiddenTool); err != nil {
		log.Fatalf("Failed to add hidden tool: %v", err)
	}

	// GetTools() excludes hidden tools
	fmt.Println("=== Tools (excluding hidden) ===")
	tools := registry.GetTools()
	for _, tool := range tools {
		fmt.Printf("- %s: %s (status: %s)\n", tool.Name, tool.Description, tool.Status)
		if tool.Status == model.ToolStatusTemporaryDisabled {
			fmt.Printf("  Reason: %s - %s\n", tool.DisableReason, tool.ErrorMessage)
		}
	}

	// GetToolsIncludingHidden() includes all tools
	fmt.Println("\n=== All Tools (including hidden) ===")
	allTools := registry.GetToolsIncludingHidden()
	for _, tool := range allTools {
		fmt.Printf("- %s: %s (status: %s)\n", tool.Name, tool.Description, tool.Status)
	}

	// Check if tools are usable
	fmt.Println("\n=== Tool Usability Check ===")
	testTools := []string{"search_docs", "analyze_data", "internal_debug", "nonexistent"}
	for _, toolName := range testTools {
		if registry.IsToolUsable(toolName) {
			fmt.Printf("✓ %s: Usable\n", toolName)
		} else {
			fmt.Printf("✗ %s: Not usable\n", toolName)
			if err := registry.CanUseTool(toolName); err != nil {
				fmt.Printf("  Error: %v\n", err)
			}
		}
	}

	// Try to use tools
	fmt.Println("\n=== Attempting to Use Tools ===")
	for _, toolName := range []string{"search_docs", "analyze_data"} {
		if err := registry.CanUseTool(toolName); err != nil {
			fmt.Printf("✗ Cannot use %s: %v\n", toolName, err)
		} else {
			fmt.Printf("✓ Can use %s\n", toolName)
		}
	}
}
