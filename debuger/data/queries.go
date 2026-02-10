package data

import (
	"sort"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/model"
)

// DataProvider provides access to debug data from the store
type DataProvider struct {
	store debuger.DebugStore
}

// NewDataProvider creates a new data provider
func NewDataProvider(store debuger.DebugStore) *DataProvider {
	return &DataProvider{store: store}
}

// getSessionLastActivity returns the last activity time for a session
// It uses ConversationState.LastActivity if available, otherwise falls back to UpdatedAt
func getSessionLastActivity(s *model.Session) time.Time {
	if s.ConversationState != nil && !s.ConversationState.LastActivity.IsZero() {
		return s.ConversationState.LastActivity
	}
	return s.UpdatedAt
}

// GetAllSessionsSorted returns all sessions grouped by userID, sorted by LastActivity (newest first)
func (dp *DataProvider) GetAllSessionsSorted() (map[string][]*model.Session, error) {
	sessionsByUser, err := dp.store.GetAllSessions()
	if err != nil {
		return nil, err
	}

	// Sort sessions by LastActivity (newest first), fallback to UpdatedAt
	for userID := range sessionsByUser {
		sort.Slice(sessionsByUser[userID], func(i, j int) bool {
			return getSessionLastActivity(sessionsByUser[userID][i]).After(getSessionLastActivity(sessionsByUser[userID][j]))
		})
	}

	return sessionsByUser, nil
}

// GetAllSessionsFlat returns all sessions as a flat slice, sorted by LastActivity (newest first)
func (dp *DataProvider) GetAllSessionsFlat() ([]*model.Session, error) {
	sessionsByUser, err := dp.GetAllSessionsSorted()
	if err != nil {
		return nil, err
	}

	var allSessions []*model.Session
	for _, sessions := range sessionsByUser {
		allSessions = append(allSessions, sessions...)
	}

	// Sort by LastActivity (newest first), fallback to UpdatedAt
	sort.Slice(allSessions, func(i, j int) bool {
		return getSessionLastActivity(allSessions[i]).After(getSessionLastActivity(allSessions[j]))
	})

	return allSessions, nil
}

// GetSessionCount returns total number of sessions
func (dp *DataProvider) GetSessionCount() (int, error) {
	sessionsByUser, err := dp.GetAllSessionsSorted()
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
func (dp *DataProvider) GetUserCount() (int, error) {
	users, err := dp.store.GetAllUsers()
	if err != nil {
		return 0, err
	}
	return len(users), nil
}

// GetAllUsers returns all users sorted by CreatedAt (newest first)
func (dp *DataProvider) GetAllUsers() ([]*model.User, error) {
	users, err := dp.store.GetAllUsers()
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (newest first)
	sort.Slice(users, func(i, j int) bool {
		return users[i].CreatedAt.After(users[j].CreatedAt)
	})

	return users, nil
}

// GetUser returns a single user
func (dp *DataProvider) GetUser(userID string) (*model.User, error) {
	return dp.store.GetUser(userID)
}

// GetSession returns a single session
func (dp *DataProvider) GetSession(sessionID string) (*model.Session, error) {
	return dp.store.GetSession(sessionID)
}

// GetAllMessages returns all messages sorted by CreatedAt (newest first)
func (dp *DataProvider) GetAllMessages() ([]*model.Message, error) {
	messages, err := dp.store.GetAllMessages()
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (newest first)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].CreatedAt.After(messages[j].CreatedAt)
	})

	return messages, nil
}

// GetMessagesBySession returns messages for a session sorted by CreatedAt (oldest first for conversation flow)
func (dp *DataProvider) GetMessagesBySession(sessionID string) ([]*model.Message, error) {
	messages, err := dp.store.GetMessagesBySession(sessionID)
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (oldest first for natural conversation order)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].CreatedAt.Before(messages[j].CreatedAt)
	})

	return messages, nil
}

