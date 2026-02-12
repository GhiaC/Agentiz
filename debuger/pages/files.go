package pages

import (
	"fmt"
	"html/template"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
)

// RenderFiles generates the opened files list HTML page
func RenderFiles(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	files, err := dp.GetAllOpenedFiles()
	if err != nil {
		return "", fmt.Errorf("failed to get files: %w", err)
	}

	// Pagination
	totalItems := len(files)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedFiles := files[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("All Opened Files", "folder-fill", totalItems)

	if len(files) == 0 {
		content += components.InfoAlert("No opened files found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "File Path"},
			{Header: "File Name"},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Opened At", NoWrap: true},
			{Header: "Closed At", NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Session", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, f := range paginatedFiles {
			status := components.BadgeWithIcon("Open", "✅", "success")
			if !f.IsOpen {
				status = components.BadgeWithIcon("Closed", "❌", "secondary")
			}
			closedAt := "N/A"
			if !f.ClosedAt.IsZero() {
				closedAt = debuger.FormatTime(f.ClosedAt)
			}

			content += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
            </tr>`,
				components.InlineCode(template.HTMLEscapeString(f.FilePath)),
				template.HTMLEscapeString(f.FileName),
				status,
				debuger.FormatTime(f.OpenedAt),
				closedAt,
				components.TruncatedLink(f.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(f.UserID), 20),
				components.TruncatedLink(f.SessionID, "/agentize/debug/sessions/"+template.URLQueryEscaper(f.SessionID), 20),
			)
		}

		content += components.TableEnd(true)
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/files")
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Opened Files") + ui.NavbarAndBody("/agentize/debug/files", content) + ui.Footer(), nil
}
