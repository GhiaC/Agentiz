package components

import (
	"fmt"
	"html/template"

	"github.com/ghiac/agentize/model"
)

// MessageDisplayConfig holds configuration for message display
type MessageDisplayConfig struct {
	ShowMessageID   bool
	ShowModel       bool
	ShowAgentType   bool
	ShowContentType bool
	ShowToolCalls   bool
	ShowNonsense    bool
	ShowTime        bool
	ContentMaxLen   int
	SessionID       string // Used for tool calls link
}

// DefaultMessageDisplayConfig returns default configuration for message display
func DefaultMessageDisplayConfig() MessageDisplayConfig {
	return MessageDisplayConfig{
		ShowMessageID:   true,
		ShowModel:       true,
		ShowAgentType:   true,
		ShowContentType: true,
		ShowToolCalls:   true,
		ShowNonsense:    true,
		ShowTime:        true,
		ContentMaxLen:   200,
	}
}

// MessageCard renders a message as a list-group-item card
func MessageCard(msg *model.Message, config MessageDisplayConfig) string {
	contentDisplay := ExpandableWithPreview(msg.Content, config.ContentMaxLen)

	// Build badges
	var badges string

	// Role badge (always shown)
	badges += RoleBadge(msg.Role)

	// Agent type badge
	if config.ShowAgentType {
		badges += " " + AgentTypeBadgeFromModel(msg.AgentType)
	}

	// Content type badge
	if config.ShowContentType {
		badges += " " + ContentTypeBadgeFromModel(msg.ContentType)
	}

	// Tool calls badge with link
	if config.ShowToolCalls && msg.HasToolCalls {
		if config.SessionID != "" {
			badges += fmt.Sprintf(` <a href="/agentize/debug/tool-calls?session=%s" class="badge bg-danger text-decoration-none">🔧 Tool Calls</a>`,
				template.URLQueryEscaper(config.SessionID))
		} else {
			badges += " " + Badge("Has Tool Calls", "danger")
		}
	}

	// Nonsense badge
	if config.ShowNonsense && msg.IsNonsense {
		badges += " " + BadgeWithIcon("Nonsense", "⚠️", "warning text-dark")
	}

	// Model badge
	modelDisplay := ""
	if config.ShowModel && msg.Model != "" {
		modelDisplay = Badge("Model: "+getModelDisplayShort(msg.Model), "secondary")
	}

	// Time display
	timeDisplay := ""
	if config.ShowTime {
		timeDisplay = formatTimeShort(msg.CreatedAt)
	}

	// Message ID
	messageIDDisplay := ""
	if config.ShowMessageID {
		messageIDDisplay = fmt.Sprintf(`<small class="text-muted">Message ID: %s</small>`, InlineCode(msg.MessageID))
	}

	return fmt.Sprintf(`
<div class="list-group-item">
    <div class="d-flex w-100 justify-content-between align-items-start mb-2">
        <div>
            %s
            %s
        </div>
        <small class="text-muted">%s</small>
    </div>
    <p class="mb-2 text-justify">%s</p>
    %s
</div>`,
		badges,
		modelDisplay,
		timeDisplay,
		contentDisplay,
		messageIDDisplay,
	)
}

// MessageListStart starts a message list container
func MessageListStart() string {
	return ListGroupStart()
}

// MessageListEnd ends a message list container
func MessageListEnd() string {
	return ListGroupEnd()
}

// AgentTypeBadgeFromModel returns a badge for agent type from model.AgentType
func AgentTypeBadgeFromModel(agentType model.AgentType) string {
	switch agentType {
	case model.AgentTypeCore:
		return Badge("Core", "primary")
	case model.AgentTypeLow:
		return Badge("Low", "info")
	case model.AgentTypeHigh:
		return Badge("High", "success")
	case model.AgentTypeUser:
		return Badge("User", "warning")
	default:
		if agentType == "" {
			return Badge("-", "secondary")
		}
		return Badge(string(agentType), "secondary")
	}
}

// AgentTypeBadgeFromString returns a badge for agent type from string
func AgentTypeBadgeFromString(agentType string) string {
	return AgentTypeBadgeFromModel(model.AgentType(agentType))
}

// ContentTypeBadgeFromModel returns a badge for content type from model.ContentType
func ContentTypeBadgeFromModel(contentType model.ContentType) string {
	switch contentType {
	case model.ContentTypeText:
		return BadgeWithIcon("Text", "📝", "light text-dark")
	case model.ContentTypeAudio:
		return BadgeWithIcon("Audio", "🎵", "warning text-dark")
	case model.ContentTypeImage:
		return BadgeWithIcon("Image", "🖼️", "info")
	default:
		if contentType == "" {
			return Badge("-", "secondary")
		}
		return Badge(string(contentType), "secondary")
	}
}

// ContentTypeBadgeFromString returns a badge for content type from string
func ContentTypeBadgeFromString(contentType string) string {
	return ContentTypeBadgeFromModel(model.ContentType(contentType))
}

// getModelDisplayShort returns a shortened model display name
func getModelDisplayShort(model string) string {
	if model == "" {
		return "-"
	}
	// Truncate long model names
	if len(model) > 20 {
		return model[:17] + "..."
	}
	return model
}

// formatTimeShort formats time for message display
func formatTimeShort(t interface{ Format(string) string }) string {
	return t.Format("2006-01-02 15:04:05")
}
