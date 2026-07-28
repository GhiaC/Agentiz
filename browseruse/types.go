// Package browseruse provides the Go-side contract for an isolated
// browser-use service. The Python/Chromium runtime lives out of process so it
// can be upgraded, restarted, and resource-limited independently of Agentize.
package browseruse

import (
	"context"
	"time"
)

// Service is the browser-use capability consumed by the Agentize engine.
// Implementations must scope jobs to sessionID and reject cross-session access.
type Service interface {
	Health(ctx context.Context) error
	Start(ctx context.Context, sessionID string, request StartJobRequest) (*Job, error)
	Get(ctx context.Context, sessionID, jobID string, wait time.Duration) (*Job, error)
	Cancel(ctx context.Context, sessionID, jobID string) (*Job, error)
}

// StartJobRequest describes one autonomous browser task.
type StartJobRequest struct {
	Task           string   `json:"task"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	MaxSteps       int      `json:"max_steps,omitempty"`
	UseVision      *bool    `json:"use_vision,omitempty"`
}

// JobStatus is the lifecycle state reported by the sidecar.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Terminal reports whether no more work will be performed for this job.
func (s JobStatus) Terminal() bool {
	switch s {
	case JobSucceeded, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

// Job is a bounded snapshot of a browser-use job.
type Job struct {
	ID          string     `json:"id"`
	Status      JobStatus  `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Result      *JobResult `json:"result,omitempty"`
	Error       string     `json:"error,omitempty"`
}

// JobResult contains the useful, size-bounded browser-use history. Screenshots
// and profiles remain in the sidecar data volume and are not copied into LLM
// context.
type JobResult struct {
	FinalResult     string                   `json:"final_result,omitempty"`
	Done            bool                     `json:"done"`
	Successful      *bool                    `json:"successful,omitempty"`
	VisitedURLs     []string                 `json:"visited_urls,omitempty"`
	Steps           int                      `json:"steps"`
	DurationSeconds float64                  `json:"duration_seconds"`
	ActionNames     []string                 `json:"action_names,omitempty"`
	Actions         []map[string]interface{} `json:"actions,omitempty"`
	Errors          []string                 `json:"errors,omitempty"`
}
