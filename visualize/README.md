# Graph Visualization

This package provides graph visualization capabilities for knowledge trees using [go-echarts](https://github.com/go-echarts/go-echarts).

## Installation

First, install the required dependency:

```bash
go get github.com/go-echarts/go-echarts/v2
```

Or add it to your `go.mod`:

```bash
go mod tidy
```

## Usage

```go
package main

import (
    "github.com/ghiac/agentize"
)

func main() {
    // Create Agentize instance
    ag, err := agentize.New("./knowledge")
    if err != nil {
        panic(err)
    }

    // Generate graph visualization
    err = ag.GenerateGraphVisualization("graph.html", "My Knowledge Tree")
    if err != nil {
        panic(err)
    }
}
```

## Features

- **Interactive Graph**: Drag nodes, zoom in/out
- **Click to View Details**: Click on any node to see detailed information in a modal popup, including:
  - Full path and description
  - Policy settings (can advance, routing mode, memory settings)
  - List of all tools with descriptions
  - Full markdown content
- **Color Coding**: 
  - Blue: Root node
  - Green: Intermediate nodes
  - Yellow: Leaf nodes (cannot advance)
  - Red: Nodes with many tools (3+)
- **Node Sizing**: Based on number of tools
- **Tooltips**: Show node details on hover
- **Force Layout**: Automatic positioning with physics simulation

## Output

The visualization is saved as an HTML file that can be opened in any web browser.

