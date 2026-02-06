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

// DebugStore is an interface for stores that support debugging
type DebugStore interface {
	GetAllSessions() (map[string][]*model.Session, error)
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
	sessionsByUser, err := h.GetAllSessions()
	if err != nil {
		return 0, err
	}
	return len(sessionsByUser), nil
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

// GenerateHTML generates the debug HTML page
func (h *DebugHandler) GenerateHTML() (string, error) {
	sessionsByUser, err := h.GetAllSessions()
	if err != nil {
		return "", fmt.Errorf("failed to get sessions: %w", err)
	}
	totalSessions, err := h.GetSessionCount()
	if err != nil {
		return "", fmt.Errorf("failed to get session count: %w", err)
	}
	totalUsers, err := h.GetUserCount()
	if err != nil {
		return "", fmt.Errorf("failed to get user count: %w", err)
	}

	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Agentize Debug - Sessions</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Oxygen, Ubuntu, Cantarell, sans-serif;
            background: #f5f7fa;
            padding: 2rem;
            line-height: 1.6;
        }
        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 2rem;
            border-radius: 12px;
            margin-bottom: 2rem;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }
        .header h1 {
            font-size: 2rem;
            margin-bottom: 0.5rem;
        }
        .stats {
            display: flex;
            gap: 2rem;
            margin-top: 1rem;
        }
        .stat {
            background: rgba(255, 255, 255, 0.2);
            padding: 0.75rem 1.5rem;
            border-radius: 8px;
        }
        .stat-value {
            font-size: 1.5rem;
            font-weight: 700;
        }
        .stat-label {
            font-size: 0.875rem;
            opacity: 0.9;
        }
        .user-section {
            background: white;
            border-radius: 12px;
            padding: 1.5rem;
            margin-bottom: 2rem;
            box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
        }
        .user-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding-bottom: 1rem;
            border-bottom: 2px solid #e2e8f0;
            margin-bottom: 1rem;
        }
        .user-id {
            font-size: 1.25rem;
            font-weight: 600;
            color: #2d3748;
        }
        .session-count {
            background: #edf2f7;
            padding: 0.25rem 0.75rem;
            border-radius: 6px;
            font-size: 0.875rem;
            color: #4a5568;
        }
        .session-card {
            background: #f7fafc;
            border: 1px solid #e2e8f0;
            border-radius: 8px;
            padding: 1.5rem;
            margin-bottom: 1rem;
            transition: all 0.2s;
        }
        .session-card:hover {
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
            border-color: #cbd5e0;
        }
        .session-header {
            display: flex;
            justify-content: space-between;
            align-items: start;
            margin-bottom: 1rem;
            flex-wrap: wrap;
            gap: 1rem;
        }
        .session-info {
            flex: 1;
        }
        .session-id {
            font-family: 'Monaco', 'Courier New', monospace;
            font-size: 0.875rem;
            color: #718096;
            margin-bottom: 0.5rem;
        }
        .session-title {
            font-size: 1.125rem;
            font-weight: 600;
            color: #2d3748;
            margin-bottom: 0.5rem;
        }
        .session-meta {
            display: flex;
            gap: 1rem;
            flex-wrap: wrap;
            font-size: 0.875rem;
            color: #718096;
        }
        .badge {
            display: inline-block;
            padding: 0.25rem 0.75rem;
            border-radius: 6px;
            font-size: 0.75rem;
            font-weight: 600;
            text-transform: uppercase;
        }
        .badge-core {
            background: #fed7d7;
            color: #c53030;
        }
        .badge-high {
            background: #bee3f8;
            color: #2c5282;
        }
        .badge-low {
            background: #c6f6d5;
            color: #22543d;
        }
        .badge-empty {
            background: #e2e8f0;
            color: #4a5568;
        }
        .messages-section {
            margin-top: 1rem;
        }
        .messages-header {
            font-weight: 600;
            color: #4a5568;
            margin-bottom: 0.75rem;
            display: flex;
            align-items: center;
            gap: 0.5rem;
        }
        .message {
            background: white;
            border-left: 3px solid #cbd5e0;
            padding: 1rem;
            margin-bottom: 0.75rem;
            border-radius: 4px;
        }
        .message-user {
            border-left-color: #4299e1;
        }
        .message-assistant {
            border-left-color: #48bb78;
        }
        .message-tool {
            border-left-color: #ed8936;
        }
        .message-system {
            border-left-color: #9f7aea;
        }
        .message-role {
            font-weight: 600;
            color: #2d3748;
            margin-bottom: 0.5rem;
            text-transform: capitalize;
        }
        .message-content {
            color: #4a5568;
            white-space: pre-wrap;
            word-wrap: break-word;
        }
        .message-content pre {
            background: #f7fafc;
            padding: 0.75rem;
            border-radius: 4px;
            overflow-x: auto;
            font-size: 0.875rem;
        }
        .summary {
            background: #fffaf0;
            border-left: 3px solid #f6ad55;
            padding: 1rem;
            margin-top: 1rem;
            border-radius: 4px;
        }
        .summary-title {
            font-weight: 600;
            color: #744210;
            margin-bottom: 0.5rem;
        }
        .summary-content {
            color: #975a16;
        }
        .empty-state {
            text-align: center;
            padding: 3rem;
            color: #718096;
        }
        .empty-state-icon {
            font-size: 4rem;
            margin-bottom: 1rem;
        }
        .toggle-btn {
            background: #667eea;
            color: white;
            border: none;
            padding: 0.5rem 1rem;
            border-radius: 6px;
            cursor: pointer;
            font-size: 0.875rem;
            transition: background 0.2s;
        }
        .toggle-btn:hover {
            background: #5568d3;
        }
        .collapsed .messages-section {
            display: none;
        }
        .user-section {
            position: relative;
        }
        .user-sessions {
            margin-top: 1rem;
        }
        .user-section.collapsed .user-sessions {
            display: none;
        }
        .user-toggle-btn {
            background: #4299e1;
            color: white;
            border: none;
            padding: 0.5rem 1rem;
            border-radius: 6px;
            cursor: pointer;
            font-size: 0.875rem;
            transition: background 0.2s;
            margin-left: 1rem;
        }
        .user-toggle-btn:hover {
            background: #3182ce;
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>🔍 Agentize Debug - Sessions</h1>
        <div class="stats">
            <div class="stat">
                <div class="stat-value">` + fmt.Sprintf("%d", totalUsers) + `</div>
                <div class="stat-label">Users</div>
            </div>
            <div class="stat">
                <div class="stat-value">` + fmt.Sprintf("%d", totalSessions) + `</div>
                <div class="stat-label">Sessions</div>
            </div>
        </div>
    </div>`

	if totalSessions == 0 {
		html += `
    <div class="empty-state">
        <div class="empty-state-icon">📭</div>
        <h2>No sessions found</h2>
        <p>Sessions will appear here once users start interacting with the system.</p>
    </div>`
	} else {
		// Sort users by number of sessions (descending)
		userIDs := make([]string, 0, len(sessionsByUser))
		for userID := range sessionsByUser {
			userIDs = append(userIDs, userID)
		}
		sort.Slice(userIDs, func(i, j int) bool {
			return len(sessionsByUser[userIDs[i]]) > len(sessionsByUser[userIDs[j]])
		})

		for _, userID := range userIDs {
			sessions := sessionsByUser[userID]
			html += fmt.Sprintf(`
    <div class="user-section collapsed">
        <div class="user-header">
            <div style="display: flex; align-items: center; flex: 1;">
                <div class="user-id">👤 User: %s</div>
                <div class="session-count">%d session(s)</div>
            </div>
            <button class="user-toggle-btn" onclick="this.closest('.user-section').classList.toggle('collapsed')">Show Sessions</button>
        </div>
        <div class="user-sessions">`, template.HTMLEscapeString(userID), len(sessions))

			for _, session := range sessions {
				agentTypeBadge := ""
				if session.AgentType != "" {
					badgeClass := "badge-empty"
					switch session.AgentType {
					case model.AgentTypeCore:
						badgeClass = "badge-core"
					case model.AgentTypeHigh:
						badgeClass = "badge-high"
					case model.AgentTypeLow:
						badgeClass = "badge-low"
					}
					agentTypeBadge = fmt.Sprintf(`<span class="badge %s">%s</span>`, badgeClass, string(session.AgentType))
				}

				title := session.Title
				if title == "" {
					title = "Untitled Session"
				}

				// Calculate message stats
				activeMsgs := len(session.ConversationState.Msgs)
				archivedMsgs := len(session.SummarizedMessages)
				totalMsgs := activeMsgs + archivedMsgs
				queueMsgs := len(session.ConversationState.Queue)
				inProgress := session.ConversationState.InProgress

				// Count messages by role
				userMsgs := 0
				assistantMsgs := 0
				toolMsgs := 0
				systemMsgs := 0
				for _, msg := range session.ConversationState.Msgs {
					switch msg.Role {
					case openai.ChatMessageRoleUser:
						userMsgs++
					case openai.ChatMessageRoleAssistant:
						assistantMsgs++
					case openai.ChatMessageRoleTool:
						toolMsgs++
					case openai.ChatMessageRoleSystem:
						systemMsgs++
					}
				}

				inProgressBadge := ""
				if inProgress {
					inProgressBadge = `<span class="badge" style="background: #f6ad55; color: #744210;">In Progress</span> `
				}

				html += fmt.Sprintf(`
        <div class="session-card collapsed">
            <div class="session-header">
                <div class="session-info">
                    <div class="session-id">%s</div>
                    <div class="session-title">%s %s</div>
                    <div class="session-meta">
                        %s
                        <span>Created: %s</span>
                        <span>Updated: %s (%s)</span>
                        <span>Messages: %d active + %d archived = %d total</span>
                        <span>Queue: %d</span>
                        <span>Nodes: %d</span>
                        <span>Tool Results: %d</span>
                    </div>
                    <div class="session-meta" style="margin-top: 0.5rem; font-size: 0.8rem;">
                        <span>📨 User: %d</span>
                        <span>🤖 Assistant: %d</span>
                        <span>🔧 Tool: %d</span>
                        <span>⚙️ System: %d</span>
                    </div>
                </div>
                <button class="toggle-btn" onclick="this.closest('.session-card').classList.toggle('collapsed')">Show Messages</button>
            </div>`,
					template.HTMLEscapeString(session.SessionID),
					template.HTMLEscapeString(title),
					inProgressBadge,
					agentTypeBadge,
					FormatTime(session.CreatedAt),
					FormatTime(session.UpdatedAt),
					FormatDuration(session.UpdatedAt),
					activeMsgs,
					archivedMsgs,
					totalMsgs,
					queueMsgs,
					len(session.NodeDigests),
					len(session.ToolResults),
					userMsgs,
					assistantMsgs,
					toolMsgs,
					systemMsgs)

				// Summary section
				if session.Summary != "" {
					summarizedAt := ""
					if !session.SummarizedAt.IsZero() {
						summarizedAt = fmt.Sprintf(" (Summarized: %s)", FormatTime(session.SummarizedAt))
					}
					html += fmt.Sprintf(`
            <div class="summary">
                <div class="summary-title">📝 Summary%s</div>
                <div class="summary-content">%s</div>
            </div>`, summarizedAt, template.HTMLEscapeString(session.Summary))
				}

				// Tags
				if len(session.Tags) > 0 {
					tagsHTML := ""
					for _, tag := range session.Tags {
						tagsHTML += fmt.Sprintf(`<span class="badge badge-empty">%s</span> `, template.HTMLEscapeString(tag))
					}
					html += fmt.Sprintf(`
            <div style="margin-top: 1rem; padding: 0.75rem; background: #f7fafc; border-radius: 6px;">
                <strong>🏷️ Tags:</strong> %s
            </div>`, tagsHTML)
				}

				// Visited Nodes with details
				if len(session.NodeDigests) > 0 {
					html += `
            <div style="margin-top: 1rem; padding: 0.75rem; background: #f7fafc; border-radius: 6px;">
                <strong>📍 Visited Nodes (%d):</strong>
                <div style="margin-top: 0.5rem; display: grid; gap: 0.5rem;">`
					for _, digest := range session.NodeDigests {
						excerpt := digest.Excerpt
						if len(excerpt) > 100 {
							excerpt = excerpt[:100] + "..."
						}
						html += fmt.Sprintf(`
                    <div style="padding: 0.5rem; background: white; border-radius: 4px; border-left: 3px solid #4299e1;">
                        <div style="font-weight: 600; color: #2d3748;"><code>%s</code></div>
                        <div style="font-size: 0.875rem; color: #4a5568; margin-top: 0.25rem;">%s</div>
                        <div style="font-size: 0.75rem; color: #718096; margin-top: 0.25rem;">%s</div>
                        <div style="font-size: 0.75rem; color: #718096; margin-top: 0.25rem;">Loaded: %s</div>
                    </div>`,
							template.HTMLEscapeString(digest.Path),
							template.HTMLEscapeString(digest.Title),
							template.HTMLEscapeString(excerpt),
							FormatTime(digest.LoadedAt))
					}
					html += `
                </div>
            </div>`
				}

				// Tool Results with details
				if len(session.ToolResults) > 0 {
					html += `
            <div style="margin-top: 1rem; padding: 0.75rem; background: #f7fafc; border-radius: 6px;">
                <strong>🔧 Tool Results (%d):</strong>
                <div style="margin-top: 0.5rem; display: grid; gap: 0.5rem;">`
					for toolID, result := range session.ToolResults {
						resultPreview := result
						if len(resultPreview) > 200 {
							resultPreview = resultPreview[:200] + "..."
						}
						html += fmt.Sprintf(`
                    <div style="padding: 0.5rem; background: white; border-radius: 4px; border-left: 3px solid #ed8936;">
                        <div style="font-weight: 600; color: #2d3748; font-family: monospace; font-size: 0.875rem;">%s</div>
                        <div style="font-size: 0.875rem; color: #4a5568; margin-top: 0.25rem; white-space: pre-wrap; word-wrap: break-word;">%s</div>
                    </div>`,
							template.HTMLEscapeString(toolID),
							template.HTMLEscapeString(resultPreview))
					}
					html += `
                </div>
            </div>`
				}

				// Queue messages
				if queueMsgs > 0 {
					html += `
            <div style="margin-top: 1rem; padding: 0.75rem; background: #fffaf0; border-radius: 6px; border-left: 3px solid #f6ad55;">
                <strong>📬 Queue Messages (%d):</strong>
                <div style="margin-top: 0.5rem;">`
					for _, msg := range session.ConversationState.Queue {
						html += fmt.Sprintf(`
                    <div style="padding: 0.5rem; background: white; border-radius: 4px; margin-bottom: 0.5rem;">
                        <div style="font-weight: 600; color: #744210;">%s</div>
                        <div style="color: #975a16; white-space: pre-wrap;">%s</div>
                    </div>`,
							template.HTMLEscapeString(msg.Role),
							template.HTMLEscapeString(msg.Content))
					}
					html += `
                </div>
            </div>`
				}

				html += fmt.Sprintf(`
            <div class="messages-section">
                <div class="messages-header">
                    💬 Active Messages (%d)
                </div>`, activeMsgs)

				if activeMsgs == 0 {
					html += `
                <div class="empty-state" style="padding: 1rem;">
                    No active messages in this session
                </div>`
				} else {
					for i, msg := range session.ConversationState.Msgs {
						msgIndex := i + 1
						messageClass := ""
						switch msg.Role {
						case openai.ChatMessageRoleUser:
							messageClass = "message-user"
						case openai.ChatMessageRoleAssistant:
							messageClass = "message-assistant"
						case openai.ChatMessageRoleTool:
							messageClass = "message-tool"
						case openai.ChatMessageRoleSystem:
							messageClass = "message-system"
						}

						html += fmt.Sprintf(`
                <div class="message %s">
                    <div class="message-role">#%d - %s</div>
                    <div class="message-content">%s</div>
                </div>`, messageClass, msgIndex, template.HTMLEscapeString(msg.Role), FormatMessage(msg))
					}
				}

				// Archived messages
				if archivedMsgs > 0 {
					html += fmt.Sprintf(`
                <div style="margin-top: 2rem; padding-top: 1rem; border-top: 2px solid #e2e8f0;">
                    <div class="messages-header">
                        📦 Archived Messages (%d)
                    </div>`, archivedMsgs)
					for i, msg := range session.SummarizedMessages {
						msgIndex := i + 1
						messageClass := ""
						switch msg.Role {
						case openai.ChatMessageRoleUser:
							messageClass = "message-user"
						case openai.ChatMessageRoleAssistant:
							messageClass = "message-assistant"
						case openai.ChatMessageRoleTool:
							messageClass = "message-tool"
						case openai.ChatMessageRoleSystem:
							messageClass = "message-system"
						}
						html += fmt.Sprintf(`
                    <div class="message %s" style="opacity: 0.7;">
                        <div class="message-role">#%d (Archived) - %s</div>
                        <div class="message-content">%s</div>
                    </div>`, messageClass, msgIndex, template.HTMLEscapeString(msg.Role), FormatMessage(msg))
					}
					html += `
                </div>`
				}

				html += `
            </div>
        </div>`
			}

			html += `
        </div>
    </div>`
		}
	}

	html += `
    <script>
        // Auto-refresh every 30 seconds
        setTimeout(function() {
            location.reload();
        }, 30000);
    </script>
</body>
</html>`

	return html, nil
}
