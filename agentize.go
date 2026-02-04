package agentize

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ghiac/agentize/documents"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/llmutils"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/ghiac/agentize/visualize"
	"github.com/gin-gonic/gin"
)

// Agentize is the main entry point for the library
// It loads and manages the entire knowledge tree
type Agentize struct {
	// Core processing engine (holds repo, sessions, functions)
	engine *engine.Engine

	// Knowledge tree cache (for visualization/docs)
	nodes map[string]*model.Node
	mu    sync.RWMutex
}

// New creates a new Agentize instance by loading the entire knowledge tree from the given path
// It recursively traverses the directory structure and loads all nodes
func New(path string) (*Agentize, error) {
	return NewWithOptions(path, nil)
}

// Options allows configuring Agentize behavior
type Options struct {
	// SessionStore allows providing a custom session store
	SessionStore store.SessionStore
	// Repository allows providing an existing repository instead of creating a new one
	Repository *fsrepo.NodeRepository
}

// NewWithOptions creates a new Agentize instance with custom options
func NewWithOptions(path string, opts *Options) (*Agentize, error) {
	// Use existing repository or create a new one
	var repo *fsrepo.NodeRepository
	var err error
	if opts != nil && opts.Repository != nil {
		repo = opts.Repository
	} else {
		repo, err = fsrepo.NewNodeRepository(path)
		if err != nil {
			return nil, fmt.Errorf("failed to create repository: %w", err)
		}
	}

	// Determine session store
	sessionStore := store.SessionStore(store.NewMemoryStore())
	if opts != nil && opts.SessionStore != nil {
		sessionStore = opts.SessionStore
	}

	// Create engine
	eng := &engine.Engine{
		Repo:      repo,
		Sessions:  sessionStore,
		Functions: model.NewFunctionRegistry(),
	}
	eng.Executor = func(toolName string, args map[string]interface{}) (string, error) {
		if eng.Functions == nil {
			return "", fmt.Errorf("function registry is not configured")
		}
		return eng.Functions.Execute(toolName, args)
	}

	// Create Agentize instance
	ag := &Agentize{
		engine: eng,
		nodes:  make(map[string]*model.Node),
	}

	// Load all nodes recursively (for visualization cache)
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
	node, err := ag.engine.Repo.LoadNode(path)
	if err != nil {
		return fmt.Errorf("failed to load node %s: %w", path, err)
	}

	// Store the node
	ag.nodes[path] = node

	// Load all child nodes recursively
	children, err := ag.engine.Repo.GetChildren(path)
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
	return ag.nodes["root"]
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

		children, err := ag.engine.Repo.GetChildren(path)
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
	ag.engine.Repo.InvalidateCache("")
	ag.mu.Unlock()

	// Reload all nodes (will acquire lock internally)
	return ag.loadAllNodes()
}

// ReloadNode reloads a specific node from the filesystem
func (ag *Agentize) ReloadNode(path string) error {
	ag.mu.Lock()
	defer ag.mu.Unlock()

	// Invalidate cache for this node
	ag.engine.Repo.InvalidateCache(path)
	delete(ag.nodes, path)

	// Reload the node and its children recursively
	return ag.loadNodeRecursiveLocked(path)
}

// GetRepository returns the underlying repository
func (ag *Agentize) GetRepository() *fsrepo.NodeRepository {
	return ag.engine.Repo
}

// GetSessionStore returns the session store
func (ag *Agentize) GetSessionStore() store.SessionStore {
	return ag.engine.Sessions
}

// GetEngine returns the internal engine
func (ag *Agentize) GetEngine() *engine.Engine {
	return ag.engine
}

// UseLLMConfig configures the LLM client for the agentize instance
func (ag *Agentize) UseLLMConfig(config engine.LLMConfig) error {
	return ag.engine.UseLLMConfig(config)
}

// InitializeSummaries generates concise summaries for all nodes that don't have one.
// This should be called after UseLLMConfig to ensure the LLM is configured.
// It runs synchronously and may take time for large knowledge trees.
// If forceSummary is true, it will regenerate summaries for all nodes, even if they already have one.
func (ag *Agentize) InitializeSummaries(ctx context.Context, forceSummary bool) error {
	llmClient := ag.engine.GetLLMClient()
	if llmClient == nil {
		return fmt.Errorf("LLM client is not configured")
	}

	llmConfig := ag.engine.GetLLMConfig()

	// Determine model to use for summary generation
	modelName := llmConfig.CollectResultModel
	if modelName == "" {
		modelName = llmConfig.Model
	}

	// Create summary config
	summaryConfig := llmutils.SummaryConfig{
		Model: modelName,
	}

	// Set up the summary generator using llmutils
	ag.engine.Repo.SetSummaryGenerator(func(ctx context.Context, content string) (string, error) {
		return llmutils.GenerateSummary(ctx, llmClient, content, summaryConfig)
	})

	// Generate summaries (force regeneration if requested)
	return ag.engine.Repo.EnsureSummaries(ctx, forceSummary)
}

