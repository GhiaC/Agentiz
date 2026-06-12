package planning

// Tests for the Chapter 02 improvements (docs/improvements/02-planning-dag.md):
// plan validation (P1/P8/P16), parallel failure reporting (P1/P2), the single
// coherent failure path (P3/P7), persist-error reporting (P6), hard step
// timeouts (P12), FilePersister (P13), interrupting Cancel (P14), empty plans
// (P15), replan-on-failure (P4) and Temporal wiring (P5).

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// countingObserver counts every lifecycle event, safely across goroutines.
type countingObserver struct {
	mu            sync.Mutex
	planCreated   int
	planCompleted int
	planFailed    int
	stepStarted   int
	stepCompleted int
	stepFailed    int
	failedStepIDs []string
}

func (o *countingObserver) OnPlanCreated(*Plan) {
	o.mu.Lock()
	o.planCreated++
	o.mu.Unlock()
}
func (o *countingObserver) OnPlanCompleted(*Plan, *PlanResult) {
	o.mu.Lock()
	o.planCompleted++
	o.mu.Unlock()
}
func (o *countingObserver) OnPlanFailed(*Plan, error) {
	o.mu.Lock()
	o.planFailed++
	o.mu.Unlock()
}
func (o *countingObserver) OnStepStarted(*Plan, *Step) {
	o.mu.Lock()
	o.stepStarted++
	o.mu.Unlock()
}
func (o *countingObserver) OnStepCompleted(*Plan, *Step, *StepResult) {
	o.mu.Lock()
	o.stepCompleted++
	o.mu.Unlock()
}
func (o *countingObserver) OnStepFailed(_ *Plan, step *Step, _ error) {
	o.mu.Lock()
	o.stepFailed++
	o.failedStepIDs = append(o.failedStepIDs, step.ID)
	o.mu.Unlock()
}

// failingStore wraps a PlanStore and fails every Update.
type failingStore struct {
	PlanStore
	updateErr error
}

func (f *failingStore) Update(context.Context, *Plan) error { return f.updateErr }

func pendingPlan(id string, steps ...*Step) *Plan {
	return &Plan{
		ID: id, UserID: "u1", SessionID: "s1", Input: "hi",
		Steps:  steps,
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
}

// --- P8/P16: ValidatePlan ---

func TestValidatePlan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		plan   *Plan
		wantOK bool
		substr string
	}{
		{name: "nil plan", plan: nil, substr: "nil plan"},
		{name: "empty plan ok", plan: &Plan{}, wantOK: true},
		{
			name:   "valid tool step",
			plan:   &Plan{Steps: []*Step{{ID: "s1", Type: StepToolCall, Config: StepConfig{ToolName: "search"}}}},
			wantOK: true,
		},
		{
			name:   "tool_call empty tool_name",
			plan:   &Plan{Steps: []*Step{{ID: "s1", Type: StepToolCall}}},
			substr: "empty tool_name",
		},
		{
			name: "unknown condition operator",
			plan: &Plan{Steps: []*Step{{
				ID: "s1", Type: StepConditional,
				Condition: &Condition{Field: "input", Operator: "containz", Value: "x"},
			}}},
			substr: "unknown operator",
		},
		{
			name: "known operators accepted",
			plan: &Plan{Steps: []*Step{{
				ID: "s1", Type: StepConditional,
				Condition: &Condition{Field: "input", Operator: "NOT_CONTAINS", Value: "x"},
			}}},
			wantOK: true,
		},
		{
			name: "conditional inside parallel",
			plan: &Plan{Steps: []*Step{{
				ID: "par", Type: StepParallel,
				Config: StepConfig{SubSteps: []*Step{{ID: "c1", Type: StepConditional}}},
			}}},
			substr: "conditional inside parallel",
		},
		{
			name: "tool sub-step without tool_name inside parallel",
			plan: &Plan{Steps: []*Step{{
				ID: "par", Type: StepParallel,
				Config: StepConfig{SubSteps: []*Step{{ID: "t1", Type: StepToolCall}}},
			}}},
			substr: "empty tool_name",
		},
		{
			name:   "invalid DAG still caught",
			plan:   &Plan{Steps: []*Step{{ID: "a", DependsOn: []string{"missing"}}}},
			substr: "unknown step",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePlan(tt.plan)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("expected valid plan, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if tt.substr != "" && !strings.Contains(err.Error(), tt.substr) {
				t.Errorf("error %q does not contain %q", err.Error(), tt.substr)
			}
		})
	}
}

