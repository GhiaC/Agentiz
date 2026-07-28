package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ghiac/agentize/engine"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

const (
	maxWorkflowNameLength   = 120
	maxWorkflowTaskOutput   = 64 * 1024
	workflowSummaryOutput   = 2 * 1024
	workflowStatusTaskLimit = 50
)

var workflowOutputReference = regexp.MustCompile(
	`\{\{tasks\.([A-Za-z0-9][A-Za-z0-9_-]{0,63})\.output\}\}`,
)

type workflowPersistence interface {
	PutWorkflowRun(*model.WorkflowRun) error
	GetWorkflowRun(string) (*model.WorkflowRun, error)
	ListWorkflowRuns(string, int) ([]*model.WorkflowRun, error)
}

type workflowToolInput struct {
	Name  string                `json:"name"`
	Tasks []*model.WorkflowTask `json:"tasks"`
}

func workflowCoreToolDefinitions(includeSchedule bool) []openai.Tool {
	taskSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type":        "string",
				"description": "Unique stable task id (letters, numbers, underscore, dash; max 64).",
			},
			"name": map[string]interface{}{
				"type":        "string",
				"description": "Short human-readable task name.",
			},
			"tool": map[string]interface{}{
				"type":        "string",
				"description": "Exact Core tool name to invoke.",
			},
			"arguments": map[string]interface{}{
				"type":        "object",
				"description": "Exact tool arguments. A dependent task may use {{tasks.<id>.output}} in string values.",
			},
			"depends_on": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Task IDs that must succeed before this task runs.",
			},
		},
		"required":             []string{"id", "name", "tool", "arguments"},
		"additionalProperties": false,
	}
	tools := []openai.Tool{
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "execute_workflow",
				Description: "Execute a deterministic DAG of exact Core tool calls. Use only when the user has " +
					"already specified multiple concrete actions or dependencies; do not invent a plan. " +
					"No planner LLM is called. Every task invocation still requests its own human approval.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]interface{}{
							"type":        "string",
							"description": "Short workflow name.",
						},
						"tasks": map[string]interface{}{
							"type": "array", "items": taskSchema,
							"minItems": 1, "maxItems": model.MaxWorkflowTasks,
						},
					},
					"required":             []string{"name", "tasks"},
					"additionalProperties": false,
				},
			},
		},
		{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "get_workflow_status",
				Description: "Read the durable status and bounded task outputs of one workflow owned by the current user.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"workflow_id": map[string]interface{}{"type": "string"},
					},
					"required":             []string{"workflow_id"},
					"additionalProperties": false,
				},
			},
		},
	}
	if includeSchedule {
		tools = append(tools, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "create_workflow_schedule",
				Description: "Create a persistent deterministic workflow schedule in its own memory/session. " +
					"The exact DAG is approved once when this tool is called; later scheduled runs execute that " +
					"unchanged state machine without a planner LLM and without per-task approval. It never routes " +
					"to or switches between worker agents. Set max_runs=1 for a one-shot workflow.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]interface{}{"type": "string"},
						"tasks": map[string]interface{}{
							"type": "array", "items": taskSchema,
							"minItems": 1, "maxItems": model.MaxWorkflowTasks,
						},
						"interval_seconds": map[string]interface{}{
							"type": "integer", "minimum": 1, "maximum": 31536000,
						},
						"max_runs": map[string]interface{}{
							"type":        "integer",
							"minimum":     0,
							"maximum":     1000000,
							"description": "0 repeats forever; 1 is one-shot.",
						},
					},
					"required":             []string{"name", "tasks", "interval_seconds"},
					"additionalProperties": false,
				},
			},
		})
	}
	return tools
}

// executeWorkflowTool creates and synchronously executes an exact tool DAG.
// There is no planner LLM: the task/tool/argument structure supplied by Core is
// validated and executed as-is.
func (ch *CoreHandler) executeWorkflowTool(
	ctx context.Context,
	userID, sessionID string,
	coreSession *model.Session,
	messageID string,
	args map[string]interface{},
) (string, error) {
	if coreSession == nil {
		return "", fmt.Errorf("Core session is required for workflow execution")
	}
	input, err := decodeWorkflowToolInput(args)
	if err != nil {
		return "", err
	}
	workflow, runErr := ch.runWorkflow(
		ctx, userID, sessionID, coreSession, messageID, "", input.Name, input.Tasks, true,
	)
	if workflow == nil {
		return "", runErr
	}
	result, summaryErr := workflowResultJSON(workflow, workflowSummaryOutput)
	if summaryErr != nil {
		return "", summaryErr
	}
	return result, runErr
}

