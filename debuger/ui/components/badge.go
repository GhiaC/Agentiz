package components

import (
	"fmt"
	"html/template"

	"github.com/ghiac/agentize/model"
)

// Badge generates a Bootstrap badge
func Badge(text, variant string) string {
	return fmt.Sprintf(`<span class="badge bg-%s">%s</span>`, variant, template.HTMLEscapeString(text))
}

// BadgeWithIcon generates a badge with an icon prefix
func BadgeWithIcon(text, icon, variant string) string {
	return fmt.Sprintf(`<span class="badge bg-%s">%s %s</span>`, variant, icon, template.HTMLEscapeString(text))
}

// RoleBadge generates a badge for message roles
func RoleBadge(role string) string {
	variant := "secondary"
	switch role {
	case "user":
		variant = "primary"
	case "assistant":
		variant = "success"
	case "tool":
		variant = "warning text-dark"
	case "system":
		variant = "info"
	}
	return Badge(role, variant)
}

// StatusBadge generates a badge for status values
func StatusBadge(status string) string {
	variant := "secondary"
	icon := ""
	switch status {
	case "success":
		variant = "success"
		icon = "✅ "
	case "failed":
		variant = "danger"
		icon = "❌ "
	case "pending":
		variant = "warning text-dark"
		icon = "⏳ "
	case "active":
		variant = "success"
		icon = "✅ "
	case "banned":
		variant = "danger"
		icon = "🚫 "
	case "open":
		variant = "success"
		icon = "✅ "
	case "closed":
		variant = "secondary"
		icon = "❌ "
	}
	return BadgeWithIcon(status, icon, variant)
}

// CountBadge generates a count badge
func CountBadge(count int, variant string) string {
	return fmt.Sprintf(`<span class="badge bg-%s">%d</span>`, variant, count)
}

// TokenBadge generates a token usage badge
func TokenBadge(total, prompt, completion int) string {
	return fmt.Sprintf(`<span class="badge bg-info">Total: %d</span><br><small class="text-muted">Prompt: %d, Completion: %d</small>`,
		total, prompt, completion)
}

// agentTypeStyle describes how one agent type is presented across the debug UI:
// its display label, the soft-pill badge variant, and the session-table row
// accent class. Keeping these together gives badges and table rows a single
// source of truth for agent-type colour-coding, so a row's accent rail always
// matches its badge. The row classes themselves are styled in debuger/ui/styles.go.
type agentTypeStyle struct {
	label    string // human-facing label
	badge    string // Bootstrap badge variant (re-skinned as a soft pill in styles.go)
	rowClass string // session-table row accent class (.row-agent-*)
}

var agentTypeStyles = map[model.AgentType]agentTypeStyle{
	model.AgentTypeCore: {label: "Core", badge: "primary", rowClass: "row-agent-core"},
	model.AgentTypeHigh: {label: "High", badge: "success", rowClass: "row-agent-high"},
	model.AgentTypeLow:  {label: "Low", badge: "info", rowClass: "row-agent-low"},
	model.AgentTypeUser: {label: "User", badge: "warning", rowClass: "row-agent-user"},
}

// AgentTypeBadge generates a badge for agent types (string). Use AgentTypeBadgeFromModel for model.AgentType.
func AgentTypeBadge(agentType string) string {
	return AgentTypeBadgeFromString(agentType)
}

// AgentTypeBadgeFromModel returns a soft-pill badge for an agent type.
func AgentTypeBadgeFromModel(agentType model.AgentType) string {
	if s, ok := agentTypeStyles[agentType]; ok {
		return Badge(s.label, s.badge)
	}
	if agentType == "" {
		return Badge("-", "secondary")
	}
	return Badge(string(agentType), "secondary")
}

// AgentTypeBadgeFromString returns a badge for agent type from string.
func AgentTypeBadgeFromString(agentType string) string {
	return AgentTypeBadgeFromModel(model.AgentType(agentType))
}

// AgentTypeRowClass returns the session-table row class that colour-codes a row
// by its agent type: a slim accent rail matching the agent's badge (plus a faint
// wash for Core, the primary agent). Empty or unknown types get no accent.
// The classes are defined in debuger/ui/styles.go.
func AgentTypeRowClass(agentType model.AgentType) string {
	if s, ok := agentTypeStyles[agentType]; ok {
		return s.rowClass
	}
	return ""
}

// BoolBadge generates a badge for boolean values
func BoolBadge(value bool, trueText, falseText string) string {
	if value {
		return Badge(trueText, "success")
	}
	return Badge(falseText, "secondary")
}

// YesNoBadge generates a Yes/No badge
func YesNoBadge(value bool) string {
	return BoolBadge(value, "Yes", "No")
}

// ActiveInactiveBadge generates an Active/Inactive badge
func ActiveInactiveBadge(active bool) string {
	if active {
		return BadgeWithIcon("Active", "✅", "success")
	}
	return BadgeWithIcon("Inactive", "❌", "secondary")
}

// TagBadges generates multiple badges from tags
func TagBadges(tags []string) string {
	if len(tags) == 0 {
		return "-"
	}
	html := ""
	for _, tag := range tags {
		html += Badge(tag, "info") + " "
	}
	return html
}
