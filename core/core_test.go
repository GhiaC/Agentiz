package core

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/planning"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

// newTestEngine returns a minimal Engine with only Functions set (no Repo/Sessions).
// Used so core tests do not depend on agentmanager's unexported newTestEngine.
func newTestEngine(tools ...string) *engine.Engine {
	eng := &engine.Engine{
		Functions: model.NewFunctionRegistry(),
	}
	for _, name := range tools {
		eng.Functions.MustRegister(name, name, func(args map[string]interface{}) (string, error) { return "", nil })
	}
	return eng
}

// newTestCoreHandler creates a CoreHandler with in-memory SQLite store, SessionHandler,
// and an AgentManager with len(agentNames) registered agents (minimal engines).
// Returns the CoreHandler and the store so callers can create users/sessions if needed.
func newTestCoreHandler(t *testing.T, agentNames []string) (*CoreHandler, *store.SQLiteStore) {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	config := model.DefaultSessionHandlerConfig()
	config.DisableLogs = true
	sh := model.NewSessionHandler(sqliteStore, config)
	am := agentmanager.New(sh)
	for _, name := range agentNames {
		cfg := agentmanager.AgentConfig{
			Name:        name,
			DisplayName: name,
			CostTier:    agentmanager.CostTierLow,
		}
		if err := am.Register(cfg, newTestEngine("tool_"+name)); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	ch := NewCoreHandler(sh, am, DefaultCoreHandlerConfig())
	return ch, sqliteStore
}

// newTestCoreHandlerWithMockStore creates a CoreHandler with MockSessionStore and an
// AgentManager with len(agentNames) registered agents. Returns the CoreHandler and the
// mock store for assertions (Sessions(), Users(), MessageCount(), ToolCallCount()).
func newTestCoreHandlerWithMockStore(t *testing.T, agentNames []string) (*CoreHandler, *MockSessionStore) {
	t.Helper()
	mockStore := NewMockSessionStore()
	config := model.DefaultSessionHandlerConfig()
	config.DisableLogs = true
	sh := model.NewSessionHandler(mockStore, config)
	am := agentmanager.New(sh)
	for _, name := range agentNames {
		cfg := agentmanager.AgentConfig{
			Name:        name,
			DisplayName: name,
			CostTier:    agentmanager.CostTierLow,
		}
		if err := am.Register(cfg, newTestEngine("tool_"+name)); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}
	ch := NewCoreHandler(sh, am, DefaultCoreHandlerConfig())
	return ch, mockStore
}

func TestNewCoreHandler_Smoke(t *testing.T) {
	ch, _ := newTestCoreHandler(t, nil) // empty AgentManager
	if ch == nil {
		t.Fatal("NewCoreHandler returned nil")
	}
	// buildSystemPrompts should not panic; may return prompts or error
	prompts, err := ch.buildSystemPrompts("user1")
	if err != nil {
		t.Logf("buildSystemPrompts (expected for empty user): %v", err)
		return
	}
	if len(prompts) == 0 {
		t.Error("buildSystemPrompts returned empty slice")
	}
}

func TestBuildSystemPrompts_ContainsBaseAndAgents(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher", "coder"})
	prompts, err := ch.buildSystemPrompts("user1")
	if err != nil {
		t.Fatalf("buildSystemPrompts: %v", err)
	}
	if len(prompts) == 0 {
		t.Fatal("buildSystemPrompts returned empty slice")
	}
	// First element should be the embedded core controller text
	first := prompts[0]
	if !strings.Contains(first, "orchestrator") && !strings.Contains(first, "Core") {
		t.Errorf("first prompt should contain controller text, got snippet: %s", first[:min(100, len(first))])
	}
	// At least one prompt should contain agent descriptions (from BuildAgentsDescriptionPrompt)
	allText := strings.Join(prompts, " ")
	if !strings.Contains(allText, "researcher") || !strings.Contains(allText, "coder") {
		t.Errorf("prompts should contain agent names; combined: %s", allText[:min(500, len(allText))])
	}
}

func TestBuildSystemPrompts_WithSessionsList(t *testing.T) {
	ch, sqliteStore := newTestCoreHandler(t, []string{"researcher"})
	user, err := sqliteStore.GetOrCreateUser("user1")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	_, err = ch.sessionHandler.CreateSessionForUser(user, model.AgentType("researcher"))
	if err != nil {
		t.Fatalf("CreateSessionForUser: %v", err)
	}
	prompts, err := ch.buildSystemPrompts("user1")
	if err != nil {
		t.Fatalf("buildSystemPrompts: %v", err)
	}
	allText := strings.Join(prompts, " ")
	if !strings.Contains(allText, "Sessions:") && !strings.Contains(allText, "Session") {
		t.Errorf("prompts should contain sessions section when user has sessions; got snippet: %s", allText[:min(600, len(allText))])
	}
}

func TestGetCoreToolsForLLM_ContainsExpectedTools(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher", "coder"})
	tools := ch.getCoreToolsForLLM()
	names := make(map[string]bool)
	var createSessionParams, changeSessionParams map[string]interface{}
	for _, tool := range tools {
		if tool.Function != nil {
			names[tool.Function.Name] = true
			if tool.Function.Name == "create_session" && tool.Function.Parameters != nil {
				createSessionParams, _ = tool.Function.Parameters.(map[string]interface{})
			}
			if tool.Function.Name == "change_session" && tool.Function.Parameters != nil {
				changeSessionParams, _ = tool.Function.Parameters.(map[string]interface{})
			}
		}
	}
	if !names["list_sessions"] {
		t.Error("tools should include list_sessions")
	}
	if !names["call_agent_researcher"] {
		t.Error("tools should include call_agent_researcher")
	}
	if !names["call_agent_coder"] {
		t.Error("tools should include call_agent_coder")
	}
	if !names["create_session"] {
		t.Error("tools should include create_session")
	}
	if !names["change_session"] {
		t.Error("tools should include change_session")
	}
	if !names["ban_user"] && !names["update_status"] {
		t.Error("tools should include at least one of ban_user or update_status")
	}
	// create_session / change_session should have agent_name enum with both agents
	for _, params := range []map[string]interface{}{createSessionParams, changeSessionParams} {
		if params == nil {
			continue
		}
		props, _ := params["properties"].(map[string]interface{})
		if props == nil {
			continue
		}
		agentNameProp, _ := props["agent_name"].(map[string]interface{})
		if agentNameProp == nil {
			continue
		}
		enumAny := agentNameProp["enum"]
		switch e := enumAny.(type) {
		case []interface{}:
			var enum []string
			for _, v := range e {
				if s, ok := v.(string); ok {
					enum = append(enum, s)
				}
			}
			if len(enum) != 2 || !contains(enum, "researcher") || !contains(enum, "coder") {
				t.Errorf("agent_name enum should contain researcher and coder, got %v", enum)
			}
		case []string:
			if len(e) != 2 || !contains(e, "researcher") || !contains(e, "coder") {
				t.Errorf("agent_name enum should contain researcher and coder, got %v", e)
			}
		}
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestProcessMessage_LLMNotConfigured verifies that ProcessMessage returns an error when UseLLMConfig was not called.
// Use empty agents so IsReady() passes and we hit the LLM check.
func TestProcessMessage_LLMNotConfigured(t *testing.T) {
	ch, _ := newTestCoreHandler(t, nil)
	ctx := context.Background()
	_, err := ch.ProcessMessage(ctx, "user1", "hello")
	if err == nil {
		t.Fatal("expected error when LLM not configured")
	}
	if !strings.Contains(err.Error(), "LLM") && !strings.Contains(err.Error(), "UseLLMConfig") && !strings.Contains(err.Error(), "configured") {
		t.Errorf("expected LLM/config related error, got %q", err.Error())
	}
}

// TestProcessMessage_AgentNotReady verifies that ProcessMessage returns an error when agents are not ready (Init not called).
func TestProcessMessage_AgentNotReady(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	// Configure LLM so we get past that check; we use in-memory store and never call Init on agents
	transport := &MockLLMTransport{}
	transport.AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{Content: "hi", Role: openai.ChatMessageRoleAssistant},
		}},
	})
	cfg := engine.LLMConfig{
		APIKey:         "test",
		Model:          "test-model",
		HTTPClient:     &http.Client{Transport: transport},
		BackupDisabled: true,
	}
	if err := ch.UseLLMConfig(cfg); err != nil {
		t.Fatalf("UseLLMConfig: %v", err)
	}
	ctx := context.Background()
	_, err := ch.ProcessMessage(ctx, "user1", "hello")
	if err == nil {
		t.Fatal("expected error when agents not ready")
	}
	if !strings.Contains(err.Error(), "ready") && !strings.Contains(err.Error(), "Init") {
		t.Errorf("expected ready/Init related error, got %q", err.Error())
	}
}

