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

	content := ui.ContainerStart()

	// Show breadcrumb if filtered
	if sessionID != "" {
		content += components.Breadcrumb([]components.BreadcrumbItem{
			{Label: "Dashboard", URL: "/agentize/debug"},
			{Label: "Sessions", URL: "/agentize/debug/sessions"},
			{Label: sessionID, URL: "/agentize/debug/sessions/" + template.URLQueryEscaper(sessionID)},
			{Label: "Tool Calls", Active: true},
		})
	}

	content += ui.CardStartWithCount(title, "tools", totalItems)

	if len(toolCalls) == 0 {
		content += components.InfoAlert("No tool calls found.")
	} else {
		// Configure tool call row display
		rowConfig := components.DefaultToolCallRowConfig()
		rowConfig.ShowUser = true
		rowConfig.BaseURL = "/agentize/debug"

		columns := components.ToolCallTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for i, tc := range paginatedToolCalls {
			content += components.ToolCallTableRow(&tc, rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.ToolCallTableScript()
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, baseURL)
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Tool Calls") + ui.NavbarAndBody("/agentize/debug/tool-calls", content) + ui.Footer(), nil
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

	content := ui.ContainerStart()

	// Breadcrumb
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Tool Calls", URL: "/agentize/debug/tool-calls"},
		{Label: tc.FunctionName, Active: true},
	})

	// Agent type badge
	agentBadge := components.AgentTypeBadgeFromModel(tc.AgentType)

	// Tool Call Info Card
	content += ui.CardStart("Tool Call Details", "tools")
	content += `<div class="row">`

	// Left column - Basic Info
	content += `<div class="col-md-6">`
	content += `<table class="table table-sm">`
	content += fmt.Sprintf(`<tr><th class="w-25">Tool ID</th><td>%s</td></tr>`, components.InlineCode(tc.ToolID))
	content += fmt.Sprintf(`<tr><th>Tool Call ID</th><td>%s</td></tr>`, components.InlineCode(tc.ToolCallID))
	content += fmt.Sprintf(`<tr><th>Function</th><td>%s</td></tr>`, components.InlineCode(tc.FunctionName))
	content += fmt.Sprintf(`<tr><th>Agent Type</th><td>%s</td></tr>`, agentBadge)
	content += fmt.Sprintf(`<tr><th>Duration</th><td>%s</td></tr>`, debuger.FormatDurationMs(tc.DurationMs))
	content += fmt.Sprintf(`<tr><th>Status</th><td>%s</td></tr>`, tc.Status)
	if tc.Error != "" {
		content += fmt.Sprintf(`<tr><th class="text-danger">Error</th><td class="text-danger">%s</td></tr>`, template.HTMLEscapeString(tc.Error))
	}
	content += fmt.Sprintf(`<tr><th>Created At</th><td>%s</td></tr>`, debuger.FormatTime(tc.CreatedAt))
	content += fmt.Sprintf(`<tr><th>Updated At</th><td>%s</td></tr>`, debuger.FormatTime(tc.UpdatedAt))
	content += `</table>`
	content += `</div>`

	// Right column - Links
	content += `<div class="col-md-6">`
	content += `<table class="table table-sm">`
	content += fmt.Sprintf(`<tr><th class="w-25">User</th><td>%s</td></tr>`,
		components.TruncatedLink(tc.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(tc.UserID), 30))
	content += fmt.Sprintf(`<tr><th>Session</th><td>%s</td></tr>`,
		components.OpenButton("/agentize/debug/sessions/"+template.URLQueryEscaper(tc.SessionID)))
	content += fmt.Sprintf(`<tr><th>Message ID</th><td>%s</td></tr>`, components.InlineCode(tc.MessageID))
	content += `</table>`
	content += `</div>`

	content += `</div>`
	content += ui.CardEnd()

	// Arguments Card
	content += ui.CardStart("Arguments", "code-slash")
	content += `<pre class="bg-light p-3 rounded" style="white-space: pre-wrap; word-wrap: break-word; max-height: 400px; overflow-y: auto;">`
	content += template.HTMLEscapeString(tc.Arguments)
	content += `</pre>`
	content += ui.CardEnd()

	// Response Card
	responseTitle := "Response"
	if tc.ResponseLength > 0 {
		responseTitle = fmt.Sprintf("Response (%s chars)", debuger.FormatChars(tc.ResponseLength))
	}
	content += ui.CardStart(responseTitle, "reply")
	if tc.Response == "" {
		content += components.InfoAlert("No response recorded yet.")
	} else {
		content += `<pre class="bg-light p-3 rounded" style="white-space: pre-wrap; word-wrap: break-word; max-height: 400px; overflow-y: auto;">`
		content += template.HTMLEscapeString(tc.Response)
		content += `</pre>`
	}
	content += ui.CardEnd()

	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Tool Call: "+tc.FunctionName) + ui.NavbarAndBody("/agentize/debug/tool-calls", content) + ui.Footer(), nil
}
