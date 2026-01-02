# Agentize

> Build intelligent agentic services from hierarchical knowledge trees

Agentize is a powerful Go library for building agentic services that navigate through hierarchical knowledge structures. Transform your filesystem-based knowledge tree into an intelligent agent that manages sessions, accumulates tools, and makes context-aware decisions.

## ✨ Key Features

- 🌳 **Knowledge Tree Navigation** - Navigate through hierarchical knowledge structures with automatic node discovery
- 🔐 **RBAC Authentication** - Fine-grained access control with role-based permissions and inheritance
- 🛠️ **Tool Aggregation** - Accumulate tools from root to current node with configurable merge strategies
- 💬 **Session Management** - Thread-safe in-memory session store with per-user state management
- 🤖 **LLM Integration** - Optional LLM-based decision making (extensible with rule-based fallback)
- 📊 **Graph Visualization** - Generate interactive HTML graphs using ECharts
- 🌐 **HTTP Server** - Production-ready HTTP API with chat endpoint and graph visualization
- 🔌 **MCP Support** - Connect to Model Context Protocol servers
- ⚡ **Function Registry** - Register and execute tool functions dynamically

## 🚀 Quick Start

### Installation

```bash
go get agentize
```

### Basic Usage

```go
package main

import "agentize"

func main() {
    // Create Agentize instance - automatically loads all nodes
    ag, err := agentize.New("./knowledge")
    if err != nil {
        panic(err)
    }
    
    // Get root node
    root := ag.GetRoot()
    fmt.Printf("Root: %s - %s\n", root.Title, root.Description)
    
    // Generate graph visualization
    ag.GenerateGraphVisualization("graph.html", "Knowledge Tree")
}
```

### Run as HTTP Server

```bash
# Build and run
make build
./bin/agentize -knowledge ./knowledge

# Or with HTTP enabled
AGENTIZE_HTTP_ENABLED=true \
AGENTIZE_FEATURE_HTTP=true \
AGENTIZE_KNOWLEDGE_PATH=./knowledge \
./bin/agentize
```

## 📁 Knowledge Tree Structure

Organize your knowledge as a filesystem tree:

```
knowledge/
  root/
    node.md          # Markdown content/instructions
    node.yaml        # Node metadata, policy, and auth
    tools.json       # Tools available at this level
    next/            # Child nodes
      node.md
      node.yaml
      tools.json
```

### Node Configuration (`node.yaml`)

```yaml
id: "root"
title: "Root Node"
description: "Entry point for the knowledge tree"

# RBAC Authentication
auth:
  inherit: true
  default:
    perms: "r"  # Read-only by default
  roles:
    admin:
      perms: "rwx"  # Full access
  users:
    "user123":
      perms: "rw"  # Read + Write

# Routing configuration
routing:
  mode: "sequential"  # or "parallel", "conditional"

# Memory persistence
memory:
  persist: ["summary", "facts"]
```

### Tools Definition (`tools.json`)

```json
{
  "tools": [
    {
      "name": "search_docs",
      "description": "Search in documentation",
      "input_schema": {
        "type": "object",
        "properties": {
          "q": { "type": "string" }
        },
        "required": ["q"]
      }
    }
  ]
}
```

## 🎯 Use Cases

- **Multi-stage AI Agents** - Build agents that progress through knowledge stages
- **Documentation Assistants** - Create context-aware documentation helpers
- **Workflow Automation** - Navigate through complex workflows with tool accumulation
- **Educational Platforms** - Progressive learning systems with tool unlocking
- **API Gateways** - Intelligent routing based on knowledge trees

## 🔧 Advanced Features

### Session Management

```go
engine := engine.NewEngine(repo, sessionStore, model.MergeStrategyOverride)

// Start a new session
session, err := engine.StartSession("user123")

// Process user input
output, err := engine.Step(session.ID, "Hello, I want to proceed")

// Advance to next node
nextSession, err := engine.Advance(session.ID)
```

### Tool Function Registry

```go
registry := model.NewFunctionRegistry()

// Register a function
registry.Register("search_docs", func(args map[string]interface{}) (string, error) {
    query := args["q"].(string)
    // Perform search...
    return results, nil
})

// Use with engine
engine.SetFunctionRegistry(registry)
```

### LLM Integration

```go
llmHandler := engine.NewLLMHandler(openaiClient, "gpt-4")
engine.SetLLMHandler(llmHandler)

// Now Step() will use LLM for decision making
output, err := engine.Step(sessionID, userInput)
```

## 🌐 HTTP API

When HTTP server is enabled:

### POST `/chat`

```bash
curl -X POST http://localhost:8080/chat \
  -H "Content-Type: application/json" \
  -d '{
    "query": "Hello, I want to proceed",
    "userID": "user123"
  }'
```

**Response:**
```json
{
  "action": "respond",
  "message": "Processing input at node: Root Node",
  "current_node": "root",
  "opened_files": ["root/node.md", "root/node.yaml", "root/tools.json"]
}
```

### GET `/graph`

Returns an interactive HTML graph visualization of the knowledge tree.

### GET `/health`

Health check endpoint.

## 🏗️ Architecture

```
Agentize/
├── model/          # Core data structures (Node, Session, Tool, Auth)
├── fsrepo/         # Filesystem repository for loading nodes
├── engine/         # Agent engine with session management
├── store/          # Session storage (in-memory, extensible)
├── server/         # HTTP server and API handlers
├── visualize/      # Graph visualization with ECharts
└── documents/      # Document generation components
```

## 🧪 Testing

```bash
# Run comprehensive test suite
make test-full

# Simple tests
make test

# With verbose output
make test-verbose
```

## 📚 Examples

Check out the `example/` directory for:
- Graph visualization
- Function registry usage
- Session management
- Tool disabling/enabling
