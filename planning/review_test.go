package planning

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/review"
)

// memReviewStore is an in-memory review.Store so the planning suspend/resume
// tests exercise the real review.Manager without importing the store package.
type memReviewStore struct {
	mu sync.Mutex
	m  map[string]*model.ReviewRequest
}

func newMemReviewStore() *memReviewStore {
	return &memReviewStore{m: map[string]*model.ReviewRequest{}}
}

func (s *memReviewStore) PutReviewRequest(r *model.ReviewRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	s.m[r.ID] = &cp
	return nil
}

func (s *memReviewStore) GetReviewRequest(id string) (*model.ReviewRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (s *memReviewStore) ListPendingReviews(userID string) ([]*model.ReviewRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*model.ReviewRequest
	for _, r := range s.m {
		if r.Status == model.ReviewPending && (userID == "" || r.UserID == userID) {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out, nil
}

// reviewPlan builds a plan with a human_review step gating a dependent llm step.
func reviewPlan(id string) (*Plan, *Step, *Step) {
	reviewStep := &Step{ID: "review", Type: StepHumanReview, Status: StepPending, Name: "Approve deploy"}
	after := &Step{ID: "after", Type: StepLLMCall, Status: StepPending, DependsOn: []string{"review"}, Config: StepConfig{Prompt: "go"}}
	return pendingPlan(id, reviewStep, after), reviewStep, after
}

// *review.Manager must satisfy the narrow planning interfaces.
var (
	_ ReviewManager     = (*review.Manager)(nil)
	_ SyncReviewManager = (*review.Manager)(nil)
)

func TestLocalRunner_HumanReview_SuspendsThenResumesOnApprove(t *testing.T) {
	ps := NewMemoryStore()
	rm := review.New(newMemReviewStore(), nil)
	llm := &mockLLM{out: "deployed"}
	lr := NewLocalRunner(llm, &mockTools{}, WithLocalStore(ps), WithLocalReviewManager(rm))

	plan, reviewStep, afterStep := reviewPlan("p1")
	_ = ps.Save(context.Background(), plan)

	// Run suspends (no parked goroutine): plan waiting, review step waiting,
	// dependent step still pending and NOT executed.
	if _, err := lr.Run(context.Background(), plan, WithStore(ps)); !errors.Is(err, ErrPlanWaiting) {
		t.Fatalf("expected ErrPlanWaiting, got %v", err)
	}
	if plan.Status != PlanWaiting || reviewStep.Status != StepWaiting || afterStep.Status != StepPending {
		t.Fatalf("statuses: plan=%s review=%s after=%s", plan.Status, reviewStep.Status, afterStep.Status)
	}
	if len(llm.calls) != 0 {
		t.Errorf("dependent step must not run while suspended, got %d calls", len(llm.calls))
	}
	reviewID := reviewIDOf(reviewStep)
	if reviewID == "" {
		t.Fatal("no review id recorded on the waiting step")
	}
	// The review is pending and discoverable by any frontend.
	if pend, _ := rm.ListPending(context.Background(), plan.UserID); len(pend) != 1 || pend[0].ID != reviewID {
		t.Fatalf("expected one pending review for the user, got %+v", pend)
	}
	// The waiting state was persisted (durable).
	if saved, _ := ps.Get(context.Background(), "p1"); saved == nil || saved.Status != PlanWaiting {
		t.Fatalf("waiting plan not persisted: %+v", saved)
	}

	// Approve via the manager — the single entry point a dashboard form or a
	// Telegram button handler calls.
	if _, err := rm.Resolve(context.Background(), reviewID, "approve", "ship it", "admin"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Resume continues from the approved step.
	result, err := lr.Resume(context.Background(), "p1")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if result == nil || result.Output != "deployed" {
		t.Errorf("resume result: %+v", result)
	}
	if len(llm.calls) != 1 {
		t.Errorf("dependent step should run once after approval, got %d", len(llm.calls))
	}
	final, _ := ps.Get(context.Background(), "p1")
	if final.Status != PlanCompleted {
		t.Errorf("final plan status: got %s, want completed", final.Status)
	}
}

func TestLocalRunner_HumanReview_RejectFailsPlan(t *testing.T) {
	ps := NewMemoryStore()
	rm := review.New(newMemReviewStore(), nil)
	lr := NewLocalRunner(&mockLLM{out: "x"}, &mockTools{}, WithLocalStore(ps), WithLocalReviewManager(rm))

	plan, reviewStep, _ := reviewPlan("p1")
	_ = ps.Save(context.Background(), plan)
	_, _ = lr.Run(context.Background(), plan, WithStore(ps))
	reviewID := reviewIDOf(reviewStep)

	if _, err := rm.Resolve(context.Background(), reviewID, "reject", "no go", "admin"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	_, err := lr.Resume(context.Background(), "p1")
	if err == nil || !strings.Contains(err.Error(), "review") {
		t.Fatalf("expected a rejection error mentioning the review, got %v", err)
	}
	if final, _ := ps.Get(context.Background(), "p1"); final.Status != PlanFailed {
		t.Errorf("plan should be failed after rejection, got %s", final.Status)
	}
}

func TestLocalRunner_HumanReview_ResumeStillPending(t *testing.T) {
	ps := NewMemoryStore()
	rm := review.New(newMemReviewStore(), nil)
	lr := NewLocalRunner(&mockLLM{out: "x"}, &mockTools{}, WithLocalStore(ps), WithLocalReviewManager(rm))

	plan, _, _ := reviewPlan("p1")
	_ = ps.Save(context.Background(), plan)
	_, _ = lr.Run(context.Background(), plan, WithStore(ps))

	// Resuming before the review is resolved keeps the plan suspended.
	if _, err := lr.Resume(context.Background(), "p1"); !errors.Is(err, ErrPlanWaiting) {
		t.Fatalf("expected ErrPlanWaiting while review is unresolved, got %v", err)
	}
}

func TestLocalRunner_HumanReview_ResumesAfterRestart(t *testing.T) {
	ps := NewMemoryStore()
	rm := review.New(newMemReviewStore(), nil)
	plan, reviewStep, _ := reviewPlan("p1")
	_ = ps.Save(context.Background(), plan)

	runner1 := NewLocalRunner(&mockLLM{out: "v"}, &mockTools{}, WithLocalStore(ps), WithLocalReviewManager(rm))
	if _, err := runner1.Run(context.Background(), plan, WithStore(ps)); !errors.Is(err, ErrPlanWaiting) {
		t.Fatalf("expected ErrPlanWaiting, got %v", err)
	}
	reviewID := reviewIDOf(reviewStep)
	if _, err := rm.Resolve(context.Background(), reviewID, "approve", "", "admin"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Simulate a process restart: a brand-new runner over the SAME persisted plan
	// store and review store resumes the suspended plan.
	runner2 := NewLocalRunner(&mockLLM{out: "v"}, &mockTools{}, WithLocalStore(ps), WithLocalReviewManager(rm))
	if _, err := runner2.Resume(context.Background(), "p1"); err != nil {
		t.Fatalf("Resume after restart: %v", err)
	}
	if final, _ := ps.Get(context.Background(), "p1"); final.Status != PlanCompleted {
		t.Errorf("plan should complete after restart+resume, got %s", final.Status)
	}
}

// TestLocalRunner_HumanReview_ConcurrentResumeSafe drives two concurrent Resume
// calls on the same suspended-then-approved plan. The per-plan resume lock
// serializes them, so the dependent step runs exactly once and the plan ends
// completed — without the lock the two resumes would both reconcile the waiting
// step, double-run the dependent step, and lose one another's update.
func TestLocalRunner_HumanReview_ConcurrentResumeSafe(t *testing.T) {
	ps := NewMemoryStore()
	rm := review.New(newMemReviewStore(), nil)
	llm := &mockLLM{out: "ok"}
	lr := NewLocalRunner(llm, &mockTools{}, WithLocalStore(ps), WithLocalReviewManager(rm))

	plan, reviewStep, _ := reviewPlan("p1")
	_ = ps.Save(context.Background(), plan)
	_, _ = lr.Run(context.Background(), plan, WithStore(ps))
	reviewID := reviewIDOf(reviewStep)
	if _, err := rm.Resolve(context.Background(), reviewID, "approve", "", "admin"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, _ = lr.Resume(context.Background(), "p1")
		}()
	}
	wg.Wait()

	final, _ := ps.Get(context.Background(), "p1")
	if final.Status != PlanCompleted {
		t.Errorf("plan should be completed after concurrent resume, got %s", final.Status)
	}
	for _, s := range final.Steps {
		if s.ID == "after" && s.Status != StepCompleted {
			t.Errorf("dependent step status = %s, want completed", s.Status)
		}
	}
	llm.mu.Lock()
	calls := len(llm.calls)
	llm.mu.Unlock()
	if calls != 1 {
		t.Errorf("dependent step should run exactly once across concurrent resumes, got %d", calls)
	}
}

// TestLocalRunner_HumanReviewInParallel_Rejected: a parallel block cannot
// partially suspend, so an async human_review sub-step is rejected with a clear
// error rather than being mishandled as a failed sub-step.
func TestLocalRunner_HumanReviewInParallel_Rejected(t *testing.T) {
	ps := NewMemoryStore()
	rm := review.New(newMemReviewStore(), nil)
	lr := NewLocalRunner(&mockLLM{out: "x"}, &mockTools{}, WithLocalStore(ps), WithLocalReviewManager(rm))

	hr := &Step{ID: "hr", Type: StepHumanReview, Status: StepPending, Name: "approve"}
	par := &Step{ID: "par", Type: StepParallel, Status: StepPending, Config: StepConfig{SubSteps: []*Step{hr}}}
	plan := pendingPlan("p1", par)
	_ = ps.Save(context.Background(), plan)

	_, err := lr.Run(context.Background(), plan, WithStore(ps))
	if err == nil || !strings.Contains(err.Error(), "cannot be a sub-step of parallel") {
		t.Fatalf("expected human_review-in-parallel rejection, got %v", err)
	}
}

func TestResumeOnReview_DrivesResumeFromResolve(t *testing.T) {
	ps := NewMemoryStore()
	rm := review.New(newMemReviewStore(), nil)
	lr := NewLocalRunner(&mockLLM{out: "done"}, &mockTools{}, WithLocalStore(ps), WithLocalReviewManager(rm))
	// Wire the resume listener: a resolution from ANY UI drives the resume.
	rm.OnResolve(ResumeOnReview(lr, nil))

	plan, reviewStep, _ := reviewPlan("p1")
	_ = ps.Save(context.Background(), plan)
	if _, err := lr.Run(context.Background(), plan, WithStore(ps)); !errors.Is(err, ErrPlanWaiting) {
		t.Fatalf("expected ErrPlanWaiting, got %v", err)
	}
	reviewID := reviewIDOf(reviewStep)

	// Resolving (the same call a dashboard POST or Telegram callback makes) fires
	// the listener, which resumes the plan asynchronously — no direct Resume here.
	if _, err := rm.Resolve(context.Background(), reviewID, "approve", "", "admin:1.2.3.4"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		final, _ := ps.Get(context.Background(), "p1")
		if final != nil && final.Status == PlanCompleted {
			break
		}
		if time.Now().After(deadline) {
			status := PlanStatus("nil")
			if final, _ := ps.Get(context.Background(), "p1"); final != nil {
				status = final.Status
			}
			t.Fatalf("plan did not complete via resume-on-resolve; status=%s", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
