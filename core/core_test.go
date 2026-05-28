package core

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
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
