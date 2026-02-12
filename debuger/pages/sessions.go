package pages

import (
	"fmt"
	"html/template"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/data"
	"github.com/ghiac/agentize/debuger/ui"
	"github.com/ghiac/agentize/debuger/ui/components"
	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// RenderSessions generates the sessions list HTML page
func RenderSessions(handler *debuger.DebugHandler, page int) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	allSessions, err := dp.GetAllSessionsFlat()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}

	// Pagination
	totalItems := len(allSessions)
	startIdx, endIdx, _ := components.GetPaginationInfo(page, totalItems, components.DefaultItemsPerPage)
	paginatedSessions := allSessions[startIdx:endIdx]

	content := ui.ContainerStart()
	content += ui.CardStartWithCount("All Sessions", "diagram-3-fill", totalItems)

	if len(allSessions) == 0 {
		content += components.InfoAlert("No sessions found.")
	} else {
		// Configure session row display
		rowConfig := components.DefaultSessionRowConfig()
		rowConfig.ShowUser = true
		rowConfig.GetFilesCount = func(sessionID string) int {
			files, _ := handler.GetStore().GetOpenedFilesBySession(sessionID)
			return len(files)
		}

		columns := components.SessionTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for i, session := range paginatedSessions {
			content += components.SessionTableRow(session, rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.SessionTableScript()
		content += components.PaginationSimple(page, totalItems, components.DefaultItemsPerPage, "/agentize/debug/sessions")
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Sessions") + ui.NavbarAndBody("/agentize/debug/sessions", content) + ui.Footer(), nil
}

// convertExMsgToMessage converts an openai.ChatCompletionMessage to model.Message for display
func convertExMsgToMessage(chatMsg openai.ChatCompletionMessage, sessionID, userID string, index int, sessionModel string, agentType model.AgentType, createdAt time.Time) *model.Message {
	return &model.Message{
		MessageID:    fmt.Sprintf("exmsg-%s-%d", sessionID, index),
		SeqID:        index,
		AgentType:    agentType,
		ContentType:  model.ContentTypeText,
		UserID:       userID,
		SessionID:    sessionID,
		Role:         chatMsg.Role,
		Content:      chatMsg.Content,
		Model:        sessionModel,
		RequestModel: sessionModel,
		HasToolCalls: len(chatMsg.ToolCalls) > 0,
		IsNonsense:   false,
		CreatedAt:    createdAt,
	}
}

// RenderSessionDetail generates the session detail HTML page
func RenderSessionDetail(handler *debuger.DebugHandler, sessionID string) (string, error) {
	dp := data.NewDataProvider(handler.GetStore())

	session, err := dp.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get session: %w", err)
	}
	if session == nil {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	// Messages for this session: newest first (created_at DESC) for listing
	allMessages, err := dp.GetMessagesBySessionDesc(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}

	// Filter to only show active (non-archived) messages
	// Active messages count is based on session.Msgs; allMessages is sorted newest-first (DESC)
	// So active messages are the first activeCount (newest) messages
	activeCount := len(session.Msgs)

	var messages []*model.Message
	if activeCount > 0 && len(allMessages) > 0 {
		if activeCount >= len(allMessages) {
			messages = allMessages
		} else {
			messages = allMessages[:activeCount]
		}
	}

	files, err := dp.GetOpenedFilesBySession(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get files: %w", err)
	}

	summarizationLogs, _ := dp.GetSummarizationLogsBySession(sessionID)
	dbToolCalls, _ := dp.GetToolCallsBySession(sessionID)
	toolCalls := data.ConvertToolCallsToInfo(dbToolCalls)

	content := ui.ContainerStart()

	// Breadcrumb
	content += components.Breadcrumb([]components.BreadcrumbItem{
		{Label: "Dashboard", URL: "/agentize/debug"},
		{Label: "Users", URL: "/agentize/debug/users"},
		{Label: session.UserID, URL: "/agentize/debug/users/" + template.URLQueryEscaper(session.UserID)},
		{Label: "Session", Active: true},
	})

	// Session info card
	title := session.Title
	if title == "" {
		title = "Untitled Session"
	}

	agentTypeBadge := components.AgentTypeBadge(string(session.AgentType))

	inProgressBadge := ""
	if session.InProgress {
		inProgressBadge = components.Badge("In Progress", "warning") + " "
	}

	// Calculate message counts from session object
	activeMessagesCount := len(session.Msgs)
	archivedMessagesCount := len(session.ArchivedMsgs)
	// If database messages count is higher, use it (messages from DB are more accurate)
	dbMessagesCount := len(messages)
	sessionTotalCount := activeMessagesCount + archivedMessagesCount
	if dbMessagesCount > sessionTotalCount {
		// DB has more messages than session object, adjust active count
		activeMessagesCount = dbMessagesCount - archivedMessagesCount
		if activeMessagesCount < 0 {
			activeMessagesCount = dbMessagesCount
			archivedMessagesCount = 0
		}
	}

	summaryDisplay := "-"
	if session.Summary != "" {
		summaryDisplay = template.HTMLEscapeString(session.Summary)
	}

	summarizedAtDisplay := "-"
	if !session.SummarizedAt.IsZero() {
		summarizedAtDisplay = debuger.FormatTime(session.SummarizedAt) + " <small>(" + debuger.FormatDuration(session.SummarizedAt) + ")</small>"
	}

	tagsDisplay := "-"
	if len(session.Tags) > 0 {
		tagsDisplay = components.TagBadges(session.Tags)
	}

	content += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h4 class="mb-0"><i class="bi bi-diagram-3-fill me-2"></i>Session Information</h4>
    </div>
    <div class="card-body">
        <div class="row g-4">
            <div class="col-md-6">
                <div class="mb-3">
                    <strong class="d-block mb-2">Session ID:</strong>
                    %s
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Title:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Agent Type:</strong>
                    <div>%s%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Model:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">User:</strong>
                    %s
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Summary:</strong>
                    <div class="text-justify">%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Tags:</strong>
                    <div>%s</div>
                </div>
            </div>
            <div class="col-md-6">
                <div class="mb-3">
                    <strong class="d-block mb-2">Created At:</strong>
                    <div class="text-muted">%s <small>(%s)</small></div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Updated At:</strong>
                    <div class="text-muted">%s <small>(%s)</small></div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Summarized At:</strong>
                    <div class="text-muted">%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Messages:</strong>
                    <div>%s + %s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Opened Files:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Message Seq:</strong>
                    <div>%s</div>
                </div>
                <div class="mb-3">
                    <strong class="d-block mb-2">Tool Seq:</strong>
                    <div>%s</div>
                </div>
            </div>
        </div>
    </div>
</div>`,
		components.CodeBlock(template.HTMLEscapeString(session.SessionID)),
		template.HTMLEscapeString(title),
		inProgressBadge,
		agentTypeBadge,
		components.InlineCode(debuger.GetModelDisplay(session.Model)),
		components.Link(session.UserID, "/agentize/debug/users/"+template.URLQueryEscaper(session.UserID)),
		summaryDisplay,
		tagsDisplay,
		debuger.FormatTime(session.CreatedAt),
		debuger.FormatDuration(session.CreatedAt),
		debuger.FormatTime(session.UpdatedAt),
		debuger.FormatDuration(session.UpdatedAt),
		summarizedAtDisplay,
		components.Badge(fmt.Sprintf("%d active", activeMessagesCount), "primary"),
		components.Badge(fmt.Sprintf("%d archived", archivedMessagesCount), "secondary"),
		components.CountBadge(len(files), "info"),
		components.CountBadge(session.MessageSeq, "info"),
		components.CountBadge(session.ToolSeq, "info"),
	)

	// System Prompts card
	var systemPrompts []string
	for _, msg := range session.Msgs {
		if msg.Role == openai.ChatMessageRoleSystem && msg.Content != "" {
			systemPrompts = append(systemPrompts, msg.Content)
		}
	}
	for _, msg := range session.ArchivedMsgs {
		if msg.Role == openai.ChatMessageRoleSystem && msg.Content != "" {
			systemPrompts = append(systemPrompts, msg.Content)
		}
	}

	if len(systemPrompts) > 0 {
		content += ui.CardStartWithCount("System Prompts", "gear-fill", len(systemPrompts))
		for i, prompt := range systemPrompts {
			promptDisplay := debuger.TruncateString(prompt, 500)
			content += fmt.Sprintf(`
<div class="mb-3">
    <strong class="d-block mb-2">System Prompt #%d:</strong>
    %s
</div>`, i+1, components.ExpandablePre(promptDisplay, 300))
		}
		content += ui.CardEnd()
	}

	// Messages card
	content += ui.CardStartWithCount("Messages", "chat-dots-fill", len(messages))

	if len(messages) == 0 {
		content += components.InfoAlert("No messages found for this session.")
	} else {
		rowConfig := components.DefaultMessageRowConfig()
		rowConfig.ShowUser = false    // Already on session page, user is known
		rowConfig.ShowSession = false // Already on session page

		columns := components.MessageTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for i, msg := range messages {
			content += components.MessageTableRow(msg, rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.MessageTableScript()
	}

	content += ui.CardEnd()

	// ArchivedMsgs card (previously ExMsgs)
	archivedCount := len(session.ArchivedMsgs)
	content += fmt.Sprintf(`
<div class="card mb-4">
    <div class="card-header">
        <h5 class="mb-0"><i class="bi bi-archive-fill me-2"></i>Archived Messages (%d) <small class="text-muted">(Debug Only)</small></h5>
    </div>
    <div class="card-body">`, archivedCount)

	if archivedCount == 0 {
		content += components.InfoAlert("No archived messages found for this session.")
	} else {
		content += components.NoteAlert("Note", "ArchivedMsgs are messages moved from Msgs after summarization. They are only displayed here for debugging purposes and are not used in normal operations.")

		// ArchivedMsgs are already sorted by CreatedAt DESC in GetSession
		rowConfig := components.DefaultMessageRowConfig()
		rowConfig.ShowUser = false    // Already on session page, user is known
		rowConfig.ShowSession = false // Already on session page
		rowConfig.BaseURL = "/agentize/debug"

		columns := components.MessageTableColumns(rowConfig)
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for i, chatMsg := range session.ArchivedMsgs {
			// Calculate original index (since we reversed, original index is len - i - 1)
			originalIndex := len(session.ArchivedMsgs) - i - 1
			msg := convertExMsgToMessage(
				chatMsg,
				sessionID,
				session.UserID,
				originalIndex,
				session.Model,
				session.AgentType,
				session.CreatedAt,
			)
			content += components.MessageTableRow(msg, rowConfig, i)
		}

		content += components.TableEnd(true)
		content += components.MessageTableScript()
	}

	content += ui.CardEnd()

	// Summarization Logs card
	content += ui.CardStartWithCount("Summarization Logs", "file-text-fill", len(summarizationLogs))

	if len(summarizationLogs) == 0 {
		content += components.InfoAlert("No summarization logs found for this session.")
	} else {
		content += components.ListGroupStart()
		for _, log := range summarizationLogs {
			statusBadge := components.StatusBadge(log.Status)

			promptDisplay := components.ExpandableWithPreview(log.PromptSent, 500)
			responseDisplay := components.ExpandableWithPreview(log.ResponseReceived, 500)

			content += fmt.Sprintf(`
<div class="list-group-item">
    <div class="d-flex w-100 justify-content-between align-items-start mb-2">
        <div>
            %s
            %s
            %s
        </div>
        <small class="text-muted">%s</small>
    </div>
    <div class="mb-2">
        <strong>Prompt Sent:</strong>
        <div class="p-2 bg-light rounded mt-1" style="white-space: pre-wrap; word-wrap: break-word; font-size: 0.9em;">%s</div>
    </div>`,
				statusBadge,
				components.Badge("Model: "+log.ModelUsed, "info"),
				components.TokenBadge(log.TotalTokens, log.PromptTokens, log.CompletionTokens),
				debuger.FormatTime(log.CreatedAt),
				promptDisplay,
			)

			if log.Status == "success" && log.ResponseReceived != "" {
				content += fmt.Sprintf(`
    <div class="mb-2">
        <strong>Response Received:</strong>
        <div class="p-2 bg-success bg-opacity-10 rounded mt-1" style="white-space: pre-wrap; word-wrap: break-word; font-size: 0.9em;">%s</div>
    </div>`,
					responseDisplay,
				)
			}

			if log.Status == "failed" && log.ErrorMessage != "" {
				content += fmt.Sprintf(`
    <div class="mb-2">
        <strong>Error:</strong>
        %s
    </div>`,
					components.ExpandablePre(log.ErrorMessage, 200),
				)
			}

			content += fmt.Sprintf(`
    <small class="text-muted">Log ID: %s</small>
</div>`,
				components.InlineCode(log.LogID),
			)
		}
		content += components.ListGroupEnd()
	}

	content += ui.CardEnd()

	// Tool Calls card
	content += ui.CardStartWithAction("Tool Calls", "tools", len(toolCalls),
		"/agentize/debug/tool-calls?session="+template.URLQueryEscaper(sessionID), "View All")

	if len(toolCalls) == 0 {
		content += components.InfoAlert("No tool calls found for this session.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "Agent", Center: true, NoWrap: true},
			{Header: "Function", NoWrap: true},
			{Header: "Arguments"},
			{Header: "Result"},
			{Header: "Time", NoWrap: true},
			{Header: "", Center: true, NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for _, tc := range toolCalls {
			argsDisplay := components.ExpandableWithPreview(tc.Arguments, 150)
			resultDisplay := components.ExpandableWithPreview(tc.Result, 150)
			agentBadge := components.AgentTypeBadgeFromString(tc.AgentType)

			content += fmt.Sprintf(`<tr>
                <td class="text-center">%s</td>
                <td class="text-nowrap">%s</td>
                <td><div class="mb-0" style="max-width: 300px; white-space: pre-wrap; word-wrap: break-word;">%s</div></td>
                <td><div class="mb-0" style="max-width: 300px; white-space: pre-wrap; word-wrap: break-word;">%s</div></td>
                <td class="text-nowrap">%s</td>
                <td class="text-center">%s</td>
            </tr>`,
				agentBadge,
				components.InlineCode(tc.FunctionName),
				argsDisplay,
				resultDisplay,
				debuger.FormatTime(tc.CreatedAt),
				components.OpenButton("/agentize/debug/tool-calls/"+template.URLQueryEscaper(tc.ToolID)),
			)
		}

		content += components.TableEnd(true)
	}

	content += ui.CardEnd()

	// Files card
	content += ui.CardStartWithCount("Opened Files", "folder-fill", len(files))

	if len(files) == 0 {
		content += components.InfoAlert("No opened files found for this session.")
	} else {
		columns := []components.ColumnConfig{
			{Header: "File Path"},
			{Header: "File Name"},
			{Header: "Status", Center: true, NoWrap: true},
			{Header: "Opened At", NoWrap: true},
			{Header: "Closed At", NoWrap: true},
		}
		content += components.TableStartWithConfig(columns, components.TableConfig{
			Striped:     false,
			Hover:       true,
			Small:       true,
			Responsive:  true,
			AlignMiddle: true,
		})

		for _, f := range files {
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
            </tr>`,
				components.InlineCode(template.HTMLEscapeString(f.FilePath)),
				template.HTMLEscapeString(f.FileName),
				status,
				debuger.FormatTime(f.OpenedAt),
				closedAt,
			)
		}

		content += components.TableEnd(true)
	}

	content += ui.CardEnd()
	content += ui.ContainerEnd()
	return ui.Header("Agentize Debug - Session: "+sessionID) + ui.NavbarAndBody("/agentize/debug", content) + ui.Footer(), nil
}
