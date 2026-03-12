package core

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
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
