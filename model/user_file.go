package model

import (
	"time"
)

// FileSource describes how a UserFile entered the system.
type FileSource string

const (
	// FileSourceUploaded means the user sent the file to the agent.
	FileSourceUploaded FileSource = "uploaded"
	// FileSourceGenerated means the file was produced for the user by an agent.
	FileSourceGenerated FileSource = "generated"
)

// UserFile represents a real, user-scoped file that was either uploaded by the
// user or generated for them. The bytes are stored via a filestore.FileStore
// keyed by StorageKey; this record holds only the metadata.
//
// UserFile is distinct from OpenedFile: OpenedFile tracks knowledge-tree nodes
// opened during a session, whereas UserFile tracks actual user documents.
type UserFile struct {
	// FileID is a unique identifier for this file (e.g. {SessionID}-uf0001).
	FileID string

	// UserID identifies the user who owns the file.
	UserID string

	// SessionID identifies the session the file was created in.
	SessionID string

	// Name is the original/display filename.
	Name string

	// MIMEType is the detected or declared content type.
	MIMEType string

	// Size is the file size in bytes.
	Size int64

	// StorageKey is the key used to retrieve the bytes from the FileStore.
	StorageKey string

	// Source indicates whether the file was uploaded or generated.
	Source FileSource

	// ParentFileID, when set, is the file this one was derived from (e.g. an
	// edited image keeps a link to its original while remaining independent).
	ParentFileID string

	// Summary is an optional human/LLM description shown in list views.
	Summary string

	// CreatedAt is when the file was recorded.
	CreatedAt time.Time
}

// ImageEditResult is the output of an image edit. Data + MIMEType are the edited
// image; Model and the token counts describe the underlying image-model call so
// the host can meter/bill it (the manage_files edit_image action surfaces these
// on its usage event).
type ImageEditResult struct {
	Data         []byte
	MIMEType     string
	Model        string
	InputTokens  int
	OutputTokens int
}

// NewUserFile creates a new UserFile record for the given session, allocating a
// sequential FileID from the session.
func NewUserFile(session *Session, name, mimeType string, size int64, storageKey string, source FileSource) *UserFile {
	return &UserFile{
		FileID:     session.GenerateUserFileID(),
		UserID:     session.UserID,
		SessionID:  session.SessionID,
		Name:       name,
		MIMEType:   mimeType,
		Size:       size,
		StorageKey: storageKey,
		Source:     source,
		CreatedAt:  time.Now(),
	}
}

// GeneratedFilesSince returns generated files present in after but not before.
// It is used by message adapters to discover artifacts created during one turn
// without re-delivering older files. Uploaded files are deliberately excluded.
func GeneratedFilesSince(before, after []*UserFile) []*UserFile {
	existing := make(map[string]struct{}, len(before))
	for _, file := range before {
		if file != nil {
			existing[file.FileID] = struct{}{}
		}
	}

	generated := make([]*UserFile, 0)
	for _, file := range after {
		if file == nil || file.Source != FileSourceGenerated {
			continue
		}
		if _, found := existing[file.FileID]; found {
			continue
		}
		generated = append(generated, file)
	}
	return generated
}
