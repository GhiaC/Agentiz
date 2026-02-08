package agentize

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/pages"
	"github.com/ghiac/agentize/documents"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/llmutils"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/ghiac/agentize/visualize"
	"github.com/gin-gonic/gin"
	"github.com/sashabaranov/go-openai"
)

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
	// FunctionRegistry allows providing an existing function registry instead of creating a new one
	FunctionRegistry *model.FunctionRegistry
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

// GetSchedulerMessageThreshold returns the message threshold from the engine's scheduler if available
func (ag *Agentize) GetSchedulerMessageThreshold() int {
	// Try to get from scheduler first
	ag.schedulerMu.RLock()
	if ag.scheduler != nil {
		threshold := ag.scheduler.GetMessageThreshold()
		ag.schedulerMu.RUnlock()
		return threshold
	}
	ag.schedulerMu.RUnlock()

	// Try engine scheduler
	if ag.engine != nil {
		return ag.engine.GetSchedulerMessageThreshold()
	}

	// Fallback: try environment variable
	if thresholdStr := os.Getenv("AGENTIZE_SCHEDULER_MESSAGE_THRESHOLD"); thresholdStr != "" {
		if threshold, err := strconv.Atoi(thresholdStr); err == nil && threshold > 0 {
			return threshold
		}
	}
	// Final fallback to default
	return 5
}

// UseLLMConfig configures the LLM client for the agentize instance
// It also automatically starts the scheduler if enabled
func (ag *Agentize) UseLLMConfig(config engine.LLMConfig) error {
	if err := ag.engine.UseLLMConfig(config); err != nil {
		return err
	}

	// Automatically start scheduler if LLM is configured
	// Use background context - scheduler will handle its own lifecycle
	ctx := context.Background()
	if err := ag.StartScheduler(ctx); err != nil {
		// Log warning but don't fail - scheduler is optional
		log.Log.Warnf("[Agentize] ⚠️  Failed to start scheduler: %v", err)
	}

	return nil
}

// StartScheduler starts the session scheduler if enabled
// It reads configuration from environment variables or uses defaults
// This should be called after UseLLMConfig to ensure LLM client is configured
func (ag *Agentize) StartScheduler(ctx context.Context) error {
	// Get LLM config from engine
	llmConfig := ag.engine.GetLLMConfig()
	if llmConfig.APIKey == "" {
		return fmt.Errorf("LLM client is not configured. Call UseLLMConfig first")
	}

	// Create a new LLM client with HTTP client wrapper that adds user_id header from context
	// This ensures that scheduler requests include user_id in headers like other LLM requests
	var baseHTTPClient *http.Client
	if llmConfig.HTTPClient != nil {
		baseHTTPClient = llmConfig.HTTPClient
	}
	llmClient := llmutils.NewOpenAIClientWithUserIDHeader(llmConfig.APIKey, llmConfig.BaseURL, baseHTTPClient)

	// Get session store from engine
	sessionStore := ag.engine.Sessions
	if sessionStore == nil {
		return fmt.Errorf("session store is not available")
	}

	// Create session handler from session store
	sessionHandlerConfig := model.DefaultSessionHandlerConfig()
	sessionHandler := model.NewSessionHandler(sessionStore, sessionHandlerConfig)

	// Set LLM client for session handler
	llmClientWrapper := &OpenAIClientWrapperForSessionHandler{
		Client: llmClient,
	}
	sessionHandler.SetLLMClient(llmClientWrapper)

	// Load scheduler config from environment or use defaults
	schedulerConfig := engine.DefaultSessionSchedulerConfig()

	// Override with environment variables if set
	if checkIntervalStr := os.Getenv("AGENTIZE_SCHEDULER_CHECK_INTERVAL_MINUTES"); checkIntervalStr != "" {
		if minutes, err := strconv.Atoi(checkIntervalStr); err == nil {
			schedulerConfig.CheckInterval = time.Duration(minutes) * time.Minute
		}
	}
	if thresholdStr := os.Getenv("AGENTIZE_SCHEDULER_SUBSEQUENT_TIME_THRESHOLD_MINUTES"); thresholdStr != "" {
		if minutes, err := strconv.Atoi(thresholdStr); err == nil {
			schedulerConfig.SubsequentTimeThreshold = time.Duration(minutes) * time.Minute
		}
	}
	if activityStr := os.Getenv("AGENTIZE_SCHEDULER_LAST_ACTIVITY_THRESHOLD_MINUTES"); activityStr != "" {
		if minutes, err := strconv.Atoi(activityStr); err == nil {
			schedulerConfig.LastActivityThreshold = time.Duration(minutes) * time.Minute
		}
	}
	if firstThresholdStr := os.Getenv("AGENTIZE_SCHEDULER_FIRST_THRESHOLD"); firstThresholdStr != "" {
		if threshold, err := strconv.Atoi(firstThresholdStr); err == nil {
			schedulerConfig.FirstSummarizationThreshold = threshold
		}
	}
	if subsequentThresholdStr := os.Getenv("AGENTIZE_SCHEDULER_SUBSEQUENT_MESSAGE_THRESHOLD"); subsequentThresholdStr != "" {
		if threshold, err := strconv.Atoi(subsequentThresholdStr); err == nil {
			schedulerConfig.SubsequentMessageThreshold = threshold
		}
	}
	if modelStr := os.Getenv("AGENTIZE_SCHEDULER_SUMMARY_MODEL"); modelStr != "" {
		schedulerConfig.SummaryModel = modelStr
	}

	// Check if scheduler is enabled
	if enabled := os.Getenv("AGENTIZE_SCHEDULER_ENABLED"); enabled == "false" {
		log.Log.Infof("[Agentize] ⏸️  Scheduler is disabled via AGENTIZE_SCHEDULER_ENABLED=false")
		return nil
	}

	// Create scheduler
	scheduler := engine.NewSessionScheduler(sessionHandler, llmClient, schedulerConfig)

	ag.schedulerMu.Lock()
	ag.scheduler = scheduler
	ag.schedulerMu.Unlock()

	// Start scheduler
	scheduler.Start(ctx)

	log.Log.Infof("[Agentize] ✅ Session scheduler started | CheckInterval: %v | FirstThreshold: %d msgs | SubsequentThreshold: %d msgs + %v | SummaryModel: %s",
		schedulerConfig.CheckInterval, schedulerConfig.FirstSummarizationThreshold, schedulerConfig.SubsequentMessageThreshold, schedulerConfig.SubsequentTimeThreshold, schedulerConfig.SummaryModel)

	return nil
}

