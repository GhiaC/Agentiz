package store

import (
	"encoding/json"
	"fmt"
	"html/template"
	"sort"
	"strings"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/sashabaranov/go-openai"
)

// ToolCallInfo represents information about a tool call
type ToolCallInfo struct {
	SessionID    string
	UserID       string
	MessageID    string
	ToolCallID   string
	FunctionName string
	Arguments    string
	Result       string
	CreatedAt    time.Time
}

// DebugStore is an interface for stores that support debugging
type DebugStore interface {
	GetAllSessions() (map[string][]*model.Session, error)
	GetAllUsers() ([]*model.User, error)
	GetAllMessages() ([]*model.Message, error)
	GetAllOpenedFiles() ([]*model.OpenedFile, error)
	GetMessagesBySession(sessionID string) ([]*model.Message, error)
	GetMessagesByUser(userID string) ([]*model.Message, error)
	GetOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error)
	GetUser(userID string) (*model.User, error)
	GetSession(sessionID string) (*model.Session, error)
	GetAllToolCalls() ([]*model.ToolCall, error)
	GetToolCallsBySession(sessionID string) ([]*model.ToolCall, error)
}

// DebugHandler provides HTML debugging interface for SessionStore
type DebugHandler struct {
	store model.SessionStore
}

// NewDebugHandler creates a new debug handler for a SessionStore
func NewDebugHandler(store model.SessionStore) (*DebugHandler, error) {
	// Check if store implements DebugStore interface
	if _, ok := store.(DebugStore); !ok {
		return nil, fmt.Errorf("store does not implement DebugStore interface")
	}
	return &DebugHandler{store: store}, nil
}

// GetAllSessions returns all sessions grouped by userID
func (h *DebugHandler) GetAllSessions() (map[string][]*model.Session, error) {
	debugStore := h.store.(DebugStore)
	sessionsByUser, err := debugStore.GetAllSessions()
	if err != nil {
		return nil, err
	}

	// Sort sessions by UpdatedAt (newest first)
	for userID := range sessionsByUser {
		sort.Slice(sessionsByUser[userID], func(i, j int) bool {
			return sessionsByUser[userID][i].UpdatedAt.After(sessionsByUser[userID][j].UpdatedAt)
		})
	}

	return sessionsByUser, nil
}

// GetSessionCount returns total number of sessions
func (h *DebugHandler) GetSessionCount() (int, error) {
	sessionsByUser, err := h.GetAllSessions()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, sessions := range sessionsByUser {
		count += len(sessions)
	}
	return count, nil
}

// GetUserCount returns number of unique users
func (h *DebugHandler) GetUserCount() (int, error) {
	debugStore := h.store.(DebugStore)
	users, err := debugStore.GetAllUsers()
	if err != nil {
		return 0, err
	}
	return len(users), nil
}

// FormatMessage formats a ChatCompletionMessage for display
func FormatMessage(msg openai.ChatCompletionMessage) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("<strong>Role:</strong> %s", msg.Role))

	if msg.Content != "" {
		content := template.HTMLEscapeString(msg.Content)
		// Truncate very long content
		if len(content) > 1000 {
			content = content[:1000] + "... (truncated)"
		}
		parts = append(parts, fmt.Sprintf("<strong>Content:</strong> %s", content))
	}

	if len(msg.ToolCalls) > 0 {
		toolCallsJSON, _ := json.MarshalIndent(msg.ToolCalls, "", "  ")
		parts = append(parts, fmt.Sprintf("<strong>Tool Calls:</strong> <pre>%s</pre>", template.HTMLEscapeString(string(toolCallsJSON))))
	}

	if msg.ToolCallID != "" {
		parts = append(parts, fmt.Sprintf("<strong>Tool Call ID:</strong> %s", msg.ToolCallID))
	}

	if msg.Name != "" {
		parts = append(parts, fmt.Sprintf("<strong>Function Name:</strong> %s", msg.Name))
	}

	if msg.FunctionCall != nil {
		funcCallJSON, _ := json.MarshalIndent(msg.FunctionCall, "", "  ")
		parts = append(parts, fmt.Sprintf("<strong>Function Call:</strong> <pre>%s</pre>", template.HTMLEscapeString(string(funcCallJSON))))
	}

	return strings.Join(parts, "<br>")
}

// FormatTime formats a time for display
func FormatTime(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	return t.Format("2006-01-02 15:04:05")
}

