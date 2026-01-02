package model

// MCP represents a Model Context Protocol server configuration
type MCP struct {
	// ID is the unique identifier for this MCP server
	ID string `yaml:"id" json:"id"`

	// Name is a human-readable name for the MCP server
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Type specifies the type of MCP server (e.g., "http", "stdio", "websocket")
	Type string `yaml:"type,omitempty" json:"type,omitempty"`

	// URL is the endpoint URL for the MCP server (for http/websocket types)
	URL string `yaml:"url,omitempty" json:"url,omitempty"`

	// Command is the command to run for stdio-based MCP servers
	Command string `yaml:"command,omitempty" json:"command,omitempty"`

	// Args are the arguments for the command (for stdio-based servers)
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`

	// Description provides additional information about this MCP server
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Config contains additional configuration as key-value pairs
	Config map[string]interface{} `yaml:"config,omitempty" json:"config,omitempty"`

	// Enabled indicates whether this MCP server is enabled (default: true)
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`

	// Timeout specifies the connection timeout in seconds (optional)
	Timeout *int `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}
