package planning

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sashabaranov/go-openai"
)

// testObserver records observer events for tests in this file.
type testObserver struct {
	planCreated   bool
	planCompleted bool
}

func (o *testObserver) OnPlanCreated(plan *Plan)                                   { o.planCreated = true }
func (o *testObserver) OnPlanCompleted(plan *Plan, result *PlanResult)             { o.planCompleted = true }
func (o *testObserver) OnPlanFailed(plan *Plan, err error)                         {}
func (o *testObserver) OnStepStarted(plan *Plan, step *Step)                       {}
func (o *testObserver) OnStepCompleted(plan *Plan, step *Step, result *StepResult) {}
func (o *testObserver) OnStepFailed(plan *Plan, step *Step, err error)             {}

func TestValidateDAG_NoCycle(t *testing.T) {
	steps := []*Step{
		{ID: "a", DependsOn: nil},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	err := ValidateDAG(steps)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateDAG_Cycle(t *testing.T) {
	steps := []*Step{
		{ID: "a", DependsOn: []string{"c"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	err := ValidateDAG(steps)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !errors.Is(err, ErrCyclicDependency) {
		t.Errorf("expected ErrCyclicDependency, got %v", err)
	}
}

func TestValidateDAG_MissingDep(t *testing.T) {
	steps := []*Step{
		{ID: "a", DependsOn: []string{"x"}},
	}
	err := ValidateDAG(steps)
	if err == nil {
		t.Fatal("expected missing dep error")
	}
	if errors.Is(err, ErrCyclicDependency) {
		t.Errorf("expected missing-dep error, not cycle: %v", err)
	}
}

func TestValidateDAG_EmptySteps(t *testing.T) {
	err := ValidateDAG(nil)
	if err != nil {
		t.Errorf("expected nil for empty steps, got %v", err)
	}
	err = ValidateDAG([]*Step{})
	if err != nil {
		t.Errorf("expected nil for empty slice, got %v", err)
	}
}

func TestValidateDAG_NilStepsInSlice(t *testing.T) {
	steps := []*Step{
		{ID: "a", DependsOn: nil},
		nil,
		{ID: "b", DependsOn: []string{"a"}},
	}
	err := ValidateDAG(steps)
	if err != nil {
		t.Fatalf("expected no error with nil in slice, got %v", err)
	}
}

func TestValidateDAG_DuplicateIDs(t *testing.T) {
	steps := []*Step{
		{ID: "x", DependsOn: nil},
		{ID: "x", DependsOn: nil},
	}
	err := ValidateDAG(steps)
	if err == nil {
		t.Fatal("expected error for duplicate step IDs")
	}
	if err != nil && !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate ID error, got %v", err)
	}
	if err != nil && !errors.Is(err, ErrDuplicateStepID) {
		t.Errorf("expected ErrDuplicateStepID, got %v", err)
	}
}

func TestValidateDAG_SelfDependency(t *testing.T) {
	steps := []*Step{
		{ID: "a", DependsOn: []string{"a"}},
	}
	err := ValidateDAG(steps)
	if err == nil {
		t.Fatal("expected cycle error for self-dependency")
	}
	if !errors.Is(err, ErrCyclicDependency) {
		t.Errorf("expected ErrCyclicDependency, got %v", err)
	}
}

func TestTopologicalSort(t *testing.T) {
	// Diamond: a -> b, a -> c, b -> d, c -> d
	steps := []*Step{
		{ID: "d", DependsOn: []string{"b", "c"}},
		{ID: "c", DependsOn: []string{"a"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "a", DependsOn: nil},
	}
	sorted, err := TopologicalSort(steps)
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	if len(sorted) != 4 {
		t.Fatalf("expected 4 steps, got %d", len(sorted))
	}
	idx := make(map[string]int)
	for i, s := range sorted {
		idx[s.ID] = i
	}
	if idx["a"] >= idx["b"] || idx["a"] >= idx["c"] {
		t.Error("a must come before b and c")
	}
	if idx["b"] >= idx["d"] || idx["c"] >= idx["d"] {
		t.Error("b and c must come before d")
	}
}

func TestTopologicalSort_WithCycle(t *testing.T) {
	steps := []*Step{
		{ID: "a", DependsOn: []string{"c"}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
	}
	_, err := TopologicalSort(steps)
	if err == nil {
		t.Fatal("expected error for cycle")
	}
	if !errors.Is(err, ErrCyclicDependency) {
		t.Errorf("expected ErrCyclicDependency, got %v", err)
	}
}

func TestTopologicalSort_MissingDep(t *testing.T) {
	steps := []*Step{
		{ID: "a", DependsOn: []string{"missing"}},
	}
	_, err := TopologicalSort(steps)
	if err == nil {
		t.Fatal("expected error for missing dependency")
	}
}

func TestTopologicalSort_Empty(t *testing.T) {
	sorted, err := TopologicalSort(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sorted != nil {
		t.Errorf("expected nil slice, got len=%d", len(sorted))
	}
}

func TestTopologicalSort_LargeGraph(t *testing.T) {
	// Linear chain of 100 steps: s0 -> s1 -> ... -> s99
	steps := make([]*Step, 100)
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("s%d", i)
		dep := []string(nil)
		if i > 0 {
			dep = []string{fmt.Sprintf("s%d", i-1)}
		}
		steps[i] = &Step{ID: id, DependsOn: dep}
	}
	sorted, err := TopologicalSort(steps)
	if err != nil {
		t.Fatalf("TopologicalSort: %v", err)
	}
	if len(sorted) != 100 {
		t.Fatalf("expected 100 steps, got %d", len(sorted))
	}
	idx := make(map[string]int)
	for i, s := range sorted {
		idx[s.ID] = i
	}
	for i := 1; i < 100; i++ {
		prev := fmt.Sprintf("s%d", i-1)
		curr := fmt.Sprintf("s%d", i)
		if idx[prev] >= idx[curr] {
			t.Errorf("dependency order violated: %s must come before %s", prev, curr)
		}
	}
}

func TestReadySteps_DepFailed(t *testing.T) {
	steps := []*Step{
		{ID: "a", Status: StepFailed, DependsOn: nil},
		{ID: "b", Status: StepPending, DependsOn: []string{"a"}},
	}
	ready := ReadySteps(steps)
	if len(ready) != 0 {
		t.Errorf("expected no ready steps when dependency failed, got %d", len(ready))
	}
}

func TestReadySteps_DepSkipped(t *testing.T) {
	steps := []*Step{
		{ID: "a", Status: StepSkipped, DependsOn: nil},
		{ID: "b", Status: StepPending, DependsOn: []string{"a"}},
	}
	ready := ReadySteps(steps)
	if len(ready) != 0 {
		t.Errorf("expected no ready steps when dependency skipped, got %d", len(ready))
	}
}

func TestReadySteps(t *testing.T) {
	steps := []*Step{
		{ID: "a", Status: StepCompleted, DependsOn: nil},
		{ID: "b", Status: StepPending, DependsOn: []string{"a"}},
		{ID: "c", Status: StepPending, DependsOn: []string{"a"}},
		{ID: "d", Status: StepPending, DependsOn: []string{"b", "c"}},
	}
	ready := ReadySteps(steps)
	if len(ready) != 2 {
		t.Errorf("expected 2 ready (b,c), got %d", len(ready))
	}
	ids := make(map[string]bool)
	for _, s := range ready {
		ids[s.ID] = true
	}
	if !ids["b"] || !ids["c"] {
		t.Error("expected b and c to be ready")
	}
}

func TestMemoryStore_SaveGetUpdateDelete(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	plan := &Plan{
		ID:        "p1",
		UserID:    "u1",
		SessionID: "s1",
		Input:     "hello",
		Steps:     []*Step{{ID: "step-1", Type: StepLLMCall, Status: StepPending}},
		Status:    PlanPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Version:   1,
	}
	if err := store.Save(ctx, plan); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "p1" || got.Input != "hello" {
		t.Errorf("Get: got %+v", got)
	}
	plan.Input = "updated"
	if err := store.Update(ctx, plan); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = store.Get(ctx, "p1")
	if got.Input != "updated" {
		t.Errorf("Update: input not updated, got %q", got.Input)
	}
	if err := store.Delete(ctx, "p1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err = store.Get(ctx, "p1")
	if !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("after Delete expected ErrPlanNotFound, got %v", err)
	}
}

func TestMemoryStore_List(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Now()
	for i, uid := range []string{"u1", "u1", "u2"} {
		plan := &Plan{
			ID:        "p-" + string(rune('0'+i)),
			UserID:    uid,
			SessionID: "s1",
			Steps:     nil,
			Status:    PlanPending,
			CreatedAt: now,
			UpdatedAt: now,
			Version:   1,
		}
		_ = store.Save(ctx, plan)
	}
	all, err := store.List(ctx, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List(): expected 3, got %d", len(all))
	}
	u1, err := store.List(ctx, "u1")
	if err != nil {
		t.Fatalf("List u1: %v", err)
	}
	if len(u1) != 2 {
		t.Errorf("List(u1): expected 2, got %d", len(u1))
	}
}

func TestMemoryStore_GetNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	_, err := store.Get(ctx, "nonexistent")
	if !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("expected ErrPlanNotFound, got %v", err)
	}
}

func TestMemoryStore_SaveDuplicateID(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	if err := store.Save(ctx, plan); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	err := store.Save(ctx, plan)
	if err == nil {
		t.Fatal("expected error when saving duplicate ID")
	}
	if err != nil && err.Error() == "" {
		t.Error("expected non-empty error message for duplicate ID")
	}
}

func TestMemoryStore_UpdateNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	plan := &Plan{ID: "nonexistent", UserID: "u1", SessionID: "s1", Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	err := store.Update(ctx, plan)
	if err == nil {
		t.Fatal("expected error when updating non-existent plan")
	}
	if !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("expected ErrPlanNotFound, got %v", err)
	}
}

func TestMemoryStore_DeleteNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	err := store.Delete(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error when deleting non-existent plan")
	}
	if !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("expected ErrPlanNotFound, got %v", err)
	}
}

func TestMemoryStore_SaveNilUpdateNil(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	if err := store.Save(ctx, nil); err != nil {
		t.Errorf("Save(nil) should return nil, got %v", err)
	}
	if err := store.Update(ctx, nil); err != nil {
		t.Errorf("Update(nil) should return nil, got %v", err)
	}
}

func TestMemoryStore_AfterShutdown(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	_ = store.Save(ctx, plan)
	if err := store.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// After shutdown: Save, Update, Delete should return "store is closed"
	if err := store.Save(ctx, &Plan{ID: "p2", UserID: "u1", SessionID: "s1", Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}); err == nil {
		t.Error("Save after Shutdown should fail")
	}
	if err := store.Update(ctx, plan); err == nil {
		t.Error("Update after Shutdown should fail")
	}
	if err := store.Delete(ctx, "p1"); err == nil {
		t.Error("Delete after Shutdown should fail")
	}
	// Get on existing plan might still return copy before close; Get on non-existent returns ErrPlanNotFound. List returns nil,nil when closed.
	list, err := store.List(ctx, "")
	if err != nil {
		t.Errorf("List after Shutdown: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List after Shutdown expected empty, got %d", len(list))
	}
}

func TestChainPlanner_CreatePlan(t *testing.T) {
	ctx := context.Background()
	cp := NewChainPlanner()
	input := PlanInput{
		UserID:    "u1",
		SessionID: "s1",
		Message:   "hello",
	}
	plan, err := cp.CreatePlan(ctx, input)
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if plan == nil || len(plan.Steps) < 1 {
		t.Fatal("expected at least one step")
	}
	if plan.Status != PlanPending {
		t.Errorf("expected PlanPending, got %s", plan.Status)
	}
}

func TestOrchestrator_Execute(t *testing.T) {
	ctx := context.Background()
	fixedPlan := &Plan{
		ID:        "fixed-1",
		UserID:    "u1",
		SessionID: "s1",
		Input:     "test",
		Steps: []*Step{
			{ID: "step-1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "hi"}},
		},
		Status:    PlanPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Version:   1,
	}
	mockPlanner := &mockPlanner{plan: fixedPlan}
	mockRunner := &mockRunner{result: &PlanResult{PlanID: "fixed-1", Output: "ok", TotalTokens: 10}}
	store := NewMemoryStore()
	orch := NewOrchestrator(mockPlanner, mockRunner, WithOrchestratorStore(store))
	input := PlanInput{UserID: "u1", SessionID: "s1", Message: "test"}
	result, err := orch.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || result.Output != "ok" {
		t.Errorf("unexpected result: %+v", result)
	}
	if !mockPlanner.called {
		t.Error("planner CreatePlan was not called")
	}
	if !mockRunner.runCalled {
		t.Error("runner Run was not called")
	}
	got, _ := store.Get(ctx, fixedPlan.ID)
	if got == nil {
		t.Error("plan was not saved to store")
	}
}

func TestOrchestrator_Execute_PlannerError(t *testing.T) {
	ctx := context.Background()
	plannerErr := errors.New("planner failed")
	mockPlanner := &mockPlannerErr{err: plannerErr}
	mockRunner := &mockRunner{result: &PlanResult{Output: "ok"}}
	store := NewMemoryStore()
	orch := NewOrchestrator(mockPlanner, mockRunner, WithOrchestratorStore(store))
	_, err := orch.Execute(ctx, PlanInput{UserID: "u1", SessionID: "s1", Message: "test"})
	if err == nil {
		t.Fatal("expected error from planner")
	}
	if !errors.Is(err, plannerErr) {
		t.Errorf("expected error to wrap plannerErr, got %v", err)
	}
}

func TestOrchestrator_Execute_RunnerError(t *testing.T) {
	ctx := context.Background()
	runnerErr := errors.New("runner failed")
	fixedPlan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "test",
		Steps:     []*Step{{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{}}},
		Status:    PlanPending,
		CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	mockPlanner := &mockPlanner{plan: fixedPlan}
	mockRunner := &mockRunnerErr{err: runnerErr}
	store := NewMemoryStore()
	orch := NewOrchestrator(mockPlanner, mockRunner, WithOrchestratorStore(store))
	_, err := orch.Execute(ctx, PlanInput{UserID: "u1", SessionID: "s1", Message: "test"})
	if err == nil {
		t.Fatal("expected error from runner")
	}
	// Result is wrapped in Execute as create+save succeeded; error comes from runner.Run
	t.Logf("runner error (wrapped): %v", err)
}

func TestOrchestrator_Execute_SaveFailsDuplicate(t *testing.T) {
	ctx := context.Background()
	fixedPlan := &Plan{
		ID: "plan_u1_1", UserID: "u1", SessionID: "s1", Input: "test",
		Steps:     []*Step{{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{}}},
		Status:    PlanPending,
		CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	mockPlanner := &mockPlanner{plan: fixedPlan}
	mockRunner := &mockRunner{result: &PlanResult{Output: "ok"}}
	store := NewMemoryStore()
	// Pre-populate with a plan for a different user whose ID collides with
	// the sequential ID that will be generated for u1 (plan_u1_1).
	collider := &Plan{ID: "plan_u1_1", UserID: "u2", SessionID: "s2", Status: PlanPending}
	_ = store.Save(ctx, collider)
	orch := NewOrchestrator(mockPlanner, mockRunner, WithOrchestratorStore(store))
	_, err := orch.Execute(ctx, PlanInput{UserID: "u1", SessionID: "s1", Message: "test"})
	if err == nil {
		t.Fatal("expected error when store.Save returns duplicate")
	}
}

type mockPlannerErr struct {
	err error
}

func (m *mockPlannerErr) CreatePlan(ctx context.Context, input PlanInput) (*Plan, error) {
	return nil, m.err
}

func (m *mockPlannerErr) Replan(ctx context.Context, plan *Plan, lastStep *Step) (*Plan, error) {
	return plan, nil
}

type mockRunnerErr struct {
	err error
}

func (m *mockRunnerErr) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	return nil, m.err
}

func (m *mockRunnerErr) RunStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	return nil, nil
}

func (m *mockRunnerErr) Cancel(ctx context.Context, planID string) error {
	return nil
}

func (m *mockRunnerErr) Resume(ctx context.Context, planID string) (*PlanResult, error) {
	return nil, nil
}

type mockPlanner struct {
	plan   *Plan
	called bool
}

func (m *mockPlanner) CreatePlan(ctx context.Context, input PlanInput) (*Plan, error) {
	m.called = true
	return m.plan, nil
}

func (m *mockPlanner) Replan(ctx context.Context, plan *Plan, lastStep *Step) (*Plan, error) {
	return plan, nil
}

type mockRunner struct {
	result    *PlanResult
	runCalled bool
}

func (m *mockRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	m.runCalled = true
	return m.result, nil
}

func (m *mockRunner) RunStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	return nil, nil
}

