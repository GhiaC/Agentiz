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
	// Summary is a short English keyword-based summary of the node content (for AI context/caching)
	Summary string
	// Auth contains user access control rules
	Auth Auth
	// Content is the markdown content from node.md
	Content string
	// Tools are the tools defined at this node level
	Tools []Tool
	// MCP is a list of MCP servers that this node can connect to
	MCP []MCP
	// Metadata
	LoadedAt time.Time
	Hash     string // Content hash for cache invalidation
}

// Auth defines user access control for the node
// Uses RBAC (Role-Based Access Control) pattern with inheritance support
//
// Example YAML:
//
//	auth:
//	  inherit: true  # Inherit from parent (default: true)
//	  default:
//	    perms: "r"  # Default: read-only for everyone
//	  roles:
//	    admin:
//	      perms: "rwx"  # Full access
//	    viewer:
//	      perms: "r"  # Read-only
//	  groups:
//	    developers:
//	      perms: "rwx"  # Full access for dev group
//	  users:
//	    "user123":
//	      perms: "rw"  # Override: read+write for specific user
//
// Permission flags:
//   - r: read (can read node content)
//   - w: write (can edit node)
//   - x: execute (can access child nodes)
//   - s: see (can see node exists)
//   - d: visible in docs
//   - g: visible in graph
//
// Priority order (highest to lowest):
//  1. User-specific override
//  2. Group permissions
//  3. Role permissions
//  4. Inherited from parent (if inherit: true)
//  5. Default permissions
//  6. Deny all
type Auth struct {
	// Inherit from parent node (default: true)
	// When true, permissions from parent node are inherited
	Inherit bool `yaml:"inherit,omitempty"`

	// Default permissions applied when user/role/group not found
	// If empty, defaults to deny-all
	Default *Permissions `yaml:"default,omitempty"`

	// Role-based access control
	// Roles can be defined globally and referenced here
	// Example: roles: { admin: { perms: "rwx" }, viewer: { perms: "r" } }
	Roles map[string]*Permissions `yaml:"roles,omitempty"`

	// User-specific overrides (takes precedence over roles/groups)
	// Use this for fine-grained control on specific users
	// Example: users: { "user123": { perms: "rw" } }
	Users map[string]*Permissions `yaml:"users,omitempty"`

	// Groups allow grouping users together
	// Groups can be defined globally and referenced here
	// Example: groups: { developers: { perms: "rwx" } }
	Groups map[string]*Permissions `yaml:"groups,omitempty"`
}

// Permissions defines what actions are allowed
// Uses permission strings (like Unix: rwx) for flexibility
type Permissions struct {
	// Permission flags using string format (similar to Unix permissions)
	// Format: "r" (read), "w" (write), "x" (execute/access_next), "s" (see), "d" (docs), "g" (graph)
	// Examples: "rwx" (full access), "r" (read-only), "rw" (read+write)
	// Empty string means no permissions
	Perms string `yaml:"perms,omitempty"`

	// Alternative: explicit boolean flags (for clarity in YAML)
	// If Perms is empty, these are used
	Read         *bool `yaml:"read,omitempty"`          // Can read the node content
	Write        *bool `yaml:"write,omitempty"`         // Can edit the node
	Execute      *bool `yaml:"execute,omitempty"`       // Can access child nodes (execute navigation)
	See          *bool `yaml:"see,omitempty"`           // Can see the node exists
	VisibleDocs  *bool `yaml:"visible_docs,omitempty"`  // Visible in documentation
	VisibleGraph *bool `yaml:"visible_graph,omitempty"` // Visible in graph visualization
}

// HasPermission checks if a permission flag is set
func (p *Permissions) HasPermission(flag rune) bool {
	if p == nil {
		return false
	}

	// Check permission string first
	if p.Perms != "" {
		for _, r := range p.Perms {
			if r == flag {
				return true
			}
		}
		return false
	}

	// Fallback to boolean flags
	switch flag {
	case 'r':
		return p.Read != nil && *p.Read
	case 'w':
		return p.Write != nil && *p.Write
	case 'x':
		return p.Execute != nil && *p.Execute
	case 's':
		return p.See != nil && *p.See
	case 'd':
		return p.VisibleDocs != nil && *p.VisibleDocs
	case 'g':
		return p.VisibleGraph != nil && *p.VisibleGraph
	}
	return false
}

// Permission flags constants
const (
	PermRead         = 'r' // Read content
	PermWrite        = 'w' // Write/edit content
	PermExecute      = 'x' // Execute/access next nodes
	PermSee          = 's' // See node exists
	PermVisibleDocs  = 'd' // Visible in docs
	PermVisibleGraph = 'g' // Visible in graph
)

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
	Summary     string `yaml:"summary,omitempty"`
	Auth        Auth   `yaml:"auth"`
	MCP         []MCP  `yaml:"mcp,omitempty"`
}

