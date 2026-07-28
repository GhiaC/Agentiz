package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ghiac/agentize/browseruse"
	"github.com/sashabaranov/go-openai"
)

const (
	browserUseToolName       = "browser_use"
	browserUseDefaultWait    = 45
	browserUseMaxWaitSeconds = 60
)

// BrowserUseToolDefinition returns the optional browser-use schema sent to the
// LLM. Session ownership is injected by the runtime and never supplied by the
// model.
func BrowserUseToolDefinition() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name: browserUseToolName,
			Description: "Run and inspect autonomous web-browser tasks in the isolated browser-use service. " +
				"Use run with a precise task; it returns the completed result when possible or a job_id for later status calls. " +
				"Use cancel to stop unneeded work. Browser profiles persist per Agentize session. " +
				"Actions: run, status, cancel.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type": "string",
						"enum": []string{"run", "status", "cancel"},
					},
					"task": map[string]interface{}{
						"type":        "string",
						"description": "Detailed browser objective. Required for run.",
					},
					"job_id": map[string]interface{}{
						"type":        "string",
						"description": "Job returned by run. Required for status and cancel.",
					},
					"allowed_domains": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"maxItems":    100,
						"description": "Optional per-job navigation allowlist. A deployment-wide operator allowlist, when configured, takes precedence.",
					},
					"max_steps": map[string]interface{}{
						"type":        "integer",
						"minimum":     1,
						"maximum":     500,
						"description": "Optional browser-use agent step limit.",
					},
					"use_vision": map[string]interface{}{
						"type":        "boolean",
						"description": "Optional override for screenshot vision. Omit to use the sidecar default.",
					},
					"wait_seconds": map[string]interface{}{
						"type":        "integer",
						"minimum":     0,
						"maximum":     browserUseMaxWaitSeconds,
						"description": "How long to wait for completion before returning. Defaults to 45 for run and 30 for status.",
					},
				},
				"required": []string{"action"},
			},
		},
	}
}

// SetBrowserUse enables or disables the optional browser-use capability.
func (e *Engine) SetBrowserUse(service browseruse.Service) {
	if e == nil {
		return
	}
	e.BrowserUse = service
	e.RegisterBrowserUseTool()
}

// RegisterBrowserUseTool registers the built-in implementation when a service
// is configured. The schema is exposed independently in GetTools.
func (e *Engine) RegisterBrowserUseTool() {
	if e == nil || e.Functions == nil || e.BrowserUse == nil {
		return
	}
	_ = e.Functions.RegisterOrReplace(
		browserUseToolName,
		"مرورگر وب",
		func(args map[string]interface{}) (string, error) {
			return e.executeBrowserUseTool(args)
		},
	)
}

