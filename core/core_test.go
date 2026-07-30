package core

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/filestore"
	"github.com/ghiac/agentize/fsrepo"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

type rejectingToolApprovalManager struct {
	requested *model.ReviewRequest
}

func (m *rejectingToolApprovalManager) Request(_ context.Context, r *model.ReviewRequest) (string, error) {
	r.ID = "rev_core_reject"
	m.requested = r
	return r.ID, nil
}

func (m *rejectingToolApprovalManager) Await(_ context.Context, _ string) (*model.ReviewRequest, error) {
	m.requested.Status = model.ReviewRejected
	m.requested.Decision = "reject"
	return m.requested, nil
}

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

func TestProcessMessageWithGeneratedFilesFindsWorkerSessionFiles(t *testing.T) {
	ch, _, coreTx, agentTx := newDispatchableCore(t, "researcher")
	worker, ok := ch.agents.Get("researcher")
	if !ok {
		t.Fatal("researcher agent not found")
	}
	fileStore, err := filestore.NewLocalFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker.Engine.SetFileStore(fileStore)
	worker.Engine.Executor = func(name string, args map[string]interface{}) (string, error) {
		return worker.Engine.Functions.Execute(name, args)
	}

	coreTx.AddResponse(coreDispatchTurn("researcher", "create a report file"))
	agentTx["researcher"].AddResponse(openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ToolCall{{
					ID:   "save-file",
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      "manage_files",
						Arguments: `{"action":"save","name":"report.txt","content":"worker output"}`,
					},
				}},
			},
			FinishReason: openai.FinishReasonToolCalls,
		}},
	})
	agentTx["researcher"].AddResponse(agentAnswer("report created"))

	response, files, err := ch.ProcessMessageWithGeneratedFiles(
		context.Background(),
		"user1",
		"make a report",
	)
	if err != nil {
		t.Fatal(err)
	}
	if response != "report created" {
		t.Fatalf("response=%q", response)
	}
	if len(files) != 1 || files[0].UserID != "user1" || files[0].Name != "report.txt" {
		t.Fatalf("worker-generated files were not returned: %+v", files)
	}
	if files[0].Source != model.FileSourceGenerated {
		t.Fatalf("unexpected source: %s", files[0].Source)
	}
}

// TestRunCoreTool_RequiredArgsValidated locks in C7: tool sites whose schema
// marks an argument required must reject a missing/empty value instead of
// silently treating it as a no-op (update_status) or a zero default (sleep).
func TestRunCoreTool_RequiredArgsValidated(t *testing.T) {
	ch, _ := newTestCoreHandler(t, nil)
	ctx := context.Background()
	cases := []struct {
		name, tool, args, substr string
	}{
		{"update_status missing message", "update_status", `{}`, "message is required"},
		{"sleep missing seconds", "sleep", `{}`, "seconds is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := openai.ToolCall{Function: openai.FunctionCall{Name: tc.tool, Arguments: tc.args}}
			_, err := ch.runCoreToolImpl(ctx, "user1", "sess1", nil, "msg1", call)
			if err == nil || !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("expected error containing %q, got %v", tc.substr, err)
			}
		})
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

// TestChangeSession_CrossUserDenied verifies a user cannot switch into another
// user's session by supplying its id. Session ids are formatted
// {userID}-{agentType}-s{seq} and thus guessable, so this is a real attack.
func TestChangeSession_CrossUserDenied(t *testing.T) {
	ch, mockStore := newTestCoreHandlerWithMockStore(t, []string{"researcher"})
	ctx := context.Background()

	// Victim creates a session.
	victim, _ := ch.sessionHandler.CreateSession("victim", model.AgentType("researcher"))
	if victim == nil {
		t.Fatal("CreateSession failed")
	}
	victim.Title = "Victim private session"
	_ = mockStore.Put(victim)
	_, _ = mockStore.GetOrCreateUser("victim")
	_, _ = mockStore.GetOrCreateUser("attacker")

	// Attacker tries to switch into the victim's session by its id.
	result, err := ch.changeSessionTool(ctx, "attacker", map[string]interface{}{
		"agent_name": "researcher",
		"session_id": victim.SessionID,
	})
	if err == nil {
		t.Fatalf("SECURITY: attacker switched into victim's session, got %q", result)
	}
	if strings.Contains(result, "Victim") {
		t.Fatalf("SECURITY: leaked victim session title: %q", result)
	}

	// Defense in depth: even a direct setActiveSessionID must refuse the foreign id.
	if err := ch.setActiveSessionID("attacker", model.AgentType("researcher"), victim.SessionID); err == nil {
		t.Fatal("SECURITY: setActiveSessionID accepted a foreign session")
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

func TestCoreToolApprovalRejectsBeforeSideEffect(t *testing.T) {
	ch, mockStore := newTestCoreHandlerWithMockStore(t, []string{"researcher"})
	manager := &rejectingToolApprovalManager{}
	ch.SetToolApprovalManager(manager)

	coreSession := model.NewSessionWithType("user1", model.AgentTypeCore)
	result := ch.executeCoreTool(
		context.Background(),
		"user1",
		coreSession.SessionID,
		coreSession,
		"msg1",
		openai.ToolCall{
			ID: "call_ban",
			Function: openai.FunctionCall{
				Name:      "ban_user",
				Arguments: `{"duration_hours":24,"message":"blocked"}`,
			},
		},
	)

	if !strings.Contains(result, "was not executed") {
		t.Fatalf("result = %q", result)
	}
	if manager.requested == nil || manager.requested.Metadata["tool_name"] != "ban_user" {
		t.Fatalf("approval request was not created correctly: %+v", manager.requested)
	}
	user, err := mockStore.GetOrCreateUser("user1")
	if err != nil {
		t.Fatalf("GetOrCreateUser: %v", err)
	}
	if user.IsCurrentlyBanned() {
		t.Fatal("rejected ban_user call changed user state")
	}
}

func TestToolApprovalRejectedErrorSupportsErrorsIs(t *testing.T) {
	err := &engine.ToolApprovalRejectedError{ReviewID: "rev_1", Decision: "reject"}
	if !errors.Is(err, engine.ErrToolApprovalRejected) {
		t.Fatalf("errors.Is(%v, ErrToolApprovalRejected) = false", err)
	}
}
