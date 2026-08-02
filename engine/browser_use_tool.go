package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ghiac/agentize/browseruse"
	"github.com/ghiac/agentize/model"
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
				"Use run with a precise task; the browser agent can search, navigate/back, wait, click, type, upload session files supplied through file_ids, scroll, find text, send keys, run page JavaScript, switch/close tabs, extract content, inspect/select dropdowns, take visual screenshots, and read/write/replace task files. " +
				"It can complete research, data extraction, login and form workflows, testing, and browser downloads. It returns the completed result when possible or a job_id for later status calls. " +
				"Browser sessions and their open tabs persist between run calls for the same Agentize session. Use tabs to inspect the current tab snapshot and close_tab to explicitly close one tab. " +
				"Use screenshot with a job_id to save the latest captured browser view as a generated user image that the host can attach to the reply. " +
				"Use downloads to list files the browser downloaded, then download to save one selected file as a generated user file. " +
				"Use cancel to stop unneeded work; cancel does not close the persistent browser session. " +
				"Actions: run, status, tabs, close_tab, screenshot, downloads, download, cancel.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"action": map[string]interface{}{
						"type": "string",
						"enum": []string{"run", "status", "tabs", "close_tab", "screenshot", "downloads", "download", "cancel"},
					},
					"task": map[string]interface{}{
						"type":        "string",
						"description": "Detailed browser objective. Required for run.",
					},
					"file_ids": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"maxItems":    10,
						"description": "User files from this session to make available for a run, such as files to upload in a web form.",
					},
					"job_id": map[string]interface{}{
						"type":        "string",
						"description": "Job returned by run. Required for status, screenshot, downloads, download, and cancel.",
					},
					"tab_id": map[string]interface{}{
						"type":        "string",
						"description": "Tab ID returned by tabs. Required for close_tab.",
					},
					"file_name": map[string]interface{}{
						"type":        "string",
						"description": "Downloaded filename returned by downloads. Required for download.",
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
		uploads, err := e.browserUseUploads(sessionID, args)
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
			Uploads:        uploads,
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

	case "tabs":
		tabService, ok := e.BrowserUse.(browseruse.TabService)
		if !ok {
			return "", fmt.Errorf("browser-use service does not support persistent tabs")
		}
		requestContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		tabs, err := tabService.Tabs(requestContext, sessionID)
		if err != nil {
			return "", fmt.Errorf("list browser tabs: %w", err)
		}
		return browserUseJSON(map[string]interface{}{
			"ok":   true,
			"tabs": tabs,
		})

	case "close_tab":
		tabID, err := browserUseRequiredString(args, "tab_id")
		if err != nil {
			return "", err
		}
		tabService, ok := e.BrowserUse.(browseruse.TabService)
		if !ok {
			return "", fmt.Errorf("browser-use service does not support persistent tabs")
		}
		requestContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		tabs, err := tabService.CloseTab(requestContext, sessionID, tabID)
		if err != nil {
			return "", fmt.Errorf("close browser tab: %w", err)
		}
		return browserUseJSON(map[string]interface{}{
			"ok":            true,
			"closed_tab_id": tabID,
			"tabs":          tabs,
		})

	case "screenshot":
		jobID, err := browserUseRequiredString(args, "job_id")
		if err != nil {
			return "", err
		}
		screenshotService, ok := e.BrowserUse.(browseruse.ScreenshotService)
		if !ok {
			return "", fmt.Errorf("browser-use service does not support screenshots")
		}
		if e.Files == nil {
			return "", fmt.Errorf("file store is not configured; cannot deliver browser screenshot")
		}
		requestContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		screenshot, err := screenshotService.Screenshot(requestContext, sessionID, jobID)
		if err != nil {
			return "", fmt.Errorf("take browser screenshot: %w", err)
		}
		if screenshot == nil || len(screenshot.Data) == 0 {
			return "", fmt.Errorf("browser-use service returned an empty screenshot")
		}
		mimeType := strings.TrimSpace(screenshot.MIMEType)
		if !strings.HasPrefix(mimeType, "image/") {
			return "", fmt.Errorf("browser-use service returned invalid screenshot MIME type %q", mimeType)
		}
		name := strings.TrimSpace(screenshot.Name)
		if name == "" {
			name = "browser-" + jobID + ".png"
		}
		file, err := e.RecordUserFile(
			sessionID,
			name,
			mimeType,
			model.FileSourceGenerated,
			screenshot.Data,
		)
		if err != nil {
			return "", fmt.Errorf("save browser screenshot: %w", err)
		}
		return browserUseJSON(map[string]interface{}{
			"ok":     true,
			"job_id": jobID,
			"screenshot": map[string]interface{}{
				"file_id":   file.FileID,
				"name":      file.Name,
				"mime_type": file.MIMEType,
				"size":      file.Size,
			},
			"delivery": map[string]interface{}{
				"type":    "generated_user_file",
				"file_id": file.FileID,
			},
		})

	case "downloads":
		jobID, err := browserUseRequiredString(args, "job_id")
		if err != nil {
			return "", err
		}
		downloadService, ok := e.BrowserUse.(browseruse.DownloadService)
		if !ok {
			return "", fmt.Errorf("browser-use service does not support downloads")
		}
		requestContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		files, err := downloadService.Downloads(requestContext, sessionID, jobID)
		if err != nil {
			return "", fmt.Errorf("list browser downloads: %w", err)
		}
		return browserUseJSON(map[string]interface{}{
			"ok":     true,
			"job_id": jobID,
			"files":  files,
			"next_action": map[string]interface{}{
				"action": "download",
				"job_id": jobID,
			},
		})

	case "download":
		jobID, err := browserUseRequiredString(args, "job_id")
		if err != nil {
			return "", err
		}
		fileName, err := browserUseRequiredString(args, "file_name")
		if err != nil {
			return "", err
		}
		downloadService, ok := e.BrowserUse.(browseruse.DownloadService)
		if !ok {
			return "", fmt.Errorf("browser-use service does not support downloads")
		}
		if e.Files == nil {
			return "", fmt.Errorf("file store is not configured; cannot deliver browser download")
		}
		requestContext, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		download, err := downloadService.Download(requestContext, sessionID, jobID, fileName)
		if err != nil {
			return "", fmt.Errorf("retrieve browser download: %w", err)
		}
		if download == nil || len(download.Data) == 0 {
			return "", fmt.Errorf("browser-use service returned an empty download")
		}
		name := strings.TrimSpace(download.Name)
		if name == "" {
			name = fileName
		}
		mimeType := strings.TrimSpace(download.MIMEType)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		file, err := e.RecordUserFile(sessionID, name, mimeType, model.FileSourceGenerated, download.Data)
		if err != nil {
			return "", fmt.Errorf("save browser download: %w", err)
		}
		return browserUseJSON(map[string]interface{}{
			"ok":     true,
			"job_id": jobID,
			"download": map[string]interface{}{
				"file_id":   file.FileID,
				"name":      file.Name,
				"mime_type": file.MIMEType,
				"size":      file.Size,
			},
			"delivery": map[string]interface{}{
				"type":    "generated_user_file",
				"file_id": file.FileID,
			},
		})

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

func (e *Engine) browserUseUploads(sessionID string, args map[string]interface{}) ([]browseruse.Upload, error) {
	fileIDs, err := browserUseStringSlice(args, "file_ids")
	if err != nil || len(fileIDs) == 0 {
		return nil, err
	}
	if len(fileIDs) > 10 {
		return nil, fmt.Errorf("file_ids cannot contain more than 10 entries")
	}
	if e.Files == nil {
		return nil, fmt.Errorf("file store is not configured; cannot stage browser uploads")
	}
	const maxUploadBytes = 10 << 20
	const maxTotalUploadBytes = 25 << 20
	var total int64
	uploads := make([]browseruse.Upload, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		data, meta, readErr := e.ReadUserFile(fileID)
		if readErr != nil || meta == nil || meta.SessionID != sessionID {
			return nil, fmt.Errorf("file is not available in this session: %s", fileID)
		}
		if len(data) == 0 || int64(len(data)) > maxUploadBytes {
			return nil, fmt.Errorf("browser upload %q must be between 1 byte and %d bytes", meta.Name, maxUploadBytes)
		}
		total += int64(len(data))
		if total > maxTotalUploadBytes {
			return nil, fmt.Errorf("browser uploads cannot exceed %d bytes total", maxTotalUploadBytes)
		}
		uploads = append(uploads, browseruse.Upload{
			Name:     meta.Name,
			MIMEType: meta.MIMEType,
			Data:     data,
		})
	}
	return uploads, nil
}

func browserUseJSON(value interface{}) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode browser-use result: %w", err)
	}
	return string(encoded), nil
}
