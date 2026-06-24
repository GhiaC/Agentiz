package store

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
)

// Conformance cases for the Chapter 03 storage improvements: boundary
// validation, message pagination, batch inserts, quotas, and maintenance
// operations. Registered in TestStoreConformance so every backend stays
// observationally identical.

func putNMessages(t *testing.T, st Store, s *model.Session, n int) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i := 1; i <= n; i++ {
		m := model.NewUserMessage(fmt.Sprintf("%s-m%04d", s.SessionID, i), i, s.UserID, s.SessionID, fmt.Sprintf("msg-%d", i), model.ContentTypeText)
		m.CreatedAt = base.Add(time.Duration(i) * time.Second)
		if err := st.PutMessage(m); err != nil {
			t.Fatalf("PutMessage %d: %v", i, err)
		}
	}
}

func testValidation(t *testing.T, st Store) {
	// Sessions: empty required IDs are rejected with ErrValidation.
	s := newSession("user-1", model.AgentTypeLow)
	s.AgentType = ""
	if err := st.Put(s); !errors.Is(err, ErrValidation) {
		t.Errorf("Put session with empty AgentType: got %v, want ErrValidation", err)
	}
	if err := st.Put(nil); !errors.Is(err, ErrValidation) {
		t.Errorf("Put nil session: got %v, want ErrValidation", err)
	}

	// Messages: empty SessionID rejected.
	m := model.NewUserMessage("m-1", 1, "user-1", "", "hi", model.ContentTypeText)
	if err := st.PutMessage(m); !errors.Is(err, ErrValidation) {
		t.Errorf("PutMessage with empty SessionID: got %v, want ErrValidation", err)
	}

	// Tool calls: invalid status enum rejected.
	tc := &model.ToolCall{ToolCallID: "tc-1", SessionID: "s-1", UserID: "user-1", FunctionName: "f", Arguments: "{}", Status: "bogus", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := st.PutToolCall(tc); !errors.Is(err, ErrValidation) {
		t.Errorf("PutToolCall with invalid status: got %v, want ErrValidation", err)
	}

	// User files: empty StorageKey rejected.
	uf := &model.UserFile{FileID: "uf-1", UserID: "user-1", SessionID: "s-1", Name: "f", CreatedAt: time.Now()}
	if err := st.PutUserFile(uf); !errors.Is(err, ErrValidation) {
		t.Errorf("PutUserFile with empty StorageKey: got %v, want ErrValidation", err)
	}
}

func testMessagePagination(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)
	putNMessages(t, st, s, 5)

	// Page 1: the 2 newest.
	page1, err := st.GetMessagesBySessionPage(s.SessionID, 2, 0)
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1) != 2 || page1[0].Content != "msg-5" || page1[1].Content != "msg-4" {
		t.Fatalf("page 1 mismatch: %+v", contents(page1))
	}
	// Page 2.
	page2, err := st.GetMessagesBySessionPage(s.SessionID, 2, 2)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2) != 2 || page2[0].Content != "msg-3" || page2[1].Content != "msg-2" {
		t.Fatalf("page 2 mismatch: %+v", contents(page2))
	}
	// Final partial page.
	page3, err := st.GetMessagesBySessionPage(s.SessionID, 2, 4)
	if err != nil {
		t.Fatalf("page 3: %v", err)
	}
	if len(page3) != 1 || page3[0].Content != "msg-1" {
		t.Fatalf("page 3 mismatch: %+v", contents(page3))
	}
	// Defaults: limit <= 0 returns a full default page; offset < 0 → 0.
	all, err := st.GetMessagesBySessionPage(s.SessionID, 0, -1)
	if err != nil {
		t.Fatalf("default page: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("default page len = %d, want 5", len(all))
	}
	// Parity with the unbounded variant.
	unbounded, err := st.GetMessagesBySession(s.SessionID)
	if err != nil {
		t.Fatalf("GetMessagesBySession: %v", err)
	}
	if len(unbounded) != 5 || unbounded[0].Content != "msg-5" || unbounded[4].Content != "msg-1" {
		t.Fatalf("unbounded mismatch: %+v", contents(unbounded))
	}
}

