package planning

import (
	"context"
	"fmt"
	"time"
)

// ChainPlanner creates simple sequential plans (LLM step then optional tool step).
type ChainPlanner struct{}

// NewChainPlanner returns a new ChainPlanner.
func NewChainPlanner() *ChainPlanner {
	return &ChainPlanner{}
}

// CreatePlan builds a plan with one LLM step and optionally a tool step if context has tools.
func (c *ChainPlanner) CreatePlan(ctx context.Context, input PlanInput) (*Plan, error) {
	planID := fmt.Sprintf("plan-%d", time.Now().UnixNano())
	now := time.Now()
	step1 := &Step{
		ID:          "step-1",
		Type:        StepLLMCall,
		Name:        "LLM",
		Description: "Process user message",
		Config: StepConfig{
			Prompt: input.Message,
		},
		DependsOn: nil,
		Status:    StepPending,
	}
	steps := []*Step{step1}
	if input.Context != nil && len(input.Context.AvailableTools) > 0 {
		step2 := &Step{
			ID:          "step-2",
			Type:        StepToolCall,
			Name:        "Tool",
			Description: "Optional tool call",
			Config:      StepConfig{ToolName: input.Context.AvailableTools[0].Name},
			DependsOn:   []string{step1.ID},
			Status:      StepPending,
		}
		steps = append(steps, step2)
	}
	plan := &Plan{
		ID:        planID,
		UserID:    input.UserID,
		SessionID: input.SessionID,
		Input:     input.Message,
		Steps:     steps,
		Status:    PlanPending,
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}
	return plan, nil
}

// Replan implements retry semantics: it resets the failed step and the plan to
// pending so the runner can resume from the point of failure. The plan version
// is bumped so observers can tell the attempts apart.
func (c *ChainPlanner) Replan(ctx context.Context, plan *Plan, lastStep *Step) (*Plan, error) {
	if lastStep != nil && lastStep.Status == StepFailed {
		lastStep.Status = StepPending
		lastStep.Error = ""
		lastStep.Result = nil
		lastStep.StartedAt = nil
		lastStep.CompletedAt = nil
	}
	plan.Status = PlanPending
	plan.Error = ""
	plan.CompletedAt = nil
	plan.Version++
	plan.UpdatedAt = time.Now()
	return plan, nil
}
