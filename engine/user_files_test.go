package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ghiac/agentize/filestore"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/sashabaranov/go-openai"
)

// recordingCallback captures the last Before/AfterAction events for billing tests.
type recordingCallback struct {
	before, after *UsageEvent
	blockErr      error
}

func (c *recordingCallback) BeforeAction(_ context.Context, ev *UsageEvent) error {
	c.before = ev
	return c.blockErr
}
func (c *recordingCallback) AfterAction(_ context.Context, ev *UsageEvent) { c.after = ev }

// editImageToolCall builds a manage_files edit_image tool call for fileID.
func editImageToolCall(fileID string) openai.ToolCall {
	args, _ := json.Marshal(map[string]interface{}{
		"action": "edit_image", "file_id": fileID, "instruction": "make it brighter",
	})
	return openai.ToolCall{ID: "tc1", Function: openai.FunctionCall{Name: "manage_files", Arguments: string(args)}}
}

func TestEditImage_Billing_CarriesModelAndTokens(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	eng.SetImageEditor(func(_ []byte, _, _ string) (*model.ImageEditResult, error) {
		return &model.ImageEditResult{Data: []byte("OUT"), MIMEType: "image/png", Model: "img-model-x", InputTokens: 5, OutputTokens: 9}, nil
	})
	cb := &recordingCallback{}
	eng.Callback = cb

	src, err := eng.RecordUserFile(session.SessionID, "o.png", "image/png", model.FileSourceUploaded, []byte("ORIG"))
	if err != nil {
		t.Fatalf("RecordUserFile: %v", err)
	}

	if _, inj := eng.executeTool(context.Background(), session, "m1", editImageToolCall(src.FileID)); inj != nil {
		t.Errorf("did not expect an injected image")
	}

	// BeforeAction must expose the sub-action so a host can pre-price/limit it.
	if cb.before == nil || cb.before.Metadata["action"] != "edit_image" {
		t.Errorf("BeforeAction missing action metadata: %+v", cb.before)
	}
	// AfterAction must carry the real image-model cost (not a zero-cost tool).
	if cb.after == nil {
		t.Fatal("AfterAction not called")
	}
	if cb.after.Model != "img-model-x" || cb.after.InputTokens != 5 || cb.after.OutputTokens != 9 || cb.after.Tokens != 14 {
		t.Errorf("AfterAction usage wrong: model=%s in=%d out=%d total=%d", cb.after.Model, cb.after.InputTokens, cb.after.OutputTokens, cb.after.Tokens)
	}
	if cb.after.Metadata["media"] != "image" || cb.after.Metadata["action"] != "edit_image" {
		t.Errorf("AfterAction metadata wrong: %v", cb.after.Metadata)
	}
}

func TestEditImage_Billing_BeforeActionBlocks(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	editorCalled := false
	eng.SetImageEditor(func(_ []byte, _, _ string) (*model.ImageEditResult, error) {
		editorCalled = true
		return &model.ImageEditResult{Data: []byte("OUT"), MIMEType: "image/png"}, nil
	})
	eng.Callback = &recordingCallback{blockErr: errors.New("insufficient credit")}

	src, _ := eng.RecordUserFile(session.SessionID, "o.png", "image/png", model.FileSourceUploaded, []byte("ORIG"))
	res, _ := eng.executeTool(context.Background(), session, "m1", editImageToolCall(src.FileID))

	if editorCalled {
		t.Error("editor must NOT run when BeforeAction blocks the action")
	}
	if !strings.Contains(res, "insufficient credit") {
		t.Errorf("expected the block message, got %q", res)
	}
}

// newUserFileTestEngine builds an Engine backed by an in-memory SQLite store and
// a local file store rooted in a temp dir, plus a persisted session.
func newUserFileTestEngine(t *testing.T) (*Engine, *model.Session) {
	t.Helper()

	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	fs, err := filestore.NewLocalFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("failed to create file store: %v", err)
	}

	eng := &Engine{
		Sessions:  sqliteStore,
		Functions: model.NewFunctionRegistry(),
		Files:     fs,
	}
	eng.Executor = func(name string, args map[string]interface{}) (string, error) {
		return eng.Functions.Execute(name, args)
	}
	eng.RegisterManageFilesTool()

	session := model.NewSessionWithID("user-1", "user-1-high-s0001", model.AgentTypeHigh)
	if err := sqliteStore.Put(session); err != nil {
		t.Fatalf("failed to persist session: %v", err)
	}
	return eng, session
}