// TestProcessMessage_RecordsRouteTrace verifies that handling a message records and
// persists a Core routing DAG (user message → decision → response) with the right
// metadata. This is the end-to-end wiring of the routing-DAG feature.
func TestProcessMessage_RecordsRouteTrace(t *testing.T) {
	ch, st := newTestCoreHandler(t, nil) // no agents → AgentManager.IsReady() is true
	transport := &MockLLMTransport{}
	transport.AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message:      openai.ChatCompletionMessage{Content: "the answer", Role: openai.ChatMessageRoleAssistant},
			FinishReason: openai.FinishReasonStop,
		}},
		Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})
	cfg := engine.LLMConfig{
		APIKey:         "test",
		Model:          "test-model",
		HTTPClient:     &http.Client{Transport: transport},
		BackupDisabled: true,
	}
	if err := ch.UseLLMConfig(cfg); err != nil {
		t.Fatalf("UseLLMConfig: %v", err)
	}
	ch.userModeration = nil // skip the nonsense check so it doesn't consume a mock response

	resp, err := ch.ProcessMessage(context.Background(), "user1", "hello there")
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if resp != "the answer" {
		t.Fatalf("response = %q, want %q", resp, "the answer")
	}

	traces, err := st.GetRouteTracesByUser("user1")
	if err != nil {
		t.Fatalf("GetRouteTracesByUser: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	tr := traces[0]
	if tr.Status != "ok" {
		t.Errorf("status = %q, want ok", tr.Status)
	}
	if tr.Message != "hello there" {
		t.Errorf("message = %q, want %q", tr.Message, "hello there")
	}
	if tr.Response != "the answer" {
		t.Errorf("response = %q, want %q", tr.Response, "the answer")
	}
	if tr.TotalTokens != 15 {
		t.Errorf("total tokens = %d, want 15", tr.TotalTokens)
	}

	var hasUser, hasDecision, hasResponse bool
	for _, n := range tr.Nodes {
		switch n.Type {
		case model.RouteNodeUserMessage:
			hasUser = true
		case model.RouteNodeDecision:
			hasDecision = true
		case model.RouteNodeResponse:
			hasResponse = true
		}
	}
	if !hasUser || !hasDecision || !hasResponse {
		t.Errorf("missing node types: user=%v decision=%v response=%v (nodes=%+v)", hasUser, hasDecision, hasResponse, tr.Nodes)
	}
}

// newReadyAgentEngine builds a fully DB-ready worker Engine that shares the given
// Sessions store (so the Core can dispatch into it) and is backed by its own
// MockLLMTransport — letting a test detect whether the agent actually ran by
// checking transport.Calls(). The API key is left empty so engine.UseLLMConfig
// does not start a background summarization scheduler.
func newReadyAgentEngine(t *testing.T, sessions store.Store, transport *MockLLMTransport) *engine.Engine {
	t.Helper()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll root: %v", err)
	}
	for name, body := range map[string]string{
		"node.yaml":  "id: \"root\"\ntitle: \"Root\"\n",
		"node.md":    "# Root",
		"tools.json": `{"tools": []}`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", name, err)
		}
	}
	repo, err := fsrepo.NewNodeRepository(filepath.Dir(root))
	if err != nil {
		t.Fatalf("NewNodeRepository: %v", err)
	}
	eng := &engine.Engine{Repo: repo, Sessions: sessions, Functions: model.NewFunctionRegistry()}
	if err := eng.UseLLMConfig(engine.LLMConfig{
		Model:          "agent-model",
		HTTPClient:     &http.Client{Transport: transport},
		BackupDisabled: true,
	}); err != nil {
		t.Fatalf("agent UseLLMConfig: %v", err)
	}
	if err := eng.Init(); err != nil {
		t.Fatalf("agent Init: %v", err)
	}
	return eng
}

