package store

import (
	"errors"
	"fmt"
	"io"

	"github.com/ghiac/agentize/model"
)

// ErrValidation marks rejections of malformed entities at the store boundary.
// Check with errors.Is.
var ErrValidation = errors.New("store: validation failed")

// ErrQuotaExceeded is returned by Put* methods when a configured storage quota
// would be exceeded. Callers (the agent layer) can detect it with errors.Is and
// answer the user gracefully instead of failing opaquely.
var ErrQuotaExceeded = errors.New("store: quota exceeded")

// ErrBackupUnsupported is returned by backends that cannot produce a backup
// stream themselves (MongoDB: use mongodump against the cluster instead).
var ErrBackupUnsupported = errors.New("store: backup not supported by this backend")

// Quotas bounds per-user/per-session growth so a single user cannot grow
// storage without limit (storage DoS). Zero values mean unlimited. Enforced by
// PutMessage/PutMessages (MaxMessagesPerSession) and PutUserFile (file count
// and total bytes per user) on every backend, returning ErrQuotaExceeded.
type Quotas struct {
	MaxMessagesPerSession int
	MaxUserFilesPerUser   int
	MaxFileBytesPerUser   int64
}

// Issue is one integrity problem found by Verify — typically an orphaned child
// row whose parent (session/user) no longer exists.
type Issue struct {
	Type   string // e.g. "orphaned_message", "orphaned_tool_call"
	ID     string // the offending entity's ID
	Detail string // human-readable description
}

// Maintainer is the optional maintenance interface backends implement alongside
// Store. Obtain it with a type assertion; all shipped backends implement it.
type Maintainer interface {
	// Backup streams a consistent snapshot of the database to w. SQLite-backed
	// stores use VACUUM INTO; MongoDB returns ErrBackupUnsupported (use
	// mongodump against the cluster).
	Backup(w io.Writer) error
	// Verify scans for integrity issues (orphaned messages, tool calls, opened
	// files, user files whose session/user is gone) and returns what it finds.
	Verify() ([]Issue, error)
}

func validationErr(entity, field string) error {
	return fmt.Errorf("%w: %s has empty %s", ErrValidation, entity, field)
}

func validateSession(s *model.Session) error {
	if s == nil {
		return fmt.Errorf("%w: session cannot be nil", ErrValidation)
	}
	if s.SessionID == "" {
		return validationErr("session", "SessionID")
	}
	if s.UserID == "" {
		return validationErr("session", "UserID")
	}
	if s.AgentType == "" {
		return validationErr("session", "AgentType")
	}
	return nil
}

func validateUser(u *model.User) error {
	if u == nil {
		return fmt.Errorf("%w: user cannot be nil", ErrValidation)
	}
	if u.UserID == "" {
		return validationErr("user", "UserID")
	}
	return nil
}

func validateMessage(m *model.Message) error {
	if m == nil {
		return fmt.Errorf("%w: message cannot be nil", ErrValidation)
	}
	if m.MessageID == "" {
		return validationErr("message", "MessageID")
	}
	if m.SessionID == "" {
		return validationErr("message", "SessionID")
	}
	if m.UserID == "" {
		return validationErr("message", "UserID")
	}
	return nil
}

func validateToolCall(tc *model.ToolCall) error {
	if tc == nil {
		return fmt.Errorf("%w: toolCall cannot be nil", ErrValidation)
	}
	if tc.ToolCallID == "" {
		return validationErr("toolCall", "ToolCallID")
	}
	if tc.SessionID == "" {
		return validationErr("toolCall", "SessionID")
	}
	if tc.UserID == "" {
		return validationErr("toolCall", "UserID")
	}
	switch tc.Status {
	case "", model.ToolCallStatusPending, model.ToolCallStatusSuccess, model.ToolCallStatusFailed:
	default:
		return fmt.Errorf("%w: toolCall has invalid status %q (want pending|success|failed)", ErrValidation, tc.Status)
	}
	return nil
}

func validateUserFile(f *model.UserFile) error {
	if f == nil {
		return fmt.Errorf("%w: userFile cannot be nil", ErrValidation)
	}
	if f.FileID == "" {
		return validationErr("userFile", "FileID")
	}
	if f.UserID == "" {
		return validationErr("userFile", "UserID")
	}
	if f.StorageKey == "" {
		return validationErr("userFile", "StorageKey")
	}
	return nil
}

func validateOpenedFile(f *model.OpenedFile) error {
	if f == nil {
		return fmt.Errorf("%w: openedFile cannot be nil", ErrValidation)
	}
	if f.FileID == "" {
		return validationErr("openedFile", "FileID")
	}
	if f.SessionID == "" {
		return validationErr("openedFile", "SessionID")
	}
	return nil
}
