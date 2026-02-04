package fsrepo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"gopkg.in/yaml.v3"
)

// SummaryGenerator generates a keyword-based English summary for a node's content
// It receives the node content and returns a short summary string
type SummaryGenerator func(ctx context.Context, content string) (string, error)

// NodeRepository handles loading nodes from the filesystem
type NodeRepository struct {
	rootPath         string
	cache            map[string]*model.Node
	mu               sync.RWMutex
	summaryGenerator SummaryGenerator
}

// NewNodeRepository creates a new repository with the given root path
func NewNodeRepository(rootPath string) (*NodeRepository, error) {
	absPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("invalid root path: %w", err)
	}

	// Verify root directory exists
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("root path does not exist: %s", absPath)
	}

	return &NodeRepository{
		rootPath: absPath,
		cache:    make(map[string]*model.Node),
	}, nil
}

// SetSummaryGenerator sets the function used to generate summaries for nodes
func (r *NodeRepository) SetSummaryGenerator(generator SummaryGenerator) {
	r.summaryGenerator = generator
}

// LoadNode loads a node from the filesystem by its path
// Path should be relative to root (e.g., "root", "root/next")
func (r *NodeRepository) LoadNode(path string) (*model.Node, error) {
	// Check cache first
	r.mu.RLock()
	if cached, ok := r.cache[path]; ok {
		r.mu.RUnlock()
		return cached, nil
	}
	r.mu.RUnlock()

	// Build full path
	fullPath := filepath.Join(r.rootPath, path)

	// Verify directory exists
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, fmt.Errorf("node path does not exist: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("node path is not a directory: %s", path)
	}

	// Load node files
	node := &model.Node{
		Path: path,
	}

	// Load node.yaml (optional)
	meta, err := r.loadNodeMeta(fullPath)
	if err == nil {
		node.ID = meta.ID
		node.Title = meta.Title
		node.Description = meta.Description
		node.Summary = meta.Summary
		node.Auth = meta.Auth
		node.MCP = meta.MCP
	} else {
		// Use defaults if node.yaml doesn't exist
		node.ID = path
		node.Auth = model.Auth{
			Inherit: true,
			Users:   make(map[string]*model.Permissions),
		}
	}

	// Load node.md (optional)
	content, err := r.loadNodeContent(fullPath)
	if err == nil {
		node.Content = content
	}

	// Load tools.json (optional)
	tools, err := r.loadTools(fullPath)
	if err == nil {
		node.Tools = tools
	}

	// Calculate hash and set metadata
	node.Hash = r.calculateHash(node.Content)
	node.LoadedAt = time.Now()

	// Cache the node
	r.mu.Lock()
	r.cache[path] = node
	r.mu.Unlock()

	return node, nil
}

// GetChildren returns all child nodes for a given path
// It scans the directory for subdirectories
func (r *NodeRepository) GetChildren(path string) ([]string, error) {
	fullPath := filepath.Join(r.rootPath, path)

	var children []string

	// Scan directory for all subdirectories (excluding special files)
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// Skip hidden directories and special directories
			if entry.Name()[0] == '.' {
				continue
			}
			if path == "root" {
				children = append(children, "root/"+entry.Name())
			} else {
				children = append(children, path+"/"+entry.Name())
			}
		}
	}

	return children, nil
}

// HasNext checks if a node has any child nodes
func (r *NodeRepository) HasNext(path string) bool {
	children, err := r.GetChildren(path)
	return err == nil && len(children) > 0
}

// NextPath returns the first child path if it exists (for backward compatibility)
func (r *NodeRepository) NextPath(path string) (string, bool) {
	children, err := r.GetChildren(path)
	if err != nil || len(children) == 0 {
		return "", false
	}
	return children[0], true
}

// loadNodeMeta loads and parses node.yaml
func (r *NodeRepository) loadNodeMeta(dirPath string) (*model.NodeMeta, error) {
	yamlPath := filepath.Join(dirPath, "node.yaml")
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, err
	}

	// For now, use a simple YAML parser
	// In production, use gopkg.in/yaml.v3
	meta := &model.NodeMeta{}
	if err := parseYAML(data, meta); err != nil {
		return nil, fmt.Errorf("failed to parse node.yaml: %w", err)
	}

	return meta, nil
}