// StopScheduler stops the session scheduler gracefully
func (ag *Agentize) StopScheduler() {
	ag.schedulerMu.Lock()
	scheduler := ag.scheduler
	ag.schedulerMu.Unlock()

	if scheduler != nil {
		scheduler.Stop()
		log.Log.Infof("[Agentize] 🛑 Session scheduler stopped")
	}
}

// GetScheduler returns the current scheduler instance
func (ag *Agentize) GetScheduler() *engine.SessionScheduler {
	ag.schedulerMu.RLock()
	defer ag.schedulerMu.RUnlock()
	return ag.scheduler
}

// GetSchedulerConfig returns the full scheduler configuration if available
// Returns nil if scheduler is not initialized
func (ag *Agentize) GetSchedulerConfig() *engine.SessionSchedulerConfig {
	// Try Agentize's scheduler first
	ag.schedulerMu.RLock()
	if ag.scheduler != nil {
		config := ag.scheduler.GetConfig()
		ag.schedulerMu.RUnlock()
		return &config
	}
	ag.schedulerMu.RUnlock()

	// Try engine's scheduler
	if ag.engine != nil {
		return ag.engine.GetSchedulerConfig()
	}

	return nil
}

// WaitForShutdown waits for shutdown signals and performs graceful shutdown
func (ag *Agentize) WaitForShutdown() {
	// Create a channel to listen for interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	// Wait for signal
	sig := <-sigChan
	log.Log.Infof("[Agentize] 📡 Received signal: %v, initiating graceful shutdown...", sig)

	// Stop scheduler gracefully
	ag.StopScheduler()

	log.Log.Infof("[Agentize] ✅ Graceful shutdown completed")
}

// OpenAIClientWrapperForSessionHandler wraps openai.Client to implement model.LLMClient interface for SessionHandler
type OpenAIClientWrapperForSessionHandler struct {
	Client *openai.Client
}

