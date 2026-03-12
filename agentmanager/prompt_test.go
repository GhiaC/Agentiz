package agentmanager

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
)

// newTestManagerWithAgents registers each config with a new test engine (optional tools per agent).
func newTestManagerWithAgents(configs []AgentConfig, toolsPerAgent [][]string) *AgentManager {
	am := New(nil)
	for i, cfg := range configs {
		tools := []string{}
		if i < len(toolsPerAgent) {
			tools = toolsPerAgent[i]
		}
		if len(tools) == 0 {
			tools = []string{"tool_" + cfg.Name}
		}
		am.Register(cfg, newTestEngine(tools...))
	}
	return am
}

func TestBuildAgentsDescriptionPrompt_Empty(t *testing.T) {
	am := New(nil)
	got := am.BuildAgentsDescriptionPrompt()
	if got != "" {
		t.Errorf("empty manager: got %q", got)
	}
}

func TestBuildAgentsDescriptionPrompt_Content(t *testing.T) {
	am := newTestManagerWithAgents(
		[]AgentConfig{
			{Name: "a1", DisplayName: "Agent One", Description: "First agent", CostTier: CostTierLow, Capabilities: []string{"cap1"}},
			{Name: "a2", DisplayName: "Agent Two", Description: "Second agent", CostTier: CostTierHigh, Capabilities: []string{"cap2", "cap3"},
				Knowledge: []KnowledgeSet{{Name: "domain1", Description: "D1"}, {Name: "domain2", Model: "gpt-4"}}},
		},
		nil,
	)
	got := am.BuildAgentsDescriptionPrompt()
	if !strings.Contains(got, "## Available Agents") {
		t.Error("missing ## Available Agents")
	}
	if !strings.Contains(got, "a1") || !strings.Contains(got, "a2") {
		t.Error("missing agent names")
	}
	if !strings.Contains(got, "First agent") || !strings.Contains(got, "Second agent") {
		t.Error("missing descriptions")
	}
	if !strings.Contains(got, "low") || !strings.Contains(got, "high") {
		t.Error("missing cost tiers")
	}
	if !strings.Contains(got, "cap1") || !strings.Contains(got, "cap2") {
		t.Error("missing capabilities")
	}
	if !strings.Contains(got, "### Knowledge Domains") {
		t.Error("missing Knowledge Domains section")
	}
	if !strings.Contains(got, "domain1") || !strings.Contains(got, "domain2") {
		t.Error("missing knowledge domain names")
	}
}

func TestBuildAgentsDescriptionPrompt_NoKnowledge(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "x", Description: "No knowledge", CostTier: CostTierLow},
	}, nil)
	got := am.BuildAgentsDescriptionPrompt()
	if strings.Contains(got, "### Knowledge Domains") {
		t.Error("should not contain Knowledge Domains when no agent has knowledge")
	}
}

func TestBuildAgentToolsPrompt_Empty(t *testing.T) {
	am := New(nil)
	got := am.BuildAgentToolsPrompt()
	if got != "" {
		t.Errorf("empty manager: got %q", got)
	}
}

func TestBuildAgentToolsPrompt_Content(t *testing.T) {
	am := newTestManagerWithAgents(
		[]AgentConfig{
			{Name: "e1", CostTier: CostTierLow},
			{Name: "e2", CostTier: CostTierLow},
		},
		[][]string{{"tool_a", "tool_b"}, {"tool_b", "tool_c"}},
	)
	got := am.BuildAgentToolsPrompt()
	if !strings.Contains(got, "## Registered Agent Tools") {
		t.Error("missing section header")
	}
	for _, name := range []string{"tool_a", "tool_b", "tool_c"} {
		if !strings.Contains(got, name) {
			t.Errorf("missing tool %q", name)
		}
	}
	// tool_b appears on both engines; should appear once in output
	count := strings.Count(got, "tool_b")
	if count != 1 {
		t.Errorf("tool_b should appear once (no duplicate), got %d", count)
	}
}

func TestBuildCallTools_Count(t *testing.T) {
	am := newTestManagerWithAgents(
		[]AgentConfig{
			{Name: "c1", CostTier: CostTierLow},
			{Name: "c2", CostTier: CostTierLow},
			{Name: "c3", CostTier: CostTierLow},
		},
		nil,
	)
	tools := am.BuildCallTools()
	if len(tools) != 3 {
		t.Errorf("BuildCallTools: got %d tools, want 3", len(tools))
	}
}

