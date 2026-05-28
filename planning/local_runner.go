package planning

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// LLMCaller is used by LocalRunner for LLM and agent-delegate steps. *engine.Engine implements it.
type LLMCaller interface {
	ProcessMessage(ctx context.Context, sessionID string, message string) (string, int, error)
}

// ToolExecutor is used by LocalRunner for tool steps. *model.FunctionRegistry implements it.
type ToolExecutor interface {
	Execute(toolName string, args map[string]interface{}) (string, error)
}

// LocalRunner runs plans in-process using the Engine and FunctionRegistry.
type LocalRunner struct {
	engine    LLMCaller
	llmClient model.LLMClient
	llmModel  string
	functions ToolExecutor
	callback  engine.Callback
	store     PlanStore
	observer  Observer
}

// Ensure LocalRunner can still be constructed with *engine.Engine and *model.FunctionRegistry.
var (
	_ LLMCaller    = (*engine.Engine)(nil)
	_ ToolExecutor = (*model.FunctionRegistry)(nil)
)

// LocalRunnerOption configures a LocalRunner.
type LocalRunnerOption func(*LocalRunner)

// WithLocalStore sets the plan store for persisting state.
func WithLocalStore(s PlanStore) LocalRunnerOption {
	return func(lr *LocalRunner) { lr.store = s }
}

// WithLocalObserver sets the observer for lifecycle events.
func WithLocalObserver(o Observer) LocalRunnerOption {
	return func(lr *LocalRunner) { lr.observer = o }
}

// WithLocalCallback sets the callback for billing/usage hooks.
func WithLocalCallback(cb engine.Callback) LocalRunnerOption {
	return func(lr *LocalRunner) { lr.callback = cb }
}

// WithLocalLLMClient sets a direct LLM client for plan steps, bypassing Engine.ProcessMessage.
// Use this when the Engine may not have LLM configured or sessions are managed externally (e.g. CoreHandler).
func WithLocalLLMClient(client model.LLMClient, modelName string) LocalRunnerOption {
	return func(lr *LocalRunner) {
		lr.llmClient = client
		lr.llmModel = modelName
	}
}

// NewLocalRunner returns a new LocalRunner. eng and funcs can be *engine.Engine and *model.FunctionRegistry.
func NewLocalRunner(eng LLMCaller, funcs ToolExecutor, opts ...LocalRunnerOption) *LocalRunner {
	lr := &LocalRunner{engine: eng, functions: funcs}
	for _, opt := range opts {
		opt(lr)
	}
	return lr
}

