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

// RenderMessages generates the messages list HTML page
// userID and sessionID are optional filters
func RenderMessages(handler *debuger.DebugHandler, page int, userID, sessionID string) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	var messages []*model.Message
	var err error
	var title string
	var baseURL string

	// Apply filters based on query params
	if sessionID != "" {
		messages, err = dp.GetMessagesBySessionDesc(sessionID)
		title = "Messages for Session: " + sessionID
		baseURL = "/agentize/debug/messages?session=" + template.URLQueryEscaper(sessionID)
	} else if userID != "" {
		messages, err = dp.GetMessagesByUser(userID)
		title = "Messages for User: " + userID
		baseURL = "/agentize/debug/messages?user=" + template.URLQueryEscaper(userID)
	} else {
		messages, err = dp.GetAllMessages()
		title = "All Messages"
		baseURL = "/agentize/debug/messages"
	}
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

	// Show breadcrumb if filtered
	if sessionID != "" {
		html += components.Breadcrumb([]components.BreadcrumbItem{
			{Label: "Dashboard", URL: "/agentize/debug"},
			{Label: "Sessions", URL: "/agentize/debug/sessions"},
			{Label: sessionID, URL: "/agentize/debug/sessions/" + template.URLQueryEscaper(sessionID)},
			{Label: "Messages", Active: true},
		})
	} else if userID != "" {
		html += components.Breadcrumb([]components.BreadcrumbItem{
			{Label: "Dashboard", URL: "/agentize/debug"},
			{Label: "Users", URL: "/agentize/debug/users"},
			{Label: userID, URL: "/agentize/debug/users/" + template.URLQueryEscaper(userID)},
			{Label: "Messages", Active: true},
		})
	}

	html += ui.CardStartWithCount(title, "chat-dots-fill", totalItems)

	if len(messages) == 0 {
		html += components.InfoAlert("No messages found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Time", NoWrap: true},
			{Header: "Agent", Center: true, NoWrap: true},
			{Header: "Type", Center: true, NoWrap: true},
			{Header: "Role", Center: true, NoWrap: true},
			{Header: "Content"},
			{Header: "Model", Center: true, NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Session", Center: true, NoWrap: true},
			{Header: "Tools", Center: true, NoWrap: true},
			{Header: "Nonsense", Center: true, NoWrap: true},
		}
		html += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, msg := range paginatedMessages {
			contentDisplay := components.ExpandableWithPreview(msg.Content, 150)

			// Tool calls - show link if has tool calls
			toolCallDisplay := components.Badge("-", "secondary")
			if msg.HasToolCalls {
				toolCallDisplay = fmt.Sprintf(`<a href="/agentize/debug/tool-calls?session=%s" class="btn btn-sm btn-outline-warning">🔧 View</a>`,
					template.URLQueryEscaper(msg.SessionID))
			}

			nonsenseBadge := components.Badge("-", "secondary")
			if msg.IsNonsense {
				nonsenseBadge = components.BadgeWithIcon("Nonsense", "⚠️", "warning text-dark")
			}

			// Agent type badge
			agentBadge := components.AgentTypeBadgeFromModel(msg.AgentType)

			// Content type badge
			contentTypeBadge := components.ContentTypeBadgeFromModel(msg.ContentType)

			html += fmt.Sprintf(`<tr>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-break">%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
            </tr>`,
				debuger.FormatTime(msg.CreatedAt),
				agentBadge,
				contentTypeBadge,
				components.RoleBadge(msg.Role),
				contentDisplay,
				components.InlineCode(debuger.GetModelDisplay(msg.Model)),
				components.TruncatedLink(msg.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(msg.UserID), 20),
				components.OpenButton("/agentize/debug/sessions/"+template.URLQueryEscaper(msg.SessionID)),
				toolCallDisplay,
				nonsenseBadge,
			)
		}

		html += components.TableEnd(true)
		html += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, baseURL)
	}

	html += ui.CardEnd()
	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}
