package pages

import (
	"encoding/json"
	"fmt"
	"html/template"
	"sort"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// RenderSummarized generates the summarization logs list HTML page with scheduler config
func RenderSummarized(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())
	config := handler.GetSchedulerConfig()

	sumStats, sessStats, err := dp.GetSummarizationStats(config)
	if err != nil {
		return "", fmt.Errorf("failed to get summarization stats: %w", err)
	}

	logs, err := dp.GetAllSummarizationLogs()
	if err != nil {
		return "", fmt.Errorf("failed to get summarization logs: %w", err)
	}

	// Pagination for logs
	totalItems := len(logs)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedLogs := logs[startIdx:endIdx]

	html := ui.Header("Agentize Debug - Summarization Logs")
	html += ui.Navbar("/agentize/debug/summarized")
	html += ui.ContainerStart()

	// Scheduler Configuration Card (if available)
	if config != nil {
		immediateThreshold := config.ImmediateSummarizationThreshold
		if immediateThreshold <= 0 {
			immediateThreshold = 50 // default
		}
		configItems := []components.ConfigItem{
			{Label: "Check Interval", Value: debuger.FormatDurationValue(config.CheckInterval)},
			{Label: "First Summarization Threshold", Value: fmt.Sprintf("%d messages", config.FirstSummarizationThreshold)},
			{Label: "Subsequent Message Threshold", Value: fmt.Sprintf("%d messages", config.SubsequentMessageThreshold)},
			{Label: "Subsequent Time Threshold", Value: debuger.FormatDurationValue(config.SubsequentTimeThreshold)},
			{Label: "Last Activity Threshold", Value: debuger.FormatDurationValue(config.LastActivityThreshold)},
			{Label: "Immediate Summarization Threshold", Value: fmt.Sprintf("%d messages (triggers immediate summarization)", immediateThreshold)},
			{Label: "Summary Model", Value: config.SummaryModel},
		}
		html += components.ConfigCard("Scheduler Configuration", configItems)
	}

	// Statistics Cards Row 1 - Summarization Logs
	html += `<div class="row g-4 mb-4">`

	html += `<div class="col-md-6 col-lg-3">`
	html += components.StatCard(
		fmt.Sprintf("%d", sumStats.TotalLogs),
		"Total Logs", "📝", "primary",
	)
	html += `</div>`

	html += `<div class="col-md-6 col-lg-3">`
	html += components.StatCard(
		fmt.Sprintf("%d", sumStats.SuccessLogs),
		"Successful", "✅", "success",
	)
	html += `</div>`

	html += `<div class="col-md-6 col-lg-3">`
	html += components.StatCard(
		fmt.Sprintf("%d", sumStats.FailedLogs),
		"Failed", "❌", "danger",
	)
	html += `</div>`

	html += `<div class="col-md-6 col-lg-3">`
	html += components.StatCard(
		fmt.Sprintf("%d", sumStats.PendingLogs),
		"Pending", "⏳", "warning",
	)
	html += `</div>`

	html += `</div>`

	// Statistics Cards Row 2 - Sessions
	html += `<div class="row g-4 mb-4">`

	html += `<div class="col-md-6 col-lg-4">`
	html += components.StatCard(
		fmt.Sprintf("%d", sessStats.SummarizedSessions),
		"Summarized Sessions", "📋", "info",
	)
	html += `</div>`

	threshold := 5
	if config != nil && config.FirstSummarizationThreshold > 0 {
		threshold = config.FirstSummarizationThreshold
	}

	html += `<div class="col-md-6 col-lg-4">`
	html += components.StatCardWithSubtext(
		fmt.Sprintf("%d", sessStats.EligibleSessions),
		"Eligible Sessions", "🎯", "secondary",
		fmt.Sprintf("(>=%d messages, not summarized)", threshold),
	)
	html += `</div>`

	html += `<div class="col-md-6 col-lg-4">`
	html += components.StatCard(
		fmt.Sprintf("%d", sessStats.TotalSessions),
		"Total Sessions", "📊", "dark",
	)
	html += `</div>`

	html += `</div>`

	// Summarization Logs Table
	html += ui.CardStartWithCount("All Summarization Logs", "file-text-fill", totalItems)

	if len(logs) == 0 {
		html += components.InfoAlert("No summarization logs found.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Status", NoWrap: true},
			{Header: "Type", Center: true, NoWrap: true},
			{Header: "Session", NoWrap: true},
			{Header: "Model", Center: true, NoWrap: true},
			{Header: "Messages", Center: true, NoWrap: true},
			{Header: "Tokens", Center: true, NoWrap: true},
			{Header: "Duration", Center: true, NoWrap: true},
			{Header: "Created At", NoWrap: true},
			{Header: "Actions", Center: true, NoWrap: true},
		}
		html += components.TableStartWithConfig(columns, components.DefaultTableConfig())

		for _, log := range paginatedLogs {
			statusBadge := components.StatusBadge(log.Status)
			tokenBadge := components.TokenBadge(log.TotalTokens, log.PromptTokens, log.CompletionTokens)

			// Summarization type badge
			typeBadge := "-"
			switch log.SummarizationType {
			case "first":
				typeBadge = components.Badge("First", "success")
			case "subsequent":
				typeBadge = components.Badge("Subsequent", "info")
			case "immediate":
				typeBadge = components.Badge("Immediate", "warning")
			}

			// Messages info
			msgsInfo := fmt.Sprintf("%d → %d", log.MessagesBeforeCount, log.MessagesAfterCount)
			if log.ArchivedMessagesCount > 0 {
				msgsInfo += fmt.Sprintf(" (%d archived)", log.ArchivedMessagesCount)
			}

			// Duration display
			durationDisplay := "-"
			if log.DurationMs > 0 {
				if log.DurationMs < 1000 {
					durationDisplay = fmt.Sprintf("%dms", log.DurationMs)
				} else {
					durationDisplay = fmt.Sprintf("%.1fs", float64(log.DurationMs)/1000)
				}
			}

			// Session title display
			sessionDisplay := log.SessionTitle
			if sessionDisplay == "" {
				sessionDisplay = debuger.TruncateString(log.SessionID, 15)
			} else {
				sessionDisplay = debuger.TruncateString(sessionDisplay, 20)
			}

			html += fmt.Sprintf(`<tr>
                <td>%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
            </tr>`,
				statusBadge,
				typeBadge,
				components.TruncatedLink(sessionDisplay, "/agentize/debug/sessions/"+template.URLQueryEscaper(log.SessionID), 20),
				components.InlineCode(debuger.TruncateString(log.ModelUsed, 15)),
				msgsInfo,
				tokenBadge,
				durationDisplay,
				debuger.FormatDuration(log.CreatedAt),
				components.ViewDetailsButton("/agentize/debug/summarized/"+template.URLQueryEscaper(log.LogID)),
			)
		}

		html += components.TableEnd(true)
		html += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/summarized")
	}

	html += ui.CardEnd()
	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}

