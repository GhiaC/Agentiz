package agentize

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/pages"
	"github.com/ghiac/agentize/documents"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes registers HTTP routes on the given gin.Engine
// Routes: /agentize, /agentize/graph, /agentize/docs, /agentize/health, /agentize/debug/*
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
	router.GET("/agentize/debug/tool-calls/:toolCallID", ag.handleDebugToolCallDetail)
	router.GET("/agentize/debug/summarized", ag.handleDebugSummarized)
	router.GET("/agentize/debug/summarized/:logID", ag.handleDebugSummarizationLogDetail)

	// Register extra debug pages from applications
	for _, p := range ag.extraDebugPages {
		router.GET(p.Path, p.Handler)
	}
}

// handleIndex handles the main index page with links to graph and docs
func (ag *Agentize) handleIndex(c *gin.Context) {
	nodes := ag.GetAllNodes()
	nodeCount := len(nodes)

	html := fmt.Sprintf(indexPageTemplate, nodeCount)
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleGraph handles graph visualization requests
func (ag *Agentize) handleGraph(c *gin.Context) {
	tmpFile := filepath.Join(os.TempDir(), "agentize_graph.html")
	if err := ag.GenerateGraphVisualization(tmpFile, "Knowledge Tree Graph"); err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate graph: %v", err)})
		return
	}

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

// createDebugHandler creates a new debug handler with scheduler configuration
func (ag *Agentize) createDebugHandler() (*debuger.DebugHandler, error) {
	sessionStore := ag.GetSessionStore()
	if sessionStore == nil {
		return nil, fmt.Errorf("session store not available")
	}

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
	userID := c.Query("user")
	sessionID := c.Query("session")
	html, err := pages.RenderMessages(handler, page, userID, sessionID)
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
	sessionID := c.Query("session")
	html, err := pages.RenderToolCalls(handler, page, sessionID)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate tool calls page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugToolCallDetail handles tool call detail page requests
func (ag *Agentize) handleDebugToolCallDetail(c *gin.Context) {
	toolCallID := c.Param("toolCallID")
	if toolCallID == "" {
		c.JSON(400, gin.H{"error": "toolCallID parameter is required"})
		return
	}

	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	html, err := pages.RenderToolCallDetail(handler, toolCallID)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate tool call detail page: %v", err)})
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

// indexPageTemplate is the HTML template for the main index page
const indexPageTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Agentize - Knowledge Management</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            display: flex; justify-content: center; align-items: center;
            min-height: 100vh; margin: 0;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            padding: 2rem;
        }
        .container {
            background: white; padding: 4rem 3rem; border-radius: 20px;
            box-shadow: 0 20px 60px rgba(0, 0, 0, 0.3);
            text-align: center; max-width: 800px; width: 100%%;
        }
        .logo {
            font-size: 3.5rem; margin-bottom: 1rem;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            -webkit-background-clip: text; -webkit-text-fill-color: transparent;
            background-clip: text; font-weight: 700;
        }
        h1 { color: #2d3748; margin-bottom: 0.5rem; font-size: 2rem; font-weight: 600; }
        .subtitle { color: #718096; margin-bottom: 3rem; font-size: 1rem; }
        .stats {
            display: flex; justify-content: center; gap: 2rem; margin-bottom: 3rem;
            padding: 1.5rem; background: #f7fafc; border-radius: 12px;
        }
        .stat-item { display: flex; flex-direction: column; align-items: center; }
        .stat-value { font-size: 2rem; font-weight: 700; color: #667eea; margin-bottom: 0.25rem; }
        .stat-label { font-size: 0.875rem; color: #718096; text-transform: uppercase; letter-spacing: 0.05em; }
        .links { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 1.5rem; margin-top: 2rem; }
        .link-card {
            display: flex; flex-direction: column; align-items: center; padding: 2rem 1.5rem;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            color: white; text-decoration: none; border-radius: 16px;
            transition: all 0.3s cubic-bezier(0.4, 0, 0.2, 1);
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1); position: relative; overflow: hidden;
        }
        .link-card::before {
            content: ''; position: absolute; top: 0; left: 0; right: 0; bottom: 0;
            background: linear-gradient(135deg, rgba(255,255,255,0.2) 0%%, rgba(255,255,255,0) 100%%);
            opacity: 0; transition: opacity 0.3s ease;
        }
        .link-card:hover::before { opacity: 1; }
        .link-card:hover { transform: translateY(-8px) scale(1.02); box-shadow: 0 12px 24px rgba(102, 126, 234, 0.4); }
        .link-card:nth-child(1) { background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%); }
        .link-card:nth-child(2) { background: linear-gradient(135deg, #f093fb 0%%, #f5576c 100%%); }
        .link-card:nth-child(3) { background: linear-gradient(135deg, #4facfe 0%%, #00f2fe 100%%); }
        .link-icon { font-size: 3rem; margin-bottom: 1rem; display: block; }
        .link-title { font-size: 1.5rem; font-weight: 600; margin-bottom: 0.5rem; }
        .link-desc { font-size: 0.9rem; opacity: 0.9; line-height: 1.4; }
        @media (max-width: 640px) {
            .container { padding: 2rem 1.5rem; }
            .logo { font-size: 2.5rem; }
            h1 { font-size: 1.5rem; }
            .stats { flex-direction: column; gap: 1rem; }
            .links { grid-template-columns: 1fr; }
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
                <div class="stat-value">%d</div>
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
