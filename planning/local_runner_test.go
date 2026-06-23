package planning

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ghiac/agentize/engine"
)

func TestLocalRunner_RunStep_RetriesOnFailure(t *testing.T) {
	// MaxRetries=2 → 1 initial attempt + 2 retries = 3 calls on persistent failure.
	llm := &mockLLM{err: errors.New("boom")}
	lr := NewLocalRunner(llm, &mockTools{})
	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi"}
	step := &Step{
		ID: "s1", Type: StepLLMCall, Status: StepPending,
		Config: StepConfig{Prompt: "hi", MaxRetries: 2},
	}

	_, err := lr.RunStep(context.Background(), plan, step)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if len(llm.calls) != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", len(llm.calls))
	}
}

func TestPropagateSkips_CascadesAllSkippedDeps(t *testing.T) {
	steps := []*Step{
		{ID: "a", Status: StepSkipped},
		{ID: "b", Status: StepPending, DependsOn: []string{"a"}},      // all deps skipped → skip
		{ID: "c", Status: StepPending, DependsOn: []string{"b"}},      // cascades from b
		{ID: "d", Status: StepCompleted},                              // a surviving result
		{ID: "e", Status: StepPending, DependsOn: []string{"a", "d"}}, // mixed → stays runnable
		{ID: "root", Status: StepPending},                             // no deps → never skipped
	}
	plan := &Plan{Steps: steps}

	if n := PropagateSkips(steps); n != 2 {
		t.Fatalf("expected 2 cascaded skips (b, c), got %d", n)
	}
	for _, id := range []string{"b", "c"} {
		if findStep(plan, id).Status != StepSkipped {
			t.Errorf("step %s should be skipped", id)
		}
	}
	for _, id := range []string{"e", "root"} {
		if findStep(plan, id).Status != StepPending {
			t.Errorf("step %s should stay pending", id)
		}
	}
}

func TestLocalRunner_Conditional_SkipsNonSelectedBranch(t *testing.T) {
	llm := &mockLLM{out: "LLMOUT", tokens: 1}
	lr := NewLocalRunner(llm, &mockTools{})
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "please say yes",
		Steps: []*Step{
			{ID: "s1", Type: StepConditional, Status: StepPending,
				Condition: &Condition{Field: "input", Operator: "contains", Value: "yes"},
				Branches:  map[string][]string{"true": {"s2"}, "false": {"s3"}}},
			{ID: "s2", Type: StepLLMCall, Name: "taken", Status: StepPending, DependsOn: []string{"s1"}, Config: StepConfig{Prompt: "go"}},
			{ID: "s3", Type: StepLLMCall, Name: "nottaken", Status: StepPending, DependsOn: []string{"s1"}, Config: StepConfig{Prompt: "no"}},
			{ID: "s4", Type: StepCollect, Name: "join", Status: StepPending, DependsOn: []string{"s2", "s3"}},
		},
		Status: PlanPending, Version: 1,
	}

	if _, err := lr.Run(context.Background(), plan); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if findStep(plan, "s3").Status != StepSkipped {
		t.Errorf("non-selected branch s3 should be skipped, got %s", findStep(plan, "s3").Status)
	}
	if findStep(plan, "s2").Status != StepCompleted {
		t.Errorf("selected branch s2 should be completed, got %s", findStep(plan, "s2").Status)
	}
	s4 := findStep(plan, "s4")
	if s4.Status != StepCompleted {
		t.Fatalf("collect s4 should be completed, got %s", s4.Status)
	}
	if !strings.Contains(s4.Result.Output, "LLMOUT") {
		t.Errorf("collect should include the taken branch output, got %q", s4.Result.Output)
	}
	if strings.Contains(s4.Result.Output, "nottaken") {
		t.Errorf("collect should not include the skipped branch, got %q", s4.Result.Output)
	}
}