// RenderSummarizedMessages generates a page showing all summarized messages from all sessions
func RenderSummarizedMessages(handler *debuger.DebugHandler) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	sessionsByUser, err := dp.GetAllSessionsSorted()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}

	// Collect all summarized messages
	var allSummarizedMessages []debuger.SummarizedMessageInfo
	totalCount := 0

	for _, sessions := range sessionsByUser {
		for _, session := range sessions {
			if len(session.SummarizedMessages) > 0 {
				for _, msg := range session.SummarizedMessages {
					allSummarizedMessages = append(allSummarizedMessages, debuger.SummarizedMessageInfo{
						SessionID:        session.SessionID,
						UserID:           session.UserID,
						SessionTitle:     session.Title,
						Role:             msg.Role,
						Content:          msg.Content,
						HasToolCalls:     len(msg.ToolCalls) > 0,
						ToolCalls:        msg.ToolCalls,
						SummarizedAt:     session.SummarizedAt,
						SessionCreatedAt: session.CreatedAt,
					})
					totalCount++
				}
			}
		}
	}

	// Sort by SummarizedAt (newest first)
	sort.Slice(allSummarizedMessages, func(i, j int) bool {
		return allSummarizedMessages[i].SummarizedAt.After(allSummarizedMessages[j].SummarizedAt)
	})

	// Count by role
	userCount := 0
	assistantCount := 0
	toolCount := 0
	for _, msg := range allSummarizedMessages {
		switch msg.Role {
		case openai.ChatMessageRoleUser:
			userCount++
		case openai.ChatMessageRoleAssistant:
			assistantCount++
		case openai.ChatMessageRoleTool:
			toolCount++
		}
	}

	html := ui.Header("Agentize Debug - Summarized Messages")
	html += ui.Navbar("/agentize/debug/summarized")
	html += ui.ContainerStart()

	// Statistics Cards
	html += `<div class="row g-4 mb-4">`

	html += `<div class="col-md-6 col-lg-3">`
	html += components.StatCard(
		fmt.Sprintf("%d", totalCount),
		"Total Messages", "📝", "primary",
	)
	html += `</div>`

	html += `<div class="col-md-6 col-lg-3">`
	html += components.StatCard(
		fmt.Sprintf("%d", userCount),
		"User", "👤", "info",
	)
	html += `</div>`

	html += `<div class="col-md-6 col-lg-3">`
	html += components.StatCard(
		fmt.Sprintf("%d", assistantCount),
		"Assistant", "🤖", "success",
	)
	html += `</div>`

	html += `<div class="col-md-6 col-lg-3">`
	html += components.StatCard(
		fmt.Sprintf("%d", toolCount),
		"Tool", "🔧", "warning",
	)
	html += `</div>`

	html += `</div>`

	// Messages card
	html += ui.CardStartWithCount("All Summarized Messages", "archive-fill", len(allSummarizedMessages))

	if len(allSummarizedMessages) == 0 {
		html += components.InfoAlert("No summarized messages found. Messages are archived here after session summarization.")
	} else {
		html += components.NoteAlert("Note", "These are archived messages that have been summarized and moved from active conversation state. They are kept for reference but are not used in normal operations.")

		html += components.ListGroupStart()

		for _, msgInfo := range allSummarizedMessages {
			contentDisplay := components.ExpandableWithPreview(msgInfo.Content, 500)

			toolCallBadge := ""
			if msgInfo.HasToolCalls {
				toolCallBadge = " " + components.Badge(fmt.Sprintf("Has Tool Calls (%d)", len(msgInfo.ToolCalls)), "danger")
			}

			sessionTitle := msgInfo.SessionTitle
			if sessionTitle == "" {
				sessionTitle = "Untitled Session"
			}

			html += fmt.Sprintf(`
<div class="list-group-item">
    <div class="d-flex w-100 justify-content-between align-items-start mb-2">
        <div>
            %s%s
            %s
            %s
        </div>
        <small class="text-muted">Summarized: %s</small>
    </div>
    <p class="mb-2 text-justify">%s</p>`,
				components.RoleBadge(msgInfo.Role),
				toolCallBadge,
				components.ButtonOutlineSmall("Open Session", "/agentize/debug/sessions/"+template.URLQueryEscaper(msgInfo.SessionID), "primary"),
				components.Badge("User: "+debuger.TruncateString(msgInfo.UserID, 20), "info"),
				debuger.FormatTime(msgInfo.SummarizedAt),
				contentDisplay,
			)

			// Show tool calls if present
			if msgInfo.HasToolCalls && len(msgInfo.ToolCalls) > 0 {
				html += `<div class="mt-2"><strong>Tool Calls:</strong>`
				for _, tc := range msgInfo.ToolCalls {
					argsJSON, _ := json.MarshalIndent(tc.Function.Arguments, "", "  ")
					html += fmt.Sprintf(`
<div class="mt-1 p-2 bg-light rounded">
    <strong>Function:</strong> %s<br>
    <strong>Arguments:</strong>
    %s
</div>`,
						components.InlineCode(tc.Function.Name),
						components.PreBlock(string(argsJSON)),
					)
				}
				html += `</div>`
			}

			html += fmt.Sprintf(`
    <small class="text-muted d-block mt-2">Session Created: %s</small>
</div>`,
				debuger.FormatTime(msgInfo.SessionCreatedAt),
			)
		}

		html += components.ListGroupEnd()
	}

	html += ui.CardEnd()
	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}

