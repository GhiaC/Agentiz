// Package planning provides a structured planning and execution layer for Agentize.
// Plans are DAGs of steps (LLM calls, tool calls, agent delegation); execution
// can be in-process (LocalRunner) or via Temporal (TemporalRunner).
package planning

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ghiac/agentize/model"
)

// OrchestratorOption configures an Orchestrator.
type OrchestratorOption func(*Orchestrator)

// WithOrchestratorStore sets the plan store (default: NewMemoryStore()).
func WithOrchestratorStore(s PlanStore) OrchestratorOption {
	return func(o *Orchestrator) { o.store = s }
}

// WithOrchestratorObserver sets the observer for lifecycle events.
func WithOrchestratorObserver(obs Observer) OrchestratorOption {
	return func(o *Orchestrator) { o.observer = obs }
}

// WithSeedConfig sets config for initial plan seeding (template plans on first run).
func WithSeedConfig(cfg SeedConfig) OrchestratorOption {
	return func(o *Orchestrator) { o.seedConfig = cfg }
}

// WithOrchestratorLLMClient sets a direct LLM client on the Orchestrator.
// This client is passed to the Runner via WithRunLLMClient on every Execute call.
func WithOrchestratorLLMClient(client model.LLMClient, modelName string) OrchestratorOption {
	return func(o *Orchestrator) {
		o.llmClient = client
		o.llmModel = modelName
	}
}

// WithOrchestratorToolExecutor sets a ToolExecutor on the Orchestrator.
// This executor is passed to the Runner via WithRunToolExecutor on every Execute call,
// allowing plan tool_call steps to execute Core tools.
func WithOrchestratorToolExecutor(exec ToolExecutor) OrchestratorOption {
	return func(o *Orchestrator) { o.toolExecutor = exec }
}

// Orchestrator wires a Planner and Runner and exposes a single Execute entrypoint.
type Orchestrator struct {
	planner      Planner
	runner       Runner
	store        PlanStore
	observer     Observer
	seedConfig   SeedConfig
	planSeqMu    sync.Mutex
	llmClient    model.LLMClient
	llmModel     string
	toolExecutor ToolExecutor
}

// NewOrchestrator returns a new Orchestrator. If no store is set via options, uses NewMemoryStore().
func NewOrchestrator(planner Planner, runner Runner, opts ...OrchestratorOption) *Orchestrator {
	o := &Orchestrator{planner: planner, runner: runner}
	for _, opt := range opts {
		opt(o)
	}
	if o.store == nil {
		o.store = NewMemoryStore()
	}
	return o
}

// Execute creates a plan, saves it, and runs it with the configured store and observer.
func (o *Orchestrator) Execute(ctx context.Context, input PlanInput) (*PlanResult, error) {
	plan, err := o.planner.CreatePlan(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("planning: create plan: %w", err)
	}

	o.planSeqMu.Lock()
	existing, _ := o.store.List(ctx, input.UserID)
	seq := len(existing) + 1
	plan.ID = fmt.Sprintf("plan_%s_%d", input.UserID, seq)
	o.planSeqMu.Unlock()

	plan.CreatedAt = time.Now()
	plan.UpdatedAt = plan.CreatedAt
	if err := o.store.Save(ctx, plan); err != nil {
		return nil, fmt.Errorf("planning: save plan: %w", err)
	}
	var obs Observer
	if o.observer != nil {
		obs = o.observer
	}
	if input.Observer != nil {
		if obs != nil {
			obs = MultiObserver(obs, input.Observer)
		} else {
			obs = input.Observer
		}
	}
	var runOpts []RunOption
	if obs != nil {
		runOpts = append(runOpts, WithObserver(obs))
	}
	runOpts = append(runOpts, WithStore(o.store))
	if o.llmClient != nil {
		runOpts = append(runOpts, WithRunLLMClient(o.llmClient, o.llmModel))
	}
	// Resolve tool executor: prefer per-call from PlanContext, fall back to orchestrator-level.
	var te ToolExecutor
	if input.Context != nil && input.Context.ToolExecutor != nil {
		te = input.Context.ToolExecutor
	} else if o.toolExecutor != nil {
		te = o.toolExecutor
	}
	if te != nil {
		runOpts = append(runOpts, WithRunToolExecutor(te))
	}
	return o.runner.Run(ctx, plan, runOpts...)
}

// SetLLMClient sets the LLM client on the Orchestrator at runtime. Useful when the
// client is not available at construction time (e.g. CoreHandler.UseLLMConfig called after UsePlanning).
func (o *Orchestrator) SetLLMClient(client model.LLMClient, modelName string) {
	o.llmClient = client
	o.llmModel = modelName
}

// SetToolExecutor sets the tool executor on the Orchestrator at runtime.
func (o *Orchestrator) SetToolExecutor(exec ToolExecutor) {
	o.toolExecutor = exec
}

// GetStore returns the plan store (for debug and external callers).
func (o *Orchestrator) GetStore() PlanStore {
	return o.store
}

// EnsureSeed creates initial template plans when the store is empty and seed config allows.
// Call once at startup (e.g. after UsePlanning) so the dashboard has plan data to display.
func (o *Orchestrator) EnsureSeed(ctx context.Context) error {
	return EnsureSeedPlans(ctx, o.store, o.seedConfig)
}

// GetPlan returns a plan by ID (for status checks and debug).
func (o *Orchestrator) GetPlan(ctx context.Context, planID string) (*Plan, error) {
	return o.store.Get(ctx, planID)
}

// Cancel stops a running plan. Returns ErrPlanNotFound if the plan does not exist.
func (o *Orchestrator) Cancel(ctx context.Context, planID string) error {
	return o.runner.Cancel(ctx, planID)
}
