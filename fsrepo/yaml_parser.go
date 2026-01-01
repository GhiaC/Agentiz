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
	var policyStarted bool
	var routingStarted bool
	var memoryStarted bool

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Handle sections
		if strings.HasSuffix(line, ":") {
			section := strings.TrimSuffix(line, ":")
			currentSection = strings.TrimSpace(section)
			if currentSection == "policy" {
				policyStarted = true
				routingStarted = false
				memoryStarted = false
			} else if currentSection == "routing" {
				routingStarted = true
				policyStarted = false
				memoryStarted = false
			} else if currentSection == "memory" {
				memoryStarted = true
				policyStarted = false
				routingStarted = false
			} else {
				// Reset all flags for other sections
				policyStarted = false
				routingStarted = false
				memoryStarted = false
			}
			continue
		}

		// Handle array items (lines starting with -) in routing section
		if strings.HasPrefix(line, "-") && routingStarted {
			childName := strings.TrimSpace(strings.TrimPrefix(line, "-"))
			childName = strings.Trim(childName, `"'`)
			if childName != "" {
				meta.Policy.Routing.Children = append(meta.Policy.Routing.Children, childName)
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
			case policyStarted:
				switch key {
				case "can_advance":
					meta.Policy.CanAdvance = parseBool(value)
				case "advance_condition":
					meta.Policy.AdvanceCondition = value
				case "max_open_files":
					meta.Policy.MaxOpenFiles = parseInt(value, 20)
				}
			case routingStarted:
				if key == "mode" {
					meta.Policy.Routing.Mode = value
				} else if key == "children" {
					// Children is an array - parse it
					meta.Policy.Routing.Children = parseStringArray(value)
				}
			case memoryStarted:
				if key == "persist" {
					// Parse array: ["summary", "facts"]
					meta.Policy.Memory.Persist = parseStringArray(value)
				}
			}
		}
	}

	// Set defaults if not specified
	if meta.Policy.Routing.Mode == "" {
		meta.Policy.Routing.Mode = "sequential"
	}
	if meta.Policy.MaxOpenFiles == 0 {
		meta.Policy.MaxOpenFiles = 20
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