func (m *mockRunner) Cancel(ctx context.Context, planID string) error {
	return nil
}

func (m *mockRunner) Resume(ctx context.Context, planID string) (*PlanResult, error) {
	return nil, nil
}

// --- Orchestrator and Observer tests ---

func TestOrchestrator_WithInputObserver(t *testing.T) {
	ctx := context.Background()
	// Use LocalRunner so observers are actually invoked during Run.
	llm := &mockLLMForTest{out: "ok", tokens: 1}
	tools := &mockToolsForTest{}
	store := NewMemoryStore()
	localRunner := NewLocalRunner(llm, tools, WithLocalStore(store))
	fixedPlan := &Plan{
		ID: "obs-1", UserID: "u1", SessionID: "s1", Input: "test",
		Steps:     []*Step{{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "test"}}},
		Status:    PlanPending,
		CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	mockPlanner := &mockPlanner{plan: fixedPlan}
	orchObs := &testObserver{}
	inputObs := &testObserver{}
	orch := NewOrchestrator(mockPlanner, localRunner, WithOrchestratorStore(store), WithOrchestratorObserver(orchObs))
	input := PlanInput{UserID: "u1", SessionID: "s1", Message: "test", Observer: inputObs}
	_, err := orch.Execute(ctx, input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !orchObs.planCreated || !orchObs.planCompleted {
		t.Errorf("orchestrator observer: planCreated=%v planCompleted=%v", orchObs.planCreated, orchObs.planCompleted)
	}
	if !inputObs.planCreated || !inputObs.planCompleted {
		t.Errorf("input observer: planCreated=%v planCompleted=%v", inputObs.planCreated, inputObs.planCompleted)
	}
}

type mockLLMForTest struct {
	out    string
	tokens int
}

func (m *mockLLMForTest) ProcessMessage(_ context.Context, _, _ string) (string, int, error) {
	return m.out, m.tokens, nil
}

type mockToolsForTest struct{}

func (m *mockToolsForTest) Execute(_ string, _ map[string]interface{}) (string, error) {
	return "", nil
}

func TestOrchestrator_Cancel(t *testing.T) {
	ctx := context.Background()
	cancelRunner := &mockRunnerWithCancel{}
	store := NewMemoryStore()
	plan := &Plan{ID: "cancel-me", UserID: "u1", SessionID: "s1", Input: "x", Status: PlanRunning, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	_ = store.Save(ctx, plan)
	orch := NewOrchestrator(&mockPlanner{plan: plan}, cancelRunner, WithOrchestratorStore(store))
	err := orch.Cancel(ctx, "cancel-me")
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !cancelRunner.cancelCalled || cancelRunner.cancelPlanID != "cancel-me" {
		t.Errorf("runner Cancel not called or wrong planID: called=%v planID=%q", cancelRunner.cancelCalled, cancelRunner.cancelPlanID)
	}
}

type mockRunnerWithCancel struct {
	mockRunner
	cancelCalled bool
	cancelPlanID string
}

func (m *mockRunnerWithCancel) Cancel(ctx context.Context, planID string) error {
	m.cancelCalled = true
	m.cancelPlanID = planID
	return nil
}

func TestMultiObserver_AllCalled(t *testing.T) {
	obs1 := &testObserver{}
	obs2 := &testObserver{}
	multi := MultiObserver(obs1, obs2)
	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi", Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	multi.OnPlanCreated(plan)
	multi.OnPlanCompleted(plan, &PlanResult{Output: "ok"})
	if !obs1.planCreated || !obs1.planCompleted {
		t.Errorf("obs1: planCreated=%v planCompleted=%v", obs1.planCreated, obs1.planCompleted)
	}
	if !obs2.planCreated || !obs2.planCompleted {
		t.Errorf("obs2: planCreated=%v planCompleted=%v", obs2.planCreated, obs2.planCompleted)
	}
}

func TestMiddleware_WithLogging(t *testing.T) {
	ctx := context.Background()
	inner := &mockRunner{result: &PlanResult{Output: "ok"}}
	wrapped := WithLogging(inner, nil)
	result, err := wrapped.Run(ctx, &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "x", Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Output != "ok" {
		t.Errorf("unexpected result: %+v", result)
	}
	if !inner.runCalled {
		t.Error("inner runner Run was not called")
	}
}

func TestMiddleware_WithRetry(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	failTwiceRunner := &retryableMockRunner{successAfter: 2, attempts: &attempts}
	wrapped := WithRetry(failTwiceRunner, 3)
	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "x", Steps: nil, Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	result, err := wrapped.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result == nil || result.Output != "ok" {
		t.Errorf("unexpected result: %+v", result)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

type retryableMockRunner struct {
	successAfter int
	attempts     *int
}

func (r *retryableMockRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	*r.attempts++
	if *r.attempts > r.successAfter {
		return &PlanResult{Output: "ok"}, nil
	}
	return nil, errors.New("transient error")
}

func (r *retryableMockRunner) RunStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	return nil, nil
}

func (r *retryableMockRunner) Cancel(ctx context.Context, planID string) error {
	return nil
}

func (r *retryableMockRunner) Resume(ctx context.Context, planID string) (*PlanResult, error) {
	return nil, nil
}

func TestMiddleware_WithMetrics(t *testing.T) {
	ctx := context.Background()
	inner := &mockRunner{result: &PlanResult{PlanID: "p1", Output: "ok"}}
	collector := &mockMetricsCollector{}
	wrapped := WithMetrics(inner, collector)
	plan := &Plan{ID: "p1", UserID: "u1", SessionID: "s1", Input: "x", Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1}
	_, err := wrapped.Run(ctx, plan)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if collector.planDurationPlanID != "p1" {
		t.Errorf("RecordPlanDuration planID: got %q", collector.planDurationPlanID)
	}
	if collector.planDuration < 0 {
		t.Errorf("RecordPlanDuration duration should be >= 0, got %v", collector.planDuration)
	}
	if collector.planCountStatus != PlanCompleted {
		t.Errorf("IncrementPlanCount: got status %s", collector.planCountStatus)
	}
}

type mockMetricsCollector struct {
	planDuration       time.Duration
	planDurationPlanID string
	planCountStatus    PlanStatus
}

func (m *mockMetricsCollector) RecordPlanDuration(planID string, d time.Duration) {
	m.planDurationPlanID = planID
	m.planDuration = d
}

func (m *mockMetricsCollector) RecordStepDuration(planID, stepID string, d time.Duration) {}

func (m *mockMetricsCollector) IncrementPlanCount(status PlanStatus) {
	m.planCountStatus = status
}

// --- Seed / EnsureSeedPlans error and edge cases ---

func TestEnsureSeedPlans_NilStore(t *testing.T) {
	ctx := context.Background()
	err := EnsureSeedPlans(ctx, nil, DefaultSeedConfig())
	if err != nil {
		t.Errorf("nil store should return nil error, got %v", err)
	}
}

func TestEnsureSeedPlans_SeedDisabled(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	cfg := SeedConfig{SeedInitialPlans: false}
	err := EnsureSeedPlans(ctx, store, cfg)
	if err != nil {
		t.Fatalf("EnsureSeedPlans: %v", err)
	}
	all, _ := store.List(ctx, "")
	if len(all) != 0 {
		t.Errorf("with SeedInitialPlans false expected no plans, got %d", len(all))
	}
}

func TestEnsureSeedPlans_StoreAlreadyHasPlans(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	existing := &Plan{
		ID: "existing-1", UserID: "u1", SessionID: "s1", Input: "x",
		Status: PlanPending, CreatedAt: time.Now(), UpdatedAt: time.Now(), Version: 1,
	}
	if err := store.Save(ctx, existing); err != nil {
		t.Fatalf("setup Save: %v", err)
	}
	err := EnsureSeedPlans(ctx, store, DefaultSeedConfig())
	if err != nil {
		t.Fatalf("EnsureSeedPlans: %v", err)
	}
	all, _ := store.List(ctx, "")
	if len(all) != 1 {
		t.Errorf("should not add seed when store has plans, expected 1 plan, got %d", len(all))
	}
}

func TestEnsureSeedPlans_AddsTemplatesWhenEmpty(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	err := EnsureSeedPlans(ctx, store, DefaultSeedConfig())
	if err != nil {
		t.Fatalf("EnsureSeedPlans: %v", err)
	}
	all, _ := store.List(ctx, "")
	if len(all) == 0 {
		t.Error("expected seed plans to be added when store was empty")
	}
	hasSeedID := false
	for _, p := range all {
		if len(p.ID) >= 5 && p.ID[:5] == "seed-" {
			hasSeedID = true
			break
		}
	}
	if !hasSeedID {
		t.Error("expected at least one plan with id prefix seed-")
	}
}

func TestChainPlanner_CreatePlan_WithTools(t *testing.T) {
	cp := NewChainPlanner()
	input := PlanInput{
		UserID:  "u1",
		Message: "do something",
		Context: &PlanContext{
			AvailableTools: []ToolInfo{{Name: "search", Description: "web search"}},
		},
	}
	plan, err := cp.CreatePlan(context.Background(), input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(plan.Steps))
	}
	if plan.Steps[0].Type != StepLLMCall {
		t.Errorf("expected first step LLM, got %s", plan.Steps[0].Type)
	}
	if plan.Steps[1].Type != StepToolCall {
		t.Errorf("expected second step ToolCall, got %s", plan.Steps[1].Type)
	}
	if plan.Steps[1].DependsOn[0] != plan.Steps[0].ID {
		t.Errorf("expected step-2 to depend on step-1")
	}
}

func TestChainPlanner_Replan_ReturnsUnchanged(t *testing.T) {
	cp := NewChainPlanner()
	plan := &Plan{ID: "p1", Input: "test"}
	step := &Step{ID: "s1"}
	result, err := cp.Replan(context.Background(), plan, step)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != plan {
		t.Error("expected Replan to return the same plan pointer")
	}
}

// --- MemoryStore Persister/Shutdown tests ---

type mockPersister struct {
	plans   []*Plan
	loadErr error
	saved   []*Plan
}

func (m *mockPersister) LoadPlans(_ context.Context) ([]*Plan, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return m.plans, nil
}

func (m *mockPersister) SavePlans(_ context.Context, plans []*Plan) error {
	m.saved = plans
	return nil
}

func TestMemoryStore_LoadFromPersister_Success(t *testing.T) {
	mp := &mockPersister{
		plans: []*Plan{
			{ID: "p1", Input: "plan1"},
			{ID: "p2", Input: "plan2"},
		},
	}
	store := NewMemoryStore(WithPersister(mp))
	if err := store.LoadFromPersister(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	p1, err := store.Get(context.Background(), "p1")
	if err != nil {
		t.Fatalf("expected p1 to exist: %v", err)
	}
	if p1.Input != "plan1" {
		t.Errorf("expected input plan1, got %s", p1.Input)
	}
	p2, err := store.Get(context.Background(), "p2")
	if err != nil {
		t.Fatalf("expected p2 to exist: %v", err)
	}
	if p2.Input != "plan2" {
		t.Errorf("expected input plan2, got %s", p2.Input)
	}
}

func TestMemoryStore_LoadFromPersister_Error(t *testing.T) {
	mp := &mockPersister{loadErr: fmt.Errorf("db down")}
	store := NewMemoryStore(WithPersister(mp))
	err := store.LoadFromPersister(context.Background())
	if err == nil {
		t.Fatal("expected error from persister")
	}
	if !strings.Contains(err.Error(), "db down") {
		t.Errorf("expected 'db down' in error, got %v", err)
	}
}

func TestMemoryStore_Shutdown_WithPersister(t *testing.T) {
	mp := &mockPersister{}
	store := NewMemoryStore(WithPersister(mp))
	ctx := context.Background()
	_ = store.Save(ctx, &Plan{ID: "p1", Input: "a"})
	_ = store.Save(ctx, &Plan{ID: "p2", Input: "b"})

	if err := store.Shutdown(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mp.saved) != 2 {
		t.Fatalf("expected 2 plans saved, got %d", len(mp.saved))
	}
}

func TestMemoryStore_Shutdown_Idempotent(t *testing.T) {
	mp := &mockPersister{}
	store := NewMemoryStore(WithPersister(mp))
	ctx := context.Background()
	_ = store.Save(ctx, &Plan{ID: "p1", Input: "a"})

	if err := store.Shutdown(ctx); err != nil {
		t.Fatalf("first shutdown error: %v", err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatalf("second shutdown error: %v", err)
	}
}

// --- Middleware edge-case tests ---

type mockRunnerCallCount struct {
	calls int
	err   error
}

func (m *mockRunnerCallCount) Run(_ context.Context, _ *Plan, _ ...RunOption) (*PlanResult, error) {
	m.calls++
	return nil, m.err
}
func (m *mockRunnerCallCount) RunStep(_ context.Context, _ *Plan, _ *Step) (*StepResult, error) {
	return nil, nil
}
func (m *mockRunnerCallCount) Cancel(_ context.Context, _ string) error { return nil }
func (m *mockRunnerCallCount) Resume(_ context.Context, _ string) (*PlanResult, error) {
	return nil, nil
}

func TestMiddleware_WithRetry_AllFail(t *testing.T) {
	inner := &mockRunnerCallCount{err: fmt.Errorf("always fails")}
	wrapped := WithRetry(inner, 2)
	_, err := wrapped.Run(context.Background(), &Plan{ID: "p1"})
	if err == nil {
		t.Fatal("expected error when all retries fail")
	}
	// initial + 2 retries = 3 calls
	if inner.calls != 3 {
		t.Errorf("expected 3 calls, got %d", inner.calls)
	}
}

func TestMiddleware_WithMetrics_Failure(t *testing.T) {
	inner := &mockRunnerCallCount{err: fmt.Errorf("fail")}
	collector := &mockMetricsCollector{}
	wrapped := WithMetrics(inner, collector)
	_, _ = wrapped.Run(context.Background(), &Plan{ID: "p1"})
	if collector.planCountStatus != PlanFailed {
		t.Errorf("expected PlanFailed, got %s", collector.planCountStatus)
	}
}

type mockDirectLLMClient struct {
	resp   string
	onCall func()
}

func (m *mockDirectLLMClient) CreateChatCompletion(_ context.Context, req openai.ChatCompletionRequest) (openai.ChatCompletionResponse, error) {
	if m.onCall != nil {
		m.onCall()
	}
	return openai.ChatCompletionResponse{
		Choices: []openai.ChatCompletionChoice{{Message: openai.ChatCompletionMessage{Content: m.resp}}},
		Usage:   openai.Usage{TotalTokens: 5},
	}, nil
}

func TestOrchestrator_Execute_PlanIDSequential(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	planner := &mockPlanner{plan: &Plan{
		ID: "tmp", UserID: "alice", SessionID: "s1", Input: "hello",
		Steps:  []*Step{{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{}}},
		Status: PlanPending, Version: 1,
	}}
	runner := &mockRunner{result: &PlanResult{Output: "ok"}}
	orch := NewOrchestrator(planner, runner, WithOrchestratorStore(store))

	_, err := orch.Execute(ctx, PlanInput{UserID: "alice", SessionID: "s1", Message: "first"})
	if err != nil {
		t.Fatalf("first execute: %v", err)
	}
	plans, _ := store.List(ctx, "alice")
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if plans[0].ID != "plan_alice_1" {
		t.Errorf("first plan ID = %q, want plan_alice_1", plans[0].ID)
	}

	planner.plan = &Plan{
		ID: "tmp2", UserID: "alice", SessionID: "s2", Input: "world",
		Steps:  []*Step{{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{}}},
		Status: PlanPending, Version: 1,
	}
	_, err = orch.Execute(ctx, PlanInput{UserID: "alice", SessionID: "s2", Message: "second"})
	if err != nil {
		t.Fatalf("second execute: %v", err)
	}
	plans, _ = store.List(ctx, "alice")
	if len(plans) != 2 {
		t.Fatalf("expected 2 plans, got %d", len(plans))
	}
	ids := map[string]bool{}
	for _, p := range plans {
		ids[p.ID] = true
	}
	if !ids["plan_alice_1"] || !ids["plan_alice_2"] {
		t.Errorf("expected plan_alice_1 and plan_alice_2, got %v", ids)
	}
}

func TestLocalRunner_WithLocalLLMClient(t *testing.T) {
	called := false
	client := &mockDirectLLMClient{
		resp: "direct-response",
		onCall: func() {
			called = true
		},
	}
	lr := NewLocalRunner(nil, nil, WithLocalLLMClient(client, "test-model"))
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hello",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "test prompt"}},
		},
		Status: PlanRunning, Version: 1,
	}
	result, err := lr.RunStep(context.Background(), plan, plan.Steps[0])
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if !called {
		t.Error("expected direct LLM client to be called")
	}
	if result.Output != "direct-response" {
		t.Errorf("output = %q, want direct-response", result.Output)
	}
}

func TestLocalRunner_WithLocalLLMClient_AgentDelegate(t *testing.T) {
	client := &mockDirectLLMClient{resp: "delegated"}
	lr := NewLocalRunner(nil, nil, WithLocalLLMClient(client, "m1"))
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepAgentDelegate, Status: StepPending, Config: StepConfig{AgentInput: "do it"}},
		},
		Status: PlanRunning, Version: 1,
	}
	result, err := lr.RunStep(context.Background(), plan, plan.Steps[0])
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if result.Output != "delegated" {
		t.Errorf("output = %q, want delegated", result.Output)
	}
}