// CreateChatCompletion implements model.LLMClient interface
func (w *OpenAIClientWrapperForSessionHandler) CreateChatCompletion(ctx context.Context, request openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	return w.Client.CreateChatCompletion(ctx, request)
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
// Routes: /graph, /docs, /health, /debug
func (ag *Agentize) RegisterRoutes(router *gin.Engine) {
	router.GET("/agentize", ag.handleIndex)
	router.GET("/agentize/graph", ag.handleGraph)
	router.GET("/agentize/docs", ag.handleDocs)
	router.GET("/agentize/health", ag.handleHealth)
	router.GET("/agentize/debug", ag.handleDebug)
	router.GET("/agentize/debug/users", ag.handleDebugUsers)
	router.GET("/agentize/debug/users/:userID", ag.handleDebugUserDetail)
	router.GET("/agentize/debug/sessions", ag.handleDebugSessions)
	router.GET("/agentize/debug/sessions/:sessionID", ag.handleDebugSessionDetail)
	router.GET("/agentize/debug/messages", ag.handleDebugMessages)
	router.GET("/agentize/debug/files", ag.handleDebugFiles)
	router.GET("/agentize/debug/tool-calls", ag.handleDebugToolCalls)
	router.GET("/agentize/debug/summarized", ag.handleDebugSummarized)
	router.GET("/agentize/debug/summarized/:logID", ag.handleDebugSummarizationLogDetail)
}

// handleIndex handles the main index page with links to graph and docs
func (ag *Agentize) handleIndex(c *gin.Context) {
	nodes := ag.GetAllNodes()
	nodeCount := len(nodes)

	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Agentize - Knowledge Management</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            display: flex;
            justify-content: center;
            align-items: center;
            min-height: 100vh;
            margin: 0;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            padding: 2rem;
        }
        .container {
            background: white;
            padding: 4rem 3rem;
            border-radius: 20px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            text-align: center;
            max-width: 800px;
            width: 100%;
        }
        .logo {
            font-size: 3.5rem;
            margin-bottom: 1rem;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
            background-clip: text;
            font-weight: 700;
        }
        h1 {
            color: #2d3748;
            margin-bottom: 0.5rem;
            font-size: 2rem;
            font-weight: 600;
        }
        .subtitle {
            color: #718096;
            margin-bottom: 3rem;
            font-size: 1rem;
        }
        .stats {
            display: flex;
            justify-content: center;
            gap: 2rem;
            margin-bottom: 3rem;
            padding: 1.5rem;
            background: #f7fafc;
            border-radius: 12px;
        }
        .stat-item {
            display: flex;
            flex-direction: column;
            align-items: center;
        }
        .stat-value {
            font-size: 2rem;
            font-weight: 700;
            color: #667eea;
            margin-bottom: 0.25rem;
        }
        .stat-label {
            font-size: 0.875rem;
            color: #718096;
            text-transform: uppercase;
            letter-spacing: 0.05em;
        }
        .links {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 1.5rem;
            margin-top: 2rem;
        }
        .link-card {
            display: flex;
            flex-direction: column;
            align-items: center;
            padding: 2rem 1.5rem;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            text-decoration: none;
            border-radius: 16px;
            transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
            position: relative;
            overflow: hidden;
        }
        .link-card::before {
            content: '';
            position: absolute;
            top: 0;
            left: 0;
            right: 0;
            bottom: 0;
            background: linear-gradient(135deg, rgba(255,255,255,0.2) 0%, rgba(255,255,255,0) 100%);
            opacity: 0;
            transition: opacity 0.3s ease;
        }
        .link-card:hover::before {
            opacity: 1;
        }
        .link-card:hover {
            transform: translateY(-8px) scale(1.02);
            box-shadow: 0 12px 24px rgba(102, 126, 234, 0.4);
        }
        .link-card:nth-child(1) {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
        }
        .link-card:nth-child(2) {
            background: linear-gradient(135deg, #f093fb 0%, #f5576c 100%);
        }
        .link-card:nth-child(3) {
            background: linear-gradient(135deg, #4facfe 0%, #00f2fe 100%);
        }
        .link-icon {
            font-size: 3rem;
            margin-bottom: 1rem;
            display: block;
        }
        .link-title {
            font-size: 1.5rem;
            font-weight: 600;
            margin-bottom: 0.5rem;
        }
        .link-desc {
            font-size: 0.9rem;
            opacity: 0.9;
            line-height: 1.4;
        }
        @media (max-width: 640px) {
            .container {
                padding: 2rem 1.5rem;
            }
            .logo {
                font-size: 2.5rem;
            }
            h1 {
                font-size: 1.5rem;
            }
            .stats {
                flex-direction: column;
                gap: 1rem;
            }
            .links {
                grid-template-columns: 1fr;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="logo">🧠</div>
        <h1>Agentize</h1>
        <p class="subtitle">Knowledge Management & Visualization Platform</p>
        <div class="stats">
            <div class="stat-item">
                <div class="stat-value">` + fmt.Sprintf("%d", nodeCount) + `</div>
                <div class="stat-label">Nodes</div>
            </div>
        </div>
        <div class="links">
            <a href="/agentize/graph" class="link-card">
                <span class="link-icon">📊</span>
                <div class="link-title">Graph</div>
                <div class="link-desc">Visualize knowledge tree structure</div>
            </a>
            <a href="/agentize/docs" class="link-card">
                <span class="link-icon">📚</span>
                <div class="link-title">Documentation</div>
                <div class="link-desc">Browse knowledge base</div>
            </a>
            <a href="/agentize/debug" class="link-card">
                <span class="link-icon">🔍</span>
                <div class="link-title">Debug</div>
                <div class="link-desc">View sessions and messages</div>
            </a>
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

// createDebugHandler creates a new debug handler with scheduler configuration
func (ag *Agentize) createDebugHandler() (*debuger.DebugHandler, error) {
	sessionStore := ag.GetSessionStore()
	if sessionStore == nil {
		return nil, fmt.Errorf("session store not available")
	}

	// Get scheduler configuration
	var schedulerConfig *debuger.SchedulerConfig
	if engineConfig := ag.GetSchedulerConfig(); engineConfig != nil {
		schedulerConfig = &debuger.SchedulerConfig{
			CheckInterval:                   engineConfig.CheckInterval,
			FirstSummarizationThreshold:     engineConfig.FirstSummarizationThreshold,
			SubsequentMessageThreshold:      engineConfig.SubsequentMessageThreshold,
			SubsequentTimeThreshold:         engineConfig.SubsequentTimeThreshold,
			LastActivityThreshold:           engineConfig.LastActivityThreshold,
			ImmediateSummarizationThreshold: engineConfig.ImmediateSummarizationThreshold,
			SummaryModel:                    engineConfig.SummaryModel,
		}
	}

	return debuger.NewDebugHandlerWithConfig(sessionStore, schedulerConfig)
}

// handleDebug handles debug page requests for dashboard
func (ag *Agentize) handleDebug(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	html, err := pages.RenderDashboard(handler)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate debug page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugUsers handles users list page requests
func (ag *Agentize) handleDebugUsers(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	page := getPageParam(c)
	html, err := pages.RenderUsers(handler, page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate users page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// getPageParam extracts page number from query params (defaults to 1)
func getPageParam(c *gin.Context) int {
	pageStr := c.Query("page")
	if pageStr == "" {
		return 1
	}
	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 1 {
		return 1
	}
	return page
}

// handleDebugUserDetail handles user detail page requests
func (ag *Agentize) handleDebugUserDetail(c *gin.Context) {
	userID := c.Param("userID")
	if userID == "" {
		c.JSON(400, gin.H{"error": "userID parameter is required"})
		return
	}

	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	html, err := pages.RenderUserDetail(handler, userID)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate user detail page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugSessions handles sessions list page requests
func (ag *Agentize) handleDebugSessions(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	page := getPageParam(c)
	html, err := pages.RenderSessions(handler, page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate sessions page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugSessionDetail handles session detail page requests
func (ag *Agentize) handleDebugSessionDetail(c *gin.Context) {
	sessionID := c.Param("sessionID")
	if sessionID == "" {
		c.JSON(400, gin.H{"error": "sessionID parameter is required"})
		return
	}

	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	html, err := pages.RenderSessionDetail(handler, sessionID)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate session detail page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugMessages handles messages list page requests
func (ag *Agentize) handleDebugMessages(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	page := getPageParam(c)
	html, err := pages.RenderMessages(handler, page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate messages page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugFiles handles opened files list page requests
func (ag *Agentize) handleDebugFiles(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	page := getPageParam(c)
	html, err := pages.RenderFiles(handler, page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate files page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugToolCalls handles tool calls list page requests
func (ag *Agentize) handleDebugToolCalls(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	page := getPageParam(c)
	html, err := pages.RenderToolCalls(handler, page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate tool calls page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugSummarized handles summarization logs list page requests
func (ag *Agentize) handleDebugSummarized(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	page := getPageParam(c)
	html, err := pages.RenderSummarized(handler, page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate summarization logs page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugSummarizationLogDetail handles summarization log detail page requests
func (ag *Agentize) handleDebugSummarizationLogDetail(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	logID := c.Param("logID")
	html, err := pages.RenderSummarizationLogDetail(handler, logID)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate summarization log detail page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}