func TestRecordAndReadUserFile(t *testing.T) {
	eng, session := newUserFileTestEngine(t)

	uf, err := eng.RecordUserFile(session.SessionID, "notes.txt", "", model.FileSourceUploaded, []byte("hello world"))
	if err != nil {
		t.Fatalf("RecordUserFile failed: %v", err)
	}
	if uf.Size != 11 {
		t.Errorf("expected size 11, got %d", uf.Size)
	}
	if uf.Source != model.FileSourceUploaded {
		t.Errorf("expected source uploaded, got %s", uf.Source)
	}
	if !strings.HasPrefix(uf.MIMEType, "text/plain") {
		t.Errorf("expected text/plain MIME, got %q", uf.MIMEType)
	}
	if !strings.Contains(uf.StorageKey, "user-1/") {
		t.Errorf("expected storage key namespaced by user, got %q", uf.StorageKey)
	}

	// Bytes must be present on disk.
	size, exists, err := eng.Files.Stat(uf.StorageKey)
	if err != nil || !exists || size != 11 {
		t.Errorf("expected stored bytes (size=11, exists), got size=%d exists=%v err=%v", size, exists, err)
	}

	// Listing returns the file.
	files, err := eng.ListUserFiles("user-1")
	if err != nil {
		t.Fatalf("ListUserFiles failed: %v", err)
	}
	if len(files) != 1 || files[0].FileID != uf.FileID {
		t.Fatalf("expected 1 file matching, got %+v", files)
	}

	// Reading returns the original bytes.
	data, meta, err := eng.ReadUserFile(uf.FileID)
	if err != nil {
		t.Fatalf("ReadUserFile failed: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("expected 'hello world', got %q", string(data))
	}
	if meta.UserID != "user-1" {
		t.Errorf("expected owner user-1, got %s", meta.UserID)
	}
}

func TestManageFilesTool_SaveListRead(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	base := map[string]interface{}{"__user_id__": "user-1", "__session_id__": session.SessionID}

	// save
	saveArgs := cloneArgs(base, map[string]interface{}{"action": "save", "name": "report.md", "content": "# Report\nbody"})
	res, err := eng.Functions.Execute("manage_files", saveArgs)
	if err != nil {
		t.Fatalf("save action failed: %v", err)
	}
	if !strings.Contains(res, "Saved file") {
		t.Fatalf("unexpected save result: %q", res)
	}

	// list shows the saved (generated) file
	listRes, err := eng.Functions.Execute("manage_files", cloneArgs(base, map[string]interface{}{"action": "list"}))
	if err != nil {
		t.Fatalf("list action failed: %v", err)
	}
	if !strings.Contains(listRes, "report.md") || !strings.Contains(listRes, "generated") {
		t.Fatalf("list did not contain expected file: %q", listRes)
	}

	// read it back via file_id parsed from the listing
	fileID := extractFileID(t, listRes)
	readRes, err := eng.Functions.Execute("manage_files", cloneArgs(base, map[string]interface{}{"action": "read", "file_id": fileID}))
	if err != nil {
		t.Fatalf("read action failed: %v", err)
	}
	if !strings.Contains(readRes, "# Report") {
		t.Fatalf("read did not return content: %q", readRes)
	}
}

func TestManageFilesTool_ReadEnforcesOwnership(t *testing.T) {
	eng, session := newUserFileTestEngine(t)

	uf, err := eng.RecordUserFile(session.SessionID, "secret.txt", "", model.FileSourceUploaded, []byte("classified"))
	if err != nil {
		t.Fatalf("RecordUserFile failed: %v", err)
	}

	// A different user attempts to read user-1's file.
	res, err := eng.Functions.Execute("manage_files", map[string]interface{}{
		"__user_id__":    "intruder",
		"__session_id__": session.SessionID,
		"action":         "read",
		"file_id":        uf.FileID,
	})
	if err != nil {
		t.Fatalf("read action errored: %v", err)
	}
	if !strings.Contains(res, "does not belong to you") {
		t.Fatalf("expected ownership refusal, got %q", res)
	}
	if strings.Contains(res, "classified") {
		t.Fatalf("ownership check leaked content: %q", res)
	}
}

func TestManageFilesTool_ReadImageInjects(t *testing.T) {
	eng, session := newUserFileTestEngine(t)

	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG signature
	uf, err := eng.RecordUserFile(session.SessionID, "pic.png", "image/png", model.FileSourceUploaded, png)
	if err != nil {
		t.Fatalf("RecordUserFile failed: %v", err)
	}

	args := map[string]interface{}{
		"__user_id__":    "user-1",
		"__session_id__": session.SessionID,
		"action":         "read",
		"file_id":        uf.FileID,
	}
	res, err := eng.Functions.Execute("manage_files", args)
	if err != nil {
		t.Fatalf("read image failed: %v", err)
	}
	if !strings.Contains(res, "Loaded image") || !strings.Contains(res, "edit_image") {
		t.Fatalf("unexpected read-image result: %q", res)
	}
	// The tool must have stashed an image to inject back via the shared args map.
	inj, ok := args[injectImageArgKey].(*injectedImage)
	if !ok || inj == nil {
		t.Fatalf("expected injected image in args, got %v", args[injectImageArgKey])
	}
	if !strings.HasPrefix(inj.DataURL, "data:image/png;base64,") {
		t.Errorf("unexpected data URL prefix: %q", inj.DataURL[:min(40, len(inj.DataURL))])
	}
	// And it must render as a multimodal message with an image part.
	msg := inj.message()
	if len(msg.MultiContent) != 2 || msg.MultiContent[1].Type != "image_url" {
		t.Errorf("expected multimodal image message, got %+v", msg.MultiContent)
	}
}

func TestManageFilesTool_GrepAndEdit(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	base := map[string]interface{}{"__user_id__": "user-1", "__session_id__": session.SessionID}

	uf, err := eng.RecordUserFile(session.SessionID, "config.txt", "text/plain",
		model.FileSourceUploaded, []byte("host=localhost\nport=8080\ndebug=false\n"))
	if err != nil {
		t.Fatalf("RecordUserFile failed: %v", err)
	}

	// grep finds the matching line with a line number.
	grepRes, err := eng.Functions.Execute("manage_files", cloneArgs(base, map[string]interface{}{
		"action": "grep", "query": "port=",
	}))
	if err != nil {
		t.Fatalf("grep failed: %v", err)
	}
	if !strings.Contains(grepRes, uf.FileID+":2:") || !strings.Contains(grepRes, "port=8080") {
		t.Fatalf("unexpected grep result: %q", grepRes)
	}

	// edit replaces a value in place.
	editRes, err := eng.Functions.Execute("manage_files", cloneArgs(base, map[string]interface{}{
		"action": "edit", "file_id": uf.FileID, "old_string": "debug=false", "new_string": "debug=true",
	}))
	if err != nil {
		t.Fatalf("edit failed: %v", err)
	}
	if !strings.Contains(editRes, "Edited") {
		t.Fatalf("unexpected edit result: %q", editRes)
	}

	data, _, err := eng.ReadUserFile(uf.FileID)
	if err != nil {
		t.Fatalf("ReadUserFile failed: %v", err)
	}
	if !strings.Contains(string(data), "debug=true") || strings.Contains(string(data), "debug=false") {
		t.Fatalf("edit not applied: %q", string(data))
	}
}

func TestManageFilesTool_DeleteEnforcesOwnershipAndRemovesBytes(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	file, err := eng.RecordUserFile(
		session.SessionID,
		"delete-me.txt",
		"text/plain",
		model.FileSourceGenerated,
		[]byte("temporary"),
	)
	if err != nil {
		t.Fatal(err)
	}

	intruderResult, err := eng.Functions.Execute("manage_files", map[string]interface{}{
		"__user_id__": "intruder",
		"action":      "delete",
		"file_id":     file.FileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(intruderResult, "does not belong to you") {
		t.Fatalf("expected owner refusal, got %q", intruderResult)
	}

	result, err := eng.Functions.Execute("manage_files", map[string]interface{}{
		"__user_id__": "user-1",
		"action":      "delete",
		"file_id":     file.FileID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Deleted file") {
		t.Fatalf("unexpected delete result: %q", result)
	}
	if _, exists, statErr := eng.Files.Stat(file.StorageKey); statErr != nil || exists {
		t.Fatalf("file bytes still exist: exists=%v err=%v", exists, statErr)
	}
	meta, err := eng.Sessions.GetUserFile(file.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if meta != nil {
		t.Fatalf("file metadata still exists: %+v", meta)
	}
}

func TestManageFilesTool_EditImageIndependent(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	// Fake editor: returns a distinct payload + usage so we can verify
	// independence and that the model/token usage flows through.
	eng.SetImageEditor(func(image []byte, mimeType, instruction string) (*model.ImageEditResult, error) {
		return &model.ImageEditResult{
			Data: []byte("EDITED-" + instruction), MIMEType: "image/png",
			Model: "fake-image-model", InputTokens: 7, OutputTokens: 11,
		}, nil
	})

	src, err := eng.RecordUserFile(session.SessionID, "orig.png", "image/png",
		model.FileSourceUploaded, []byte("ORIGINAL"))
	if err != nil {
		t.Fatalf("RecordUserFile failed: %v", err)
	}

	res, err := eng.Functions.Execute("manage_files", map[string]interface{}{
		"__user_id__":    "user-1",
		"__session_id__": session.SessionID,
		"action":         "edit_image",
		"file_id":        src.FileID,
		"instruction":    "add-hat",
	})
	if err != nil {
		t.Fatalf("edit_image failed: %v", err)
	}
	if !strings.Contains(res, "NEW file") {
		t.Fatalf("unexpected edit_image result: %q", res)
	}

	files, err := eng.ListUserFiles("user-1")
	if err != nil {
		t.Fatalf("ListUserFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 independent files, got %d", len(files))
	}

	// Original bytes unchanged.
	origData, _, _ := eng.ReadUserFile(src.FileID)
	if string(origData) != "ORIGINAL" {
		t.Errorf("original image was modified: %q", string(origData))
	}

	// Find the edited file and verify provenance + content.
	var edited *model.UserFile
	for _, f := range files {
		if f.FileID != src.FileID {
			edited = f
		}
	}
	if edited == nil {
		t.Fatal("edited file not found")
	}
	if edited.ParentFileID != src.FileID {
		t.Errorf("expected ParentFileID=%s, got %s", src.FileID, edited.ParentFileID)
	}
	if edited.Source != model.FileSourceGenerated {
		t.Errorf("expected generated source, got %s", edited.Source)
	}
	editedData, _, _ := eng.ReadUserFile(edited.FileID)
	if string(editedData) != "EDITED-add-hat" {
		t.Errorf("unexpected edited content: %q", string(editedData))
	}
}

func TestManageFilesTool_EditImageNotConfigured(t *testing.T) {
	eng, session := newUserFileTestEngine(t)
	src, _ := eng.RecordUserFile(session.SessionID, "orig.png", "image/png",
		model.FileSourceUploaded, []byte("ORIGINAL"))

	res, err := eng.Functions.Execute("manage_files", map[string]interface{}{
		"__user_id__":    "user-1",
		"__session_id__": session.SessionID,
		"action":         "edit_image",
		"file_id":        src.FileID,
		"instruction":    "x",
	})
	if err != nil {
		t.Fatalf("edit_image errored: %v", err)
	}
	if !strings.Contains(res, "not configured") {
		t.Fatalf("expected not-configured message, got %q", res)
	}
}

// cloneArgs merges extra keys into a copy of base.
func cloneArgs(base, extra map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// extractFileID pulls the first "id=..." token out of a list result line.
func extractFileID(t *testing.T, listing string) string {
	t.Helper()
	idx := strings.Index(listing, "id=")
	if idx < 0 {
		t.Fatalf("no id= token in listing: %q", listing)
	}
	rest := listing[idx+3:]
	if end := strings.IndexAny(rest, " \n"); end >= 0 {
		return rest[:end]
	}
	return rest
}
