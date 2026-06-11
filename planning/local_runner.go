package planning

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

// ReviewFunc decides whether a human_review step's content is approved. Wire it
// with WithLocalReviewer. When unset, human_review auto-approves (pass-through)
// so plans containing review gates still run in-process.
type ReviewFunc func(ctx context.Context, plan *Plan, step *Step, content string) (approved bool, note string, err error)

// LocalRunner runs plans in-process using the Engine and FunctionRegistry.
type LocalRunner struct {
	engine    LLMCaller
	llmClient model.LLMClient
	llmModel  string
	functions ToolExecutor
	callback  engine.Callback
	store     PlanStore
	observer  Observer
	reviewer  ReviewFunc
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

// WithLocalReviewer wires a human-review decision function for human_review
// steps. When unset those steps auto-approve (pass-through).
func WithLocalReviewer(fn ReviewFunc) LocalRunnerOption {
	return func(lr *LocalRunner) { lr.reviewer = fn }
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

		// Cascade skips from any conditional that ran in the previous iteration
		// so steps with no surviving input path don't deadlock the plan.
		PropagateSkips(plan.Steps)

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
			// A step earlier in this batch (e.g. a conditional) may have skipped
			// this one; respect that rather than running it anyway.
			if step.Status != StepPending {
				continue
			}
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

	// Honor a per-step timeout when configured (opt-in: Timeout > 0). The
	// plan-level timeout from WithTimeout still applies via the parent ctx.
	stepCtx := ctx
	if step.Config.Timeout > 0 {
		var cancel context.CancelFunc
		stepCtx, cancel = context.WithTimeout(ctx, step.Config.Timeout)
		defer cancel()
	}

	// Honor a per-step retry budget when configured (opt-in: MaxRetries > 0).
	// Default MaxRetries == 0 means a single attempt — unchanged behavior.
	attempts := step.Config.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var result *StepResult
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		result, err = lr.dispatchStep(stepCtx, plan, step)
		if err == nil || stepCtx.Err() != nil || attempt == attempts-1 {
			break
		}
		// Back off before retrying, but abort promptly if the context ends.
		timer := time.NewTimer(stepRetryBackoff)
		select {
		case <-timer.C:
		case <-stepCtx.Done():
			timer.Stop()
		}
	}

	if err != nil {
		return nil, err
	}
	if result != nil {
		result.Duration = time.Since(start)
	}
	return result, nil
}

// stepRetryBackoff is the delay between per-step retry attempts.
const stepRetryBackoff = 250 * time.Millisecond

// dispatchStep routes a step to the handler for its type.
func (lr *LocalRunner) dispatchStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	switch step.Type {
	case StepToolCall:
		return lr.runToolStep(ctx, plan, step)
	case StepLLMCall:
		return lr.runLLMStep(ctx, plan, step)
	case StepAgentDelegate:
		return lr.runAgentDelegateStep(ctx, plan, step)
	case StepParallel:
		return lr.runParallelStep(ctx, plan, step)
	case StepConditional:
		return lr.runConditionalStep(ctx, plan, step)
	case StepCollect:
		return lr.runCollectStep(ctx, plan, step)
	case StepHumanReview:
		return lr.runHumanReviewStep(ctx, plan, step)
	default:
		return nil, ErrInvalidStep
	}
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
	return lr.runPrompt(ctx, plan, prompt)
}

func (lr *LocalRunner) runAgentDelegateStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	input := step.Config.AgentInput
	if input == "" {
		input = plan.Input
	}
	return lr.runPrompt(ctx, plan, input)
}

// runPrompt sends a prompt to the configured LLM — a direct client when set,
// otherwise the Engine's session-aware ProcessMessage. Shared by the LLM,
// agent-delegate and collect-synthesis steps.
func (lr *LocalRunner) runPrompt(ctx context.Context, plan *Plan, prompt string) (*StepResult, error) {
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

// runConditionalStep evaluates the step's Condition and skips the steps listed
// under the branch keys that were NOT selected. Branch keys are "true"/"false"
// (the boolean result of the condition). Steps in non-selected branches are
// marked skipped; PropagateSkips then cascades to their descendants. Branch step
// lists should be complete and the branch steps should depend on this step.
func (lr *LocalRunner) runConditionalStep(_ context.Context, plan *Plan, step *Step) (*StepResult, error) {
	selected, branchKey := evalCondition(step.Condition, plan, step)

	var skipped []string
	if _, ok := step.Branches[branchKey]; ok {
		kept := make(map[string]bool)
		for _, id := range step.Branches[branchKey] {
			kept[id] = true
		}
		for key, ids := range step.Branches {
			if key == branchKey {
				continue
			}
			for _, id := range ids {
				if kept[id] {
					continue // listed under the selected branch too → keep
				}
				if s := findStep(plan, id); s != nil && s.Status == StepPending {
					s.Status = StepSkipped
					skipped = append(skipped, id)
				}
			}
		}
	}

	return &StepResult{
		Output:   fmt.Sprintf("condition → %v (branch %q)", selected, branchKey),
		Metadata: map[string]any{"branch": branchKey, "selected": selected, "skipped": skipped},
	}, nil
}

// runCollectStep aggregates the outputs of this step's completed dependencies
// (a fan-in / join — the counterpart to parallel). With a Config.Prompt and an
// LLM available it synthesizes them; otherwise it returns the labeled outputs.
func (lr *LocalRunner) runCollectStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	var b strings.Builder
	var tokens int
	var from []string
	for _, d := range dependencyResults(plan, step) {
		if d.Status == StepSkipped || d.Result == nil || d.Result.Output == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		label := d.Name
		if label == "" {
			label = d.ID
		}
		b.WriteString("## " + label + "\n" + d.Result.Output)
		tokens += d.Result.TokensUsed
		from = append(from, d.ID)
	}
	collected := b.String()

	if step.Config.Prompt != "" && (lr.llmClient != nil || lr.engine != nil) {
		res, err := lr.runPrompt(ctx, plan, step.Config.Prompt+"\n\n"+collected)
		if err != nil {
			return nil, err
		}
		res.TokensUsed += tokens
		if res.Metadata == nil {
			res.Metadata = map[string]any{}
		}
		res.Metadata["collected_from"] = from
		return res, nil
	}
	return &StepResult{Output: collected, TokensUsed: tokens, Metadata: map[string]any{"collected_from": from}}, nil
}