// UseFunctionRegistry configures the function registry for tool execution
func (ag *Agentize) UseFunctionRegistry(registry *model.FunctionRegistry) {
	ag.engine.UseFunctionRegistry(registry)
}

// ProcessMessage routes a user message through the LLM workflow and tool executor
func (ag *Agentize) ProcessMessage(
	ctx context.Context,
	sessionID string,
	userMessage string,
) (string, int, error) {
	return ag.engine.ProcessMessage(ctx, sessionID, userMessage)
}

// CreateSession initializes a fresh session anchored at the root node
func (ag *Agentize) CreateSession(userID string) (*model.Session, error) {
	return ag.engine.CreateSession(userID)
}

// SetProgress sets the progress state for a session
func (ag *Agentize) SetProgress(sessionID string, inProgress bool) error {
	return ag.engine.SetProgress(sessionID, inProgress)
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

// RegisterRoutes registers HTTP routes on the given gin.Engine
// Routes: /graph, /docs, /health
func (ag *Agentize) RegisterRoutes(router *gin.Engine) {
	router.GET("/agentize", ag.handleIndex)
	router.GET("/agentize/graph", ag.handleGraph)
	router.GET("/agentize/docs", ag.handleDocs)
	router.GET("/agentize/health", ag.handleHealth)
}

// handleIndex handles the main index page with links to graph and docs
func (ag *Agentize) handleIndex(c *gin.Context) {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Agentize</title>
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            min-height: 100vh;
            margin: 0;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
        }
        .container {
            background: white;
            padding: 3rem;
            border-radius: 12px;
            box-shadow: 0 10px 40px rgba(0, 0, 0, 0.2);
            text-align: center;
        }
        h1 {
            color: #333;
            margin-bottom: 2rem;
            font-size: 2.5rem;
        }
        .links {
            display: flex;
            gap: 1.5rem;
            justify-content: center;
            flex-wrap: wrap;
        }
        a {
            display: inline-block;
            padding: 1rem 2rem;
            background: #667eea;
            color: white;
            text-decoration: none;
            border-radius: 8px;
            font-weight: 600;
            transition: all 0.3s ease;
            font-size: 1.1rem;
        }
        a:hover {
            background: #5568d3;
            transform: translateY(-2px);
            box-shadow: 0 5px 15px rgba(102, 126, 234, 0.4);
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Agentize</h1>
        <div class="links">
            <a href="/agentize/graph">Graph</a>
            <a href="/agentize/docs">Docs</a>
        </div>
    </div>
</body>
</html>`
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleGraph handles graph visualization requests
func (ag *Agentize) handleGraph(c *gin.Context) {
	// Generate graph to a temporary file
	tmpFile := filepath.Join(os.TempDir(), "agentize_graph.html")
	if err := ag.GenerateGraphVisualization(tmpFile, "Knowledge Tree Graph"); err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate graph: %v", err)})
		return
	}

	// Read and serve the file
	content, err := os.ReadFile(tmpFile)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to read graph file: %v", err)})
		return
	}

	contentStr := strings.Replace(string(content),
		`<script src="https://go-echarts.github.io/go-echarts-assets/assets/echarts.min.js"></script>`,
		`<script src="https://cdn.jsdelivr.net/npm/echarts@5/dist/echarts.min.js"></script>`,
		-1)

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, contentStr)
}

// handleDocs handles documentation requests
func (ag *Agentize) handleDocs(c *gin.Context) {
	nodes := ag.GetAllNodes()
	repo := ag.GetRepository()

	doc := documents.NewAgentizeDocument(nodes, func(path string) ([]string, error) {
		return repo.GetChildren(path)
	})

	registeredTools := ag.GetRegisteredTools()
	html, err := doc.GenerateHTMLWithRegisteredTools(registeredTools)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate documentation: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, string(html))
}

// handleHealth handles health check requests
func (ag *Agentize) handleHealth(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":  "ok",
		"nodes":   len(ag.nodes),
		"version": Version(),
	})
}

// Version returns the current version of the library
func Version() string {
	return "0.1.0"
}

// GetRegisteredTools returns the list of registered tool names from the FunctionRegistry
func (ag *Agentize) GetRegisteredTools() []string {
	if ag.engine != nil && ag.engine.Functions != nil {
		return ag.engine.Functions.GetAllRegistered()
	}
	return nil
}
