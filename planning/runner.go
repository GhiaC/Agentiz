package planning

import (
	"context"
	"time"

	"github.com/ghiac/agentize/model"
)

// PlanResult is the result of running a plan.
type PlanResult struct {
	PlanID      string        `json:"plan_id"`
	Output      string        `json:"output"`
	Steps       []*Step       `json:"steps"`
	TotalTokens int           `json:"total_tokens"`
	Duration    time.Duration `json:"duration"`
}

type runConfig struct {
	observer     Observer
	store        PlanStore
	timeout      time.Duration
	llmClient    model.LLMClient
	llmModel     string
	toolExecutor ToolExecutor
}

// RunOption configures how Run is executed.
type RunOption func(*runConfig)

// WithObserver sets the observer for plan/step lifecycle events.
func WithObserver(o Observer) RunOption {
	return func(c *runConfig) { c.observer = o }
}

// WithStore sets the store for persisting plan state.
func WithStore(s PlanStore) RunOption {
	return func(c *runConfig) { c.store = s }
}

// WithTimeout sets a timeout for the entire plan execution.
func WithTimeout(d time.Duration) RunOption {
	return func(c *runConfig) { c.timeout = d }
}

// WithRunLLMClient passes a direct LLM client to the runner for this execution,
// bypassing Engine.ProcessMessage. Used by Orchestrator when the caller (e.g. CoreHandler) provides its own client.
func WithRunLLMClient(client model.LLMClient, modelName string) RunOption {
	return func(c *runConfig) {
		c.llmClient = client
		c.llmModel = modelName
	}
}

// WithRunToolExecutor overrides the ToolExecutor for this run, allowing Core tools
// to be executed during plan steps instead of (or in addition to) the FunctionRegistry.
func WithRunToolExecutor(exec ToolExecutor) RunOption {
	return func(c *runConfig) { c.toolExecutor = exec }
}

// Runner executes a plan (in-process or via external workflow engine).
type Runner interface {
	Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error)
	RunStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error)
	Cancel(ctx context.Context, planID string) error
	Resume(ctx context.Context, planID string) (*PlanResult, error)
}
