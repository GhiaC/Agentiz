package model

import "time"

// Node represents a single node in the knowledge tree
type Node struct {
	// Path is the relative path from root (e.g., "root", "root/next", "root/next/next")
	Path string

	// ID is the node identifier (from node.yaml or derived from Path)
	ID string

	// Title and Description from node.yaml
	Title       string
	Description string

	// Policy contains routing and behavior rules
	Policy Policy

	// Content is the markdown content from node.md
	Content string

	// Tools are the tools defined at this node level
	Tools []Tool

	// Metadata
	LoadedAt time.Time
	Hash     string // Content hash for cache invalidation
}

// Policy defines node behavior and routing rules
type Policy struct {
	CanAdvance       bool    `yaml:"can_advance"`
	AdvanceCondition string  `yaml:"advance_condition"`
	MaxOpenFiles     int     `yaml:"max_open_files"`
	Routing          Routing `yaml:"routing"`
	Memory           Memory  `yaml:"memory"`
}

// Routing defines how navigation works
type Routing struct {
	Mode     string   `yaml:"mode"`     // "sequential" for now
	Children []string `yaml:"children"` // List of child node names (instead of "next")
}

// Memory defines what should be persisted
type Memory struct {
	Persist []string `yaml:"persist"` // e.g., ["summary", "facts"]
}

// ToolStatus represents the status of a tool
type ToolStatus string

const (
	// ToolStatusActive means the tool is active and can be used
	ToolStatusActive ToolStatus = "active"
	// ToolStatusTemporaryDisabled means the tool is temporarily disabled (broken/maintenance)
	ToolStatusTemporaryDisabled ToolStatus = "temporary_disabled"
	// ToolStatusHidden means the tool is hidden and won't appear in listings
	ToolStatusHidden ToolStatus = "hidden"
)

// DisableReason represents why a tool is disabled
type DisableReason string

const (
	// DisableReasonNone means tool is not disabled
	DisableReasonNone DisableReason = ""
	// DisableReasonMaintenance means tool is under maintenance
	DisableReasonMaintenance DisableReason = "maintenance"
	// DisableReasonError means tool has an error
	DisableReasonError DisableReason = "error"
	// DisableReasonDeprecated means tool is deprecated
	DisableReasonDeprecated DisableReason = "deprecated"
	// DisableReasonRateLimit means tool is rate limited
	DisableReasonRateLimit DisableReason = "rate_limit"
	// DisableReasonUnavailable means tool service is unavailable
	DisableReasonUnavailable DisableReason = "unavailable"
	// DisableReasonCustom means custom reason (check ErrorMessage)
	DisableReasonCustom DisableReason = "custom"
)

// Tool represents a tool definition
type Tool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`

	// Status controls tool availability
	// Default: ToolStatusActive
	Status ToolStatus `json:"status,omitempty"`

	// DisableReason specifies why the tool is disabled (only relevant if Status is TemporaryDisabled)
	DisableReason DisableReason `json:"disable_reason,omitempty"`

	// ErrorMessage provides additional details about why the tool is disabled
	ErrorMessage string `json:"error_message,omitempty"`
}

// NodeMeta is the parsed structure from node.yaml
type NodeMeta struct {
	ID          string `yaml:"id"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Policy      Policy `yaml:"policy"`
}
