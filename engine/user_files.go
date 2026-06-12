package engine

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/metrics"
	"github.com/ghiac/agentize/model"
)

// userFileStore is the subset of store methods used for user files. Engine.Sessions
// is the full store.Store, which always provides these, so userFiles() never fails;
// the interface is kept only to narrow the surface the file code depends on.
type userFileStore interface {
	PutUserFile(*model.UserFile) error
	GetUserFile(string) (*model.UserFile, error)
	GetUserFilesByUser(string) ([]*model.UserFile, error)
}

// userFiles returns the store narrowed to the user-file methods. The bool is
// always true (kept for call-site compatibility) because Engine.Sessions is a
// store.Store, which implements the full contract.
func (e *Engine) userFiles() (userFileStore, bool) {
	return e.Sessions, true
}

// ImageEditorFunc edits an image given an instruction, returning the edited
// image plus the model/token usage of the underlying image-model call (so the
// host can meter/bill it). It is pluggable so an application can wire any
// image-capable model/API. The implementation owns its own timeouts.
type ImageEditorFunc func(image []byte, mimeType, instruction string) (*model.ImageEditResult, error)

// SetImageEditor wires the function used by the manage_files edit_image action.
func (e *Engine) SetImageEditor(editor ImageEditorFunc) {
	e.ImageEditor = editor
}

// RecordUserFile stores the given bytes via the FileStore and records the
// metadata in the database, returning the saved record. It is the single entry
// point shared by the upload path and the manage_files "save" action.
//
// source should be model.FileSourceUploaded for files the user sent, or
// model.FileSourceGenerated for files produced for the user.
func (e *Engine) RecordUserFile(sessionID, name, mimeType string, source model.FileSource, data []byte) (*model.UserFile, error) {
	return e.recordUserFile(sessionID, name, mimeType, source, "", data)
}

// recordUserFile is the core implementation; parentFileID links a derived file
// (e.g. an edited image) back to its original while keeping it independent.
func (e *Engine) recordUserFile(sessionID, name, mimeType string, source model.FileSource, parentFileID string, data []byte) (*model.UserFile, error) {
	if e.Files == nil {
		return nil, fmt.Errorf("file store not configured")
	}
	st, ok := e.userFiles()
	if !ok {
		return nil, fmt.Errorf("session store does not support user files")
	}

	session, err := e.Sessions.Get(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to load session: %w", err)
	}

	if name == "" {
		name = "file"
	}
	if mimeType == "" {
		mimeType = detectMIME(name, data)
	}

	// Allocate a sequential file ID and persist the incremented counter.
	fileID := session.GenerateUserFileID()
	if err := e.Sessions.Put(session); err != nil {
		return nil, fmt.Errorf("failed to persist session: %w", err)
	}

	key := fmt.Sprintf("%s/%s-%s", session.UserID, fileID, sanitizeFileName(name))
	storageKey, err := e.Files.Save(key, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to store file bytes: %w", err)
	}

	uf := &model.UserFile{
		FileID:       fileID,
		UserID:       session.UserID,
		SessionID:    sessionID,
		Name:         name,
		MIMEType:     mimeType,
		Size:         int64(len(data)),
		StorageKey:   storageKey,
		Source:       source,
		ParentFileID: parentFileID,
		CreatedAt:    time.Now(),
	}
	if err := st.PutUserFile(uf); err != nil {
		_ = e.Files.Delete(storageKey) // best-effort rollback of orphaned bytes
		return nil, fmt.Errorf("failed to record file metadata: %w", err)
	}
	metrics.FileBytes("stored", uf.Size)
	return uf, nil
}

// UpdateUserFileContent overwrites the bytes of an existing file in place and
// updates its size/MIME metadata. Used by the manage_files text "edit" action.
func (e *Engine) UpdateUserFileContent(fileID string, data []byte) (*model.UserFile, error) {
	if e.Files == nil {
		return nil, fmt.Errorf("file store not configured")
	}
	st, ok := e.userFiles()
	if !ok {
		return nil, fmt.Errorf("session store does not support user files")
	}
	meta, err := st.GetUserFile(fileID)
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, fmt.Errorf("file not found: %s", fileID)
	}
	if _, err := e.Files.Save(meta.StorageKey, bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("failed to write file bytes: %w", err)
	}
	meta.Size = int64(len(data))
	if err := st.PutUserFile(meta); err != nil {
		return nil, fmt.Errorf("failed to update file metadata: %w", err)
	}
	return meta, nil
}

