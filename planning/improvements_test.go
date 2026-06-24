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
			name: "valid conditional inside parallel",
			plan: &Plan{Steps: []*Step{{
				ID: "par", Type: StepParallel,
				Config: StepConfig{SubSteps: []*Step{
					{ID: "c1", Type: StepConditional, Condition: &Condition{Field: "input", Operator: "contains", Value: "x"}, Branches: map[string][]string{"true": {"t"}, "false": {"f"}}},
					{ID: "t", Type: StepLLMCall, DependsOn: []string{"c1"}, Config: StepConfig{Prompt: "t"}},
					{ID: "f", Type: StepLLMCall, DependsOn: []string{"c1"}, Config: StepConfig{Prompt: "f"}},
				}},
			}}},
			wantOK: true,
		},
		{
			name: "conditional in parallel branches outside block",
			plan: &Plan{Steps: []*Step{{
				ID: "par", Type: StepParallel,
				Config: StepConfig{SubSteps: []*Step{
					{ID: "c1", Type: StepConditional, Branches: map[string][]string{"true": {"external"}}},
				}},
			}}},
			substr: "not a sub-step",
		},
		{
			name: "conditional in parallel branch missing dependency",
			plan: &Plan{Steps: []*Step{{
				ID: "par", Type: StepParallel,
				Config: StepConfig{SubSteps: []*Step{
					{ID: "c1", Type: StepConditional, Branches: map[string][]string{"true": {"t"}}},
					{ID: "t", Type: StepLLMCall, Config: StepConfig{Prompt: "t"}}, // missing DependsOn c1
				}},
			}}},
			substr: "does not depend on it",
		},
		{
			name: "conditional in parallel target gated twice",
			plan: &Plan{Steps: []*Step{{
				ID: "par", Type: StepParallel,
				Config: StepConfig{SubSteps: []*Step{
					{ID: "c1", Type: StepConditional, Branches: map[string][]string{"true": {"t"}}},
					{ID: "c2", Type: StepConditional, Branches: map[string][]string{"true": {"t"}}},
					{ID: "t", Type: StepLLMCall, DependsOn: []string{"c1", "c2"}, Config: StepConfig{Prompt: "t"}},
				}},
			}}},
			substr: "gated by two conditionals",
		},
		{
			// A parallel block runs as an isolated mini-plan, so a sub-step
			// depending on a step OUTSIDE the block could never resolve — reject
			// it up front instead of silently never running it.
			name: "parallel sub-step depends outside the block",
			plan: &Plan{Steps: []*Step{
				{ID: "ext", Type: StepLLMCall, Config: StepConfig{Prompt: "x"}},
				{ID: "par", Type: StepParallel, DependsOn: []string{"ext"},
					Config: StepConfig{SubSteps: []*Step{
						{ID: "t", Type: StepToolCall, DependsOn: []string{"ext"}, Config: StepConfig{ToolName: "tp"}},
					}}},
			}},
			substr: "not a sub-step of the same parallel block",
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

func TestLLMPlanner_ParsePlan_AcceptsValidConditionalInsideParallel(t *testing.T) {
	// A conditional sub-step whose branch targets are in-block and depend on it
	// is now valid (the runner isolates each branch — see runParallelStep).
	content := `{"steps":[{"id":"par","type":"parallel","sub_steps":[
		{"id":"c1","type":"conditional","condition":{"field":"input","operator":"contains","value":"x"},"branches":{"true":["t"],"false":["f"]}},
		{"id":"t","type":"llm_call","depends_on":["c1"],"config":{"prompt":"t"}},
		{"id":"f","type":"llm_call","depends_on":["c1"],"config":{"prompt":"f"}}
	]}]}`
	p := NewLLMPlanner(&mockChatClient{response: content}, "m")
	plan, err := p.CreatePlan(context.Background(), PlanInput{UserID: "u1", Message: "go"})
	if err != nil {
		t.Fatalf("expected valid conditional-in-parallel plan, got %v", err)
	}
	if len(plan.Steps) != 1 || len(plan.Steps[0].Config.SubSteps) != 3 {
		t.Fatalf("unexpected parsed shape: %+v", plan.Steps)
	}
}

func TestLLMPlanner_ParsePlan_RejectsConditionalBranchEscapingParallel(t *testing.T) {
	// A conditional sub-step branching to a step outside its parallel block is
	// still rejected at parse time (it could not be isolated safely).
	content := `{"steps":[{"id":"par","type":"parallel","sub_steps":[
		{"id":"c1","type":"conditional","branches":{"true":["outside"]}}
	]}]}`
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

// --- P1 (long-term): a conditional may now run INSIDE a parallel block; the
// runner executes each block as an isolated mini-plan so a conditional's
// skip-propagation stays within the block and never races siblings. ---

func hasCall(calls []string, name string) bool {
	for _, c := range calls {
		if c == name {
			return true
		}
	}
	return false
}

func TestLocalRunner_ConditionalInParallel_Runs(t *testing.T) {
	tools := &perToolExecutor{errBy: map[string]error{}}
	obs := &countingObserver{}
	lr := NewLocalRunner(&mockLLM{out: "ok"}, tools, WithLocalObserver(obs))

	cond := &Step{
		ID: "cond", Type: StepConditional, Status: StepPending,
		Condition: &Condition{Field: "input", Operator: "contains", Value: "yes"},
		Branches:  map[string][]string{"true": {"t"}, "false": {"f"}},
	}
	tStep := &Step{ID: "t", Type: StepToolCall, Status: StepPending, DependsOn: []string{"cond"}, Config: StepConfig{ToolName: "tpath"}}
	fStep := &Step{ID: "f", Type: StepToolCall, Status: StepPending, DependsOn: []string{"cond"}, Config: StepConfig{ToolName: "fpath"}}
	indep := &Step{ID: "indep", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "ipath"}}
	par := &Step{ID: "par", Type: StepParallel, Status: StepPending, Config: StepConfig{SubSteps: []*Step{cond, tStep, fStep, indep}}}

	plan := pendingPlan("p1", par)
	plan.Input = "yes please"

	if _, err := lr.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if plan.Status != PlanCompleted {
		t.Fatalf("plan status: got %s", plan.Status)
	}
	if cond.Status != StepCompleted || tStep.Status != StepCompleted || indep.Status != StepCompleted {
		t.Errorf("statuses: cond=%s t=%s indep=%s", cond.Status, tStep.Status, indep.Status)
	}
	if fStep.Status != StepSkipped {
		t.Errorf("non-selected branch should be skipped, got %s", fStep.Status)
	}
	tools.mu.Lock()
	calls := append([]string(nil), tools.calls...)
	tools.mu.Unlock()
	if hasCall(calls, "fpath") {
		t.Errorf("skipped branch tool must not execute, calls=%v", calls)
	}
	if !hasCall(calls, "tpath") || !hasCall(calls, "ipath") {
		t.Errorf("selected + independent tools should run, calls=%v", calls)
	}
}

func TestLocalRunner_ConditionalInParallel_NoDataRace(t *testing.T) {
	tools := &perToolExecutor{errBy: map[string]error{}}
	store := NewMemoryStore()
	lr := NewLocalRunner(&mockLLM{out: "ok"}, tools, WithLocalStore(store))

	// Many independent conditional groups in ONE parallel block: every
	// conditional runs concurrently in the first batch and skips ONLY its own
	// false-branch sub-step; the selected sub-steps then run concurrently in the
	// next batch. Run with -race: a skip that escaped its branch — or any shared
	// plan.Steps mutation — would trip the detector.
	var subs []*Step
	for i := 0; i < 12; i++ {
		c := fmt.Sprintf("c%d", i)
		tt := fmt.Sprintf("t%d", i)
		ff := fmt.Sprintf("f%d", i)
		subs = append(subs,
			&Step{ID: c, Type: StepConditional, Status: StepPending,
				Condition: &Condition{Field: "input", Operator: "contains", Value: "yes"},
				Branches:  map[string][]string{"true": {tt}, "false": {ff}}},
			&Step{ID: tt, Type: StepToolCall, Status: StepPending, DependsOn: []string{c}, Config: StepConfig{ToolName: tt}},
			&Step{ID: ff, Type: StepToolCall, Status: StepPending, DependsOn: []string{c}, Config: StepConfig{ToolName: ff}},
		)
	}
	par := &Step{ID: "par", Type: StepParallel, Status: StepPending, Config: StepConfig{SubSteps: subs}}
	plan := pendingPlan("p1", par)
	plan.Input = "yes"
	_ = store.Save(context.Background(), plan)

	if _, err := lr.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if plan.Status != PlanCompleted {
		t.Errorf("plan status: got %s", plan.Status)
	}
	for _, s := range subs {
		switch {
		case strings.HasPrefix(s.ID, "t"):
			if s.Status != StepCompleted {
				t.Errorf("selected %s: got %s", s.ID, s.Status)
			}
		case strings.HasPrefix(s.ID, "f"):
			if s.Status != StepSkipped {
				t.Errorf("non-selected %s: got %s", s.ID, s.Status)
			}
		}
	}
}

func TestLocalRunner_ConditionalInParallel_SkipStaysInBranch(t *testing.T) {
	tools := &perToolExecutor{errBy: map[string]error{}}
	lr := NewLocalRunner(&mockLLM{out: "ok"}, tools)

	// Group A picks the true branch, group B picks the false branch — in the same
	// parallel block. Each conditional must affect only its own targets.
	condA := &Step{ID: "condA", Type: StepConditional, Status: StepPending,
		Condition: &Condition{Field: "input", Operator: "contains", Value: "A"},
		Branches:  map[string][]string{"true": {"tA"}, "false": {"fA"}}}
	tA := &Step{ID: "tA", Type: StepToolCall, Status: StepPending, DependsOn: []string{"condA"}, Config: StepConfig{ToolName: "tA"}}
	fA := &Step{ID: "fA", Type: StepToolCall, Status: StepPending, DependsOn: []string{"condA"}, Config: StepConfig{ToolName: "fA"}}
	condB := &Step{ID: "condB", Type: StepConditional, Status: StepPending,
		Condition: &Condition{Field: "input", Operator: "contains", Value: "ZZZ"},
		Branches:  map[string][]string{"true": {"tB"}, "false": {"fB"}}}
	tB := &Step{ID: "tB", Type: StepToolCall, Status: StepPending, DependsOn: []string{"condB"}, Config: StepConfig{ToolName: "tB"}}
	fB := &Step{ID: "fB", Type: StepToolCall, Status: StepPending, DependsOn: []string{"condB"}, Config: StepConfig{ToolName: "fB"}}

	par := &Step{ID: "par", Type: StepParallel, Status: StepPending,
		Config: StepConfig{SubSteps: []*Step{condA, tA, fA, condB, tB, fB}}}
	plan := pendingPlan("p1", par)
	plan.Input = "A only" // contains "A", not "ZZZ"

	if _, err := lr.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tA.Status != StepCompleted || fA.Status != StepSkipped {
		t.Errorf("group A: tA=%s fA=%s", tA.Status, fA.Status)
	}
	if fB.Status != StepCompleted || tB.Status != StepSkipped {
		t.Errorf("group B: tB=%s fB=%s", tB.Status, fB.Status)
	}
}

// Defense in depth: even when a parallel block is run directly via RunStep
// (bypassing ValidatePlan), a sub-step whose dependency cannot resolve inside
// the isolated block must surface as an error, never be silently dropped while
// the block reports success.
func TestLocalRunner_ParallelStep_StalledSubStepErrors(t *testing.T) {
	lr := NewLocalRunner(&mockLLM{out: "ok"}, &perToolExecutor{errBy: map[string]error{}})
	// "t" depends on "ghost", which is not a sub-step of the block.
	par := &Step{ID: "par", Type: StepParallel, Status: StepPending,
		Config: StepConfig{SubSteps: []*Step{
			{ID: "t", Type: StepToolCall, Status: StepPending, DependsOn: []string{"ghost"}, Config: StepConfig{ToolName: "tp"}},
		}}}
	plan := pendingPlan("p1", par)

	_, err := lr.RunStep(context.Background(), plan, par)
	if !errors.Is(err, ErrStepFailed) {
		t.Fatalf("expected ErrStepFailed for a stalled sub-step, got %v", err)
	}
	if !strings.Contains(err.Error(), "did not run") {
		t.Errorf("error should explain the stall: %v", err)
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

// A hard step Timeout bounds the TOTAL time across retries, not each attempt —
// otherwise MaxRetries would multiply the configured timeout (the "3×" footgun).
func TestLocalRunner_RunStep_HardTimeout_BoundedAcrossRetries(t *testing.T) {
	llm := &mockLLM{out: "late", delay: 2 * time.Second} // ignores ctx
	lr := NewLocalRunner(llm, &mockTools{})
	step := &Step{
		ID: "s1", Type: StepLLMCall, Status: StepPending,
		Config: StepConfig{Prompt: "x", Timeout: 50 * time.Millisecond, MaxRetries: 2},
	}
	plan := pendingPlan("p1", step)

	start := time.Now()
	_, err := lr.RunStep(context.Background(), plan, step)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrStepTimeout) {
		t.Fatalf("expected ErrStepTimeout, got %v", err)
	}
	if elapsed >= time.Second {
		t.Errorf("timeout was not bounded across retries: took %s", elapsed)
	}
	llm.mu.Lock()
	calls := len(llm.calls)
	llm.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected a single attempt once the shared deadline fired, got %d", calls)
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

// P6/P10: the TemporalRunner reports store-sync failures via the persist-error
// handler instead of swallowing them, mirroring the LocalRunner contract.
func TestTemporalRunner_PersistErrorHandlerCalled(t *testing.T) {
	storeErr := errors.New("disk full")
	var mu sync.Mutex
	var reported []error
	adapter := &mockTemporalAdapter{
		workflowID: "wf-1",
		queries: []*Plan{
			{ID: "p1", Status: PlanCompleted, Steps: []*Step{{ID: "s1", Status: StepCompleted, Result: &StepResult{Output: "done"}}}},
		},
	}
	runner := NewTemporalRunner(adapter,
		WithTemporalStore(&failingStore{PlanStore: NewMemoryStore(), updateErr: storeErr}),
		WithTemporalPersistErrorHandler(func(_ *Plan, err error) {
			mu.Lock()
			reported = append(reported, err)
			mu.Unlock()
		}),
	)
	result, err := runner.Run(context.Background(), &Plan{ID: "p1", Status: PlanPending})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Output != "done" {
		t.Errorf("output: got %q", result.Output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) == 0 {
		t.Fatal("expected persist-error handler to be called on store sync failure")
	}
	for _, e := range reported {
		if !errors.Is(e, storeErr) {
			t.Errorf("expected store error, got %v", e)
		}
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

// Test case: conditional inside parallel, branch target with out-of-block dependency
func TestLocalRunner_ConditionalInParallel_TransitiveDep_Bug(t *testing.T) {
	tools := &perToolExecutor{errBy: map[string]error{}}
	lr := NewLocalRunner(&mockLLM{out: "ok"}, tools)

	// Plan structure:
	// par (parallel)
	//   cond (conditional)
	//   t (depends on cond and external) ← cond is in-block, but external is NOT
	// external (outside the parallel)

	cond := &Step{
		ID: "cond", Type: StepConditional, Status: StepPending,
		Condition: &Condition{Field: "input", Operator: "contains", Value: "yes"},
		Branches:  map[string][]string{"true": {"t"}},
	}
	// t depends on BOTH cond (in-block) and external (out-of-block)
	tStep := &Step{
		ID: "t", Type: StepToolCall, Status: StepPending,
		DependsOn: []string{"cond", "external"},
		Config:    StepConfig{ToolName: "tpath"},
	}
	par := &Step{ID: "par", Type: StepParallel, Status: StepPending, Config: StepConfig{SubSteps: []*Step{cond, tStep}}}
	external := &Step{ID: "external", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "epath"}}

	plan := pendingPlan("p1", par, external)
	plan.Input = "yes please"

	// ValidatePlan should catch this, but if it doesn't...
	err := ValidatePlan(plan)
	if err != nil {
		t.Logf("ValidatePlan caught the issue: %v", err)
		return
	}

	// If ValidatePlan passed, then Run should either fail or silently leave t unexecuted
	result, err := lr.Run(context.Background(), plan)
	if err != nil {
		t.Logf("Run failed (good): %v", err)
		return
	}

	// Check if t was actually executed
	if result != nil {
		t.Logf("Plan completed. Checking if t was executed...")
		for _, s := range plan.Steps {
			if s.ID == "par" && s.Type == StepParallel {
				for _, sub := range s.Config.SubSteps {
					if sub.ID == "t" {
						if sub.Status != StepCompleted {
							t.Errorf("BUG CONFIRMED: t is %s (should be Completed or error should have been returned)", sub.Status)
						} else {
							t.Logf("t was executed: %s", sub.Status)
						}
					}
				}
			}
		}
	}
}
