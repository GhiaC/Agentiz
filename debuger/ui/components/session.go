package components

import (
	"fmt"
	"html/template"
	"time"

	"github.com/ghiac/agentize/model"
)

// SessionRowConfig holds configuration for session table row display
type SessionRowConfig struct {
	ShowUser      bool                       // Show user column with link
	BaseURL       string                     // Base URL for links
	GetFilesCount func(sessionID string) int // Function to get files count
}

// DefaultSessionRowConfig returns default configuration for session row
func DefaultSessionRowConfig() SessionRowConfig {
	return SessionRowConfig{
		ShowUser:      true,
		BaseURL:       "/agentize/debug",
		GetFilesCount: func(sessionID string) int { return 0 },
	}
}

// SessionTableColumns returns the column configuration for session table
func SessionTableColumns(config SessionRowConfig) []ColumnConfig {
	columns := []ColumnConfig{
		{Header: "", Center: true, NoWrap: true},      // Expand button
		{Header: "Time", NoWrap: true},                // Updated time (ago format)
		{Header: "Agent", Center: true, NoWrap: true}, // Agent type badge
		{Header: "Title"},                             // Session title
		{Header: "Model", Center: true, NoWrap: true}, // Model name
		{Header: "Msgs", Center: true, NoWrap: true},  // Message count
	}
	if config.ShowUser {
		columns = append(columns, ColumnConfig{Header: "User", NoWrap: true})
	}
	columns = append(columns,
		ColumnConfig{Header: "Status", Center: true, NoWrap: true}, // In Progress / Summarized
		ColumnConfig{Header: "", Center: true, NoWrap: true},       // Open button
	)
	return columns
}

