package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sashabaranov/go-openai"
)

type mockLLMClient struct {
	response string
	err      error
}

func (m *mockLLMClient) CreateChatCompletion(_ context.Context, _ openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if m.err != nil {
		return openai.ChatCompletionResponse{}, m.err
	}
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: m.response}},
		},
	}, nil
}

func TestLLMPlanner_CreatePlan_Success(t *testing.T) {
	plan := llmPlanJSON{Steps: []struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Config      struct {
			Prompt     string         `json:"prompt"`
			ToolName   string         `json:"tool_name"`
			ToolArgs   map[string]any `json:"tool_args"`
			AgentInput string         `json:"agent_input"`
		} `json:"config"`
		DependsOn []string `json:"depends_on"`
	}{
		{ID: "s1", Type: "llm_call", Name: "test-step"},
	}}
	data, _ := json.Marshal(plan)

	p := NewLLMPlanner(&mockLLMClient{response: string(data)}, "test-model")
	result, err := p.CreatePlan(context.Background(), PlanInput{
		UserID:  "u1",
		Message: "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(result.Steps))
	}
	if result.Steps[0].ID != "s1" {
		t.Errorf("expected step ID s1, got %s", result.Steps[0].ID)
	}
	if result.Status != PlanPending {
		t.Errorf("expected PlanPending, got %s", result.Status)
	}
	if result.Version != 1 {
		t.Errorf("expected version 1, got %d", result.Version)
	}
}

func TestLLMPlanner_CreatePlan_WithMarkdownFence(t *testing.T) {
	inner := `{"steps":[{"id":"s1","type":"tool_call","name":"search","config":{"tool_name":"web_search"}}]}`
	fenced := "```json\n" + inner + "\n```"

	p := NewLLMPlanner(&mockLLMClient{response: fenced}, "test-model")
	result, err := p.CreatePlan(context.Background(), PlanInput{Message: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(result.Steps))
	}
	if result.Steps[0].Config.ToolName != "web_search" {
		t.Errorf("expected tool_name web_search, got %s", result.Steps[0].Config.ToolName)
	}
}

func TestLLMPlanner_CreatePlan_InvalidJSON(t *testing.T) {
	p := NewLLMPlanner(&mockLLMClient{response: "this is not json"}, "test-model")
	_, err := p.CreatePlan(context.Background(), PlanInput{Message: "test"})
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestLLMPlanner_CreatePlan_TruncatesAtMaxSteps(t *testing.T) {
	var steps []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
	}
	for i := 0; i < 25; i++ {
		steps = append(steps, struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Name string `json:"name"`
		}{ID: fmt.Sprintf("s%d", i+1), Type: "llm_call", Name: fmt.Sprintf("step-%d", i+1)})
	}
	data, _ := json.Marshal(map[string]any{"steps": steps})

	p := NewLLMPlanner(&mockLLMClient{response: string(data)}, "test-model")
	p.SetMaxSteps(5)
	result, err := p.CreatePlan(context.Background(), PlanInput{Message: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Steps) != 5 {
		t.Fatalf("expected 5 steps (truncated), got %d", len(result.Steps))
	}
}

