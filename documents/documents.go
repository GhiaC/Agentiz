package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"log"

	"agentize/documents/components"
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
	Auth        Auth     `json:"auth,omitempty"`
}

// Tool represents a tool definition
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
	Status      string                 `json:"status,omitempty"`
}

// Auth represents node authentication/authorization
type Auth struct {
	Users []UserPermissions `json:"users,omitempty"`
}

// UserPermissions represents permissions for a user
type UserPermissions struct {
	UserID         string `json:"user_id"`
	CanEdit        bool   `json:"can_edit"`
	CanRead        bool   `json:"can_read"`
	CanAccessNext  bool   `json:"can_access_next"`
	CanSee         bool   `json:"can_see"`
	VisibleInDocs  bool   `json:"visible_in_docs"`
	VisibleInGraph bool   `json:"visible_in_graph"`
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

		// Convert auth users from map[string]*Permissions to UserPermissions slice
		authUsers := make([]UserPermissions, 0, len(node.Auth.Users))
		for userID, perms := range node.Auth.Users {
			if perms == nil {
				continue
			}
			authUsers = append(authUsers, UserPermissions{
				UserID:         userID,
				CanEdit:        perms.HasPermission('w'),
				CanRead:        perms.HasPermission('r'),
				CanAccessNext:  perms.HasPermission('x'),
				CanSee:         perms.HasPermission('s'),
				VisibleInDocs:  perms.HasPermission('d'),
				VisibleInGraph: perms.HasPermission('g'),
			})
		}

		nodeDoc := &NodeDocument{
			Path:        path,
			ID:          node.ID,
			Title:       node.Title,
			Description: node.Description,
			Content:     node.Content,
			Children:    children,
			Auth: Auth{
				Users: authUsers,
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

// GenerateHTML generates an HTML page using templ components
func (d *AgentizeDocument) GenerateHTML() ([]byte, error) {
	// Prepare JSON data
	treeJSON, _ := json.Marshal(d.tree)
	nodesJSON, _ := json.Marshal(d.nodes)

	// Construct JavaScript assignment strings
	treeDataJS := "const treeData = " + string(treeJSON) + ";"
	nodesDataJS := "const nodesData = " + string(nodesJSON) + ";"

	// Render using templ
	var buf bytes.Buffer
	ctx := context.Background()
	if err := components.Page(treeDataJS, nodesDataJS).Render(ctx, &buf); err != nil {
		return nil, err
	}

	// Replace the literal string placeholders with actual JavaScript
	html := buf.Bytes()
	html = bytes.ReplaceAll(html, []byte("{ treeData }"), []byte(treeDataJS))
	html = bytes.ReplaceAll(html, []byte("{ nodesData }"), []byte(nodesDataJS))

	return html, nil
}
