package planning

import "time"

// --- Step Type ---

type StepType string

const (
	StepLLMCall       StepType = "llm_call"
	StepToolCall      StepType = "tool_call"
	StepAgentDelegate StepType = "agent_delegate"
	StepParallel      StepType = "parallel"
	StepConditional   StepType = "conditional"
	StepCollect       StepType = "collect"
	StepHumanReview   StepType = "human_review"
)

// --- Step Status ---

type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepRunning   StepStatus = "running"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
	// StepWaiting marks a human_review step suspended awaiting an async review
	// decision. The runner does NOT park a goroutine; the plan is persisted and
	// resumed when the review resolves (see Resume / ResumeOnReview).
	StepWaiting StepStatus = "waiting"
)

// --- Plan Status ---

type PlanStatus string

const (
	PlanPending   PlanStatus = "pending"
	PlanRunning   PlanStatus = "running"
	PlanCompleted PlanStatus = "completed"
	PlanFailed    PlanStatus = "failed"
	PlanCancelled PlanStatus = "cancelled"
	// PlanWaiting marks a plan suspended on a human_review step awaiting an async
	// decision. The plan is persisted and resumes when the review resolves.
	PlanWaiting PlanStatus = "waiting"
)

// --- Condition (for conditional steps) ---

type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

// --- StepConfig ---

type StepConfig struct {
	Model        string         `json:"model,omitempty"`
	Prompt       string         `json:"prompt,omitempty"`
	SystemPrompt string         `json:"system_prompt,omitempty"`
	ToolName     string         `json:"tool_name,omitempty"`
	ToolArgs     map[string]any `json:"tool_args,omitempty"`
	AgentType    string         `json:"agent_type,omitempty"`
	AgentInput   string         `json:"agent_input,omitempty"`
	SubSteps     []*Step        `json:"sub_steps,omitempty"`
	MaxRetries   int            `json:"max_retries,omitempty"`
	// Timeout bounds a single execution attempt of this step (opt-in: > 0).
	// The runner enforces it as a hard deadline: when it elapses, the attempt
	// is abandoned and the plan moves on even if the engine/tool ignores the
	// context. Trade-off: the abandoned goroutine keeps running in the
	// background until it returns on its own (it cannot be forcibly stopped).
	// With MaxRetries > 0 the timeout applies across all attempts of the step,
	// not per attempt.
	Timeout time.Duration `json:"timeout,omitempty"`
}

// --- StepResult ---

type StepResult struct {
	Output     string         `json:"output"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	TokensUsed int            `json:"tokens_used,omitempty"`
	Duration   time.Duration  `json:"duration,omitempty"`
}

// --- Step ---

type Step struct {
	ID          string              `json:"id"`
	Type        StepType            `json:"type"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Config      StepConfig          `json:"config"`
	DependsOn   []string            `json:"depends_on,omitempty"`
	Status      StepStatus          `json:"status"`
	Result      *StepResult         `json:"result,omitempty"`
	StartedAt   *time.Time          `json:"started_at,omitempty"`
	CompletedAt *time.Time          `json:"completed_at,omitempty"`
	Error       string              `json:"error,omitempty"`
	Condition   *Condition          `json:"condition,omitempty"`
	Branches    map[string][]string `json:"branches,omitempty"`
}

// --- Plan ---

type Plan struct {
	ID            string     `json:"id"`
	UserID        string     `json:"user_id"`
	SessionID     string     `json:"session_id"`
	Input         string     `json:"input"`
	Steps         []*Step    `json:"steps"`
	Status        PlanStatus `json:"status"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Output        string     `json:"output,omitempty"`
	Error         string     `json:"error,omitempty"`
	Version       int        `json:"version"`
	SystemPrompts []string   `json:"system_prompts,omitempty"`
}