// RenderSummarizationLogDetail generates the detail page for a single summarization log
func RenderSummarizationLogDetail(handler *debuger.DebugHandler, logID string) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	// Get all logs and find the one we need
	allLogs, err := dp.GetAllSummarizationLogs()
	if err != nil {
		return "", fmt.Errorf("failed to get summarization logs: %w", err)
	}

	var log *model.SummarizationLog
	for _, l := range allLogs {
		if l.LogID == logID {
			log = l
			break
		}
	}

	if log == nil {
		return "", fmt.Errorf("summarization log not found: %s", logID)
	}

	html := ui.Header("Agentize Debug - Summarization Log: " + logID)
	html += ui.Navbar("/agentize/debug/summarized")
	html += ui.ContainerStart()

	// Breadcrumb
	html += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Summarization Logs", URL: "/agentize/debug/summarized"},
		{Label: debuger.TruncateString(logID, 20), Active: true},
	})

	// Status badge
	statusBadge := components.StatusBadge(log.Status)

	// Type badge
	typeBadge := "-"
	switch log.SummarizationType {
	case "first":
		typeBadge = components.Badge("First Summarization", "success")
	case "subsequent":
		typeBadge = components.Badge("Subsequent Summarization", "info")
	case "immediate":
		typeBadge = components.Badge("Immediate Summarization", "warning")
	}

	// Duration display
	durationDisplay := "-"
	if log.DurationMs > 0 {
		if log.DurationMs < 1000 {
			durationDisplay = fmt.Sprintf("%dms", log.DurationMs)
		} else {
			durationDisplay = fmt.Sprintf("%.2fs", float64(log.DurationMs)/1000)
		}
	}

	// Completed at display
	completedAtDisplay := "-"
	if !log.CompletedAt.IsZero() {
		completedAtDisplay = debuger.FormatTime(log.CompletedAt) + " <small>(" + debuger.FormatDuration(log.CompletedAt) + ")</small>"
	}

	// Main info card
	html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-file-text-fill me-2"></i>Summarization Log Details</h4>
    </div>
    <div class="card-body">
        <div class="row g-4">
            <div class="col-md-6">
                <div class="mb-3">
                    <strong class="d-block mb-2">Log ID:</strong>
                    %s
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Status:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Summarization Type:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Session:</strong>
                    %s
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Session Title:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">User:</strong>
                    %s
                </div>
            </div>
            <div class="col-md-6">
                <div class="mb-3">
                    <strong class="d-block mb-2">Model Used:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Requested Model:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Duration:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Created At:</strong>
                    <div class="text-muted">%s <small>(%s)</small></div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Completed At:</strong>
                    <div class="text-muted">%s</div>
                </div>
            </div>
        </div>
    </div>