func TestLocalRunner_NoLLMCaller_ReturnsError(t *testing.T) {
	lr := NewLocalRunner(nil, nil)
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "test"}},
		},
		Status: PlanRunning, Version: 1,
	}
	_, err := lr.RunStep(context.Background(), plan, plan.Steps[0])
	if err == nil {
		t.Fatal("expected error when no LLM caller configured")
	}
	if !strings.Contains(err.Error(), "no LLM caller configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLocalRunner_RunWithRunLLMClient(t *testing.T) {
	client := &mockDirectLLMClient{resp: "via-run-option"}
	lr := NewLocalRunner(nil, nil)
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "hello",
		Steps: []*Step{
			{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "test"}},
		},
		Status: PlanPending, Version: 1,
	}
	result, err := lr.Run(context.Background(), plan, WithRunLLMClient(client, "m1"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Output != "via-run-option" {
		t.Errorf("output = %q, want via-run-option", result.Output)
	}
}

func TestOrchestrator_Execute_WithLLMClient(t *testing.T) {
	client := &mockDirectLLMClient{resp: "orchestrator-llm"}
	planner := &mockPlanner{plan: &Plan{
		ID: "tmp", UserID: "bob", SessionID: "s1", Input: "hi",
		Steps:  []*Step{{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "run"}}},
		Status: PlanPending, Version: 1,
	}}
	runner := NewLocalRunner(nil, nil)
	store := NewMemoryStore()
	orch := NewOrchestrator(planner, runner,
		WithOrchestratorStore(store),
		WithOrchestratorLLMClient(client, "test-model"),
	)
	result, err := orch.Execute(context.Background(), PlanInput{UserID: "bob", SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "orchestrator-llm" {
		t.Errorf("output = %q, want orchestrator-llm", result.Output)
	}
}

func TestOrchestrator_SetLLMClient(t *testing.T) {
	client := &mockDirectLLMClient{resp: "set-later"}
	planner := &mockPlanner{plan: &Plan{
		ID: "tmp", UserID: "u1", SessionID: "s1", Input: "hi",
		Steps:  []*Step{{ID: "s1", Type: StepLLMCall, Status: StepPending, Config: StepConfig{Prompt: "go"}}},
		Status: PlanPending, Version: 1,
	}}
	runner := NewLocalRunner(nil, nil)
	store := NewMemoryStore()
	orch := NewOrchestrator(planner, runner, WithOrchestratorStore(store))

	orch.SetLLMClient(client, "m1")

	result, err := orch.Execute(context.Background(), PlanInput{UserID: "u1", SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "set-later" {
		t.Errorf("output = %q, want set-later", result.Output)
	}
}

// mockToolExecutorForTest records calls and returns a fixed response.
type mockToolExecutorForTest struct {
	lastToolName string
	lastArgs     map[string]interface{}
	response     string
	err          error
}

func (m *mockToolExecutorForTest) Execute(toolName string, args map[string]interface{}) (string, error) {
	m.lastToolName = toolName
	m.lastArgs = args
	return m.response, m.err
}

func TestLocalRunner_WithRunToolExecutor(t *testing.T) {
	mockExec := &mockToolExecutorForTest{response: "tool-result"}
	client := &mockDirectLLMClient{resp: "llm-result"}
	lr := NewLocalRunner(nil, nil, WithLocalLLMClient(client, "m1"))
	plan := &Plan{
		ID: "p1", UserID: "u1", SessionID: "s1", Input: "test",
		Steps: []*Step{
			{ID: "s1", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "web_search", ToolArgs: map[string]any{"query": "gold"}}},
		},
		Status: PlanPending, Version: 1,
	}
	result, err := lr.Run(context.Background(), plan, WithRunToolExecutor(mockExec))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mockExec.lastToolName != "web_search" {
		t.Errorf("tool executor tool name = %q, want web_search", mockExec.lastToolName)
	}
	if q, _ := mockExec.lastArgs["query"].(string); q != "gold" {
		t.Errorf("tool executor query arg = %q, want gold", q)
	}
	if result.Output != "tool-result" {
		t.Errorf("output = %q, want tool-result", result.Output)
	}
}

func TestOrchestrator_WithToolExecutorOption(t *testing.T) {
	mockExec := &mockToolExecutorForTest{response: "from-orch-executor"}
	client := &mockDirectLLMClient{resp: "llm"}
	planner := &mockPlanner{plan: &Plan{
		ID: "tmp", UserID: "u1", SessionID: "s1", Input: "test",
		Steps: []*Step{
			{ID: "s1", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "sleep", ToolArgs: map[string]any{"seconds": 1.0}}},
		},
		Status: PlanPending, Version: 1,
	}}
	runner := NewLocalRunner(nil, nil, WithLocalLLMClient(client, "m1"))
	store := NewMemoryStore()
	orch := NewOrchestrator(planner, runner,
		WithOrchestratorStore(store),
		WithOrchestratorToolExecutor(mockExec),
	)
	result, err := orch.Execute(context.Background(), PlanInput{UserID: "u1", SessionID: "s1", Message: "test"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if mockExec.lastToolName != "sleep" {
		t.Errorf("tool name = %q, want sleep", mockExec.lastToolName)
	}
	if result.Output != "from-orch-executor" {
		t.Errorf("output = %q, want from-orch-executor", result.Output)
	}
}

func TestOrchestrator_SetToolExecutor(t *testing.T) {
	mockExec := &mockToolExecutorForTest{response: "set-later-tool"}
	client := &mockDirectLLMClient{resp: "llm"}
	planner := &mockPlanner{plan: &Plan{
		ID: "tmp", UserID: "u1", SessionID: "s1", Input: "test",
		Steps: []*Step{
			{ID: "s1", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "my_tool", ToolArgs: map[string]any{}}},
		},
		Status: PlanPending, Version: 1,
	}}
	runner := NewLocalRunner(nil, nil, WithLocalLLMClient(client, "m1"))
	store := NewMemoryStore()
	orch := NewOrchestrator(planner, runner, WithOrchestratorStore(store))
	orch.SetToolExecutor(mockExec)

	result, err := orch.Execute(context.Background(), PlanInput{UserID: "u1", SessionID: "s1", Message: "test"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Output != "set-later-tool" {
		t.Errorf("output = %q, want set-later-tool", result.Output)
	}
}

func TestOrchestrator_PlanContextToolExecutor_OverridesOrchestrator(t *testing.T) {
	orchExec := &mockToolExecutorForTest{response: "orch-level"}
	contextExec := &mockToolExecutorForTest{response: "context-level"}
	client := &mockDirectLLMClient{resp: "llm"}
	planner := &mockPlanner{plan: &Plan{
		ID: "tmp", UserID: "u1", SessionID: "s1", Input: "test",
		Steps: []*Step{
			{ID: "s1", Type: StepToolCall, Status: StepPending, Config: StepConfig{ToolName: "web_search", ToolArgs: map[string]any{"query": "test"}}},
		},
		Status: PlanPending, Version: 1,
	}}
	runner := NewLocalRunner(nil, nil, WithLocalLLMClient(client, "m1"))
	store := NewMemoryStore()
	orch := NewOrchestrator(planner, runner,
		WithOrchestratorStore(store),
		WithOrchestratorToolExecutor(orchExec),
	)
	result, err := orch.Execute(context.Background(), PlanInput{
		UserID:    "u1",
		SessionID: "s1",
		Message:   "test",
		Context: &PlanContext{
			ToolExecutor: contextExec,
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// PlanContext.ToolExecutor should take priority over orchestrator-level.
	if contextExec.lastToolName != "web_search" {
		t.Errorf("context executor not called, tool = %q", contextExec.lastToolName)
	}
	if orchExec.lastToolName != "" {
		t.Errorf("orchestrator executor should not be called, but got tool = %q", orchExec.lastToolName)
	}
	if result.Output != "context-level" {
		t.Errorf("output = %q, want context-level", result.Output)
	}
}
