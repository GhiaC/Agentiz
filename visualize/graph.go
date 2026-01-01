package visualize

import (
	"fmt"
	"os"
	"strings"

	"github.com/go-echarts/go-echarts/v2/charts"
	"github.com/go-echarts/go-echarts/v2/components"
	"github.com/go-echarts/go-echarts/v2/opts"

	"agentize/model"
)

// GraphVisualizer creates graph visualizations of knowledge trees
type GraphVisualizer struct {
	nodes map[string]*model.Node
}

// NewGraphVisualizer creates a new graph visualizer
func NewGraphVisualizer(nodes map[string]*model.Node) *GraphVisualizer {
	return &GraphVisualizer{
		nodes: nodes,
	}
}

// GenerateGraph creates an ECharts graph from the knowledge tree
func (gv *GraphVisualizer) GenerateGraph(title string) *charts.Graph {
	graph := charts.NewGraph()
	graph.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{
			Title:    title,
			Subtitle: fmt.Sprintf("Knowledge Tree with %d nodes", len(gv.nodes)),
		}),
		charts.WithInitializationOpts(opts.Initialization{
			Width:  "1200px",
			Height: "800px",
		}),
		charts.WithTooltipOpts(opts.Tooltip{
			Show: opts.Bool(true),
		}),
		charts.WithLegendOpts(opts.Legend{
			Show: opts.Bool(true),
		}),
	)

	// Convert nodes to graph nodes
	graphNodes := gv.convertNodes()

	// Create links between nodes
	links := gv.createLinks()

	// Add series
	graph.AddSeries("knowledge-tree", graphNodes, links,
		charts.WithGraphChartOpts(opts.GraphChart{
			Layout: "force",
			Roam:   opts.Bool(true),
			Force: &opts.GraphForce{
				Repulsion:  1000,
				Gravity:    0.1,
				EdgeLength: 200,
			},
			Categories: gv.createCategories(),
		}),
		charts.WithLabelOpts(opts.Label{
			Show: opts.Bool(true),
		}),
		charts.WithLineStyleOpts(opts.LineStyle{
			Curveness: 0.3,
			Width:     2,
		}),
	)

	return graph
}