// newDispatchableCore wires a CoreHandler whose registered agents are all
// dispatch-ready: they share one in-memory store and each has its own
// MockLLMTransport. Returns the handler, the store (for route-trace lookups), the
// Core's own transport, and the per-agent transports keyed by name.
func newDispatchableCore(t *testing.T, agentNames ...string) (*CoreHandler, *store.SQLiteStore, *MockLLMTransport, map[string]*MockLLMTransport) {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	cfg := model.DefaultSessionHandlerConfig()
	cfg.DisableLogs = true
	sh := model.NewSessionHandler(sqliteStore, cfg)
	am := agentmanager.New(sh)

	agentTx := make(map[string]*MockLLMTransport, len(agentNames))
	for _, name := range agentNames {
		tx := &MockLLMTransport{}
		agentTx[name] = tx
		ac := agentmanager.AgentConfig{
			Name:        name,
			DisplayName: name,
			AgentType:   model.AgentType(name),
			CostTier:    agentmanager.CostTierLow,
		}
		if err := am.Register(ac, newReadyAgentEngine(t, sqliteStore, tx)); err != nil {
			t.Fatalf("Register %s: %v", name, err)
		}
	}

	ch := NewCoreHandler(sh, am, DefaultCoreHandlerConfig())
	coreTx := &MockLLMTransport{}
	if err := ch.UseLLMConfig(engine.LLMConfig{
		APIKey:         "test",
		Model:          "core-model",
		HTTPClient:     &http.Client{Transport: coreTx},
		BackupDisabled: true,
	}); err != nil {
		t.Fatalf("core UseLLMConfig: %v", err)
	}
	ch.userModeration = nil // skip the nonsense check so it doesn't consume a mock response
	return ch, sqliteStore, coreTx, agentTx
}