func TestLocalRunner_Collect_AggregatesDependencies(t *testing.T) {
	lr := NewLocalRunner(&mockLLM{}, &mockTools{})
	plan := &Plan{ID: "p1", Steps: []*Step{
		{ID: "a", Name: "Alpha", Status: StepCompleted, Result: &StepResult{Output: "AAA", TokensUsed: 2}},
		{ID: "b", Name: "Beta", Status: StepCompleted, Result: &StepResult{Output: "BBB", TokensUsed: 3}},
		{ID: "c", Type: StepCollect, Status: StepPending, DependsOn: []string{"a", "b"}},
	}}

	res, err := lr.RunStep(context.Background(), plan, findStep(plan, "c"))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if !strings.Contains(res.Output, "AAA") || !strings.Contains(res.Output, "BBB") {
		t.Errorf("collect should aggregate both outputs, got %q", res.Output)
	}
	if res.TokensUsed != 5 {
		t.Errorf("collect should sum dependency tokens, got %d want 5", res.TokensUsed)
	}
}

func TestLocalRunner_LLMStep_ReceivesDependencyOutputs(t *testing.T) {
	llm := &mockLLM{out: "summary", tokens: 7}
	lr := NewLocalRunner(llm, &mockTools{})
	plan := &Plan{ID: "p1", Input: "original request", Steps: []*Step{
		{ID: "a", Name: "Namespaces", Status: StepCompleted, Result: &StepResult{Output: "ns-1\nns-2"}},
		{ID: "b", Type: StepLLMCall, Status: StepPending,
			Config: StepConfig{Prompt: "Summarize the namespaces"}, DependsOn: []string{"a"}},
	}}

	res, err := lr.RunStep(context.Background(), plan, findStep(plan, "b"))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Output != "summary" {
		t.Errorf("unexpected output %q", res.Output)
	}
	if len(llm.calls) != 1 {
		t.Fatalf("expected exactly 1 LLM call, got %d", len(llm.calls))
	}
	sent := llm.calls[0]
	if !strings.Contains(sent, "Summarize the namespaces") {
		t.Errorf("prompt should retain the step's own prompt, got %q", sent)
	}
	if !strings.Contains(sent, "ns-1") || !strings.Contains(sent, "ns-2") {
		t.Errorf("llm_call should receive its dependency's output, got %q", sent)
	}
	if !strings.Contains(sent, "Namespaces") {
		t.Errorf("dependency output should be labeled by step name, got %q", sent)
	}
}

func TestLocalRunner_HumanReview_AutoApproveByDefault(t *testing.T) {
	lr := NewLocalRunner(&mockLLM{}, &mockTools{}) // no reviewer wired
	plan := &Plan{ID: "p1", Input: "review me", Steps: []*Step{
		{ID: "s1", Type: StepHumanReview, Status: StepPending},
	}}

	res, err := lr.RunStep(context.Background(), plan, findStep(plan, "s1"))
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if res.Output != "review me" {
		t.Errorf("expected pass-through of the input, got %q", res.Output)
	}
}

func TestLocalRunner_HumanReview_RejectFailsStep(t *testing.T) {
	lr := NewLocalRunner(&mockLLM{}, &mockTools{}, WithLocalReviewer(
		func(_ context.Context, _ *Plan, _ *Step, _ string) (bool, string, error) {
			return false, "needs changes", nil
		}))
	plan := &Plan{ID: "p1", Input: "x", Steps: []*Step{
		{ID: "s1", Type: StepHumanReview, Status: StepPending},
	}}

	_, err := lr.RunStep(context.Background(), plan, findStep(plan, "s1"))
	if err == nil {
		t.Fatal("expected a rejection error")
	}
	if !strings.Contains(err.Error(), "needs changes") {
		t.Errorf("error should carry the reviewer note, got %v", err)
	}
}

func TestLocalRunner_RunStep_NoRetryByDefault(t *testing.T) {
	// Default MaxRetries=0 must keep the single-attempt behavior unchanged.
	llm := &mockLLM{err: errors.New("boom")}
	lr := NewLocalRunner(llm, &mockTools{})
	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi"}
	step := &Step{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}}

	if _, err := lr.RunStep(context.Background(), plan, step); err == nil {
		t.Fatal("expected error")
	}
	if len(llm.calls) != 1 {
		t.Fatalf("expected exactly 1 attempt with no retries, got %d", len(llm.calls))
	}
}

