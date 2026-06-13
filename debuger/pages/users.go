package pages

import (
	"fmt"
	"html/template"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
)

// userLastActivity returns the most recent activity time for a user: the newest
// of the user's own UpdatedAt and their latest session's activity, falling back
// to CreatedAt. sessions is expected sorted newest-first (GetAllSessionsSorted).
func userLastActivity(user *model.User, sessions []*model.Session) time.Time {
	last := user.UpdatedAt
	if len(sessions) > 0 {
		st := sessions[0].UpdatedAt
		if st.IsZero() {
			st = sessions[0].CreatedAt
		}
		if st.After(last) {
			last = st
		}
	}
	if last.IsZero() {
		last = user.CreatedAt
	}
	return last
}

// relativeTimeCell renders a time as a relative "X ago" label with the absolute
// timestamp as a hover tooltip. muted dims the text (used for secondary columns).
func relativeTimeCell(t time.Time, muted bool) string {
	if t.IsZero() {
		return `<span class="text-muted">Never</span>`
	}
	cls := ""
	if muted {
		cls = ` class="text-muted"`
	}
	return fmt.Sprintf(`<span%s title="%s">%s</span>`, cls, debuger.FormatTime(t), debuger.FormatTimeAgo(t))
}

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

	// Sort users by Last Activity (most recently active first) so the list
	// surfaces the users worth looking at; ties fall back to UserID for stability.
	sort.SliceStable(users, func(i, j int) bool {
		ai := userLastActivity(users[i], sessionsByUser[users[i].UserID])
		aj := userLastActivity(users[j], sessionsByUser[users[j].UserID])
		if ai.Equal(aj) {
			return users[i].UserID < users[j].UserID
		}
		return ai.After(aj)
	})

	// Pagination
	totalItems := len(users)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedUsers := users[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("All Users", "people-fill", totalItems)

	if len(users) == 0 {
		content += components.InfoAlert("No users found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "User ID", NoWrap: true},
			{Header: "User", NoWrap: true},
			{Header: "Sessions", Center: true, NoWrap: true},
			{Header: "Last Activity", NoWrap: true},
			{Header: "Created", NoWrap: true},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Nonsense", Center: true, NoWrap: true},
			{Header: "Actions", Center: true, NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, user := range paginatedUsers {
			userSessions := sessionsByUser[user.UserID]
			sessionCount := len(userSessions)

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

			// Combined identity: name on top, @username muted below. Falls back
			// to whichever is present, or "-" when the user has neither.
			userCell := `<span class="text-muted">-</span>`
			switch {
			case user.Name != "" && user.Username != "":
				userCell = fmt.Sprintf(`%s<br><small class="text-muted">@%s</small>`,
					template.HTMLEscapeString(user.Name), template.HTMLEscapeString(user.Username))
			case user.Name != "":
				userCell = template.HTMLEscapeString(user.Name)
			case user.Username != "":
				userCell = fmt.Sprintf(`<span class="text-muted">@%s</span>`, template.HTMLEscapeString(user.Username))
			}

			// Sessions / Nonsense: only badge when non-zero, otherwise a quiet "0".
			sessionsCell := `<span class="text-muted">0</span>`
			if sessionCount > 0 {
				sessionsCell = components.CountBadge(sessionCount, "primary")
			}
			nonsenseCell := `<span class="text-muted">0</span>`
			if user.NonsenseCount > 0 {
				nonsenseCell = components.CountBadge(user.NonsenseCount, "warning text-dark")
			}

			content += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
            </tr>`,
				components.InlineCode(template.HTMLEscapeString(user.UserID)),
				userCell,
				sessionsCell,
				relativeTimeCell(userLastActivity(user, userSessions), false),
				relativeTimeCell(user.CreatedAt, true),
				banStatus,
				nonsenseCell,
				components.ViewDetailsButton("/agentize/debug/users/"+template.URLQueryEscaper(user.UserID)),
			)
		}

		content += components.TableEnd(true)
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/users")
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Users") + ui.NavbarAndBody("/agentize/debug/users", content) + ui.Footer(), nil
}

// RenderUserDetail generates the user detail HTML page.
// If showDeletedSuccess is true, shows a success alert for "data deleted".
func RenderUserDetail(handler *debuger.DebugHandler, userID string, showDeletedSuccess ...bool) (string, error) {
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

	userDocs, err := dp.GetUserFilesByUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get documents: %w", err)
	}

	content := ui.ContainerStart()

	// Breadcrumb
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Users", URL: "/agentize/debug/users"},
		{Label: userID, Active: true},
	})

	if len(showDeletedSuccess) > 0 && showDeletedSuccess[0] {
		content += components.SuccessAlert("All messages, sessions, quota, consumption and invoices for this user have been deleted successfully.")
	}

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

	// Delete user data button (form with confirmation). The ?confirm=<userID>
	// param satisfies the server-side typed-confirmation guard on the endpoint.
	deleteFormAction := "/agentize/debug/users/" + url.PathEscape(userID) + "/delete-data?confirm=" + url.QueryEscape(userID)

	content += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header d-flex justify-content-between align-items-center">
        <h4 class="mb-0"><i class="bi bi-person-fill me-2"></i>User Information</h4>
        <form method="POST" action="%s" onsubmit="return confirm('Are you sure? All messages, sessions, quota, consumption and invoices for this user will be deleted.');" class="d-inline">
            <button type="submit" class="btn btn-sm btn-outline-danger"><i class="bi bi-trash me-1"></i>Delete all user data (messages, sessions, quota, consumption, invoices)</button>
        </form>
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
		deleteFormAction,
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

	// Optional billing/credit summary (when provider is set by the application)
	if billingHTML, err := handler.GetUserBillingHTML(userID); err == nil && billingHTML != "" {
		content += billingHTML
	}

	// Core Agent (brain) panel: the long-term memory the Core router operates on
	// for this user — its Core session's summary/tags/model and known-document count.
	var coreSession *model.Session
	for _, s := range userSessions {
		if s.AgentType == model.AgentTypeCore {
			coreSession = s
			break
		}
	}
	content += renderCoreBrainCard(coreSession, len(userDocs))

	// Sessions card
	content += ui.CardStartWithCount("Sessions", "diagram-3-fill", len(userSessions))

	if len(userSessions) == 0 {
		content += components.InfoAlert("No sessions found for this user.")
	} else {
		content += components.ListGroupStart()
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

			content += fmt.Sprintf(`
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
		content += components.ListGroupEnd()
	}

	content += ui.CardEnd()

	// Messages card
	content += ui.CardStartWithAction("Messages", "chat-dots-fill", len(messages),
		"/agentize/debug/messages?user="+template.URLQueryEscaper(userID), "View All")

	if len(messages) == 0 {
		content += components.InfoAlert("No messages found for this user.")
	} else {
		rowConfig := components.DefaultMessageRowConfig()
		rowConfig.ShowUser = false // Already on user page
		rowConfig.ShowSession = true

		columns := components.MessageTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		// Show first 10 messages (already sorted by CreatedAt DESC - newest first)
		displayCount := debuger.Min(len(messages), 10)
		for i := 0; i < displayCount; i++ {
			content += components.MessageTableRow(messages[i], rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.MessageTableScript()
	}

	content += ui.CardEnd()

	// Files card
	content += ui.CardStartWithCount("Opened Files", "folder-fill", len(userFiles))

	if len(userFiles) == 0 {
		content += components.InfoAlert("No opened files found for this user.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "File Path"},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Opened At", NoWrap: true},
			{Header: "Session", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
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

			content += fmt.Sprintf(`<tr>
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

		content += components.TableEnd(true)
	}

	content += ui.CardEnd()

	// Documents card (real user files: uploaded or generated)
	content += ui.CardStartWithCount("Documents", "file-earmark-text-fill", len(userDocs))

	if len(userDocs) == 0 {
		content += components.InfoAlert("No documents found for this user.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Name"},
			{Header: "Source", Center: true, NoWrap: true},
			{Header: "Type", NoWrap: true},
			{Header: "Size", NoWrap: true},
			{Header: "Created At", NoWrap: true},
			{Header: "Session", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for _, f := range userDocs {
			content += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-nowrap">%s</td>
            </tr>`,
				template.HTMLEscapeString(f.Name),
				fileSourceBadge(f.Source),
				components.InlineCode(template.HTMLEscapeString(f.MIMEType)),
				formatBytes(f.Size),
				debuger.FormatTime(f.CreatedAt),
				components.TruncatedLink(f.SessionID, "/agentize/debug/sessions/"+template.URLQueryEscaper(f.SessionID), 8),
			)
		}

		content += components.TableEnd(true)
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - User: "+template.HTMLEscapeString(userID)) + ui.NavbarAndBody("/agentize/debug/users", content) + ui.Footer(), nil
}

// renderCoreBrainCard renders the "Core Agent (Brain)" card on the user detail
// page: the long-term memory the Core router uses to decide where to send this
// user's messages. coreSession is the user's AgentType==core session (may be nil);
// docCount is how many real user files (documents) the Core can reference when
// delegating to an agent.
func renderCoreBrainCard(coreSession *model.Session, docCount int) string {
	out := ui.CardStart("Core Agent (Brain)", "cpu-fill")
	if coreSession == nil {
		out += components.InfoAlert("No Core session yet — the Core builds memory after the user's first messages.")
		out += ui.CardEnd()
		return out
	}

	summary := `<span class="text-muted">No summary yet</span>`
	if coreSession.Summary != "" {
		summary = template.HTMLEscapeString(coreSession.Summary)
	}
	tags := `<span class="text-muted">-</span>`
	if len(coreSession.Tags) > 0 {
		tags = template.HTMLEscapeString(strings.Join(coreSession.Tags, ", "))
	}
	summarizedAt := "Never"
	if !coreSession.SummarizedAt.IsZero() {
		summarizedAt = debuger.FormatTime(coreSession.SummarizedAt)
	}

	out += fmt.Sprintf(`
<div class="card-body">
    <p class="text-muted mb-3">The long-term memory the Core router uses to route this user's messages. Updated in the background by summarization.</p>
    <table class="table table-sm table-borderless mb-0">
        <tbody>
            <tr><td class="text-end fw-bold align-top" style="width: 170px; padding: 0.5rem 1rem;">Core Session:</td><td style="padding: 0.5rem 1rem;">%s</td></tr>
            <tr><td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Model:</td><td style="padding: 0.5rem 1rem;">%s</td></tr>
            <tr><td class="text-end fw-bold align-top" style="padding: 0.5rem 1rem;">Memory (Summary):</td><td style="padding: 0.5rem 1rem;">%s</td></tr>
            <tr><td class="text-end fw-bold align-top" style="padding: 0.5rem 1rem;">Topics (Tags):</td><td style="padding: 0.5rem 1rem;">%s</td></tr>
            <tr><td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Last Summarized:</td><td class="text-muted" style="padding: 0.5rem 1rem;">%s</td></tr>
            <tr><td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Messages:</td><td style="padding: 0.5rem 1rem;">%s</td></tr>
            <tr><td class="text-end fw-bold" style="padding: 0.5rem 1rem;">Known Documents:</td><td style="padding: 0.5rem 1rem;">%s</td></tr>
        </tbody>
    </table>
</div>`,
		components.InlineCode(coreSession.SessionID),
		components.InlineCode(debuger.GetModelDisplay(coreSession.Model)),
		summary,
		tags,
		summarizedAt,
		components.CountBadge(coreSession.MessageSeq, "info"),
		components.CountBadge(docCount, "secondary"),
	)
	out += ui.CardEnd()
	return out
}