func decodeWorkflowToolInput(args map[string]interface{}) (workflowToolInput, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return workflowToolInput{}, fmt.Errorf("failed to encode workflow arguments: %w", err)
	}
	var input workflowToolInput
	if err := json.Unmarshal(data, &input); err != nil {
		return workflowToolInput{}, fmt.Errorf("failed to decode workflow arguments: %w", err)
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return workflowToolInput{}, fmt.Errorf("name is required and must be a non-empty string")
	}
	if len(input.Name) > maxWorkflowNameLength {
		return workflowToolInput{}, fmt.Errorf("name must not exceed %d characters", maxWorkflowNameLength)
	}
	return input, nil
}

func (ch *CoreHandler) runWorkflow(
	ctx context.Context,
	userID, sessionID string,
	coreSession *model.Session,
	messageID, scheduleID, name string,
	tasks []*model.WorkflowTask,
	requireTaskApproval bool,
) (*model.WorkflowRun, error) {
	workflowStore, ok := ch.sessionHandler.GetStore().(workflowPersistence)
	if !ok {
		return nil, fmt.Errorf("workflow persistence is not configured")
	}
	tasks, err := cloneWorkflowTasks(tasks)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	workflow := &model.WorkflowRun{
		WorkflowID: newWorkflowID(), UserID: userID, SessionID: sessionID,
		MessageID: messageID, ScheduleID: scheduleID, Name: strings.TrimSpace(name),
		Status: model.WorkflowPending, Tasks: tasks, CreatedAt: now, UpdatedAt: now,
	}
	for _, task := range workflow.Tasks {
		task.Status = model.WorkflowTaskPending
		task.Output = ""
		task.Error = ""
		task.StartedAt = time.Time{}
		task.CompletedAt = time.Time{}
	}
	order, err := workflow.Validate()
	if err != nil {
		return nil, err
	}
	if err := ch.validateWorkflowTools(workflow.Tasks, scheduleID != ""); err != nil {
		return nil, err
	}
	if err := workflowStore.PutWorkflowRun(workflow); err != nil {
		return nil, fmt.Errorf("persist pending workflow: %w", err)
	}

	workflow.Status = model.WorkflowRunning
	workflow.StartedAt = time.Now()
	workflow.UpdatedAt = workflow.StartedAt
	if err := workflowStore.PutWorkflowRun(workflow); err != nil {
		return workflow, fmt.Errorf("persist running workflow: %w", err)
	}

	taskByID := make(map[string]*model.WorkflowTask, len(workflow.Tasks))
	for _, task := range workflow.Tasks {
		taskByID[task.ID] = task
	}

	var firstFailure error
	for _, taskIndex := range order {
		task := workflow.Tasks[taskIndex]
		if ctxErr := ctx.Err(); ctxErr != nil {
			cancelPendingWorkflowTasks(workflow.Tasks, ctxErr)
			workflow.Status = model.WorkflowCancelled
			workflow.Error = ctxErr.Error()
			firstFailure = ctxErr
			break
		}

		dependencyOutputs := make(map[string]string, len(task.DependsOn))
		blockedBy := ""
		for _, dependencyID := range task.DependsOn {
			dependency := taskByID[dependencyID]
			if dependency.Status != model.WorkflowTaskSucceeded {
				blockedBy = dependencyID
				break
			}
			dependencyOutputs[dependencyID] = dependency.Output
		}
		if blockedBy != "" {
			task.Status = model.WorkflowTaskSkipped
			task.Error = fmt.Sprintf("dependency %q did not succeed", blockedBy)
			task.CompletedAt = time.Now()
			workflow.UpdatedAt = task.CompletedAt
			if err := workflowStore.PutWorkflowRun(workflow); err != nil {
				return workflow, fmt.Errorf("persist skipped workflow task %q: %w", task.ID, err)
			}
			continue
		}

		resolvedArguments, err := resolveWorkflowArguments(task.Arguments, dependencyOutputs)
		if err != nil {
			task.Status = model.WorkflowTaskFailed
			task.Error = err.Error()
			task.CompletedAt = time.Now()
			workflow.UpdatedAt = task.CompletedAt
			if firstFailure == nil {
				firstFailure = fmt.Errorf("task %s: %w", task.ID, err)
			}
			if persistErr := workflowStore.PutWorkflowRun(workflow); persistErr != nil {
				return workflow, fmt.Errorf("persist failed workflow task %q: %w", task.ID, persistErr)
			}
			continue
		}
		argumentData, err := json.Marshal(resolvedArguments)
		if err != nil {
			return workflow, fmt.Errorf("encode task %q arguments: %w", task.ID, err)
		}

		task.Status = model.WorkflowTaskRunning
		task.StartedAt = time.Now()
		workflow.UpdatedAt = task.StartedAt
		if err := workflowStore.PutWorkflowRun(workflow); err != nil {
			return workflow, fmt.Errorf("persist running workflow task %q: %w", task.ID, err)
		}

		output, executionErr := ch.executeCoreToolWithError(
			ctx, userID, sessionID, coreSession, messageID,
			openai.ToolCall{
				ID:   workflow.WorkflowID + ":" + task.ID,
				Type: openai.ToolTypeFunction,
				Function: openai.FunctionCall{
					Name: task.Tool, Arguments: string(argumentData),
				},
			},
			requireTaskApproval,
		)
		task.Output = truncateWorkflowText(output, maxWorkflowTaskOutput)
		task.CompletedAt = time.Now()
		workflow.UpdatedAt = task.CompletedAt
		if ctxErr := ctx.Err(); ctxErr != nil {
			task.Status = model.WorkflowTaskCancelled
			task.Error = ctxErr.Error()
			workflow.Status = model.WorkflowCancelled
			workflow.Error = ctxErr.Error()
			firstFailure = ctxErr
			cancelPendingWorkflowTasks(workflow.Tasks, ctxErr)
		} else if executionErr != nil {
			task.Status = model.WorkflowTaskFailed
			task.Error = executionErr.Error()
			if firstFailure == nil {
				firstFailure = fmt.Errorf("task %s: %w", task.ID, executionErr)
			}
		} else {
			task.Status = model.WorkflowTaskSucceeded
		}
		if err := workflowStore.PutWorkflowRun(workflow); err != nil {
			return workflow, fmt.Errorf("persist completed workflow task %q: %w", task.ID, err)
		}
		if workflow.Status == model.WorkflowCancelled {
			break
		}
	}

	completedAt := time.Now()
	workflow.CompletedAt = completedAt
	workflow.UpdatedAt = completedAt
	if workflow.Status != model.WorkflowCancelled {
		if firstFailure != nil {
			workflow.Status = model.WorkflowFailed
			workflow.Error = firstFailure.Error()
		} else {
			workflow.Status = model.WorkflowSucceeded
		}
	}
	if err := workflowStore.PutWorkflowRun(workflow); err != nil {
		return workflow, fmt.Errorf("persist terminal workflow: %w", err)
	}
	return workflow, firstFailure
}