// GetMessagesBySessionDesc returns messages for a session sorted by CreatedAt (newest first for listing)
func (dp *DataProvider) GetMessagesBySessionDesc(sessionID string) ([]*model.Message, error) {
	messages, err := dp.store.GetMessagesBySession(sessionID)
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (newest first)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].CreatedAt.After(messages[j].CreatedAt)
	})

	return messages, nil
}

// GetMessagesByUser returns messages for a user sorted by CreatedAt (newest first)
func (dp *DataProvider) GetMessagesByUser(userID string) ([]*model.Message, error) {
	messages, err := dp.store.GetMessagesByUser(userID)
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (newest first)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].CreatedAt.After(messages[j].CreatedAt)
	})

	return messages, nil
}

// GetAllOpenedFiles returns all opened files sorted by OpenedAt (newest first)
func (dp *DataProvider) GetAllOpenedFiles() ([]*model.OpenedFile, error) {
	files, err := dp.store.GetAllOpenedFiles()
	if err != nil {
		return nil, err
	}

	// Sort by OpenedAt (newest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].OpenedAt.After(files[j].OpenedAt)
	})

	return files, nil
}

// GetOpenedFilesBySession returns opened files for a session sorted by OpenedAt (newest first)
func (dp *DataProvider) GetOpenedFilesBySession(sessionID string) ([]*model.OpenedFile, error) {
	files, err := dp.store.GetOpenedFilesBySession(sessionID)
	if err != nil {
		return nil, err
	}

	// Sort by OpenedAt (newest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].OpenedAt.After(files[j].OpenedAt)
	})

	return files, nil
}

// GetOpenedFilesByUser returns opened files for a user sorted by OpenedAt (newest first)
func (dp *DataProvider) GetOpenedFilesByUser(userID string) ([]*model.OpenedFile, error) {
	allFiles, err := dp.store.GetAllOpenedFiles()
	if err != nil {
		return nil, err
	}
	var userFiles []*model.OpenedFile
	for _, f := range allFiles {
		if f.UserID == userID {
			userFiles = append(userFiles, f)
		}
	}

	// Sort by OpenedAt (newest first)
	sort.Slice(userFiles, func(i, j int) bool {
		return userFiles[i].OpenedAt.After(userFiles[j].OpenedAt)
	})

	return userFiles, nil
}

// GetAllToolCalls returns all tool calls sorted by CreatedAt (newest first)
func (dp *DataProvider) GetAllToolCalls() ([]*model.ToolCall, error) {
	toolCalls, err := dp.store.GetAllToolCalls()
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (newest first)
	sort.Slice(toolCalls, func(i, j int) bool {
		return toolCalls[i].CreatedAt.After(toolCalls[j].CreatedAt)
	})

	return toolCalls, nil
}

// GetToolCallsBySession returns tool calls for a session sorted by CreatedAt (oldest first for natural flow)
func (dp *DataProvider) GetToolCallsBySession(sessionID string) ([]*model.ToolCall, error) {
	toolCalls, err := dp.store.GetToolCallsBySession(sessionID)
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (oldest first for natural conversation order)
	sort.Slice(toolCalls, func(i, j int) bool {
		return toolCalls[i].CreatedAt.Before(toolCalls[j].CreatedAt)
	})

	return toolCalls, nil
}

// GetToolCallByID returns a tool call by its ID
func (dp *DataProvider) GetToolCallByID(toolCallID string) (*model.ToolCall, error) {
	return dp.store.GetToolCallByID(toolCallID)
}

// GetAllSummarizationLogs returns all summarization logs
func (dp *DataProvider) GetAllSummarizationLogs() ([]*model.SummarizationLog, error) {
	logs, err := dp.store.GetAllSummarizationLogs()
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (newest first)
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].CreatedAt.After(logs[j].CreatedAt)
	})

	return logs, nil
}

