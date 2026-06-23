package planning

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/log"
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

// PersistErrorFunc is invoked when persisting plan state to the store fails.
// Wire it with WithLocalPersistErrorHandler so host apps can halt or alert when
// in-memory and persisted plan state start to diverge.
type PersistErrorFunc func(plan *Plan, err error)

// cancelRegistry tracks the cancel function of each running plan so Cancel can
// interrupt in-flight steps. It is shared by pointer across the per-run shallow
// copies of LocalRunner.
type cancelRegistry struct {
	mu sync.Mutex
	m  map[string]context.CancelFunc
}

func newCancelRegistry() *cancelRegistry {
	return &cancelRegistry{m: make(map[string]context.CancelFunc)}
}

func (r *cancelRegistry) add(id string, fn context.CancelFunc) {
	r.mu.Lock()
	r.m[id] = fn
	r.mu.Unlock()
}

func (r *cancelRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.m, id)
	r.mu.Unlock()
}

// cancel signals the running plan with the given ID, if any, and reports
// whether a running plan was found.
func (r *cancelRegistry) cancel(id string) bool {
	r.mu.Lock()
	fn, ok := r.m[id]
	delete(r.m, id)
	r.mu.Unlock()
	if ok {
		fn()
	}
	return ok
}

// LocalRunner runs plans in-process using the Engine and FunctionRegistry.
type LocalRunner struct {
	engine         LLMCaller
	llmClient      model.LLMClient
	llmModel       string
	functions      ToolExecutor
	callback       engine.Callback
	store          PlanStore
	observer       Observer
	reviewer       ReviewFunc
	logger         *log.Logger
	onPersistError PersistErrorFunc
	cancels        *cancelRegistry
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

// WithLocalLogger sets the logger for plan/step transitions and store failures
// (default: log.Log).
func WithLocalLogger(l *log.Logger) LocalRunnerOption {
	return func(lr *LocalRunner) {
		if l != nil {
			lr.logger = l
		}
	}
}

// WithLocalPersistErrorHandler wires a callback invoked whenever persisting
// plan state to the store fails. Store failures are also logged.
func WithLocalPersistErrorHandler(fn PersistErrorFunc) LocalRunnerOption {
	return func(lr *LocalRunner) { lr.onPersistError = fn }
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
	lr := &LocalRunner{
		engine:    eng,
		functions: funcs,
		logger:    log.Log,
		cancels:   newCancelRegistry(),
	}
	for _, opt := range opts {
		opt(lr)
	}
	if lr.logger == nil {
		lr.logger = log.Log
	}
	return lr
}

// persist writes the plan to the store, logging and reporting failures via the
// persist-error handler instead of dropping them silently.
func (lr *LocalRunner) persist(ctx context.Context, plan *Plan) {
	if lr.store == nil {
		return
	}
	// Persist even when the run context was cancelled or timed out — the final
	// state transition (failed/cancelled) must still reach the store.
	ctx = context.WithoutCancel(ctx)
	if err := lr.store.Update(ctx, plan); err != nil {
		lr.logger.Warnf("planning: persist plan %s failed: %v", plan.ID, err)
		if lr.onPersistError != nil {
			lr.onPersistError(plan, err)
		}
	}
}

// failPlan applies a plan-level failure (or cancellation): all mutations first,
// one store update, one OnPlanFailed notification.
func (lr *LocalRunner) failPlan(ctx context.Context, plan *Plan, status PlanStatus, err error) error {
	now := time.Now()
	plan.Status = status
	plan.Error = err.Error()
	plan.UpdatedAt = now
	plan.CompletedAt = &now
	lr.persist(ctx, plan)
	lr.logger.Errorf("planning: plan %s %s: %v", plan.ID, status, err)
	if lr.observer != nil {
		lr.observer.OnPlanFailed(plan, err)
	}
	return err
}

// failStep applies a step failure and the resulting plan failure as one
// coherent transition: all mutations first, a single store update, then exactly
// one OnStepFailed (step-specific) and one OnPlanFailed (plan-level) event.
func (lr *LocalRunner) failStep(ctx context.Context, plan *Plan, step *Step, err error) error {
	now := time.Now()
	step.Status = StepFailed
	step.Error = err.Error()
	step.CompletedAt = &now
	plan.Status = PlanFailed
	plan.Error = err.Error()
	plan.UpdatedAt = now
	plan.CompletedAt = &now
	lr.persist(ctx, plan)
	lr.logger.Errorf("planning: plan %s step %s failed: %v", plan.ID, step.ID, err)
	if lr.observer != nil {
		lr.observer.OnStepFailed(plan, step, err)
		lr.observer.OnPlanFailed(plan, err)
	}
	return fmt.Errorf("step %q failed: %w", step.ID, err)
}

// Run executes the plan: validation, then run ready steps until all done.
func (lr *LocalRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	cfg := &runConfig{}
	for _, o := range opts {
		o(cfg)
	}

	// Resolve run-level overrides into a shallow copy so concurrent runs with
	// different clients/executors/stores/observers don't interfere, and so
	// RunStep (and parallel sub-steps) see the same store/observer as Run.
	cp := *lr
	if cfg.llmClient != nil && lr.llmClient == nil {
		cp.llmClient = cfg.llmClient
		cp.llmModel = cfg.llmModel
	}
	if cfg.toolExecutor != nil {
		cp.functions = cfg.toolExecutor
	}
	if cfg.store != nil {
		cp.store = cfg.store
	}
	if cfg.observer != nil {
		cp.observer = cfg.observer
	}
	rl := &cp

	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}

	// Register a cancel func so Cancel(planID) interrupts in-flight steps.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	if plan.ID != "" {
		lr.cancels.add(plan.ID, cancelRun)
		defer lr.cancels.remove(plan.ID)
	}
	ctx = runCtx

	plan.Status = PlanRunning
	plan.UpdatedAt = time.Now()
	rl.persist(ctx, plan)
	rl.logger.Infof("planning: plan %s started (%d steps)", plan.ID, len(plan.Steps))
	if rl.observer != nil {
		rl.observer.OnPlanCreated(plan)
	}

	if err := ValidatePlan(plan); err != nil {
		return nil, rl.failPlan(ctx, plan, PlanFailed, err)
	}

	start := time.Now()

	// An empty plan trivially succeeds.
	if len(plan.Steps) == 0 {
		return rl.completePlan(ctx, plan, "", 0, start), nil
	}

	var totalTokens int
	var finalOutput string

	for {
		select {
		case <-ctx.Done():
			return nil, rl.failPlan(ctx, plan, PlanCancelled, ctx.Err())
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
			return nil, rl.failPlan(ctx, plan, PlanFailed,
				fmt.Errorf("%w: no ready steps and plan not complete", ErrStepFailed))
		}

		for _, step := range ready {
			// A step earlier in this batch (e.g. a conditional) may have skipped
			// this one; respect that rather than running it anyway.
			if step.Status != StepPending {
				continue
			}
			result, err := rl.RunStep(ctx, plan, step)
			if err != nil {
				// A step aborted because the plan context ended (Cancel or
				// plan-level timeout) is a cancelled plan, not a failed one.
				if ctx.Err() != nil {
					return nil, rl.failPlan(ctx, plan, PlanCancelled, ctx.Err())
				}
				return nil, rl.failStep(ctx, plan, step, err)
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
			rl.persist(ctx, plan)
			rl.logger.Debugf("planning: plan %s step %s completed", plan.ID, step.ID)
			if rl.observer != nil {
				rl.observer.OnStepCompleted(plan, step, result)
			}
		}
	}

	return rl.completePlan(ctx, plan, finalOutput, totalTokens, start), nil
}

