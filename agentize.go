package agentize

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/filestore"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/imageedit"
	"github.com/ghiac/agentize/llmutils"
	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/review"
	"github.com/ghiac/agentize/store"
	"github.com/ghiac/agentize/visualize"
	"github.com/gin-gonic/gin"
)

// Version returns the current version of the library
func Version() string {
	return "0.1.0"
}

// DebugPage represents an external page added to the debug panel.
type DebugPage struct {
	Path    string          // e.g., "/agentize/debug/quota"
	Title   string          // Nav label
	Icon    string          // Nav icon (emoji or Bootstrap icon name for cards)
	Handler gin.HandlerFunc // Route handler
	// NoNav, when true, registers the route but does not add an item to the sidebar.
	NoNav bool
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

	// Extra debug pages registered by applications (shown in sidebar)
	extraDebugPages []DebugPage
	// Extra debug routes without sidebar entry (e.g. detail pages)
	extraDebugRoutes []DebugPage

	// Optional: provider for user billing/credit HTML on debug user detail page
	userBillingHTMLProvider debuger.UserBillingHTMLProvider

	// Optional: provider for the Core agent's live system-prompt sections on the
	// debug user detail page. Wire it to a Core via SetCoreSystemPromptProvider
	// (e.g. coreHandler.SystemPromptSectionsFor); when unset, a store-only preview
	// is installed as the default in createDebugHandler.
	coreSystemPromptProvider debuger.CoreSystemPromptProvider

	// Optional: provider for extra rows appended to the System Info panel's "more
	// info" section (e.g. application config provenance). Called on each render.
	extraSystemInfoProvider func() []debuger.InfoKV

	// Optional: hook called after DeleteUserData (sessions/messages) so app can delete quota/consumption etc.
	userDeleteDataHook func(userID string) error

	// Human-in-the-loop reviews. The manager is created lazily (the store always
	// supports reviews); a metrics ResolveListener is registered on creation.
	reviewManager *review.Manager
	reviewMu      sync.Mutex

	// Admin credentials protecting the /agentize web pages. When empty, the
	// values of AGENTIZE_ADMIN_USERNAME / AGENTIZE_ADMIN_PASSWORD are used; if
	// those are also empty, the pages are served without authentication.
	adminUsername string
	adminPassword string

	// Per-IP rate limiter for raw user-file downloads (defense against bulk
	// exfiltration by fileID enumeration, even for authenticated admins).
	rawFileLimiter *ipRateLimiter
}