func TestBuildCallTools_Naming(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "researcher", CostTier: CostTierHigh},
	}, nil)
	tools := am.BuildCallTools()
	if len(tools) != 1 {
		t.Fatalf("got %d tools", len(tools))
	}
	want := "call_agent_researcher"
	if tools[0].Function.Name != want {
		t.Errorf("tool name: got %q, want %q", tools[0].Function.Name, want)
	}
}

func TestBuildCallTools_Description(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "x", DisplayName: "Display X", Description: "Does X", CostTier: CostTierMedium},
	}, nil)
	tools := am.BuildCallTools()
	if len(tools) != 1 {
		t.Fatalf("got %d tools", len(tools))
	}
	desc := tools[0].Function.Description
	if !strings.Contains(desc, "Display X") || !strings.Contains(desc, "medium") {
		t.Errorf("description should contain DisplayName and CostTier: %q", desc)
	}
}

func TestBuildCallTools_Schema(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "y", CostTier: CostTierLow},
	}, nil)
	tools := am.BuildCallTools()
	if len(tools) != 1 {
		t.Fatalf("got %d tools", len(tools))
	}
	params, _ := tools[0].Function.Parameters.(map[string]interface{})
	if params == nil {
		t.Fatal("parameters nil")
	}
	props, _ := params["properties"].(map[string]interface{})
	if props == nil {
		t.Fatal("properties nil")
	}
	msg, _ := props["message"].(map[string]interface{})
	if msg == nil {
		t.Fatal("message property nil")
	}
	if msg["type"] != "string" {
		t.Errorf("message type: got %v", msg["type"])
	}
	// required can be []interface{} or []string depending on SDK
	reqAny := params["required"]
	switch req := reqAny.(type) {
	case []interface{}:
		if len(req) != 1 || req[0] != "message" {
			t.Errorf("required: got %v", req)
		}
	case []string:
		if len(req) != 1 || req[0] != "message" {
			t.Errorf("required: got %v", req)
		}
	default:
		t.Errorf("required: unexpected type %T %v", reqAny, reqAny)
	}
}

func TestBuildSessionManagementTools_Count(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "low", CostTier: CostTierLow},
		{Name: "high", CostTier: CostTierHigh},
	}, nil)
	tools := am.BuildSessionManagementTools()
	if len(tools) != 2 {
		t.Errorf("got %d tools, want 2 (create_session, change_session)", len(tools))
	}
	if len(tools) >= 1 && tools[0].Function.Name != "create_session" {
		t.Errorf("first tool: got %q", tools[0].Function.Name)
	}
	if len(tools) >= 2 && tools[1].Function.Name != "change_session" {
		t.Errorf("second tool: got %q", tools[1].Function.Name)
	}
}

func TestBuildSessionManagementTools_EnumValues(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "alpha", CostTier: CostTierLow},
		{Name: "beta", CostTier: CostTierLow},
	}, nil)
	tools := am.BuildSessionManagementTools()
	if len(tools) < 1 {
		t.Fatal("no tools")
	}
	createParams, _ := tools[0].Function.Parameters.(map[string]interface{})
	if createParams == nil {
		t.Fatal("create_session parameters nil")
	}
	props, _ := createParams["properties"].(map[string]interface{})
	if props == nil {
		t.Fatal("properties nil")
	}
	agentNameProp, _ := props["agent_name"].(map[string]interface{})
	if agentNameProp == nil {
		t.Fatal("agent_name property nil")
	}
	enumAny := agentNameProp["enum"]
	var enum []string
	switch e := enumAny.(type) {
	case []interface{}:
		for _, v := range e {
			enum = append(enum, v.(string))
		}
	case []string:
		enum = e
	default:
		t.Fatalf("enum: unexpected type %T", enumAny)
	}
	if len(enum) != 2 {
		t.Fatalf("enum len: got %d", len(enum))
	}
	if enum[0] != "alpha" || enum[1] != "beta" {
		t.Errorf("enum: got %v", enum)
	}
}

func TestBuildSessionManagementTools_Empty(t *testing.T) {
	am := New(nil)
	tools := am.BuildSessionManagementTools()
	if tools != nil {
		t.Errorf("empty manager: got %v", tools)
	}
}