func (ch *CoreHandler) validateWorkflowTools(tasks []*model.WorkflowTask, scheduled bool) error {
	for _, task := range tasks {
		switch task.Tool {
		case "execute_workflow", "get_workflow_status", "create_workflow_schedule":
			return fmt.Errorf("task %q cannot invoke workflow control tool %q", task.ID, task.Tool)
		}
		if scheduled && task.Tool == "manage_schedules" {
			return fmt.Errorf("scheduled workflow task %q cannot manage other schedules", task.ID)
		}
		if scheduled && (strings.HasPrefix(task.Tool, "call_agent_") ||
			task.Tool == "create_session" || task.Tool == "change_session") {
			return fmt.Errorf("scheduled workflow task %q cannot route to or change an agent", task.ID)
		}
		if !ch.coreTools.Has(task.Tool) {
			return fmt.Errorf("task %q references unknown Core tool %q", task.ID, task.Tool)
		}
	}
	return nil
}

func (ch *CoreHandler) getWorkflowStatusTool(userID string, args map[string]interface{}) (string, error) {
	workflowID, err := requireStringArg(args, "workflow_id")
	if err != nil {
		return "", err
	}
	workflowStore, ok := ch.sessionHandler.GetStore().(workflowPersistence)
	if !ok {
		return "", fmt.Errorf("workflow persistence is not configured")
	}
	workflow, err := workflowStore.GetWorkflowRun(workflowID)
	if err != nil {
		return "", err
	}
	if workflow == nil || workflow.UserID != userID {
		return "", fmt.Errorf("workflow not found")
	}
	return workflowResultJSON(workflow, workflowSummaryOutput)
}

