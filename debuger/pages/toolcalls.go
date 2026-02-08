package pages

import (
	"fmt"
	"html/template"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
)

// RenderToolCalls generates the tool calls list HTML page
func RenderToolCalls(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	dbToolCalls, err := dp.GetAllToolCalls()
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

	html += ui.CardStartWithCount("All Tool Calls", "tools", totalItems)

	if len(toolCalls) == 0 {
		html += components.InfoAlert("No tool calls found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Function", NoWrap: true},
			{Header: "Arguments"},
			{Header: "Result"},
			{Header: "User", NoWrap: true},
			{Header: "Session", Center: true, NoWrap: true},
			{Header: "Time", NoWrap: true},
		}
		html += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, tc := range paginatedToolCalls {
			argsDisplay := components.ExpandableWithPreview(tc.Arguments, 100)
			resultDisplay := components.ExpandableWithPreview(tc.Result, 100)

			html += fmt.Sprintf(`<tr>
                <td class="text-nowrap">%s</td>
                <td><div class="mb-0" style="max-width: 200px; font-size: 0.8em; white-space: pre-wrap; word-wrap: break-word;">%s</div></td>
                <td><div class="mb-0" style="max-width: 200px; font-size: 0.8em; white-space: pre-wrap; word-wrap: break-word;">%s</div></td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
            </tr>`,
				components.InlineCode(tc.FunctionName),
				argsDisplay,
				resultDisplay,
				components.TruncatedLink(tc.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(tc.UserID), 20),
				components.OpenButton("/agentize/debug/sessions/"+template.URLQueryEscaper(tc.SessionID)),
				debuger.FormatTime(tc.CreatedAt),
			)
		}

		html += components.TableEnd(true)
		html += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/tool-calls")
	}

	html += ui.CardEnd()
	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}