// SessionTableRow renders a session as an expandable table row
// Returns the collapsed row + expanded row (hidden by default)
func SessionTableRow(session *model.Session, config SessionRowConfig, rowIndex int) string {
	// Title
	title := session.Title
	if title == "" {
		title = "Untitled"
	}
	if len(title) > 40 {
		title = title[:37] + "..."
	}

	// Agent type badge
	agentBadge := AgentTypeBadge(string(session.AgentType))

	// Model display
	modelDisplay := "-"
	if session.Model != "" {
		modelDisplay = getModelDisplayShort(session.Model)
	}

	// Message count (active only)
	msgCount := 0
	if session.ConversationState != nil {
		msgCount = len(session.ConversationState.Msgs)
	}

	// Format time as "ago"
	timeAgo := formatTimeAgo(session.UpdatedAt)

	// Status badges
	var statusBadges string
	if session.ConversationState != nil && session.ConversationState.InProgress {
		statusBadges = Badge("⏳ Active", "warning text-dark")
	} else if !session.SummarizedAt.IsZero() {
		statusBadges = Badge("📋 Summarized", "success")
	} else {
		statusBadges = Badge("Idle", "secondary")
	}

	// Row styling
	rowClass := ""
	if session.AgentType == model.AgentTypeCore {
		rowClass = "table-danger"
	}

	// Build the collapsed row
	rowID := fmt.Sprintf("session-row-%d", rowIndex)
	expandBtnID := fmt.Sprintf("session-expand-%d", rowIndex)

	html := fmt.Sprintf(`<tr id="%s" class="%s">
		<td class="text-center">
			<button class="btn btn-sm btn-outline-secondary" type="button" onclick="toggleSessionRow('%s', '%s')" id="%s">
				<i class="bi bi-chevron-down"></i>
			</button>
		</td>
		<td class="text-nowrap">%s</td>
		<td class="text-center">%s</td>
		<td class="text-break" style="max-width: 250px;">%s</td>
		<td class="text-center">%s</td>
		<td class="text-center">%s</td>`,
		rowID, rowClass,
		rowID, expandBtnID, expandBtnID,
		timeAgo,
		agentBadge,
		template.HTMLEscapeString(title),
		InlineCode(modelDisplay),
		CountBadge(msgCount, "primary"),
	)

	// Add user column if configured
	if config.ShowUser {
		html += fmt.Sprintf(`<td class="text-nowrap">%s</td>`,
			TruncatedLink(session.UserID, config.BaseURL+"/users/"+template.URLQueryEscaper(session.UserID), 15))
	}

	html += fmt.Sprintf(`
		<td class="text-center">%s</td>
		<td class="text-center">%s</td>
	</tr>`,
		statusBadges,
		OpenButton(config.BaseURL+"/sessions/"+template.URLQueryEscaper(session.SessionID)),
	)

	// Build the expanded details row (hidden by default)
	// Base columns: expand, time, agent, title, model, msgs, status, open = 8
	colSpan := 8
	if config.ShowUser {
		colSpan++
	}

	// Calculate message counts
	activeMsgs := 0
	if session.ConversationState != nil {
		activeMsgs = len(session.ConversationState.Msgs)
	}
	archivedMsgs := len(session.SummarizedMessages)
	if len(session.ExMsgs) > archivedMsgs {
		archivedMsgs = len(session.ExMsgs)
	}

	// Summary display
	summaryDisplay := "-"
	if session.Summary != "" {
		if len(session.Summary) > 200 {
			summaryDisplay = session.Summary[:197] + "..."
		} else {
			summaryDisplay = session.Summary
		}
	}

	// Tags display
	tagsDisplay := "-"
	if len(session.Tags) > 0 {
		tagsDisplay = TagBadges(session.Tags)
	}

	// Summarized at display
	summarizedAtDisplay := "-"
	if !session.SummarizedAt.IsZero() {
		summarizedAtDisplay = session.SummarizedAt.Format("2006-01-02 15:04:05") + " (" + formatTimeAgo(session.SummarizedAt) + ")"
	}

	// Get files count if function provided
	filesCount := 0
	if config.GetFilesCount != nil {
		filesCount = config.GetFilesCount(session.SessionID)
	}

	// In progress badge
	inProgressDisplay := Badge("No", "secondary")
	if session.ConversationState != nil && session.ConversationState.InProgress {
		inProgressDisplay = BadgeWithIcon("Yes", "⏳", "warning text-dark")
	}

	html += fmt.Sprintf(`<tr id="%s-details" style="display: none;" class="table-light">
		<td colspan="%d">
			<div class="p-3">
				<div class="row">
					<div class="col-md-6">
						<table class="table table-sm table-borderless mb-0">
							<tr><th class="text-muted" style="width: 140px;">Session ID</th><td>%s</td></tr>
							<tr><th class="text-muted">User ID</th><td>%s</td></tr>
							<tr><th class="text-muted">Agent Type</th><td>%s</td></tr>
							<tr><th class="text-muted">Model</th><td>%s</td></tr>
							<tr><th class="text-muted">Title</th><td>%s</td></tr>
							<tr><th class="text-muted">Active Messages</th><td>%s</td></tr>
							<tr><th class="text-muted">Archived Messages</th><td>%s</td></tr>
							<tr><th class="text-muted">Message Seq</th><td>%d</td></tr>
							<tr><th class="text-muted">Tool Seq</th><td>%d</td></tr>
							<tr><th class="text-muted">Opened Files</th><td>%s</td></tr>
						</table>
					</div>
					<div class="col-md-6">
						<table class="table table-sm table-borderless mb-0">
							<tr><th class="text-muted" style="width: 140px;">Created At</th><td>%s</td></tr>
							<tr><th class="text-muted">Updated At</th><td>%s</td></tr>
							<tr><th class="text-muted">Summarized At</th><td>%s</td></tr>
							<tr><th class="text-muted">In Progress</th><td>%s</td></tr>
							<tr><th class="text-muted">Tags</th><td>%s</td></tr>
						</table>
					</div>
				</div>
				<div class="mt-3">
					<strong class="text-muted">Summary:</strong>
					<div class="bg-white border rounded p-2 mt-1" style="white-space: pre-wrap; word-wrap: break-word;">%s</div>
				</div>
				<div class="mt-3 d-flex gap-2">
					<a href="%s/sessions/%s" class="btn btn-sm btn-primary"><i class="bi bi-box-arrow-up-right"></i> View Details</a>
					<a href="%s/messages?session=%s" class="btn btn-sm btn-outline-secondary"><i class="bi bi-chat-dots"></i> Messages</a>
					<a href="%s/tool-calls?session=%s" class="btn btn-sm btn-outline-secondary"><i class="bi bi-tools"></i> Tool Calls</a>
				</div>
			</div>
		</td>
	</tr>`,
		rowID, colSpan,
		InlineCode(session.SessionID),
		TruncatedLink(session.UserID, config.BaseURL+"/users/"+template.URLQueryEscaper(session.UserID), 40),
		agentBadge,
		InlineCode(session.Model),
		template.HTMLEscapeString(session.Title),
		CountBadge(activeMsgs, "primary"),
		CountBadge(archivedMsgs, "secondary"),
		session.MessageSeq,
		session.ToolSeq,
		CountBadge(filesCount, "info"),
		formatDateTime(session.CreatedAt),
		formatDateTime(session.UpdatedAt),
		summarizedAtDisplay,
		inProgressDisplay,
		tagsDisplay,
		template.HTMLEscapeString(summaryDisplay),
		config.BaseURL, template.URLQueryEscaper(session.SessionID),
		config.BaseURL, template.URLQueryEscaper(session.SessionID),
		config.BaseURL, template.URLQueryEscaper(session.SessionID),
	)

	return html
}

// SessionTableScript returns the JavaScript needed for expandable session rows
func SessionTableScript() string {
	return `
<script>
function toggleSessionRow(rowId, btnId) {
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

// formatDateTime formats time for display
func formatDateTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}
