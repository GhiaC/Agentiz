package agentize

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/debuger/pages"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/gin-gonic/gin"
)

// TestUserFileRecordingAndDashboard covers the end-to-end path that was broken:
// a user file must be persisted, listed, shown on the Documents debug page,
// downloadable via the raw endpoint, and reflected in the System Info panel.
func TestUserFileRecordingAndDashboard(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	defer os.RemoveAll(knowledge)

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	dbStore, err := store.NewDBStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("failed to create db store: %v", err)
	}

	fileDir := t.TempDir()
	ag, err := NewWithOptions(knowledge, &Options{SessionStore: dbStore, FileStoreDir: fileDir})
	if err != nil {
		t.Fatalf("failed to create agentize: %v", err)
	}

	sess, err := ag.CreateSession("user-1")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Record an uploaded file — the same primitive ProcessMessageWithFiles and a
	// bot integration use when a user sends an attachment.
	uf, err := ag.RecordUserFile(sess.SessionID, "notes.txt", "text/plain", model.FileSourceUploaded, []byte("hello world"))
	if err != nil {
		t.Fatalf("RecordUserFile failed: %v", err)
	}
	if uf.FileID == "" {
		t.Fatal("expected non-empty file id")
	}

	// It must be retrievable and listed for the user.
	files, err := ag.ListUserFiles("user-1")
	if err != nil {
		t.Fatalf("ListUserFiles failed: %v", err)
	}
	if len(files) != 1 || files[0].FileID != uf.FileID {
		t.Fatalf("expected 1 listed file matching %s, got %+v", uf.FileID, files)
	}

	// SystemInfo must reflect the backends and the document count.
	info := ag.SystemInfo()
	if info.Database.Type != "SQLite" {
		t.Errorf("expected Database.Type SQLite, got %q", info.Database.Type)
	}
	if info.Database.Location != dbPath {
		t.Errorf("expected Database.Location %q, got %q", dbPath, info.Database.Location)
	}
	if info.FileStore.Type != "Local Disk" {
		t.Errorf("expected FileStore.Type 'Local Disk', got %q", info.FileStore.Type)
	}
	if info.FileStore.Location != fileDir {
		t.Errorf("expected FileStore.Location %q, got %q", fileDir, info.FileStore.Location)
	}
	if info.TotalDocuments != 1 {
		t.Errorf("expected TotalDocuments 1, got %d", info.TotalDocuments)
	}

	// The Documents debug page must render the file (not "No documents found").
	handler, err := debuger.NewDebugHandlerWithConfig(ag.GetSessionStore(), nil)
	if err != nil {
		t.Fatalf("failed to create debug handler: %v", err)
	}
	html, err := pages.RenderDocuments(handler, 1)
	if err != nil {
		t.Fatalf("RenderDocuments failed: %v", err)
	}
	if !strings.Contains(html, "notes.txt") {
		t.Error("documents page should list the uploaded file name")
	}
	if strings.Contains(html, "No documents found") {
		t.Error("documents page should not be empty")
	}

	// The dashboard must render the System Info panel reporting the DB backend.
	dashHTML, err := pages.RenderDashboard(handler, &info)
	if err != nil {
		t.Fatalf("RenderDashboard failed: %v", err)
	}
	for _, want := range []string{"System Info", "SQLite", "Local Disk", "Documents"} {
		if !strings.Contains(dashHTML, want) {
			t.Errorf("dashboard should contain %q", want)
		}
	}

	// The raw file-serve endpoint must return the stored bytes.
	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "1") // no creds in test: register dashboard in dev mode
	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agentize/debug/documents/"+uf.FileID+"/raw", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw endpoint status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "hello world" {
		t.Errorf("raw endpoint body = %q, want %q", got, "hello world")
	}
}

// TestAppendUploadedFilesNote verifies the note appended to a message references
// the saved files so the agent can read them on demand.
func TestAppendUploadedFilesNote(t *testing.T) {
	if got := appendUploadedFilesNote("hi", nil); got != "hi" {
		t.Errorf("expected unchanged message with no files, got %q", got)
	}

	files := []*model.UserFile{{FileID: "s-uf0001", Name: "a.png", MIMEType: "image/png", Size: 12}}
	got := appendUploadedFilesNote("look at this", files)
	if !strings.Contains(got, "s-uf0001") || !strings.Contains(got, "a.png") {
		t.Errorf("note should reference file id and name, got %q", got)
	}
	if !strings.Contains(got, "manage_files") {
		t.Errorf("note should tell the agent to use manage_files, got %q", got)
	}
	if !strings.Contains(got, "look at this") {
		t.Errorf("note should preserve the original message, got %q", got)
	}
}

func TestGeneratedUserFilesSinceReturnsOnlyNewGeneratedFiles(t *testing.T) {
	before := []*model.UserFile{
		{FileID: "old-generated", Source: model.FileSourceGenerated},
		{FileID: "old-upload", Source: model.FileSourceUploaded},
	}
	after := []*model.UserFile{
		{FileID: "new-upload", Source: model.FileSourceUploaded},
		{FileID: "new-screenshot", Source: model.FileSourceGenerated, MIMEType: "image/png"},
		{FileID: "old-generated", Source: model.FileSourceGenerated},
	}
	got := generatedUserFilesSince(before, after)
	if len(got) != 1 || got[0].FileID != "new-screenshot" {
		t.Fatalf("unexpected generated files: %+v", got)
	}
}