// completePlan applies the completed transition, persists it once and emits
// OnPlanCompleted.
func (lr *LocalRunner) completePlan(ctx context.Context, plan *Plan, output string, totalTokens int, start time.Time) *PlanResult {
	plan.Status = PlanCompleted
	plan.Output = output
	now := time.Now()
	plan.CompletedAt = &now
	plan.UpdatedAt = now
	lr.persist(ctx, plan)
	lr.logger.Infof("planning: plan %s completed in %s", plan.ID, time.Since(start).Round(time.Millisecond))

	result := &PlanResult{
		PlanID:      plan.ID,
		Output:      output,
		Steps:       plan.Steps,
		TotalTokens: totalTokens,
		Duration:    time.Since(start),
	}
	if lr.observer != nil {
		lr.observer.OnPlanCompleted(plan, result)
	}
	return result
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

	lr.logger.Debugf("planning: plan %s step %s started (%s)", plan.ID, step.ID, step.Type)
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
		result, err = lr.dispatchWithDeadline(stepCtx, plan, step)
		if err == nil || stepCtx.Err() != nil || attempt == attempts-1 {
			break
		}
		lr.logger.Warnf("planning: plan %s step %s attempt %d/%d failed, retrying: %v",
			plan.ID, step.ID, attempt+1, attempts, err)
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

// dispatchWithDeadline runs dispatchStep in a goroutine and returns as soon as
// the step context ends, even when the underlying engine or tool ignores the
// context. The abandoned attempt keeps running in the background until it
// returns on its own (goroutines cannot be forcibly stopped); the plan moves on
// regardless. See StepConfig.Timeout for the trade-off.
func (lr *LocalRunner) dispatchWithDeadline(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	type outcome struct {
		res *StepResult
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		res, err := lr.dispatchStep(ctx, plan, step)
		ch <- outcome{res: res, err: err}
	}()
	select {
	case o := <-ch:
		return o.res, o.err
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("step %q: %w", step.ID, ErrStepTimeout)
		}
		return nil, ctx.Err()
	}
}

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
	// Feed the step the outputs of the steps it depends on, so a dependent
	// llm_call can actually act on upstream data (e.g. summarize a prior
	// tool_call's result). Without this an llm_call only ever sees its own
	// prompt plus the original user input — never its dependencies' outputs —
	// which silently drops data across the DAG. collect remains the right
	// choice for a pure fan-in/join; this just makes a dependent llm_call
	// behave the way a plan author naturally expects.
	deps, _, _, missing := collectDependencyOutputs(plan, step)
	if len(missing) > 0 {
		lr.logger.Warnf("planning: plan %s llm_call step %s: dependencies contributed no output: %v",
			plan.ID, step.ID, missing)
	}
	if deps != "" {
		prompt += "\n\n" + deps
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
// Dependencies that contributed no output (skipped, or nil/empty result) are
// reported under the "missing_inputs" metadata key and logged.
func (lr *LocalRunner) runCollectStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	collected, tokens, from, missing := collectDependencyOutputs(plan, step)
	if len(missing) > 0 {
		lr.logger.Warnf("planning: plan %s collect step %s: dependencies contributed no output: %v",
			plan.ID, step.ID, missing)
	}
	meta := map[string]any{"collected_from": from}
	if len(missing) > 0 {
		meta["missing_inputs"] = missing
	}

	if step.Config.Prompt != "" && (lr.llmClient != nil || lr.engine != nil) {
		res, err := lr.runPrompt(ctx, plan, step.Config.Prompt+"\n\n"+collected)
		if err != nil {
			return nil, err
		}
		res.TokensUsed += tokens
		if res.Metadata == nil {
			res.Metadata = map[string]any{}
		}
		for k, v := range meta {
			res.Metadata[k] = v
		}
		return res, nil
	}
	return &StepResult{Output: collected, TokensUsed: tokens, Metadata: meta}, nil
}