// FormatDuration formats duration since a time
func FormatDuration(t time.Time) string {
	if t.IsZero() {
		return "N/A"
	}
	duration := time.Since(t)
	if duration < time.Minute {
		return fmt.Sprintf("%d seconds ago", int(duration.Seconds()))
	} else if duration < time.Hour {
		return fmt.Sprintf("%d minutes ago", int(duration.Minutes()))
	} else if duration < 24*time.Hour {
		return fmt.Sprintf("%.1f hours ago", duration.Hours())
	} else {
		return fmt.Sprintf("%d days ago", int(duration.Hours()/24))
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// generateNavigationBar generates Bootstrap navigation bar
func generateNavigationBar(currentPage string) string {
	navItems := []struct {
		URL  string
		Icon string
		Text string
	}{
		{"/agentize/debug", "📊", "Dashboard"},
		{"/agentize/debug/users", "👤", "Users"},
		{"/agentize/debug/sessions", "📋", "Sessions"},
		{"/agentize/debug/messages", "💬", "Messages"},
		{"/agentize/debug/files", "📁", "Files"},
		{"/agentize/debug/tool-calls", "🔧", "Tool Calls"},
	}

	navHTML := `<nav class="navbar navbar-expand-lg navbar-dark" style="background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); box-shadow: 0 2px 10px rgba(0,0,0,0.1);">
    <div class="container-fluid">
        <a class="navbar-brand fw-bold" href="/agentize/debug">
            <i class="bi bi-bug-fill me-2"></i>Agentize Debug
        </a>
        <button class="navbar-toggler" type="button" data-bs-toggle="collapse" data-bs-target="#navbarNav" aria-controls="navbarNav" aria-expanded="false" aria-label="Toggle navigation">
            <span class="navbar-toggler-icon"></span>
        </button>
        <div class="collapse navbar-collapse" id="navbarNav">
            <ul class="navbar-nav ms-auto">`

	for _, item := range navItems {
		active := ""
		if item.URL == currentPage {
			active = "active fw-bold"
		}
		navHTML += fmt.Sprintf(`
                <li class="nav-item">
                    <a class="nav-link %s" href="%s">%s %s</a>
                </li>`, active, item.URL, item.Icon, item.Text)
	}

	navHTML += `
            </ul>
        </div>
    </div>
</nav>`

	return navHTML
}

// generateBootstrapHeader generates HTML header with Bootstrap CDN
func generateBootstrapHeader(title string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s</title>
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/css/bootstrap.min.css" rel="stylesheet" integrity="sha384-T3c6CoIi6uLrA9TneNEoa7RxnatzjcDSCmG1MXxSR1GAsXEV/Dwwykc2MPK8M2HN" crossorigin="anonymous">
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/bootstrap-icons@1.11.1/font/bootstrap-icons.css">
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            min-height: 100vh;
        }
        .main-container {
            background: white;
            border-radius: 15px;
            box-shadow: 0 10px 40px rgba(0,0,0,0.1);
            padding: 2rem;
            margin: 2rem 0;
        }
        .card {
            border: none;
            border-radius: 10px;
            box-shadow: 0 2px 10px rgba(0,0,0,0.08);
            transition: transform 0.2s, box-shadow 0.2s;
        }
        .card:hover {
            transform: translateY(-2px);
            box-shadow: 0 4px 20px rgba(0,0,0,0.12);
        }
        .card-header {
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            color: white;
            border-radius: 10px 10px 0 0 !important;
            font-weight: 600;
        }
        .table {
            border-radius: 8px;
            overflow: hidden;
        }
        .table thead {
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            color: white;
        }
        .table tbody tr {
            transition: background-color 0.2s;
        }
        .table tbody tr:hover {
            background-color: #f8f9fa;
        }
        .badge {
            padding: 0.4em 0.8em;
            font-weight: 500;
        }
        .text-justify {
            text-align: justify;
        }
        code {
            background-color: #f4f4f4;
            padding: 0.2em 0.4em;
            border-radius: 4px;
            font-size: 0.9em;
        }
        pre {
            background-color: #f8f9fa;
            padding: 1rem;
            border-radius: 6px;
            border: 1px solid #e9ecef;
        }
    </style>
    <script src="https://cdn.jsdelivr.net/npm/bootstrap@5.3.2/dist/js/bootstrap.bundle.min.js" integrity="sha384-BBtl+eGJRgqQAUMxJ7pMwbEyER4l1g+O15P+16Ep7Q9Q+zqX6gSbd85u4mG4QzX+" crossorigin="anonymous"></script>
</head>
<body>`, template.HTMLEscapeString(title))
}

// generateBootstrapFooter generates HTML footer
func generateBootstrapFooter() string {
	return `
    <script>
        // Auto-refresh every 30 seconds
        setTimeout(function() {
            location.reload();
        }, 30000);
    </script>
</body>
</html>`
}

// extractToolCallsFromSession extracts tool calls from session messages
func (h *DebugHandler) extractToolCallsFromSession(session *model.Session) []ToolCallInfo {
	var toolCalls []ToolCallInfo

	// Extract from active messages
	for _, msg := range session.ConversationState.Msgs {
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				argsJSON, _ := json.MarshalIndent(tc.Function.Arguments, "", "  ")
				// Find corresponding tool result
				result := ""
				if toolResult, ok := session.ToolResults[tc.ID]; ok {
					result = toolResult
				}

				toolCalls = append(toolCalls, ToolCallInfo{
					SessionID:    session.SessionID,
					UserID:       session.UserID,
					MessageID:    "", // We don't have message ID in ChatCompletionMessage
					ToolCallID:   tc.ID,
					FunctionName: tc.Function.Name,
					Arguments:    string(argsJSON),
					Result:       result,
					CreatedAt:    session.UpdatedAt, // Approximate
				})
			}
		}
	}

	return toolCalls
}

// extractToolCallsFromMessages extracts tool calls from database messages
func (h *DebugHandler) extractToolCallsFromMessages(messages []*model.Message, session *model.Session) []ToolCallInfo {
	var toolCalls []ToolCallInfo
	debugStore := h.store.(DebugStore)

	// Get session to access ToolResults
	if session == nil && len(messages) > 0 {
		sess, err := debugStore.GetSession(messages[0].SessionID)
		if err == nil {
			session = sess
		}
	}

	for _, msg := range messages {
		if msg.HasToolCalls {
			// We need to get the actual ChatCompletionMessage to access ToolCalls
			// For now, we'll mark it and try to get from session
			if session != nil {
				// Look for messages with tool calls in session
				for _, smsg := range session.ConversationState.Msgs {
					if len(smsg.ToolCalls) > 0 {
						for _, tc := range smsg.ToolCalls {
							argsJSON, _ := json.MarshalIndent(tc.Function.Arguments, "", "  ")
							result := ""
							if toolResult, ok := session.ToolResults[tc.ID]; ok {
								result = toolResult
							}

							toolCalls = append(toolCalls, ToolCallInfo{
								SessionID:    msg.SessionID,
								UserID:       msg.UserID,
								MessageID:    msg.MessageID,
								ToolCallID:   tc.ID,
								FunctionName: tc.Function.Name,
								Arguments:    string(argsJSON),
								Result:       result,
								CreatedAt:    msg.CreatedAt,
							})
						}
					}
				}
			}
		}
	}

	return toolCalls
}

// GenerateDashboardHTML generates the dashboard HTML page
func (h *DebugHandler) GenerateDashboardHTML() (string, error) {
	debugStore := h.store.(DebugStore)

	totalUsers, err := h.GetUserCount()
	if err != nil {
		return "", fmt.Errorf("failed to get user count: %w", err)
	}

	totalSessions, err := h.GetSessionCount()
	if err != nil {
		return "", fmt.Errorf("failed to get session count: %w", err)
	}

	allMessages, err := debugStore.GetAllMessages()
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}
	totalMessages := len(allMessages)

	allFiles, err := debugStore.GetAllOpenedFiles()
	if err != nil {
		return "", fmt.Errorf("failed to get files: %w", err)
	}
	totalFiles := len(allFiles)

	// Count tool calls
	totalToolCalls := 0
	for _, msg := range allMessages {
		if msg.HasToolCalls {
			totalToolCalls++
		}
	}

	html := generateBootstrapHeader("Agentize Debug - Dashboard")
	html += generateNavigationBar("/agentize/debug")
	html += `<div class="container">
    <div class="main-container">`

	// Stats cards
	html += `<div class="row g-4 mb-4">
        <div class="col-md-6 col-lg-4 col-xl-2">
            <div class="card text-center h-100 border-primary">
                <div class="card-body d-flex flex-column justify-content-center">
                    <h2 class="card-title text-primary mb-3" style="font-size: 2.5rem; font-weight: bold;">` + fmt.Sprintf("%d", totalUsers) + `</h2>
                    <p class="card-text mb-3" style="font-size: 1.1rem;">👤 Users</p>
                    <a href="/agentize/debug/users" class="btn btn-sm btn-outline-primary mt-auto">View Details</a>
                </div>
            </div>
        </div>
        <div class="col-md-6 col-lg-4 col-xl-2">
            <div class="card text-center h-100 border-success">
                <div class="card-body d-flex flex-column justify-content-center">
                    <h2 class="card-title text-success mb-3" style="font-size: 2.5rem; font-weight: bold;">` + fmt.Sprintf("%d", totalSessions) + `</h2>
                    <p class="card-text mb-3" style="font-size: 1.1rem;">📊 Sessions</p>
                </div>
            </div>
        </div>
        <div class="col-md-6 col-lg-4 col-xl-2">
            <div class="card text-center h-100 border-info">
                <div class="card-body d-flex flex-column justify-content-center">
                    <h2 class="card-title text-info mb-3" style="font-size: 2.5rem; font-weight: bold;">` + fmt.Sprintf("%d", totalMessages) + `</h2>
                    <p class="card-text mb-3" style="font-size: 1.1rem;">💬 Messages</p>
                    <a href="/agentize/debug/messages" class="btn btn-sm btn-outline-info mt-auto">View Details</a>
                </div>
            </div>
        </div>
        <div class="col-md-6 col-lg-4 col-xl-2">
            <div class="card text-center h-100 border-warning">
                <div class="card-body d-flex flex-column justify-content-center">
                    <h2 class="card-title text-warning mb-3" style="font-size: 2.5rem; font-weight: bold;">` + fmt.Sprintf("%d", totalFiles) + `</h2>
                    <p class="card-text mb-3" style="font-size: 1.1rem;">📁 Files</p>
                    <a href="/agentize/debug/files" class="btn btn-sm btn-outline-warning mt-auto">View Details</a>
                </div>
            </div>
        </div>
        <div class="col-md-6 col-lg-4 col-xl-2">
            <div class="card text-center h-100 border-danger">
                <div class="card-body d-flex flex-column justify-content-center">
                    <h2 class="card-title text-danger mb-3" style="font-size: 2.5rem; font-weight: bold;">` + fmt.Sprintf("%d", totalToolCalls) + `</h2>
                    <p class="card-text mb-3" style="font-size: 1.1rem;">🔧 Tool Calls</p>
                    <a href="/agentize/debug/tool-calls" class="btn btn-sm btn-outline-danger mt-auto">View Details</a>
                </div>
            </div>
        </div>
    </div>`

	// Quick links
	html += `<div class="row">
        <div class="col-12">
            <div class="card">
                <div class="card-header">
                    <h5 class="mb-0"><i class="bi bi-link-45deg me-2"></i>Quick Links</h5>
                </div>
                <div class="card-body">
                    <div class="row g-3">
                        <div class="col-md-6 col-lg-3">
                            <a href="/agentize/debug/users" class="card text-decoration-none text-dark h-100">
                                <div class="card-body text-center">
                                    <div class="mb-3" style="font-size: 3rem;">👤</div>
                                    <h6 class="card-title">View All Users</h6>
                                    <p class="card-text text-muted small text-justify">Browse all users and their sessions with detailed information</p>
                                </div>
                            </a>
                        </div>
                        <div class="col-md-6 col-lg-3">
                            <a href="/agentize/debug/messages" class="card text-decoration-none text-dark h-100">
                                <div class="card-body text-center">
                                    <div class="mb-3" style="font-size: 3rem;">💬</div>
                                    <h6 class="card-title">View All Messages</h6>
                                    <p class="card-text text-muted small text-justify">See all messages across all sessions with full context</p>
                                </div>
                            </a>
                        </div>
                        <div class="col-md-6 col-lg-3">
                            <a href="/agentize/debug/files" class="card text-decoration-none text-dark h-100">
                                <div class="card-body text-center">
                                    <div class="mb-3" style="font-size: 3rem;">📁</div>
                                    <h6 class="card-title">View All Opened Files</h6>
                                    <p class="card-text text-muted small text-justify">Browse all files that were opened during sessions</p>
                                </div>
                            </a>
                        </div>
                        <div class="col-md-6 col-lg-3">
                            <a href="/agentize/debug/tool-calls" class="card text-decoration-none text-dark h-100">
                                <div class="card-body text-center">
                                    <div class="mb-3" style="font-size: 3rem;">🔧</div>
                                    <h6 class="card-title">View All Tool Calls</h6>
                                    <p class="card-text text-muted small text-justify">See all tool calls and their results in detail</p>
                                </div>
                            </a>
                        </div>
                    </div>
                </div>
            </div>
        </div>
    </div>`

	html += `</div>
    </div>`
	html += generateBootstrapFooter()

	return html, nil
}

// GenerateHTML generates the debug HTML page (legacy - redirects to dashboard)
func (h *DebugHandler) GenerateHTML() (string, error) {
	return h.GenerateDashboardHTML()
}

// GenerateUsersHTML generates the users list HTML page
func (h *DebugHandler) GenerateUsersHTML() (string, error) {
	debugStore := h.store.(DebugStore)

	users, err := debugStore.GetAllUsers()
	if err != nil {
		return "", fmt.Errorf("failed to get users: %w", err)
	}

	sessionsByUser, err := h.GetAllSessions()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}

	html := generateBootstrapHeader("Agentize Debug - Users")
	html += generateNavigationBar("/agentize/debug/users")
	html += `<div class="container">
    <div class="main-container">
        <div class="card">
            <div class="card-header">
                <h4 class="mb-0"><i class="bi bi-people-fill me-2"></i>All Users (` + fmt.Sprintf("%d", len(users)) + `)</h4>
            </div>
            <div class="card-body">`

	if len(users) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No users found.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-striped table-hover align-middle">
                    <thead>
                        <tr>
                            <th class="text-nowrap">User ID</th>
                            <th class="text-nowrap">Name</th>
                            <th class="text-nowrap">Username</th>
                            <th class="text-center text-nowrap">Sessions</th>
                            <th class="text-center text-nowrap">Ban Status</th>
                            <th class="text-center text-nowrap">Nonsense Count</th>
                            <th class="text-nowrap">Created At</th>
                            <th class="text-center text-nowrap">Actions</th>
                        </tr>
                    </thead>
                    <tbody>`

		for _, user := range users {
			sessionCount := len(sessionsByUser[user.UserID])
			banStatus := `<span class="badge bg-success">✅ Active</span>`
			if user.IsCurrentlyBanned() {
				banText := "🚫 Banned"
				if !user.BanUntil.IsZero() {
					banText += fmt.Sprintf(" (until %s)", FormatTime(user.BanUntil))
				} else {
					banText += " (permanent)"
				}
				banStatus = fmt.Sprintf(`<span class="badge bg-danger">%s</span>`, banText)
			}

			nameDisplay := "-"
			if user.Name != "" {
				nameDisplay = template.HTMLEscapeString(user.Name)
			}
			usernameDisplay := "-"
			if user.Username != "" {
				usernameDisplay = template.HTMLEscapeString(user.Username)
			}

			html += fmt.Sprintf(`
                        <tr>
                            <td><code class="text-break">%s</code></td>
                            <td>%s</td>
                            <td>%s</td>
                            <td class="text-center"><span class="badge bg-primary">%d</span></td>
                            <td class="text-center">%s</td>
                            <td class="text-center"><span class="badge bg-warning text-dark">%d</span></td>
                            <td class="text-nowrap">%s</td>
                            <td class="text-center"><a href="/agentize/debug/users/%s" class="btn btn-sm btn-outline-primary">View Details</a></td>
                        </tr>`,
				template.HTMLEscapeString(user.UserID),
				nameDisplay,
				usernameDisplay,
				sessionCount,
				banStatus,
				user.NonsenseCount,
				FormatTime(user.CreatedAt),
				template.URLQueryEscaper(user.UserID))
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>
    </div>
    </div>`
	html += generateBootstrapFooter()

	return html, nil
}

// GenerateUserDetailHTML generates the user detail HTML page
func (h *DebugHandler) GenerateUserDetailHTML(userID string) (string, error) {
	debugStore := h.store.(DebugStore)

	user, err := debugStore.GetUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get user: %w", err)
	}
	if user == nil {
		return "", fmt.Errorf("user not found: %s", userID)
	}

	sessionsByUser, err := h.GetAllSessions()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}
	userSessions := sessionsByUser[userID]

	messages, err := debugStore.GetMessagesByUser(userID)
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}

	// Get opened files for user
	allFiles, err := debugStore.GetAllOpenedFiles()
	if err != nil {
		return "", fmt.Errorf("failed to get files: %w", err)
	}
	var userFiles []*model.OpenedFile
	for _, f := range allFiles {
		if f.UserID == userID {
			userFiles = append(userFiles, f)
		}
	}

	html := generateBootstrapHeader("Agentize Debug - User: " + userID)
	html += generateNavigationBar("/agentize/debug/users")
	html += `<div class="container">
    <div class="main-container">
        <nav aria-label="breadcrumb" class="mb-4">
            <ol class="breadcrumb">
                <li class="breadcrumb-item"><a href="/agentize/debug">Dashboard</a></li>
                <li class="breadcrumb-item"><a href="/agentize/debug/users">Users</a></li>
                <li class="breadcrumb-item active">` + template.HTMLEscapeString(userID) + `</li>
            </ol>
        </nav>`

	// User info card
	banStatus := "✅ Active"
	if user.IsCurrentlyBanned() {
		banStatus = "🚫 Banned"
		if !user.BanUntil.IsZero() {
			banStatus += fmt.Sprintf(" (until %s)", FormatTime(user.BanUntil))
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
                            <code class="d-block p-2 bg-light rounded text-break">%s</code>
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
                            <span class="badge bg-warning text-dark fs-6">%d</span>
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
                    </div>
                </div>
            </div>
        </div>`,
		template.HTMLEscapeString(user.UserID),
		nameDisplay,
		usernameDisplay,
		banStatus,
		user.NonsenseCount,
		FormatTime(user.CreatedAt),
		FormatTime(user.UpdatedAt),
		FormatTime(user.LastNonsenseTime))

	// Sessions card
	html += fmt.Sprintf(`
        <div class="card mb-4">
            <div class="card-header">
                <h5 class="mb-0"><i class="bi bi-diagram-3-fill me-2"></i>Sessions (%d)</h5>
            </div>
            <div class="card-body">`, len(userSessions))

	if len(userSessions) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No sessions found for this user.
            </div>`
	} else {
		html += `<div class="list-group">`
		for _, session := range userSessions {
			title := session.Title
			if title == "" {
				title = "Untitled Session"
			}
			html += fmt.Sprintf(`
                <a href="/agentize/debug/sessions/%s" class="list-group-item list-group-item-action">
                    <div class="d-flex w-100 justify-content-between align-items-start">
                        <div class="flex-grow-1">
                            <h6 class="mb-2">%s</h6>
                            <p class="mb-1"><code class="text-break">%s</code></p>
                            <small class="text-muted">Updated: %s</small>
                        </div>
                        <span class="badge bg-secondary ms-2">%s</span>
                    </div>
                </a>`,
				template.URLQueryEscaper(session.SessionID),
				template.HTMLEscapeString(title),
				template.HTMLEscapeString(session.SessionID),
				FormatTime(session.UpdatedAt),
				template.HTMLEscapeString(string(session.AgentType)))
		}
		html += `</div>`
	}

	html += `</div>
        </div>`

	// Messages card
	html += fmt.Sprintf(`
        <div class="card mb-4">
            <div class="card-header d-flex justify-content-between align-items-center">
                <h5 class="mb-0"><i class="bi bi-chat-dots-fill me-2"></i>Messages (%d)</h5>
                <a href="/agentize/debug/messages?user=%s" class="btn btn-sm btn-light">View All</a>
            </div>
            <div class="card-body">`, len(messages), template.URLQueryEscaper(userID))

	if len(messages) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No messages found for this user.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-sm align-middle">
                    <thead>
                        <tr>
                            <th class="text-nowrap">Time</th>
                            <th class="text-center text-nowrap">Role</th>
                            <th>Content</th>
                            <th class="text-nowrap">Session</th>
                        </tr>
                    </thead>
                    <tbody>`

		// Show last 10 messages
		displayCount := len(messages)
		if displayCount > 10 {
			displayCount = 10
		}
		for i := len(messages) - displayCount; i < len(messages); i++ {
			msg := messages[i]
			content := msg.Content
			if len(content) > 100 {
				content = content[:100] + "..."
			}
			badgeClass := "bg-secondary"
			switch msg.Role {
			case openai.ChatMessageRoleUser:
				badgeClass = "bg-primary"
			case openai.ChatMessageRoleAssistant:
				badgeClass = "bg-success"
			case openai.ChatMessageRoleTool:
				badgeClass = "bg-warning text-dark"
			case openai.ChatMessageRoleSystem:
				badgeClass = "bg-info"
			}
			html += fmt.Sprintf(`
                        <tr>
                            <td class="text-nowrap">%s</td>
                            <td class="text-center"><span class="badge %s">%s</span></td>
                            <td class="text-break">%s</td>
                            <td class="text-nowrap"><a href="/agentize/debug/sessions/%s" class="text-decoration-none">%s</a></td>
                        </tr>`,
				FormatTime(msg.CreatedAt),
				badgeClass,
				template.HTMLEscapeString(msg.Role),
				template.HTMLEscapeString(content),
				template.URLQueryEscaper(msg.SessionID),
				template.HTMLEscapeString(msg.SessionID[:8]+"..."))
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>`

	// Files card
	html += fmt.Sprintf(`
        <div class="card mb-4">
            <div class="card-header">
                <h5 class="mb-0"><i class="bi bi-folder-fill me-2"></i>Opened Files (%d)</h5>
            </div>
            <div class="card-body">`, len(userFiles))

	if len(userFiles) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No opened files found for this user.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-sm align-middle">
                    <thead>
                        <tr>
                            <th>File Path</th>
                            <th class="text-center text-nowrap">Status</th>
                            <th class="text-nowrap">Opened At</th>
                            <th class="text-nowrap">Session</th>
                        </tr>
                    </thead>
                    <tbody>`

		for _, f := range userFiles {
			status := `<span class="badge bg-success">✅ Open</span>`
			if !f.IsOpen {
				status = `<span class="badge bg-secondary">❌ Closed</span>`
			}
			html += fmt.Sprintf(`
                        <tr>
                            <td><code class="text-break">%s</code></td>
                            <td class="text-center">%s</td>
                            <td class="text-nowrap">%s</td>
                            <td class="text-nowrap"><a href="/agentize/debug/sessions/%s" class="text-decoration-none">%s</a></td>
                        </tr>`,
				template.HTMLEscapeString(f.FilePath),
				status,
				FormatTime(f.OpenedAt),
				template.URLQueryEscaper(f.SessionID),
				template.HTMLEscapeString(f.SessionID[:8]+"..."))
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>
    </div>
    </div>`
	html += generateBootstrapFooter()

	return html, nil
}

// GenerateSessionDetailHTML generates the session detail HTML page
func (h *DebugHandler) GenerateSessionDetailHTML(sessionID string) (string, error) {
	debugStore := h.store.(DebugStore)

	session, err := debugStore.GetSession(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get session: %w", err)
	}
	if session == nil {
		return "", fmt.Errorf("session not found: %s", sessionID)
	}

	messages, err := debugStore.GetMessagesBySession(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}

	files, err := debugStore.GetOpenedFilesBySession(sessionID)
	if err != nil {
		return "", fmt.Errorf("failed to get files: %w", err)
	}

	// Get tool calls from database instead of session
	dbToolCalls, err2 := debugStore.GetToolCallsBySession(sessionID)
	if err2 != nil {
		// If error, use empty slice
		dbToolCalls = []*model.ToolCall{}
	}

	// Convert to ToolCallInfo
	var toolCalls []ToolCallInfo
	for _, tc := range dbToolCalls {
		toolCalls = append(toolCalls, ToolCallInfo{
			SessionID:    tc.SessionID,
			UserID:       tc.UserID,
			MessageID:    tc.MessageID,
			ToolCallID:   tc.ToolCallID,
			FunctionName: tc.FunctionName,
			Arguments:    tc.Arguments,
			Result:       tc.Response,
			CreatedAt:    tc.CreatedAt,
		})
	}

	html := generateBootstrapHeader("Agentize Debug - Session: " + sessionID)
	html += generateNavigationBar("/agentize/debug")
	html += `<div class="container">
    <div class="main-container">
        <nav aria-label="breadcrumb" class="mb-4">
            <ol class="breadcrumb">
                <li class="breadcrumb-item"><a href="/agentize/debug">Dashboard</a></li>
                <li class="breadcrumb-item"><a href="/agentize/debug/users">Users</a></li>
                <li class="breadcrumb-item"><a href="/agentize/debug/users/` + template.URLQueryEscaper(session.UserID) + `">` + template.HTMLEscapeString(session.UserID) + `</a></li>
                <li class="breadcrumb-item active">Session</li>
            </ol>
        </nav>`

	// Session info card
	title := session.Title
	if title == "" {
		title = "Untitled Session"
	}
	agentTypeBadge := ""
	if session.AgentType != "" {
		badgeClass := "bg-secondary"
		switch session.AgentType {
		case model.AgentTypeCore:
			badgeClass = "bg-danger"
		case model.AgentTypeHigh:
			badgeClass = "bg-primary"
		case model.AgentTypeLow:
			badgeClass = "bg-success"
		}
		agentTypeBadge = fmt.Sprintf(`<span class="badge %s">%s</span>`, badgeClass, string(session.AgentType))
	}

	inProgressBadge := ""
	if session.ConversationState != nil && session.ConversationState.InProgress {
		inProgressBadge = `<span class="badge bg-warning">In Progress</span> `
	}

	// Extract system prompts from ConversationState.Msgs
	var systemPrompts []string
	if session.ConversationState != nil {
		for _, msg := range session.ConversationState.Msgs {
			if msg.Role == openai.ChatMessageRoleSystem && msg.Content != "" {
				systemPrompts = append(systemPrompts, msg.Content)
			}
		}
		// Also check summarized messages
		for _, msg := range session.SummarizedMessages {
			if msg.Role == openai.ChatMessageRoleSystem && msg.Content != "" {
				systemPrompts = append(systemPrompts, msg.Content)
			}
		}
	}

	activeMessagesCount := 0
	archivedMessagesCount := len(session.SummarizedMessages)
	if session.ConversationState != nil {
		activeMessagesCount = len(session.ConversationState.Msgs)
	}

	html += fmt.Sprintf(`
        <div class="card mb-4">
            <div class="card-header">
                <h4 class="mb-0"><i class="bi bi-diagram-3-fill me-2"></i>Session Information</h4>
            </div>
            <div class="card-body">
                <div class="row g-4">
                    <div class="col-md-6">
                        <div class="mb-3">
                            <strong class="d-block mb-2">Session ID:</strong>
                            <code class="d-block p-2 bg-light rounded text-break">%s</code>
                        </div>
                        <div class="mb-3">
                            <strong class="d-block mb-2">Title:</strong>
                            <div>%s</div>
                        </div>
                        <div class="mb-3">
                            <strong class="d-block mb-2">Agent Type:</strong>
                            <div>%s %s</div>
                        </div>
                        <div class="mb-3">
                            <strong class="d-block mb-2">User:</strong>
                            <a href="/agentize/debug/users/%s" class="text-decoration-none">%s</a>
                        </div>
                    </div>
                    <div class="col-md-6">
                        <div class="mb-3">
                            <strong class="d-block mb-2">Created At:</strong>
                            <div class="text-muted">%s</div>
                        </div>
                        <div class="mb-3">
                            <strong class="d-block mb-2">Updated At:</strong>
                            <div class="text-muted">%s <small>(%s)</small></div>
                        </div>
                        <div class="mb-3">
                            <strong class="d-block mb-2">Messages:</strong>
                            <div><span class="badge bg-primary">%d active</span> + <span class="badge bg-secondary">%d archived</span></div>
                        </div>
                        <div class="mb-3">
                            <strong class="d-block mb-2">Opened Files:</strong>
                            <div><span class="badge bg-info">%d</span></div>
                        </div>
                    </div>
                </div>
            </div>
        </div>`,
		template.HTMLEscapeString(session.SessionID),
		template.HTMLEscapeString(title),
		inProgressBadge,
		agentTypeBadge,
		template.URLQueryEscaper(session.UserID),
		template.HTMLEscapeString(session.UserID),
		FormatTime(session.CreatedAt),
		FormatTime(session.UpdatedAt),
		FormatDuration(session.UpdatedAt),
		activeMessagesCount,
		archivedMessagesCount,
		len(files))

	// System Prompts card
	if len(systemPrompts) > 0 {
		html += `<div class="card mb-4">
            <div class="card-header">
                <h5 class="mb-0"><i class="bi bi-gear-fill me-2"></i>System Prompts (` + fmt.Sprintf("%d", len(systemPrompts)) + `)</h5>
            </div>
            <div class="card-body">`
		for i, prompt := range systemPrompts {
			promptDisplay := template.HTMLEscapeString(prompt)
			if len(promptDisplay) > 500 {
				promptDisplay = promptDisplay[:500] + "..."
			}
			html += fmt.Sprintf(`
                <div class="mb-3">
                    <strong class="d-block mb-2">System Prompt #%d:</strong>
                    <pre class="p-3 bg-light rounded" style="max-height: 300px; overflow-y: auto; white-space: pre-wrap; word-wrap: break-word;">%s</pre>
                </div>`, i+1, promptDisplay)
		}
		html += `</div>
        </div>`
	} else {
		html += `<div class="card mb-4">
            <div class="card-header">
                <h5 class="mb-0"><i class="bi bi-gear-fill me-2"></i>System Prompts</h5>
            </div>
            <div class="card-body">
                <div class="alert alert-info text-center">
                    <i class="bi bi-info-circle me-2"></i>No system prompts found in this session.
                </div>
            </div>
        </div>`
	}

	// Messages card
	html += fmt.Sprintf(`
        <div class="card mb-4">
            <div class="card-header">
                <h5 class="mb-0"><i class="bi bi-chat-dots-fill me-2"></i>Messages (%d)</h5>
            </div>
            <div class="card-body">`, len(messages))

	if len(messages) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No messages found for this session.
            </div>`
	} else {
		html += `<div class="list-group">`
		for _, msg := range messages {
			content := msg.Content
			if len(content) > 200 {
				content = content[:200] + "..."
			}
			badgeClass := "bg-secondary"
			switch msg.Role {
			case openai.ChatMessageRoleUser:
				badgeClass = "bg-primary"
			case openai.ChatMessageRoleAssistant:
				badgeClass = "bg-success"
			case openai.ChatMessageRoleTool:
				badgeClass = "bg-warning text-dark"
			case openai.ChatMessageRoleSystem:
				badgeClass = "bg-info"
			}

			toolCallBadge := ""
			if msg.HasToolCalls {
				toolCallBadge = ` <span class="badge bg-danger">Has Tool Calls</span>`
			}

			html += fmt.Sprintf(`
                <div class="list-group-item">
                    <div class="d-flex w-100 justify-content-between align-items-start mb-2">
                        <div>
                            <span class="badge %s me-2">%s</span>%s
                        </div>
                        <small class="text-muted">%s</small>
                    </div>
                    <p class="mb-2 text-justify">%s</p>
                    <small class="text-muted">Message ID: <code>%s</code></small>
                </div>`,
				badgeClass,
				template.HTMLEscapeString(msg.Role),
				toolCallBadge,
				FormatTime(msg.CreatedAt),
				template.HTMLEscapeString(content),
				template.HTMLEscapeString(msg.MessageID))
		}
		html += `</div>`
	}

	html += `</div>
        </div>`

	// Tool Calls card
	html += fmt.Sprintf(`
        <div class="card mb-4">
            <div class="card-header d-flex justify-content-between align-items-center">
                <h5 class="mb-0"><i class="bi bi-tools me-2"></i>Tool Calls (%d)</h5>
                <a href="/agentize/debug/tool-calls?session=%s" class="btn btn-sm btn-light">View All</a>
            </div>
            <div class="card-body">`, len(toolCalls), template.URLQueryEscaper(sessionID))

	if len(toolCalls) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No tool calls found for this session.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-sm align-middle">
                    <thead>
                        <tr>
                            <th class="text-nowrap">Function</th>
                            <th>Arguments</th>
                            <th>Result</th>
                            <th class="text-nowrap">Time</th>
                        </tr>
                    </thead>
                    <tbody>`

		for _, tc := range toolCalls {
			args := tc.Arguments
			if len(args) > 150 {
				args = args[:150] + "..."
			}
			result := tc.Result
			if len(result) > 150 {
				result = result[:150] + "..."
			}
			html += fmt.Sprintf(`
                        <tr>
                            <td class="text-nowrap"><code>%s</code></td>
                            <td><pre class="mb-0" style="max-width: 300px; overflow: auto; white-space: pre-wrap; word-wrap: break-word;">%s</pre></td>
                            <td><pre class="mb-0" style="max-width: 300px; overflow: auto; white-space: pre-wrap; word-wrap: break-word;">%s</pre></td>
                            <td class="text-nowrap">%s</td>
                        </tr>`,
				template.HTMLEscapeString(tc.FunctionName),
				template.HTMLEscapeString(args),
				template.HTMLEscapeString(result),
				FormatTime(tc.CreatedAt))
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>`

	// Files card
	html += fmt.Sprintf(`
        <div class="card mb-4">
            <div class="card-header">
                <h5 class="mb-0"><i class="bi bi-folder-fill me-2"></i>Opened Files (%d)</h5>
            </div>
            <div class="card-body">`, len(files))

	if len(files) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No opened files found for this session.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-sm align-middle">
                    <thead>
                        <tr>
                            <th>File Path</th>
                            <th>File Name</th>
                            <th class="text-center text-nowrap">Status</th>
                            <th class="text-nowrap">Opened At</th>
                            <th class="text-nowrap">Closed At</th>
                        </tr>
                    </thead>
                    <tbody>`

		for _, f := range files {
			status := `<span class="badge bg-success">✅ Open</span>`
			if !f.IsOpen {
				status = `<span class="badge bg-secondary">❌ Closed</span>`
			}
			closedAt := "N/A"
			if !f.ClosedAt.IsZero() {
				closedAt = FormatTime(f.ClosedAt)
			}
			html += fmt.Sprintf(`
                        <tr>
                            <td><code class="text-break">%s</code></td>
                            <td>%s</td>
                            <td class="text-center">%s</td>
                            <td class="text-nowrap">%s</td>
                            <td class="text-nowrap">%s</td>
                        </tr>`,
				template.HTMLEscapeString(f.FilePath),
				template.HTMLEscapeString(f.FileName),
				status,
				FormatTime(f.OpenedAt),
				closedAt)
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>
    </div>
    </div>`
	html += generateBootstrapFooter()

	return html, nil
}

// GenerateSessionsHTML generates the sessions list HTML page
func (h *DebugHandler) GenerateSessionsHTML() (string, error) {
	debugStore := h.store.(DebugStore)

	sessionsByUser, err := h.GetAllSessions()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}

	// Flatten sessions list
	var allSessions []*model.Session
	for _, sessions := range sessionsByUser {
		allSessions = append(allSessions, sessions...)
	}

	// Sort by UpdatedAt (newest first)
	sort.Slice(allSessions, func(i, j int) bool {
		return allSessions[i].UpdatedAt.After(allSessions[j].UpdatedAt)
	})

	html := generateBootstrapHeader("Agentize Debug - Sessions")
	html += generateNavigationBar("/agentize/debug/sessions")
	html += `<div class="container">
    <div class="main-container">
        <div class="card">
            <div class="card-header">
                <h4 class="mb-0"><i class="bi bi-diagram-3-fill me-2"></i>All Sessions (` + fmt.Sprintf("%d", len(allSessions)) + `)</h4>
            </div>
            <div class="card-body">`

	if len(allSessions) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No sessions found.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-striped table-hover align-middle">
                    <thead>
                        <tr>
                            <th class="text-nowrap">Session ID</th>
                            <th class="text-nowrap">Title</th>
                            <th class="text-center text-nowrap">Agent Type</th>
                            <th class="text-nowrap">User</th>
                            <th class="text-center text-nowrap">Messages</th>
                            <th class="text-center text-nowrap">Files</th>
                            <th class="text-nowrap">Updated At</th>
                            <th class="text-center text-nowrap">Actions</th>
                        </tr>
                    </thead>
                    <tbody>`

		for _, session := range allSessions {
			title := session.Title
			if title == "" {
				title = "Untitled Session"
			}

			agentTypeBadge := ""
			rowClass := ""
			if session.AgentType != "" {
				badgeClass := "bg-secondary"
				switch session.AgentType {
				case model.AgentTypeCore:
					badgeClass = "bg-danger"
					rowClass = "table-danger" // Highlight Core sessions
				case model.AgentTypeHigh:
					badgeClass = "bg-primary"
				case model.AgentTypeLow:
					badgeClass = "bg-success"
				}
				agentTypeBadge = fmt.Sprintf(`<span class="badge %s">%s</span>`, badgeClass, string(session.AgentType))
			} else {
				agentTypeBadge = `<span class="badge bg-secondary">-</span>`
			}

			activeMessagesCount := 0
			if session.ConversationState != nil {
				activeMessagesCount = len(session.ConversationState.Msgs)
			}
			totalMessagesCount := activeMessagesCount + len(session.SummarizedMessages)

			// Get opened files count
			files, err := debugStore.GetOpenedFilesBySession(session.SessionID)
			filesCount := 0
			if err == nil {
				filesCount = len(files)
			}

			html += fmt.Sprintf(`
                        <tr class="%s">
                            <td><code class="text-break">%s</code></td>
                            <td>%s</td>
                            <td class="text-center">%s</td>
                            <td class="text-nowrap"><a href="/agentize/debug/users/%s" class="text-decoration-none">%s</a></td>
                            <td class="text-center"><span class="badge bg-primary">%d</span></td>
                            <td class="text-center"><span class="badge bg-info">%d</span></td>
                            <td class="text-nowrap">%s</td>
                            <td class="text-center"><a href="/agentize/debug/sessions/%s" class="btn btn-sm btn-outline-primary">View Details</a></td>
                        </tr>`,
				rowClass,
				template.HTMLEscapeString(session.SessionID),
				template.HTMLEscapeString(title),
				agentTypeBadge,
				template.URLQueryEscaper(session.UserID),
				template.HTMLEscapeString(session.UserID[:min(20, len(session.UserID))]+"..."),
				totalMessagesCount,
				filesCount,
				FormatTime(session.UpdatedAt),
				template.URLQueryEscaper(session.SessionID))
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>
    </div>
    </div>`
	html += generateBootstrapFooter()

	return html, nil
}

// GenerateMessagesHTML generates the messages list HTML page
func (h *DebugHandler) GenerateMessagesHTML() (string, error) {
	debugStore := h.store.(DebugStore)

	messages, err := debugStore.GetAllMessages()
	if err != nil {
		return "", fmt.Errorf("failed to get messages: %w", err)
	}

	html := generateBootstrapHeader("Agentize Debug - Messages")
	html += generateNavigationBar("/agentize/debug/messages")
	html += `<div class="container">
    <div class="main-container">
        <div class="card">
            <div class="card-header">
                <h4 class="mb-0"><i class="bi bi-chat-dots-fill me-2"></i>All Messages (` + fmt.Sprintf("%d", len(messages)) + `)</h4>
            </div>
            <div class="card-body">`

	if len(messages) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No messages found.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-striped table-hover align-middle">
                    <thead>
                        <tr>
                            <th class="text-nowrap">Time</th>
                            <th class="text-center text-nowrap">Role</th>
                            <th>Content</th>
                            <th class="text-nowrap">User</th>
                            <th class="text-nowrap">Session</th>
                            <th class="text-center text-nowrap">Tool Calls</th>
                        </tr>
                    </thead>
                    <tbody>`

		for _, msg := range messages {
			content := msg.Content
			if len(content) > 150 {
				content = content[:150] + "..."
			}
			badgeClass := "bg-secondary"
			switch msg.Role {
			case openai.ChatMessageRoleUser:
				badgeClass = "bg-primary"
			case openai.ChatMessageRoleAssistant:
				badgeClass = "bg-success"
			case openai.ChatMessageRoleTool:
				badgeClass = "bg-warning text-dark"
			case openai.ChatMessageRoleSystem:
				badgeClass = "bg-info"
			}

			toolCallBadge := ""
			if msg.HasToolCalls {
				toolCallBadge = `<span class="badge bg-danger">Yes</span>`
			} else {
				toolCallBadge = `<span class="badge bg-secondary">No</span>`
			}

			html += fmt.Sprintf(`
                        <tr>
                            <td class="text-nowrap">%s</td>
                            <td class="text-center"><span class="badge %s">%s</span></td>
                            <td class="text-break">%s</td>
                            <td class="text-nowrap"><a href="/agentize/debug/users/%s" class="text-decoration-none">%s</a></td>
                            <td class="text-nowrap"><a href="/agentize/debug/sessions/%s" class="text-decoration-none">%s</a></td>
                            <td class="text-center">%s</td>
                        </tr>`,
				FormatTime(msg.CreatedAt),
				badgeClass,
				template.HTMLEscapeString(msg.Role),
				template.HTMLEscapeString(content),
				template.URLQueryEscaper(msg.UserID),
				template.HTMLEscapeString(msg.UserID[:min(20, len(msg.UserID))]+"..."),
				template.URLQueryEscaper(msg.SessionID),
				template.HTMLEscapeString(msg.SessionID[:min(20, len(msg.SessionID))]+"..."),
				toolCallBadge)
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>
    </div>
    </div>`
	html += generateBootstrapFooter()

	return html, nil
}

// GenerateFilesHTML generates the opened files list HTML page
func (h *DebugHandler) GenerateFilesHTML() (string, error) {
	debugStore := h.store.(DebugStore)

	files, err := debugStore.GetAllOpenedFiles()
	if err != nil {
		return "", fmt.Errorf("failed to get files: %w", err)
	}

	html := generateBootstrapHeader("Agentize Debug - Opened Files")
	html += generateNavigationBar("/agentize/debug/files")
	html += `<div class="container">
    <div class="main-container">
        <div class="card">
            <div class="card-header">
                <h4 class="mb-0"><i class="bi bi-folder-fill me-2"></i>All Opened Files (` + fmt.Sprintf("%d", len(files)) + `)</h4>
            </div>
            <div class="card-body">`

	if len(files) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No opened files found.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-striped table-hover align-middle">
                    <thead>
                        <tr>
                            <th>File Path</th>
                            <th>File Name</th>
                            <th class="text-center text-nowrap">Status</th>
                            <th class="text-nowrap">Opened At</th>
                            <th class="text-nowrap">Closed At</th>
                            <th class="text-nowrap">User</th>
                            <th class="text-nowrap">Session</th>
                        </tr>
                    </thead>
                    <tbody>`

		for _, f := range files {
			status := `<span class="badge bg-success">✅ Open</span>`
			if !f.IsOpen {
				status = `<span class="badge bg-secondary">❌ Closed</span>`
			}
			closedAt := "N/A"
			if !f.ClosedAt.IsZero() {
				closedAt = FormatTime(f.ClosedAt)
			}
			html += fmt.Sprintf(`
                        <tr>
                            <td><code class="text-break">%s</code></td>
                            <td>%s</td>
                            <td class="text-center">%s</td>
                            <td class="text-nowrap">%s</td>
                            <td class="text-nowrap">%s</td>
                            <td class="text-nowrap"><a href="/agentize/debug/users/%s" class="text-decoration-none">%s</a></td>
                            <td class="text-nowrap"><a href="/agentize/debug/sessions/%s" class="text-decoration-none">%s</a></td>
                        </tr>`,
				template.HTMLEscapeString(f.FilePath),
				template.HTMLEscapeString(f.FileName),
				status,
				FormatTime(f.OpenedAt),
				closedAt,
				template.URLQueryEscaper(f.UserID),
				template.HTMLEscapeString(f.UserID[:min(20, len(f.UserID))]+"..."),
				template.URLQueryEscaper(f.SessionID),
				template.HTMLEscapeString(f.SessionID[:min(20, len(f.SessionID))]+"..."))
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>
    </div>
    </div>`
	html += generateBootstrapFooter()

	return html, nil
}

// GenerateToolCallsHTML generates the tool calls list HTML page
func (h *DebugHandler) GenerateToolCallsHTML() (string, error) {
	debugStore := h.store.(DebugStore)

	// Get tool calls from database
	dbToolCalls, err := debugStore.GetAllToolCalls()
	if err != nil {
		return "", fmt.Errorf("failed to get tool calls: %w", err)
	}

	// Convert to ToolCallInfo for display
	var allToolCalls []ToolCallInfo
	for _, tc := range dbToolCalls {
		allToolCalls = append(allToolCalls, ToolCallInfo{
			SessionID:    tc.SessionID,
			UserID:       tc.UserID,
			MessageID:    tc.MessageID,
			ToolCallID:   tc.ToolCallID,
			FunctionName: tc.FunctionName,
			Arguments:    tc.Arguments,
			Result:       tc.Response,
			CreatedAt:    tc.CreatedAt,
		})
	}

	html := generateBootstrapHeader("Agentize Debug - Tool Calls")
	html += generateNavigationBar("/agentize/debug/tool-calls")
	html += `<div class="container">
    <div class="main-container">
        <div class="card">
            <div class="card-header">
                <h4 class="mb-0"><i class="bi bi-tools me-2"></i>All Tool Calls (` + fmt.Sprintf("%d", len(allToolCalls)) + `)</h4>
            </div>
            <div class="card-body">`

	if len(allToolCalls) == 0 {
		html += `<div class="alert alert-info text-center">
                <i class="bi bi-info-circle me-2"></i>No tool calls found.
            </div>`
	} else {
		html += `<div class="table-responsive">
                <table class="table table-striped table-hover align-middle">
                    <thead>
                        <tr>
                            <th class="text-nowrap">Function</th>
                            <th>Arguments</th>
                            <th>Result</th>
                            <th class="text-nowrap">User</th>
                            <th class="text-nowrap">Session</th>
                            <th class="text-nowrap">Time</th>
                        </tr>
                    </thead>
                    <tbody>`

		for _, tc := range allToolCalls {
			args := tc.Arguments
			if len(args) > 100 {
				args = args[:100] + "..."
			}
			result := tc.Result
			if len(result) > 100 {
				result = result[:100] + "..."
			}
			html += fmt.Sprintf(`
                        <tr>
                            <td class="text-nowrap"><code>%s</code></td>
                            <td><pre class="mb-0" style="max-width: 200px; overflow: auto; font-size: 0.8em; white-space: pre-wrap; word-wrap: break-word;">%s</pre></td>
                            <td><pre class="mb-0" style="max-width: 200px; overflow: auto; font-size: 0.8em; white-space: pre-wrap; word-wrap: break-word;">%s</pre></td>
                            <td class="text-nowrap"><a href="/agentize/debug/users/%s" class="text-decoration-none">%s</a></td>
                            <td class="text-nowrap"><a href="/agentize/debug/sessions/%s" class="text-decoration-none">%s</a></td>
                            <td class="text-nowrap">%s</td>
                        </tr>`,
				template.HTMLEscapeString(tc.FunctionName),
				template.HTMLEscapeString(args),
				template.HTMLEscapeString(result),
				template.URLQueryEscaper(tc.UserID),
				template.HTMLEscapeString(tc.UserID[:min(20, len(tc.UserID))]+"..."),
				template.URLQueryEscaper(tc.SessionID),
				template.HTMLEscapeString(tc.SessionID[:min(20, len(tc.SessionID))]+"..."),
				FormatTime(tc.CreatedAt))
		}

		html += `</tbody>
                </table>
            </div>`
	}

	html += `</div>
        </div>
    </div>
    </div>`
	html += generateBootstrapFooter()

	return html, nil
}
