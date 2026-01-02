package agentize

import (
	"os"
	"path/filepath"
	"testing"

	"agentize/model"
)

// TestFullKnowledgeTreeIntegration tests the complete Agentize functionality
// with a realistic multi-level knowledge tree
func TestFullKnowledgeTreeIntegration(t *testing.T) {
	// Create a complete knowledge tree
	knowledgePath := createFullKnowledgeTree(t)
	defer os.RemoveAll(knowledgePath)

	t.Run("Create Agentize instance", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize: %v", err)
		}

		// Verify root node
		root := ag.GetRoot()
		if root == nil {
			t.Fatal("Root node should not be nil")
		}
		if root.Path != "root" {
			t.Errorf("Expected root path 'root', got '%s'", root.Path)
		}
		if root.Title != "Main Entry Point" {
			t.Errorf("Expected root title 'Main Entry Point', got '%s'", root.Title)
		}
		if len(root.Tools) != 2 {
			t.Errorf("Expected 2 tools in root, got %d", len(root.Tools))
		}
	})

	t.Run("Load all nodes", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize: %v", err)
		}

		allNodes := ag.GetAllNodes()
		expectedNodeCount := 4 // root, next, next/next, next/next/next
		if len(allNodes) != expectedNodeCount {
			t.Errorf("Expected %d nodes, got %d", expectedNodeCount, len(allNodes))
		}

		// Verify all expected paths exist
		expectedPaths := []string{
			"root",
			"root/next",
			"root/next/next",
			"root/next/next/next",
		}

		for _, path := range expectedPaths {
			if _, exists := allNodes[path]; !exists {
				t.Errorf("Expected node '%s' not found", path)
			}
		}
	})

	t.Run("Get node paths in order", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize: %v", err)
		}

		paths := ag.GetNodePaths()
		expectedPaths := []string{
			"root",
			"root/next",
			"root/next/next",
			"root/next/next/next",
		}

		if len(paths) != len(expectedPaths) {
			t.Fatalf("Expected %d paths, got %d", len(expectedPaths), len(paths))
		}

		for i, expected := range expectedPaths {
			if paths[i] != expected {
				t.Errorf("Path[%d]: expected '%s', got '%s'", i, expected, paths[i])
			}
		}
	})

	t.Run("Verify node content", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize: %v", err)
		}

		// Test root node
		root, err := ag.GetNode("root")
		if err != nil {
			t.Fatalf("Failed to get root node: %v", err)
		}
		if root.Description != "This is the main entry point" {
			t.Errorf("Root description mismatch: got '%s'", root.Description)
		}
		if !root.CanUserAccessNextSimple("test") {
			t.Error("Root should allow advance for test user")
		}

		// Test second level node
		next, err := ag.GetNode("root/next")
		if err != nil {
			t.Fatalf("Failed to get next node: %v", err)
		}
		if next.Title != "Second Level" {
			t.Errorf("Expected title 'Second Level', got '%s'", next.Title)
		}
		if len(next.Tools) != 1 {
			t.Errorf("Expected 1 tool in next node, got %d", len(next.Tools))
		}
		if next.Tools[0].Name != "process_data" {
			t.Errorf("Expected tool 'process_data', got '%s'", next.Tools[0].Name)
		}

		// Test third level node
		third, err := ag.GetNode("root/next/next")
		if err != nil {
			t.Fatalf("Failed to get third node: %v", err)
		}
		if third.Title != "Third Level" {
			t.Errorf("Expected title 'Third Level', got '%s'", third.Title)
		}
		if third.CanUserAccessNextSimple("test") {
			t.Error("Third level should not allow advance for test user")
		}
		if len(third.Tools) != 2 {
			t.Errorf("Expected 2 tools in third node, got %d", len(third.Tools))
		}

		// Test fourth level node (leaf)
		fourth, err := ag.GetNode("root/next/next/next")
		if err != nil {
			t.Fatalf("Failed to get fourth node: %v", err)
		}
		if fourth.Title != "Final Level" {
			t.Errorf("Expected title 'Final Level', got '%s'", fourth.Title)
		}
		if len(fourth.Content) == 0 {
			t.Error("Fourth node should have content")
		}
	})

	t.Run("Verify tools aggregation", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize: %v", err)
		}

		// Check tools at each level
		root := ag.GetRoot()
		if len(root.Tools) != 2 {
			t.Errorf("Root should have 2 tools, got %d", len(root.Tools))
		}

		// Verify tool names
		toolNames := make(map[string]bool)
		for _, tool := range root.Tools {
			toolNames[tool.Name] = true
		}
		if !toolNames["search"] {
			t.Error("Root should have 'search' tool")
		}
		if !toolNames["query"] {
			t.Error("Root should have 'query' tool")
		}
	})

	t.Run("Test reload functionality", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize: %v", err)
		}

		initialCount := len(ag.GetAllNodes())

		// Reload
		if err := ag.Reload(); err != nil {
			t.Fatalf("Failed to reload: %v", err)
		}

		reloadedCount := len(ag.GetAllNodes())
		if reloadedCount != initialCount {
			t.Errorf("Node count changed after reload: %d -> %d", initialCount, reloadedCount)
		}

		// Verify root still exists
		root := ag.GetRoot()
		if root == nil {
			t.Fatal("Root should still exist after reload")
		}
	})

	t.Run("Test reload specific node", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize: %v", err)
		}

		// Reload a specific node
		if err := ag.ReloadNode("root/next"); err != nil {
			t.Fatalf("Failed to reload node: %v", err)
		}

		// Verify node still exists
		node, err := ag.GetNode("root/next")
		if err != nil {
			t.Fatalf("Failed to get reloaded node: %v", err)
		}
		if node.Title != "Second Level" {
			t.Errorf("Node title changed after reload: got '%s'", node.Title)
		}
	})

	t.Run("Test with options", func(t *testing.T) {
		opts := &Options{
			ToolStrategy: model.MergeStrategyAppend,
		}

		ag, err := NewWithOptions(knowledgePath, opts)
		if err != nil {
			t.Fatalf("Failed to create Agentize with options: %v", err)
		}

		if ag.GetToolStrategy() != model.MergeStrategyAppend {
			t.Errorf("Expected tool strategy 'append', got '%s'", ag.GetToolStrategy())
		}

		// Verify nodes still loaded
		if len(ag.GetAllNodes()) == 0 {
			t.Fatal("Nodes should be loaded with options")
		}
	})

	t.Run("Test disabled tools", func(t *testing.T) {
		ag, err := New(knowledgePath)
		if err != nil {
			t.Fatalf("Failed to create Agentize: %v", err)
		}

		// Get third level node which has a disabled tool
		third, err := ag.GetNode("root/next/next")
		if err != nil {
			t.Fatalf("Failed to get third node: %v", err)
		}

		if len(third.Tools) != 2 {
			t.Fatalf("Expected 2 tools in third node, got %d", len(third.Tools))
		}

		// Find the disabled tool
		var disabledTool *model.Tool
		for i := range third.Tools {
			if third.Tools[i].Status == model.ToolStatusTemporaryDisabled {
				disabledTool = &third.Tools[i]
				break
			}
		}

		if disabledTool == nil {
			t.Fatal("Expected to find a disabled tool in third node")
		}

		if disabledTool.Name != "analyze" {
			t.Errorf("Expected disabled tool name 'analyze', got '%s'", disabledTool.Name)
		}

		if disabledTool.DisableReason != model.DisableReasonMaintenance {
			t.Errorf("Expected disable reason 'maintenance', got '%s'", disabledTool.DisableReason)
		}

		if disabledTool.ErrorMessage == "" {
			t.Error("Disabled tool should have an error message")
		}

		// Verify the active tool
		var activeTool *model.Tool
		for i := range third.Tools {
			if third.Tools[i].Status == model.ToolStatusActive {
				activeTool = &third.Tools[i]
				break
			}
		}

		if activeTool == nil {
			t.Fatal("Expected to find an active tool in third node")
		}

		if activeTool.Name != "optimize" {
			t.Errorf("Expected active tool name 'optimize', got '%s'", activeTool.Name)
		}
	})
}

