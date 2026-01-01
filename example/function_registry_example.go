package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"agentize"
	"agentize/engine"
	"agentize/fsrepo"
	"agentize/model"
	"agentize/store"
)

// This example demonstrates how to register Go functions for tools
func functionRegistryExample() {
	// Create a temporary knowledge tree
	examplePath := createFunctionRegistryExampleKnowledgeTree()
	defer os.RemoveAll(examplePath)

	// Create repository and session store
	repo, err := fsrepo.NewNodeRepository(examplePath)
	if err != nil {
		log.Fatalf("Failed to create repository: %v", err)
	}

	sessionStore := store.NewMemoryStore()

	// Create function registry and register all tool functions
	// This is the STANDARD way to register functions - do it at startup
	functionRegistry := model.NewFunctionRegistry()

	// Register functions for all tools that will be used
	// These functions must match the tool names defined in tools.json files
	functionRegistry.MustRegister("search_docs", searchDocsFunction)
	functionRegistry.MustRegister("query_database", queryDatabaseFunction)
	functionRegistry.MustRegister("process_data", processDataFunction)

	// Optional: Validate that all tools have functions
	// This is recommended to catch missing functions early
	ag, err := agentize.New(examplePath)
	if err != nil {
		log.Fatalf("Failed to create Agentize: %v", err)
	}

	// Get all tools from the knowledge tree
	allNodes := ag.GetAllNodes()
	toolRegistry := model.NewToolRegistry(model.MergeStrategyOverride)
	for _, node := range allNodes {
		toolRegistry.AddTools(node.Tools)
	}

	// Validate that all active tools have functions
	if err := functionRegistry.ValidateAllTools(toolRegistry); err != nil {
		log.Printf("Warning: Some tools are missing functions: %v", err)
		// In production, you might want to fail here or log it
	}

	fmt.Println("=== Function Registry Example ===")
	fmt.Println("All tool functions registered successfully!")
	fmt.Printf("Registered functions: %v\n", functionRegistry.GetAllRegistered())

	// Create engine with function registry
	eng := engine.NewEngineWithFunctions(
		repo,
		sessionStore,
		model.MergeStrategyOverride,
		functionRegistry,
	)

	// Now you can use the engine and it will execute the registered functions
	// when tools are called by the LLM
	fmt.Println("\nEngine is ready to use with registered functions!")
	fmt.Printf("Engine created with %d registered functions\n", len(functionRegistry.GetAllRegistered()))

	// Example: Start a session
	session, err := eng.StartSession("user123")
	if err != nil {
		log.Printf("Failed to start session: %v", err)
	} else {
		fmt.Printf("Session started: %s\n", session.SessionID)
	}

	_ = eng // Use engine
}

// Example tool functions
// These functions implement the ToolFunction signature: func(args map[string]interface{}) (string, error)

func searchDocsFunction(args map[string]interface{}) (string, error) {
	query, ok := args["q"].(string)
	if !ok {
		return "", fmt.Errorf("missing or invalid 'q' parameter")
	}

	// Simulate search
	result := fmt.Sprintf("Found 5 documents matching '%s'", query)
	return result, nil
}

func queryDatabaseFunction(args map[string]interface{}) (string, error) {
	sql, ok := args["sql"].(string)
	if !ok {
		return "", fmt.Errorf("missing or invalid 'sql' parameter")
	}

	// Simulate database query
	result := fmt.Sprintf("Query executed: %s\nResults: 10 rows returned", sql)
	return result, nil
}

func processDataFunction(args map[string]interface{}) (string, error) {
	data, ok := args["data"].(string)
	if !ok {
		return "", fmt.Errorf("missing or invalid 'data' parameter")
	}

	// Simulate data processing
	result := fmt.Sprintf("Processed data: %s\nStatus: Success", data)
	return result, nil
}

func createFunctionRegistryExampleKnowledgeTree() string {
	tmpDir, err := os.MkdirTemp("", "agentize-function-registry-*")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create root node
	rootPath := filepath.Join(tmpDir, "root")
	os.MkdirAll(rootPath, 0755)

	yamlContent := `id: "root"
title: "Root Node"
description: "Root node with tools"
policy:
  can_advance: true
  advance_condition: "proceed"
  max_open_files: 20
routing:
  mode: "sequential"
`
	os.WriteFile(filepath.Join(rootPath, "node.yaml"), []byte(yamlContent), 0644)

	mdContent := `# Root Node

This node has tools that require Go functions.
`
	os.WriteFile(filepath.Join(rootPath, "node.md"), []byte(mdContent), 0644)

	toolsContent := `[
  {
    "name": "search_docs",
    "description": "Search in documentation",
    "input_schema": {
      "type": "object",
      "properties": {
        "q": {
          "type": "string",
          "description": "Search query"
        }
      },
      "required": ["q"]
    }
  },
  {
    "name": "query_database",
    "description": "Query the database",
    "input_schema": {
      "type": "object",
      "properties": {
        "sql": {
          "type": "string",
          "description": "SQL query"
        }
      },
      "required": ["sql"]
    }
  }
]
`
	os.WriteFile(filepath.Join(rootPath, "tools.json"), []byte(toolsContent), 0644)

	// Create child node
	childPath := filepath.Join(rootPath, "child")
	os.MkdirAll(childPath, 0755)

	childYaml := `id: "child"
title: "Child Node"
description: "Child node with additional tools"
policy:
  can_advance: false
  max_open_files: 15
routing:
  mode: "sequential"
`
	os.WriteFile(filepath.Join(childPath, "node.yaml"), []byte(childYaml), 0644)

	childMd := `# Child Node

This node has additional tools.
`
	os.WriteFile(filepath.Join(childPath, "node.md"), []byte(childMd), 0644)

	childTools := `[
  {
    "name": "process_data",
    "description": "Process data",
    "input_schema": {
      "type": "object",
      "properties": {
        "data": {
          "type": "string",
          "description": "Data to process"
        }
      },
      "required": ["data"]
    }
  }
]
`
	os.WriteFile(filepath.Join(childPath, "tools.json"), []byte(childTools), 0644)

	return tmpDir
}
