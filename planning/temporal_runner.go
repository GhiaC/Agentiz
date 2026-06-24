package planning

import (
	"context"
	"fmt"
	"time"

	"github.com/ghiac/agentize/log"
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
	adapter        TemporalAdapter
	store          PlanStore
	logger         *log.Logger
	onPersistError PersistErrorFunc
}

// TemporalRunnerOption configures a TemporalRunner.
type TemporalRunnerOption func(*TemporalRunner)

// WithTemporalStore sets the plan store for syncing state (e.g. from Query).
func WithTemporalStore(s PlanStore) TemporalRunnerOption {
	return func(tr *TemporalRunner) { tr.store = s }
}

// WithTemporalLogger sets the logger for workflow lifecycle and store-sync
// failures (default: log.Log).
func WithTemporalLogger(l *log.Logger) TemporalRunnerOption {
	return func(tr *TemporalRunner) {
		if l != nil {
			tr.logger = l
		}
	}
}

// WithTemporalPersistErrorHandler wires a callback invoked whenever syncing
// queried workflow state to the store fails, mirroring the LocalRunner contract
// so host apps can halt or alert when in-memory and persisted state diverge.
func WithTemporalPersistErrorHandler(fn PersistErrorFunc) TemporalRunnerOption {
	return func(tr *TemporalRunner) { tr.onPersistError = fn }
}

// NewTemporalRunner returns a new TemporalRunner.
func NewTemporalRunner(adapter TemporalAdapter, opts ...TemporalRunnerOption) *TemporalRunner {
	tr := &TemporalRunner{adapter: adapter, logger: log.Log}
	for _, opt := range opts {
		opt(tr)
	}
	if tr.logger == nil {
		tr.logger = log.Log
	}
	return tr
}

// persist syncs queried workflow state to the store, logging and reporting
// failures via the persist-error handler instead of dropping them silently
// (the LocalRunner P6/P10 contract, carried to the Temporal path).
func (tr *TemporalRunner) persist(ctx context.Context, store PlanStore, plan *Plan) {
	if store == nil || plan == nil {
		return
	}
	if err := store.Update(context.WithoutCancel(ctx), plan); err != nil {
		tr.logger.Warnf("planning: temporal sync plan %s to store failed: %v", plan.ID, err)
		if tr.onPersistError != nil {
			tr.onPersistError(plan, err)
		}
	}
}

// Run starts the workflow and polls until completion, then returns the plan result.
// Graceful shutdown: when ctx is cancelled (e.g. SIGTERM), Run returns immediately with ctx.Err().
func (tr *TemporalRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	workflowID, err := tr.adapter.StartWorkflow(ctx, plan)
	if err != nil {
		tr.logger.Errorf("planning: temporal start workflow for plan %s failed: %v", plan.ID, err)
		return nil, err
	}
	tr.logger.Infof("planning: temporal plan %s started (workflow %s)", plan.ID, workflowID)
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
			tr.logger.Infof("planning: temporal plan %s run aborted: %v", plan.ID, ctx.Err())
			return nil, ctx.Err()
		case <-ticker.C:
			current, err := tr.adapter.QueryWorkflow(ctx, workflowID, "PlanState")
			if err != nil {
				tr.logger.Warnf("planning: temporal query plan %s (workflow %s) failed: %v", plan.ID, workflowID, err)
				continue
			}
			tr.persist(ctx, store, current)
			if current != nil && (current.Status == PlanCompleted || current.Status == PlanFailed || current.Status == PlanCancelled) {
				tr.logger.Infof("planning: temporal plan %s reached %s", current.ID, current.Status)
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
	tr.logger.Infof("planning: temporal plan %s cancel requested", planID)
	if err := tr.adapter.CancelWorkflow(ctx, planID); err != nil {
		tr.logger.Errorf("planning: temporal cancel plan %s failed: %v", planID, err)
		return err
	}
	return nil
}

// Resume sends a Resume signal to the workflow, then polls for result (requires store to load plan).
func (tr *TemporalRunner) Resume(ctx context.Context, planID string) (*PlanResult, error) {
	tr.logger.Infof("planning: temporal plan %s resuming", planID)
	if err := tr.adapter.SignalWorkflow(ctx, planID, "Resume", nil); err != nil {
		tr.logger.Errorf("planning: temporal resume signal for plan %s failed: %v", planID, err)
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
