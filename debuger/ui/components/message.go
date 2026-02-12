package components

import (
	"fmt"
	"html/template"
	"time"

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
	case model.ContentTypePDF:
		return BadgeWithIcon("PDF", "📄", "secondary")
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

// MessageRowConfig holds configuration for message table row display
type MessageRowConfig struct {
	ShowUser    bool   // Show user column with link
	ShowSession bool   // Show session column with link
	BaseURL     string // Base URL for links
}

// DefaultMessageRowConfig returns default configuration for message row
func DefaultMessageRowConfig() MessageRowConfig {
	return MessageRowConfig{
		ShowUser:    true,
		ShowSession: true,
		BaseURL:     "/agentize/debug",
	}
}

// MessageTableColumns returns the column configuration for message table
func MessageTableColumns(config MessageRowConfig) []ColumnConfig {
	columns := []ColumnConfig{
		{Header: "", Center: true, NoWrap: true}, // Expand button
		{Header: "Seq", Center: true, NoWrap: true},
		{Header: "Time", NoWrap: true},
		{Header: "Agent", Center: true, NoWrap: true},
		{Header: "Type", Center: true, NoWrap: true},
		{Header: "Role", Center: true, NoWrap: true},
		{Header: "Content"},
		{Header: "Model", Center: true, NoWrap: true},
	}
	if config.ShowUser {
		columns = append(columns, ColumnConfig{Header: "User", NoWrap: true})
	}
	columns = append(columns,
		ColumnConfig{Header: "Tools", Center: true, NoWrap: true},
		ColumnConfig{Header: "Nonsense", Center: true, NoWrap: true},
	)
	return columns
}

// MessageTableRow renders a message as an expandable table row
// Returns the collapsed row + expanded row (hidden by default)
func MessageTableRow(msg *model.Message, config MessageRowConfig, rowIndex int) string {
	contentPreview := TruncatedText(msg.Content, 100)
	agentBadge := AgentTypeBadgeFromModel(msg.AgentType)
	contentTypeBadge := ContentTypeBadgeFromModel(msg.ContentType)
	roleBadge := RoleBadge(msg.Role)

	// Model display
	modelDisplay := "-"
	if msg.Model != "" {
		modelDisplay = getModelDisplayShort(msg.Model)
	}

	// Tool calls badge
	toolCallDisplay := Badge("-", "secondary")
	if msg.HasToolCalls {
		toolCallDisplay = fmt.Sprintf(`<a href="%s/tool-calls?session=%s" class="btn btn-sm btn-outline-warning">🔧 View</a>`,
			config.BaseURL, template.URLQueryEscaper(msg.SessionID))
	}

	// Nonsense badge
	nonsenseBadge := Badge("-", "secondary")
	if msg.IsNonsense {
		nonsenseBadge = BadgeWithIcon("Nonsense", "⚠️", "warning text-dark")
	}

	// Format time as "ago"
	timeAgo := formatTimeAgo(msg.CreatedAt)

	// Build the collapsed row
	rowID := fmt.Sprintf("msg-row-%d", rowIndex)
	expandBtnID := fmt.Sprintf("msg-expand-%d", rowIndex)

	// SeqID display
	seqDisplay := fmt.Sprintf("%d", msg.SeqID)

	html := fmt.Sprintf(`<tr id="%s">
		<td class="text-center">
			<button class="btn btn-sm btn-outline-secondary" type="button" onclick="toggleMessageRow('%s', '%s')" id="%s">
				<i class="bi bi-chevron-down"></i>
			</button>
		</td>
		<td class="text-center">%s</td>
		<td class="text-nowrap">%s</td>
		<td class="text-center">%s</td>
		<td class="text-center">%s</td>
		<td class="text-center">%s</td>
		<td class="text-break" style="max-width: 300px;">%s</td>
		<td class="text-center">%s</td>`,
		rowID,
		rowID, expandBtnID, expandBtnID,
		seqDisplay,
		timeAgo,
		agentBadge,
		contentTypeBadge,
		roleBadge,
		contentPreview,
		InlineCode(modelDisplay),
	)

	// Add user column if configured
	if config.ShowUser {
		html += fmt.Sprintf(`<td class="text-nowrap">%s</td>`,
			TruncatedLink(msg.UserID, config.BaseURL+"/users/"+template.URLQueryEscaper(msg.UserID), 15))
	}

	html += fmt.Sprintf(`
		<td class="text-center">%s</td>
		<td class="text-center">%s</td>
	</tr>`,
		toolCallDisplay,
		nonsenseBadge,
	)

	// Build the expanded details row (hidden by default)
	// Base columns: expand button, seq, time, agent, type, role, content, model, tools, nonsense = 10
	colSpan := 10
	if config.ShowUser {
		colSpan++
	}

	html += fmt.Sprintf(`<tr id="%s-details" style="display: none;" class="table-light">
		<td colspan="%d">
			<div class="p-3">
				<div class="row">
					<div class="col-md-6">
						<table class="table table-sm table-borderless mb-0">
							<tr><th class="text-muted" style="width: 140px;">Message ID</th><td>%s</td></tr>
							<tr><th class="text-muted">Seq ID</th><td>%d</td></tr>
							<tr><th class="text-muted">Session ID</th><td>%s</td></tr>
							<tr><th class="text-muted">User ID</th><td>%s</td></tr>
							<tr><th class="text-muted">Created At</th><td>%s</td></tr>
							<tr><th class="text-muted">Agent Type</th><td>%s</td></tr>
							<tr><th class="text-muted">Content Type</th><td>%s</td></tr>
							<tr><th class="text-muted">Role</th><td>%s</td></tr>
						</table>
					</div>
					<div class="col-md-6">
						<table class="table table-sm table-borderless mb-0">
							<tr><th class="text-muted" style="width: 140px;">Model</th><td>%s</td></tr>
							<tr><th class="text-muted">Request Model</th><td>%s</td></tr>
							<tr><th class="text-muted">Prompt Tokens</th><td>%d</td></tr>
							<tr><th class="text-muted">Completion Tokens</th><td>%d</td></tr>
							<tr><th class="text-muted">Total Tokens</th><td>%d</td></tr>
							<tr><th class="text-muted">Max Tokens</th><td>%d</td></tr>
							<tr><th class="text-muted">Temperature</th><td>%.2f</td></tr>
							<tr><th class="text-muted">Finish Reason</th><td>%s</td></tr>
							<tr><th class="text-muted">Has Tool Calls</th><td>%s</td></tr>
							<tr><th class="text-muted">Is Nonsense</th><td>%s</td></tr>
						</table>
					</div>
				</div>
				<div class="mt-3">
					<strong class="text-muted">Full Content:</strong>
					<pre class="bg-white border rounded p-2 mt-1" style="white-space: pre-wrap; word-wrap: break-word; max-height: 400px; overflow-y: auto;">%s</pre>
				</div>
			</div>
		</td>
	</tr>`,
		rowID, colSpan,
		InlineCode(msg.MessageID),
		msg.SeqID,
		TruncatedLink(msg.SessionID, config.BaseURL+"/sessions/"+template.URLQueryEscaper(msg.SessionID), 40),
		TruncatedLink(msg.UserID, config.BaseURL+"/users/"+template.URLQueryEscaper(msg.UserID), 40),
		msg.CreatedAt.Format("2006-01-02 15:04:05"),
		agentBadge,
		contentTypeBadge,
		roleBadge,
		InlineCode(msg.Model),
		InlineCode(msg.RequestModel),
		msg.PromptTokens,
		msg.CompletionTokens,
		msg.TotalTokens,
		msg.MaxTokens,
		msg.Temperature,
		getFinishReasonDisplay(msg.FinishReason),
		getBoolBadge(msg.HasToolCalls),
		getBoolBadge(msg.IsNonsense),
		template.HTMLEscapeString(msg.Content),
	)

	return html
}

// MessageTableScript returns the JavaScript needed for expandable rows
func MessageTableScript() string {
	return `
<script>
function toggleMessageRow(rowId, btnId) {
    var detailsRow = document.getElementById(rowId + '-details');
    var btn = document.getElementById(btnId);
    if (detailsRow.style.display === 'none') {
        detailsRow.style.display = 'table-row';
        btn.innerHTML = '<i class="bi bi-chevron-up"></i>';
    } else {
        detailsRow.style.display = 'none';
        btn.innerHTML = '<i class="bi bi-chevron-down"></i>';
    }
}
</script>
`
}

// formatTimeAgo formats time as "X ago" format
func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	duration := time.Since(t)
	if duration < time.Minute {
		return fmt.Sprintf("%ds ago", int(duration.Seconds()))
	} else if duration < time.Hour {
		return fmt.Sprintf("%dm ago", int(duration.Minutes()))
	} else if duration < 24*time.Hour {
		return fmt.Sprintf("%.1fh ago", duration.Hours())
	}
	return fmt.Sprintf("%dd ago", int(duration.Hours()/24))
}

// Helper to format bool as badge
func getBoolBadge(val bool) string {
	if val {
		return BadgeWithIcon("Yes", "✅", "success")
	}
	return Badge("No", "secondary")
}

// Helper to display finish reason
func getFinishReasonDisplay(reason string) string {
	if reason == "" {
		return Badge("-", "secondary")
	}
	switch reason {
	case "stop":
		return Badge("stop", "success")
	case "tool_calls":
		return Badge("tool_calls", "warning text-dark")
	case "length":
		return Badge("length", "danger")
	default:
		return Badge(reason, "secondary")
	}
}