// TestProcessMessage_MultipleAgentCalls_OnlyFirstDispatched verifies that when a
// single Core turn emits MORE THAN ONE call_agent_* tool call, only the first
// agent actually runs. The Core dispatches only — it returns the first agent's
// answer verbatim — so running later agents would burn a full worker-agent call
// (LLM cost + latency) for an answer that is discarded. The redundant call is
// skipped and recorded on the routing DAG as a skipped dispatch.
func TestProcessMessage_MultipleAgentCalls_OnlyFirstDispatched(t *testing.T) {
	ch, st, coreTx, agentTx := newDispatchableCore(t, "alpha", "beta")

	// One Core turn that asks to call two agents at once.
	coreTx.AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ToolCall{
					{ID: "c1", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "call_agent_alpha", Arguments: `{"message":"hi alpha"}`}},
					{ID: "c2", Type: openai.ToolTypeFunction, Function: openai.FunctionCall{Name: "call_agent_beta", Arguments: `{"message":"hi beta"}`}},
				},
			},
			FinishReason: openai.FinishReasonToolCalls,
		}},
		Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})
	// alpha answers; beta would answer "beta answer" if it (wrongly) ran.
	agentTx["alpha"].AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "alpha answer"},
			FinishReason: openai.FinishReasonStop,
		}},
		Usage: openai.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	})
	agentTx["beta"].AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: "beta answer"},
			FinishReason: openai.FinishReasonStop,
		}},
	})

	resp, err := ch.ProcessMessage(context.Background(), "user1", "do both")
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}

	// The first agent is dispatched and its answer returned verbatim.
	if resp != "alpha answer" {
		t.Fatalf("response = %q, want %q (first agent's answer)", resp, "alpha answer")
	}
	// The second agent must never run: its LLM transport stays untouched.
	if got := agentTx["beta"].Calls(); got != 0 {
		t.Errorf("beta agent made %d LLM call(s); want 0 (redundant call_agent_* must be skipped)", got)
	}
	if got := agentTx["alpha"].Calls(); got == 0 {
		t.Error("alpha agent never ran; want it dispatched")
	}

	// The routing DAG records alpha as a real dispatch and beta as a skipped one.
	traces, err := st.GetRouteTracesByUser("user1")
	if err != nil {
		t.Fatalf("GetRouteTracesByUser: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	tr := traces[0]

	var ran, skipped []model.RouteNode
	for _, n := range tr.Nodes {
		if n.Type != model.RouteNodeAgentDispatch {
			continue
		}
		if n.Status == model.RouteStatusSkipped {
			skipped = append(skipped, n)
		} else {
			ran = append(ran, n)
		}
	}
	if len(ran) != 1 || ran[0].Agent != "alpha" {
		t.Errorf("dispatched nodes = %+v, want exactly one for alpha", ran)
	}
	if len(skipped) != 1 || skipped[0].Agent != "beta" {
		t.Errorf("skipped nodes = %+v, want exactly one for beta", skipped)
	}
	if agents := tr.DispatchedAgents(); len(agents) != 1 || agents[0] != "alpha" {
		t.Errorf("DispatchedAgents() = %v, want [alpha] (skipped agent excluded)", agents)
	}
}

// TestExecutePlan_Disabled verifies that execute_plan returns an error when planning is not enabled.
func TestExecutePlan_Disabled(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	ctx := context.Background()
	result, err := ch.executePlanTool(ctx, "user1", "sess1", "msg1", map[string]interface{}{"message": "do something"})
	if err == nil {
		t.Fatal("expected error when planning not enabled")
	}
	if result != "" {
		t.Errorf("expected empty result, got %q", result)
	}
	if !strings.Contains(err.Error(), "planning") && !strings.Contains(err.Error(), "enabled") {
		t.Errorf("expected planning not enabled error, got %q", err.Error())
	}
}

// TestCreateSession_Tool verifies create_session creates a session and sets it active.
func TestCreateSession_Tool(t *testing.T) {
	ch, mockStore := newTestCoreHandlerWithMockStore(t, []string{"researcher"})
	ctx := context.Background()
	result, err := ch.createSessionTool(ctx, "user1", map[string]interface{}{
		"agent_name": "researcher",
		"title":      "My Session",
	})
	if err != nil {
		t.Fatalf("createSessionTool: %v", err)
	}
	if result == "" || !strings.Contains(result, "researcher") {
		t.Errorf("unexpected result: %q", result)
	}
	sessions := mockStore.Sessions()
	if len(sessions) == 0 {
		t.Error("expected at least one session after create_session")
	}
	user, _ := mockStore.GetOrCreateUser("user1")
	if user == nil {
		t.Fatal("expected user to exist")
	}
	if user.GetActiveSessionID(model.AgentType("researcher")) == "" {
		t.Error("expected active session to be set for researcher")
	}
}

// TestChangeSession_Tool verifies change_session switches to an existing session.
func TestChangeSession_Tool(t *testing.T) {
	ch, mockStore := newTestCoreHandlerWithMockStore(t, []string{"researcher"})
	ctx := context.Background()
	// Create a session first
	sess, _ := ch.sessionHandler.CreateSession("user1", model.AgentType("researcher"))
	if sess == nil {
		t.Fatal("CreateSession failed")
	}
	sess.Title = "First"
	_ = mockStore.Put(sess)
	_, _ = mockStore.GetOrCreateUser("user1")
	result, err := ch.changeSessionTool(ctx, "user1", map[string]interface{}{
		"agent_name": "researcher",
		"session_id": sess.SessionID,
	})
	if err != nil {
		t.Fatalf("changeSessionTool: %v", err)
	}
	if result == "" || !strings.Contains(result, "First") {
		t.Errorf("unexpected result: %q", result)
	}
}

// TestBanUser_Tool verifies ban_user sets user ban state.
func TestBanUser_Tool(t *testing.T) {
	ch, mockStore := newTestCoreHandlerWithMockStore(t, []string{"researcher"})
	ctx := context.Background()
	_, _ = mockStore.GetOrCreateUser("user1")
	result, err := ch.banUserTool(ctx, "user1", map[string]interface{}{
		"duration_hours": float64(24),
		"message":        "Test ban",
	})
	if err != nil {
		t.Fatalf("banUserTool: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
	user, _ := mockStore.GetOrCreateUser("user1")
	if user == nil || !user.IsCurrentlyBanned() {
		t.Error("expected user to be banned")
	}
	if user.BanMessage != "Test ban" {
		t.Errorf("expected ban message 'Test ban', got %q", user.BanMessage)
	}
}

// TestExecutePlan_Success verifies execute_plan returns result when orchestrator is set with mock planner/runner.
func TestExecutePlan_Success(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	fixedPlan := &planning.Plan{
		ID: "plan-1", UserID: "u1", SessionID: "s1", Input: "task",
		Steps: []*planning.Step{
			{ID: "s1", Type: planning.StepLLMCall, Status: planning.StepPending, Config: planning.StepConfig{Prompt: "hi"}},
		},
		Status: planning.PlanPending, Version: 1,
	}
	store := planning.NewMemoryStore()
	planner := &mockOrchestratorPlanner{plan: fixedPlan}
	runner := &mockOrchestratorRunner{result: &planning.PlanResult{Output: "plan output"}}
	orch := planning.NewOrchestrator(planner, runner, planning.WithOrchestratorStore(store))
	ch.UsePlanning(orch)
	ctx := context.Background()
	result, err := ch.executePlanTool(ctx, "user1", "sess1", "msg1", map[string]interface{}{"message": "do task"})
	if err != nil {
		t.Fatalf("executePlanTool: %v", err)
	}
	if result != "plan output" {
		t.Errorf("expected result 'plan output', got %q", result)
	}
}

type mockOrchestratorPlanner struct {
	plan *planning.Plan
}

func (m *mockOrchestratorPlanner) CreatePlan(ctx context.Context, input planning.PlanInput) (*planning.Plan, error) {
	return m.plan, nil
}

func (m *mockOrchestratorPlanner) Replan(ctx context.Context, plan *planning.Plan, lastStep *planning.Step) (*planning.Plan, error) {
	return plan, nil
}

type mockOrchestratorRunner struct {
	result *planning.PlanResult
}

func (m *mockOrchestratorRunner) Run(ctx context.Context, plan *planning.Plan, opts ...planning.RunOption) (*planning.PlanResult, error) {
	return m.result, nil
}

func (m *mockOrchestratorRunner) RunStep(ctx context.Context, plan *planning.Plan, step *planning.Step) (*planning.StepResult, error) {
	return nil, nil
}

func (m *mockOrchestratorRunner) Cancel(ctx context.Context, planID string) error {
	return nil
}

func (m *mockOrchestratorRunner) Resume(ctx context.Context, planID string) (*planning.PlanResult, error) {
	return nil, nil
}

// --- getPlanStatusTool / cancelPlanTool tests ---

func TestGetPlanStatusTool_NoOrchestrator(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	_, err := ch.getPlanStatusTool(context.Background(), map[string]interface{}{"plan_id": "plan-1"})
	if err == nil {
		t.Fatal("expected error when orchestrator nil")
	}
	if !strings.Contains(err.Error(), "planning") {
		t.Errorf("expected planning-related error, got %q", err.Error())
	}
}

func TestGetPlanStatusTool_Success(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	store := planning.NewMemoryStore()
	plan := &planning.Plan{
		ID:     "plan-42",
		Status: planning.PlanCompleted,
		Steps: []*planning.Step{
			{ID: "s1", Status: planning.StepCompleted},
		},
		Output: "final result",
	}
	_ = store.Save(context.Background(), plan)
	planner := &mockOrchestratorPlanner{plan: plan}
	runner := &mockOrchestratorRunner{}
	orch := planning.NewOrchestrator(planner, runner, planning.WithOrchestratorStore(store))
	ch.UsePlanning(orch)

	result, err := ch.getPlanStatusTool(context.Background(), map[string]interface{}{"plan_id": "plan-42"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "plan-42") {
		t.Errorf("expected plan-42 in result, got %q", result)
	}
	if !strings.Contains(result, "completed") {
		t.Errorf("expected completed status in result, got %q", result)
	}
}

func TestCancelPlanTool_NoOrchestrator(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	_, err := ch.cancelPlanTool(context.Background(), map[string]interface{}{"plan_id": "plan-1"})
	if err == nil {
		t.Fatal("expected error when orchestrator nil")
	}
}

func TestCancelPlanTool_Success(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	store := planning.NewMemoryStore()
	plan := &planning.Plan{
		ID:     "plan-99",
		Status: planning.PlanRunning,
		Steps:  []*planning.Step{{ID: "s1", Status: planning.StepRunning}},
	}
	_ = store.Save(context.Background(), plan)

	cancelCalled := false
	runner := &mockCancelRunner{onCancel: func(id string) error {
		cancelCalled = true
		if id != "plan-99" {
			t.Errorf("expected plan-99, got %s", id)
		}
		return nil
	}}
	planner := &mockOrchestratorPlanner{plan: plan}
	orch := planning.NewOrchestrator(planner, runner, planning.WithOrchestratorStore(store))
	ch.UsePlanning(orch)

	result, err := ch.cancelPlanTool(context.Background(), map[string]interface{}{"plan_id": "plan-99"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "plan-99") {
		t.Errorf("expected plan-99 in result, got %q", result)
	}
	if !cancelCalled {
		t.Error("expected Cancel to be called on runner")
	}
}

type mockCancelRunner struct {
	onCancel func(string) error
}

func (m *mockCancelRunner) Run(_ context.Context, _ *planning.Plan, _ ...planning.RunOption) (*planning.PlanResult, error) {
	return nil, nil
}
func (m *mockCancelRunner) RunStep(_ context.Context, _ *planning.Plan, _ *planning.Step) (*planning.StepResult, error) {
	return nil, nil
}
func (m *mockCancelRunner) Cancel(_ context.Context, planID string) error {
	if m.onCancel != nil {
		return m.onCancel(planID)
	}
	return nil
}
func (m *mockCancelRunner) Resume(_ context.Context, _ string) (*planning.PlanResult, error) {
	return nil, nil
}

func TestBuildPlanToolInfos(t *testing.T) {
	ch, _ := newTestCoreHandler(t, []string{"researcher"})
	infos := ch.buildPlanToolInfos()
	if len(infos) == 0 {
		t.Fatal("expected non-empty tool infos")
	}
	nameSet := make(map[string]bool)
	for _, info := range infos {
		nameSet[info.Name] = true
		if info.Description == "" {
			t.Errorf("tool %q has empty description", info.Name)
		}
	}
	if !nameSet["list_sessions"] {
		t.Error("expected list_sessions in tool infos")
	}
	if !nameSet["call_agent_researcher"] {
		t.Error("expected call_agent_researcher in tool infos")
	}
	if !nameSet["sleep"] {
		t.Error("expected sleep in tool infos")
	}
}

func TestExecutePlanTool_PassesToolsAndExecutor(t *testing.T) {
	ch, _ := newTestCoreHandlerWithMockStore(t, []string{"researcher"})
	store := planning.NewMemoryStore()

	var capturedInput planning.PlanInput
	planner := &capturingPlanner{
		plan: &planning.Plan{
			ID: "tmp", UserID: "u1", SessionID: "s1", Input: "test",
			Steps:   []*planning.Step{{ID: "s1", Type: planning.StepLLMCall, Status: planning.StepPending, Config: planning.StepConfig{Prompt: "hi"}}},
			Status:  planning.PlanPending,
			Version: 1,
		},
		onCreatePlan: func(input planning.PlanInput) {
			capturedInput = input
		},
	}
	runner := &mockCancelRunner{}
	orch := planning.NewOrchestrator(planner, runner, planning.WithOrchestratorStore(store))
	ch.UsePlanning(orch)

	_, _ = ch.executePlanTool(context.Background(), "u1", "s1", "msg1", map[string]interface{}{
		"message": "do something",
	})

	if capturedInput.Context == nil {
		t.Fatal("expected PlanContext to be set")
	}
	if len(capturedInput.Context.AvailableTools) == 0 {
		t.Error("expected AvailableTools to be populated")
	}
	if capturedInput.Context.ToolExecutor == nil {
		t.Error("expected ToolExecutor to be set")
	}
	hasleep := false
	for _, tool := range capturedInput.Context.AvailableTools {
		if tool.Name == "sleep" {
			hasleep = true
		}
	}
	if !hasleep {
		t.Error("expected 'sleep' in AvailableTools")
	}
}

type capturingPlanner struct {
	plan         *planning.Plan
	onCreatePlan func(input planning.PlanInput)
}

func (m *capturingPlanner) CreatePlan(_ context.Context, input planning.PlanInput) (*planning.Plan, error) {
	if m.onCreatePlan != nil {
		m.onCreatePlan(input)
	}
	return m.plan, nil
}

func (m *capturingPlanner) Replan(_ context.Context, plan *planning.Plan, _ *planning.Step) (*planning.Plan, error) {
	return plan, nil
}
