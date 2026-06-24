package agentize

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ghiac/agentize/core"
	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/pages"
	"github.com/ghiac/agentize/documents"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes registers HTTP routes on the given gin.Engine
// Routes: /agentize, /agentize/graph, /agentize/docs, /agentize/health, /agentize/debug/*
//
// When admin credentials are configured (SetAdminCredentials or the
// AGENTIZE_ADMIN_USERNAME / AGENTIZE_ADMIN_PASSWORD env vars), every route
// except /agentize/health and the login endpoints requires a signed-in admin.
//
// Prometheus metrics are NOT served on this router. Set AGENTIZE_METRICS_ADDR
// (e.g. ":9091") to expose them on a dedicated, unauthenticated port instead —
// see startMetricsServerFromEnv.
func (ag *Agentize) RegisterRoutes(router *gin.Engine) {
	ag.initAdminAuth()
	ag.startMetricsServerFromEnv()

	// The health check is always available (no admin session needed).
	router.GET("/agentize/health", ag.handleHealth)

	// Safe by default: the dashboard can read conversations and delete user data,
	// so when NO admin credentials are configured it is registered ONLY if the
	// operator explicitly opts into the unauthenticated mode for local dev
	// (AGENTIZE_DEBUG_UNSAFE=1). In production, set AGENTIZE_ADMIN_USERNAME/PASSWORD
	// (or call SetAdminCredentials) — then the pages register behind the login.
	if !ag.adminAuthEnabled() && strings.TrimSpace(os.Getenv("AGENTIZE_DEBUG_UNSAFE")) != "1" {
		log.Log.Warnf("[Agentize] 🔒 /agentize admin pages NOT registered — set AGENTIZE_ADMIN_USERNAME/PASSWORD (or SetAdminCredentials) to protect them, or AGENTIZE_DEBUG_UNSAFE=1 to expose them unauthenticated for local dev")
		return
	}
	if !ag.adminAuthEnabled() {
		log.Log.Warnf("[Agentize] ⚠️  AGENTIZE_DEBUG_UNSAFE=1 — /agentize admin pages are served UNAUTHENTICATED (intended for local dev only)")
	}

	if ag.rawFileLimiter == nil {
		// Throttle raw user-file downloads to 10/min per IP (burst 10), guarding
		// against bulk exfiltration by fileID enumeration even when authenticated.
		ag.rawFileLimiter = newIPRateLimiter(10, 10)
	}

	// Login endpoints (no admin session required, but only meaningful when auth is on).
	router.GET(adminLoginPath, ag.handleLoginPage)
	router.POST(adminLoginPath, ag.handleLoginSubmit)
	router.GET("/agentize/logout", ag.handleLogout)

	p := ag.requireAdmin
	router.GET("/agentize", p(ag.handleIndex))
	router.GET("/agentize/graph", p(ag.handleGraph))
	router.GET("/agentize/docs", p(ag.handleDocs))
	router.GET("/agentize/debug", p(ag.handleDebug))
	router.GET("/agentize/debug/users", p(ag.handleDebugUsers))
	router.GET("/agentize/debug/users/:userID", p(ag.handleDebugUserDetail))
	router.POST("/agentize/debug/users/:userID/delete-data", p(ag.handleDebugUserDeleteData))
	router.GET("/agentize/debug/sessions", p(ag.handleDebugSessions))
	router.GET("/agentize/debug/sessions/:sessionID", p(ag.handleDebugSessionDetail))
	router.GET("/agentize/debug/plans", p(ag.handleDebugPlans))
	router.GET("/agentize/debug/plans/:planID", p(ag.handleDebugPlanDetail))
	router.GET("/agentize/debug/messages", p(ag.handleDebugMessages))
	router.GET("/agentize/debug/files", p(ag.handleDebugFiles))
	router.GET("/agentize/debug/documents", p(ag.handleDebugDocuments))
	router.GET("/agentize/debug/documents/:fileID/raw", p(ag.handleDebugDocumentRaw))
	router.GET("/agentize/debug/tool-calls", p(ag.handleDebugToolCalls))
	router.GET("/agentize/debug/tool-calls/:toolID", p(ag.handleDebugToolCallDetail))
	router.GET("/agentize/debug/routes", p(ag.handleDebugRoutes))
	router.GET("/agentize/debug/routes/:traceID", p(ag.handleDebugRouteDetail))
	router.GET("/agentize/debug/summarized", p(ag.handleDebugSummarized))
	router.GET("/agentize/debug/summarized/:logID", p(ag.handleDebugSummarizationLogDetail))
	router.GET("/agentize/debug/reviews", p(ag.handleDebugReviews))
	router.POST("/agentize/debug/reviews/:reviewID/resolve", p(ag.handleDebugReviewResolve))

	// Register extra debug pages (with sidebar entry)
	for _, page := range ag.extraDebugPages {
		router.GET(page.Path, p(page.Handler))
	}
	// Register extra debug routes (no sidebar entry, e.g. detail pages)
	for _, page := range ag.extraDebugRoutes {
		router.GET(page.Path, p(page.Handler))
	}
}

