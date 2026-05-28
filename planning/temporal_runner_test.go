package planning

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type mockTemporalAdapter struct {
	startErr    error
	workflowID  string
	queries     []*Plan
	queryIdx    atomic.Int32
	cancelledID string
	signalledID string
	signalName  string
}

func (m *mockTemporalAdapter) StartWorkflow(_ context.Context, _ *Plan) (string, error) {
	if m.startErr != nil {
		return "", m.startErr
	}
	return m.workflowID, nil
}

func (m *mockTemporalAdapter) QueryWorkflow(_ context.Context, _ string, _ string) (*Plan, error) {
	idx := int(m.queryIdx.Add(1)) - 1
	if idx < len(m.queries) {
		return m.queries[idx], nil
	}
	return m.queries[len(m.queries)-1], nil
}

func (m *mockTemporalAdapter) CancelWorkflow(_ context.Context, id string) error {
	m.cancelledID = id
	return nil
}

func (m *mockTemporalAdapter) SignalWorkflow(_ context.Context, id string, signal string, _ any) error {
	m.signalledID = id
	m.signalName = signal
	return nil
}

func TestTemporalRunner_RunStep_ReturnsInvalidStep(t *testing.T) {
	adapter := &mockTemporalAdapter{}
	runner := NewTemporalRunner(adapter)
	_, err := runner.RunStep(context.Background(), &Plan{}, &Step{})
	if !errors.Is(err, ErrInvalidStep) {
		t.Errorf("expected ErrInvalidStep, got %v", err)
	}
}

func TestTemporalRunner_Run_Success(t *testing.T) {
	adapter := &mockTemporalAdapter{
		workflowID: "wf-1",
		queries: []*Plan{
			{ID: "p1", Status: PlanRunning, Steps: []*Step{{ID: "s1", Status: StepRunning}}},
			{ID: "p1", Status: PlanCompleted, Steps: []*Step{{ID: "s1", Status: StepCompleted, Result: &StepResult{Output: "done", TokensUsed: 42}}}},
		},
	}
	runner := NewTemporalRunner(adapter)
	plan := &Plan{ID: "p1", Status: PlanPending}
	result, err := runner.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "done" {
		t.Errorf("expected output 'done', got %q", result.Output)
	}
	if result.TotalTokens != 42 {
		t.Errorf("expected 42 tokens, got %d", result.TotalTokens)
	}
}

func TestTemporalRunner_Run_ContextCancelled(t *testing.T) {
	adapter := &mockTemporalAdapter{workflowID: "wf-1"}
	runner := NewTemporalRunner(adapter)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runner.Run(ctx, &Plan{ID: "p1"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestTemporalRunner_Cancel(t *testing.T) {
	adapter := &mockTemporalAdapter{}
	runner := NewTemporalRunner(adapter)
	err := runner.Cancel(context.Background(), "plan-42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter.cancelledID != "plan-42" {
		t.Errorf("expected cancelledID plan-42, got %s", adapter.cancelledID)
	}
}

func TestTemporalRunner_Resume(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	plan := &Plan{
		ID:     "plan-99",
		Status: PlanPending,
		Steps:  []*Step{{ID: "s1", Status: StepPending}},
	}
	_ = store.Save(ctx, plan)

	adapter := &mockTemporalAdapter{
		workflowID: "plan-99",
		queries: []*Plan{
			{ID: "plan-99", Status: PlanCompleted, Steps: []*Step{{ID: "s1", Status: StepCompleted, Result: &StepResult{Output: "resumed"}}}},
		},
	}
	runner := NewTemporalRunner(adapter, WithTemporalStore(store))
	result, err := runner.Resume(ctx, "plan-99")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if adapter.signalledID != "plan-99" {
		t.Errorf("expected signal to plan-99, got %s", adapter.signalledID)
	}
	if adapter.signalName != "Resume" {
		t.Errorf("expected signal name Resume, got %s", adapter.signalName)
	}
	if result.Output != "resumed" {
		t.Errorf("expected output 'resumed', got %q", result.Output)
	}
}
