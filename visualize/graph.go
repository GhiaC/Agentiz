package visualize

import (
	"encoding/json"
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
	// Count total nodes including tools
	totalTools := 0
	for _, node := range gv.nodes {
		totalTools += len(node.Tools)
	}

	graph := charts.NewGraph()
	graph.SetGlobalOptions(
		charts.WithTitleOpts(opts.Title{
			Title:    title,
			Subtitle: fmt.Sprintf("Knowledge Tree with %d nodes and %d tools", len(gv.nodes), totalTools),
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
	// Pre-allocate with estimated size (nodes + tools)
	estimatedSize := len(gv.nodes)
	for _, node := range gv.nodes {
		estimatedSize += len(node.Tools)
	}
	graphNodes := make([]opts.GraphNode, 0, estimatedSize)

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

		// Add tool nodes
		for _, tool := range node.Tools {
			// Skip hidden tools
			if tool.Status == model.ToolStatusHidden {
				continue
			}

			toolNodeName := tool.Name
			toolCategory := 4 // Tool category

			toolNode := opts.GraphNode{
				Name:       toolNodeName,
				Value:      1,
				Category:   toolCategory,
				SymbolSize: 20, // Smaller size for tools
				ItemStyle:  gv.getNodeStyle(toolCategory),
			}

			graphNodes = append(graphNodes, toolNode)
		}
	}

	return graphNodes
}

// createLinks creates links between nodes based on parent-child relationships
func (gv *GraphVisualizer) createLinks() []opts.GraphLink {
	links := make([]opts.GraphLink, 0)

	for path, node := range gv.nodes {
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

		// Create links from node to its tools
		nodeName := gv.getNodeName(path)
		for _, tool := range node.Tools {
			// Skip hidden tools
			if tool.Status == model.ToolStatusHidden {
				continue
			}

			toolNodeName := tool.Name
			links = append(links, opts.GraphLink{
				Source: nodeName,
				Target: toolNodeName,
				Value:  0.5, // Lighter weight for tool links
				LineStyle: &opts.LineStyle{
					Width:     1,
					Curveness: 0.1,
					Type:      "dashed", // Dashed line for tool links
				},
			})
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
		{
			Name: "Tool",
			ItemStyle: &opts.ItemStyle{
				Color: "#73c0de", // Light Blue
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
		"#73c0de", // Tool - Light Blue
	}

	if category >= len(colors) {
		category = 1
	}

	borderWidth := float32(2)
	if category == 4 { // Tools have thinner border
		borderWidth = float32(1)
	}

	return &opts.ItemStyle{
		Color:       colors[category],
		BorderColor: "#fff",
		BorderWidth: borderWidth,
	}
}

// nodeData represents node data for JavaScript
type nodeData struct {
	Path        string                 `json:"path"`
	ID          string                 `json:"id"`
	Title       string                 `json:"title"`
	Description string                 `json:"description"`
	Content     string                 `json:"content"`
	CanAdvance  bool                   `json:"can_advance"`
	Tools       []toolData             `json:"tools"`
	Policy      map[string]interface{} `json:"policy"`
}

type toolData struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
	Status      string                 `json:"status"`
}

// prepareNodeData prepares node data for JavaScript
func (gv *GraphVisualizer) prepareNodeData() map[string]nodeData {
	data := make(map[string]nodeData)

	for path, node := range gv.nodes {
		nodeName := gv.getNodeName(path)

		tools := make([]toolData, 0, len(node.Tools))
		for _, tool := range node.Tools {
			if tool.Status != model.ToolStatusHidden {
				tools = append(tools, toolData{
					Name:        tool.Name,
					Description: tool.Description,
					InputSchema: tool.InputSchema,
					Status:      string(tool.Status),
				})
			}
		}

		policy := map[string]interface{}{
			"can_advance":       node.Policy.CanAdvance,
			"advance_condition": node.Policy.AdvanceCondition,
			"max_open_files":    node.Policy.MaxOpenFiles,
			"routing_mode":      node.Policy.Routing.Mode,
			"routing_children":  node.Policy.Routing.Children,
			"memory_persist":    node.Policy.Memory.Persist,
		}

		data[nodeName] = nodeData{
			Path:        path,
			ID:          node.ID,
			Title:       node.Title,
			Description: node.Description,
			Content:     node.Content,
			CanAdvance:  node.Policy.CanAdvance,
			Tools:       tools,
			Policy:      policy,
		}
	}

	return data
}

// SaveToFile saves the graph to an HTML file with click-to-view details functionality
func (gv *GraphVisualizer) SaveToFile(filename string, title string) error {
	graph := gv.GenerateGraph(title)

	page := components.NewPage()
	page.AddCharts(graph)

	// Create a temporary file to render the page
	tmpFile, err := os.CreateTemp("", "graph-*.html")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpFileName := tmpFile.Name()
	defer os.Remove(tmpFileName)
	tmpFile.Close()

	// Render the page to temp file
	tmpF, err := os.Create(tmpFileName)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	if err := page.Render(tmpF); err != nil {
		tmpF.Close()
		return fmt.Errorf("failed to render page: %w", err)
	}
	tmpF.Close()

	// Read the rendered content
	renderedContent, err := os.ReadFile(tmpFileName)
	if err != nil {
		return fmt.Errorf("failed to read rendered content: %w", err)
	}

	// Prepare node data for JavaScript
	nodeData := gv.prepareNodeData()
	nodeDataJSON, err := json.Marshal(nodeData)
	if err != nil {
		return fmt.Errorf("failed to marshal node data: %w", err)
	}

	// Prepare modal HTML, CSS, and JavaScript
	modalHTML := gv.generateModalHTML(string(nodeDataJSON))

	// Find the closing </body> tag and insert modal before it
	content := string(renderedContent)
	bodyCloseIdx := strings.LastIndex(content, "</body>")
	if bodyCloseIdx == -1 {
		// If no </body> tag, append at the end
		content += modalHTML
	} else {
		// Insert modal before </body>
		content = content[:bodyCloseIdx] + modalHTML + content[bodyCloseIdx:]
	}

	// Write the final content to file
	return os.WriteFile(filename, []byte(content), 0644)
}

// generateModalHTML generates the HTML, CSS, and JavaScript for the modal
func (gv *GraphVisualizer) generateModalHTML(nodeDataJSON string) string {
	return fmt.Sprintf(`
<style>
.node-modal {
	display: none;
	position: fixed;
	z-index: 10000;
	left: 0;
	top: 0;
	width: 100%%;
	height: 100%%;
	background-color: rgba(0,0,0,0.5);
	overflow: auto;
}

.node-modal-content {
	background-color: #fefefe;
	margin: 5%% auto;
	padding: 20px;
	border: 1px solid #888;
	border-radius: 8px;
	width: 80%%;
	max-width: 800px;
	max-height: 90%%;
	overflow-y: auto;
	box-shadow: 0 4px 6px rgba(0,0,0,0.1);
}

.node-modal-header {
	display: flex;
	justify-content: space-between;
	align-items: center;
	margin-bottom: 20px;
	padding-bottom: 10px;
	border-bottom: 2px solid #eee;
}

.node-modal-title {
	font-size: 24px;
	font-weight: bold;
	color: #333;
	margin: 0;
}

.node-modal-close {
	color: #aaa;
	font-size: 28px;
	font-weight: bold;
	cursor: pointer;
	line-height: 20px;
}

.node-modal-close:hover,
.node-modal-close:focus {
	color: #000;
	text-decoration: none;
}

.node-modal-section {
	margin-bottom: 20px;
}

.node-modal-section-title {
	font-size: 18px;
	font-weight: bold;
	color: #5470c6;
	margin-bottom: 10px;
	padding-bottom: 5px;
	border-bottom: 1px solid #eee;
}

.node-modal-section-content {
	color: #666;
	line-height: 1.6;
	white-space: pre-wrap;
	word-wrap: break-word;
}

.node-modal-tools {
	display: grid;
	grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
	gap: 10px;
}

.node-modal-tool {
	background-color: #f5f5f5;
	padding: 10px;
	border-radius: 4px;
	border-left: 3px solid #73c0de;
}

.node-modal-tool-name {
	font-weight: bold;
	color: #333;
	margin-bottom: 5px;
}

.node-modal-tool-desc {
	font-size: 14px;
	color: #666;
}

.node-modal-policy {
	background-color: #f9f9f9;
	padding: 10px;
	border-radius: 4px;
	font-family: monospace;
	font-size: 12px;
}

.node-modal-content-text {
	background-color: #f9f9f9;
	padding: 15px;
	border-radius: 4px;
	max-height: 300px;
	overflow-y: auto;
	font-family: 'Courier New', monospace;
	font-size: 13px;
	line-height: 1.5;
}
</style>

<div id="nodeModal" class="node-modal">
	<div class="node-modal-content">
		<div class="node-modal-header">
			<h2 class="node-modal-title" id="modalTitle">Node Details</h2>
			<span class="node-modal-close" id="modalClose">&times;</span>
		</div>
		<div id="modalBody"></div>
	</div>
</div>

<script>
const nodeData = %s;

function escapeHtml(text) {
	const div = document.createElement('div');
	div.textContent = text;
	return div.innerHTML;
}

function formatJson(obj) {
	return JSON.stringify(obj, null, 2);
}

function showNodeDetails(nodeName) {
	const data = nodeData[nodeName];
	if (!data) {
		console.warn('Node data not found for:', nodeName);
		return;
	}

	const modal = document.getElementById('nodeModal');
	const modalTitle = document.getElementById('modalTitle');
	const modalBody = document.getElementById('modalBody');

	modalTitle.textContent = data.title || data.id || nodeName;

	let html = '';

	// Path
	if (data.path) {
		html += '<div class="node-modal-section">';
		html += '<div class="node-modal-section-title">Path</div>';
		html += '<div class="node-modal-section-content">' + escapeHtml(data.path) + '</div>';
		html += '</div>';
	}

	// Description
	if (data.description) {
		html += '<div class="node-modal-section">';
		html += '<div class="node-modal-section-title">Description</div>';
		html += '<div class="node-modal-section-content">' + escapeHtml(data.description) + '</div>';
		html += '</div>';
	}

	// Policy
	if (data.policy) {
		html += '<div class="node-modal-section">';
		html += '<div class="node-modal-section-title">Policy</div>';
		html += '<div class="node-modal-policy">' + escapeHtml(formatJson(data.policy)) + '</div>';
		html += '</div>';
	}

	// Tools
	if (data.tools && data.tools.length > 0) {
		html += '<div class="node-modal-section">';
		html += '<div class="node-modal-section-title">Tools (' + data.tools.length + ')</div>';
		html += '<div class="node-modal-tools">';
		data.tools.forEach(tool => {
			html += '<div class="node-modal-tool">';
			html += '<div class="node-modal-tool-name">' + escapeHtml(tool.name) + '</div>';
			if (tool.description) {
				html += '<div class="node-modal-tool-desc">' + escapeHtml(tool.description) + '</div>';
			}
			if (tool.status && tool.status !== 'active') {
				html += '<div style="color: #ee6666; font-size: 12px; margin-top: 5px;">Status: ' + escapeHtml(tool.status) + '</div>';
			}
			html += '</div>';
		});
		html += '</div>';
		html += '</div>';
	}

	// Content
	if (data.content) {
		html += '<div class="node-modal-section">';
		html += '<div class="node-modal-section-title">Content</div>';
		html += '<div class="node-modal-content-text">' + escapeHtml(data.content) + '</div>';
		html += '</div>';
	}

	modalBody.innerHTML = html;
	modal.style.display = 'block';
}

function closeModal() {
	document.getElementById('nodeModal').style.display = 'none';
}

// Close modal when clicking outside
window.onclick = function(event) {
	const modal = document.getElementById('nodeModal');
	if (event.target == modal) {
		closeModal();
	}
}

// Close modal button
document.getElementById('modalClose').onclick = closeModal;

// Add click event listener to chart after it's rendered
(function() {
	let chart = null;
	let attempts = 0;
	const maxAttempts = 30;
	
	function findAndAttachChart() {
		attempts++;
		
		// Wait for echarts to be available
		if (typeof echarts === 'undefined') {
			if (attempts < maxAttempts) {
				setTimeout(findAndAttachChart, 200);
			}
			return;
		}
		
		// Try to find chart instance
		const chartContainers = document.querySelectorAll('div[id], div[style*="width"]');
		for (let i = 0; i < chartContainers.length; i++) {
			try {
				const instance = echarts.getInstanceByDom(chartContainers[i]);
				if (instance) {
					chart = instance;
					break;
				}
			} catch (e) {
				// Continue
			}
		}
		
		// If not found, try getting all instances
		if (!chart) {
			try {
				const allInstances = echarts.getInstanceByDom(document.body);
				if (allInstances && allInstances.length > 0) {
					chart = allInstances[0];
				}
			} catch (e) {
				// Continue
			}
		}
		
		// If still not found, try document
		if (!chart) {
			try {
				const allInstances = echarts.getInstanceByDom(document);
				if (allInstances && allInstances.length > 0) {
					chart = allInstances[0];
				}
			} catch (e) {
				// Continue
			}
		}
		
		if (chart) {
			// Attach click handler using echarts API
			chart.on('click', function(params) {
				console.log('ECharts click event:', params);
				if (params && params.data && params.data.name) {
					const nodeName = params.data.name;
					console.log('Node clicked:', nodeName);
					showNodeDetails(nodeName);
				}
			});
			console.log('ECharts click handler attached successfully');
			
			// Also add event delegation as backup
			setupEventDelegation();
		} else {
			if (attempts < maxAttempts) {
				setTimeout(findAndAttachChart, 300);
			} else {
				console.warn('Could not find chart instance, using event delegation fallback');
				setupEventDelegation();
			}
		}
	}
	
	// Fallback: Event delegation on SVG elements
	function setupEventDelegation() {
		// Wait for SVG to be rendered
		setTimeout(function() {
			const svg = document.querySelector('svg');
			if (svg) {
				svg.addEventListener('click', function(e) {
					const target = e.target;
					let nodeName = null;
					
					// Method 1: Check if target is a circle/ellipse (node symbol)
					if (target && (target.tagName === 'circle' || target.tagName === 'ellipse')) {
						// Find the parent group that contains this node
						let parent = target.parentElement;
						while (parent && parent.tagName !== 'g') {
							parent = parent.parentElement;
						}
						
						if (parent) {
							// Look for text element in the same group
							const textElement = parent.querySelector('text');
							if (textElement) {
								// Extract node name from text (might be multiline, take first line)
								const textContent = textElement.textContent.trim();
								const lines = textContent.split('\n');
								if (lines.length > 0) {
									// Remove tool count if present: "Title\n(5 tools)" -> "Title"
									nodeName = lines[0].replace(/\s*\(.*tools?\)\s*$/i, '').trim();
								}
							}
						}
					}
					
					// Method 2: Check if clicked on text element directly
					if (!nodeName && target && target.tagName === 'text') {
						const textContent = target.textContent.trim();
						const lines = textContent.split('\n');
						if (lines.length > 0) {
							nodeName = lines[0].replace(/\s*\(.*tools?\)\s*$/i, '').trim();
						}
					}
					
					if (nodeName && nodeData[nodeName]) {
						console.log('SVG element clicked, node:', nodeName);
						showNodeDetails(nodeName);
					}
				}, true); // Use capture phase
				console.log('Event delegation setup complete');
			} else {
				// Retry if SVG not found yet
				setTimeout(setupEventDelegation, 500);
			}
		}, 1000);
	}
	
	// Start looking for chart
	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', function() {
			setTimeout(findAndAttachChart, 500);
		});
	} else {
		setTimeout(findAndAttachChart, 500);
	}
})();
</script>
`, nodeDataJSON)
}