// loadNodeContent loads node.md
func (r *NodeRepository) loadNodeContent(dirPath string) (string, error) {
	mdPath := filepath.Join(dirPath, "node.md")
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// loadTools loads and parses tools.json
func (r *NodeRepository) loadTools(dirPath string) ([]model.Tool, error) {
	jsonPath := filepath.Join(dirPath, "tools.json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, err
	}

	var toolsData struct {
		Tools []model.Tool `json:"tools"`
	}

	if err := json.Unmarshal(data, &toolsData); err != nil {
		return nil, fmt.Errorf("failed to parse tools.json: %w", err)
	}

	// Set default status for tools that don't have one
	for i := range toolsData.Tools {
		if toolsData.Tools[i].Status == "" {
			toolsData.Tools[i].Status = model.ToolStatusActive
		}
	}

	return toolsData.Tools, nil
}

// calculateHash calculates SHA256 hash of content
func (r *NodeRepository) calculateHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// InvalidateCache clears the cache for a specific path or all paths
func (r *NodeRepository) InvalidateCache(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if path == "" {
		r.cache = make(map[string]*model.Node)
	} else {
		delete(r.cache, path)
	}
}

// LoadAllTools recursively loads all nodes starting from "root" and returns all tools.
// This is a temporary/test method for the first version to get all tools without needing to open nodes.
func (r *NodeRepository) LoadAllTools() ([]model.Tool, error) {
	var allTools []model.Tool
	err := r.collectToolsRecursive("root", &allTools)
	if err != nil {
		return nil, err
	}
	return allTools, nil
}

// collectToolsRecursive recursively collects tools from a node and all its children
func (r *NodeRepository) collectToolsRecursive(path string, tools *[]model.Tool) error {
	// Load the node
	node, err := r.LoadNode(path)
	if err != nil {
		return err
	}

	// Add tools from this node
	*tools = append(*tools, node.Tools...)

	// Recursively collect tools from children
	children, err := r.GetChildren(path)
	if err != nil {
		return nil // No children, not an error
	}

	for _, childPath := range children {
		if err := r.collectToolsRecursive(childPath, tools); err != nil {
			// Log warning but continue with other children
			continue
		}
	}

	return nil
}

// saveNodeMeta writes the NodeMeta to node.yaml file
func (r *NodeRepository) saveNodeMeta(path string, meta *model.NodeMeta) error {
	fullPath := filepath.Join(r.rootPath, path, "node.yaml")

	data, err := yaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal node meta: %w", err)
	}

	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write node.yaml: %w", err)
	}

	return nil
}

// EnsureSummaries iterates through all nodes and generates summaries for those missing them.
// It uses the configured SummaryGenerator to create concise English summaries.
// Summaries are saved to the node.yaml files.
// If forceSummary is true, it will regenerate summaries for all nodes, even if they already have one.
func (r *NodeRepository) EnsureSummaries(ctx context.Context, forceSummary bool) error {
	if r.summaryGenerator == nil {
		return fmt.Errorf("summary generator not configured")
	}

	return r.ensureSummariesRecursive(ctx, "root", forceSummary)
}

// ensureSummariesRecursive recursively ensures summaries for a node and all its children
func (r *NodeRepository) ensureSummariesRecursive(ctx context.Context, path string, forceSummary bool) error {
	// Invalidate cache to get fresh data
	r.InvalidateCache(path)

	// Load the node
	node, err := r.LoadNode(path)
	if err != nil {
		return err
	}

	// Check if summary needs to be generated:
	// - forceSummary: regenerate even if exists
	// - node.Summary == "": generate if missing
	// - node.Content != "": only if there's content to summarize
	needsSummary := (forceSummary || node.Summary == "") && node.Content != ""

	if needsSummary {
		action := "Generating"
		if forceSummary && node.Summary != "" {
			action = "Regenerating"
		}
		log.Log.Infof("%s summary for node: %s", action, path)

		// Generate summary using LLM
		summary, err := r.summaryGenerator(ctx, node.Content)
		if err != nil {
			log.Log.Warnf("Failed to generate summary for %s: %v", path, err)
		} else {
			// Load existing meta to preserve other fields
			fullPath := filepath.Join(r.rootPath, path)
			meta, err := r.loadNodeMeta(fullPath)
			if err != nil {
				// Create new meta if it doesn't exist
				meta = &model.NodeMeta{
					ID:    node.ID,
					Title: node.Title,
				}
			}

			// Update summary
			meta.Summary = summary

			// Save back to file
			if err := r.saveNodeMeta(path, meta); err != nil {
				log.Log.Warnf("Failed to save summary for %s: %v", path, err)
			} else {
				log.Log.Infof("Summary saved for node: %s", path)

				// Update cache
				r.mu.Lock()
				if cached, ok := r.cache[path]; ok {
					cached.Summary = summary
				}
				r.mu.Unlock()
			}
		}
	}

	// Process children
	children, err := r.GetChildren(path)
	if err != nil {
		return nil // No children, not an error
	}

	for _, childPath := range children {
		if err := r.ensureSummariesRecursive(ctx, childPath, forceSummary); err != nil {
			// Log warning but continue with other children
			log.Log.Warnf("Error processing child %s: %v", childPath, err)
			continue
		}
	}

	return nil
}
