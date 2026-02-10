package pages

import (
	"fmt"
	"html/template"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

// RenderToolCalls generates the tool calls list HTML page
// sessionID is an optional filter
func RenderToolCalls(handler *debuger.DebugHandler, page int, sessionID string) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	var dbToolCalls []*model.ToolCall
	var err error
	var title string
	var baseURL string

	// Apply filter based on session query param
	if sessionID != "" {
		dbToolCalls, err = dp.GetToolCallsBySession(sessionID)
		title = "Tool Calls for Session: " + sessionID
		baseURL = "/agentize/debug/tool-calls?session=" + template.URLQueryEscaper(sessionID)
	} else {
		dbToolCalls, err = dp.GetAllToolCalls()
		title = "All Tool Calls"
		baseURL = "/agentize/debug/tool-calls"
	}
	if err != nil {
		return "", fmt.Errorf("failed to get tool calls: %w", err)
	}

	toolCalls := data.ConvertToolCallsToInfo(dbToolCalls)

	// Pagination
	totalItems := len(toolCalls)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedToolCalls := toolCalls[startIdx:endIdx]

	html := ui.Header("Agentize Debug - Tool Calls")
	html += ui.Navbar("/agentize/debug/tool-calls")
	html += ui.ContainerStart()

	// Show breadcrumb if filtered
	if sessionID != "" {
		html += components.Breadcrumb([]components.BreadcrumbItem{
			{Label: "Dashboard", URL: "/agentize/debug"},
			{Label: "Sessions", URL: "/agentize/debug/sessions"},
			{Label: sessionID, URL: "/agentize/debug/sessions/" + template.URLQueryEscaper(sessionID)},
			{Label: "Tool Calls", Active: true},
		})
	}

	html += ui.CardStartWithCount(title, "tools", totalItems)

	if len(toolCalls) == 0 {
		html += components.InfoAlert("No tool calls found.")
	} else {
		// Configure tool call row display
		rowConfig := components.DefaultToolCallRowConfig()
		rowConfig.ShowUser = true
		rowConfig.BaseURL = "/agentize/debug"

		columns := components.ToolCallTableColumns(rowConfig)
		html += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for i, tc := range paginatedToolCalls {
			html += components.ToolCallTableRow(&tc, rowConfig, i)
		}

		html += components.TableEnd(true)
		html += components.ToolCallTableScript()
		html += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, baseURL)
	}

	html += ui.CardEnd()
	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}

// RenderToolCallDetail generates a detailed view for a single tool call
func RenderToolCallDetail(handler *debuger.DebugHandler, toolID string) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	tc, err := dp.GetToolCallByToolID(toolID)
	if err != nil {
		return "", fmt.Errorf("failed to get tool call: %w", err)
	}
	if tc == nil {
		return "", fmt.Errorf("tool call not found: %s", toolID)
	}

	html := ui.Header("Agentize Debug - Tool Call: " + tc.FunctionName)
	html += ui.Navbar("/agentize/debug/tool-calls")
	html += ui.ContainerStart()

	// Breadcrumb
	html += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Tool Calls", URL: "/agentize/debug/tool-calls"},
		{Label: tc.FunctionName, Active: true},
	})

	// Agent type badge
	agentBadge := components.AgentTypeBadgeFromModel(tc.AgentType)

	// Tool Call Info Card
	html += ui.CardStart("Tool Call Details", "tools")
	html += `<div class="row">`

	// Left column - Basic Info
	html += `<div class="col-md-6">`
	html += `<table class="table table-sm">`
	html += fmt.Sprintf(`<tr><th class="w-25">Tool ID</th><td>%s</td></tr>`, components.InlineCode(tc.ToolID))
	html += fmt.Sprintf(`<tr><th>Tool Call ID</th><td>%s</td></tr>`, components.InlineCode(tc.ToolCallID))
	html += fmt.Sprintf(`<tr><th>Function</th><td>%s</td></tr>`, components.InlineCode(tc.FunctionName))
	html += fmt.Sprintf(`<tr><th>Agent Type</th><td>%s</td></tr>`, agentBadge)
	html += fmt.Sprintf(`<tr><th>Duration</th><td>%s</td></tr>`, debuger.FormatDurationMs(tc.DurationMs))
	html += fmt.Sprintf(`<tr><th>Created At</th><td>%s</td></tr>`, debuger.FormatTime(tc.CreatedAt))
	html += fmt.Sprintf(`<tr><th>Updated At</th><td>%s</td></tr>`, debuger.FormatTime(tc.UpdatedAt))
	html += `</table>`
	html += `</div>`

	// Right column - Links
	html += `<div class="col-md-6">`
	html += `<table class="table table-sm">`
	html += fmt.Sprintf(`<tr><th class="w-25">User</th><td>%s</td></tr>`,
		components.TruncatedLink(tc.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(tc.UserID), 30))
	html += fmt.Sprintf(`<tr><th>Session</th><td>%s</td></tr>`,
		components.OpenButton("/agentize/debug/sessions/"+template.URLQueryEscaper(tc.SessionID)))
	html += fmt.Sprintf(`<tr><th>Message ID</th><td>%s</td></tr>`, components.InlineCode(tc.MessageID))
	html += `</table>`
	html += `</div>`

	html += `</div>`
	html += ui.CardEnd()

	// Arguments Card
	html += ui.CardStart("Arguments", "code-slash")
	html += `<pre class="bg-light p-3 rounded" style="white-space: pre-wrap; word-wrap: break-word; max-height: 400px; overflow-y: auto;">`
	html += template.HTMLEscapeString(tc.Arguments)
	html += `</pre>`
	html += ui.CardEnd()

	// Response Card
	responseTitle := "Response"
	if tc.ResponseLength > 0 {
		responseTitle = fmt.Sprintf("Response (%s chars)", debuger.FormatChars(tc.ResponseLength))
	}
	html += ui.CardStart(responseTitle, "reply")
	if tc.Response == "" {
		html += components.InfoAlert("No response recorded yet.")
	} else {
		html += `<pre class="bg-light p-3 rounded" style="white-space: pre-wrap; word-wrap: break-word; max-height: 400px; overflow-y: auto;">`
		html += template.HTMLEscapeString(tc.Response)
		html += `</pre>`
	}
	html += ui.CardEnd()

	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}