</div>`,
		components.CodeBlock(log.LogID),
		statusBadge,
		typeBadge,
		components.Link(debuger.TruncateString(log.SessionID, 30), "/agentize/debug/sessions/"+template.URLQueryEscaper(log.SessionID)),
		template.HTMLEscapeString(log.SessionTitle),
		components.Link(log.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(log.UserID)),
		components.InlineCode(log.ModelUsed),
		components.InlineCode(log.RequestedModel),
		durationDisplay,
		debuger.FormatTime(log.CreatedAt),
		debuger.FormatDuration(log.CreatedAt),
		completedAtDisplay,
	)

	// Messages Info Card
	html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-chat-dots-fill me-2"></i>Message Counts</h5>
    </div>
    <div class="card-body">
        <div class="row">
            <div class="col-md-4">
                <div class="text-center p-3 bg-light rounded">
                    <h3 class="mb-0">%d</h3>
                    <small class="text-muted">Messages Before</small>
                </div>
            </div>
            <div class="col-md-4">
                <div class="text-center p-3 bg-light rounded">
                    <h3 class="mb-0">%d</h3>
                    <small class="text-muted">Messages After</small>
                </div>
            </div>
            <div class="col-md-4">
                <div class="text-center p-3 bg-light rounded">
                    <h3 class="mb-0">%d</h3>
                    <small class="text-muted">Archived Total</small>
                </div>
            </div>
        </div>
    </div>
</div>`,
		log.MessagesBeforeCount,
		log.MessagesAfterCount,
		log.ArchivedMessagesCount,
	)

	// Token Usage Card
	html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-coin me-2"></i>Token Usage</h5>
    </div>
    <div class="card-body">
        <div class="row">
            <div class="col-md-4">
                <div class="text-center p-3 bg-info bg-opacity-10 rounded">
                    <h3 class="mb-0">%d</h3>
                    <small class="text-muted">Prompt Tokens</small>
                </div>
            </div>
            <div class="col-md-4">
                <div class="text-center p-3 bg-success bg-opacity-10 rounded">
                    <h3 class="mb-0">%d</h3>
                    <small class="text-muted">Completion Tokens</small>
                </div>
            </div>
            <div class="col-md-4">
                <div class="text-center p-3 bg-primary bg-opacity-10 rounded">
                    <h3 class="mb-0">%d</h3>
                    <small class="text-muted">Total Tokens</small>
                </div>
            </div>
        </div>
    </div>
