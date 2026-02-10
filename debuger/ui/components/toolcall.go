package components

import (
	"fmt"
	"html/template"

	"github.com/ghiac/agentize/debuger"
)

// ToolCallRowConfig holds configuration for tool call table row display
type ToolCallRowConfig struct {
	ShowUser    bool   // Show user column with link
	ShowSession bool   // Show session column with link
	ShowMessage bool   // Show message column with link
	BaseURL     string // Base URL for links
}

// DefaultToolCallRowConfig returns default configuration for tool call row
func DefaultToolCallRowConfig() ToolCallRowConfig {
	return ToolCallRowConfig{
		ShowUser:    true,
		ShowSession: false, // Session ID not needed in list view
		ShowMessage: false, // Usually not needed in list view
		BaseURL:     "/agentize/debug",
	}
}

// ToolCallTableColumns returns the column configuration for tool call table
func ToolCallTableColumns(config ToolCallRowConfig) []ColumnConfig {
	columns := []ColumnConfig{
		{Header: "", Center: true, NoWrap: true},         // Expand button
		{Header: "Time", NoWrap: true},                   // Created time (ago format)
		{Header: "Agent", Center: true, NoWrap: true},    // Agent type badge
		{Header: "Function", NoWrap: true},               // Function name
		{Header: "Arguments"},                            // Arguments preview
		{Header: "Result"},                               // Result preview
		{Header: "Duration", Center: true, NoWrap: true}, // Duration
		{Header: "Status", Center: true, NoWrap: true},   // Success/Failed
	}
	if config.ShowUser {
		columns = append(columns, ColumnConfig{Header: "User", NoWrap: true})
	}
	if config.ShowSession {
		columns = append(columns, ColumnConfig{Header: "Session", NoWrap: true})
	}
	columns = append(columns,
		ColumnConfig{Header: "", Center: true, NoWrap: true}, // Open button
	)
	return columns
}

