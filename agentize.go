package agentize

import (
	"fmt"
	"sync"

	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/ghiac/agentize/visualize"
)

// Agentize is the main entry point for the library
// It loads and manages the entire knowledge tree
type Agentize struct {
	// Repository for loading nodes from filesystem
	repo *fsrepo.NodeRepository

	// All loaded nodes indexed by path
	nodes map[string]*model.Node

	// Root node
	root *model.Node

	// Mutex for thread-safe access
	mu sync.RWMutex

	// Session store (optional, can be set later)
	sessionStore store.SessionStore

	// Tool merge strategy
	toolStrategy model.MergeStrategy
}

// New creates a new Agentize instance by loading the entire knowledge tree from the given path
// It recursively traverses the directory structure and loads all nodes
func New(path string) (*Agentize, error) {
	return NewWithOptions(path, nil)
}

// Options allows configuring Agentize behavior
type Options struct {
	// ToolStrategy defines how tools with the same name should be merged
	ToolStrategy model.MergeStrategy
	// SessionStore allows providing a custom session store
	SessionStore store.SessionStore
}

// NewWithOptions creates a new Agentize instance with custom options
func NewWithOptions(path string, opts *Options) (*Agentize, error) {
	// Create repository
	repo, err := fsrepo.NewNodeRepository(path)
	if err != nil {
		return nil, fmt.Errorf("failed to create repository: %w", err)
	}

	// Create Agentize instance
	ag := &Agentize{
		repo:         repo,
		nodes:        make(map[string]*model.Node),
		sessionStore: store.NewMemoryStore(),
		toolStrategy: model.MergeStrategyOverride,
	}

	// Apply options
	if opts != nil {
		if opts.SessionStore != nil {
			ag.sessionStore = opts.SessionStore
		}
		if opts.ToolStrategy != "" {
			ag.toolStrategy = opts.ToolStrategy
		}
	}

	// Load all nodes recursively
	if err := ag.loadAllNodes(); err != nil {
		return nil, fmt.Errorf("failed to load knowledge tree: %w", err)
	}

	return ag, nil
}

// loadAllNodes recursively loads all nodes from the knowledge tree
func (ag *Agentize) loadAllNodes() error {
	ag.mu.Lock()
	defer ag.mu.Unlock()

	// Start from root
	return ag.loadNodeRecursiveLocked("root")
}

// loadNodeRecursiveLocked recursively loads a node and all its children
// Must be called with ag.mu.Lock() already held
func (ag *Agentize) loadNodeRecursiveLocked(path string) error {
	// Check if already loaded
	if _, exists := ag.nodes[path]; exists {
		return nil
	}

	// Load the node (repository has its own locking)
	node, err := ag.repo.LoadNode(path)
	if err != nil {
		return fmt.Errorf("failed to load node %s: %w", path, err)
	}

	// Store the node
	ag.nodes[path] = node

	// Set root if this is the root node
	if path == "root" {
		ag.root = node
	}

	// Load all child nodes recursively
	children, err := ag.repo.GetChildren(path)
	if err == nil {
		for _, childPath := range children {
			if err := ag.loadNodeRecursiveLocked(childPath); err != nil {
				return err
			}
		}
	}

	return nil
}

// GetNode returns a node by its path
func (ag *Agentize) GetNode(path string) (*model.Node, error) {
	ag.mu.RLock()
	defer ag.mu.RUnlock()

	node, ok := ag.nodes[path]
	if !ok {
		return nil, fmt.Errorf("node not found: %s", path)
	}
	return node, nil
}

// GetAllNodes returns all loaded nodes
func (ag *Agentize) GetAllNodes() map[string]*model.Node {
	ag.mu.RLock()
	defer ag.mu.RUnlock()

	// Return a copy to prevent external modification
	nodes := make(map[string]*model.Node)
	for k, v := range ag.nodes {
		nodes[k] = v
	}
	return nodes
}

// GetRoot returns the root node
func (ag *Agentize) GetRoot() *model.Node {
	ag.mu.RLock()
	defer ag.mu.RUnlock()
	return ag.root
}

// GetNodePaths returns all node paths in order (from root to deepest)
func (ag *Agentize) GetNodePaths() []string {
	ag.mu.RLock()
	defer ag.mu.RUnlock()

	paths := make([]string, 0, len(ag.nodes))
	visited := make(map[string]bool)

	// DFS traversal starting from root
	var traverse func(path string)
	traverse = func(path string) {
		if visited[path] {
			return
		}
		visited[path] = true
		paths = append(paths, path)

		children, err := ag.repo.GetChildren(path)
		if err == nil {
			for _, childPath := range children {
				traverse(childPath)
			}
		}
	}

	traverse("root")
	return paths
}

// Reload reloads all nodes from the filesystem
func (ag *Agentize) Reload() error {
	ag.mu.Lock()
	// Clear cache
	ag.nodes = make(map[string]*model.Node)
	ag.repo.InvalidateCache("")
	ag.mu.Unlock()

	// Reload all nodes (will acquire lock internally)
	return ag.loadAllNodes()
}

// ReloadNode reloads a specific node from the filesystem
func (ag *Agentize) ReloadNode(path string) error {
	ag.mu.Lock()
	defer ag.mu.Unlock()

	// Invalidate cache for this node
	ag.repo.InvalidateCache(path)
	delete(ag.nodes, path)

	// Reload the node and its children recursively
	return ag.loadNodeRecursiveLocked(path)
}

// GetRepository returns the underlying repository
func (ag *Agentize) GetRepository() *fsrepo.NodeRepository {
	return ag.repo
}

// GetSessionStore returns the session store
func (ag *Agentize) GetSessionStore() store.SessionStore {
	return ag.sessionStore
}

// GetToolStrategy returns the tool merge strategy
func (ag *Agentize) GetToolStrategy() model.MergeStrategy {
	return ag.toolStrategy
}

// GenerateGraphVisualization generates a graph visualization of the knowledge tree
// and saves it to an HTML file
func (ag *Agentize) GenerateGraphVisualization(filename string, title string) error {
	ag.mu.RLock()
	nodes := make(map[string]*model.Node)
	for k, v := range ag.nodes {
		nodes[k] = v
	}
	ag.mu.RUnlock()

	visualizer := visualize.NewGraphVisualizer(nodes)
	return visualizer.SaveToFile(filename, title)
}

// Version returns the current version of the library
func Version() string {
	return "0.1.0"
}

// GetRegisteredTools returns the list of registered tool names
// For Agentize, this returns nil as tools are registered externally
// Implementations like InfraAgentBot should override this method
func (ag *Agentize) GetRegisteredTools() []string {
	return nil
}
