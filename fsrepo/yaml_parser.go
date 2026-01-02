package fsrepo

import (
	"agentize/model"
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
	var routingStarted bool
	var currentUserID string
	var currentPerms *model.Permissions

	// Initialize Users map if nil
	if meta.Auth.Users == nil {
		meta.Auth.Users = make(map[string]*model.Permissions)
	}

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
				routingStarted = false
				currentUserID = ""
				currentPerms = nil
			} else if currentSection == "routing" {
				routingStarted = true
				authStarted = false
				currentUserID = ""
				currentPerms = nil
			} else if authStarted && !strings.Contains(section, ":") {
				// This might be a user ID (quoted or unquoted)
				userID := strings.Trim(section, `"'`)
				if userID != "" {
					currentUserID = userID
					currentPerms = &model.Permissions{}
					meta.Auth.Users[currentUserID] = currentPerms
				}
			} else {
				// Reset all flags for other sections
				authStarted = false
				routingStarted = false
				currentUserID = ""
				currentPerms = nil
			}
			continue
		}

		// Handle array items (lines starting with -) in routing section
		if strings.HasPrefix(line, "-") && routingStarted {
			childName := strings.TrimSpace(strings.TrimPrefix(line, "-"))
			childName = strings.Trim(childName, `"'`)
			if childName != "" {
				meta.Routing.Children = append(meta.Routing.Children, childName)
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
			case currentPerms != nil && currentUserID != "":
				// Parse user permissions
				switch key {
				case "perms":
					currentPerms.Perms = value
				case "read":
					b := parseBool(value)
					currentPerms.Read = &b
				case "write":
					b := parseBool(value)
					currentPerms.Write = &b
				case "execute":
					b := parseBool(value)
					currentPerms.Execute = &b
				case "see":
					b := parseBool(value)
					currentPerms.See = &b
				case "visible_docs":
					b := parseBool(value)
					currentPerms.VisibleDocs = &b
				case "visible_graph":
					b := parseBool(value)
					currentPerms.VisibleGraph = &b
				}
			case routingStarted:
				if key == "mode" {
					meta.Routing.Mode = value
				} else if key == "children" {
					// Children is an array - parse it
					meta.Routing.Children = parseStringArray(value)
				}
			}
		}
	}

	// Set defaults if not specified
	if meta.Routing.Mode == "" {
		meta.Routing.Mode = "sequential"
	}

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
