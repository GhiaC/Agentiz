package model

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const MaxWorkflowTasks = 50

var workflowTaskIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// WorkflowStatus is the durable lifecycle state of a deterministic tool DAG.
type WorkflowStatus string

const (
	WorkflowPending   WorkflowStatus = "pending"
	WorkflowRunning   WorkflowStatus = "running"
	WorkflowSucceeded WorkflowStatus = "succeeded"
	WorkflowFailed    WorkflowStatus = "failed"
	WorkflowCancelled WorkflowStatus = "cancelled"
)

// WorkflowTaskStatus is the durable lifecycle state of one workflow task.
type WorkflowTaskStatus string

const (
	WorkflowTaskPending   WorkflowTaskStatus = "pending"
	WorkflowTaskRunning   WorkflowTaskStatus = "running"
	WorkflowTaskSucceeded WorkflowTaskStatus = "succeeded"
	WorkflowTaskFailed    WorkflowTaskStatus = "failed"
	WorkflowTaskSkipped   WorkflowTaskStatus = "skipped"
	WorkflowTaskCancelled WorkflowTaskStatus = "cancelled"
)

// WorkflowTask is one explicit Core-tool invocation in a WorkflowRun.
// DependsOn contains task IDs and Arguments is passed to the tool verbatim after
// deterministic {{tasks.<id>.output}} substitution.
type WorkflowTask struct {
	ID        string         `json:"id" bson:"id"`
	Name      string         `json:"name" bson:"name"`
	Tool      string         `json:"tool" bson:"tool"`
	Arguments map[string]any `json:"arguments" bson:"arguments"`
	DependsOn []string       `json:"depends_on,omitempty" bson:"depends_on,omitempty"`

	Status      WorkflowTaskStatus `json:"status" bson:"status"`
	Output      string             `json:"output,omitempty" bson:"output,omitempty"`
	Error       string             `json:"error,omitempty" bson:"error,omitempty"`
	StartedAt   time.Time          `json:"started_at,omitempty" bson:"started_at,omitempty"`
	CompletedAt time.Time          `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
}

// WorkflowRun is a user-owned, durable DAG of exact tool invocations. The DAG
// is operational state, not conversational content, so stores persist it as a
// first-class entity and messages only need to retain its WorkflowID.
type WorkflowRun struct {
	WorkflowID string          `json:"workflow_id" bson:"workflow_id"`
	UserID     string          `json:"user_id" bson:"user_id"`
	SessionID  string          `json:"session_id" bson:"session_id"`
	MessageID  string          `json:"message_id,omitempty" bson:"message_id,omitempty"`
	ScheduleID string          `json:"schedule_id,omitempty" bson:"schedule_id,omitempty"`
	Name       string          `json:"name" bson:"name"`
	Status     WorkflowStatus  `json:"status" bson:"status"`
	Tasks      []*WorkflowTask `json:"tasks" bson:"tasks"`

	Error       string    `json:"error,omitempty" bson:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt   time.Time `json:"updated_at" bson:"updated_at"`
	StartedAt   time.Time `json:"started_at,omitempty" bson:"started_at,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty" bson:"completed_at,omitempty"`
}

// Validate checks persistence fields and all DAG invariants. It returns the
// deterministic topological order so callers do not need to validate twice.
func (w *WorkflowRun) Validate() ([]int, error) {
	if w == nil {
		return nil, fmt.Errorf("workflow is required")
	}
	if strings.TrimSpace(w.WorkflowID) == "" {
		return nil, fmt.Errorf("workflow_id is required")
	}
	if strings.TrimSpace(w.UserID) == "" {
		return nil, fmt.Errorf("user_id is required")
	}
	if strings.TrimSpace(w.SessionID) == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if strings.TrimSpace(w.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	switch w.Status {
	case WorkflowPending, WorkflowRunning, WorkflowSucceeded, WorkflowFailed, WorkflowCancelled:
	default:
		return nil, fmt.Errorf("invalid workflow status %q", w.Status)
	}
	order, err := WorkflowTopologicalOrder(w.Tasks)
	if err != nil {
		return nil, err
	}
	for _, task := range w.Tasks {
		switch task.Status {
		case WorkflowTaskPending, WorkflowTaskRunning, WorkflowTaskSucceeded,
			WorkflowTaskFailed, WorkflowTaskSkipped, WorkflowTaskCancelled:
		default:
			return nil, fmt.Errorf("task %q has invalid status %q", task.ID, task.Status)
		}
	}
	return order, nil
}

// WorkflowTopologicalOrder validates tasks and returns their indexes in a
// stable topological order. Tasks that become ready together retain input order.
func WorkflowTopologicalOrder(tasks []*WorkflowTask) ([]int, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("at least one task is required")
	}
	if len(tasks) > MaxWorkflowTasks {
		return nil, fmt.Errorf("workflow may contain at most %d tasks", MaxWorkflowTasks)
	}

	indexByID := make(map[string]int, len(tasks))
	for i, task := range tasks {
		if task == nil {
			return nil, fmt.Errorf("task %d is required", i)
		}
		if !workflowTaskIDPattern.MatchString(task.ID) {
			return nil, fmt.Errorf("task %d id %q must match %s", i, task.ID, workflowTaskIDPattern)
		}
		if _, exists := indexByID[task.ID]; exists {
			return nil, fmt.Errorf("duplicate task id %q", task.ID)
		}
		if strings.TrimSpace(task.Name) == "" {
			return nil, fmt.Errorf("task %q name is required", task.ID)
		}
		if strings.TrimSpace(task.Tool) == "" {
			return nil, fmt.Errorf("task %q tool is required", task.ID)
		}
		indexByID[task.ID] = i
	}

	indegree := make([]int, len(tasks))
	dependents := make([][]int, len(tasks))
	for i, task := range tasks {
		seen := make(map[string]struct{}, len(task.DependsOn))
		for _, dependencyID := range task.DependsOn {
			if dependencyID == task.ID {
				return nil, fmt.Errorf("task %q cannot depend on itself", task.ID)
			}
			if _, duplicate := seen[dependencyID]; duplicate {
				return nil, fmt.Errorf("task %q has duplicate dependency %q", task.ID, dependencyID)
			}
			seen[dependencyID] = struct{}{}
			dependencyIndex, exists := indexByID[dependencyID]
			if !exists {
				return nil, fmt.Errorf("task %q depends on unknown task %q", task.ID, dependencyID)
			}
			indegree[i]++
			dependents[dependencyIndex] = append(dependents[dependencyIndex], i)
		}
	}

	order := make([]int, 0, len(tasks))
	processed := make([]bool, len(tasks))
	for len(order) < len(tasks) {
		progressed := false
		for i := range tasks {
			if processed[i] || indegree[i] != 0 {
				continue
			}
			processed[i] = true
			order = append(order, i)
			for _, dependent := range dependents[i] {
				indegree[dependent]--
			}
			progressed = true
		}
		if !progressed {
			return nil, fmt.Errorf("workflow task dependencies contain a cycle")
		}
	}
	return order, nil
}

// Terminal reports whether no more workflow state transitions are expected.
func (s WorkflowStatus) Terminal() bool {
	return s == WorkflowSucceeded || s == WorkflowFailed || s == WorkflowCancelled
}
