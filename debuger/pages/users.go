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

	// Build active sessions display for detail page
	activeSessionsHTML := "-"
	if len(user.ActiveSessionIDs) > 0 {
		var parts []string
		for agentType, sessionID := range user.ActiveSessionIDs {
			if sessionID != "" {
				link := fmt.Sprintf(`<a href="/agentize/debug/sessions/%s">%s: %s</a>`,
					template.URLQueryEscaper(sessionID),
					template.HTMLEscapeString(string(agentType)),
					components.InlineCode(debuger.TruncateString(sessionID, 16)))
				parts = append(parts, link)
			}
		}
		if len(parts) > 0 {
			activeSessionsHTML = strings.Join(parts, "<br>")
		}
	}

	html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-person-fill me-2"></i>User Information</h4>
    </div>
    <div class="card-body">
        <div class="row g-4">
            <div class="col-md-6">
                <div class="mb-3">
                    <strong class="d-block mb-2">User ID:</strong>
                    %s
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Name:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Username:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Status:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Nonsense Count:</strong>
                    %s
                </div>
            </div>
            <div class="col-md-6">
                <div class="mb-3">
                    <strong class="d-block mb-2">Created At:</strong>
                    <div class="text-muted">%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Updated At:</strong>
                    <div class="text-muted">%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Last Nonsense:</strong>
                    <div class="text-muted">%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Active Sessions:</strong>
                    <div>%s</div>
                </div>
            </div>
        </div>
    </div>
</div>`,
		components.CodeBlock(template.HTMLEscapeString(user.UserID)),
		nameDisplay,
		usernameDisplay,
		banStatus,
		components.CountBadge(user.NonsenseCount, "warning text-dark"),
		debuger.FormatTime(user.CreatedAt),
		debuger.FormatTime(user.UpdatedAt),
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
            <small class="text-muted">Created: %s | Updated: %s</small>
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
				debuger.FormatTime(session.CreatedAt),
				debuger.FormatTime(session.UpdatedAt),
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
		columns := []components.ColumnConfig{
			{Header: "Time", NoWrap: true},
			{Header: "Agent", Center: true, NoWrap: true},
			{Header: "Type", Center: true, NoWrap: true},
			{Header: "Role", Center: true, NoWrap: true},
			{Header: "Content"},
			{Header: "Model", Center: true, NoWrap: true},
			{Header: "Session", NoWrap: true},
			{Header: "Nonsense", Center: true, NoWrap: true},
		}
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
			msg := messages[i]
			contentDisplay := components.ExpandableWithPreview(msg.Content, 100)

			nonsenseBadge := components.Badge("-", "secondary")
			if msg.IsNonsense {
				nonsenseBadge = components.BadgeWithIcon("Nonsense", "⚠️", "warning text-dark")
			}

			agentBadge := components.AgentTypeBadgeFromModel(msg.AgentType)
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
            </tr>`,
				debuger.FormatTime(msg.CreatedAt),
				agentBadge,
				contentTypeBadge,
				components.RoleBadge(msg.Role),
				contentDisplay,
				components.InlineCode(debuger.GetModelDisplay(msg.Model)),
				components.OpenButton("/agentize/debug/sessions/"+template.URLQueryEscaper(msg.SessionID)),
				nonsenseBadge,
			)
		}

		html += components.TableEnd(true)
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
