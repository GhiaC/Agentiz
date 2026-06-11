package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/ghiac/agentize/debuger"
	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

// TestRenderDocuments verifies the Documents page renders both the empty state
// and a populated table without error.
func TestRenderDocuments(t *testing.T) {
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	handler, err := debuger.NewDebugHandler(sqliteStore)
	if err != nil {
		t.Fatalf("failed to create debug handler: %v", err)
	}

	// Empty state.
	html, err := RenderDocuments(handler, 1)
	if err != nil {
		t.Fatalf("RenderDocuments (empty) failed: %v", err)
	}
	if !strings.Contains(html, "No documents found") {
		t.Errorf("expected empty-state message, got:\n%s", html)
	}

	// Populated state.
	uf := &model.UserFile{
		FileID:     "user-1-high-s0001-uf0001",
		UserID:     "user-1",
		SessionID:  "user-1-high-s0001",
		Name:       "report.md",
		MIMEType:   "text/markdown",
		Size:       2048,
		StorageKey: "user-1/report.md",
		Source:     model.FileSourceGenerated,
		CreatedAt:  time.Unix(1_700_000_000, 0),
	}
	if err := sqliteStore.PutUserFile(uf); err != nil {
		t.Fatalf("PutUserFile failed: %v", err)
	}

	html, err = RenderDocuments(handler, 1)
	if err != nil {
		t.Fatalf("RenderDocuments (populated) failed: %v", err)
	}
	for _, want := range []string{"report.md", "Generated", "2.0 KB", "text/markdown"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page missing %q\n%s", want, html)
		}
	}
}