// Run executes the plan: topological sort, then run ready steps until all done.
func (lr *LocalRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	cfg := &runConfig{}
	for _, o := range opts {
		o(cfg)
	}

	// Apply run-level overrides via shallow copy so concurrent
	// calls with different clients/executors don't interfere with each other.
	rl := lr
	needsCopy := (cfg.llmClient != nil && lr.llmClient == nil) || (cfg.toolExecutor != nil)
	if needsCopy {
		cp := *lr
		if cfg.llmClient != nil && lr.llmClient == nil {
			cp.llmClient = cfg.llmClient
			cp.llmModel = cfg.llmModel
		}
		if cfg.toolExecutor != nil {
			cp.functions = cfg.toolExecutor
		}
		rl = &cp
	}

	store := cfg.store
	if store == nil {
		store = rl.store
	}
	observer := cfg.observer
	if observer == nil {
		observer = rl.observer
	}
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}

	plan.Status = PlanRunning
	plan.UpdatedAt = time.Now()
	if store != nil {
		_ = store.Update(ctx, plan)
	}
	if observer != nil {
		observer.OnPlanCreated(plan)
	}

	_, err := TopologicalSort(plan.Steps)
	if err != nil {
		plan.Status = PlanFailed
		plan.Error = err.Error()
		if store != nil {
			_ = store.Update(ctx, plan)
		}
		if observer != nil {
			observer.OnPlanFailed(plan, err)
		}
		return nil, err
	}

	start := time.Now()
	var totalTokens int
	var finalOutput string

	for {
		select {
		case <-ctx.Done():
			plan.Status = PlanCancelled
			plan.Error = ctx.Err().Error()
			plan.UpdatedAt = time.Now()
			if store != nil {
				_ = store.Update(ctx, plan)
			}
			if observer != nil {
				observer.OnPlanFailed(plan, ctx.Err())
			}
			return nil, ctx.Err()
		default:
		}

		ready := ReadySteps(plan.Steps)
		if len(ready) == 0 {
			allDone := true
			for _, s := range plan.Steps {
				if s.Status != StepCompleted && s.Status != StepFailed && s.Status != StepSkipped {
					allDone = false
					break
				}
			}
			if allDone {
				break
			}
			plan.Status = PlanFailed
			plan.Error = "no ready steps and plan not complete"
			if store != nil {
				_ = store.Update(ctx, plan)
			}
			if observer != nil {
				observer.OnPlanFailed(plan, ErrStepFailed)
			}
			return nil, ErrStepFailed
		}

		for _, step := range ready {
			result, err := rl.RunStep(ctx, plan, step)
			if err != nil {
				step.Status = StepFailed
				step.Error = err.Error()
				now := time.Now()
				step.CompletedAt = &now
				if store != nil {
					_ = store.Update(ctx, plan)
				}
				if observer != nil {
					observer.OnStepFailed(plan, step, err)
				}
				plan.Status = PlanFailed
				plan.Error = err.Error()
				now2 := time.Now()
				plan.CompletedAt = &now2
				if store != nil {
					_ = store.Update(ctx, plan)
				}
				if observer != nil {
					observer.OnPlanFailed(plan, err)
				}
				return nil, err
			}
			step.Status = StepCompleted
			step.Result = result
			now := time.Now()
			step.CompletedAt = &now
			if result != nil {
				totalTokens += result.TokensUsed
				if result.Output != "" {
					finalOutput = result.Output
				}
			}
			if store != nil {
				_ = store.Update(ctx, plan)
			}
			if observer != nil {
				observer.OnStepCompleted(plan, step, result)
			}
		}
	}

	plan.Status = PlanCompleted
	plan.Output = finalOutput
	now := time.Now()
	plan.CompletedAt = &now
	plan.UpdatedAt = now
	if store != nil {
		_ = store.Update(ctx, plan)
	}

	result := &PlanResult{
		PlanID:      plan.ID,
		Output:      finalOutput,
		Steps:       plan.Steps,
		TotalTokens: totalTokens,
		Duration:    time.Since(start),
	}
	if observer != nil {
		observer.OnPlanCompleted(plan, result)
	}
	return result, nil
}

// RunStep executes a single step based on its type.
func (lr *LocalRunner) RunStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	start := time.Now()
	step.Status = StepRunning
	step.StartedAt = &start

	if lr.callback != nil {
		ev := &engine.UsageEvent{
			UserID:    plan.UserID,
			SessionID: plan.SessionID,
			EventType: engine.EventToolCall,
			Name:      step.Config.ToolName,
		}
		if step.Type == StepLLMCall {
			ev.EventType = engine.EventLLMCall
			ev.Name = engine.EventNameLLMCall
		}
		if step.Type == StepAgentDelegate {
			ev.EventType = engine.EventAgentRouting
		}
		if err := lr.callback.BeforeAction(ctx, ev); err != nil {
			return nil, err
		}
		defer func() {
			ev.Duration = time.Since(start)
			lr.callback.AfterAction(ctx, ev)
		}()
	}

	if lr.observer != nil {
		lr.observer.OnStepStarted(plan, step)
	}

	var result *StepResult
	var err error

	switch step.Type {
	case StepToolCall:
		result, err = lr.runToolStep(ctx, plan, step)
	case StepLLMCall:
		result, err = lr.runLLMStep(ctx, plan, step)
	case StepAgentDelegate:
		result, err = lr.runAgentDelegateStep(ctx, plan, step)
	case StepParallel:
		result, err = lr.runParallelStep(ctx, plan, step)
	default:
		return nil, ErrInvalidStep
	}

	if err != nil {
		return nil, err
	}
	if result != nil {
		result.Duration = time.Since(start)
	}
	return result, nil
}

