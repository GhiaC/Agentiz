package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/planning"
	"github.com/sashabaranov/go-openai"
)

// planStatusObserver sends plan progress to the client by updating the given message (messageID) via NotifyStatus.
type planStatusObserver struct {
	ch          *CoreHandler
	ctx         context.Context
	userID      string
	sessionID   string
	messageID   string
	lastMessage string // user request
}

func (o *planStatusObserver) OnPlanCreated(plan *planning.Plan) {
	report := formatPlanStatusReport(plan, nil, nil, "", o.lastMessage, "created")
	engine.NotifyStatus(o.ctx, o.userID, o.sessionID, engine.StatusCustom, report, engine.OptMessageID(o.messageID))
}

func (o *planStatusObserver) OnStepStarted(plan *planning.Plan, step *planning.Step) {
	report := formatPlanStatusReport(plan, step, nil, "", o.lastMessage, "step_started")
	engine.NotifyStatus(o.ctx, o.userID, o.sessionID, engine.StatusCustom, report, engine.OptMessageID(o.messageID))
}

func (o *planStatusObserver) OnStepCompleted(plan *planning.Plan, step *planning.Step, result *planning.StepResult) {
	report := formatPlanStatusReport(plan, step, result, "", o.lastMessage, "step_completed")
	engine.NotifyStatus(o.ctx, o.userID, o.sessionID, engine.StatusCustom, report, engine.OptMessageID(o.messageID))
}

func (o *planStatusObserver) OnStepFailed(plan *planning.Plan, step *planning.Step, err error) {
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	report := formatPlanStatusReport(plan, step, nil, errStr, o.lastMessage, "step_failed")
	engine.NotifyStatus(o.ctx, o.userID, o.sessionID, engine.StatusCustom, report, engine.OptMessageID(o.messageID))
}

func (o *planStatusObserver) OnPlanCompleted(plan *planning.Plan, result *planning.PlanResult) {
	report := formatPlanStatusReportFromResult(plan, result, o.lastMessage, "completed")
	engine.NotifyStatus(o.ctx, o.userID, o.sessionID, engine.StatusCustom, report, engine.OptMessageID(o.messageID))
}

func (o *planStatusObserver) OnPlanFailed(plan *planning.Plan, err error) {
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	report := formatPlanStatusReport(plan, nil, nil, errStr, o.lastMessage, "failed")
	engine.NotifyStatus(o.ctx, o.userID, o.sessionID, engine.StatusCustom, report, engine.OptMessageID(o.messageID))
}

func formatPlanStatusReport(plan *planning.Plan, step *planning.Step, result *planning.StepResult, errStr, lastMessage, event string) string {
	var b strings.Builder
	b.WriteString("Plan: " + plan.ID + " | Status: " + string(plan.Status))
	if event != "" {
		b.WriteString(" | Event: " + event)
	}
	if lastMessage != "" {
		if len(lastMessage) > 80 {
			b.WriteString(" | Request: " + lastMessage[:77] + "...")
		} else {
			b.WriteString(" | Request: " + lastMessage)
		}
	}
	completed := 0
	for _, s := range plan.Steps {
		if s.Status == planning.StepCompleted {
			completed++
		}
	}
	b.WriteString(fmt.Sprintf(" | Steps: %d/%d", completed, len(plan.Steps)))
	if step != nil {
		b.WriteString(" | Current: " + step.Name + " (" + string(step.Status) + ")")
		if result != nil && result.Output != "" {
			out := result.Output
			if len(out) > 120 {
				out = out[:117] + "..."
			}
			b.WriteString(" | Last output: " + out)
		}
	}
	if errStr != "" {
		b.WriteString(" | Error: " + errStr)
	}
	return b.String()
}

func formatPlanStatusReportFromResult(plan *planning.Plan, result *planning.PlanResult, lastMessage, event string) string {
	var b strings.Builder
	b.WriteString("Plan: " + plan.ID + " | Status: " + string(plan.Status) + " | Event: " + event)
	if lastMessage != "" {
		if len(lastMessage) > 80 {
			b.WriteString(" | Request: " + lastMessage[:77] + "...")
		} else {
			b.WriteString(" | Request: " + lastMessage)
		}
	}
	if result != nil && result.Output != "" {
		out := result.Output
		if len(out) > 200 {
			out = out[:197] + "..."
		}
		b.WriteString(" | Final output: " + out)
	}
	return b.String()
}

