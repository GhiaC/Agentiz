package components

import (
	"fmt"
	"html/template"
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

// AgentTypeBadge generates a badge for agent types
func AgentTypeBadge(agentType string) string {
	if agentType == "" {
		return Badge("-", "secondary")
	}
	variant := "secondary"
	switch agentType {
	case "core":
		variant = "danger"
	case "high":
		variant = "primary"
	case "low":
		variant = "success"
	}
	return Badge(agentType, variant)
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