// mockLLM implements LLMCaller for tests.
type mockLLM struct {
	out    string
	tokens int
	err    error
	delay  time.Duration
	calls  []string
	mu     sync.Mutex
}

func (m *mockLLM) ProcessMessage(_ context.Context, _, message string) (string, int, error) {
	m.mu.Lock()
	m.calls = append(m.calls, message)
	m.mu.Unlock()
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.err != nil {
		return "", 0, m.err
	}
	return m.out, m.tokens, nil
}

// mockTools implements ToolExecutor for tests.
type mockTools struct {
	out string
	err error
}

func (m *mockTools) Execute(_ string, _ map[string]interface{}) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.out, nil
}

func TestLocalRunner_Run_Success_SingleLLMStep(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{out: "hello", tokens: 10}
	tools := &mockTools{}
	store := NewMemoryStore()
	lr := NewLocalRunner(llm, tools, WithLocalStore(store))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Output != "hello" || result.TotalTokens != 10 {
		t.Errorf("unexpected result: %+v", result)
	}
	if plan.Status != PlanCompleted {
		t.Errorf("plan status: got %s", plan.Status)
	}
	got, _ := store.Get(ctx, "p1")
	if got != nil && got.Status != PlanCompleted {
		t.Errorf("store plan status: got %s", got.Status)
	}
}