// runHumanReviewStep submits the content under review (its dependencies' outputs,
// else the plan input) to the wired reviewer. With no reviewer it auto-approves
// and passes the content through; a rejection fails the step. Dependencies with
// no output are reported under the "missing_inputs" metadata key and logged.
func (lr *LocalRunner) runHumanReviewStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	var b strings.Builder
	var missing []string
	for _, d := range dependencyResults(plan, step) {
		if d.Result == nil || d.Result.Output == "" {
			missing = append(missing, d.ID)
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(d.Result.Output)
	}
	content := b.String()
	if content == "" {
		content = plan.Input
	}
	if len(missing) > 0 {
		lr.logger.Warnf("planning: plan %s human_review step %s: dependencies contributed no output: %v",
			plan.ID, step.ID, missing)
	}
	meta := map[string]any{}
	if len(missing) > 0 {
		meta["missing_inputs"] = missing
	}

	if lr.reviewer == nil {
		meta["review"] = "auto-approved (no reviewer configured)"
		return &StepResult{Output: content, Metadata: meta}, nil
	}
	approved, note, err := lr.reviewer(ctx, plan, step, content)
	if err != nil {
		return nil, err
	}
	if !approved {
		return nil, fmt.Errorf("human review rejected step %q: %s", step.ID, note)
	}
	meta["review"] = "approved"
	meta["note"] = note
	return &StepResult{Output: content, Metadata: meta}, nil
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

// collectDependencyOutputs concatenates the outputs of step's completed
// dependencies into one labeled block ("## <name>\n<output>", with a blank line
// between entries), in DependsOn order. It returns that block, the sum of the
// dependencies' reported tokens, the IDs that contributed, and the IDs that
// were skipped or produced no output. Shared by collect and llm_call so a step
// can actually see the data it depends on.
func collectDependencyOutputs(plan *Plan, step *Step) (text string, tokens int, from, missing []string) {
	var b strings.Builder
	for _, d := range dependencyResults(plan, step) {
		if d.Status == StepSkipped || d.Result == nil || d.Result.Output == "" {
			missing = append(missing, d.ID)
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
	return b.String(), tokens, from, missing
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
// Unknown operators are rejected up front by ValidatePlan; the empty operator
// and "truthy" (and, defensively, anything unknown) fall back to a truthiness
// check on the operand.
func compareValues(actual, op, value string) bool {
	switch normalizeOperator(op) {
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

// runParallelStep runs the sub-steps concurrently and waits for ALL of them to
// finish (no early return — no goroutine is left writing into shared state
// after this function returns). Conditional sub-steps are rejected because they
// mutate sibling step statuses concurrently. On failure every failed sub-step
// is marked failed and reported to the observer individually, and the returned
// error names each failed sub-step.
func (lr *LocalRunner) runParallelStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	subs := step.Config.SubSteps
	if len(subs) == 0 {
		return &StepResult{}, nil
	}
	for _, s := range subs {
		if s != nil && s.Type == StepConditional {
			return nil, fmt.Errorf("%w: step %q: conditional inside parallel is not supported", ErrInvalidStep, s.ID)
		}
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
	var failures []error
	now := time.Now()
	for i, e := range errs {
		sub := subs[i]
		if e != nil {
			sub.Status = StepFailed
			sub.Error = e.Error()
			sub.CompletedAt = &now
			lr.logger.Errorf("planning: plan %s parallel step %s sub-step %s failed: %v",
				plan.ID, step.ID, sub.ID, e)
			if lr.observer != nil {
				lr.observer.OnStepFailed(plan, sub, e)
			}
			failures = append(failures, fmt.Errorf("sub-step %q: %w", sub.ID, e))
			continue
		}
		sub.Status = StepCompleted
		sub.Result = results[i]
		sub.CompletedAt = &now
		if lr.observer != nil {
			lr.observer.OnStepCompleted(plan, sub, results[i])
		}
		if results[i] != nil {
			combined += results[i].Output
			totalTokens += results[i].TokensUsed
		}
	}
	if len(failures) > 0 {
		return nil, fmt.Errorf("parallel step %q: %w", step.ID, errors.Join(failures...))
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

// Cancel interrupts the plan if it is currently running (its context is
// cancelled, aborting in-flight steps) and marks it cancelled in the store.
func (lr *LocalRunner) Cancel(ctx context.Context, planID string) error {
	signalled := lr.cancels.cancel(planID)
	if signalled {
		lr.logger.Infof("planning: plan %s cancel signalled", planID)
	}
	store := lr.store
	if store == nil {
		if signalled {
			return nil // the running plan will persist its cancelled state
		}
		return ErrNoPlanStore
	}
	plan, err := store.Get(ctx, planID)
	if err != nil {
		if signalled {
			return nil
		}
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
	lr.logger.Infof("planning: plan %s resuming", planID)
	plan.Status = PlanRunning
	plan.UpdatedAt = time.Now()
	lr.persist(ctx, plan)
	return lr.Run(ctx, plan, WithStore(store), WithObserver(lr.observer))
}
