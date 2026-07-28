package model

import (
	"fmt"
	"strings"
	"time"
)

// TaskScheduleStatus is the persisted lifecycle state of a scheduled task.
type TaskScheduleStatus string

const (
	TaskScheduleActive    TaskScheduleStatus = "active"
	TaskSchedulePaused    TaskScheduleStatus = "paused"
	TaskScheduleCompleted TaskScheduleStatus = "completed"
)

// TaskRunStatus is the outcome of one scheduled execution.
type TaskRunStatus string

const (
	TaskRunRunning   TaskRunStatus = "running"
	TaskRunSucceeded TaskRunStatus = "succeeded"
	TaskRunFailed    TaskRunStatus = "failed"
	TaskRunCancelled TaskRunStatus = "cancelled"
)

// TaskSchedule is a persistent, user-owned task that is executed repeatedly.
// Prompt is sent through the owning agent/session. When ConclusionModel is set,
// the raw output is also sent to that (usually cheaper) model and its compact
// conclusion is persisted alongside the raw output.
type TaskSchedule struct {
	ScheduleID       string             `json:"schedule_id" bson:"schedule_id"`
	UserID           string             `json:"user_id" bson:"user_id"`
	SourceSessionID  string             `json:"source_session_id,omitempty" bson:"source_session_id,omitempty"`
	SessionID        string             `json:"session_id" bson:"session_id"`
	AgentType        AgentType          `json:"agent_type,omitempty" bson:"agent_type,omitempty"`
	Name             string             `json:"name" bson:"name"`
	Prompt           string             `json:"prompt" bson:"prompt"`
	WorkflowTasks    []*WorkflowTask    `json:"workflow_tasks,omitempty" bson:"workflow_tasks,omitempty"`
	IntervalSeconds  int64              `json:"interval_seconds" bson:"interval_seconds"`
	MaxRuns          int64              `json:"max_runs,omitempty" bson:"max_runs,omitempty"`
	ConclusionModel  string             `json:"conclusion_model,omitempty" bson:"conclusion_model,omitempty"`
	ConclusionPrompt string             `json:"conclusion_prompt,omitempty" bson:"conclusion_prompt,omitempty"`
	Status           TaskScheduleStatus `json:"status" bson:"status"`

	RunCount       int64         `json:"run_count" bson:"run_count"`
	LastRunStatus  TaskRunStatus `json:"last_run_status,omitempty" bson:"last_run_status,omitempty"`
	LastWorkflowID string        `json:"last_workflow_id,omitempty" bson:"last_workflow_id,omitempty"`
	LastOutput     string        `json:"last_output,omitempty" bson:"last_output,omitempty"`
	LastConclusion string        `json:"last_conclusion,omitempty" bson:"last_conclusion,omitempty"`
	LastError      string        `json:"last_error,omitempty" bson:"last_error,omitempty"`
	LastRunAt      time.Time     `json:"last_run_at,omitempty" bson:"last_run_at,omitempty"`
	NextRunAt      time.Time     `json:"next_run_at" bson:"next_run_at"`
	CreatedAt      time.Time     `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at" bson:"updated_at"`
}

// Validate checks the invariants shared by the API, LLM tool, and stores.
func (s *TaskSchedule) Validate() error {
	if s == nil {
		return fmt.Errorf("schedule is required")
	}
	if strings.TrimSpace(s.ScheduleID) == "" {
		return fmt.Errorf("schedule_id is required")
	}
	if strings.TrimSpace(s.UserID) == "" {
		return fmt.Errorf("user_id is required")
	}
	if strings.TrimSpace(s.SessionID) == "" {
		return fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name is required")
	}
	hasPrompt := strings.TrimSpace(s.Prompt) != ""
	hasWorkflow := len(s.WorkflowTasks) > 0
	if hasPrompt == hasWorkflow {
		return fmt.Errorf("exactly one of prompt or workflow_tasks is required")
	}
	if hasWorkflow {
		if _, err := WorkflowTopologicalOrder(s.WorkflowTasks); err != nil {
			return fmt.Errorf("invalid workflow_tasks: %w", err)
		}
	}
	if s.IntervalSeconds < 1 {
		return fmt.Errorf("interval_seconds must be at least 1")
	}
	if s.MaxRuns < 0 {
		return fmt.Errorf("max_runs cannot be negative")
	}
	if s.Status != TaskScheduleActive && s.Status != TaskSchedulePaused && s.Status != TaskScheduleCompleted {
		return fmt.Errorf("invalid schedule status %q", s.Status)
	}
	return nil
}

// Interval returns the schedule interval as a duration.
func (s *TaskSchedule) Interval() time.Duration {
	if s == nil || s.IntervalSeconds < 1 {
		return time.Second
	}
	return time.Duration(s.IntervalSeconds) * time.Second
}

// TaskScheduleRun is the persisted history row for one execution.
type TaskScheduleRun struct {
	RunID            string        `json:"run_id" bson:"run_id"`
	ScheduleID       string        `json:"schedule_id" bson:"schedule_id"`
	UserID           string        `json:"user_id" bson:"user_id"`
	SessionID        string        `json:"session_id" bson:"session_id"`
	Status           TaskRunStatus `json:"status" bson:"status"`
	Output           string        `json:"output,omitempty" bson:"output,omitempty"`
	Conclusion       string        `json:"conclusion,omitempty" bson:"conclusion,omitempty"`
	ConclusionModel  string        `json:"conclusion_model,omitempty" bson:"conclusion_model,omitempty"`
	WorkflowID       string        `json:"workflow_id,omitempty" bson:"workflow_id,omitempty"`
	Error            string        `json:"error,omitempty" bson:"error,omitempty"`
	PromptTokens     int           `json:"prompt_tokens,omitempty" bson:"prompt_tokens,omitempty"`
	CompletionTokens int           `json:"completion_tokens,omitempty" bson:"completion_tokens,omitempty"`
	StartedAt        time.Time     `json:"started_at" bson:"started_at"`
	CompletedAt      time.Time     `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
}
