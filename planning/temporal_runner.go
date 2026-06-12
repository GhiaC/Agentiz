package planning

import (
	"context"
	"fmt"
	"time"
)

// TemporalAdapter abstracts Temporal workflow operations so the core package does not depend on the Temporal SDK.
type TemporalAdapter interface {
	StartWorkflow(ctx context.Context, plan *Plan) (workflowID string, err error)
	SignalWorkflow(ctx context.Context, workflowID string, signal string, data any) error
	QueryWorkflow(ctx context.Context, workflowID string, query string) (*Plan, error)
	CancelWorkflow(ctx context.Context, workflowID string) error
}

// TemporalRunner runs plans by delegating to a Temporal workflow via the adapter.
type TemporalRunner struct {
	adapter TemporalAdapter
	store   PlanStore
}

// TemporalRunnerOption configures a TemporalRunner.
type TemporalRunnerOption func(*TemporalRunner)

// WithTemporalStore sets the plan store for syncing state (e.g. from Query).
func WithTemporalStore(s PlanStore) TemporalRunnerOption {
	return func(tr *TemporalRunner) { tr.store = s }
}

// NewTemporalRunner returns a new TemporalRunner.
func NewTemporalRunner(adapter TemporalAdapter, opts ...TemporalRunnerOption) *TemporalRunner {
	tr := &TemporalRunner{adapter: adapter}
	for _, opt := range opts {
		opt(tr)
	}
	return tr
}

// Run starts the workflow and polls until completion, then returns the plan result.
// Graceful shutdown: when ctx is cancelled (e.g. SIGTERM), Run returns immediately with ctx.Err().
func (tr *TemporalRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	workflowID, err := tr.adapter.StartWorkflow(ctx, plan)
	if err != nil {
		return nil, err
	}
	cfg := &runConfig{}
	for _, o := range opts {
		o(cfg)
	}
	store := cfg.store
	if store == nil {
		store = tr.store
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			current, err := tr.adapter.QueryWorkflow(ctx, workflowID, "PlanState")
			if err != nil {
				continue
			}
			if store != nil && current != nil {
				_ = store.Update(ctx, current)
			}
			if current != nil && (current.Status == PlanCompleted || current.Status == PlanFailed || current.Status == PlanCancelled) {
				var output string
				var totalTokens int
				for _, s := range current.Steps {
					if s.Result != nil {
						output = s.Result.Output
						totalTokens += s.Result.TokensUsed
					}
				}
				return &PlanResult{
					PlanID:      current.ID,
					Output:      output,
					Steps:       current.Steps,
					TotalTokens: totalTokens,
				}, nil
			}
		}
	}
}

// RunStep is not supported for TemporalRunner; steps execute inside the
// Temporal workflow, not in this process. Use Run, or signal the workflow via
// the adapter.
func (tr *TemporalRunner) RunStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	return nil, fmt.Errorf("%w: TemporalRunner cannot run individual steps; steps execute inside the Temporal workflow", ErrInvalidStep)
}

// Cancel requests cancellation of the workflow.
func (tr *TemporalRunner) Cancel(ctx context.Context, planID string) error {
	return tr.adapter.CancelWorkflow(ctx, planID)
}

// Resume sends a Resume signal to the workflow, then polls for result (requires store to load plan).
func (tr *TemporalRunner) Resume(ctx context.Context, planID string) (*PlanResult, error) {
	if err := tr.adapter.SignalWorkflow(ctx, planID, "Resume", nil); err != nil {
		return nil, err
	}
	if tr.store == nil {
		return nil, ErrNoPlanStore
	}
	plan, err := tr.store.Get(ctx, planID)
	if err != nil {
		return nil, err
	}
	return tr.Run(ctx, plan, WithStore(tr.store))
}