// ResolvePermissions resolves permissions for a user, considering inheritance
// parentNode can be nil if this is root or inheritance is disabled
// userRoles is a list of roles the user belongs to (can be empty)
// userGroups is a list of groups the user belongs to (can be empty)
func (n *Node) ResolvePermissions(userID string, parentNode *Node, userRoles []string, userGroups []string) *Permissions {
	// Priority order (highest to lowest):
	// 1. User-specific override
	// 2. Group permissions
	// 3. Role permissions
	// 4. Inherited from parent (if enabled)
	// 5. Default permissions
	// 6. Deny all (if nothing matches)

	// 1. Check user-specific override
	if n.Auth.Users != nil {
		if perms, ok := n.Auth.Users[userID]; ok && perms != nil {
			return perms
		}
	}

	// 2. Check group permissions (first match wins)
	if n.Auth.Groups != nil {
		for _, group := range userGroups {
			if perms, ok := n.Auth.Groups[group]; ok && perms != nil {
				return perms
			}
		}
	}

	// 3. Check role permissions (first match wins)
	if n.Auth.Roles != nil {
		for _, role := range userRoles {
			if perms, ok := n.Auth.Roles[role]; ok && perms != nil {
				return perms
			}
		}
	}

	// 4. Inherit from parent if enabled (default: true)
	if n.Auth.Inherit && parentNode != nil {
		// Recursively resolve parent permissions (without passing parent again to avoid infinite loop)
		parentPerms := parentNode.ResolvePermissions(userID, nil, userRoles, userGroups)
		if parentPerms != nil {
			return parentPerms
		}
	}

	// 5. Use default permissions
	if n.Auth.Default != nil {
		return n.Auth.Default
	}

	// 6. Deny all (return nil)
	return nil
}

// CanUserAccessNext checks if a user can access child nodes
func (n *Node) CanUserAccessNext(userID string, parentNode *Node, userRoles []string, userGroups []string) bool {
	perms := n.ResolvePermissions(userID, parentNode, userRoles, userGroups)
	if perms == nil {
		// No auth configured, allow by default for backward compatibility
		return true
	}
	return perms.HasPermission(PermExecute)
}

// CanUserSee checks if a user can see the node
func (n *Node) CanUserSee(userID string, parentNode *Node, userRoles []string, userGroups []string) bool {
	perms := n.ResolvePermissions(userID, parentNode, userRoles, userGroups)
	if perms == nil {
		// No auth configured, allow by default for backward compatibility
		return true
	}
	return perms.HasPermission(PermSee)
}

// CanUserRead checks if a user can read the node
func (n *Node) CanUserRead(userID string, parentNode *Node, userRoles []string, userGroups []string) bool {
	perms := n.ResolvePermissions(userID, parentNode, userRoles, userGroups)
	if perms == nil {
		// No auth configured, allow by default for backward compatibility
		return true
	}
	return perms.HasPermission(PermRead)
}

// CanUserEdit checks if a user can edit the node
func (n *Node) CanUserEdit(userID string, parentNode *Node, userRoles []string, userGroups []string) bool {
	perms := n.ResolvePermissions(userID, parentNode, userRoles, userGroups)
	if perms == nil {
		// No auth configured, allow by default for backward compatibility
		return true
	}
	return perms.HasPermission(PermWrite)
}

// IsVisibleInDocs checks if node should be visible in documentation for a user
func (n *Node) IsVisibleInDocs(userID string, parentNode *Node, userRoles []string, userGroups []string) bool {
	perms := n.ResolvePermissions(userID, parentNode, userRoles, userGroups)
	if perms == nil {
		// No auth configured, visible by default for backward compatibility
		return true
	}
	return perms.HasPermission(PermVisibleDocs)
}

// IsVisibleInGraph checks if node should be visible in graph for a user
func (n *Node) IsVisibleInGraph(userID string, parentNode *Node, userRoles []string, userGroups []string) bool {
	perms := n.ResolvePermissions(userID, parentNode, userRoles, userGroups)
	if perms == nil {
		// No auth configured, visible by default for backward compatibility
		return true
	}
	return perms.HasPermission(PermVisibleGraph)
}

// Convenience methods for backward compatibility (without parent/roles/groups)
// These assume no inheritance and no roles/groups
func (n *Node) CanUserAccessNextSimple(userID string) bool {
	return n.CanUserAccessNext(userID, nil, nil, nil)
}

func (n *Node) CanUserSeeSimple(userID string) bool {
	return n.CanUserSee(userID, nil, nil, nil)
}

func (n *Node) CanUserReadSimple(userID string) bool {
	return n.CanUserRead(userID, nil, nil, nil)
}

func (n *Node) CanUserEditSimple(userID string) bool {
	return n.CanUserEdit(userID, nil, nil, nil)
}

func (n *Node) IsVisibleInDocsSimple(userID string) bool {
	return n.IsVisibleInDocs(userID, nil, nil, nil)
}

func (n *Node) IsVisibleInGraphSimple(userID string) bool {
	return n.IsVisibleInGraph(userID, nil, nil, nil)
}
