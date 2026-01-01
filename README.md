# Agentize

A Go library for building agentic services from a knowledge tree structure. Agentize reads a hierarchical filesystem-based knowledge tree and creates an agentic service that manages sessions, accumulates tools, and navigates through nodes.

## Features

- **Knowledge Tree Navigation**: Read and navigate through a hierarchical knowledge structure
- **Session Management**: Maintain separate state for each user/session
- **Tool Aggregation**: Accumulate tools from root to current node with configurable merge strategies
- **Rule-based Decision Making**: Built-in rule-based engine for node advancement (extensible with LLM)
- **In-memory Session Store**: Fast in-memory storage with thread-safe operations
- **Graph Visualization**: Generate interactive HTML graphs of your knowledge tree using ECharts
- **HTTP Server**: Optional HTTP API with chat endpoint and graph visualization

## Installation

```bash
go get agentize
```

## Quick Start

### As a Library

```go
package main

import "agentize"

func main() {
    ag, err := agentize.New("./knowledge")
    if err != nil {
        panic(err)
    }
    
    // Use the library...
}
```

### As a Server

```bash
# Enable HTTP server via environment variables
export AGENTIZE_HTTP_ENABLED=true
export AGENTIZE_FEATURE_HTTP=true
export AGENTIZE_KNOWLEDGE_PATH=./knowledge

# Run the server
go run cmd/agentize/main.go
```

Or build and run:

```bash
go build -o agentize cmd/agentize/main.go
./agentize -knowledge ./knowledge
```

## Configuration

Configuration is done via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENTIZE_KNOWLEDGE_PATH` | `./knowledge` | Path to knowledge tree directory |
| `AGENTIZE_HTTP_ENABLED` | `false` | Enable HTTP server |
| `AGENTIZE_HTTP_HOST` | `0.0.0.0` | HTTP server host |
| `AGENTIZE_HTTP_PORT` | `8080` | HTTP server port |
| `AGENTIZE_FEATURE_HTTP` | `false` | Feature flag for HTTP server |
| `AGENTIZE_FEATURE_GRAPH` | `true` | Feature flag for graph visualization |

**Note**: HTTP server requires both `AGENTIZE_HTTP_ENABLED=true` AND `AGENTIZE_FEATURE_HTTP=true` to be enabled.

## HTTP API

When HTTP server is enabled, the following endpoints are available:

### POST /chat

Chat endpoint for interacting with the agent.

**Request:**
```json
{
  "query": "Hello, I want to proceed",
  "userID": "user123"
}
```

**Response:**
```json
{
  "action": "respond",
  "message": "Processing input at node: Root Node\n...",
  "current_node": "root",
  "opened_files": ["root/node.md", "root/node.yaml", "root/tools.json"]
}
```

### GET /graph

Returns an interactive HTML graph visualization of the knowledge tree.

### GET /health

Health check endpoint.

**Response:**
```json
{
  "status": "ok"
}
```

## Knowledge Tree Structure

Your knowledge tree should follow this structure:

```
knowledge/
  root/
    node.md          # Markdown content/instructions
    node.yaml        # Node metadata and policy
    tools.json       # Tools available at this level
    next/            # Optional: next level
      node.md
      node.yaml
      tools.json
      next/
        ...
```

### File Formats

**node.yaml**:
```yaml
id: "root"
title: "Root Node"
description: "Description of this node"
policy:
  can_advance: true
  advance_condition: "proceed"  # Keyword to trigger advance
  max_open_files: 20
routing:
  mode: "sequential"
memory:
  persist: ["summary", "facts"]
```

**tools.json**:
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

## Usage Examples

### Library Usage

```go
package main

import (
    "agentize"
    "agentize/model"
)

func main() {
    // Create Agentize instance - automatically loads all nodes
    ag, err := agentize.New("./knowledge")
    if err != nil {
        panic(err)
    }

    // Get root node
    root := ag.GetRoot()
    fmt.Printf("Root: %s - %s\n", root.Title, root.Description)

    // Get all nodes
    allNodes := ag.GetAllNodes()
    fmt.Printf("Loaded %d nodes\n", len(allNodes))

    // Generate graph visualization
    err = ag.GenerateGraphVisualization("graph.html", "Knowledge Tree")
    if err != nil {
        panic(err)
    }
}
```

### HTTP Server Usage

```bash
# Start server
export AGENTIZE_HTTP_ENABLED=true
export AGENTIZE_FEATURE_HTTP=true
go run cmd/agentize/main.go

# Test chat endpoint
curl -X POST http://localhost:8080/chat \
  -H "Content-Type: application/json" \
  -d '{"query": "Hello", "userID": "user123"}'

# View graph
open http://localhost:8080/graph
```

## Makefile Commands

The project includes a Makefile with convenient commands:

| Command | Description |
|---------|-------------|
| `make build` | Build the agentize server binary |
| `make run` | Run the server (requires env vars) |
| `make run-server` | Run server with HTTP enabled |
| `make test-full` | Run comprehensive test suite (format, vet, tests, coverage) |
| `make test` | Run simple tests |
| `make test-verbose` | Run tests with verbose output |
| `make clean` | Remove build artifacts and coverage files |
| `make deps` | Install/update dependencies |

## Testing

Agentize includes a comprehensive test suite. You can run tests in several ways:

### Quick Test Commands

```bash
# Run comprehensive test suite (recommended)
# This runs: format check, vet, tests, and coverage
make test-full

# Run simple tests
make test

# Run tests with verbose output
make test-verbose

# Or use Go directly
go test ./...
go test -v ./...
```

### Test Script

The project includes a test script (`scripts/test.sh`) that performs:

1. **Dependency Check**: Verifies and updates Go modules
2. **Format Check**: Ensures code follows Go formatting standards
3. **Vet Check**: Runs `go vet` to catch common errors
4. **Test Execution**: Runs all tests with verbose output
5. **Coverage Report**: Generates test coverage statistics

You can run it directly:

```bash
bash scripts/test.sh
```

Or use the Makefile:

```bash
make test-full
```

### Test Coverage

To view detailed coverage report:

```bash
# Generate coverage file
go test -coverprofile=coverage.out ./...

# View HTML coverage report
go tool cover -html=coverage.out
```

## Architecture

- **model/**: Core data structures (Node, Session, Tool, Policy)
- **fsrepo/**: Filesystem repository for loading nodes
- **store/**: Session storage (in-memory implementation)
- **engine/**: Agent engine with session management and navigation
- **server/**: HTTP server and API handlers
- **config/**: Configuration management
- **visualize/**: Graph visualization using ECharts
- **cmd/agentize/**: Main executable
- **scripts/**: Utility scripts (test runner, etc.)

## MVP Status

This is MVP-1 implementation:
- ✅ Parse tree from filesystem
- ✅ Load a Node
- ✅ Compute accumulated tools from root to current
- ✅ Session in-memory
- ✅ Advance sequential
- ✅ Graph visualization
- ✅ HTTP server with chat endpoint

Future enhancements (MVP-2, MVP-3):
- Persistent session storage (Redis, PostgreSQL)
- LLM-based decision provider
- Tool execution sandbox
- WebSocket support for real-time chat

## License

[Add your license here]