// startMetricsServerFromEnv exposes the Prometheus metrics on a dedicated,
// unauthenticated port when AGENTIZE_METRICS_ADDR is set (e.g. ":9091"). The
// metrics are deliberately kept off the main /agentize server, which sits behind
// the interactive admin login a scraper cannot satisfy. Ensure the dedicated
// port is reachable only by Prometheus (bind an internal interface / restrict at
// the network layer). When the variable is unset, metrics are not exposed.
func (ag *Agentize) startMetricsServerFromEnv() {
	addr := os.Getenv("AGENTIZE_METRICS_ADDR")
	if addr == "" {
		log.Log.Infof("[Agentize] ℹ️  Metrics endpoint not exposed — set AGENTIZE_METRICS_ADDR (e.g. \":9091\") to serve Prometheus metrics on a dedicated port")
		return
	}
	if _, err := metrics.StartServer(addr); err != nil {
		log.Log.Errorf("[Agentize] ⚠️  Failed to start metrics server on %s: %v", addr, err)
		return
	}
	log.Log.Infof("[Agentize] 📊 Metrics server listening on %s (/metrics, /agentize/metrics)", addr)
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

	handler, err := debuger.NewDebugHandlerWithConfig(sessionStore, schedulerConfig)
	if err != nil {
		return nil, err
	}
	if ag.userBillingHTMLProvider != nil {
		handler.SetUserBillingHTMLProvider(ag.userBillingHTMLProvider)
	}

	// Wire the "Core System Prompt" card. A host with a live Core sets the
	// provider (e.g. coreHandler.SystemPromptSectionsFor); otherwise fall back to
	// a store-only preview so the card still shows the controller rules and this
	// user's memory/files/sessions, flagged as a preview.
	if ag.coreSystemPromptProvider != nil {
		handler.SetCoreSystemPromptProvider(ag.coreSystemPromptProvider, false)
	} else if sessionStore := ag.GetSessionStore(); sessionStore != nil {
		handler.SetCoreSystemPromptProvider(func(userID string) ([]model.PromptSection, error) {
			return core.PreviewSystemPromptSections(sessionStore, userID, core.CoreHandlerConfig{})
		}, true)
	}

	return handler, nil
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

	sysInfo := ag.SystemInfo()
	html, err := pages.RenderDashboardWithPlanning(handler, ag.GetPlanStore() != nil, &sysInfo)
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

	showDeleted := c.Query("deleted") == "1"
	html, err := pages.RenderUserDetail(handler, userID, showDeleted)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate user detail page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugReviews lists pending human-in-the-loop reviews across all users.
func (ag *Agentize) handleDebugReviews(c *gin.Context) {
	reviews, err := ag.ListPendingReviews(c.Request.Context(), "")
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to load reviews: %v", err)})
		return
	}
	html, err := pages.RenderReviews(reviews)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate reviews page: %v", err)})
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugReviewResolve records an approve/reject decision for one review. It
// is audited and routes through the same Manager.Resolve every UI uses, so a
// plan suspended on the review resumes (when ResumeOnReview is wired).
func (ag *Agentize) handleDebugReviewResolve(c *gin.Context) {
	reviewID := c.Param("reviewID")
	if reviewID == "" {
		c.JSON(400, gin.H{"error": "reviewID parameter is required"})
		return
	}
	// Typed-confirmation guard (CSRF defense-in-depth on top of SameSite=Lax):
	// the caller must echo the review id in ?confirm=, which a forged cross-site
	// POST cannot supply (review ids are random). Matches the delete-data endpoint.
	if c.Query("confirm") != reviewID {
		log.Log.Warnf("[Agentize] [AUDIT] resolve-review REJECTED (confirmation mismatch) | review=%s ip=%s", reviewID, c.ClientIP())
		metrics.AuditAction("resolve_review", "rejected")
		c.JSON(400, gin.H{"error": "confirmation required: re-submit with ?confirm=<reviewID> matching the review"})
		return
	}
	decision := c.PostForm("decision")
	note := c.PostForm("note")
	// The dashboard only offers approve/reject; reject anything else so a tampered
	// or forged form cannot drive an arbitrary decision.
	if decision != "approve" && decision != "reject" {
		log.Log.Warnf("[Agentize] [AUDIT] resolve-review REJECTED (bad decision %q) | review=%s ip=%s", decision, reviewID, c.ClientIP())
		metrics.AuditAction("resolve_review", "rejected")
		c.JSON(400, gin.H{"error": "decision must be \"approve\" or \"reject\""})
		return
	}

	log.Log.Warnf("[Agentize] [AUDIT] resolve-review START | review=%s decision=%s ip=%s", reviewID, decision, c.ClientIP())
	if _, err := ag.ResolveReview(c.Request.Context(), reviewID, decision, note, "admin:"+c.ClientIP()); err != nil {
		metrics.AuditAction("resolve_review", "error")
		log.Log.Errorf("[Agentize] [AUDIT] resolve-review FAILED | review=%s ip=%s error=%v", reviewID, c.ClientIP(), err)
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to resolve review: %v", err)})
		return
	}
	metrics.AuditAction("resolve_review", "ok")
	log.Log.Warnf("[Agentize] [AUDIT] resolve-review OK | review=%s decision=%s ip=%s", reviewID, decision, c.ClientIP())
	c.Redirect(302, "/agentize/debug/reviews")
}

// handleDebugUserDeleteData deletes all sessions and messages for a user
func (ag *Agentize) handleDebugUserDeleteData(c *gin.Context) {
	userID := c.Param("userID")
	if userID == "" {
		c.JSON(400, gin.H{"error": "userID parameter is required"})
		return
	}

	// Typed-confirmation guard: the caller must echo the exact target userID in a
	// ?confirm= param, so a blind or forged POST to the path cannot wipe data.
	if c.Query("confirm") != userID {
		log.Log.Warnf("[Agentize] [AUDIT] delete-user-data REJECTED (confirmation mismatch) | user=%s ip=%s", userID, c.ClientIP())
		metrics.AuditAction("delete_user_data", "rejected")
		c.JSON(400, gin.H{"error": "confirmation required: re-submit with ?confirm=<userID> matching the target user"})
		return
	}

	// Audit the start of a destructive operation before doing any work.
	log.Log.Warnf("[Agentize] [AUDIT] delete-user-data START | user=%s ip=%s", userID, c.ClientIP())

	handler, err := ag.createDebugHandler()
	if err != nil {
		metrics.AuditAction("delete_user_data", "error")
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	// Remove stored file bytes first (best-effort) so they are not orphaned once
	// the metadata rows are deleted by DeleteUserData below.
	if err := ag.engine.DeleteUserFileBytes(userID); err != nil {
		log.Log.Warnf("[Agentize] ⚠️  Failed to delete some user file bytes | UserID: %s | Error: %v", userID, err)
	}

	if err := handler.GetStore().DeleteUserData(userID); err != nil {
		metrics.AuditAction("delete_user_data", "error")
		log.Log.Errorf("[Agentize] [AUDIT] delete-user-data FAILED | user=%s ip=%s error=%v", userID, c.ClientIP(), err)
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to delete user data: %v", err)})
		return
	}

	if ag.userDeleteDataHook != nil {
		if err := ag.userDeleteDataHook(userID); err != nil {
			metrics.AuditAction("delete_user_data", "error")
			log.Log.Errorf("[Agentize] [AUDIT] delete-user-data hook FAILED | user=%s ip=%s error=%v", userID, c.ClientIP(), err)
			c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to delete user billing/quota data: %v", err)})
			return
		}
	}

	metrics.AuditAction("delete_user_data", "ok")
	log.Log.Warnf("[Agentize] [AUDIT] delete-user-data OK | user=%s ip=%s", userID, c.ClientIP())
	c.Redirect(302, "/agentize/debug/users/"+url.PathEscape(userID)+"?deleted=1")
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

// handleDebugPlans handles plans list page requests (planning execution plans)
func (ag *Agentize) handleDebugPlans(c *gin.Context) {
	page := getPageParam(c)
	html, err := pages.RenderPlans(ag.GetPlanStore(), page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate plans page: %v", err)})
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugPlanDetail handles plan detail page requests
func (ag *Agentize) handleDebugPlanDetail(c *gin.Context) {
	planID := c.Param("planID")
	if planID == "" {
		c.JSON(400, gin.H{"error": "planID parameter is required"})
		return
	}
	html, err := pages.RenderPlanDetail(ag.GetPlanStore(), planID)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate plan detail page: %v", err)})
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

// handleDebugDocuments handles the user documents (files) list page requests
func (ag *Agentize) handleDebugDocuments(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	page := getPageParam(c)
	html, err := pages.RenderDocuments(handler, page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate documents page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugDocumentRaw streams the bytes of a stored user file. By default the
// file is served inline so images preview in the browser; add ?download=1 to
// force a download.
func (ag *Agentize) handleDebugDocumentRaw(c *gin.Context) {
	fileID := c.Param("fileID")
	if fileID == "" {
		c.JSON(400, gin.H{"error": "fileID parameter is required"})
		return
	}

	if ag.rawFileLimiter != nil && !ag.rawFileLimiter.allow(c.ClientIP()) {
		c.Header("Retry-After", "6")
		c.JSON(429, gin.H{"error": "rate limit exceeded for raw file downloads; slow down"})
		return
	}

	data, meta, err := ag.ReadUserFile(fileID)
	if err != nil {
		c.JSON(404, gin.H{"error": fmt.Sprintf("file not found: %v", err)})
		return
	}

	mimeType := meta.MIMEType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	disposition := "inline"
	if c.Query("download") == "1" {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, sanitizeContentDispositionName(meta.Name)))
	c.Data(200, mimeType, data)
}

// sanitizeContentDispositionName strips characters that would break a
// Content-Disposition header's quoted filename.
func sanitizeContentDispositionName(name string) string {
	if name == "" {
		return "file"
	}
	return strings.NewReplacer("\"", "", "\\", "", "\n", "", "\r", "").Replace(name)
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
	toolID := c.Param("toolID")
	if toolID == "" {
		c.JSON(400, gin.H{"error": "toolID parameter is required"})
		return
	}

	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	html, err := pages.RenderToolCallDetail(handler, toolID)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate tool call detail page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugRoutes handles the routing-DAG list page (one trace per user message)
func (ag *Agentize) handleDebugRoutes(c *gin.Context) {
	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	page := getPageParam(c)
	html, err := pages.RenderRoutes(handler, page)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate routes page: %v", err)})
		return
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(200, html)
}

// handleDebugRouteDetail handles the routing-DAG detail page (interactive graph)
func (ag *Agentize) handleDebugRouteDetail(c *gin.Context) {
	traceID := c.Param("traceID")
	if traceID == "" {
		c.JSON(400, gin.H{"error": "traceID parameter is required"})
		return
	}

	handler, err := ag.createDebugHandler()
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	html, err := pages.RenderRouteDetail(handler, traceID)
	if err != nil {
		c.JSON(500, gin.H{"error": fmt.Sprintf("Failed to generate route detail page: %v", err)})
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