// GetSummarizationLogsBySession returns summarization logs for a session sorted by CreatedAt (newest first)
func (dp *DataProvider) GetSummarizationLogsBySession(sessionID string) ([]*model.SummarizationLog, error) {
	logs, err := dp.store.GetSummarizationLogsBySession(sessionID)
	if err != nil {
		return nil, err
	}

	// Sort by CreatedAt (newest first)
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].CreatedAt.After(logs[j].CreatedAt)
	})

	return logs, nil
}

// GetDashboardStats returns statistics for the dashboard
func (dp *DataProvider) GetDashboardStats() (*debuger.DashboardStats, error) {
	userCount, err := dp.GetUserCount()
	if err != nil {
		return nil, err
	}

	sessionCount, err := dp.GetSessionCount()
	if err != nil {
		return nil, err
	}

	messages, err := dp.store.GetAllMessages()
	if err != nil {
		return nil, err
	}

	files, err := dp.store.GetAllOpenedFiles()
	if err != nil {
		return nil, err
	}

	// Count tool calls
	toolCallCount := 0
	for _, msg := range messages {
		if msg.HasToolCalls {
			toolCallCount++
		}
	}

	return &debuger.DashboardStats{
		TotalUsers:     userCount,
		TotalSessions:  sessionCount,
		TotalMessages:  len(messages),
		TotalFiles:     len(files),
		TotalToolCalls: toolCallCount,
	}, nil
}

// GetSummarizationStats returns statistics for summarization
func (dp *DataProvider) GetSummarizationStats(config *debuger.SchedulerConfig) (*debuger.SummarizationStats, *debuger.SessionStats, error) {
	logs, err := dp.store.GetAllSummarizationLogs()
	if err != nil {
		return nil, nil, err
	}

	// Count log statuses
	sumStats := &debuger.SummarizationStats{
		TotalLogs: len(logs),
	}
	for _, log := range logs {
		switch log.Status {
		case "success":
			sumStats.SuccessLogs++
		case "failed":
			sumStats.FailedLogs++
		default:
			sumStats.PendingLogs++
		}
	}

	// Get session stats
	sessions, err := dp.GetAllSessionsFlat()
	if err != nil {
		return nil, nil, err
	}

	threshold := 5 // default
	if config != nil && config.FirstSummarizationThreshold > 0 {
		threshold = config.FirstSummarizationThreshold
	}

	sessStats := &debuger.SessionStats{
		TotalSessions: len(sessions),
	}

	for _, session := range sessions {
		if !session.SummarizedAt.IsZero() {
			sessStats.SummarizedSessions++
		}

		msgCount := 0
		if session.ConversationState != nil {
			msgCount = len(session.ConversationState.Msgs)
		}

		if msgCount > 0 {
			sessStats.SessionsWithMessages++
		} else {
			sessStats.SessionsWithoutMessages++
		}

		// Check if eligible for summarization
		if session.SummarizedAt.IsZero() && msgCount >= threshold {
			sessStats.EligibleSessions++
		}
	}

	return sumStats, sessStats, nil
}

// ConvertToolCallsToInfo converts model.ToolCall to debuger.ToolCallInfo
func ConvertToolCallsToInfo(toolCalls []*model.ToolCall) []debuger.ToolCallInfo {
	result := make([]debuger.ToolCallInfo, len(toolCalls))
	for i, tc := range toolCalls {
		result[i] = debuger.ToolCallInfo{
			SessionID:    tc.SessionID,
			UserID:       tc.UserID,
			MessageID:    tc.MessageID,
			ToolCallID:   tc.ToolCallID,
			AgentType:    string(tc.AgentType),
			FunctionName: tc.FunctionName,
			Arguments:    tc.Arguments,
			Result:       tc.Response,
			ResultLength: tc.ResponseLength,
			DurationMs:   tc.DurationMs,
			CreatedAt:    tc.CreatedAt,
		}
	}
	return result
}
