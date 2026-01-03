package fsrepo

import (
	"github.com/ghiac/agentize/model"
	"fmt"
	"strings"
)

// parseYAML is a simple YAML parser for MVP
// This is a basic implementation - for production use gopkg.in/yaml.v3
func parseYAML(data []byte, v interface{}) error {
	content := string(data)
	lines := strings.Split(content, "\n")

	meta, ok := v.(*model.NodeMeta)
	if !ok {
		return fmt.Errorf("unsupported type for YAML parsing")
	}

	var currentSection string
	var authStarted bool
	var currentUserID string
	var currentPerms *model.Permissions
	var inDefaultSection bool
	var inheritExplicitlySet bool

	// Initialize Users map if nil
	if meta.Auth.Users == nil {
		meta.Auth.Users = make(map[string]*model.Permissions)
	}

	// Initialize Default permissions if needed
	if meta.Auth.Default == nil {
		meta.Auth.Default = &model.Permissions{}
	}

	// Default inherit to true (as documented)
	meta.Auth.Inherit = true

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Handle sections
		if strings.HasSuffix(line, ":") {
			section := strings.TrimSuffix(line, ":")
			currentSection = strings.TrimSpace(section)
			if currentSection == "auth" {
				authStarted = true
				currentUserID = ""
				currentPerms = nil
				inDefaultSection = false
			} else if authStarted && currentSection == "default" {
				// Handle default section in auth
				inDefaultSection = true
				currentUserID = ""
				currentPerms = meta.Auth.Default
			} else if authStarted && !strings.Contains(section, ":") {
				// This might be a user ID (quoted or unquoted)
				userID := strings.Trim(section, `"'`)
				if userID != "" && userID != "default" {
					inDefaultSection = false
					currentUserID = userID
					currentPerms = &model.Permissions{}
					meta.Auth.Users[currentUserID] = currentPerms
				}
			} else {
				// Reset all flags for other sections
				authStarted = false
				currentUserID = ""
				currentPerms = nil
				inDefaultSection = false
			}
			continue
		}

		// Handle array items in auth.users section (e.g., "- user_id: test")
		if strings.HasPrefix(line, "-") && authStarted {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "-"))
			if strings.Contains(rest, ":") {
				parts := strings.SplitN(rest, ":", 2)
				if len(parts) == 2 {
					key := strings.TrimSpace(parts[0])
					value := strings.TrimSpace(parts[1])
					value = strings.Trim(value, `"'`)
					if key == "user_id" && value != "" {
						currentUserID = value
						currentPerms = &model.Permissions{}
						meta.Auth.Users[currentUserID] = currentPerms
					}
				}
			}
			continue
		}

		// Parse key-value pairs
		if strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			value = strings.Trim(value, `"'`)

			switch {
			case key == "id":
				meta.ID = value
			case key == "title":
				meta.Title = value
			case key == "description":
				meta.Description = value
			case key == "inherit" && authStarted:
				meta.Auth.Inherit = parseBool(value)
				inheritExplicitlySet = true
			case currentPerms != nil && (currentUserID != "" || inDefaultSection):
				// Parse user permissions or default permissions
				switch key {
				case "perms":
					currentPerms.Perms = value
				case "read", "can_read":
					b := parseBool(value)
					currentPerms.Read = &b
				case "write", "can_edit":
					b := parseBool(value)
					currentPerms.Write = &b
				case "execute", "can_access_next":
					b := parseBool(value)
					currentPerms.Execute = &b
				case "see", "can_see":
					b := parseBool(value)
					currentPerms.See = &b
				case "visible_docs", "visible_in_docs":
					b := parseBool(value)
					currentPerms.VisibleDocs = &b
				case "visible_graph", "visible_in_graph":
					b := parseBool(value)
					currentPerms.VisibleGraph = &b
				}
			}
		}
	}

	// Inherit defaults to true if not explicitly set
	// (we already set it to true at the beginning, so if it wasn't explicitly set to false, it stays true)
	_ = inheritExplicitlySet // Keep for future use if needed

	return nil
}

func parseBool(s string) bool {
	s = strings.ToLower(s)
	return s == "true" || s == "yes" || s == "1"
}

func parseInt(s string, defaultValue int) int {
	// Simple integer parsing - for MVP
	// In production, use strconv.Atoi
	if s == "" {
		return defaultValue
	}
	var result int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = result*10 + int(c-'0')
		} else {
			break
		}
	}
	if result == 0 {
		return defaultValue
	}
	return result
}

func parseStringArray(s string) []string {
	// Parse array like: ["summary", "facts"] or [summary, facts]
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		return []string{}
	}

	var result []string
	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"'`)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