func TestLocalRunner_Run_RejectsInvalidPlanUpfront(t *testing.T) {
	llm := &mockLLM{out: "ok"}
	obs := &countingObserver{}
	lr := NewLocalRunner(llm, &mockTools{}, WithLocalObserver(obs))

	plan := pendingPlan("p1",
		&Step{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "a"}},
		&Step{ID: "s2", Type: StepToolCall, Status: StepPending}, // empty tool_name
	)
	_, err := lr.Run(context.Background(), plan)
	if !errors.Is(err, ErrInvalidStep) {
		t.Fatalf("expected ErrInvalidStep, got %v", err)
	}
	if len(llm.calls) != 0 {
		t.Errorf("expected no step to execute, got %d LLM calls", len(llm.calls))
	}
	if plan.Status != PlanFailed {
		t.Errorf("plan status: got %s", plan.Status)
	}
	if obs.planFailed != 1 {
		t.Errorf("OnPlanFailed: got %d, want 1", obs.planFailed)
	}
}

func TestLLMPlanner_ParsePlan_RejectsConditionalInsideParallel(t *testing.T) {
	content := `{"steps":[{"id":"par","type":"parallel","sub_steps":[{"id":"c1","type":"conditional"}]}]}`
	p := NewLLMPlanner(&mockChatClient{response: content}, "m")
	_, err := p.CreatePlan(context.Background(), PlanInput{UserID: "u1", Message: "go"})
	if !errors.Is(err, ErrInvalidStep) {
		t.Fatalf("expected ErrInvalidStep, got %v", err)
	}
}

func TestLLMPlanner_ParsePlan_RejectsUnknownOperator(t *testing.T) {
	content := `{"steps":[{"id":"c1","type":"conditional","condition":{"field":"input","operator":"containz","value":"x"}}]}`
	p := NewLLMPlanner(&mockChatClient{response: content}, "m")
	_, err := p.CreatePlan(context.Background(), PlanInput{UserID: "u1", Message: "go"})
	if !errors.Is(err, ErrInvalidStep) {
		t.Fatalf("expected ErrInvalidStep, got %v", err)
	}
}

// --- P1: conditional inside parallel rejected at run time too ---

func TestLocalRunner_ParallelStep_RejectsConditionalSubStep(t *testing.T) {
	lr := NewLocalRunner(&mockLLM{out: "ok"}, &mockTools{})
	step := &Step{
		ID: "par", Type: StepParallel,
		Config: StepConfig{SubSteps: []*Step{{ID: "c1", Type: StepConditional, Status: StepPending}}},
	}
	_, err := lr.RunStep(context.Background(), pendingPlan("p1", step), step)
	if !errors.Is(err, ErrInvalidStep) || !strings.Contains(err.Error(), "conditional inside parallel") {
		t.Fatalf("expected conditional-inside-parallel rejection, got %v", err)
	}
}

// --- P2: parallel sub-step failure names the sub-step and notifies the observer ---

type perToolExecutor struct {
	mu    sync.Mutex
	errBy map[string]error
	calls []string
}

func (m *perToolExecutor) Execute(toolName string, _ map[string]interface{}) (string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, toolName)
	m.mu.Unlock()
	if err := m.errBy[toolName]; err != nil {
		return "", err
	}
	return "ok-" + toolName, nil
}

