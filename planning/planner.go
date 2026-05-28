package planning

import (
	"context"

	"github.com/sashabaranov/go-openai"
)

// ToolInfo describes an available tool for the planner.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters,omitempty"`
}

// PlanContext provides context for plan creation (tools, history, previous plan).
type PlanContext struct {
	AvailableTools []ToolInfo
	SessionHistory []openai.ChatCompletionMessage
	SystemPrompts  []string
	PreviousPlan   *Plan
	ToolExecutor   ToolExecutor
}

// PlanInput is the input to CreatePlan.
type PlanInput struct {
	UserID    string
	SessionID string
	Message   string
	Context   *PlanContext
	// Observer, when set, is merged with the orchestrator's observer for this run (e.g. to report status to a specific message).
	Observer Observer
}

// Planner creates and optionally revises execution plans.
type Planner interface {
	CreatePlan(ctx context.Context, input PlanInput) (*Plan, error)
	Replan(ctx context.Context, plan *Plan, lastStep *Step) (*Plan, error)
}