func (ch *CoreHandler) executePlanTool(ctx context.Context, userID, sessionID, messageID string, args map[string]interface{}) (string, error) {
	if ch.orchestrator == nil {
		return "", fmt.Errorf("planning not enabled")
	}
	message, _ := args["message"].(string)
	if message == "" {
		return "", fmt.Errorf("message is required for execute_plan")
	}
	input := planning.PlanInput{
		UserID:    userID,
		SessionID: sessionID,
		Message:   message,
		Context: &planning.PlanContext{
			AvailableTools: ch.buildPlanToolInfos(),
			ToolExecutor:   &planToolExecutor{ch: ch, ctx: ctx, userID: userID, sessionID: sessionID, messageID: messageID},
		},
		Observer: &planStatusObserver{ch: ch, ctx: ctx, userID: userID, sessionID: sessionID, messageID: messageID, lastMessage: message},
	}
	result, err := ch.orchestrator.Execute(ctx, input)
	metrics.PlanRun(metrics.Status(err))
	if result != nil {
		// Record each executed step with its real status instead of a blanket
		// "ok"; steps that never ran (pending/skipped) are not counted.
		for _, s := range result.Steps {
			switch s.Status {
			case planning.StepCompleted:
				metrics.PlanStep("ok")
			case planning.StepFailed:
				metrics.PlanStep("error")
			}
		}
	}
	if err != nil {
		return "", fmt.Errorf("execute_plan: %w", err)
	}
	if result != nil && result.Output != "" {
		return result.Output, nil
	}
	if result != nil && len(result.Steps) > 0 {
		if last := result.Steps[len(result.Steps)-1]; last.Result != nil && last.Result.Output != "" {
			return last.Result.Output, nil
		}
	}
	return "", nil
}

// buildPlanToolInfos converts Core's OpenAI tool definitions to planning.ToolInfo
// so the LLM planner knows what tools are available when designing workflows.
func (ch *CoreHandler) buildPlanToolInfos() []planning.ToolInfo {
	coreTools := ch.getCoreToolsForLLM()
	infos := make([]planning.ToolInfo, 0, len(coreTools))
	for _, t := range coreTools {
		if t.Function == nil {
			continue
		}
		infos = append(infos, planning.ToolInfo{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}
	return infos
}

// planToolExecutor routes plan tool_call steps back to CoreHandler.runCoreToolImpl.
type planToolExecutor struct {
	ch        *CoreHandler
	ctx       context.Context
	userID    string
	sessionID string
	messageID string
}

func (e *planToolExecutor) Execute(toolName string, args map[string]interface{}) (string, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("plan tool executor: marshal args: %w", err)
	}
	toolCall := openai.ToolCall{
		Function: openai.FunctionCall{Name: toolName, Arguments: string(argsJSON)},
	}
	return e.ch.runCoreToolImpl(e.ctx, e.userID, e.sessionID, e.messageID, toolCall)
}

func (ch *CoreHandler) getPlanStatusTool(ctx context.Context, args map[string]interface{}) (string, error) {
	if ch.orchestrator == nil {
		return "", fmt.Errorf("planning not enabled")
	}
	planID, _ := args["plan_id"].(string)
	if planID == "" {
		return "", fmt.Errorf("plan_id is required for get_plan_status")
	}
	plan, err := ch.orchestrator.GetPlan(ctx, planID)
	if err != nil {
		return "", fmt.Errorf("get_plan_status: %w", err)
	}
	completed := 0
	for _, s := range plan.Steps {
		if s.Status == planning.StepCompleted {
			completed++
		}
	}
	report := fmt.Sprintf("Plan %s: status=%s, steps %d/%d", plan.ID, plan.Status, completed, len(plan.Steps))
	if plan.Output != "" {
		report += " | output: " + plan.Output
	}
	if plan.Error != "" {
		report += " | error: " + plan.Error
	}
	return report, nil
}

func (ch *CoreHandler) cancelPlanTool(ctx context.Context, args map[string]interface{}) (string, error) {
	if ch.orchestrator == nil {
		return "", fmt.Errorf("planning not enabled")
	}
	planID, _ := args["plan_id"].(string)
	if planID == "" {
		return "", fmt.Errorf("plan_id is required for cancel_plan")
	}
	if err := ch.orchestrator.Cancel(ctx, planID); err != nil {
		return "", fmt.Errorf("cancel_plan: %w", err)
	}
	return "Plan " + planID + " cancelled.", nil
}