func TestBuildActiveSessionsPrompt_NoActive(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "a", DisplayName: "Agent A", CostTier: CostTierLow},
	}, nil)
	getSession := func(sessionID string) (*model.Session, error) { return nil, nil }
	getActiveID := func(userID string, agentType model.AgentType) string { return "" }
	got := am.BuildActiveSessionsPrompt(getSession, getActiveID, "user1")
	if got != "" {
		t.Errorf("no active sessions: expected empty, got %q", got)
	}
}

func TestBuildActiveSessionsPrompt_WithActive(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "a", DisplayName: "Agent A", CostTier: CostTierLow},
	}, nil)
	session := &model.Session{SessionID: "sess-1", Title: "Test Topic"}
	getSession := func(sessionID string) (*model.Session, error) {
		if sessionID == "sess-1" {
			return session, nil
		}
		return nil, nil
	}
	getActiveID := func(userID string, agentType model.AgentType) string {
		return "sess-1"
	}
	got := am.BuildActiveSessionsPrompt(getSession, getActiveID, "user1")
	if !strings.Contains(got, "Test Topic") {
		t.Error("output should contain session title")
	}
	if !strings.Contains(got, "sess-1") {
		t.Error("output should contain session ID")
	}
}

func TestBuildSessionContextPrompt_NoSession(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "a", CostTier: CostTierLow},
	}, nil)
	getSession := func(sessionID string) (*model.Session, error) { return nil, nil }
	getActiveID := func(userID string, agentType model.AgentType) string { return "" }
	got := am.BuildSessionContextPrompt("a", getSession, getActiveID, "user1")
	if got != "" {
		t.Errorf("no active session: got %q", got)
	}
}

func TestBuildSessionContextPrompt_WithSummary(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "a", DisplayName: "Agent A", CostTier: CostTierLow},
	}, nil)
	session := &model.Session{SessionID: "s1", Summary: "test summary", Tags: []string{"go", "testing"}}
	getSession := func(sessionID string) (*model.Session, error) {
		if sessionID == "s1" {
			return session, nil
		}
		return nil, nil
	}
	getActiveID := func(userID string, agentType model.AgentType) string { return "s1" }
	got := am.BuildSessionContextPrompt("a", getSession, getActiveID, "user1")
	if !strings.Contains(got, "## Session Context") {
		t.Error("missing ## Session Context")
	}
	if !strings.Contains(got, "test summary") {
		t.Error("missing summary text")
	}
	if !strings.Contains(got, "go") || !strings.Contains(got, "testing") {
		t.Error("missing tags")
	}
}

func TestBuildAllSessionContextsPrompt_Empty(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "a", CostTier: CostTierLow},
	}, nil)
	getSession := func(sessionID string) (*model.Session, error) { return nil, nil }
	getActiveID := func(userID string, agentType model.AgentType) string { return "" }
	got := am.BuildAllSessionContextsPrompt(getSession, getActiveID, "user1")
	if got != "" {
		t.Errorf("no context: got %q", got)
	}
}

func TestBuildAllSessionContextsPrompt_Multiple(t *testing.T) {
	am := newTestManagerWithAgents([]AgentConfig{
		{Name: "a1", CostTier: CostTierLow},
		{Name: "a2", CostTier: CostTierLow},
	}, nil)
	s1 := &model.Session{SessionID: "s1", Summary: "summary one"}
	s2 := &model.Session{SessionID: "s2", Summary: "summary two"}
	getSession := func(sessionID string) (*model.Session, error) {
		if sessionID == "s1" {
			return s1, nil
		}
		if sessionID == "s2" {
			return s2, nil
		}
		return nil, nil
	}
	getActiveID := func(userID string, agentType model.AgentType) string {
		if agentType == model.AgentType("a1") {
			return "s1"
		}
		if agentType == model.AgentType("a2") {
			return "s2"
		}
		return ""
	}
	got := am.BuildAllSessionContextsPrompt(getSession, getActiveID, "user1")
	if !strings.Contains(got, "# Agent Session Contexts") {
		t.Error("missing header")
	}
	if !strings.Contains(got, "summary one") || !strings.Contains(got, "summary two") {
		t.Error("missing agent summaries")
	}
}