func TestLocalRunner_ParallelStep_SubStepFailureReported(t *testing.T) {
	boom := errors.New("boom")
	tools := &perToolExecutor{errBy: map[string]error{"bad": boom}}
	obs := &countingObserver{}
	lr := NewLocalRunner(&mockLLM{out: "ok"}, tools, WithLocalObserver(obs))

	subBad := &Step{ID: "sub-bad", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "bad"}}
	subGood := &Step{ID: "sub-good", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "good"}}
	par := &Step{ID: "par", Type: StepParallel, Status: StepPending, Config: StepConfig{SubSteps: []*Step{subBad, subGood}}}
	plan := pendingPlan("p1", par)

	_, err := lr.Run(context.Background(), plan)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("expected wrapped boom, got %v", err)
	}
	if !strings.Contains(err.Error(), `sub-step "sub-bad"`) {
		t.Errorf("error does not name the failed sub-step: %v", err)
	}
	// All branches ran to completion despite the failure (no early return).
	if len(tools.calls) != 2 {
		t.Errorf("expected both sub-steps to run, got calls %v", tools.calls)
	}
	if subBad.Status != StepFailed || subGood.Status != StepCompleted {
		t.Errorf("sub-step statuses: bad=%s good=%s", subBad.Status, subGood.Status)
	}
	// Observer was told about the failed sub-step AND the parent parallel step.
	obs.mu.Lock()
	failedIDs := append([]string(nil), obs.failedStepIDs...)
	obs.mu.Unlock()
	want := map[string]bool{"sub-bad": true, "par": true}
	for _, id := range failedIDs {
		delete(want, id)
	}
	if len(want) > 0 {
		t.Errorf("OnStepFailed missing for %v (got %v)", want, failedIDs)
	}
}

// --- P3/P7: failure events fire exactly once each ---

func TestLocalRunner_Run_FailurePath_SingleEvents(t *testing.T) {
	boom := errors.New("boom")
	obs := &countingObserver{}
	lr := NewLocalRunner(&mockLLM{err: boom}, &mockTools{}, WithLocalObserver(obs))

	plan := pendingPlan("p1", &Step{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "x"}})
	_, err := lr.Run(context.Background(), plan)
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	if obs.stepFailed != 1 {
		t.Errorf("OnStepFailed: got %d, want exactly 1", obs.stepFailed)
	}
	if obs.planFailed != 1 {
		t.Errorf("OnPlanFailed: got %d, want exactly 1", obs.planFailed)
	}
	if plan.Status != PlanFailed || plan.Steps[0].Status != StepFailed {
		t.Errorf("statuses: plan=%s step=%s", plan.Status, plan.Steps[0].Status)
	}
	if plan.CompletedAt == nil || plan.Steps[0].CompletedAt == nil {
		t.Error("expected CompletedAt set on plan and step")
	}
}

// --- P6: store failures are reported, not swallowed ---

func TestLocalRunner_Run_PersistErrorHandlerCalled(t *testing.T) {
	storeErr := errors.New("disk full")
	var mu sync.Mutex
	var reported []error
	lr := NewLocalRunner(&mockLLM{out: "ok"}, &mockTools{},
		WithLocalStore(&failingStore{PlanStore: NewMemoryStore(), updateErr: storeErr}),
		WithLocalPersistErrorHandler(func(_ *Plan, err error) {
			mu.Lock()
			reported = append(reported, err)
			mu.Unlock()
		}),
	)
	plan := pendingPlan("p1", &Step{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "x"}})
	result, err := lr.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("run should still succeed despite store failures: %v", err)
	}
	if result == nil || plan.Status != PlanCompleted {
		t.Fatalf("expected completed plan, got %s", plan.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) == 0 {
		t.Fatal("expected persist-error handler to be called")
	}
	for _, e := range reported {
		if !errors.Is(e, storeErr) {
			t.Errorf("expected store error, got %v", e)
		}
	}
}

// --- P12: hard timeout even when the engine ignores the context ---

