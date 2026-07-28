package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ghiac/agentize/browseruse"
	"github.com/ghiac/agentize/model"
)

type fakeBrowserUseService struct {
	startSession  string
	startRequest  browseruse.StartJobRequest
	getSession    string
	getJobID      string
	getWait       time.Duration
	cancelSession string
	cancelJobID   string
	job           *browseruse.Job
}

func (f *fakeBrowserUseService) Health(context.Context) error { return nil }

func (f *fakeBrowserUseService) Start(
	_ context.Context,
	sessionID string,
	request browseruse.StartJobRequest,
) (*browseruse.Job, error) {
	f.startSession = sessionID
	f.startRequest = request
	return f.job, nil
}

func (f *fakeBrowserUseService) Get(
	_ context.Context,
	sessionID, jobID string,
	wait time.Duration,
) (*browseruse.Job, error) {
	f.getSession = sessionID
	f.getJobID = jobID
	f.getWait = wait
	return f.job, nil
}

func (f *fakeBrowserUseService) Cancel(
	_ context.Context,
	sessionID, jobID string,
) (*browseruse.Job, error) {
	f.cancelSession = sessionID
	f.cancelJobID = jobID
	return f.job, nil
}

func TestBrowserUseToolRunUsesTrustedSessionAndReturnsNextAction(t *testing.T) {
	t.Parallel()

	service := &fakeBrowserUseService{
		job: &browseruse.Job{
			ID:        "job-1",
			Status:    browseruse.JobRunning,
			CreatedAt: time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		},
	}
	registry := model.NewFunctionRegistry()
	engine := &Engine{Functions: registry, BrowserUse: service}
	engine.RegisterBrowserUseTool()

	result, err := registry.Execute(browserUseToolName, map[string]interface{}{
		"action":          "run",
		"task":            "Inspect example.com",
		"max_steps":       float64(12),
		"allowed_domains": []interface{}{"example.com"},
		"wait_seconds":    float64(0),
		"__session_id__":  "trusted-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.startSession != "trusted-session" {
		t.Fatalf("unexpected session: %q", service.startSession)
	}
	if service.startRequest.Task != "Inspect example.com" ||
		service.startRequest.MaxSteps != 12 ||
		len(service.startRequest.AllowedDomains) != 1 {
		t.Fatalf("unexpected request: %#v", service.startRequest)
	}
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		t.Fatal(err)
	}
	if response["next_action"] == nil {
		t.Fatalf("expected next_action in %s", result)
	}
}

func TestBrowserUseToolStatusAndCancel(t *testing.T) {
	t.Parallel()

	service := &fakeBrowserUseService{
		job: &browseruse.Job{
			ID:        "job-2",
			Status:    browseruse.JobCancelled,
			CreatedAt: time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC),
		},
	}
	engine := &Engine{Functions: model.NewFunctionRegistry(), BrowserUse: service}
	engine.RegisterBrowserUseTool()

	if _, err := engine.executeBrowserUseTool(map[string]interface{}{
		"action":         "status",
		"job_id":         "job-2",
		"wait_seconds":   float64(0),
		"__session_id__": "session-2",
	}); err != nil {
		t.Fatal(err)
	}
	if service.getSession != "session-2" || service.getJobID != "job-2" {
		t.Fatalf("unexpected get call: %q %q", service.getSession, service.getJobID)
	}

	if _, err := engine.executeBrowserUseTool(map[string]interface{}{
		"action":         "cancel",
		"job_id":         "job-2",
		"__session_id__": "session-2",
	}); err != nil {
		t.Fatal(err)
	}
	if service.cancelSession != "session-2" || service.cancelJobID != "job-2" {
		t.Fatalf("unexpected cancel call: %q %q", service.cancelSession, service.cancelJobID)
	}
}

func TestBrowserUseToolRequiresTrustedSession(t *testing.T) {
	t.Parallel()

	engine := &Engine{BrowserUse: &fakeBrowserUseService{}}
	if _, err := engine.executeBrowserUseTool(map[string]interface{}{
		"action": "run",
		"task":   "do something",
	}); err == nil {
		t.Fatal("expected missing session error")
	}
}

func TestBrowserUseToolDefinitionDeclaresActions(t *testing.T) {
	t.Parallel()

	definition := BrowserUseToolDefinition()
	if definition.Function == nil || definition.Function.Name != browserUseToolName {
		t.Fatalf("unexpected definition: %#v", definition)
	}
}