func TestLocalRunner_Run_Success_SingleToolStep(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{}
	tools := &mockTools{out: "tool-result"}
	store := NewMemoryStore()
	lr := NewLocalRunner(llm, tools, WithLocalStore(store))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "run",
		Steps: []*Step{
			{ID: "s1", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "x", ToolArgs: map[string]interface{}{}}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Output != "tool-result" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestLocalRunner_Run_DAGInvalid_Cycle(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	lr := NewLocalRunner(&mockLLM{}, &mockTools{}, WithLocalStore(store))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "x",
		Steps: []*Step{
			{ID: "a", DependsOn: []string{"c"}, Status: StepPending, Config: StepConfig{}},
			{ID: "b", DependsOn: []string{"a"}, Status: StepPending, Config: StepConfig{}},
			{ID: "c", DependsOn: []string{"b"}, Status: StepPending, Config: StepConfig{}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err == nil {
		t.Fatal("expected error for cycle")
	}
	if !errors.Is(err, ErrCyclicDependency) {
		t.Errorf("expected ErrCyclicDependency, got %v", err)
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if plan.Status != PlanFailed {
		t.Errorf("plan status: got %s", plan.Status)
	}
	got, _ := store.Get(ctx, "p1")
	if got != nil && got.Status != PlanFailed {
		t.Errorf("store plan status: got %s", got.Status)
	}
}

func TestLocalRunner_Run_StepFails(t *testing.T) {
	ctx := context.Background()
	stepErr := errors.New("llm failed")
	llm := &mockLLM{err: stepErr}
	tools := &mockTools{}
	store := NewMemoryStore()
	lr := NewLocalRunner(llm, tools, WithLocalStore(store))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, stepErr) {
		t.Errorf("expected stepErr, got %v", err)
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if plan.Status != PlanFailed {
		t.Errorf("plan status: got %s", plan.Status)
	}
}

func TestLocalRunner_Run_ContextCancelled_GracefulShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	llm := &mockLLM{out: "x", tokens: 1}
	tools := &mockTools{}
	store := NewMemoryStore()
	lr := NewLocalRunner(llm, tools, WithLocalStore(store))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err == nil {
		t.Fatal("expected context error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if plan.Status != PlanCancelled {
		t.Errorf("plan status: got %s", plan.Status)
	}
}

func TestLocalRunner_RunStep_UnknownType(t *testing.T) {
	ctx := context.Background()
	lr := NewLocalRunner(&mockLLM{}, &mockTools{})
	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "x", Status: PlanRunning, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	step := &Step{ID: "s1", Type: StepType("bogus_unhandled_type"), Status: StepPending, Config: StepConfig{}}

	result, err := lr.RunStep(ctx, plan, step)
	if err == nil {
		t.Fatal("expected error for unknown step type")
	}
	if !errors.Is(err, ErrInvalidStep) {
		t.Errorf("expected ErrInvalidStep, got %v", err)
	}
	if result != nil {
		t.Error("expected nil result")
	}
}

func TestLocalRunner_Cancel_NilStore(t *testing.T) {
	ctx := context.Background()
	lr := NewLocalRunner(&mockLLM{}, &mockTools{}) // no store
	err := lr.Cancel(ctx, "any")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNoPlanStore) {
		t.Errorf("expected ErrNoPlanStore, got %v", err)
	}
}

func TestLocalRunner_Cancel_PlanNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	lr := NewLocalRunner(&mockLLM{}, &mockTools{}, WithLocalStore(store))
	err := lr.Cancel(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("expected ErrPlanNotFound, got %v", err)
	}
}

func TestLocalRunner_Cancel_Success(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "x",
		Status: PlanRunning, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	_ = store.Save(ctx, plan)
	lr := NewLocalRunner(&mockLLM{}, &mockTools{}, WithLocalStore(store))
	err := lr.Cancel(ctx, "p1")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ := store.Get(ctx, "p1")
	if got == nil || got.Status != PlanCancelled {
		t.Errorf("expected plan cancelled in store, got %+v", got)
	}
}

func TestLocalRunner_Resume_NilStore(t *testing.T) {
	ctx := context.Background()
	lr := NewLocalRunner(&mockLLM{}, &mockTools{})
	_, err := lr.Resume(ctx, "any")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNoPlanStore) {
		t.Errorf("expected ErrNoPlanStore, got %v", err)
	}
}

func TestLocalRunner_Resume_PlanNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	lr := NewLocalRunner(&mockLLM{}, &mockTools{}, WithLocalStore(store))
	_, err := lr.Resume(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("expected ErrPlanNotFound, got %v", err)
	}
}

func TestLocalRunner_Resume_StoreRestoreAndContinue(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{out: "resumed", tokens: 5}
	tools := &mockTools{}
	store := NewMemoryStore()
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "resume me",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "go"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	_ = store.Save(ctx, plan)
	lr := NewLocalRunner(llm, tools, WithLocalStore(store))

	result, err := lr.Resume(ctx, "p1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if result == nil || result.Output != "resumed" {
		t.Errorf("unexpected result: %+v", result)
	}
	got, _ := store.Get(ctx, "p1")
	if got == nil {
		t.Error("expected plan in store after Resume")
	} else if got.Status != PlanCompleted {
		t.Errorf("expected status PlanCompleted, got %s", got.Status)
	}
}

func TestLocalRunner_Run_ObserverCalled(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{out: "ok", tokens: 1}
	tools := &mockTools{}
	obs := &mockObserver{}
	lr := NewLocalRunner(llm, tools, WithLocalObserver(obs))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	_, err := lr.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !obs.planCreated || !obs.stepStarted || !obs.stepCompleted || !obs.planCompleted {
		t.Errorf("observer not fully called: created=%v stepStarted=%v stepCompleted=%v planCompleted=%v",
			obs.planCreated, obs.stepStarted, obs.stepCompleted, obs.planCompleted)
	}
}

type mockObserver struct {
	planCreated   bool
	stepStarted   bool
	stepCompleted bool
	planCompleted bool
	planFailed    bool
	stepFailed    bool
}

func (m *mockObserver) OnPlanCreated(plan *Plan)                       { m.planCreated = true }
func (m *mockObserver) OnPlanCompleted(plan *Plan, result *PlanResult) { m.planCompleted = true }
func (m *mockObserver) OnPlanFailed(plan *Plan, err error)             { m.planFailed = true }
func (m *mockObserver) OnStepStarted(plan *Plan, step *Step)           { m.stepStarted = true }
func (m *mockObserver) OnStepCompleted(plan *Plan, step *Step, result *StepResult) {
	m.stepCompleted = true
}
func (m *mockObserver) OnStepFailed(plan *Plan, step *Step, err error) { m.stepFailed = true }

func TestLocalRunner_Run_MultiStepDAG(t *testing.T) {
	ctx := context.Background()
	// Diamond: A -> B, A -> C, B+C -> D
	llm := &mockLLM{out: "ok", tokens: 1}
	tools := &mockTools{out: "tool-ok"}
	store := NewMemoryStore()
	lr := NewLocalRunner(llm, tools, WithLocalStore(store))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "diamond",
		Steps: []*Step{
			{ID: "a", Type: StepLLMCall, Status: StepPending, DependsOn: nil, Config: StepConfig{Prompt: "a"}},
			{ID: "b", Type: StepLLMCall, Status: StepPending, DependsOn: []string{"a"}, Config: StepConfig{Prompt: "b"}},
			{ID: "c", Type: StepToolCall, Status: StepPending, DependsOn: []string{"a"}, Config: StepConfig{ToolName: "x", ToolArgs: map[string]interface{}{}}},
			{ID: "d", Type: StepLLMCall, Status: StepPending, DependsOn: []string{"b", "c"}, Config: StepConfig{Prompt: "d"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	for _, s := range plan.Steps {
		if s.Status != StepCompleted {
			t.Errorf("step %s: expected StepCompleted, got %s", s.ID, s.Status)
		}
	}
	if plan.Status != PlanCompleted {
		t.Errorf("plan status: got %s", plan.Status)
	}
	// Final output should be from last step (d)
	if result.Output != "ok" {
		t.Errorf("result.Output: got %q", result.Output)
	}
}

func TestLocalRunner_Run_ParallelStep_OneSubFails(t *testing.T) {
	ctx := context.Background()
	stepErr := errors.New("sub failed")
	llm := &mockLLM{out: "ok", tokens: 1}
	tools := &mockTools{err: stepErr}
	lr := NewLocalRunner(llm, tools)

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "parallel",
		Steps: []*Step{
			{
				ID: "par", Type: StepParallel, Status: StepPending, Config: StepConfig{
					SubSteps: []*Step{
						{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "a"}},
						{ID: "s2", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "fail", ToolArgs: map[string]interface{}{}}},
					},
				},
			},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err == nil {
		t.Fatal("expected error from parallel sub-step failure")
	}
	if !errors.Is(err, stepErr) {
		t.Errorf("expected stepErr, got %v", err)
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if plan.Status != PlanFailed {
		t.Errorf("plan status: got %s", plan.Status)
	}
}

func TestLocalRunner_Run_ParallelStep_Empty(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{}
	tools := &mockTools{}
	lr := NewLocalRunner(llm, tools)

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "empty parallel",
		Steps: []*Step{
			{ID: "par", Type: StepParallel, Status: StepPending, Config: StepConfig{SubSteps: nil}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Output != "" {
		t.Errorf("expected empty output, got %q", result.Output)
	}
	if plan.Steps[0].Status != StepCompleted {
		t.Errorf("parallel step status: got %s", plan.Steps[0].Status)
	}
}

func TestLocalRunner_Run_AgentDelegate(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{out: "agent said hello", tokens: 5}
	tools := &mockTools{}
	lr := NewLocalRunner(llm, tools)

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "user request",
		Steps: []*Step{
			{ID: "s1", Type: StepAgentDelegate, Status: StepPending, Config: StepConfig{AgentInput: "delegate this"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Output != "agent said hello" {
		t.Errorf("unexpected result: %+v", result)
	}
	// Engine should have been called with AgentInput
	if len(llm.calls) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(llm.calls))
	}
	if llm.calls[0] != "delegate this" {
		t.Errorf("LLM called with wrong input: %q", llm.calls[0])
	}
}

func TestLocalRunner_Run_Timeout(t *testing.T) {
	ctx := context.Background()
	// The step's wall-clock delay is an order of magnitude larger than the
	// timeout, so the deadline has reliably elapsed by the time the step returns
	// — the runner then cancels the plan regardless of host speed/jitter.
	slowLLM := &mockLLM{out: "slow", tokens: 1, delay: 100 * time.Millisecond}
	tools := &mockTools{}
	store := NewMemoryStore()
	lr := NewLocalRunner(slowLLM, tools, WithLocalStore(store))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan, WithTimeout(10*time.Millisecond))
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if plan.Status != PlanCancelled {
		t.Errorf("plan status: got %s", plan.Status)
	}
}

func TestLocalRunner_Run_CallbackBeforeAction_Blocks(t *testing.T) {
	ctx := context.Background()
	blockErr := errors.New("blocked by callback")
	llm := &mockLLM{out: "ok", tokens: 1}
	tools := &mockTools{}
	cb := &mockCallback{beforeErr: blockErr}
	lr := NewLocalRunner(llm, tools, WithLocalCallback(cb))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err == nil {
		t.Fatal("expected error from BeforeAction")
	}
	if !errors.Is(err, blockErr) {
		t.Errorf("expected blockErr, got %v", err)
	}
	if result != nil {
		t.Error("expected nil result")
	}
	if !cb.beforeCalled {
		t.Error("BeforeAction should have been called")
	}
}

func TestLocalRunner_Run_CallbackAfterAction_Called(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{out: "ok", tokens: 3}
	tools := &mockTools{}
	cb := &mockCallback{}
	lr := NewLocalRunner(llm, tools, WithLocalCallback(cb))

	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	_, err := lr.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !cb.afterCalled {
		t.Error("AfterAction should have been called")
	}
}

func TestLocalRunner_Run_MultipleReadySteps(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{out: "ok", tokens: 1}
	tools := &mockTools{out: "t"}
	store := NewMemoryStore()
	lr := NewLocalRunner(llm, tools, WithLocalStore(store))

	// Two independent steps (both ready at once)
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "multi",
		Steps: []*Step{
			{ID: "a", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "a"}},
			{ID: "b", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "x", ToolArgs: map[string]interface{}{}}},
		},
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	result, err := lr.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if plan.Steps[0].Status != StepCompleted || plan.Steps[1].Status != StepCompleted {
		t.Errorf("both steps should be completed: %s, %s", plan.Steps[0].Status, plan.Steps[1].Status)
	}
}

func TestLocalRunner_RunStep_AgentDelegateFallsBackToPlanInput(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{out: "done", tokens: 1}
	tools := &mockTools{}
	lr := NewLocalRunner(llm, tools)

	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "plan input here", Status: PlanRunning, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	step := &Step{ID: "s1", Type: StepAgentDelegate, Status: StepPending, Config: StepConfig{AgentInput: ""}}
	_, err := lr.RunStep(ctx, plan, step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if len(llm.calls) != 1 || llm.calls[0] != "plan input here" {
		t.Errorf("expected LLM called with plan.Input, got %v", llm.calls)
	}
}

func TestLocalRunner_RunStep_LLMStepFallsBackToPlanInput(t *testing.T) {
	ctx := context.Background()
	llm := &mockLLM{out: "done", tokens: 1}
	tools := &mockTools{}
	lr := NewLocalRunner(llm, tools)

	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "plan input here", Status: PlanRunning, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	step := &Step{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: ""}}
	_, err := lr.RunStep(ctx, plan, step)
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if len(llm.calls) != 1 || llm.calls[0] != "plan input here" {
		t.Errorf("expected LLM called with plan.Input, got %v", llm.calls)
	}
}

// mockCallback implements engine.Callback for tests.
type mockCallback struct {
	beforeErr    error
	beforeCalled bool
	afterCalled  bool
}

func (m *mockCallback) BeforeAction(ctx context.Context, ev *engine.UsageEvent) error {
	m.beforeCalled = true
	return m.beforeErr
}

func (m *mockCallback) AfterAction(ctx context.Context, ev *engine.UsageEvent) {
	m.afterCalled = true
}