func TestLocalRunner_RunStep_HardTimeout_EngineIgnoresContext(t *testing.T) {
	// mockLLM sleeps without checking ctx — exactly the engine the hard
	// deadline protects against.
	llm := &mockLLM{out: "late", delay: 2 * time.Second}
	lr := NewLocalRunner(llm, &mockTools{})
	step := &Step{
		ID: "s1", Type: StepLLMCall, Status: StepPending,
		Config: StepConfig{Prompt: "x", Timeout: 30 * time.Millisecond},
	}
	plan := pendingPlan("p1", step)

	start := time.Now()
	_, err := lr.RunStep(context.Background(), plan, step)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrStepTimeout) {
		t.Fatalf("expected ErrStepTimeout, got %v", err)
	}
	if elapsed >= time.Second {
		t.Errorf("runner did not move on at the deadline: took %s", elapsed)
	}
}

// --- P13: FilePersister ---

func TestFilePersister_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "plans.jsonl")
	p := NewFilePersister(path)
	ctx := context.Background()

	// Missing file: no plans, no error.
	plans, err := p.LoadPlans(ctx)
	if err != nil || plans != nil {
		t.Fatalf("expected empty load from missing file, got %v / %v", plans, err)
	}

	in := []*Plan{
		pendingPlan("p1", &Step{ID: "s1", Type: StepLLMCall, Status: StepCompleted, Result: &StepResult{Output: "hello"}}),
		pendingPlan("p2"),
	}
	if err := p.SavePlans(ctx, in); err != nil {
		t.Fatalf("SavePlans: %v", err)
	}
	out, err := p.LoadPlans(ctx)
	if err != nil {
		t.Fatalf("LoadPlans: %v", err)
	}
	if len(out) != 2 || out[0].ID != "p1" || out[1].ID != "p2" {
		t.Fatalf("unexpected plans: %+v", out)
	}
	if out[0].Steps[0].Result.Output != "hello" {
		t.Errorf("step result lost in round trip")
	}

	// Works with MemoryStore end to end.
	store := NewMemoryStore(WithPersister(p))
	if err := store.LoadFromPersister(ctx); err != nil {
		t.Fatalf("LoadFromPersister: %v", err)
	}
	got, err := store.Get(ctx, "p1")
	if err != nil || got.ID != "p1" {
		t.Fatalf("expected p1 from store, got %v / %v", got, err)
	}
}

// --- P14: Cancel interrupts an in-flight step ---

type blockingLLM struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingLLM) ProcessMessage(ctx context.Context, _, _ string) (string, int, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return "", 0, ctx.Err()
}

func TestLocalRunner_Cancel_InterruptsRunningPlan(t *testing.T) {
	llm := &blockingLLM{started: make(chan struct{})}
	store := NewMemoryStore()
	lr := NewLocalRunner(llm, &mockTools{}, WithLocalStore(store))

	plan := pendingPlan("p1", &Step{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "x"}})
	_ = store.Save(context.Background(), plan)

	done := make(chan error, 1)
	go func() {
		_, err := lr.Run(context.Background(), plan)
		done <- err
	}()

	<-llm.started
	if err := lr.Cancel(context.Background(), "p1"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error from cancelled run")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Cancel — in-flight step was not interrupted")
	}
	if plan.Status != PlanCancelled {
		t.Errorf("plan status: got %s, want cancelled", plan.Status)
	}
}

// --- P15: empty plan trivially succeeds ---

func TestLocalRunner_Run_EmptyPlanCompletes(t *testing.T) {
	obs := &countingObserver{}
	store := NewMemoryStore()
	lr := NewLocalRunner(&mockLLM{}, &mockTools{}, WithLocalStore(store), WithLocalObserver(obs))

	plan := pendingPlan("p1")
	_ = store.Save(context.Background(), plan)
	result, err := lr.Run(context.Background(), plan)
	if err != nil {
		t.Fatalf("empty plan should succeed: %v", err)
	}
	if result == nil || result.Output != "" {
		t.Fatalf("expected empty result, got %+v", result)
	}
	if plan.Status != PlanCompleted {
		t.Errorf("plan status: got %s", plan.Status)
	}
	if obs.planCompleted != 1 || obs.planFailed != 0 {
		t.Errorf("observer: completed=%d failed=%d", obs.planCompleted, obs.planFailed)
	}
}

