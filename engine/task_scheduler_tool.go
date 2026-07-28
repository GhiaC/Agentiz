package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// TaskSchedulerToolDefinition returns the built-in manage_schedules schema sent
// to the LLM. Identity fields are injected by the runtime, never supplied by the model.
func TaskSchedulerToolDefinition() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: "manage_schedules",
			Description: "Create and manage persistent recurring tasks owned by the current user/session. " +
				"Each schedule gets a dedicated session, so its history and memory are isolated and retained. " +
				"A schedule repeatedly sends its prompt through one fixed agent. Optionally set conclusion_model " +
				"to send each raw output to a cheaper model for a compact conclusion. " +
				"Actions: create, list, get, stop, resume, run_now, delete.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type": "string",
						"enum": []string{"create", "list", "get", "stop", "resume", "run_now", "delete"},
					},
					"schedule_id": map[string]interface{}{
						"type":        "string",
						"description": "Required for get/stop/resume/run_now/delete.",
					},
					"name": map[string]interface{}{
						"type":        "string",
						"description": "Short schedule name; required for create.",
					},
					"agent_name": map[string]interface{}{
						"type":        "string",
						"description": "Core only: fixed worker agent that owns the schedule's dedicated session; required when Core creates a prompt schedule.",
					},
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "Task to execute on every iteration; required for create.",
					},
					"interval_seconds": map[string]interface{}{
						"type":        "integer",
						"minimum":     1,
						"maximum":     31536000,
						"description": "Delay between completed runs, in seconds; required for create.",
					},
					"max_runs": map[string]interface{}{
						"type":        "integer",
						"minimum":     0,
						"description": "Optional execution limit. 0 or omitted repeats forever; 1 creates a one-shot schedule.",
					},
					"conclusion_model": map[string]interface{}{
						"type":        "string",
						"description": "Optional cheaper model that receives the raw task output and produces the stored conclusion.",
					},
					"conclusion_prompt": map[string]interface{}{
						"type":        "string",
						"description": "Optional instructions for the conclusion model (for example, decide whether a target condition was met).",
					},
				},
				"required": []string{"action"},
			},
		},
	}
}

// RegisterTaskSchedulerTool registers the built-in implementation. The schema is
// added independently in GetTools so it never needs a tools.json entry.
func (e *Engine) RegisterTaskSchedulerTool() {
	if e == nil || e.Functions == nil {
		return
	}
	_ = e.Functions.RegisterOrReplace(
		"manage_schedules",
		"مدیریت زمان‌بندی‌ها",
		func(args map[string]interface{}) (string, error) {
			scheduler := e.GetTaskScheduler()
			if scheduler == nil {
				return "", fmt.Errorf("task scheduler is not configured")
			}
			return scheduler.ExecuteTool(args)
		},
	)
}

// ExecuteTool applies a manage_schedules action with trusted identity injected
// under __user_id__ and __session_id__ by the Engine/Core runtimes.
func (s *TaskScheduler) ExecuteTool(args map[string]interface{}) (string, error) {
	return s.executeTool(args, "")
}

// ExecuteToolForAgent is the trusted Core entry point for binding a newly
// created prompt schedule to one fixed worker-agent type. The model cannot set
// agentType itself; Core resolves agent_name through AgentManager first.
func (s *TaskScheduler) ExecuteToolForAgent(
	args map[string]interface{},
	agentType model.AgentType,
) (string, error) {
	return s.executeTool(args, agentType)
}

