package agentize

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
	"github.com/gin-gonic/gin"
)

// TestUserDetailPage_CoreSystemPromptCard exercises the full route: with no live
// Core wired, Agentize installs the store-only preview provider, and the user
// detail page renders the new "Core System Prompt" card (collapsed, badged
// PREVIEW) alongside the now-collapsible secondary cards.
func TestUserDetailPage_CoreSystemPromptCard(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	defer os.RemoveAll(knowledge)

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	dbStore, err := store.NewDBStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("failed to create db store: %v", err)
	}

	ag, err := NewWithOptions(knowledge, &Options{SessionStore: dbStore, FileStoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("failed to create agentize: %v", err)
	}

	const userID = "user-1"
	if _, err := dbStore.GetOrCreateUser(userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Seed a Core session so the preview renders the Core Session Context section.
	if err := dbStore.Put(&model.Session{
		SessionID: "core-s1",
		UserID:    userID,
		AgentType: model.AgentTypeCore,
		Summary:   "remembers the user prefers Go",
		Tags:      []string{"golang"},
	}); err != nil {
		t.Fatalf("seed core session: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agentize/debug/users/"+userID, nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("user detail status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{
		"Core System Prompt",            // the new card
		"PREVIEW",                       // no live Core wired → store preview
		"collapsible-card",              // the secondary cards are collapsible
		"collapsible-section",           // each prompt section is its own collapsible
		"Core Controller",               // a real prompt section rendered
		"Required",                      // section classification badges
		"Static",                        //   "
		"remembers the user prefers Go", // the seeded Core summary surfaced
		"Sessions",                      // existing secondary cards still present
		"<details",                      // native collapsible markup
	} {
		if !strings.Contains(body, want) {
			t.Errorf("user detail page should contain %q", want)
		}
	}

	// Every collapsible card must be collapsed by default (no open attribute).
	if strings.Contains(body, `collapsible-card mb-4" open`) {
		t.Error("collapsible cards must be collapsed by default")
	}
}
