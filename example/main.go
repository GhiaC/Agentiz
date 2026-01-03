package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

func main() {
	// Example usage of the agentize library

	// 1. Create a knowledge tree structure (for demonstration)
	// In real usage, this would already exist on disk
	examplePath := createExampleKnowledgeTree()
	defer os.RemoveAll(examplePath)

	// 2. Create repository
	repo, err := fsrepo.NewNodeRepository(examplePath)
	if err != nil {
		log.Fatalf("Failed to create repository: %v", err)
	}

	// 3. Create session store
	sessionStore := store.NewMemoryStore()

	// 4. Create engine
	eng := engine.NewEngine(repo, sessionStore, model.MergeStrategyOverride)

	// 5. Start a session
	session, err := eng.StartSession("user123")
	if err != nil {
		log.Fatalf("Failed to start session: %v", err)
	}

	fmt.Printf("Started session: %s\n", session.SessionID)
	fmt.Printf("Current node: %s\n", session.CurrentNodePath)
	fmt.Printf("Accumulated tools: %d\n", len(session.AccumulatedTools))

	// 6. Get context
	context, err := eng.GetContext(session.SessionID)
	if err != nil {
		log.Fatalf("Failed to get context: %v", err)
	}

	fmt.Printf("\nCurrent Node Title: %s\n", context.CurrentNode.Title)
	fmt.Printf("Current Node Description: %s\n", context.CurrentNode.Description)
	fmt.Printf("Content preview: %s\n", context.CurrentNode.Content[:min(50, len(context.CurrentNode.Content))])

	// 7. Step (process user input)
	output, err := eng.Step(session.SessionID, "Hello, I want to proceed")
	if err != nil {
		log.Fatalf("Failed to step: %v", err)
	}

	fmt.Printf("\nStep Output:\n")
	fmt.Printf("  Action: %s\n", output.Action)
	fmt.Printf("  Message: %s\n", output.Message)

	// 8. Advance to next node
	if repo.HasNext(session.CurrentNodePath) {
		updatedSession, err := eng.Advance(session.SessionID)
		if err != nil {
			log.Fatalf("Failed to advance: %v", err)
		}

		fmt.Printf("\nAdvanced to: %s\n", updatedSession.CurrentNodePath)
		fmt.Printf("Total tools after advance: %d\n", len(updatedSession.AccumulatedTools))
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// createExampleKnowledgeTree creates a temporary knowledge tree for demonstration
func createExampleKnowledgeTree() string {
	tmpDir, err := os.MkdirTemp("", "agentize-example-*")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create root node
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0755)

	// root/node.yaml
	yamlContent := `id: "root"
title: "Root Node"
description: "This is the root of the knowledge tree"
auth:
  users:
    - user_id: "user123"
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

	// root/node.md
	mdContent := `# Root Node

This is the root node of the knowledge tree. It contains initial instructions and context.
`
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte(mdContent), 0644)

	// root/tools.json
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

	// next/node.yaml
	nextYaml := `id: "next"
title: "Next Node"
description: "Second level node"
auth:
  users:
    - user_id: "user123"
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

	// next/node.md
	nextMd := `# Next Node

This is the second level node.
`
	os.WriteFile(filepath.Join(nextPath, "node.md"), []byte(nextMd), 0644)

	// next/tools.json
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
