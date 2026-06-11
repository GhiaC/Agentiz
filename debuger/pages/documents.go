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

// RenderDocuments generates the user documents (files) list HTML page.
func RenderDocuments(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	files, err := dp.GetAllUserFiles()
	if err != nil {
		return "", fmt.Errorf("failed to get documents: %w", err)
	}

	// Pagination
	totalItems := len(files)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedFiles := files[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("All Documents", "file-earmark-text-fill", totalItems)

	if len(files) == 0 {
		content += components.InfoAlert("No documents found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Name"},
			{Header: "Source", Center: true, NoWrap: true},
			{Header: "Type", NoWrap: true},
			{Header: "Size", NoWrap: true},
			{Header: "From", NoWrap: true},
			{Header: "Created At", NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Session", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, f := range paginatedFiles {
			derived := "-"
			if f.ParentFileID != "" {
				derived = components.InlineCode(template.HTMLEscapeString(f.ParentFileID))
			}
			content += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
            </tr>`,
				template.HTMLEscapeString(f.Name),
				fileSourceBadge(f.Source),
				components.InlineCode(template.HTMLEscapeString(f.MIMEType)),
				formatBytes(f.Size),
				derived,
				debuger.FormatTime(f.CreatedAt),
				components.TruncatedLink(f.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(f.UserID), 20),
				components.TruncatedLink(f.SessionID, "/agentize/debug/sessions/"+template.URLQueryEscaper(f.SessionID), 20),
			)
		}

		content += components.TableEnd(true)
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/documents")
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Documents") + ui.NavbarAndBody("/agentize/debug/documents", content) + ui.Footer(), nil
}

// fileSourceBadge renders a colored badge for a file source.
func fileSourceBadge(source model.FileSource) string {
	switch source {
	case model.FileSourceGenerated:
		return components.BadgeWithIcon("Generated", "🤖", "success")
	case model.FileSourceUploaded:
		return components.BadgeWithIcon("Uploaded", "📤", "info")
	default:
		return components.Badge(string(source), "secondary")
	}
}

// formatBytes renders a byte count as a human-readable size.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