// --- P4: replan-on-failure is reachable from the Orchestrator ---

// flakyReplanPlanner produces a plan whose tool step fails, then "fixes" it on
// Replan by switching to a working tool.
type flakyReplanPlanner struct {
	mu           sync.Mutex
	replanCalls  int
	lastFailedID string
}

func (p *flakyReplanPlanner) CreatePlan(_ context.Context, input PlanInput) (*Plan, error) {
	return &Plan{
		UserID: input.UserID, SessionID: input.SessionID, Input: input.Message,
		Steps:  []*Step{{ID: "s1", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "bad"}}},
		Status: PlanPending, Version: 1,
	}, nil
}

func (p *flakyReplanPlanner) Replan(_ context.Context, plan *Plan, lastStep *Step) (*Plan, error) {
	p.mu.Lock()
	p.replanCalls++
	if lastStep != nil {
		p.lastFailedID = lastStep.ID
	}
	p.mu.Unlock()
	return &Plan{
		ID: plan.ID, UserID: plan.UserID, SessionID: plan.SessionID, Input: plan.Input,
		Steps:  []*Step{{ID: "s1", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "good"}}},
		Status: PlanPending, Version: plan.Version + 1, CreatedAt: plan.CreatedAt, UpdatedAt: time.Now(),
	}, nil
}

