package agentize

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghiac/agentize/model"
)

func TestNew(t *testing.T) {
	// Create temporary knowledge tree
	tmpDir := createTestKnowledgeTree(t)
	defer os.RemoveAll(tmpDir)

	// Create Agentize instance
	ag, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create Agentize: %v", err)
	}

	// Check root node
	root := ag.GetRoot()
	if root == nil {
		t.Fatal("Root node should not be nil")
	}
	if root.Path != "root" {
		t.Errorf("Expected root path 'root', got '%s'", root.Path)
	}

	// Check all nodes are loaded
	allNodes := ag.GetAllNodes()
	if len(allNodes) < 1 {
		t.Fatal("Should have at least root node loaded")
	}

	// Check node paths
	paths := ag.GetNodePaths()
	if len(paths) < 1 {
		t.Fatal("Should have at least one path")
	}
	if paths[0] != "root" {
		t.Errorf("First path should be 'root', got '%s'", paths[0])
	}
}

func TestNewWithOptions(t *testing.T) {
	tmpDir := createTestKnowledgeTree(t)
	defer os.RemoveAll(tmpDir)

	opts := &Options{
		ToolStrategy: model.MergeStrategyAppend,
	}

	ag, err := NewWithOptions(tmpDir, opts)
	if err != nil {
		t.Fatalf("Failed to create Agentize with options: %v", err)
	}

	if ag.GetToolStrategy() != model.MergeStrategyAppend {
		t.Errorf("Expected tool strategy 'append', got '%s'", ag.GetToolStrategy())
	}
}

func TestGetNode(t *testing.T) {
	tmpDir := createTestKnowledgeTree(t)
	defer os.RemoveAll(tmpDir)

	ag, err := New(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create Agentize: %v", err)
	}

	// Get root node
	node, err := ag.GetNode("root")
	if err != nil {
		t.Fatalf("Failed to get root node: %v", err)
	}
	if node.Path != "root" {
		t.Errorf("Expected path 'root', got '%s'", node.Path)
	}

	// Try to get non-existent node
	_, err = ag.GetNode("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent node")
	}
}

func TestReload(t *testing.T) {
	tmpDir := createTestKnowledgeTree(t)
	defer os.RemoveAll(tmpDir)

	ag, err := New(tmpDir)
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
		t.Errorf("Node count should remain the same after reload, got %d vs %d", reloadedCount, initialCount)
	}
}

func createTestKnowledgeTree(t *testing.T) string {
	tmpDir, err := os.MkdirTemp("", "agentize-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create root node
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0755)

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
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte("# Root\n\nRoot content."), 0644)
	os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(`{"tools": []}`), 0644)

	// Create next node
	nextPath := filepath.Join(rootPath, "next")
	os.MkdirAll(nextPath, 0755)
	os.WriteFile(filepath.Join(nextPath, "node.yaml"), []byte(`id: "next"`), 0644)
	os.WriteFile(filepath.Join(nextPath, "node.md"), []byte("# Next\n\nNext content."), 0644)

	return tmpDir
}
