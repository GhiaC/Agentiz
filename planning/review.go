package planning

import (
	"context"
	"errors"
	"strings"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
)

// ReviewManager raises async human reviews and looks up their decisions. A
// *review.Manager satisfies it structurally — planning depends only on this
// narrow interface (plus model), never on the review package, so the approval
// abstraction stays generic and reusable. Wire it with WithLocalReviewManager.
type ReviewManager interface {
	// Request creates a pending review, persists it, and returns its id.
	Request(ctx context.Context, r *model.ReviewRequest) (string, error)
	// Get returns the review by id, or (nil, nil) when absent.
	Get(ctx context.Context, id string) (*model.ReviewRequest, error)
}

// SyncReviewManager additionally blocks until a review resolves, for the
// synchronous ReviewFunc bridge (single-process use without durability).
type SyncReviewManager interface {
	ReviewManager
	// Await blocks until the review resolves, ctx is done, or it expires.
	Await(ctx context.Context, id string) (*model.ReviewRequest, error)
}

// reviewTitle is the human-facing prompt for a step's review.
func reviewTitle(step *Step) string {
	if step != nil && step.Name != "" {
		return "Approve: " + step.Name
	}
	if step != nil {
		return "Approve step " + step.ID
	}
	return "Approve step"
}

// SyncReviewer adapts a review manager to the synchronous ReviewFunc: it raises a
// review and BLOCKS (Await) until a human resolves it, mapping the decision to
// approved/note. Use it (via WithLocalReviewer) for simple in-process flows that
// do not need durable suspend/resume; for durability use WithLocalReviewManager.
func SyncReviewer(m SyncReviewManager) ReviewFunc {
	return func(ctx context.Context, plan *Plan, step *Step, content string) (bool, string, error) {
		req := &model.ReviewRequest{
			UserID:    plan.UserID,
			SessionID: plan.SessionID,
			Kind:      "plan_step",
			RefID:     plan.ID + "/" + step.ID,
			Title:     reviewTitle(step),
			Content:   content,
			Metadata:  map[string]any{"plan_id": plan.ID, "step_id": step.ID},
		}
		id, err := m.Request(ctx, req)
		if err != nil {
			return false, "", err
		}
		resolved, err := m.Await(ctx, id)
		if err != nil {
			return false, "", err
		}
		if resolved == nil {
			return false, "", nil
		}
		return resolved.IsApproved(), resolved.Note, nil
	}
}

// ResumeOnReview returns a review resolve-listener that resumes the plan a
// resolved "plan_step" review belongs to (its RefID is "<planID>/<stepID>").
// Register it on the review manager — e.g. manager.OnResolve(planning.ResumeOnReview(runner, nil))
// — so a decision from ANY frontend (Telegram button, dashboard form, API)
// drives the resume through the one Resolve entry point. Resumption runs in its
// own goroutine so Resolve never blocks on plan execution.
func ResumeOnReview(runner Runner, logger *log.Logger) func(context.Context, *model.ReviewRequest) {
	if logger == nil {
		logger = log.Log
	}
	return func(ctx context.Context, r *model.ReviewRequest) {
		if r == nil || r.Kind != "plan_step" {
			return
		}
		planID, _, ok := strings.Cut(r.RefID, "/")
		if !ok || planID == "" {
			return
		}
		go func() {
			// Detach from the Resolve caller's context so resumption is not tied
			// to a short-lived request.
			rctx := context.WithoutCancel(ctx)
			if _, err := runner.Resume(rctx, planID); err != nil && !errors.Is(err, ErrPlanWaiting) {
				logger.Warnf("planning: resume-on-review plan %s: %v", planID, err)
			}
		}()
	}
}