func contents(msgs []*model.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func testPutMessagesBatch(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)

	base := time.Now().Add(-time.Hour)
	var batch []*model.Message
	for i := 1; i <= 3; i++ {
		m := model.NewUserMessage(fmt.Sprintf("%s-b%04d", s.SessionID, i), i, "user-1", s.SessionID, fmt.Sprintf("batch-%d", i), model.ContentTypeText)
		m.CreatedAt = base.Add(time.Duration(i) * time.Second)
		batch = append(batch, m)
	}
	if err := st.PutMessages(batch); err != nil {
		t.Fatalf("PutMessages: %v", err)
	}
	msgs, err := st.GetMessagesBySession(s.SessionID)
	if err != nil {
		t.Fatalf("GetMessagesBySession: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages after batch, want 3", len(msgs))
	}

	// A batch containing an invalid message writes nothing.
	bad := model.NewUserMessage("", 9, "user-1", s.SessionID, "bad", model.ContentTypeText)
	if err := st.PutMessages([]*model.Message{bad}); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid batch: got %v, want ErrValidation", err)
	}
	if msgs, _ := st.GetMessagesBySession(s.SessionID); len(msgs) != 3 {
		t.Errorf("invalid batch must write nothing; got %d messages", len(msgs))
	}

	// Empty batch is a no-op.
	if err := st.PutMessages(nil); err != nil {
		t.Errorf("empty batch: %v", err)
	}
}

// quotaSetter is implemented by every shipped backend.
type quotaSetter interface{ SetQuotas(Quotas) }

func testQuotas(t *testing.T, st Store) {
	qs, ok := st.(quotaSetter)
	if !ok {
		t.Fatalf("backend %T does not support quotas", st)
	}
	qs.SetQuotas(Quotas{MaxMessagesPerSession: 2, MaxUserFilesPerUser: 1, MaxFileBytesPerUser: 100})

	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)

	// Message quota: third message in the session is rejected.
	putNMessages(t, st, s, 2)
	m3 := model.NewUserMessage(s.SessionID+"-m9999", 3, "user-1", s.SessionID, "over", model.ContentTypeText)
	if err := st.PutMessage(m3); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("PutMessage over quota: got %v, want ErrQuotaExceeded", err)
	}
	// Re-upserting an existing message id still counts against the limit
	// conservatively, but updating files with the same ID does not (below).

	// File count quota.
	uf1 := &model.UserFile{FileID: "uf-1", UserID: "user-1", SessionID: s.SessionID, Name: "a", Size: 10, StorageKey: "user-1/uf-1-a", CreatedAt: time.Now()}
	if err := st.PutUserFile(uf1); err != nil {
		t.Fatalf("PutUserFile: %v", err)
	}
	uf2 := &model.UserFile{FileID: "uf-2", UserID: "user-1", SessionID: s.SessionID, Name: "b", Size: 10, StorageKey: "user-1/uf-2-b", CreatedAt: time.Now()}
	if err := st.PutUserFile(uf2); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("PutUserFile over count quota: got %v, want ErrQuotaExceeded", err)
	}
	// Updating the SAME file is allowed (not a new file).
	uf1.Summary = "updated"
	if err := st.PutUserFile(uf1); err != nil {
		t.Errorf("PutUserFile update of existing file rejected: %v", err)
	}

	// Byte quota.
	qs.SetQuotas(Quotas{MaxFileBytesPerUser: 100})
	big := &model.UserFile{FileID: "uf-3", UserID: "user-1", SessionID: s.SessionID, Name: "c", Size: 1000, StorageKey: "user-1/uf-3-c", CreatedAt: time.Now()}
	if err := st.PutUserFile(big); !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("PutUserFile over byte quota: got %v, want ErrQuotaExceeded", err)
	}

	// Zero quotas = unlimited.
	qs.SetQuotas(Quotas{})
	if err := st.PutMessage(m3); err != nil {
		t.Errorf("PutMessage with quotas disabled: %v", err)
	}
}

