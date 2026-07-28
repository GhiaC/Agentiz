package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

type immediateToolApprovalManager struct {
	status    model.ReviewStatus
	decision  string
	note      string
	requested *model.ReviewRequest
}

func (m *immediateToolApprovalManager) Request(_ context.Context, r *model.ReviewRequest) (string, error) {
	r.ID = "rev_test"
	m.requested = r
	return r.ID, nil
}

func (m *immediateToolApprovalManager) Await(_ context.Context, id string) (*model.ReviewRequest, error) {
	m.requested.ID = id
	m.requested.Status = m.status
	m.requested.Decision = m.decision
	m.requested.Note = m.note
	return m.requested, nil
}

func TestAwaitToolApprovalBuildsGenericReview(t *testing.T) {
	manager := &immediateToolApprovalManager{
		status:   model.ReviewApproved,
		decision: "approve",
	}
	_, err := AwaitToolApproval(context.Background(), manager, ToolApprovalRequest{
		RefID:       "session-t1",
		UserID:      "u1",
		SessionID:   "s1",
		AgentType:   model.AgentTypeHigh,
		ToolName:    "send_email",
		DisplayName: "Send email",
		Arguments:   `{"to":"ops@example.com"}`,
	})
	if err != nil {
		t.Fatalf("AwaitToolApproval: %v", err)
	}
	r := manager.requested
	if r.Kind != "tool_call" || r.RefID != "session-t1" {
		t.Fatalf("unexpected subject: kind=%q ref=%q", r.Kind, r.RefID)
	}
	if r.UserID != "u1" || r.SessionID != "s1" {
		t.Fatalf("unexpected owner: user=%q session=%q", r.UserID, r.SessionID)
	}
	if got := r.Metadata["tool_name"]; got != "send_email" {
		t.Errorf("tool_name metadata = %v", got)
	}
	if got := r.Metadata["agent_type"]; got != string(model.AgentTypeHigh) {
		t.Errorf("agent_type metadata = %v", got)
	}
	if r.Content != `{"to":"ops@example.com"}` {
		t.Errorf("review content = %q", r.Content)
	}
}

func TestAwaitToolApprovalRejectsFailClosed(t *testing.T) {
	manager := &immediateToolApprovalManager{
		status:   model.ReviewRejected,
		decision: "reject",
		note:     "recipient is wrong",
	}
	_, err := AwaitToolApproval(context.Background(), manager, ToolApprovalRequest{ToolName: "send_email"})
	if !errors.Is(err, ErrToolApprovalRejected) {
		t.Fatalf("error = %v, want ErrToolApprovalRejected", err)
	}
}

func TestEngineToolApprovalControlsExecution(t *testing.T) {
	tests := []struct {
		name      string
		status    model.ReviewStatus
		decision  string
		wantCalls int
	}{
		{name: "approved", status: model.ReviewApproved, decision: "approve", wantCalls: 1},
		{name: "rejected", status: model.ReviewRejected, decision: "reject", wantCalls: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := store.NewSQLiteStore(":memory:")
			if err != nil {
				t.Fatalf("store: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })

			calls := 0
			e := &Engine{
				Sessions: st,
				Executor: func(_ string, _ map[string]interface{}) (string, error) {
					calls++
					return "executed", nil
				},
				ToolApprovalManager: &immediateToolApprovalManager{
					status:   tt.status,
					decision: tt.decision,
				},
			}
			session := model.NewSessionWithType("u1", model.AgentTypeHigh)
			result, _ := e.executeTool(context.Background(), session, "m1", openai.ToolCall{
				ID: "call_1",
				Function: openai.FunctionCall{
					Name:      "dangerous_action",
					Arguments: `{}`,
				},
			})

			if calls != tt.wantCalls {
				t.Fatalf("executor calls = %d, want %d (result=%q)", calls, tt.wantCalls, result)
			}
			if tt.wantCalls == 0 && result == "executed" {
				t.Fatal("rejected tool returned execution result")
			}
		})
	}
}
