package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ghiac/agentize"
)

// This example demonstrates how to generate a graph visualization of the knowledge tree
func graphExample() {
	// Create a temporary knowledge tree for demonstration
	examplePath := createGraphExampleKnowledgeTree()
	defer os.RemoveAll(examplePath)

	// Create Agentize instance
	ag, err := agentize.New(examplePath)
	if err != nil {
		log.Fatalf("Failed to create Agentize: %v", err)
	}

	fmt.Println("=== Generating Graph Visualization ===")
	fmt.Printf("Loaded %d nodes\n", len(ag.GetAllNodes()))

	// Generate graph visualization
	outputFile := "knowledge_tree_graph.html"
	if err := ag.GenerateGraphVisualization(outputFile, "Knowledge Tree Visualization"); err != nil {
		log.Fatalf("Failed to generate graph: %v", err)
	}

	fmt.Printf("Graph saved to: %s\n", outputFile)
	fmt.Println("Open the HTML file in your browser to view the graph!")
}

func createGraphExampleKnowledgeTree() string {
	tmpDir, err := os.MkdirTemp("", "agentize-graph-example-*")
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

This is the root node of the knowledge tree.
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
    },
    {
      "name": "query",
      "description": "Query data",
      "input_schema": {
        "type": "object",
        "properties": {
          "sql": {
            "type": "string"
          }
        },
        "required": ["sql"]
      }
    }
  ]
}
`
	os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(toolsContent), 0644)

	// Create second level node
	nextPath := filepath.Join(rootPath, "next")
	os.MkdirAll(nextPath, 0755)

	nextYaml := `id: "second_level"
title: "Second Level"
description: "Second level node"
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
	os.WriteFile(filepath.Join(nextPath, "node.yaml"), []byte(nextYaml), 0644)

	nextMd := `# Second Level

Second level content.
`
	os.WriteFile(filepath.Join(nextPath, "node.md"), []byte(nextMd), 0644)

	nextTools := `{
  "tools": [
    {
      "name": "process_data",
      "description": "Process data",
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

	// Create third level node (leaf)
	thirdPath := filepath.Join(nextPath, "next")
	os.MkdirAll(thirdPath, 0755)

	thirdYaml := `id: "third_level"
title: "Third Level"
description: "Final level"
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
	os.WriteFile(filepath.Join(thirdPath, "node.yaml"), []byte(thirdYaml), 0644)

	thirdMd := `# Third Level

Final level content.
`
	os.WriteFile(filepath.Join(thirdPath, "node.md"), []byte(thirdMd), 0644)

	thirdTools := `{
  "tools": [
    {
      "name": "finalize",
      "description": "Finalize process",
      "input_schema": {
        "type": "object",
        "properties": {
          "result": {
            "type": "string"
          }
        },
        "required": ["result"]
      }
    },
    {
      "name": "analyze",
      "description": "Analyze results",
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
	os.WriteFile(filepath.Join(thirdPath, "tools.json"), []byte(thirdTools), 0644)

	return tmpDir
}