// createFullKnowledgeTree creates a complete multi-level knowledge tree for testing
func createFullKnowledgeTree(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "agentize-full-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// ===== ROOT NODE =====
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0755)

	rootYAML := `id: "root"
title: "Main Entry Point"
description: "This is the main entry point"
auth:
  users:
    - user_id: "test"
      can_edit: true
      can_read: true
      can_access_next: true
      can_see: true
      visible_in_docs: true
      visible_in_graph: true
routing:
  mode: "sequential"
`
	os.WriteFile(filepath.Join(rootPath, "node.yaml"), []byte(rootYAML), 0644)

	rootMD := `# Main Entry Point

Welcome to the knowledge tree. This is where everything begins.

## Instructions

1. Start by understanding the context
2. Use available tools to gather information
3. Proceed to next level when ready

## Key Concepts

- **Context**: Understanding the problem domain
- **Tools**: Available functions to interact with the system
- **Navigation**: Moving through the knowledge tree
`
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte(rootMD), 0644)

	rootTools := `{
  "tools": [
    {
      "name": "search",
      "description": "Search through documentation and knowledge base",
      "input_schema": {
        "type": "object",
        "properties": {
          "query": {
            "type": "string",
            "description": "Search query"
          },
          "limit": {
            "type": "integer",
            "description": "Maximum number of results"
          }
        },
        "required": ["query"]
      }
    },
    {
      "name": "query",
      "description": "Query structured data",
      "input_schema": {
        "type": "object",
        "properties": {
          "sql": {
            "type": "string",
            "description": "SQL query string"
          }
        },
        "required": ["sql"]
      }
    }
  ]
}
`
	os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(rootTools), 0644)

	// ===== SECOND LEVEL NODE =====
	nextPath := filepath.Join(rootPath, "next")
	os.MkdirAll(nextPath, 0755)

	nextYAML := `id: "second_level"
title: "Second Level"
description: "Second level of the knowledge tree"
auth:
  users:
    - user_id: "test"
      can_edit: true
      can_read: true
      can_access_next: true
      can_see: true
      visible_in_docs: true
      visible_in_graph: true
routing:
  mode: "sequential"
`
	os.WriteFile(filepath.Join(nextPath, "node.yaml"), []byte(nextYAML), 0644)

	nextMD := `# Second Level

You've progressed to the second level. Here you'll find more specific information.

## What's Next?

This level focuses on:
- Data processing
- Analysis techniques
- Intermediate concepts

Continue when you're ready to dive deeper.
`
	os.WriteFile(filepath.Join(nextPath, "node.md"), []byte(nextMD), 0644)

	nextTools := `{
  "tools": [
    {
      "name": "process_data",
      "description": "Process and transform data",
      "input_schema": {
        "type": "object",
        "properties": {
          "data": {
            "type": "string",
            "description": "Data to process"
          },
          "format": {
            "type": "string",
            "description": "Output format"
          }
        },
        "required": ["data"]
      }
    }
  ]
}
`
	os.WriteFile(filepath.Join(nextPath, "tools.json"), []byte(nextTools), 0644)

	// ===== THIRD LEVEL NODE =====
	thirdPath := filepath.Join(nextPath, "next")
	os.MkdirAll(thirdPath, 0755)

	thirdYAML := `id: "third_level"
title: "Third Level"
description: "Deep dive into advanced topics"
auth:
  users:
    - user_id: "test"
      can_edit: true
      can_read: true
      can_access_next: false
      can_see: true
      visible_in_docs: true
      visible_in_graph: true
routing:
  mode: "sequential"
`
	os.WriteFile(filepath.Join(thirdPath, "node.yaml"), []byte(thirdYAML), 0644)

	thirdMD := `# Third Level

Advanced concepts and deep knowledge.

## Advanced Topics

- Complex algorithms
- Advanced patterns
- Expert-level knowledge

This is the deepest level before the final stage.
`
	os.WriteFile(filepath.Join(thirdPath, "node.md"), []byte(thirdMD), 0644)

	thirdTools := `{
  "tools": [
    {
      "name": "analyze",
      "description": "Perform deep analysis",
      "input_schema": {
        "type": "object",
        "properties": {
          "target": {
            "type": "string"
          }
        },
        "required": ["target"]
      },
      "status": "temporary_disabled",
      "disable_reason": "maintenance",
      "error_message": "Analysis service is under maintenance until 2024-02-01"
    },
    {
      "name": "optimize",
      "description": "Optimize performance",
      "input_schema": {
        "type": "object",
        "properties": {
          "config": {
            "type": "object"
          }
        },
        "required": ["config"]
      },
      "status": "active"
    }
  ]
}
`
	os.WriteFile(filepath.Join(thirdPath, "tools.json"), []byte(thirdTools), 0644)

	// ===== FOURTH LEVEL NODE (FINAL) =====
	fourthPath := filepath.Join(thirdPath, "next")
	os.MkdirAll(fourthPath, 0755)

	fourthYAML := `id: "final_level"
title: "Final Level"
description: "The final destination"
auth:
  users:
    - user_id: "test"
      can_edit: true
      can_read: true
      can_access_next: false
      can_see: true
      visible_in_docs: true
      visible_in_graph: true
routing:
  mode: "sequential"
`
	os.WriteFile(filepath.Join(fourthPath, "node.yaml"), []byte(fourthYAML), 0644)

	fourthMD := `# Final Level

Congratulations! You've reached the final level of the knowledge tree.

## Summary

This represents the culmination of your journey through the knowledge tree.

## Key Takeaways

- Understanding the structure
- Using tools effectively
- Navigating through levels

You've mastered the knowledge tree!
`
	os.WriteFile(filepath.Join(fourthPath, "node.md"), []byte(fourthMD), 0644)

	fourthTools := `{
  "tools": [
    {
      "name": "finalize",
      "description": "Finalize the process",
      "input_schema": {
        "type": "object",
        "properties": {
          "result": {
            "type": "string"
          }
        },
        "required": ["result"]
      }
    }
  ]
}
`
	os.WriteFile(filepath.Join(fourthPath, "tools.json"), []byte(fourthTools), 0644)

	return tmpDir
}
