package debuger

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/sashabaranov/go-openai"
)

// FormatTime formats a time for display
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	return t.Format("2006-01-02 15:04:05")
}

// FormatDuration formats duration since a time
func FormatDuration(t time.Time) string {
	if t.IsZero() {
		return "N/A"
	}
	duration := time.Since(t)
	if duration < time.Minute {
		return fmt.Sprintf("%d seconds ago", int(duration.Seconds()))
	} else if duration < time.Hour {
		return fmt.Sprintf("%d minutes ago", int(duration.Minutes()))
	} else if duration < 24*time.Hour {
		return fmt.Sprintf("%.1f hours ago", duration.Hours())
	}
	return fmt.Sprintf("%d days ago", int(duration.Hours()/24))
}

// FormatDurationValue formats a duration value for display
func FormatDurationValue(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	} else if d < time.Hour {
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return fmt.Sprintf("%.1f hours", d.Hours())
}

// FormatMessage formats a ChatCompletionMessage for display
func FormatMessage(msg openai.ChatCompletionMessage) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("<strong>Role:</strong> %s", msg.Role))

	if msg.Content != "" {
		content := template.HTMLEscapeString(msg.Content)
		if len(content) > 1000 {
			content = content[:1000] + "... (truncated)"
		}
		parts = append(parts, fmt.Sprintf("<strong>Content:</strong> %s", content))
	}

	if len(msg.ToolCalls) > 0 {
		toolCallsJSON, _ := json.MarshalIndent(msg.ToolCalls, "", "  ")
		parts = append(parts, fmt.Sprintf("<strong>Tool Calls:</strong> <pre>%s</pre>", template.HTMLEscapeString(string(toolCallsJSON))))
	}

	if msg.ToolCallID != "" {
		parts = append(parts, fmt.Sprintf("<strong>Tool Call ID:</strong> %s", msg.ToolCallID))
	}

	if msg.Name != "" {
		parts = append(parts, fmt.Sprintf("<strong>Function Name:</strong> %s", msg.Name))
	}

	if msg.FunctionCall != nil {
		funcCallJSON, _ := json.MarshalIndent(msg.FunctionCall, "", "  ")
		parts = append(parts, fmt.Sprintf("<strong>Function Call:</strong> <pre>%s</pre>", template.HTMLEscapeString(string(funcCallJSON))))
	}

	return strings.Join(parts, "<br>")
}

// GetModelDisplay returns a formatted model name for display (plain text)
func GetModelDisplay(modelName string) string {
	if modelName == "" {
		return "-"
	}
	return modelName
}

// GetModelDisplayHTML returns a formatted model name with HTML styling
func GetModelDisplayHTML(modelName string) string {
	if modelName == "" {
		return `<span class="text-muted">-</span>`
	}
	return template.HTMLEscapeString(modelName)
}

// TruncateString truncates a string to a maximum length
func TruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Min returns the minimum of two integers
func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Max returns the maximum of two integers
func Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// EscapeHTML escapes a string for safe HTML display
func EscapeHTML(s string) string {
	return template.HTMLEscapeString(s)
}

// EscapeURL escapes a string for safe URL use
func EscapeURL(s string) string {
	return template.URLQueryEscaper(s)
}

// GetRoleBadgeClass returns the Bootstrap badge class for a message role
func GetRoleBadgeClass(role string) string {
	switch role {
	case openai.ChatMessageRoleUser:
		return "bg-primary"
	case openai.ChatMessageRoleAssistant:
		return "bg-success"
	case openai.ChatMessageRoleTool:
		return "bg-warning text-dark"
	case openai.ChatMessageRoleSystem:
		return "bg-info"
	default:
		return "bg-secondary"
	}
}

// SafeSubstring returns a safe substring, handling edge cases
func SafeSubstring(s string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end > len(s) {
		end = len(s)
	}
	if start >= end {
		return ""
	}
	return s[start:end]
}

// JSONPretty formats JSON with indentation
func JSONPretty(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// FormatChars formats character count in human readable format
func FormatChars(chars int) string {
	if chars == 0 {
		return "0"
	}
	if chars < 1000 {
		return fmt.Sprintf("%d", chars)
	}
	if chars < 1000000 {
		return fmt.Sprintf("%.1fK", float64(chars)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(chars)/1000000)
}

// FormatDurationMs formats duration in milliseconds to human readable format
func FormatDurationMs(ms int64) string {
	if ms == 0 {
		return "-"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	seconds := float64(ms) / 1000
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%.1fm", minutes)
	}
	hours := minutes / 60
	return fmt.Sprintf("%.1fh", hours)
}
