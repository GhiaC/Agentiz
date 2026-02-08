package pages

import (
	"fmt"
	"html/template"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
)

// RenderMessages generates the messages list HTML page
func RenderMessages(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	messages, err := dp.GetAllMessages()
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}

	// Pagination
	totalItems := len(messages)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedMessages := messages[startIdx:endIdx]

	html := ui.Header("Agentize Debug - Messages")
	html += ui.Navbar("/agentize/debug/messages")
	html += ui.ContainerStart()

	html += ui.CardStartWithCount("All Messages", "chat-dots-fill", totalItems)

	if len(messages) == 0 {
		html += components.InfoAlert("No messages found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Time", NoWrap: true},
			{Header: "Role", Center: true, NoWrap: true},
			{Header: "Content"},
			{Header: "Model", Center: true, NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Session", Center: true, NoWrap: true},
			{Header: "Tool Calls", Center: true, NoWrap: true},
			{Header: "Nonsense", Center: true, NoWrap: true},
		}
		html += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, msg := range paginatedMessages {
			contentDisplay := components.ExpandableWithPreview(msg.Content, 150)

			toolCallBadge := components.YesNoBadge(msg.HasToolCalls)

			nonsenseBadge := components.Badge("-", "secondary")
			if msg.IsNonsense {
				nonsenseBadge = components.BadgeWithIcon("Nonsense", "⚠️", "warning text-dark")
			}

			html += fmt.Sprintf(`<tr>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
                <td class="text-break">%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
            </tr>`,
				debuger.FormatTime(msg.CreatedAt),
				components.RoleBadge(msg.Role),
				contentDisplay,
				components.InlineCode(debuger.GetModelDisplay(msg.Model)),
				components.TruncatedLink(msg.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(msg.UserID), 20),
				components.OpenButton("/agentize/debug/sessions/"+template.URLQueryEscaper(msg.SessionID)),
				toolCallBadge,
				nonsenseBadge,
			)
		}

		html += components.TableEnd(true)
		html += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/messages")
	}

	html += ui.CardEnd()
	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}
