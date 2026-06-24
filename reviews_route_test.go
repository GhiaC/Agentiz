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

	// Raise a review through the host API.
	req := model.NewReviewRequest("plan_step", "plan-1/step-2")
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

	// POST the approve form — the dashboard's resolution path.
	post := httptest.NewRequest(http.MethodPost,
		"/agentize/debug/reviews/"+reviewID+"/resolve",
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