func (e *Engine) executeBrowserUseTool(args map[string]interface{}) (string, error) {
	if e.BrowserUse == nil {
		return "", fmt.Errorf("browser-use service is not configured")
	}
	sessionID, _ := args["__session_id__"].(string)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("browser-use tool requires authenticated session context")
	}
	action, _ := args["action"].(string)
	action = strings.ToLower(strings.TrimSpace(action))

	switch action {
	case "run":
		task, err := browserUseRequiredString(args, "task")
		if err != nil {
			return "", err
		}
		maxSteps, err := browserUseOptionalInteger(args, "max_steps", 0, 0, 500)
		if err != nil {
			return "", err
		}
		allowedDomains, err := browserUseStringSlice(args, "allowed_domains")
		if err != nil {
			return "", err
		}
		var useVision *bool
		if value, exists := args["use_vision"]; exists {
			typed, ok := value.(bool)
			if !ok {
				return "", fmt.Errorf("use_vision must be a boolean")
			}
			useVision = &typed
		}
		waitSeconds, err := browserUseOptionalInteger(
			args, "wait_seconds", browserUseDefaultWait, 0, browserUseMaxWaitSeconds,
		)
		if err != nil {
			return "", err
		}

		startContext, cancelStart := context.WithTimeout(context.Background(), 20*time.Second)
		job, err := e.BrowserUse.Start(startContext, sessionID, browseruse.StartJobRequest{
			Task:           task,
			AllowedDomains: allowedDomains,
			MaxSteps:       maxSteps,
			UseVision:      useVision,
		})
		cancelStart()
		if err != nil {
			return "", fmt.Errorf("start browser-use job: %w", err)
		}
		if waitSeconds > 0 && !job.Status.Terminal() {
			waitContext, cancelWait := context.WithTimeout(
				context.Background(),
				time.Duration(waitSeconds+5)*time.Second,
			)
			updated, pollErr := e.BrowserUse.Get(
				waitContext,
				sessionID,
				job.ID,
				time.Duration(waitSeconds)*time.Second,
			)
			cancelWait()
			if pollErr == nil {
				job = updated
			} else {
				return browserUseJSON(map[string]interface{}{
					"ok":         true,
					"job":        job,
					"poll_error": pollErr.Error(),
					"next_action": map[string]interface{}{
						"action": "status",
						"job_id": job.ID,
					},
				})
			}
		}
		return browserUseJobJSON(job)

	case "status":
		jobID, err := browserUseRequiredString(args, "job_id")
		if err != nil {
			return "", err
		}
		waitSeconds, err := browserUseOptionalInteger(
			args, "wait_seconds", 30, 0, browserUseMaxWaitSeconds,
		)
		if err != nil {
			return "", err
		}
		requestContext, cancel := context.WithTimeout(
			context.Background(),
			time.Duration(waitSeconds+5)*time.Second,
		)
		defer cancel()
		job, err := e.BrowserUse.Get(
			requestContext,
			sessionID,
			jobID,
			time.Duration(waitSeconds)*time.Second,
		)
		if err != nil {
			return "", fmt.Errorf("get browser-use job: %w", err)
		}
		return browserUseJobJSON(job)

	case "cancel":
		jobID, err := browserUseRequiredString(args, "job_id")
		if err != nil {
			return "", err
		}
		requestContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		job, err := e.BrowserUse.Cancel(requestContext, sessionID, jobID)
		if err != nil {
			return "", fmt.Errorf("cancel browser-use job: %w", err)
		}
		return browserUseJobJSON(job)

	default:
		return "", fmt.Errorf("unsupported browser-use action %q", action)
	}
}

func browserUseJobJSON(job *browseruse.Job) (string, error) {
	response := map[string]interface{}{"ok": true, "job": job}
	if job != nil && !job.Status.Terminal() {
		response["next_action"] = map[string]interface{}{
			"action": "status",
			"job_id": job.ID,
		}
	}
	return browserUseJSON(response)
}

func browserUseRequiredString(args map[string]interface{}, key string) (string, error) {
	value, _ := args[key].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return value, nil
}

func browserUseOptionalInteger(
	args map[string]interface{},
	key string,
	defaultValue, minimum, maximum int,
) (int, error) {
	value, exists := args[key]
	if !exists || value == nil {
		return defaultValue, nil
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case float32:
		number = float64(typed)
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		number = parsed
	default:
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	if math.Trunc(number) != number {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	integer := int(number)
	if integer < minimum || integer > maximum {
		return 0, fmt.Errorf("%s must be between %d and %d", key, minimum, maximum)
	}
	return integer, nil
}

func browserUseStringSlice(args map[string]interface{}, key string) ([]string, error) {
	value, exists := args[key]
	if !exists || value == nil {
		return nil, nil
	}
	var raw []interface{}
	switch typed := value.(type) {
	case []interface{}:
		raw = typed
	case []string:
		raw = make([]interface{}, len(typed))
		for index, item := range typed {
			raw[index] = item
		}
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	if len(raw) > 100 {
		return nil, fmt.Errorf("%s cannot contain more than 100 entries", key)
	}
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be an array of strings", key)
		}
		text = strings.TrimSpace(text)
		if text == "" || len(text) > 255 {
			return nil, fmt.Errorf("%s entries must be 1-255 characters", key)
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		result = append(result, text)
	}
	return result, nil
}

func browserUseJSON(value interface{}) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode browser-use result: %w", err)
	}
	return string(encoded), nil
}