func (lr *LocalRunner) runToolStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	if step.Config.ToolName == "" {
		return nil, fmt.Errorf("tool_call step %q has empty tool_name", step.ID)
	}
	if lr.functions == nil {
		return nil, fmt.Errorf("no tool executor configured for tool_call step %q (tool: %s)", step.ID, step.Config.ToolName)
	}
	args := make(map[string]interface{})
	for k, v := range step.Config.ToolArgs {
		args[k] = v
	}
	out, err := lr.functions.Execute(step.Config.ToolName, args)
	if err != nil {
		return nil, err
	}
	return &StepResult{Output: out}, nil
}

func (lr *LocalRunner) runLLMStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	prompt := step.Config.Prompt
	if prompt == "" {
		prompt = plan.Input
	}
	if lr.llmClient != nil {
		return lr.directLLMCall(ctx, prompt)
	}
	if lr.engine == nil {
		return nil, fmt.Errorf("no LLM caller configured: set either engine or llmClient")
	}
	out, tokens, err := lr.engine.ProcessMessage(ctx, plan.SessionID, prompt)
	if err != nil {
		return nil, err
	}
	return &StepResult{Output: out, TokensUsed: tokens}, nil
}

func (lr *LocalRunner) runAgentDelegateStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	input := step.Config.AgentInput
	if input == "" {
		input = plan.Input
	}
	if lr.llmClient != nil {
		return lr.directLLMCall(ctx, input)
	}
	if lr.engine == nil {
		return nil, fmt.Errorf("no LLM caller configured: set either engine or llmClient")
	}
	out, tokens, err := lr.engine.ProcessMessage(ctx, plan.SessionID, input)
	if err != nil {
		return nil, err
	}
	return &StepResult{Output: out, TokensUsed: tokens}, nil
}

func (lr *LocalRunner) runParallelStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	subs := step.Config.SubSteps
	if len(subs) == 0 {
		return &StepResult{}, nil
	}
	var wg sync.WaitGroup
	results := make([]*StepResult, len(subs))
	errs := make([]error, len(subs))
	for i, sub := range subs {
		wg.Add(1)
		go func(idx int, s *Step) {
			defer wg.Done()
			res, err := lr.RunStep(ctx, plan, s)
			results[idx] = res
			errs[idx] = err
		}(i, sub)
	}
	wg.Wait()
	var combined string
	var totalTokens int
	for i, e := range errs {
		if e != nil {
			return nil, e
		}
		if results[i] != nil {
			combined += results[i].Output
			totalTokens += results[i].TokensUsed
		}
	}
	return &StepResult{Output: combined, TokensUsed: totalTokens}, nil
}

func (lr *LocalRunner) directLLMCall(ctx context.Context, prompt string) (*StepResult, error) {
	resp, err := lr.llmClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: lr.llmModel,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: prompt},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("LLM returned no choices")
	}
	return &StepResult{
		Output:     resp.Choices[0].Message.Content,
		TokensUsed: resp.Usage.TotalTokens,
	}, nil
}

// Cancel marks the plan as cancelled in the store.
func (lr *LocalRunner) Cancel(ctx context.Context, planID string) error {
	store := lr.store
	if store == nil {
		return ErrNoPlanStore
	}
	plan, err := store.Get(ctx, planID)
	if err != nil {
		return err
	}
	plan.Status = PlanCancelled
	plan.UpdatedAt = time.Now()
	return store.Update(ctx, plan)
}

// Resume loads the plan from store and continues execution from the next ready steps.
func (lr *LocalRunner) Resume(ctx context.Context, planID string) (*PlanResult, error) {
	store := lr.store
	if store == nil {
		return nil, ErrNoPlanStore
	}
	plan, err := store.Get(ctx, planID)
	if err != nil {
		return nil, err
	}
	plan.Status = PlanRunning
	plan.UpdatedAt = time.Now()
	_ = store.Update(ctx, plan)
	return lr.Run(ctx, plan, WithStore(store), WithObserver(lr.observer))
}
