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

	content := ui.ContainerStart()

	// Show breadcrumb if filtered
	if sessionID != "" {
		content += components.Breadcrumb([]components.BreadcrumbItem{
			{Label: "Dashboard", URL: "/agentize/debug"},
			{Label: "Sessions", URL: "/agentize/debug/sessions"},
			{Label: sessionID, URL: "/agentize/debug/sessions/" + template.URLQueryEscaper(sessionID)},
			{Label: "Messages", Active: true},
		})
	} else if userID != "" {
		content += components.Breadcrumb([]components.BreadcrumbItem{
			{Label: "Dashboard", URL: "/agentize/debug"},
			{Label: "Users", URL: "/agentize/debug/users"},
			{Label: userID, URL: "/agentize/debug/users/" + template.URLQueryEscaper(userID)},
			{Label: "Messages", Active: true},
		})
	}

	content += ui.CardStartWithCount(title, "chat-dots-fill", totalItems)

	if len(messages) == 0 {
		content += components.InfoAlert("No messages found.")
	} else {
		rowConfig := components.DefaultMessageRowConfig()
		rowConfig.ShowUser = true
		rowConfig.ShowSession = true

		columns := components.MessageTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for i, msg := range paginatedMessages {
			content += components.MessageTableRow(msg, rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.MessageTableScript()
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, baseURL)
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Messages") + ui.NavbarAndBody("/agentize/debug/messages", content) + ui.Footer(), nil
}