func TestOrchestrator_ReplanOnFailure(t *testing.T) {
	tools := &perToolExecutor{errBy: map[string]error{"bad": errors.New("bad tool")}}
	planner := &flakyReplanPlanner{}
	runner := NewLocalRunner(&mockLLM{out: "ok"}, tools)
	orch := NewOrchestrator(planner, runner,
		WithOrchestratorStore(NewMemoryStore()),
		WithReplanOnFailure(2),
	)

	result, err := orch.Execute(context.Background(), PlanInput{UserID: "u1", SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("expected success after replan, got %v", err)
	}
	if result.Output != "ok-good" {
		t.Errorf("output: got %q", result.Output)
	}
	if planner.replanCalls != 1 {
		t.Errorf("Replan calls: got %d, want 1", planner.replanCalls)
	}
	if planner.lastFailedID != "s1" {
		t.Errorf("Replan got failed step %q, want s1", planner.lastFailedID)
	}
}

func TestOrchestrator_NoReplanByDefault(t *testing.T) {
	tools := &perToolExecutor{errBy: map[string]error{"bad": errors.New("bad tool")}}
	planner := &flakyReplanPlanner{}
	runner := NewLocalRunner(&mockLLM{out: "ok"}, tools)
	orch := NewOrchestrator(planner, runner, WithOrchestratorStore(NewMemoryStore()))

	_, err := orch.Execute(context.Background(), PlanInput{UserID: "u1", SessionID: "s1", Message: "go"})
	if err == nil {
		t.Fatal("expected failure without WithReplanOnFailure")
	}
	if planner.replanCalls != 0 {
		t.Errorf("Replan must not be called by default, got %d calls", planner.replanCalls)
	}
}

func TestChainPlanner_Replan_ResetsFailedStep(t *testing.T) {
	cp := NewChainPlanner()
	failed := &Step{ID: "s1", Status: StepFailed, Error: "boom"}
	plan := &Plan{ID: "p1", Status: PlanFailed, Error: "boom", Steps: []*Step{failed}, Version: 1}
	out, err := cp.Replan(context.Background(), plan, failed)
	if err != nil {
		t.Fatalf("Replan: %v", err)
	}
	if out.Status != PlanPending || failed.Status != StepPending || failed.Error != "" {
		t.Errorf("expected reset to pending, got plan=%s step=%s err=%q", out.Status, failed.Status, failed.Error)
	}
	if out.Version != 2 {
		t.Errorf("version: got %d, want 2", out.Version)
	}
}

// --- P5: Temporal runner wiring ---

func TestOrchestrator_WithTemporalRunner(t *testing.T) {
	adapter := &mockTemporalAdapter{
		workflowID: "wf-1",
		queries: []*Plan{
			{ID: "p1", Status: PlanCompleted, Steps: []*Step{{ID: "s1", Status: StepCompleted, Result: &StepResult{Output: "from-temporal"}}}},
		},
	}
	planner := &mockPlanner{plan: &Plan{
		Steps:  []*Step{{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "x"}}},
		Status: PlanPending,
	}}
	orch := NewOrchestrator(planner, nil, // no in-process runner needed
		WithOrchestratorStore(NewMemoryStore()),
		WithTemporalRunner(adapter),
	)
	result, err := orch.Execute(context.Background(), PlanInput{UserID: "u1", SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("Execute via temporal: %v", err)
	}
	if result.Output != "from-temporal" {
		t.Errorf("output: got %q", result.Output)
	}
}

func TestTemporalRunner_RunStep_DescriptiveError(t *testing.T) {
	runner := NewTemporalRunner(&mockTemporalAdapter{})
	_, err := runner.RunStep(context.Background(), &Plan{}, &Step{ID: "s1"})
	if !errors.Is(err, ErrInvalidStep) {
		t.Fatalf("expected ErrInvalidStep, got %v", err)
	}
	if !strings.Contains(err.Error(), "Temporal workflow") {
		t.Errorf("error should explain where steps run: %v", err)
	}
}

// --- P9: collect reports dependencies that contributed nothing ---

func TestLocalRunner_CollectStep_ReportsMissingInputs(t *testing.T) {
	lr := NewLocalRunner(nil, &mockTools{}) // no LLM: collect returns labeled outputs
	done := &Step{ID: "a", Type: StepLLMCall, Status: StepCompleted, Result: &StepResult{Output: "A out"}}
	skipped := &Step{ID: "b", Type: StepLLMCall, Status: StepSkipped}
	collect := &Step{ID: "c", Type: StepCollect, Status: StepPending, DependsOn: []string{"a", "b"}}
	plan := pendingPlan("p1", done, skipped, collect)

	res, err := lr.RunStep(context.Background(), plan, collect)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	missing, _ := res.Metadata["missing_inputs"].([]string)
	if len(missing) != 1 || missing[0] != "b" {
		t.Errorf("missing_inputs: got %v, want [b]", res.Metadata["missing_inputs"])
	}
	if !strings.Contains(res.Output, "A out") {
		t.Errorf("collected output lost: %q", res.Output)
	}
}

// --- P11: race coverage for parallel branches (run with -race) ---

func TestLocalRunner_Parallel_NoDataRace(t *testing.T) {
	tools := &perToolExecutor{errBy: map[string]error{}}
	store := NewMemoryStore()
	lr := NewLocalRunner(&mockLLM{out: "ok", tokens: 1}, tools, WithLocalStore(store))

	// Two parallel fan-outs plus a collect join, with concurrent result writes.
	var subs1, subs2 []*Step
	for i := 0; i < 8; i++ {
		subs1 = append(subs1, &Step{ID: fmt.Sprintf("a%d", i), Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: fmt.Sprintf("t%d", i)}})
		subs2 = append(subs2, &Step{ID: fmt.Sprintf("b%d", i), Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "x"}})
	}
	plan := pendingPlan("p1",
		&Step{ID: "par1", Type: StepParallel, Status: StepPending, Config: StepConfig{SubSteps: subs1}},
		&Step{ID: "par2", Type: StepParallel, Status: StepPending, DependsOn: []string{"par1"}, Config: StepConfig{SubSteps: subs2}},
		&Step{ID: "join", Type: StepCollect, Status: StepPending, DependsOn: []string{"par1", "par2"}},
	)
	_ = store.Save(context.Background(), plan)
	if _, err := lr.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if plan.Status != PlanCompleted {
		t.Errorf("plan status: got %s", plan.Status)
	}
}