// Options allows configuring Agentize behavior
type Options struct {
	// SessionStore allows providing a custom session store
	SessionStore store.SessionStore
	// Repository allows providing an existing repository instead of creating a new one
	Repository *fsrepo.NodeRepository
	// FunctionRegistry allows providing an existing function registry instead of creating a new one
	FunctionRegistry *model.FunctionRegistry
	// FileStore allows providing a custom pluggable byte storage for user files.
	// When nil, a local-disk store rooted at FileStoreDir is used.
	FileStore filestore.FileStore
	// FileStoreDir is the base directory for the default local file store.
	// Ignored when FileStore is provided. Defaults to "./data/files".
	FileStoreDir string
	// ImageEditor, when set, enables the manage_files edit_image action by
	// wiring an image-capable model/API. Can also be set later via SetImageEditor.
	ImageEditor engine.ImageEditorFunc
	// DisableToolApprovals opts out of the default human approval gate. By
	// default every tool call is persisted as a review and waits for an explicit
	// approve/reject decision before execution.
	DisableToolApprovals bool
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

	// The engine relies on the full, unified store.Store contract (sessions, users,
	// messages, files, tool calls, summarization logs, visited nodes). Every
	// built-in backend implements it; reject anything that doesn't, loudly and at
	// startup, instead of silently skipping persistence later.
	fullStore, ok := sessionStore.(store.Store)
	if !ok {
		return nil, fmt.Errorf("session store (%T) must implement the full store.Store interface", sessionStore)
	}

	// Wrap the store so every database operation is timed
	// (agentize_store_query_duration_seconds{operation,backend}). The wrapper is
	// a transparent pass-through; it does not change any behavior.
	fullStore = store.NewMetered(fullStore)

	// Determine function registry
	functionRegistry := model.NewFunctionRegistry()
	if opts != nil && opts.FunctionRegistry != nil {
		functionRegistry = opts.FunctionRegistry
	}

	// Determine file store (pluggable byte storage for user files)
	var fileStore filestore.FileStore
	if opts != nil && opts.FileStore != nil {
		fileStore = opts.FileStore
	} else {
		dir := "./data/files"
		if opts != nil && opts.FileStoreDir != "" {
			dir = opts.FileStoreDir
		}
		localStore, err := filestore.NewLocalFileStore(dir)
		if err != nil {
			return nil, fmt.Errorf("failed to create file store: %w", err)
		}
		fileStore = localStore
	}

	// Create engine
	eng := &engine.Engine{
		Repo:      repo,
		Sessions:  fullStore,
		Functions: functionRegistry,
		Files:     fileStore,
	}
	if opts != nil && opts.ImageEditor != nil {
		eng.ImageEditor = opts.ImageEditor
	}
	eng.Executor = func(toolName string, args map[string]interface{}) (string, error) {
		if eng.Functions == nil {
			return "", fmt.Errorf("function registry is not configured")
		}
		return eng.Functions.Execute(toolName, args)
	}

	// Initialize engine (session mutexes, progress guard, DB readiness).
	// Required before any ProcessMessage to avoid nil pointer dereference on sessionProgress.
	if err := eng.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize engine: %w", err)
	}

	// Register the file-manager tool (manage_files) so the agent can use files.
	eng.RegisterManageFilesTool()

	// Register the result-inspection tools (collect_result, inspect_result) so the
	// agent can pull back parts of oversized tool results. These enforce per-user
	// ownership of buffered output, so they replace any host-provided variants.
	eng.RegisterTextTools()

	// Create the persistent recurring-task scheduler and expose manage_schedules
	// as a built-in LLM tool. Its worker starts after UseLLMConfig.
	eng.InitializeTaskScheduler()

	// Create Agentize instance
	ag := &Agentize{
		engine: eng,
		nodes:  make(map[string]*model.Node),
	}
	if opts == nil || !opts.DisableToolApprovals {
		eng.SetToolApprovalManager(ag.ReviewManager())
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

// GetSessionStore returns the session store as the full store.Store contract.
// (store.Store satisfies store.SessionStore, so existing callers keep working.)
func (ag *Agentize) GetSessionStore() store.Store {
	return ag.engine.Sessions
}

// --- Human-in-the-loop reviews (chapter 10) ---

// GetReviewStore returns the durable review store (store.Store satisfies
// review.Store on every backend).
func (ag *Agentize) GetReviewStore() review.Store {
	return ag.engine.Sessions
}

// ReviewManager returns the lazily-created review manager — the one object every
// frontend and tool-approval gate talks to.
func (ag *Agentize) ReviewManager() *review.Manager {
	ag.reviewMu.Lock()
	defer ag.reviewMu.Unlock()
	if ag.reviewManager == nil {
		ag.reviewManager = review.New(ag.GetReviewStore(), nil)
		// Count every decision and keep the pending gauge fresh regardless of
		// which frontend made the decision.
		ag.reviewManager.OnResolve(func(_ context.Context, r *model.ReviewRequest) {
			metrics.RecordReview(r.Kind, string(r.Status))
			ag.refreshPendingReviewsGauge()
		})
	}
	return ag.reviewManager
}

// SetReviewNotifier wires the creation hook so a frontend (Telegram, push, email)
// is given the chance to present a review the moment it is raised. Optional: when
// unset, reviews are still persisted and resolvable from any other surface.
func (ag *Agentize) SetReviewNotifier(n review.Notifier) {
	ag.ReviewManager().SetNotifier(n)
}

// RequestReview creates a pending review, persists it, fires the notifier, and
// returns its id. Generic (Kind/RefID describe the
// subject) so any host code can gate any action on a human decision.
func (ag *Agentize) RequestReview(ctx context.Context, r *model.ReviewRequest) (string, error) {
	id, err := ag.ReviewManager().Request(ctx, r)
	if err == nil {
		ag.refreshPendingReviewsGauge()
	}
	return id, err
}

// refreshPendingReviewsGauge sets agentize_reviews_pending from the global pending
// count. Best-effort: a store error leaves the previous value.
func (ag *Agentize) refreshPendingReviewsGauge() {
	if pend, err := ag.GetReviewStore().ListPendingReviews(""); err == nil {
		metrics.SetPendingReviews(len(pend))
	}
}

// ResolveReview records a decision on a pending review and returns the updated
// record. It is the single entry point every UI calls (the dashboard POST, a
// Telegram button handler, an API) and is idempotent.
func (ag *Agentize) ResolveReview(ctx context.Context, reviewID, decision, note, decidedBy string) (*model.ReviewRequest, error) {
	return ag.ReviewManager().Resolve(ctx, reviewID, decision, note, decidedBy)
}

// ListPendingReviews returns pending reviews for a user (userID == "" = all),
// and refreshes the pending-reviews gauge.
func (ag *Agentize) ListPendingReviews(_ context.Context, userID string) ([]*model.ReviewRequest, error) {
	reviews, err := ag.GetReviewStore().ListPendingReviews(userID)
	if err != nil {
		return nil, err
	}
	if userID == "" {
		metrics.SetPendingReviews(len(reviews))
	}
	return reviews, nil
}

// GetEngine returns the internal engine
func (ag *Agentize) GetEngine() *engine.Engine {
	return ag.engine
}

// GetTaskScheduler returns the persistent general-purpose task scheduler.
func (ag *Agentize) GetTaskScheduler() *engine.TaskScheduler {
	if ag == nil || ag.engine == nil {
		return nil
	}
	return ag.engine.GetTaskScheduler()
}

// CreateTaskSchedule creates a recurring task through the public Go API.
func (ag *Agentize) CreateTaskSchedule(input engine.CreateTaskScheduleInput) (*model.TaskSchedule, error) {
	scheduler := ag.GetTaskScheduler()
	if scheduler == nil {
		return nil, fmt.Errorf("task scheduler is not configured")
	}
	return scheduler.Create(input)
}

// ListTaskSchedules lists schedules for a user. Empty userID is the admin view.
func (ag *Agentize) ListTaskSchedules(userID string) ([]*model.TaskSchedule, error) {
	scheduler := ag.GetTaskScheduler()
	if scheduler == nil {
		return nil, fmt.Errorf("task scheduler is not configured")
	}
	return scheduler.List(userID)
}

// RecordUserFile stores a file the user sent (or that was generated for them)
// against the given session: the bytes go to the configured FileStore and the
// metadata is recorded in the database. Use model.FileSourceUploaded for files
// the user sent, or model.FileSourceGenerated for produced files. This is the
// entry point an application's upload handler should call.
func (ag *Agentize) RecordUserFile(sessionID, name, mimeType string, source model.FileSource, data []byte) (*model.UserFile, error) {
	return ag.engine.RecordUserFile(sessionID, name, mimeType, source, data)
}

// ListUserFiles returns all files owned by a user, newest first.
func (ag *Agentize) ListUserFiles(userID string) ([]*model.UserFile, error) {
	return ag.engine.ListUserFiles(userID)
}

// ReadUserFile returns the bytes and metadata for a file by ID.
func (ag *Agentize) ReadUserFile(fileID string) ([]byte, *model.UserFile, error) {
	return ag.engine.ReadUserFile(fileID)
}

// SetImageEditor wires an image-editing backend, enabling the manage_files
// edit_image action. The function receives the source image bytes + MIME type
// and an instruction, and returns the edited image bytes + MIME type.
func (ag *Agentize) SetImageEditor(editor engine.ImageEditorFunc) {
	ag.engine.SetImageEditor(editor)
}

// UseOpenRouterImageEditor enables image editing via OpenRouter's image-output
// models (default Gemini 2.5 Flash Image). It wires the built-in editor as the
// manage_files edit_image backend. Provide at least cfg.APIKey.
func (ag *Agentize) UseOpenRouterImageEditor(cfg imageedit.OpenRouterConfig) {
	ag.SetImageEditor(imageedit.NewOpenRouter(cfg).EditImage)
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

// UploadedFile describes a file the user sent alongside a message. Data holds the
// raw bytes; MIMEType is optional and detected from the name/content when empty.
type UploadedFile struct {
	Name     string
	MIMEType string
	Data     []byte
}

// ProcessMessageWithFiles records each uploaded file against the session as an
// uploaded user file — so it is persisted to the database, appears on the
// Documents debug page, and can be read later via the manage_files tool — and
// then processes the user message. A compact note listing the saved files is
// appended to the message so the agent knows they arrived and can read them on
// demand. It returns the assistant response, the token count, and the saved
// file records.
//
// This is the entry point a chat/bot integration should call when a user sends
// one or more attachments. Recording is best-effort: a file that fails to save
// is logged and skipped rather than failing the whole message.
func (ag *Agentize) ProcessMessageWithFiles(ctx context.Context, sessionID, userMessage string, files []UploadedFile) (string, int, []*model.UserFile, error) {
	var saved []*model.UserFile
	for _, f := range files {
		uf, err := ag.RecordUserFile(sessionID, f.Name, f.MIMEType, model.FileSourceUploaded, f.Data)
		if err != nil {
			log.Log.Warnf("[Agentize] ⚠️  Failed to record uploaded file %q for session %s: %v", f.Name, sessionID, err)
			continue
		}
		log.Log.Infof("[Agentize] 📎 Recorded uploaded file | id=%s | name=%q | type=%s | size=%dB | session=%s",
			uf.FileID, uf.Name, uf.MIMEType, uf.Size, sessionID)
		saved = append(saved, uf)
	}

	message := appendUploadedFilesNote(userMessage, saved)
	response, tokens, err := ag.ProcessMessage(ctx, sessionID, message)
	return response, tokens, saved, err
}

// ProcessMessageWithFile is a single-file convenience wrapper around
// ProcessMessageWithFiles.
func (ag *Agentize) ProcessMessageWithFile(ctx context.Context, sessionID, userMessage, name, mimeType string, data []byte) (string, int, *model.UserFile, error) {
	resp, tokens, saved, err := ag.ProcessMessageWithFiles(ctx, sessionID, userMessage, []UploadedFile{{Name: name, MIMEType: mimeType, Data: data}})
	var uf *model.UserFile
	if len(saved) > 0 {
		uf = saved[0]
	}
	return resp, tokens, uf, err
}

// appendUploadedFilesNote appends a compact note listing the saved files so the
// agent can reference and read them (via the manage_files tool) on demand. When
// no files were saved, the message is returned unchanged.
func appendUploadedFilesNote(userMessage string, files []*model.UserFile) string {
	if len(files) == 0 {
		return userMessage
	}
	var b strings.Builder
	b.WriteString(userMessage)
	if strings.TrimSpace(userMessage) != "" {
		b.WriteString("\n\n")
	}
	b.WriteString("[The user attached the following file(s). Use the manage_files tool (action=read) with the file_id to view a file when needed.]\n")
	for _, f := range files {
		b.WriteString(fmt.Sprintf("- file_id=%s name=%q type=%s size=%dB\n", f.FileID, f.Name, f.MIMEType, f.Size))
	}
	return b.String()
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

// AddDebugPage registers an external page to the debug panel.
// If page.NoNav is true, only the route is registered (no sidebar entry).
// Otherwise the page appears in the debugger sidebar.
func (ag *Agentize) AddDebugPage(page DebugPage) {
	if page.NoNav {
		ag.extraDebugRoutes = append(ag.extraDebugRoutes, page)
		return
	}
	ag.extraDebugPages = append(ag.extraDebugPages, page)
	ui.RegisterNavItem(ui.NavItem{URL: page.Path, Icon: page.Icon, Text: page.Title})
}

// SetUserBillingHTMLProvider sets the optional provider for billing/credit HTML on the user detail page (/agentize/debug/users/:userID).
// When set, the returned HTML is rendered on the user detail page below the user info card.
func (ag *Agentize) SetUserBillingHTMLProvider(fn debuger.UserBillingHTMLProvider) {
	ag.userBillingHTMLProvider = fn
}

// SetCoreSystemPromptProvider wires the "Core System Prompt" card on the user
// detail page to a live Core agent, so the page shows the exact system-prompt
// array the Core assembles for that user. Typical wiring:
//
//	ag.SetCoreSystemPromptProvider(coreHandler.SystemPromptSectionsFor)
//
// When left unset, Agentize installs a store-only preview (core.PreviewSystemPromptSections)
// so the card still shows the controller rules and the user's memory/files/sessions,
// with agent-dependent sections marked as available only with a live Core.
func (ag *Agentize) SetCoreSystemPromptProvider(fn debuger.CoreSystemPromptProvider) {
	ag.coreSystemPromptProvider = fn
}

// SetExtraSystemInfoProvider sets an optional provider whose rows are appended to
// the System Info panel's "more info" section on every render. Applications use it
// to surface their own runtime facts (e.g. where each config value was read from).
func (ag *Agentize) SetExtraSystemInfoProvider(fn func() []debuger.InfoKV) {
	ag.extraSystemInfoProvider = fn
}

// SetUserDeleteDataHook sets an optional hook called after DeleteUserData (sessions, messages) for a user.
// The application can use it to delete quota usage, consumption records, balance, etc. for that user.
func (ag *Agentize) SetUserDeleteDataHook(fn func(userID string) error) {
	ag.userDeleteDataHook = fn
}

// GetDebugNavItems returns the full set of navigation items including extra pages.
func (ag *Agentize) GetDebugNavItems() []ui.NavItem {
	items := ui.DefaultNavItems()
	for _, p := range ag.extraDebugPages {
		items = append(items, ui.NavItem{URL: p.Path, Icon: p.Icon, Text: p.Title})
	}
	return items
}

// WaitForShutdown waits for shutdown signals and performs graceful shutdown
func (ag *Agentize) WaitForShutdown() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	sig := <-sigChan
	log.Log.Infof("[Agentize] 📡 Received signal: %v, initiating graceful shutdown...", sig)

	ag.StopScheduler()
	if ag.engine != nil {
		ag.engine.StopTaskScheduler()
	}
	log.Log.Infof("[Agentize] ✅ Graceful shutdown completed")
}
