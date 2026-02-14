package debuger

import (
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// DebugStore is an interface for stores that support debugging operations
type DebugStore interface {
	GetAllSessions() (map[string][]*model.Session, error)
	GetAllUsers() ([]*model.User, error)
	GetAllMessages() ([]*model.Message, error)
	GetAllOpenedFiles() ([]*model.OpenedFile, error)
	GetMessagesBySession(sessionID string) ([]*model.Message, error)
	GetMessagesByUser(userID string) ([]*model.Message, error)
	GetOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error)
	GetUser(userID string) (*model.User, error)
	GetSession(sessionID string) (*model.Session, error)
	GetAllToolCalls() ([]*model.ToolCall, error)
	GetToolCallsBySession(sessionID string) ([]*model.ToolCall, error)
	GetToolCallByID(toolCallID string) (*model.ToolCall, error)
	GetToolCallByToolID(toolID string) (*model.ToolCall, error)
	PutSummarizationLog(log *model.SummarizationLog) error
	GetSummarizationLogsBySession(sessionID string) ([]*model.SummarizationLog, error)
	GetAllSummarizationLogs() ([]*model.SummarizationLog, error)

	// DeleteUserData deletes all sessions, messages, tool calls, summarization logs,
	// and opened files for a user. Resets user's ActiveSessionIDs and SessionSeqs.
	DeleteUserData(userID string) error
}

// SchedulerConfig holds scheduler configuration for display in debug pages
type SchedulerConfig struct {
	CheckInterval                   time.Duration
	FirstSummarizationThreshold     int
	SubsequentMessageThreshold      int
	SubsequentTimeThreshold         time.Duration
	LastActivityThreshold           time.Duration
	ImmediateSummarizationThreshold int
	SummaryModel                    string
}

// ToolCallInfo represents information about a tool call for display
type ToolCallInfo struct {
	SessionID    string
	UserID       string
	MessageID    string
	ToolID       string // Sequential tool ID (e.g., user123-core-s0001-t0001)
	ToolCallID   string // OpenAI's tool call ID
	AgentType    string
	FunctionName string
	Arguments    string
	Result       string
	ResultLength int
	DurationMs   int64
	Status       string // pending, success, failed
	Error        string // error message when status=failed
	CreatedAt    time.Time
}

// SummarizedMessageInfo represents information about a summarized message
type SummarizedMessageInfo struct {
	SessionID        string
	UserID           string
	SessionTitle     string
	Role             string
	Content          string
	HasToolCalls     bool
	ToolCalls        []openai.ToolCall
	SummarizedAt     time.Time
	SessionCreatedAt time.Time
}

// PageData holds common data passed to page renderers
type PageData struct {
	Title       string
	CurrentPage string
}

// DashboardStats holds statistics for the dashboard
type DashboardStats struct {
	TotalUsers     int
	TotalSessions  int
	TotalMessages  int
	TotalFiles     int
	TotalToolCalls int
}

// SessionStats holds statistics for sessions
type SessionStats struct {
	TotalSessions           int
	SummarizedSessions      int
	EligibleSessions        int
	SessionsWithMessages    int
	SessionsWithoutMessages int
}

// SummarizationStats holds statistics for summarization logs
type SummarizationStats struct {
	TotalLogs   int
	SuccessLogs int
	FailedLogs  int
	PendingLogs int
}

// BreadcrumbItem represents a single breadcrumb navigation item
type BreadcrumbItem struct {
	Label  string
	URL    string
	Active bool
}

// NavItem represents a navigation menu item
type NavItem struct {
	URL    string
	Icon   string
	Text   string
	Active bool
}

// TableColumn represents a table column configuration
type TableColumn struct {
	Header string
	Width  string
	Align  string // left, center, right
	NoWrap bool
}

// StatCardData holds data for a statistics card
type StatCardData struct {
	Value    string
	Label    string
	Icon     string
	Color    string // primary, success, danger, warning, info, secondary
	LinkURL  string
	LinkText string
}
