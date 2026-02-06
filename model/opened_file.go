package model

import (
	"fmt"
	"time"
)

// OpenedFile represents a file that was opened in a session
type OpenedFile struct {
	// FileID is a unique identifier for this opened file record
	FileID string

	// SessionID identifies the session this file belongs to
	SessionID string

	// UserID identifies the user who owns the session
	UserID string

	// FilePath is the path of the opened file/node
	FilePath string

	// FileName is the name/title of the file
	FileName string

	// OpenedAt is when the file was opened
	OpenedAt time.Time

	// ClosedAt is when the file was closed (zero if still open)
	ClosedAt time.Time

	// IsOpen indicates if the file is currently open
	IsOpen bool
}

// NewOpenedFile creates a new opened file record
func NewOpenedFile(sessionID string, userID string, filePath string, fileName string) *OpenedFile {
	now := time.Now()
	return &OpenedFile{
		FileID:    generateFileID(sessionID, filePath, now),
		SessionID: sessionID,
		UserID:    userID,
		FilePath:  filePath,
		FileName:  fileName,
		OpenedAt:  now,
		ClosedAt:  time.Time{},
		IsOpen:    true,
	}
}

// Close marks the file as closed
func (f *OpenedFile) Close() {
	f.IsOpen = false
	f.ClosedAt = time.Now()
}

// generateFileID generates a unique file ID
func generateFileID(sessionID string, filePath string, timestamp time.Time) string {
	date := timestamp.Format("060102150405") // YYMMDDHHMMSS
	return fmt.Sprintf("%s-%s-%s-%s", date, sessionID[:min(8, len(sessionID))], filePath[:min(8, len(filePath))], randomString(4))
}