// EditImageFile edits the source image via the configured ImageEditor and saves
// the result as a NEW independent file linked to the original via ParentFileID.
// It also returns the editor's *model.ImageEditResult so the caller (the
// manage_files edit_image action) can attach the image-model usage to its
// billing/usage event.
func (e *Engine) EditImageFile(sessionID, sourceFileID, instruction, name string) (*model.UserFile, *model.ImageEditResult, error) {
	if e.ImageEditor == nil {
		return nil, nil, fmt.Errorf("image editor not configured")
	}
	data, meta, err := e.ReadUserFile(sourceFileID)
	if err != nil {
		return nil, nil, err
	}
	log.Log.Infof("[Engine] 🎨 edit_image start | user=%s | source=%s (%s, %dB) | instruction=%q",
		meta.UserID, sourceFileID, meta.MIMEType, len(data), truncateForLog(instruction, 120))

	result, err := e.ImageEditor(data, meta.MIMEType, instruction)
	if err != nil {
		log.Log.Warnf("[Engine] ❌ edit_image failed | source=%s | err=%v", sourceFileID, err)
		return nil, nil, fmt.Errorf("image edit failed: %w", err)
	}
	if result == nil || len(result.Data) == 0 {
		log.Log.Warnf("[Engine] ❌ edit_image returned no image | source=%s", sourceFileID)
		return nil, nil, fmt.Errorf("image editor returned no image")
	}
	editedMIME := result.MIMEType
	if editedMIME == "" {
		editedMIME = meta.MIMEType
	}
	if name == "" {
		name = derivedImageName(meta.Name, editedMIME)
	}
	uf, err := e.recordUserFile(sessionID, name, editedMIME, model.FileSourceGenerated, meta.FileID, result.Data)
	if err != nil {
		log.Log.Warnf("[Engine] ❌ edit_image save failed | source=%s | err=%v", sourceFileID, err)
		return nil, result, err
	}
	log.Log.Infof("[Engine] ✅ edit_image saved | source=%s → new=%s | %s | %dB (was %dB) | model=%s | tokens in=%d out=%d",
		sourceFileID, uf.FileID, uf.MIMEType, uf.Size, len(data), result.Model, result.InputTokens, result.OutputTokens)
	return uf, result, nil
}

// ReadUserFile returns the bytes and metadata for a file by ID.
func (e *Engine) ReadUserFile(fileID string) ([]byte, *model.UserFile, error) {
	if e.Files == nil {
		return nil, nil, fmt.Errorf("file store not configured")
	}
	st, ok := e.userFiles()
	if !ok {
		return nil, nil, fmt.Errorf("session store does not support user files")
	}

	meta, err := st.GetUserFile(fileID)
	if err != nil {
		return nil, nil, err
	}
	if meta == nil {
		return nil, nil, fmt.Errorf("file not found: %s", fileID)
	}

	rc, err := e.Files.Open(meta.StorageKey)
	if err != nil {
		return nil, meta, fmt.Errorf("failed to open file bytes: %w", err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, meta, fmt.Errorf("failed to read file bytes: %w", err)
	}
	metrics.FileBytes("read", int64(len(data)))
	return data, meta, nil
}

// ListUserFiles returns all files owned by the user, newest first.
func (e *Engine) ListUserFiles(userID string) ([]*model.UserFile, error) {
	st, ok := e.userFiles()
	if !ok {
		return nil, fmt.Errorf("session store does not support user files")
	}
	return st.GetUserFilesByUser(userID)
}

// DeleteUserFileBytes removes the stored bytes for all of a user's files via the
// FileStore. Metadata rows are removed separately by the store's DeleteUserData;
// this prevents orphaned bytes from lingering after a user is purged. It is
// best-effort: individual deletion failures are aggregated and returned, but do
// not stop processing of the remaining files. No-op when no file store is set.
func (e *Engine) DeleteUserFileBytes(userID string) error {
	if e.Files == nil {
		return nil
	}
	files, err := e.ListUserFiles(userID)
	if err != nil {
		return err
	}
	var failures []string
	for _, f := range files {
		if err := e.Files.Delete(f.StorageKey); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", f.FileID, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("failed to delete some file bytes: %s", strings.Join(failures, "; "))
	}
	return nil
}

// sanitizeFileName makes a filename safe to embed in a storage key: drops any
// directory components and replaces characters outside a small safe set.
func sanitizeFileName(name string) string {
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, " ", "_")
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "file"
	}
	return out
}

// detectMIME derives a content type from the filename extension, falling back to
// sniffing the first bytes of content.
func detectMIME(name string, data []byte) string {
	if ext := filepath.Ext(name); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return t
		}
	}
	if len(data) > 0 {
		return http.DetectContentType(data)
	}
	return "application/octet-stream"
}

// derivedImageName builds a name for an edited image based on the original.
func derivedImageName(originalName, editedMIME string) string {
	ext := filepath.Ext(originalName)
	base := strings.TrimSuffix(filepath.Base(originalName), ext)
	if base == "" {
		base = "image"
	}
	if exts, _ := mime.ExtensionsByType(editedMIME); len(exts) > 0 {
		ext = exts[0]
	}
	if ext == "" {
		ext = ".png"
	}
	return base + "-edited" + ext
}

// isTextMIME reports whether a MIME type is safe to render as text to the LLM.
func isTextMIME(mimeType string) bool {
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch {
	case strings.Contains(mimeType, "json"),
		strings.Contains(mimeType, "xml"),
		strings.Contains(mimeType, "yaml"),
		strings.Contains(mimeType, "csv"),
		strings.Contains(mimeType, "javascript"),
		strings.Contains(mimeType, "x-www-form-urlencoded"):
		return true
	}
	return false
}
