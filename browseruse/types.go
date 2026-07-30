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

// ScreenshotService is an optional extension implemented by services that can
// return the latest screenshot captured for a browser job.
type ScreenshotService interface {
	Screenshot(ctx context.Context, sessionID, jobID string) (*Screenshot, error)
}

// DownloadService is an optional extension implemented by services that expose
// files downloaded by a browser job. Downloads remain scoped to their owning
// session and are copied into Agentize's user-file store only on request.
type DownloadService interface {
	Downloads(ctx context.Context, sessionID, jobID string) ([]Download, error)
	Download(ctx context.Context, sessionID, jobID, name string) (*DownloadFile, error)
}

// DebugService is an optional extension implemented by services that expose
// bounded, operator-facing browser job and network-load metadata.
type DebugService interface {
	Debug(ctx context.Context, jobLimit, loadLimit int) (*DebugSnapshot, error)
}

// StartJobRequest describes one autonomous browser task.
type StartJobRequest struct {
	Task           string   `json:"task"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	MaxSteps       int      `json:"max_steps,omitempty"`
	UseVision      *bool    `json:"use_vision,omitempty"`
	Uploads        []Upload `json:"uploads,omitempty"`
}

// Upload is a user-owned file staged only for one browser job. The sidecar
// gives its path to browser-use so the agent can attach it to a web form.
type Upload struct {
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data_base64"`
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
	ID                  string     `json:"id"`
	Status              JobStatus  `json:"status"`
	CreatedAt           time.Time  `json:"created_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	CompletedAt         *time.Time `json:"completed_at,omitempty"`
	Result              *JobResult `json:"result,omitempty"`
	Error               string     `json:"error,omitempty"`
	ScreenshotAvailable bool       `json:"screenshot_available,omitempty"`
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

// Screenshot is a browser image returned by the sidecar. The engine records it
// as a generated UserFile before exposing it to the model or host application.
type Screenshot struct {
	Data     []byte
	Name     string
	MIMEType string
}

// Download is safe metadata for a file downloaded by a browser job.
type Download struct {
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
}

// DownloadFile contains a downloaded file's metadata and bytes.
type DownloadFile struct {
	Download
	Data []byte `json:"-"`
}

// BrowserLoad is a safe, bounded projection of one network request. Request and
// response bodies and headers are deliberately excluded.
type BrowserLoad struct {
	StartedAt  *time.Time `json:"started_at,omitempty"`
	DurationMs float64    `json:"duration_ms"`
	Method     string     `json:"method"`
	URL        string     `json:"url"`
	Status     int        `json:"status"`
	StatusText string     `json:"status_text,omitempty"`
	MIMEType   string     `json:"mime_type,omitempty"`
	Bytes      int64      `json:"bytes"`
	Failed     bool       `json:"failed"`
}

// DebugJob augments a normal job snapshot with ownership and bounded network
// metadata for the protected debugger UI.
type DebugJob struct {
	Job
	SessionID string        `json:"session_id"`
	Task      string        `json:"task"`
	LoadCount int           `json:"load_count"`
	Loads     []BrowserLoad `json:"loads,omitempty"`
}

// DebugSnapshot is the protected operational view returned by DebugService.
type DebugSnapshot struct {
	TotalJobs         int        `json:"total_jobs"`
	RunningJobs       int        `json:"running_jobs"`
	MaxJobs           int        `json:"max_jobs"`
	MaxConcurrentJobs int        `json:"max_concurrent_jobs"`
	Jobs              []DebugJob `json:"jobs"`
}