func testMaintainer(t *testing.T, st Store) {
	m, ok := st.(Maintainer)
	if !ok {
		t.Fatalf("backend %T does not implement Maintainer", st)
	}

	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)
	putNMessages(t, st, s, 2)

	// A healthy store has no integrity issues.
	issues, err := m.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("expected no issues on healthy store, got %+v", issues)
	}

	// Backup either streams bytes (SQLite) or reports it is unsupported (MongoDB).
	var buf bytes.Buffer
	err = m.Backup(&buf)
	switch {
	case err == nil:
		if buf.Len() == 0 {
			t.Error("Backup succeeded but wrote no bytes")
		}
	case errors.Is(err, ErrBackupUnsupported):
		// Documented MongoDB behavior: use mongodump.
	default:
		t.Errorf("Backup: unexpected error %v", err)
	}
}

// testVerifyDetectsOrphans deletes a session row (Delete does not cascade) and
// asserts Verify reports each session-scoped child it orphaned — including
// summarization logs, which the orphan scan previously omitted on both backends.
func testVerifyDetectsOrphans(t *testing.T, st Store) {
	m, ok := st.(Maintainer)
	if !ok {
		t.Fatalf("backend %T does not implement Maintainer", st)
	}
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)
	putNMessages(t, st, s, 1)
	if err := st.PutToolCall(newToolCall(s, s.SessionID+"-t0001", "call_x", "do")); err != nil {
		t.Fatalf("PutToolCall: %v", err)
	}
	if err := st.PutSummarizationLog(model.NewSummarizationLog(s)); err != nil {
		t.Fatalf("PutSummarizationLog: %v", err)
	}

	if err := st.Delete(s.SessionID); err != nil {
		t.Fatalf("Delete session: %v", err)
	}

	issues, err := m.Verify()
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	found := map[string]bool{}
	for _, is := range issues {
		found[is.Type] = true
	}
	for _, want := range []string{"orphaned_message", "orphaned_tool_call", "orphaned_summarization_log"} {
		if !found[want] {
			t.Errorf("Verify did not detect %s (issues: %+v)", want, issues)
		}
	}
}

// testReviews exercises the ReviewStore contract (chapter 10): round-trip,
// pending-list by user and across all users, resolve dropping the review from
// pending, and DeleteUserData removing a user's reviews — identically on both
// backends.
func testReviews(t *testing.T, st Store) {
	// Absent -> (nil, nil), never an error.
	if got, err := st.GetReviewRequest("nope"); err != nil || got != nil {
		t.Fatalf("GetReviewRequest(absent) = %v, %v; want nil, nil", got, err)
	}

	r := model.NewReviewRequest("plan_step", "plan-1/step-2")
	r.UserID = "user-1"
	r.SessionID = "sess-1"
	r.Title = "Approve deploy"
	r.Content = "ship it?"
	if err := st.PutReviewRequest(r); err != nil {
		t.Fatalf("PutReviewRequest: %v", err)
	}

	got, err := st.GetReviewRequest(r.ID)
	if err != nil || got == nil {
		t.Fatalf("GetReviewRequest: %v, %v", got, err)
	}
	if got.Kind != "plan_step" || got.RefID != "plan-1/step-2" || got.Status != model.ReviewPending || got.Title != "Approve deploy" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Pending list, by user and across all users.
	for _, uid := range []string{"user-1", ""} {
		pend, err := st.ListPendingReviews(uid)
		if err != nil {
			t.Fatalf("ListPendingReviews(%q): %v", uid, err)
		}
		if len(pend) != 1 || pend[0].ID != r.ID {
			t.Fatalf("ListPendingReviews(%q) = %+v, want [%s]", uid, contentsRev(pend), r.ID)
		}
	}

	// A second user's review is isolated per-user but visible in the all list.
	r2 := model.NewReviewRequest("payment", "order-9")
	r2.UserID = "user-2"
	if err := st.PutReviewRequest(r2); err != nil {
		t.Fatalf("PutReviewRequest r2: %v", err)
	}
	if pend, _ := st.ListPendingReviews("user-1"); len(pend) != 1 {
		t.Errorf("user-1 pending should stay 1, got %d", len(pend))
	}
	if pend, _ := st.ListPendingReviews(""); len(pend) != 2 {
		t.Errorf("all-users pending should be 2, got %d", len(pend))
	}

	// Resolving (an upsert) drops the review from the pending list.
	got.Status = model.ReviewApproved
	got.Decision = "approve"
	got.DecidedBy = "admin"
	got.DecidedAt = time.Now()
	if err := st.PutReviewRequest(got); err != nil {
		t.Fatalf("PutReviewRequest (resolve): %v", err)
	}
	if pend, _ := st.ListPendingReviews("user-1"); len(pend) != 0 {
		t.Errorf("resolved review should drop from pending, got %d", len(pend))
	}
	if reloaded, _ := st.GetReviewRequest(r.ID); reloaded == nil ||
		reloaded.Status != model.ReviewApproved || reloaded.Decision != "approve" || reloaded.DecidedBy != "admin" {
		t.Errorf("resolved review not persisted: %+v", reloaded)
	}

	// DeleteUserData removes the user's reviews but leaves other users'.
	if err := st.DeleteUserData("user-1"); err != nil {
		t.Fatalf("DeleteUserData: %v", err)
	}
	if got, _ := st.GetReviewRequest(r.ID); got != nil {
		t.Errorf("review should be gone after DeleteUserData, got %+v", got)
	}
	if pend, _ := st.ListPendingReviews(""); len(pend) != 1 || pend[0].ID != r2.ID {
		t.Errorf("only user-2's pending review should remain, got %+v", contentsRev(pend))
	}
}