// runHumanReviewStep submits the content under review (its dependencies' outputs,
// else the plan input) to the wired reviewer. With no reviewer it auto-approves
// and passes the content through; a rejection fails the step.
func (lr *LocalRunner) runHumanReviewStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	var b strings.Builder
	for _, d := range dependencyResults(plan, step) {
		if d.Result != nil && d.Result.Output != "" {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			b.WriteString(d.Result.Output)
		}
	}
	content := b.String()
	if content == "" {
		content = plan.Input
	}

	if lr.reviewer == nil {
		return &StepResult{Output: content, Metadata: map[string]any{"review": "auto-approved (no reviewer configured)"}}, nil
	}
	approved, note, err := lr.reviewer(ctx, plan, step, content)
	if err != nil {
		return nil, err
	}
	if !approved {
		return nil, fmt.Errorf("human review rejected step %q: %s", step.ID, note)
	}
	return &StepResult{Output: content, Metadata: map[string]any{"review": "approved", "note": note}}, nil
}

// findStep returns the step with the given ID, or nil.
func findStep(plan *Plan, id string) *Step {
	for _, s := range plan.Steps {
		if s != nil && s.ID == id {
			return s
		}
	}
	return nil
}

// dependencyResults returns the dependency steps of step, in DependsOn order.
func dependencyResults(plan *Plan, step *Step) []*Step {
	var deps []*Step
	for _, id := range step.DependsOn {
		if d := findStep(plan, id); d != nil {
			deps = append(deps, d)
		}
	}
	return deps
}

// evalCondition evaluates cond against the plan/step and returns (matched,
// branchKey) where branchKey is "true" or "false". A nil condition is true.
func evalCondition(cond *Condition, plan *Plan, step *Step) (bool, string) {
	if cond == nil {
		return true, "true"
	}
	if compareValues(conditionOperand(cond.Field, plan, step), cond.Operator, cond.Value) {
		return true, "true"
	}
	return false, "false"
}

// conditionOperand resolves the left-hand value of a condition. "input" (or an
// empty field with a single dependency) reads the plan input or that dep's
// output; otherwise a field naming a step yields that step's output.
func conditionOperand(field string, plan *Plan, step *Step) string {
	if field == "" {
		if len(step.DependsOn) == 1 {
			if d := findStep(plan, step.DependsOn[0]); d != nil && d.Result != nil {
				return d.Result.Output
			}
		}
		return plan.Input
	}
	if field == "input" {
		return plan.Input
	}
	if d := findStep(plan, field); d != nil && d.Result != nil {
		return d.Result.Output
	}
	return ""
}

// compareValues applies a small comparison DSL used by conditional steps.
func compareValues(actual, op, value string) bool {
	switch strings.ToLower(strings.TrimSpace(op)) {
	case "eq", "==", "equals":
		return actual == value
	case "ne", "!=", "not_equals":
		return actual != value
	case "contains":
		return strings.Contains(actual, value)
	case "not_contains":
		return !strings.Contains(actual, value)
	case "empty":
		return strings.TrimSpace(actual) == ""
	case "not_empty":
		return strings.TrimSpace(actual) != ""
	case "gt", ">":
		return numericCompare(actual, value) > 0
	case "lt", "<":
		return numericCompare(actual, value) < 0
	case "gte", ">=":
		return numericCompare(actual, value) >= 0
	case "lte", "<=":
		return numericCompare(actual, value) <= 0
	default:
		a := strings.TrimSpace(strings.ToLower(actual))
		return a != "" && a != "false" && a != "0" && a != "no"
	}
}

// numericCompare compares two numeric strings, falling back to string order when
// either side is not a number.
func numericCompare(a, b string) int {
	fa, ea := strconv.ParseFloat(strings.TrimSpace(a), 64)
	fb, eb := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if ea != nil || eb != nil {
		return strings.Compare(a, b)
	}
	switch {
	case fa < fb:
		return -1
	case fa > fb:
		return 1
	default:
		return 0
	}
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
