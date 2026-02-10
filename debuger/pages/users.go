package pages

import (
	"fmt"
	"html/template"
	"strings"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
)

// RenderUsers generates the users list HTML page
func RenderUsers(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	users, err := dp.GetAllUsers()
	if err != nil {
		return "", fmt.Errorf("failed to get users: %w", err)
	}

	sessionsByUser, err := dp.GetAllSessionsSorted()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}

	// Pagination
	totalItems := len(users)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedUsers := users[startIdx:endIdx]

	html := ui.Header("Agentize Debug - Users")
	html += ui.Navbar("/agentize/debug/users")
	html += ui.ContainerStart()

	html += ui.CardStartWithCount("All Users", "people-fill", totalItems)

	if len(users) == 0 {
		html += components.InfoAlert("No users found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "User ID", NoWrap: true},
			{Header: "Name", NoWrap: true},
			{Header: "Username", NoWrap: true},
			{Header: "Sessions", Center: true, NoWrap: true},
			{Header: "Ban Status", Center: true, NoWrap: true},
			{Header: "Nonsense Count", Center: true, NoWrap: true},
			{Header: "Created At", NoWrap: true},
			{Header: "Actions", Center: true, NoWrap: true},
		}
		html += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, user := range paginatedUsers {
			sessionCount := len(sessionsByUser[user.UserID])

			banStatus := components.BadgeWithIcon("Active", "✅", "success")
			if user.IsCurrentlyBanned() {
				banText := "Banned"
				if !user.BanUntil.IsZero() {
					banText += fmt.Sprintf(" (until %s)", debuger.FormatTime(user.BanUntil))
				} else {
					banText += " (permanent)"
				}
				banStatus = components.BadgeWithIcon(banText, "🚫", "danger")
			}

			nameDisplay := "-"
			if user.Name != "" {
				nameDisplay = template.HTMLEscapeString(user.Name)
			}
			usernameDisplay := "-"
			if user.Username != "" {
				usernameDisplay = template.HTMLEscapeString(user.Username)
			}

			html += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td>%s</td>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
            </tr>`,
				components.InlineCode(template.HTMLEscapeString(user.UserID)),
				nameDisplay,
				usernameDisplay,
				components.CountBadge(sessionCount, "primary"),
				banStatus,
				components.CountBadge(user.NonsenseCount, "warning text-dark"),
				debuger.FormatTime(user.CreatedAt),
				components.ViewDetailsButton("/agentize/debug/users/"+template.URLQueryEscaper(user.UserID)),
			)
		}

		html += components.TableEnd(true)
		html += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/users")
	}

	html += ui.CardEnd()
	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}

// RenderUserDetail generates the user detail HTML page
func RenderUserDetail(handler *debuger.DebugHandler, userID string) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	user, err := dp.GetUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return "", fmt.Errorf("user not found: %s", userID)
	}

	sessionsByUser, err := dp.GetAllSessionsSorted()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}
	userSessions := sessionsByUser[userID]

	messages, err := dp.GetMessagesByUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}

	userFiles, err := dp.GetOpenedFilesByUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get files: %w", err)
	}

	html := ui.Header("Agentize Debug - User: " + userID)
	html += ui.Navbar("/agentize/debug/users")
	html += ui.ContainerStart()

	// Breadcrumb
	html += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Users", URL: "/agentize/debug/users"},
		{Label: userID, Active: true},
	})

	// User info card
	banStatus := "✅ Active"
	if user.IsCurrentlyBanned() {
		banStatus = "🚫 Banned"
		if !user.BanUntil.IsZero() {
			banStatus += fmt.Sprintf(" (until %s)", debuger.FormatTime(user.BanUntil))
		} else {
			banStatus += " (permanent)"
		}
		if user.BanMessage != "" {
			banStatus += ": " + template.HTMLEscapeString(user.BanMessage)
		}
	}

	nameDisplay := "-"
	if user.Name != "" {
		nameDisplay = template.HTMLEscapeString(user.Name)
	}
	usernameDisplay := "-"
	if user.Username != "" {
		usernameDisplay = template.HTMLEscapeString(user.Username)
	}

	// Build active sessions display for detail page - show full text without truncation
	activeSessionsHTML := "-"
	if len(user.ActiveSessionIDs) > 0 {
		var parts []string
		for agentType, sessionID := range user.ActiveSessionIDs {
			if sessionID != "" {
				link := fmt.Sprintf(`<a href="/agentize/debug/sessions/%s">%s: %s</a>`,
					template.URLQueryEscaper(sessionID),
					template.HTMLEscapeString(string(agentType)),
					components.InlineCode(template.HTMLEscapeString(sessionID)))
				parts = append(parts, link)
			}
		}
		if len(parts) > 0 {
			activeSessionsHTML = strings.Join(parts, "<br>")
		}
	}

	// Calculate total MessageSeq and ToolSeq from all user sessions
	totalMessageSeq := 0
	totalToolSeq := 0
	for _, session := range userSessions {
		totalMessageSeq += session.MessageSeq
		totalToolSeq += session.ToolSeq
	}

	// Build ban details display
	isBannedDisplay := "No"
	if user.IsBanned {
		isBannedDisplay = "Yes"
	}
	banUntilDisplay := "-"
	if !user.BanUntil.IsZero() {
		banUntilDisplay = debuger.FormatTime(user.BanUntil)
	} else if user.IsBanned {
		banUntilDisplay = "Permanent"
	}
	banMessageDisplay := "-"
	if user.BanMessage != "" {
		banMessageDisplay = template.HTMLEscapeString(user.BanMessage)
	}

	html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-person-fill me-2"></i>User Information</h4>
    </div>
    <div class="card-body p-0">
        <div class="row g-0">
            <div class="col-md-6">
                <table class="table table-sm table-borderless mb-0">
                    <tbody>
                        <tr>
                            <td class="text-end fw-bold" style="width: 140px; padding: 0.5rem 1rem;">User ID:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Name:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Username:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Status:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Is Banned:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Ban Until:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold align-top" style="padding: 0.5rem 1rem;">Ban Message:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                    </tbody>
                </table>
            </div>
            <div class="col-md-6">
                <table class="table table-sm table-borderless mb-0">
                    <tbody>
                        <tr>
                            <td class="text-end fw-bold" style="width: 140px; padding: 0.5rem 1rem;">Created At:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Updated At:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Total Message Seq:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Total Tool Seq:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Nonsense Count:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Last Nonsense:</td>
                            <td style="padding: 0.5rem 1rem;" class="text-muted">%s</td>
                        </tr>
                        <tr>
                            <td class="text-end fw-bold align-top" style="padding: 0.5rem 1rem;">Active Sessions:</td>
                            <td style="padding: 0.5rem 1rem;">%s</td>
                        </tr>
                    </tbody>
                </table>
            </div>
        </div>
    </div>
</div>`,
		components.CodeBlock(template.HTMLEscapeString(user.UserID)),
		nameDisplay,
		usernameDisplay,
		banStatus,
		isBannedDisplay,
		banUntilDisplay,
		banMessageDisplay,
		debuger.FormatTime(user.CreatedAt),
		debuger.FormatTime(user.UpdatedAt),
		components.CountBadge(totalMessageSeq, "info"),
		components.CountBadge(totalToolSeq, "info"),
		components.CountBadge(user.NonsenseCount, "warning text-dark"),
		debuger.FormatTime(user.LastNonsenseTime),
		activeSessionsHTML,
	)

	// Sessions card
	html += ui.CardStartWithCount("Sessions", "diagram-3-fill", len(userSessions))

	if len(userSessions) == 0 {
		html += components.InfoAlert("No sessions found for this user.")
	} else {
		html += components.ListGroupStart()
		for _, session := range userSessions {
			title := session.Title
			if title == "" {
				title = "Untitled Session"
			}

			summaryDisplay := "-"
			if session.Summary != "" {
				summaryDisplay = debuger.TruncateString(session.Summary, 100)
			}

			summarizedAtDisplay := "-"
			if !session.SummarizedAt.IsZero() {
				summarizedAtDisplay = debuger.FormatTime(session.SummarizedAt)
			}

			tagsDisplay := "-"
			if len(session.Tags) > 0 {
				tagsDisplay = template.HTMLEscapeString(strings.Join(session.Tags, ", "))
			}

			html += fmt.Sprintf(`
<a href="/agentize/debug/sessions/%s" class="list-group-item list-group-item-action">
    <div class="d-flex w-100 justify-content-between align-items-start">
        <div class="flex-grow-1">
            <h6 class="mb-2">%s</h6>
            <small class="text-muted">SessionID: %s | MsgSeq: %d</small>
            <small class="text-muted d-block">Created: %s | Updated: %s</small>
            <small class="text-muted d-block">Model: %s</small>
            <small class="text-muted d-block">Summary: %s</small>
            <small class="text-muted d-block">Summarized At: %s</small>
            <small class="text-muted d-block">Tags: %s</small>
        </div>
        %s
    </div>
</a>`,
				template.URLQueryEscaper(session.SessionID),
				template.HTMLEscapeString(title),
				components.InlineCode(session.SessionID),
				session.MessageSeq,
				debuger.FormatDuration(session.CreatedAt),
				debuger.FormatDuration(session.UpdatedAt),
				components.InlineCode(debuger.GetModelDisplay(session.Model)),
				template.HTMLEscapeString(summaryDisplay),
				summarizedAtDisplay,
				tagsDisplay,
				components.Badge(string(session.AgentType), "secondary"),
			)
		}
		html += components.ListGroupEnd()
	}

	html += ui.CardEnd()

	// Messages card
	html += ui.CardStartWithAction("Messages", "chat-dots-fill", len(messages),
		"/agentize/debug/messages?user="+template.URLQueryEscaper(userID), "View All")

	if len(messages) == 0 {
		html += components.InfoAlert("No messages found for this user.")
	} else {
		rowConfig := components.DefaultMessageRowConfig()
		rowConfig.ShowUser = false // Already on user page
		rowConfig.ShowSession = true

		columns := components.MessageTableColumns(rowConfig)
		html += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		// Show first 10 messages (already sorted by CreatedAt DESC - newest first)
		displayCount := debuger.Min(len(messages), 10)
		for i := 0; i < displayCount; i++ {
			html += components.MessageTableRow(messages[i], rowConfig, i)
		}

		html += components.TableEnd(true)
		html += components.MessageTableScript()
	}

	html += ui.CardEnd()

	// Files card
	html += ui.CardStartWithCount("Opened Files", "folder-fill", len(userFiles))

	if len(userFiles) == 0 {
		html += components.InfoAlert("No opened files found for this user.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "File Path"},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Opened At", NoWrap: true},
			{Header: "Session", NoWrap: true},
		}
		html += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for _, f := range userFiles {
			status := components.BadgeWithIcon("Open", "✅", "success")
			if !f.IsOpen {
				status = components.BadgeWithIcon("Closed", "❌", "secondary")
			}

			html += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
            </tr>`,
				components.InlineCode(template.HTMLEscapeString(f.FilePath)),
				status,
				debuger.FormatTime(f.OpenedAt),
				components.TruncatedLink(f.SessionID, "/agentize/debug/sessions/"+template.URLQueryEscaper(f.SessionID), 8),
			)
		}

		html += components.TableEnd(true)
	}

	html += ui.CardEnd()
	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}