</div>`,
		log.PromptTokens,
		log.CompletionTokens,
		log.TotalTokens,
	)

	// Before/After Summary Card
	previousSummary := log.PreviousSummary
	if previousSummary == "" {
		previousSummary = "(No previous summary)"
	}
	generatedSummary := log.GeneratedSummary
	if generatedSummary == "" {
		generatedSummary = "(No summary generated)"
	}

	html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-arrow-left-right me-2"></i>Summary Comparison</h5>
    </div>
    <div class="card-body">
        <div class="row">
            <div class="col-md-6">
                <strong class="d-block mb-2">Previous Summary:</strong>
                <div class="p-3 bg-light rounded">%s</div>
            </div>
            <div class="col-md-6">
                <strong class="d-block mb-2">Generated Summary:</strong>
                <div class="p-3 bg-success bg-opacity-10 rounded">%s</div>
            </div>
        </div>
    </div>
</div>`,
		template.HTMLEscapeString(previousSummary),
		template.HTMLEscapeString(generatedSummary),
	)

	// Tags Comparison Card
	previousTags := log.PreviousTags
	if previousTags == "" {
		previousTags = "(No previous tags)"
	}
	generatedTags := log.GeneratedTags
	if generatedTags == "" {
		generatedTags = "(No tags generated)"
	}

	html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-tags-fill me-2"></i>Tags Comparison</h5>
    </div>
    <div class="card-body">
        <div class="row">
            <div class="col-md-6">
                <strong class="d-block mb-2">Previous Tags:</strong>
                <div class="p-3 bg-light rounded">%s</div>
            </div>
            <div class="col-md-6">
                <strong class="d-block mb-2">Generated Tags:</strong>
                <div class="p-3 bg-success bg-opacity-10 rounded">%s</div>
            </div>
        </div>
    </div>
</div>`,
		template.HTMLEscapeString(previousTags),
		template.HTMLEscapeString(generatedTags),
	)

	// Generated Title Card (if any)
	if log.GeneratedTitle != "" {
		html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-type me-2"></i>Generated Title</h5>
    </div>
    <div class="card-body">
        <div class="p-3 bg-success bg-opacity-10 rounded">%s</div>
    </div>
</div>`,
			template.HTMLEscapeString(log.GeneratedTitle),
		)
	}

	// Error Message Card (if any)
	if log.ErrorMessage != "" {
		html += fmt.Sprintf(`
<div class="card mb-4 border-danger">
    <div class="card-header bg-danger text-white">
        <h5 class="mb-0"><i class="bi bi-exclamation-triangle-fill me-2"></i>Error Message</h5>
    </div>
    <div class="card-body">
        %s
    </div>
</div>`,
			components.PreBlock(log.ErrorMessage),
		)
	}

	// Prompt Sent Card
	html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-send-fill me-2"></i>Prompt Sent</h5>
    </div>
    <div class="card-body">
        %s
    </div>
</div>`,
		components.ExpandablePre(log.PromptSent, 500),
	)

	// Response Received Card
	if log.ResponseReceived != "" {
		html += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-reply-fill me-2"></i>Response Received</h5>
    </div>
    <div class="card-body">
        %s
    </div>
</div>`,
			components.ExpandablePre(log.ResponseReceived, 500),
		)
	}

	html += ui.ContainerEnd()
	html += ui.Footer()

	return html, nil
}
