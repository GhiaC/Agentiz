package documents

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"text/template"

	"agentize/model"
)

// AgentizeDocument represents the knowledge tree structure
type AgentizeDocument struct {
	nodes              map[string]*NodeDocument
	nodesWithoutFields []NodeDocument
	tree               *TreeNode
}

// NodeDocument represents a node in the knowledge tree
type NodeDocument struct {
	Path        string   `json:"path"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Content     string   `json:"content,omitempty"`
	Children    []string `json:"children,omitempty"`
	Tools       []Tool   `json:"tools,omitempty"`
	Policy      Policy   `json:"policy,omitempty"`
}

// Tool represents a tool definition
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
	Status      string                 `json:"status,omitempty"`
}

// Policy represents node policy
type Policy struct {
	CanAdvance       bool     `json:"can_advance"`
	AdvanceCondition string   `json:"advance_condition,omitempty"`
	MaxOpenFiles     int      `json:"max_open_files,omitempty"`
	RoutingMode      string   `json:"routing_mode,omitempty"`
	Children         []string `json:"children,omitempty"`
	MemoryPersist    []string `json:"memory_persist,omitempty"`
}

// TreeNode represents a node in the tree structure
type TreeNode struct {
	Path        string      `json:"path"`
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Children    []*TreeNode `json:"children,omitempty"`
	Level       int         `json:"level"`
}

// NewAgentizeDocument creates a new document structure from Agentize nodes
func NewAgentizeDocument(nodes map[string]*model.Node, getChildren func(string) ([]string, error)) *AgentizeDocument {
	doc := &AgentizeDocument{
		nodes:              make(map[string]*NodeDocument),
		nodesWithoutFields: []NodeDocument{},
	}

	// Convert model nodes to document nodes
	for path, node := range nodes {
		children, _ := getChildren(path)
		
		nodeDoc := &NodeDocument{
			Path:        path,
			ID:          node.ID,
			Title:       node.Title,
			Description: node.Description,
			Content:     node.Content,
			Children:    children,
			Policy: Policy{
				CanAdvance:       node.Policy.CanAdvance,
				AdvanceCondition: node.Policy.AdvanceCondition,
				MaxOpenFiles:     node.Policy.MaxOpenFiles,
				RoutingMode:      node.Policy.Routing.Mode,
				Children:         node.Policy.Routing.Children,
				MemoryPersist:    node.Policy.Memory.Persist,
			},
		}

		// Convert tools
		for _, tool := range node.Tools {
			nodeDoc.Tools = append(nodeDoc.Tools, Tool{
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
				Status:      string(tool.Status),
			})
		}

		doc.nodes[path] = nodeDoc

		// Create version without fields for system prompt
		withoutFields := NodeDocument{
			Path:        path,
			ID:          node.ID,
			Title:       node.Title,
			Description: node.Description,
			Children:    children,
		}
		doc.nodesWithoutFields = append(doc.nodesWithoutFields, withoutFields)
	}

	// Build tree structure
	doc.tree = doc.buildTree("root", 0, getChildren)

	log.Printf("AgentizeDocument loaded %d nodes", len(doc.nodes))
	log.Printf("AgentizeDocument tree depth: %d", doc.getTreeDepth(doc.tree))

	return doc
}

// buildTree recursively builds the tree structure
func (d *AgentizeDocument) buildTree(path string, level int, getChildren func(string) ([]string, error)) *TreeNode {
	nodeDoc, exists := d.nodes[path]
	if !exists {
		return nil
	}

	treeNode := &TreeNode{
		Path:        path,
		ID:          nodeDoc.ID,
		Title:       nodeDoc.Title,
		Description: nodeDoc.Description,
		Level:       level,
		Children:    []*TreeNode{},
	}

	// Get children and build recursively
	children, err := getChildren(path)
	if err == nil {
		for _, childPath := range children {
			childNode := d.buildTree(childPath, level+1, getChildren)
			if childNode != nil {
				treeNode.Children = append(treeNode.Children, childNode)
			}
		}
	}

	return treeNode
}

// getTreeDepth calculates the maximum depth of the tree
func (d *AgentizeDocument) getTreeDepth(node *TreeNode) int {
	if node == nil {
		return 0
	}
	if len(node.Children) == 0 {
		return node.Level + 1
	}
	maxDepth := node.Level + 1
	for _, child := range node.Children {
		depth := d.getTreeDepth(child)
		if depth > maxDepth {
			maxDepth = depth
		}
	}
	return maxDepth
}

// GetNodesJson returns all nodes as JSON string
func (d *AgentizeDocument) GetNodesJson() string {
	j, _ := json.Marshal(d.nodes)
	return string(j)
}

// GetTreeJson returns the tree structure as JSON string
func (d *AgentizeDocument) GetTreeJson() string {
	j, _ := json.Marshal(d.tree)
	return string(j)
}

// GetNodesSystemPrompt returns nodes without fields for system prompt
func (d *AgentizeDocument) GetNodesSystemPrompt() string {
	j, _ := json.Marshal(d.nodesWithoutFields)
	return string(j)
}

// GetNode returns a node by path
func (d *AgentizeDocument) GetNode(path string) *NodeDocument {
	return d.nodes[path]
}

// GetNodes returns all nodes
func (d *AgentizeDocument) GetNodes() map[string]*NodeDocument {
	return d.nodes
}

// GetTree returns the tree root
func (d *AgentizeDocument) GetTree() *TreeNode {
	return d.tree
}

// GetAllPaths returns all node paths
func (d *AgentizeDocument) GetAllPaths() []string {
	paths := make([]string, 0, len(d.nodes))
	for path := range d.nodes {
		paths = append(paths, path)
	}
	return paths
}

// GenerateHTML generates an HTML page from the template
func (d *AgentizeDocument) GenerateHTML() ([]byte, error) {
	// Try to load template from multiple possible paths
	templatePaths := []string{
		"./documents/template.html",
		"documents/template.html",
		filepath.Join("documents", "template.html"),
		filepath.Join(".", "documents", "template.html"),
	}

	var templateContent []byte
	var err error
	for _, path := range templatePaths {
		templateContent, err = os.ReadFile(path)
		if err == nil {
			break
		}
	}

	if err != nil {
		// Use embedded template as fallback
		templateContent = []byte(embeddedTemplate)
	}

	// Parse template
	tmpl, err := template.New("documents").Parse(string(templateContent))
	if err != nil {
		return nil, err
	}

	// Prepare data
	treeJSON, _ := json.Marshal(d.tree)
	nodesJSON, _ := json.Marshal(d.nodes)

	data := struct {
		TreeData  string
		NodesData string
	}{
		TreeData:  string(treeJSON),
		NodesData: string(nodesJSON),
	}

	// Execute template
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// embeddedTemplate is a fallback template if file is not found
const embeddedTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Agentize Knowledge Tree Documentation</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            padding: 20px;
        }

        .container {
            max-width: 1400px;
            margin: 0 auto;
            background: white;
            border-radius: 12px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            overflow: hidden;
        }

        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 30px;
            text-align: center;
        }

        .header h1 {
            font-size: 2.5em;
            margin-bottom: 10px;
        }

        .header p {
            opacity: 0.9;
            font-size: 1.1em;
        }

        .stats {
            display: flex;
            justify-content: center;
            gap: 30px;
            padding: 20px;
            background: #f8f9fa;
            border-bottom: 1px solid #e9ecef;
        }

        .stat {
            text-align: center;
        }

        .stat-value {
            font-size: 2em;
            font-weight: bold;
            color: #667eea;
        }

        .stat-label {
            color: #6c757d;
            font-size: 0.9em;
            margin-top: 5px;
        }

        .content {
            padding: 30px;
        }

        .tree-container {
            margin-top: 20px;
        }

        .tree-node {
            margin: 15px 0;
            padding: 15px;
            border-left: 4px solid #667eea;
            background: #f8f9fa;
            border-radius: 8px;
            transition: all 0.3s ease;
        }

        .tree-node:hover {
            background: #e9ecef;
            transform: translateX(5px);
            box-shadow: 0 4px 12px rgba(0, 0, 0, 0.1);
        }

        .node-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 10px;
        }

        .node-title {
            font-size: 1.3em;
            font-weight: bold;
            color: #333;
        }

        .node-path {
            font-family: 'Courier New', monospace;
            color: #6c757d;
            font-size: 0.9em;
            background: white;
            padding: 4px 8px;
            border-radius: 4px;
        }

        .node-description {
            color: #555;
            margin: 10px 0;
            line-height: 1.6;
        }

        .node-meta {
            display: flex;
            gap: 15px;
            margin-top: 10px;
            flex-wrap: wrap;
        }

        .meta-item {
            display: flex;
            align-items: center;
            gap: 5px;
            font-size: 0.9em;
            color: #6c757d;
        }

        .meta-badge {
            background: #667eea;
            color: white;
            padding: 2px 8px;
            border-radius: 12px;
            font-size: 0.8em;
        }

        .children {
            margin-left: 30px;
            margin-top: 15px;
            border-left: 2px solid #dee2e6;
            padding-left: 20px;
        }

        .tools-section {
            margin-top: 15px;
            padding-top: 15px;
            border-top: 1px solid #dee2e6;
        }

        .tools-title {
            font-weight: bold;
            color: #667eea;
            margin-bottom: 10px;
        }

        .tool-item {
            background: white;
            padding: 10px;
            margin: 5px 0;
            border-radius: 6px;
            border: 1px solid #dee2e6;
        }

        .tool-name {
            font-weight: bold;
            color: #333;
        }

        .tool-description {
            color: #6c757d;
            font-size: 0.9em;
            margin-top: 5px;
        }

        .search-box {
            margin-bottom: 20px;
            padding: 15px;
            background: #f8f9fa;
            border-radius: 8px;
        }

        .search-input {
            width: 100%;
            padding: 12px;
            border: 2px solid #dee2e6;
            border-radius: 6px;
            font-size: 1em;
            transition: border-color 0.3s;
        }

        .search-input:focus {
            outline: none;
            border-color: #667eea;
        }

        .level-indicator {
            display: inline-block;
            width: 20px;
            height: 20px;
            border-radius: 50%;
            background: #667eea;
            color: white;
            text-align: center;
            line-height: 20px;
            font-size: 0.8em;
            margin-right: 10px;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="header">
            <h1>🌳 Agentize Knowledge Tree</h1>
            <p>Interactive Documentation Browser</p>
        </div>

        <div class="stats">
            <div class="stat">
                <div class="stat-value" id="total-nodes">0</div>
                <div class="stat-label">Total Nodes</div>
            </div>
            <div class="stat">
                <div class="stat-value" id="total-tools">0</div>
                <div class="stat-label">Total Tools</div>
            </div>
            <div class="stat">
                <div class="stat-value" id="tree-depth">0</div>
                <div class="stat-label">Tree Depth</div>
            </div>
        </div>

        <div class="content">
            <div class="search-box">
                <input type="text" class="search-input" id="search-input" placeholder="Search nodes, tools, or descriptions...">
            </div>

            <div class="tree-container" id="tree-container">
                <!-- Tree will be rendered here -->
            </div>
        </div>
    </div>

    <script>
        const treeData = {{.TreeData}};
        const nodesData = {{.NodesData}};

        function calculateStats() {
            const totalNodes = Object.keys(nodesData).length;
            let totalTools = 0;
            let maxDepth = 0;

            function countTools(node) {
                if (nodesData[node.path] && nodesData[node.path].tools) {
                    totalTools += nodesData[node.path].tools.length;
                }
                if (node.level > maxDepth) {
                    maxDepth = node.level;
                }
                if (node.children) {
                    node.children.forEach(countTools);
                }
            }

            if (treeData) {
                countTools(treeData);
            }

            document.getElementById('total-nodes').textContent = totalNodes;
            document.getElementById('total-tools').textContent = totalTools;
            document.getElementById('tree-depth').textContent = maxDepth + 1;
        }

        function renderNode(node, isRoot = false) {
            const nodeData = nodesData[node.path] || {};
            const level = node.level || 0;
            const indent = level * 30;

            let html = '<div class="tree-node" data-path="' + escapeHtml(node.path) + '" data-level="' + level + '" style="margin-left: ' + indent + 'px;">' +
                '<div class="node-header">' +
                '<div>' +
                '<span class="level-indicator">' + level + '</span>' +
                '<span class="node-title">' + escapeHtml(node.title || node.id || node.path) + '</span>' +
                '</div>' +
                '<span class="node-path">' + escapeHtml(node.path) + '</span>' +
                '</div>';

            if (node.description || nodeData.description) {
                html += '<div class="node-description">' + escapeHtml(node.description || nodeData.description || '') + '</div>';
            }

            html += '<div class="node-meta">';
            
            if (nodeData.policy) {
                if (nodeData.policy.can_advance) {
                    html += '<span class="meta-item"><span class="meta-badge">Can Advance</span></span>';
                }
                if (nodeData.policy.routing_mode) {
                    html += '<span class="meta-item">Routing: <strong>' + escapeHtml(nodeData.policy.routing_mode) + '</strong></span>';
                }
                if (nodeData.children && nodeData.children.length > 0) {
                    html += '<span class="meta-item">Children: <strong>' + nodeData.children.length + '</strong></span>';
                }
            }

            if (nodeData.tools && nodeData.tools.length > 0) {
                html += '<span class="meta-item">Tools: <strong>' + nodeData.tools.length + '</strong></span>';
            }

            html += '</div>';

            if (nodeData.tools && nodeData.tools.length > 0) {
                html += '<div class="tools-section">';
                html += '<div class="tools-title">🔧 Tools (' + nodeData.tools.length + ')</div>';
                nodeData.tools.forEach(function(tool) {
                    html += '<div class="tool-item">' +
                        '<div class="tool-name">' + escapeHtml(tool.name) + '</div>' +
                        '<div class="tool-description">' + escapeHtml(tool.description || '') + '</div>' +
                        '</div>';
                });
                html += '</div>';
            }

            if (nodeData.content) {
                const contentPreview = nodeData.content.substring(0, 200);
                html += '<details style="margin-top: 10px;">' +
                    '<summary style="cursor: pointer; color: #667eea; font-weight: bold;">📄 Content Preview</summary>' +
                    '<div style="margin-top: 10px; padding: 10px; background: white; border-radius: 6px; font-size: 0.9em; color: #555;">' +
                    escapeHtml(contentPreview) + (nodeData.content.length > 200 ? '...' : '') +
                    '</div>' +
                    '</details>';
            }

            html += '</div>';

            if (node.children && node.children.length > 0) {
                html += '<div class="children">';
                node.children.forEach(function(child) {
                    html += renderNode(child);
                });
                html += '</div>';
            }

            return html;
        }

        function renderTree() {
            const container = document.getElementById('tree-container');
            if (treeData) {
                container.innerHTML = renderNode(treeData, true);
            } else {
                container.innerHTML = '<p>No tree data available</p>';
            }
            calculateStats();
        }

        function setupSearch() {
            const searchInput = document.getElementById('search-input');
            searchInput.addEventListener('input', function(e) {
                const query = e.target.value.toLowerCase();
                const nodes = document.querySelectorAll('.tree-node');
                
                nodes.forEach(function(node) {
                    const path = node.getAttribute('data-path');
                    const nodeData = nodesData[path] || {};
                    const toolText = (nodeData.tools || []).map(function(t) { return t.name + ' ' + t.description; }).join(' ');
                    const text = (nodeData.title || '') + ' ' + (nodeData.description || '') + ' ' + path + ' ' + toolText;
                    const lowerText = text.toLowerCase();
                    
                    if (lowerText.includes(query)) {
                        node.style.display = '';
                        let parent = node.parentElement;
                        while (parent && parent.classList.contains('children')) {
                            parent.style.display = '';
                            parent = parent.parentElement;
                        }
                    } else {
                        node.style.display = 'none';
                    }
                });
            });
        }

        function escapeHtml(text) {
            const div = document.createElement('div');
            div.textContent = text;
            return div.innerHTML;
        }

        renderTree();
        setupSearch();
    </script>
</body>
</html>`