func (ch *CoreHandler) createWorkflowScheduleTool(
	userID, sourceSessionID string,
	args map[string]interface{},
) (string, error) {
	if ch.taskScheduler == nil {
		return "", fmt.Errorf("task scheduler is not configured")
	}
	input, err := decodeWorkflowToolInput(args)
	if err != nil {
		return "", err
	}
	if err := ch.validateWorkflowTools(input.Tasks, true); err != nil {
		return "", err
	}
	intervalSeconds, err := workflowIntegerArg(args, "interval_seconds", 1, 31_536_000, true)
	if err != nil {
		return "", err
	}
	maxRuns, err := workflowIntegerArg(args, "max_runs", 0, 1_000_000, false)
	if err != nil {
		return "", err
	}
	schedule, err := ch.taskScheduler.Create(engine.CreateTaskScheduleInput{
		UserID: userID, SessionID: sourceSessionID, AgentType: model.AgentTypeWorkflow,
		Name: input.Name, WorkflowTasks: input.Tasks,
		Interval: time.Duration(intervalSeconds) * time.Second, MaxRuns: maxRuns,
	})
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(map[string]interface{}{
		"ok": true, "schedule_id": schedule.ScheduleID, "name": schedule.Name,
		"status": schedule.Status, "session_id": schedule.SessionID,
		"interval_seconds": schedule.IntervalSeconds, "max_runs": schedule.MaxRuns,
		"next_run_at":      schedule.NextRunAt,
		"execution_policy": "fixed DAG; no planner LLM; no per-task approval",
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func workflowIntegerArg(
	args map[string]interface{},
	key string,
	minimum, maximum int64,
	required bool,
) (int64, error) {
	raw, exists := args[key]
	if !exists {
		if required {
			return 0, fmt.Errorf("%s is required and must be an integer", key)
		}
		return 0, nil
	}
	value, ok := raw.(float64)
	if !ok || value != float64(int64(value)) {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	integer := int64(value)
	if integer < minimum || integer > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return integer, nil
}

func workflowResultJSON(workflow *model.WorkflowRun, outputLimit int) (string, error) {
	tasks := make([]map[string]interface{}, 0, min(len(workflow.Tasks), workflowStatusTaskLimit))
	for i, task := range workflow.Tasks {
		if i >= workflowStatusTaskLimit {
			break
		}
		tasks = append(tasks, map[string]interface{}{
			"id": task.ID, "name": task.Name, "tool": task.Tool,
			"depends_on": task.DependsOn, "status": task.Status,
			"output": truncateWorkflowText(task.Output, outputLimit),
			"error":  truncateWorkflowText(task.Error, outputLimit),
		})
	}
	data, err := json.Marshal(map[string]interface{}{
		"workflow_id": workflow.WorkflowID, "schedule_id": workflow.ScheduleID,
		"name": workflow.Name, "status": workflow.Status,
		"error": truncateWorkflowText(workflow.Error, outputLimit), "tasks": tasks,
		"created_at": workflow.CreatedAt, "started_at": workflow.StartedAt,
		"completed_at": workflow.CompletedAt,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func cloneWorkflowTasks(tasks []*model.WorkflowTask) ([]*model.WorkflowTask, error) {
	data, err := json.Marshal(tasks)
	if err != nil {
		return nil, fmt.Errorf("encode workflow tasks: %w", err)
	}
	var cloned []*model.WorkflowTask
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, fmt.Errorf("decode workflow tasks: %w", err)
	}
	return cloned, nil
}

func resolveWorkflowArguments(arguments map[string]any, dependencyOutputs map[string]string) (map[string]any, error) {
	if arguments == nil {
		return map[string]any{}, nil
	}
	resolved, err := resolveWorkflowValue(arguments, dependencyOutputs)
	if err != nil {
		return nil, err
	}
	result, ok := resolved.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("task arguments must be an object")
	}
	return result, nil
}

func resolveWorkflowValue(value any, dependencyOutputs map[string]string) (any, error) {
	switch typed := value.(type) {
	case string:
		var referenceErr error
		resolved := workflowOutputReference.ReplaceAllStringFunc(typed, func(match string) string {
			parts := workflowOutputReference.FindStringSubmatch(match)
			output, ok := dependencyOutputs[parts[1]]
			if !ok {
				referenceErr = fmt.Errorf(
					"output reference %q requires that task to be listed in depends_on and to succeed",
					parts[1],
				)
				return match
			}
			return output
		})
		return resolved, referenceErr
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			resolved, err := resolveWorkflowValue(item, dependencyOutputs)
			if err != nil {
				return nil, err
			}
			result[key] = resolved
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			resolved, err := resolveWorkflowValue(item, dependencyOutputs)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil
	default:
		return value, nil
	}
}

func cancelPendingWorkflowTasks(tasks []*model.WorkflowTask, cause error) {
	now := time.Now()
	for _, task := range tasks {
		if task.Status == model.WorkflowTaskPending || task.Status == model.WorkflowTaskRunning {
			task.Status = model.WorkflowTaskCancelled
			task.Error = cause.Error()
			task.CompletedAt = now
		}
	}
}

func truncateWorkflowText(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "\n[truncated]"
}

func newWorkflowID() string {
	var random [6]byte
	if _, err := rand.Read(random[:]); err == nil {
		return fmt.Sprintf("wf_%d_%s", time.Now().UnixNano(), hex.EncodeToString(random[:]))
	}
	return fmt.Sprintf("wf_%d", time.Now().UnixNano())
}