func TestLLMPlanner_CreatePlan_LLMError(t *testing.T) {
	p := NewLLMPlanner(&mockLLMClient{err: fmt.Errorf("rate limit")}, "test-model")
	_, err := p.CreatePlan(context.Background(), PlanInput{Message: "test"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLLMPlanner_Replan_Success(t *testing.T) {
	inner := `{"steps":[{"id":"s1","type":"llm_call","name":"revised"}]}`
	p := NewLLMPlanner(&mockLLMClient{response: inner}, "test-model")

	plan := &Plan{
		ID:        "plan-1",
		UserID:    "u1",
		SessionID: "sess-1",
		Input:     "original",
		Version:   3,
	}
	step := &Step{
		ID:     "s1",
		Result: &StepResult{Output: "done"},
	}
	newPlan, err := p.Replan(context.Background(), plan, step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newPlan.Version != 4 {
		t.Errorf("expected version 4, got %d", newPlan.Version)
	}
	if newPlan.ID != "plan-1" {
		t.Errorf("expected plan ID plan-1, got %s", newPlan.ID)
	}
}

func TestLLMPlanner_Replan_NilResult(t *testing.T) {
	inner := `{"steps":[{"id":"s1","type":"llm_call","name":"revised"}]}`
	p := NewLLMPlanner(&mockLLMClient{response: inner}, "test-model")

	plan := &Plan{
		ID:      "plan-1",
		Input:   "original",
		Version: 1,
	}
	step := &Step{ID: "s1", Result: nil}

	newPlan, err := p.Replan(context.Background(), plan, step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newPlan == nil {
		t.Fatal("expected non-nil plan")
	}
}

// captureLLMClient records the request for assertions.
type captureLLMClient struct {
	response string
	lastReq  openai.ChatCompletionRequest
}

func (m *captureLLMClient) CreateChatCompletion(_ context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	m.lastReq = req
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{
			{Message: openai.ChatCompletionMessage{Content: m.response}},
		},
	}, nil
}

func TestFormatToolsPrompt_Empty(t *testing.T) {
	result := FormatToolsPrompt(nil)
	if result != "" {
		t.Errorf("expected empty string for nil tools, got %q", result)
	}
	result = FormatToolsPrompt([]ToolInfo{})
	if result != "" {
		t.Errorf("expected empty string for empty tools, got %q", result)
	}
}

func TestFormatToolsPrompt_WithTools(t *testing.T) {
	tools := []ToolInfo{
		{Name: "web_search", Description: "Search the web", Parameters: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
			},
		}},
		{Name: "sleep", Description: "Wait N seconds"},
	}
	result := FormatToolsPrompt(tools)
	if !strings.Contains(result, "web_search") {
		t.Error("expected tool name web_search in prompt")
	}
	if !strings.Contains(result, "Search the web") {
		t.Error("expected tool description in prompt")
	}
	if !strings.Contains(result, "sleep") {
		t.Error("expected tool name sleep in prompt")
	}
	if !strings.Contains(result, "Parameters:") {
		t.Error("expected Parameters section for web_search")
	}
}

func TestLLMPlanner_BuildSystemPrompts_NoContext(t *testing.T) {
	planJSON := `{"steps":[{"id":"s1","type":"llm_call","name":"test"}]}`
	client := &captureLLMClient{response: planJSON}
	p := NewLLMPlanner(client, "m1")

	_, err := p.CreatePlan(context.Background(), PlanInput{Message: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With no context, only the base planner prompt should be sent as a system message.
	systemMsgs := 0
	for _, msg := range client.lastReq.Messages {
		if msg.Role == openai.ChatMessageRoleSystem {
			systemMsgs++
		}
	}
	if systemMsgs != 1 {
		t.Errorf("expected 1 system message (base prompt), got %d", systemMsgs)
	}
}

func TestLLMPlanner_BuildSystemPrompts_WithToolsAndPrompts(t *testing.T) {
	planJSON := `{"steps":[{"id":"s1","type":"llm_call","name":"test"}]}`
	client := &captureLLMClient{response: planJSON}
	p := NewLLMPlanner(client, "m1")

	_, err := p.CreatePlan(context.Background(), PlanInput{
		Message: "search for gold",
		Context: &PlanContext{
			AvailableTools: []ToolInfo{
				{Name: "web_search", Description: "Search the web"},
			},
			SystemPrompts: []string{"You are a gold expert."},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect: base prompt + tools prompt + 1 extra system prompt = 3 system messages, then 1 user message.
	systemMsgs := 0
	userMsgs := 0
	for _, msg := range client.lastReq.Messages {
		switch msg.Role {
		case openai.ChatMessageRoleSystem:
			systemMsgs++
		case openai.ChatMessageRoleUser:
			userMsgs++
		}
	}
	if systemMsgs != 3 {
		t.Errorf("expected 3 system messages (base + tools + custom), got %d", systemMsgs)
	}
	if userMsgs != 1 {
		t.Errorf("expected 1 user message, got %d", userMsgs)
	}
	// The user message should be the last message.
	lastMsg := client.lastReq.Messages[len(client.lastReq.Messages)-1]
	if lastMsg.Role != openai.ChatMessageRoleUser || lastMsg.Content != "search for gold" {
		t.Errorf("last message should be user 'search for gold', got role=%s content=%q", lastMsg.Role, lastMsg.Content)
	}
}

func TestLLMPlanner_BuildSystemPrompts_EmptySystemPromptSkipped(t *testing.T) {
	planJSON := `{"steps":[{"id":"s1","type":"llm_call","name":"test"}]}`
	client := &captureLLMClient{response: planJSON}
	p := NewLLMPlanner(client, "m1")

	_, err := p.CreatePlan(context.Background(), PlanInput{
		Message: "test",
		Context: &PlanContext{
			SystemPrompts: []string{"", "real prompt", ""},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	systemMsgs := 0
	for _, msg := range client.lastReq.Messages {
		if msg.Role == openai.ChatMessageRoleSystem {
			systemMsgs++
		}
	}
	// base prompt + "real prompt" = 2 (empty strings skipped)
	if systemMsgs != 2 {
		t.Errorf("expected 2 system messages (empty ones skipped), got %d", systemMsgs)
	}
}

func TestLLMPlanner_CreatePlan_WithSessionHistory(t *testing.T) {
	planJSON := `{"steps":[{"id":"s1","type":"llm_call","name":"test"}]}`
	client := &captureLLMClient{response: planJSON}
	p := NewLLMPlanner(client, "m1")

	_, err := p.CreatePlan(context.Background(), PlanInput{
		Message: "continue",
		Context: &PlanContext{
			SessionHistory: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleUser, Content: "previous message"},
				{Role: openai.ChatMessageRoleAssistant, Content: "previous reply"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Session history should be between system messages and the final user message.
	msgs := client.lastReq.Messages
	if len(msgs) != 4 { // 1 system + 2 history + 1 user
		t.Fatalf("expected 4 messages, got %d", len(msgs))
	}
	if msgs[0].Role != openai.ChatMessageRoleSystem {
		t.Errorf("msgs[0] should be system, got %s", msgs[0].Role)
	}
	if msgs[len(msgs)-1].Content != "continue" {
		t.Errorf("last message should be 'continue', got %q", msgs[len(msgs)-1].Content)
	}
}
