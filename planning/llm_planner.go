package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// FormatToolsPrompt formats a slice of ToolInfo into a readable system prompt section.
func FormatToolsPrompt(tools []ToolInfo) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Available Tools\n\n")
	b.WriteString("Use these tools in `tool_call` steps. Reference them by exact `tool_name`.\n\n")
	for _, t := range tools {
		b.WriteString("## ")
		b.WriteString(t.Name)
		b.WriteString("\n")
		b.WriteString(t.Description)
		b.WriteString("\n")
		if t.Parameters != nil {
			if params, err := json.Marshal(t.Parameters); err == nil {
				b.WriteString("Parameters: ")
				b.Write(params)
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// LLMPlanner uses an LLM to generate structured execution plans from the user message and available tools.
type LLMPlanner struct {
	client   model.LLMClient
	model    string
	maxSteps int
}

// NewLLMPlanner returns a new LLMPlanner.
func NewLLMPlanner(client model.LLMClient, modelName string) *LLMPlanner {
	return &LLMPlanner{
		client:   client,
		model:    modelName,
		maxSteps: 20,
	}
}

// SetMaxSteps sets the maximum number of steps to allow in a generated plan.
func (p *LLMPlanner) SetMaxSteps(n int) { p.maxSteps = n }

// CreatePlan asks the LLM to produce a JSON execution plan and parses it into a Plan.
func (p *LLMPlanner) CreatePlan(ctx context.Context, input PlanInput) (*Plan, error) {
	prompts := p.buildSystemPrompts(input)
	var messages []openai.ChatCompletionMessage
	for _, pr := range prompts {
		messages = append(messages, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleSystem, Content: pr,
		})
	}
	if input.Context != nil && len(input.Context.SessionHistory) > 0 {
		messages = append(messages, input.Context.SessionHistory...)
	}
	messages = append(messages, openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser, Content: input.Message,
	})

	resp, err := p.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: p.model, Messages: messages,
	})
	if err != nil {
		return nil, fmt.Errorf("planning: llm create plan: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("planning: empty llm response")
	}
	plan, err := p.parsePlanJSON(resp.Choices[0].Message.Content, input)
	if err != nil {
		return nil, fmt.Errorf("planning: parse plan: %w", err)
	}
	plan.SystemPrompts = prompts
	plan.Status = PlanPending
	plan.CreatedAt = time.Now()
	plan.UpdatedAt = plan.CreatedAt
	plan.Version = 1
	return plan, nil
}

// Replan sends the current plan and last step result to the LLM and parses an updated plan.
func (p *LLMPlanner) Replan(ctx context.Context, plan *Plan, lastStep *Step) (*Plan, error) {
	replanInput := PlanInput{
		UserID:    plan.UserID,
		SessionID: plan.SessionID,
		Message:   plan.Input,
	}
	prompts := p.buildSystemPrompts(replanInput)
	var messages []openai.ChatCompletionMessage
	for _, pr := range prompts {
		messages = append(messages, openai.ChatCompletionMessage{
			Role: openai.ChatMessageRoleSystem, Content: pr,
		})
	}
	lastOutput := ""
	if lastStep.Result != nil {
		lastOutput = lastStep.Result.Output
	}
	feedback := fmt.Sprintf("Previous plan (ID: %s). Last step completed: %s (result: %s). Adjust remaining steps or provide updated full plan as JSON.",
		plan.ID, lastStep.ID, lastOutput)
	messages = append(messages, openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleUser, Content: feedback,
	})

	resp, err := p.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: p.model, Messages: messages,
	})
	if err != nil {
		return nil, fmt.Errorf("planning: llm replan: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("planning: empty llm response")
	}
	newPlan, err := p.parsePlanJSON(resp.Choices[0].Message.Content, replanInput)
	if err != nil {
		return nil, err
	}
	newPlan.ID = plan.ID
	newPlan.Status = PlanPending
	newPlan.Version = plan.Version + 1
	newPlan.CreatedAt = plan.CreatedAt
	newPlan.UpdatedAt = time.Now()
	return newPlan, nil
}

func (p *LLMPlanner) buildSystemPrompts(input PlanInput) []string {
	prompts := []string{PlannerPrompt()}
	if input.Context != nil {
		if len(input.Context.AvailableTools) > 0 {
			prompts = append(prompts, FormatToolsPrompt(input.Context.AvailableTools))
		}
		for _, sp := range input.Context.SystemPrompts {
			if sp != "" {
				prompts = append(prompts, sp)
			}
		}
	}
	return prompts
}

// llmPlanJSON is the shape we expect from the LLM for parsing.
type llmPlanJSON struct {
	Steps []struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Config      struct {
			Prompt     string         `json:"prompt"`
			ToolName   string         `json:"tool_name"`
			ToolArgs   map[string]any `json:"tool_args"`
			AgentInput string         `json:"agent_input"`
		} `json:"config"`
		DependsOn []string `json:"depends_on"`
	} `json:"steps"`
}

func (p *LLMPlanner) parsePlanJSON(content string, input PlanInput) (*Plan, error) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "```") {
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimPrefix(content, "json")
		content = strings.TrimSpace(content)
		if idx := strings.Index(content, "```"); idx >= 0 {
			content = content[:idx]
		}
		content = strings.TrimSpace(content)
	}
	var parsed llmPlanJSON
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, err
	}
	planID := fmt.Sprintf("plan-%d", time.Now().UnixNano())
	now := time.Now()
	plan := &Plan{
		ID:        planID,
		UserID:    input.UserID,
		SessionID: input.SessionID,
		Input:     input.Message,
		Steps:     make([]*Step, 0, len(parsed.Steps)),
		Status:    PlanPending,
		CreatedAt: now,
		UpdatedAt: now,
		Version:   1,
	}
	for i, s := range parsed.Steps {
		stepType := StepType(s.Type)
		if stepType == "" {
			stepType = StepLLMCall
		}
		// Downgrade tool_call with missing tool_name to llm_call so the runner
		// doesn't choke on a blank tool_name from a bad LLM response.
		if stepType == StepToolCall && s.Config.ToolName == "" {
			stepType = StepLLMCall
			if s.Config.Prompt == "" {
				s.Config.Prompt = s.Name + ": " + s.Description
			}
		}
		id := s.ID
		if id == "" {
			id = fmt.Sprintf("step-%d", i+1)
		}
		step := &Step{
			ID:          id,
			Type:        stepType,
			Name:        s.Name,
			Description: s.Description,
			Config: StepConfig{
				Prompt:     s.Config.Prompt,
				ToolName:   s.Config.ToolName,
				ToolArgs:   s.Config.ToolArgs,
				AgentInput: s.Config.AgentInput,
			},
			DependsOn: s.DependsOn,
			Status:    StepPending,
		}
		plan.Steps = append(plan.Steps, step)
	}
	if len(plan.Steps) > p.maxSteps {
		plan.Steps = plan.Steps[:p.maxSteps]
	}
	return plan, nil
}
