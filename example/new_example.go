package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ghiac/agentize"
	"github.com/ghiac/agentize/model"
)

// This example demonstrates the new Agentize.New() API
func newExample() {
	// Create a temporary knowledge tree for demonstration
	examplePath := createNewExampleKnowledgeTree()
	defer os.RemoveAll(examplePath)

	// Create Agentize instance - this automatically loads all nodes
	ag, err := agentize.New(examplePath)
	if err != nil {
		log.Fatalf("Failed to create Agentize: %v", err)
	}

	fmt.Println("=== Agentize Instance Created ===")
	fmt.Printf("Loaded %d nodes\n\n", len(ag.GetAllNodes()))

	// Get root node
	root := ag.GetRoot()
	if root != nil {
		fmt.Println("=== Root Node ===")
		fmt.Printf("Path: %s\n", root.Path)
		fmt.Printf("ID: %s\n", root.ID)
		fmt.Printf("Title: %s\n", root.Title)
		fmt.Printf("Description: %s\n", root.Description)
		fmt.Printf("Tools: %d\n", len(root.Tools))
		fmt.Println()
	}

	// Get all node paths
	paths := ag.GetNodePaths()
	fmt.Println("=== Node Paths (in order) ===")
	for i, path := range paths {
		fmt.Printf("%d. %s\n", i+1, path)
	}
	fmt.Println()

	// Get all nodes
	allNodes := ag.GetAllNodes()
	fmt.Println("=== All Nodes ===")
	for path, node := range allNodes {
		fmt.Printf("Path: %s\n", path)
		fmt.Printf("  Title: %s\n", node.Title)
		fmt.Printf("  Tools: %d\n", len(node.Tools))
		fmt.Printf("  Content length: %d chars\n", len(node.Content))
		fmt.Println()
	}

	// Get a specific node
	if len(paths) > 1 {
		node, err := ag.GetNode(paths[1])
		if err != nil {
			log.Printf("Failed to get node: %v", err)
		} else {
			fmt.Printf("=== Node: %s ===\n", paths[1])
			fmt.Printf("Title: %s\n", node.Title)
			fmt.Printf("Content preview: %s\n", node.Content[:minInt(50, len(node.Content))])
		}
	}

	// Example with options
	fmt.Println("\n=== Creating with Options ===")
	opts := &agentize.Options{
		ToolStrategy: model.MergeStrategyAppend,
	}
	ag2, err := agentize.NewWithOptions(examplePath, opts)
	if err != nil {
		log.Fatalf("Failed to create Agentize with options: %v", err)
	}
	fmt.Printf("Tool strategy: %s\n", ag2.GetToolStrategy())
}

func createNewExampleKnowledgeTree() string {
	tmpDir, err := os.MkdirTemp("", "agentize-example-*")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create root node
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0755)

	yamlContent := `id: "root"
title: "Root Node"
description: "This is the root of the knowledge tree"
auth:
  users:
    - user_id: "default"
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

	mdContent := `# Root Node

This is the root node of the knowledge tree. It contains initial instructions and context.
`
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte(mdContent), 0644)

	toolsContent := `{
  "tools": [
    {
      "name": "search_docs",
      "description": "Search in documentation",
      "input_schema": {
        "type": "object",
        "properties": {
          "q": {
            "type": "string"
          }
        },
        "required": ["q"]
      }
    }
  ]
}
`
	os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(toolsContent), 0644)

	// Create next node
	nextPath := filepath.Join(rootPath, "next")
	os.MkdirAll(nextPath, 0755)

	nextYaml := `id: "next"
title: "Next Node"
description: "Second level node"
auth:
  users:
    - user_id: "default"
      can_edit: true
      can_read: true
      can_access_next: false
      can_see: true
      visible_in_docs: true
      visible_in_graph: true
routing:
  mode: "sequential"
`
	os.WriteFile(filepath.Join(nextPath, "node.yaml"), []byte(nextYaml), 0644)

	nextMd := `# Next Node

This is the second level node.
`
	os.WriteFile(filepath.Join(nextPath, "node.md"), []byte(nextMd), 0644)

	nextTools := `{
  "tools": [
    {
      "name": "analyze_data",
      "description": "Analyze data",
      "input_schema": {
        "type": "object",
        "properties": {
          "data": {
            "type": "string"
          }
        },
        "required": ["data"]
      }
    }
  ]
}
`
	os.WriteFile(filepath.Join(nextPath, "tools.json"), []byte(nextTools), 0644)

	return tmpDir
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
