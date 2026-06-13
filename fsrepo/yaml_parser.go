package fsrepo

import (
	"fmt"
	"strings"

	"github.com/ghiac/agentize/model"
	"gopkg.in/yaml.v3"
)

// parseYAML parses a node.yaml document into model.NodeMeta using a real YAML
// parser (gopkg.in/yaml.v3). Malformed YAML or invalid values fail loudly — a
// node with wrong permissions is a security issue, not a parse nicety.
//
// Supported auth layouts (all produced by older trees and the docs):
//
//	auth:
//	  inherit: false
//	  default: { perms: "r" }
//	  users:
//	    "user-1": { perms: "rw" }          # map form
//	  roles:  { admin: { perms: "rwx" } }
//	  groups: { devs:  { perms: "rw" } }
//
//	auth:
//	  users:
//	    - user_id: "user-1"                # legacy list form
//	      can_read: true
//
//	auth:
//	  "user-1":                            # legacy bare-user form
//	    perms: "rw"
//
// Permission keys accept both the canonical names (read/write/execute/see/
// visible_docs/visible_graph) and the legacy aliases (can_read/can_edit/
// can_access_next/can_see/visible_in_docs/visible_in_graph).
func parseYAML(data []byte, meta *model.NodeMeta) error {
	var doc struct {
		ID          string      `yaml:"id"`
		Title       string      `yaml:"title"`
		Description string      `yaml:"description"`
		Summary     string      `yaml:"summary"`
		Auth        yamlAuth    `yaml:"auth"`
		MCP         []model.MCP `yaml:"mcp"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	meta.ID = doc.ID
	meta.Title = doc.Title
	meta.Description = doc.Description
	meta.Summary = doc.Summary
	meta.MCP = doc.MCP

	meta.Auth = doc.Auth.toModel()
	return nil
}

// yamlAuth decodes the auth section, supporting both the canonical schema and
// the legacy layouts described on parseYAML.
type yamlAuth struct {
	inherit    bool
	inheritSet bool
	defaults   *model.Permissions
	roles      map[string]*model.Permissions
	groups     map[string]*model.Permissions
	users      map[string]*model.Permissions
}

func (a *yamlAuth) toModel() model.Auth {
	out := model.Auth{
		Inherit: true, // documented default
		Default: a.defaults,
		Roles:   a.roles,
		Groups:  a.groups,
		Users:   a.users,
	}
	if a.inheritSet {
		out.Inherit = a.inherit
	}
	if out.Default == nil {
		out.Default = &model.Permissions{}
	}
	if out.Users == nil {
		out.Users = make(map[string]*model.Permissions)
	}
	return out
}

// UnmarshalYAML implements yaml.Unmarshaler for the auth mapping.
func (a *yamlAuth) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("auth: expected a mapping, got %s", yamlKind(value))
	}
	a.users = make(map[string]*model.Permissions)
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		val := value.Content[i+1]
		switch key {
		case "inherit":
			b, err := decodeYAMLBool(val)
			if err != nil {
				return fmt.Errorf("auth.inherit: %w", err)
			}
			a.inherit = b
			a.inheritSet = true
		case "default":
			perms, err := decodePermissions(val)
			if err != nil {
				return fmt.Errorf("auth.default: %w", err)
			}
			a.defaults = perms
		case "roles", "groups":
			m, err := decodePermissionsMap(val)
			if err != nil {
				return fmt.Errorf("auth.%s: %w", key, err)
			}
			if key == "roles" {
				a.roles = m
			} else {
				a.groups = m
			}
		case "users":
			if err := a.decodeUsers(val); err != nil {
				return fmt.Errorf("auth.users: %w", err)
			}
		default:
			// Legacy bare-user form: any other key under auth is a user ID.
			perms, err := decodePermissions(val)
			if err != nil {
				return fmt.Errorf("auth.%q: %w", key, err)
			}
			a.users[key] = perms
		}
	}
	return nil
}

// decodeUsers accepts the map form (userID → permissions) and the legacy list
// form ([{user_id: ..., can_read: ...}, ...]).
func (a *yamlAuth) decodeUsers(value *yaml.Node) error {
	switch value.Kind {
	case yaml.MappingNode:
		m, err := decodePermissionsMap(value)
		if err != nil {
			return err
		}
		for id, perms := range m {
			a.users[id] = perms
		}
		return nil
	case yaml.SequenceNode:
		for _, item := range value.Content {
			if item.Kind != yaml.MappingNode {
				return fmt.Errorf("list entries must be mappings with a user_id")
			}
			var userID string
			perms := &model.Permissions{}
			for i := 0; i+1 < len(item.Content); i += 2 {
				key := item.Content[i].Value
				val := item.Content[i+1]
				if key == "user_id" {
					userID = strings.TrimSpace(val.Value)
					continue
				}
				if err := setPermissionField(perms, key, val); err != nil {
					return err
				}
			}
			if userID == "" {
				return fmt.Errorf("list entry is missing user_id")
			}
			a.users[userID] = perms
		}
		return nil
	default:
		return fmt.Errorf("expected a mapping or list, got %s", yamlKind(value))
	}
}

// decodePermissionsMap decodes name → permissions mappings (roles/groups/users).
func decodePermissionsMap(value *yaml.Node) (map[string]*model.Permissions, error) {
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a mapping, got %s", yamlKind(value))
	}
	out := make(map[string]*model.Permissions)
	for i := 0; i+1 < len(value.Content); i += 2 {
		name := value.Content[i].Value
		perms, err := decodePermissions(value.Content[i+1])
		if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		out[name] = perms
	}
	return out, nil
}

// decodePermissions decodes one permissions mapping, accepting canonical names
// and legacy aliases. Unknown keys fail loudly.
func decodePermissions(value *yaml.Node) (*model.Permissions, error) {
	if value.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected a permissions mapping, got %s", yamlKind(value))
	}
	perms := &model.Permissions{}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key := value.Content[i].Value
		if err := setPermissionField(perms, key, value.Content[i+1]); err != nil {
			return nil, err
		}
	}
	return perms, nil
}

// setPermissionField assigns one permission key (canonical or legacy alias).
func setPermissionField(perms *model.Permissions, key string, value *yaml.Node) error {
	if key == "perms" {
		perms.Perms = strings.TrimSpace(value.Value)
		return nil
	}
	b, err := decodeYAMLBool(value)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	switch key {
	case "read", "can_read":
		perms.Read = &b
	case "write", "can_edit":
		perms.Write = &b
	case "execute", "can_access_next":
		perms.Execute = &b
	case "see", "can_see":
		perms.See = &b
	case "visible_docs", "visible_in_docs":
		perms.VisibleDocs = &b
	case "visible_graph", "visible_in_graph":
		perms.VisibleGraph = &b
	default:
		return fmt.Errorf("unknown permission key %q", key)
	}
	return nil
}

// decodeYAMLBool accepts YAML booleans plus the legacy textual forms
// (yes/no/1/0) older trees used. Anything else is an error.
func decodeYAMLBool(value *yaml.Node) (bool, error) {
	if value.Kind != yaml.ScalarNode {
		return false, fmt.Errorf("expected a boolean, got %s", yamlKind(value))
	}
	switch strings.ToLower(strings.TrimSpace(value.Value)) {
	case "true", "yes", "1", "on":
		return true, nil
	case "false", "no", "0", "off", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value %q", value.Value)
	}
}

// yamlKind names a node kind for error messages.
func yamlKind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "list"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "unknown"
	}
}