func contentsRev(rs []*model.ReviewRequest) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

// testMessageSeqRestore guards the MessageSeq-restoration path (SQLite MAX,
// Mongo sort+limit FindOne — S16): on reload, a session's MessageSeq must be
// raised to the highest stored seq_id so a restart never reuses a message ID.
func testMessageSeqRestore(t *testing.T, st Store) {
	// DBStore serves the warm-cached session object (whose MessageSeq is the
	// in-process source of truth and is incremented as messages are added). The
	// store-side restoration is a cold-load reconciliation that DBStore performs
	// only on a cache miss (delegating to the raw SQLiteStore). This test writes
	// messages out of band, so it exercises the raw backends — SQLiteStore and
	// MongoDBStore — which is exactly where getMaxSeqIDForSession lives.
	if _, cached := st.(*DBStore); cached {
		t.Skip("DBStore serves the warm-cached session; restoration is a cold-load behavior of the raw backends")
	}
	// Regular session via Get.
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)
	putNMessages(t, st, s, 3) // seq_ids 1..3
	got, err := st.Get(s.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.MessageSeq != 3 {
		t.Errorf("regular session MessageSeq = %d, want 3 (restored from max seq_id)", got.MessageSeq)
	}

	// Core session via GetCoreSession (a separate restoration call site).
	cs := newSession("user-2", model.AgentTypeCore)
	mustPutSession(t, st, cs)
	putNMessages(t, st, cs, 4) // seq_ids 1..4
	gotCore, err := st.GetCoreSession("user-2")
	if err != nil {
		t.Fatalf("GetCoreSession: %v", err)
	}
	if gotCore.MessageSeq != 4 {
		t.Errorf("core session MessageSeq = %d, want 4", gotCore.MessageSeq)
	}
}

// testDeletionAudit asserts every delete path records an audit entry (S20) on
// every backend, via the auditDeletionHook test seam (the Prometheus counter is
// unexported and log scraping is brittle).
func testDeletionAudit(t *testing.T, st Store) {
	var mu sync.Mutex
	var got []string
	auditDeletionHook = func(entity, _, _ string) {
		mu.Lock()
		got = append(got, entity)
		mu.Unlock()
	}
	defer func() { auditDeletionHook = nil }()

	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)
	if err := st.Delete(s.SessionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	uf := &model.UserFile{FileID: "uf-1", UserID: "user-1", SessionID: s.SessionID, StorageKey: "user-1/uf-1", Name: "f", CreatedAt: time.Now()}
	if err := st.PutUserFile(uf); err != nil {
		t.Fatalf("PutUserFile: %v", err)
	}
	if err := st.DeleteUserFile("uf-1"); err != nil {
		t.Fatalf("DeleteUserFile: %v", err)
	}
	if err := st.DeleteUserData("user-1"); err != nil {
		t.Fatalf("DeleteUserData: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"session": false, "user_file": false, "user_data": false}
	for _, e := range got {
		if _, ok := want[e]; ok {
			want[e] = true
		}
	}
	for e, seen := range want {
		if !seen {
			t.Errorf("delete path did not audit %q (got %v)", e, got)
		}
	}
}