func (s *TaskScheduler) executeTool(
	args map[string]interface{},
	agentType model.AgentType,
) (string, error) {
	if s == nil {
		return "", fmt.Errorf("task scheduler is not configured")
	}
	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))
	userID, _ := args["__user_id__"].(string)
	sessionID, _ := args["__session_id__"].(string)
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(sessionID) == "" {
		return "", fmt.Errorf("schedule tool requires authenticated user and session context")
	}

	switch action {
	case "create":
		name, err := taskScheduleRequiredString(args, "name")
		if err != nil {
			return "", err
		}
		prompt, err := taskScheduleRequiredString(args, "prompt")
		if err != nil {
			return "", err
		}
		seconds, err := taskScheduleInteger(args, "interval_seconds")
		if err != nil {
			return "", err
		}
		maxRuns, err := taskScheduleOptionalInteger(args, "max_runs")
		if err != nil {
			return "", err
		}
		schedule, err := s.Create(CreateTaskScheduleInput{
			UserID: userID, SessionID: sessionID, AgentType: agentType,
			Name: name, Prompt: prompt,
			Interval:         time.Duration(seconds) * time.Second,
			MaxRuns:          maxRuns,
			ConclusionModel:  taskScheduleOptionalString(args, "conclusion_model"),
			ConclusionPrompt: taskScheduleOptionalString(args, "conclusion_prompt"),
		})
		if err != nil {
			return "", err
		}
		return taskScheduleJSON(map[string]interface{}{"ok": true, "schedule": schedule})

	case "list":
		schedules, err := s.List(userID)
		if err != nil {
			return "", err
		}
		items := make([]map[string]interface{}, 0, len(schedules))
		for _, schedule := range schedules {
			items = append(items, taskScheduleSummary(schedule))
		}
		return taskScheduleJSON(map[string]interface{}{"ok": true, "schedules": items})

	case "get":
		id, err := taskScheduleRequiredString(args, "schedule_id")
		if err != nil {
			return "", err
		}
		schedule, err := s.Get(id, userID)
		if err != nil {
			return "", err
		}
		if schedule == nil {
			return "", fmt.Errorf("schedule not found")
		}
		runs, err := s.Runs(id, userID, 5)
		if err != nil {
			return "", err
		}
		runItems := make([]map[string]interface{}, 0, len(runs))
		for _, run := range runs {
			runItems = append(runItems, map[string]interface{}{
				"run_id": run.RunID, "status": run.Status,
				"output":           truncateTaskScheduleText(run.Output, 2000),
				"conclusion":       truncateTaskScheduleText(run.Conclusion, 2000),
				"error":            truncateTaskScheduleText(run.Error, 1000),
				"conclusion_model": run.ConclusionModel,
				"prompt_tokens":    run.PromptTokens, "completion_tokens": run.CompletionTokens,
				"started_at": run.StartedAt, "completed_at": run.CompletedAt,
			})
		}
		detail := taskScheduleSummary(schedule)
		detail["prompt"] = truncateTaskScheduleText(schedule.Prompt, 4000)
		detail["conclusion_prompt"] = truncateTaskScheduleText(schedule.ConclusionPrompt, 2000)
		detail["last_output"] = truncateTaskScheduleText(schedule.LastOutput, 2000)
		return taskScheduleJSON(map[string]interface{}{"ok": true, "schedule": detail, "runs": runItems})

	case "stop":
		id, err := taskScheduleRequiredString(args, "schedule_id")
		if err != nil {
			return "", err
		}
		schedule, err := s.Pause(id, userID)
		if err != nil {
			return "", err
		}
		return taskScheduleJSON(map[string]interface{}{"ok": true, "schedule": taskScheduleSummary(schedule)})

	case "resume":
		id, err := taskScheduleRequiredString(args, "schedule_id")
		if err != nil {
			return "", err
		}
		schedule, err := s.Resume(id, userID)
		if err != nil {
			return "", err
		}
		return taskScheduleJSON(map[string]interface{}{"ok": true, "schedule": taskScheduleSummary(schedule)})

	case "run_now":
		id, err := taskScheduleRequiredString(args, "schedule_id")
		if err != nil {
			return "", err
		}
		schedule, err := s.RunNow(id, userID)
		if err != nil {
			return "", err
		}
		return taskScheduleJSON(map[string]interface{}{"ok": true, "schedule": taskScheduleSummary(schedule)})

	case "delete":
		id, err := taskScheduleRequiredString(args, "schedule_id")
		if err != nil {
			return "", err
		}
		if err := s.Delete(id, userID); err != nil {
			return "", err
		}
		return taskScheduleJSON(map[string]interface{}{"ok": true, "deleted_schedule_id": id})
	default:
		return "", fmt.Errorf("unsupported schedule action %q", action)
	}
}

func taskScheduleRequiredString(args map[string]interface{}, key string) (string, error) {
	value, ok := args[key].(string)
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("%s is required and must be a non-empty string", key)
	}
	return value, nil
}

func taskScheduleOptionalString(args map[string]interface{}, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}

func taskScheduleInteger(args map[string]interface{}, key string) (int64, error) {
	var value int64
	switch raw := args[key].(type) {
	case int:
		value = int64(raw)
	case int64:
		value = raw
	case float64:
		if math.Trunc(raw) != raw {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		value = int64(raw)
	case json.Number:
		parsed, err := raw.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		value = parsed
	default:
		return 0, fmt.Errorf("%s is required and must be an integer", key)
	}
	if value < 1 || value > int64(maxTaskScheduleInterval/time.Second) {
		return 0, fmt.Errorf("%s must be between 1 and 31536000", key)
	}
	return value, nil
}

func taskScheduleOptionalInteger(args map[string]interface{}, key string) (int64, error) {
	if _, exists := args[key]; !exists {
		return 0, nil
	}
	return taskScheduleIntegerRange(args, key, 0, 1_000_000)
}

func taskScheduleIntegerRange(args map[string]interface{}, key string, minValue, maxValue int64) (int64, error) {
	var value int64
	switch raw := args[key].(type) {
	case int:
		value = int64(raw)
	case int64:
		value = raw
	case float64:
		if math.Trunc(raw) != raw {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		value = int64(raw)
	case json.Number:
		parsed, err := raw.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		value = parsed
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minValue, maxValue)
	}
	return value, nil
}

func taskScheduleSummary(schedule *model.TaskSchedule) map[string]interface{} {
	kind := "prompt"
	if len(schedule.WorkflowTasks) > 0 {
		kind = "workflow"
	}
	return map[string]interface{}{
		"schedule_id": schedule.ScheduleID, "name": schedule.Name, "status": schedule.Status,
		"kind": kind, "agent_type": schedule.AgentType, "session_id": schedule.SessionID,
		"interval_seconds": schedule.IntervalSeconds, "next_run_at": schedule.NextRunAt,
		"max_runs": schedule.MaxRuns, "run_count": schedule.RunCount,
		"last_run_status": schedule.LastRunStatus, "last_workflow_id": schedule.LastWorkflowID,
		"last_conclusion":  truncateTaskScheduleText(schedule.LastConclusion, 1000),
		"last_error":       truncateTaskScheduleText(schedule.LastError, 1000),
		"conclusion_model": schedule.ConclusionModel,
	}
}

func taskScheduleJSON(value interface{}) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
