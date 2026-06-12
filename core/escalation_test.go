package core

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/ghiac/agentize/agentmanager"
	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

// newTieredCore is like newDispatchableCore but registers each agent at the
// given cost tier, so escalation paths (low → medium → high) can be exercised.
func newTieredCore(t *testing.T, agents map[string]agentmanager.CostTier) (*CoreHandler, *store.SQLiteStore, *MockLLMTransport, map[string]*MockLLMTransport) {
	t.Helper()
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	cfg := model.DefaultSessionHandlerConfig()
	cfg.DisableLogs = true
	sh := model.NewSessionHandler(sqliteStore, cfg)
	am := agentmanager.New(sh)

	agentTx := make(map[string]*MockLLMTransport, len(agents))
	for name, tier := range agents {
		tx := &MockLLMTransport{}
		agentTx[name] = tx
		ac := agentmanager.AgentConfig{
			Name:        name,
			DisplayName: name,
			AgentType:   model.AgentType(name),
			CostTier:    tier,
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

func agentAnswer(content string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message:      openai.ChatCompletionMessage{Role: openai.ChatMessageRoleAssistant, Content: content},
			FinishReason: openai.FinishReasonStop,
		}},
		Usage: openai.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}
}

func coreDispatchTurn(agent, message string) openai.ChatCompletionResponse {
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{
			Message: openai.ChatCompletionMessage{
				Role: openai.ChatMessageRoleAssistant,
				ToolCalls: []openai.ToolCall{{
					ID:       "c1",
					Type:     openai.ToolTypeFunction,
					Function: openai.FunctionCall{Name: "call_agent_" + agent, Arguments: `{"message":"` + message + `"}`},
				}},
			},
			FinishReason: openai.FinishReasonToolCalls,
		}},
		Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
}

// TestEscalation_LowToHigher verifies the ESCALATE: contract end to end: the
// Core dispatches to a low-tier agent; the agent answers "ESCALATE: …"; the
// Core re-dispatches the same message to a higher-tier agent and returns the
// higher agent's answer verbatim, recording the escalation on the routing DAG.
func TestEscalation_LowToHigher(t *testing.T) {
	ch, st, coreTx, agentTx := newTieredCore(t, map[string]agentmanager.CostTier{
		"cheap": agentmanager.CostTierLow,
		"smart": agentmanager.CostTierHigh,
	})

	coreTx.AddResponse(coreDispatchTurn("cheap", "hard question"))
	agentTx["cheap"].AddResponse(agentAnswer("ESCALATE: this needs a stronger model"))
	agentTx["smart"].AddResponse(agentAnswer("smart answer"))

	resp, err := ch.ProcessMessage(context.Background(), "user1", "hard question")
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if resp != "smart answer" {
		t.Fatalf("response = %q, want the escalated agent's answer", resp)
	}
	if agentTx["smart"].Calls() == 0 {
		t.Error("high-tier agent never ran; want escalation to dispatch it")
	}

	traces, err := st.GetRouteTracesByUser("user1")
	if err != nil {
		t.Fatalf("GetRouteTracesByUser: %v", err)
	}
	if len(traces) != 1 {
		t.Fatalf("got %d traces, want 1", len(traces))
	}
	var escalations []model.RouteNode
	for _, n := range traces[0].Nodes {
		if n.Type == model.RouteNodeEscalation {
			escalations = append(escalations, n)
		}
	}
	if len(escalations) != 1 || escalations[0].Agent != "smart" {
		t.Errorf("escalation nodes = %+v, want exactly one for agent smart", escalations)
	}
}

// TestEscalation_NoHigherTier verifies the graceful fallback: when no
// higher-tier agent exists, the ESCALATE: text itself is returned (the Core
// does not error and does not loop).
func TestEscalation_NoHigherTier(t *testing.T) {
	ch, _, coreTx, agentTx := newTieredCore(t, map[string]agentmanager.CostTier{
		"solo": agentmanager.CostTierHigh, // high tier → nothing above it
	})

	coreTx.AddResponse(coreDispatchTurn("solo", "question"))
	agentTx["solo"].AddResponse(agentAnswer("ESCALATE: nobody can help"))

	resp, err := ch.ProcessMessage(context.Background(), "user1", "question")
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if !strings.HasPrefix(resp, "ESCALATE:") {
		t.Fatalf("response = %q, want the raw ESCALATE text when no higher tier exists", resp)
	}
}

// TestEscalation_MediumSkippedToHigh verifies the low→high fallthrough when no
// medium-tier agent is registered.
func TestEscalation_MediumSkippedToHigh(t *testing.T) {
	ch, _, coreTx, agentTx := newTieredCore(t, map[string]agentmanager.CostTier{
		"cheap": agentmanager.CostTierLow,
		"best":  agentmanager.CostTierHigh, // no medium registered
	})

	coreTx.AddResponse(coreDispatchTurn("cheap", "question"))
	agentTx["cheap"].AddResponse(agentAnswer("ESCALATE: over my head"))
	agentTx["best"].AddResponse(agentAnswer("best answer"))

	resp, err := ch.ProcessMessage(context.Background(), "user1", "question")
	if err != nil {
		t.Fatalf("ProcessMessage: %v", err)
	}
	if resp != "best answer" {
		t.Fatalf("response = %q, want %q (low must escalate straight to high when medium is absent)", resp, "best answer")
	}
}