// ToolCallTableRow renders a tool call as an expandable table row
// Returns the collapsed row + expanded row (hidden by default)
func ToolCallTableRow(tc *debuger.ToolCallInfo, config ToolCallRowConfig, rowIndex int) string {
	// Agent type badge
	agentBadge := AgentTypeBadgeFromString(tc.AgentType)

	// Arguments preview
	argsPreview := TruncatedText(tc.Arguments, 80)

	// Result preview
	resultPreview := TruncatedText(tc.Result, 80)

	// Format time as "ago"
	timeAgo := formatTimeAgo(tc.CreatedAt)

	// Duration display
	durationDisplay := debuger.FormatDurationMs(tc.DurationMs)

	// Status badge
	statusBadge := BadgeWithIcon("Success", "✅", "success")
	if tc.Result == "" {
		statusBadge = Badge("Pending", "warning text-dark")
	} else if len(tc.Result) > 0 && tc.ResultLength == 0 {
		statusBadge = Badge("Empty", "secondary")
	}

	// Build the collapsed row
	rowID := fmt.Sprintf("toolcall-row-%d", rowIndex)
	expandBtnID := fmt.Sprintf("toolcall-expand-%d", rowIndex)

	html := fmt.Sprintf(`<tr id="%s">
		<td class="text-center">
			<button class="btn btn-sm btn-outline-secondary" type="button" onclick="toggleToolCallRow('%s', '%s')" id="%s">
				<i class="bi bi-chevron-down"></i>
			</button>
		</td>
		<td class="text-nowrap">%s</td>
		<td class="text-center">%s</td>
		<td class="text-nowrap">%s</td>
		<td class="text-break" style="max-width: 200px; font-size: 0.9em;">%s</td>
		<td class="text-break" style="max-width: 200px; font-size: 0.9em;">%s</td>
		<td class="text-center">%s</td>
		<td class="text-center">%s</td>`,
		rowID,
		rowID, expandBtnID, expandBtnID,
		timeAgo,
		agentBadge,
		InlineCode(tc.FunctionName),
		argsPreview,
		resultPreview,
		durationDisplay,
		statusBadge,
	)

	// Add user column if configured
	if config.ShowUser {
		html += fmt.Sprintf(`<td class="text-nowrap">%s</td>`,
			TruncatedLink(tc.UserID, config.BaseURL+"/users/"+template.URLQueryEscaper(tc.UserID), 15))
	}

	// Add session column if configured
	if config.ShowSession {
		html += fmt.Sprintf(`<td class="text-nowrap">%s</td>`,
			TruncatedLink(tc.SessionID, config.BaseURL+"/sessions/"+template.URLQueryEscaper(tc.SessionID), 20))
	}

	html += fmt.Sprintf(`
		<td class="text-center">%s</td>
	</tr>`,
		OpenButton(config.BaseURL+"/tool-calls/"+template.URLQueryEscaper(tc.ToolID)),
	)

	// Build the expanded details row (hidden by default)
	// Base columns: expand, time, agent, function, arguments, result, duration, status = 8
	colSpan := 8
	if config.ShowUser {
		colSpan++
	}
	if config.ShowSession {
		colSpan++
	}
	colSpan++ // Open button

	// Result length display
	resultLenDisplay := debuger.FormatChars(tc.ResultLength)

	// Format created/updated times
	createdAtDisplay := formatDateTime(tc.CreatedAt)

	html += fmt.Sprintf(`<tr id="%s-details" style="display: none;" class="table-light">
		<td colspan="%d">
			<div class="p-3">
				<div class="row">
					<div class="col-md-6">
						<table class="table table-sm table-borderless mb-0">
							<tr><th class="text-muted" style="width: 140px;">Tool ID</th><td>%s</td></tr>
							<tr><th class="text-muted">Tool Call ID</th><td>%s</td></tr>
							<tr><th class="text-muted">Function Name</th><td>%s</td></tr>
							<tr><th class="text-muted">Agent Type</th><td>%s</td></tr>
							<tr><th class="text-muted">User ID</th><td>%s</td></tr>
							<tr><th class="text-muted">Session ID</th><td>%s</td></tr>
							<tr><th class="text-muted">Message ID</th><td>%s</td></tr>
						</table>
					</div>
					<div class="col-md-6">
						<table class="table table-sm table-borderless mb-0">
							<tr><th class="text-muted" style="width: 140px;">Created At</th><td>%s</td></tr>
							<tr><th class="text-muted">Duration</th><td>%s</td></tr>
							<tr><th class="text-muted">Result Length</th><td>%s</td></tr>
							<tr><th class="text-muted">Status</th><td>%s</td></tr>
						</table>
					</div>
				</div>
				<div class="mt-3">
					<strong class="text-muted">Arguments:</strong>
					<pre class="bg-white border rounded p-2 mt-1" style="white-space: pre-wrap; word-wrap: break-word; max-height: 300px; overflow-y: auto; font-size: 0.9em;">%s</pre>
				</div>
				<div class="mt-3">
					<strong class="text-muted">Result:</strong>
					<pre class="bg-white border rounded p-2 mt-1" style="white-space: pre-wrap; word-wrap: break-word; max-height: 300px; overflow-y: auto; font-size: 0.9em;">%s</pre>
				</div>
				<div class="mt-3 d-flex gap-2">
					<a href="%s/tool-calls/%s" class="btn btn-sm btn-primary"><i class="bi bi-box-arrow-up-right"></i> View Details</a>
					<a href="%s/sessions/%s" class="btn btn-sm btn-outline-secondary"><i class="bi bi-diagram-3"></i> Session</a>
					<a href="%s/messages?session=%s" class="btn btn-sm btn-outline-secondary"><i class="bi bi-chat-dots"></i> Messages</a>
				</div>
			</div>
		</td>
	</tr>`,
		rowID, colSpan,
		InlineCode(tc.ToolID),
		InlineCode(tc.ToolCallID),
		InlineCode(tc.FunctionName),
		agentBadge,
		TruncatedLink(tc.UserID, config.BaseURL+"/users/"+template.URLQueryEscaper(tc.UserID), 40),
		TruncatedLink(tc.SessionID, config.BaseURL+"/sessions/"+template.URLQueryEscaper(tc.SessionID), 40),
		InlineCode(tc.MessageID),
		createdAtDisplay,
		durationDisplay,
		resultLenDisplay,
		statusBadge,
		template.HTMLEscapeString(tc.Arguments),
		template.HTMLEscapeString(tc.Result),
		config.BaseURL, template.URLQueryEscaper(tc.ToolID),
		config.BaseURL, template.URLQueryEscaper(tc.SessionID),
		config.BaseURL, template.URLQueryEscaper(tc.SessionID),
	)

	return html
}

// ToolCallTableScript returns the JavaScript needed for expandable tool call rows
func ToolCallTableScript() string {
	return `
<script>
function toggleToolCallRow(rowId, btnId) {
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