// getNodeName extracts the node name from a path (last part after "/")
func (gv *GraphVisualizer) getNodeName(path string) string {
	if path == "root" {
		return "root"
	}
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

// convertNodes converts model.Node to opts.GraphNode
func (gv *GraphVisualizer) convertNodes() []opts.GraphNode {
	graphNodes := make([]opts.GraphNode, 0, len(gv.nodes))

	for path, node := range gv.nodes {
		// Determine node category based on properties
		category := gv.getNodeCategory(node)

		// Extract node name from path (just the last part)
		nodeName := gv.getNodeName(path)

		// Create label with title and tool count
		label := node.Title
		if label == "" {
			label = node.ID
		}
		if label == "" {
			label = nodeName
		}

		// Add tool count to label
		toolCount := len(node.Tools)
		if toolCount > 0 {
			label = fmt.Sprintf("%s\n(%d tools)", label, toolCount)
		}

		// Create tooltip with more details
		tooltip := fmt.Sprintf("Path: %s\nTitle: %s\nDescription: %s\nTools: %d",
			path, node.Title, node.Description, toolCount)

		if !node.Policy.CanAdvance {
			tooltip += "\n⚠ Cannot advance"
		}

		graphNode := opts.GraphNode{
			Name:       nodeName,               // Use node name instead of full path
			Value:      float32(toolCount + 1), // Size based on tool count
			Category:   category,
			SymbolSize: gv.calculateNodeSize(node),
			ItemStyle:  gv.getNodeStyle(category),
		}

		graphNodes = append(graphNodes, graphNode)
	}

	return graphNodes
}

// createLinks creates links between nodes based on parent-child relationships
func (gv *GraphVisualizer) createLinks() []opts.GraphLink {
	links := make([]opts.GraphLink, 0)

	for path := range gv.nodes {
		// Find all direct children of this node
		// A child path is path + "/" + childName where childName doesn't contain "/"
		for childPath := range gv.nodes {
			if strings.HasPrefix(childPath, path+"/") {
				remaining := strings.TrimPrefix(childPath, path+"/")
				// Check if this is a direct child (no more slashes)
				if !strings.Contains(remaining, "/") {
					// Use node names instead of full paths for Source and Target
					sourceName := gv.getNodeName(path)
					targetName := gv.getNodeName(childPath)
					links = append(links, opts.GraphLink{
						Source: sourceName,
						Target: targetName,
						Value:  1,
						LineStyle: &opts.LineStyle{
							Width:     2,
							Curveness: 0.3,
						},
					})
				}
			}
		}
	}

	return links
}

// getNextPath determines child paths for a given node path
// This is a helper that tries to infer child paths from the nodes map
func (gv *GraphVisualizer) getNextPath(path string) string {
	// Try to find children by checking if any node path starts with path + "/"
	for nodePath := range gv.nodes {
		if strings.HasPrefix(nodePath, path+"/") {
			// Extract the immediate child name
			remaining := strings.TrimPrefix(nodePath, path+"/")
			if !strings.Contains(remaining, "/") {
				// This is a direct child
				if path == "root" {
					return "root/" + remaining
				}
				return path + "/" + remaining
			}
		}
	}
	return ""
}

// getNodeCategory determines the category of a node for styling
func (gv *GraphVisualizer) getNodeCategory(n *model.Node) int {
	// Category 0: Root node
	// Category 1: Intermediate nodes
	// Category 2: Leaf nodes (cannot advance)
	// Category 3: Nodes with many tools

	if n.Path == "root" {
		return 0
	}

	if !n.Policy.CanAdvance {
		return 2
	}

	if len(n.Tools) >= 3 {
		return 3
	}

	return 1
}

// createCategories creates category definitions for the graph
func (gv *GraphVisualizer) createCategories() []*opts.GraphCategory {
	return []*opts.GraphCategory{
		{
			Name: "Root",
			ItemStyle: &opts.ItemStyle{
				Color: "#5470c6", // Blue
			},
		},
		{
			Name: "Intermediate",
			ItemStyle: &opts.ItemStyle{
				Color: "#91cc75", // Green
			},
		},
		{
			Name: "Leaf",
			ItemStyle: &opts.ItemStyle{
				Color: "#fac858", // Yellow
			},
		},
		{
			Name: "Tool Rich",
			ItemStyle: &opts.ItemStyle{
				Color: "#ee6666", // Red
			},
		},
	}
}

// calculateNodeSize calculates the size of a node based on its properties
func (gv *GraphVisualizer) calculateNodeSize(node *model.Node) float32 {
	baseSize := 30.0
	toolSize := float32(len(node.Tools)) * 5.0

	// Root node is larger
	if node.Path == "root" {
		return float32(baseSize) + toolSize + 20
	}

	// Leaf nodes are slightly smaller
	if !node.Policy.CanAdvance {
		return float32(baseSize) + toolSize
	}

	return float32(baseSize) + toolSize + 10
}

// getNodeStyle returns the style for a node based on its category
func (gv *GraphVisualizer) getNodeStyle(category int) *opts.ItemStyle {
	colors := []string{
		"#5470c6", // Root - Blue
		"#91cc75", // Intermediate - Green
		"#fac858", // Leaf - Yellow
		"#ee6666", // Tool Rich - Red
	}

	if category >= len(colors) {
		category = 1
	}

	return &opts.ItemStyle{
		Color:       colors[category],
		BorderColor: "#fff",
		BorderWidth: 2,
	}
}

// SaveToFile saves the graph to an HTML file
func (gv *GraphVisualizer) SaveToFile(filename string, title string) error {
	graph := gv.GenerateGraph(title)

	page := components.NewPage()
	page.AddCharts(graph)

	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	return page.Render(f)
}
