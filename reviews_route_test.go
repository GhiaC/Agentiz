package agentize

import (
	"context"
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

// TestDebugReviews_ListAndResolve exercises the full dashboard path: a pending
// review is listed at /agentize/debug/reviews, and POSTing the approve form to
// /agentize/debug/reviews/:id/resolve drives the SAME Manager.Resolve every UI
// uses (a Telegram button handler would call the identical ag.ResolveReview),
// resolving the review as approved.
func TestDebugReviews_ListAndResolve(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	defer os.RemoveAll(knowledge)

	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	dbStore, err := store.NewDBStoreWithPath(dbPath)
	if err != nil {
		t.Fatalf("create db store: %v", err)
	}
	ag, err := NewWithOptions(knowledge, &Options{SessionStore: dbStore, FileStoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create agentize: %v", err)
	}
	// No admin credentials in this test: opt into the unauthenticated dev mode so
	// the dashboard routes register (safe-by-default otherwise skips them).
	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "1")

	// Raise a review through the host API.
	req := model.NewReviewRequest("tool_call", "session-1-t2")
	req.UserID = "user-1"
	req.Title = "Approve deploy"
	reviewID, err := ag.RequestReview(context.Background(), req)
	if err != nil {
		t.Fatalf("RequestReview: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	// The pending review appears on the list page with an approve form.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agentize/debug/reviews", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reviews list status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, reviewID) || !strings.Contains(body, "Approve") {
		t.Fatalf("reviews page missing review %s or approve form", reviewID)
	}

	// POST the approve form — the dashboard's resolution path. The ?confirm=<id>
	// typed-confirmation guard must be supplied (CSRF defense-in-depth).
	post := httptest.NewRequest(http.MethodPost,
		"/agentize/debug/reviews/"+reviewID+"/resolve?confirm="+reviewID,
		strings.NewReader("decision=approve&note=ship"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, post)
	if rec2.Code != http.StatusFound { // 302 redirect back to the list
		t.Fatalf("resolve status = %d, want 302; body=%s", rec2.Code, rec2.Body.String())
	}

	// The review is now resolved as approved, durably.
	got, err := ag.GetReviewStore().GetReviewRequest(reviewID)
	if err != nil || got == nil {
		t.Fatalf("GetReviewRequest: %v, %v", got, err)
	}
	if !got.IsApproved() || got.Decision != "approve" || got.Note != "ship" {
		t.Errorf("review not approved via dashboard form: %+v", got)
	}
	// And it drops out of the pending list.
	if pend, _ := ag.ListPendingReviews(context.Background(), ""); len(pend) != 0 {
		t.Errorf("resolved review should not remain pending, got %d", len(pend))
	}
}

// TestDebugReviews_ResolveGuards verifies the CSRF/typed-confirmation and
// decision-whitelist guards on the resolve POST: a missing ?confirm or a
// non-approve/reject decision is rejected and leaves the review pending.
func TestDebugReviews_ResolveGuards(t *testing.T) {
	knowledge := createTestKnowledgeTree(t)
	defer os.RemoveAll(knowledge)
	dbStore, err := store.NewDBStoreWithPath(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	ag, err := NewWithOptions(knowledge, &Options{SessionStore: dbStore, FileStoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("agentize: %v", err)
	}
	t.Setenv("AGENTIZE_DEBUG_UNSAFE", "1")

	req := model.NewReviewRequest("tool_call", "session-1-t2")
	req.UserID = "u1"
	id, _ := ag.RequestReview(context.Background(), req)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	ag.RegisterRoutes(router)

	post := func(url, body string) int {
		r := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}

	// Missing ?confirm → 400 (CSRF guard).
	if code := post("/agentize/debug/reviews/"+id+"/resolve", "decision=approve"); code != 400 {
		t.Errorf("resolve without confirm should be 400, got %d", code)
	}
	// Bad decision (with confirm) → 400 (whitelist).
	if code := post("/agentize/debug/reviews/"+id+"/resolve?confirm="+id, "decision=delete-everything"); code != 400 {
		t.Errorf("bad decision should be 400, got %d", code)
	}
	// The review must still be pending after both rejected attempts.
	if got, _ := ag.GetReviewStore().GetReviewRequest(id); got == nil || got.IsResolved() {
		t.Errorf("review must stay pending after rejected resolves, got %+v", got)
	}
}
