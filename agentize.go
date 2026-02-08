package agentize

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/llmutils"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/ghiac/agentize/visualize"
)

// Version returns the current version of the library
func Version() string {
	return "0.1.0"
}

// Agentize is the main entry point for the library
// It loads and manages the entire knowledge tree
type Agentize struct {
	// Core processing engine (holds repo, sessions, functions)
	engine *engine.Engine

	// Knowledge tree cache (for visualization/docs)
	nodes map[string]*model.Node
	mu    sync.RWMutex

	// Session scheduler for automatic summarization
	scheduler   *engine.SessionScheduler
	schedulerMu sync.RWMutex
}

// Options allows configuring Agentize behavior
type Options struct {
	// SessionStore allows providing a custom session store
	SessionStore store.SessionStore
	// Repository allows providing an existing repository instead of creating a new one
	Repository *fsrepo.NodeRepository
	// FunctionRegistry allows providing an existing function registry instead of creating a new one
	FunctionRegistry *model.FunctionRegistry
}

// New creates a new Agentize instance by loading the entire knowledge tree from the given path
func New(path string) (*Agentize, error) {
	return NewWithOptions(path, nil)
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
	var sessionStore store.SessionStore
	if opts != nil && opts.SessionStore != nil {
		sessionStore = opts.SessionStore
	} else {
		dbStore, err := store.NewDBStore()
		if err != nil {
			return nil, fmt.Errorf("failed to create DBStore: %w", err)
		}
		sessionStore = dbStore
	}

	// Determine function registry
	functionRegistry := model.NewFunctionRegistry()
	if opts != nil && opts.FunctionRegistry != nil {
		functionRegistry = opts.FunctionRegistry
	}

	// Create engine
	eng := &engine.Engine{
		Repo:      repo,
		Sessions:  sessionStore,
		Functions: functionRegistry,
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

// ============================================================================
// Node Management
// ============================================================================

// loadAllNodes recursively loads all nodes from the knowledge tree
func (ag *Agentize) loadAllNodes() error {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return ag.loadNodeRecursiveLocked("root")
}

// loadNodeRecursiveLocked recursively loads a node and all its children
// Must be called with ag.mu.Lock() already held
func (ag *Agentize) loadNodeRecursiveLocked(path string) error {
	if _, exists := ag.nodes[path]; exists {
		return nil
	}

	node, err := ag.engine.Repo.LoadNode(path)
	if err != nil {
		return fmt.Errorf("failed to load node %s: %w", path, err)
	}

	ag.nodes[path] = node

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
	ag.nodes = make(map[string]*model.Node)
	ag.engine.Repo.InvalidateCache("")
	ag.mu.Unlock()

	return ag.loadAllNodes()
}

// ReloadNode reloads a specific node from the filesystem
func (ag *Agentize) ReloadNode(path string) error {
	ag.mu.Lock()
	defer ag.mu.Unlock()

	ag.engine.Repo.InvalidateCache(path)
	delete(ag.nodes, path)

	return ag.loadNodeRecursiveLocked(path)
}

// ============================================================================
// Engine & Store Accessors
// ============================================================================

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

// GetRegisteredTools returns the list of registered tool names from the FunctionRegistry
func (ag *Agentize) GetRegisteredTools() []string {
	if ag.engine != nil && ag.engine.Functions != nil {
		return ag.engine.Functions.GetAllRegistered()
	}
	return nil
}

// ============================================================================
// LLM Configuration
// ============================================================================

// UseLLMConfig configures the LLM client for the agentize instance
// It also automatically starts the scheduler if enabled
func (ag *Agentize) UseLLMConfig(config engine.LLMConfig) error {
	if err := ag.engine.UseLLMConfig(config); err != nil {
		return err
	}

	// Automatically start scheduler if LLM is configured
	ctx := context.Background()
	if err := ag.StartScheduler(ctx); err != nil {
		log.Log.Warnf("[Agentize] ⚠️  Failed to start scheduler: %v", err)
	}

	return nil
}

// UseFunctionRegistry configures the function registry for tool execution
func (ag *Agentize) UseFunctionRegistry(registry *model.FunctionRegistry) {
	ag.engine.UseFunctionRegistry(registry)
}

// InitializeSummaries generates concise summaries for all nodes that don't have one
func (ag *Agentize) InitializeSummaries(ctx context.Context, forceSummary bool) error {
	llmClient := ag.engine.GetLLMClient()
	if llmClient == nil {
		return fmt.Errorf("LLM client is not configured")
	}

	llmConfig := ag.engine.GetLLMConfig()

	modelName := llmConfig.CollectResultModel
	if modelName == "" {
		modelName = llmConfig.Model
	}

	summaryConfig := llmutils.SummaryConfig{Model: modelName}

	ag.engine.Repo.SetSummaryGenerator(func(ctx context.Context, content string) (string, error) {
		return llmutils.GenerateSummary(ctx, llmClient, content, summaryConfig)
	})

	return ag.engine.Repo.EnsureSummaries(ctx, forceSummary)
}

// ============================================================================
// Session & Message Processing
// ============================================================================

// ProcessMessage routes a user message through the LLM workflow and tool executor
func (ag *Agentize) ProcessMessage(ctx context.Context, sessionID string, userMessage string) (string, int, error) {
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

// ============================================================================
// Visualization
// ============================================================================

// GenerateGraphVisualization generates a graph visualization of the knowledge tree
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

// ============================================================================
// Lifecycle
// ============================================================================

// WaitForShutdown waits for shutdown signals and performs graceful shutdown
func (ag *Agentize) WaitForShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigChan
	log.Log.Infof("[Agentize] 📡 Received signal: %v, initiating graceful shutdown...", sig)

	ag.StopScheduler()

	log.Log.Infof("[Agentize] ✅ Graceful shutdown completed")
}
