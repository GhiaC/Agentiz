package planning

import (
	_ "embed"
)

//go:embed prompt.md
var corePromptMD []byte

//go:embed planner_prompt.md
var plannerPromptMD []byte

// CorePrompt returns the planning decision-making prompt for Core.
// When this is injected into Core's system prompt and execute_plan is available,
// the Core agent can decide when to use planning vs direct agent calls.
func CorePrompt() string {
	return string(corePromptMD)
}

// PlannerPrompt returns the system prompt that teaches the LLM planner
// how to build DAG workflows from a user request and available tools.
func PlannerPrompt() string {
	return string(plannerPromptMD)
}
