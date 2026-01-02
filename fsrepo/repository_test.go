package fsrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodeRepository(t *testing.T) {
	// Create temporary knowledge tree
	tmpDir, err := os.MkdirTemp("", "agentize-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create root node
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0755)

	// Create node.yaml
	yamlContent := `id: "root"
title: "Test Root"
description: "Test description"
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
	os.WriteFile(filepath.Join(rootPath, "node.yaml"), []byte(yamlContent), 0644)

	// Create node.md
	mdContent := "# Test Root\n\nThis is test content."
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte(mdContent), 0644)

	// Create tools.json
	toolsContent := `{
  "tools": [
    {
      "name": "test_tool",
      "description": "A test tool",
      "input_schema": {
        "type": "object",
        "properties": {
          "param": { "type": "string" }
        }
      }
    }
  ]
}
`
	os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(toolsContent), 0644)

	// Create repository
	repo, err := NewNodeRepository(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create repository: %v", err)
	}

	// Test LoadNode
	node, err := repo.LoadNode("root")
	if err != nil {
		t.Fatalf("Failed to load node: %v", err)
	}

	if node.ID != "root" {
		t.Errorf("Expected ID 'root', got '%s'", node.ID)
	}

	if node.Title != "Test Root" {
		t.Errorf("Expected title 'Test Root', got '%s'", node.Title)
	}

	if node.Content != mdContent {
		t.Errorf("Content mismatch")
	}

	if len(node.Tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(node.Tools))
	}

	if node.Tools[0].Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got '%s'", node.Tools[0].Name)
	}

	// Test HasNext (should be false)
	if repo.HasNext("root") {
		t.Error("Expected HasNext to be false")
	}

	// Create next node
	nextPath := filepath.Join(rootPath, "next")
	os.MkdirAll(nextPath, 0755)
	os.WriteFile(filepath.Join(nextPath, "node.yaml"), []byte(`id: "next"`), 0644)

	// Test HasNext (should be true now)
	if !repo.HasNext("root") {
		t.Error("Expected HasNext to be true")
	}

	// Test NextPath
	nextPathStr, ok := repo.NextPath("root")
	if !ok {
		t.Error("Expected NextPath to return true")
	}
	if nextPathStr != "root/next" {
		t.Errorf("Expected 'root/next', got '%s'", nextPathStr)
	}
}
