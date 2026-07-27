package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
)

// This file is the single source of truth for backend parity. Every test runs
// UNCHANGED against each backend (SQLite file, SQLite in-memory, and a real
// MongoDB via testcontainers). If SQLite and MongoDB ever diverge in behavior,
// the same assertion fails on one of them — which is exactly what we want.
//
// MongoDB requires Docker. When Docker is unavailable the MongoDB backend is
// skipped gracefully and the SQLite backends still run, so `go test ./store/`
// always works. Set MONGODB_URI to point at an external MongoDB instead.

// ---------------------------------------------------------------------------
// Backend matrix
// ---------------------------------------------------------------------------

type backendFactory struct {
	name     string
	newStore func(t *testing.T) Store
}

var (
	mongoOnce sync.Once
	mongoURI  string
	mongoDBN  int64
)

// sharedMongoURI lazily starts ONE MongoDB container for the whole package run.
// Returns "" when Docker (and MONGODB_URI) are unavailable so callers can skip.
func sharedMongoURI() string {
	mongoOnce.Do(func() {
		if uri := os.Getenv("MONGODB_URI"); uri != "" {
			mongoURI = uri
			return
		}
		ctx := context.Background()
		container, err := mongodb.Run(ctx, "mongo:7")
		if err != nil {
			// Docker not available — leave empty to skip the MongoDB backend.
			return
		}
		uri, err := container.ConnectionString(ctx)
		if err != nil {
			return
		}
		mongoURI = uri
		// Container lives for the test binary's lifetime; the testcontainers
		// reaper cleans it up after the process exits.
	})
	return mongoURI
}

// conformanceBackends returns every backend to run the suite against. Each
// newStore produces a fresh, isolated store.
func conformanceBackends(t *testing.T) []backendFactory {
	t.Helper()
	backends := []backendFactory{
		{
			name: "sqlite-file",
			newStore: func(t *testing.T) Store {
				st, err := NewDBStoreWithPath(filepath.Join(t.TempDir(), "conformance.db"))
				if err != nil {
					t.Fatalf("sqlite-file: %v", err)
				}
				return st
			},
		},
		{
			name: "sqlite-memory",
			newStore: func(t *testing.T) Store {
				st, err := NewSQLiteStore(":memory:")
				if err != nil {
					t.Fatalf("sqlite-memory: %v", err)
				}
				return st
			},
		},
	}

	if uri := sharedMongoURI(); uri != "" {
		backends = append(backends, backendFactory{
			name: "mongodb",
			newStore: func(t *testing.T) Store {
				cfg := DefaultMongoDBStoreConfig()
				cfg.URI = uri
				// Unique database per store so sub-tests are fully isolated.
				cfg.Database = fmt.Sprintf("conf_%d", atomic.AddInt64(&mongoDBN, 1))
				st, err := NewMongoDBStore(cfg)
				if err != nil {
					t.Fatalf("mongodb: %v", err)
				}
				return st
			},
		})
	} else {
		t.Log("MongoDB backend skipped (Docker/MONGODB_URI unavailable); SQLite backends still run")
	}
	return backends
}

// TestStoreConformance runs the full contract suite against every backend.
func TestStoreConformance(t *testing.T) {
	for _, b := range conformanceBackends(t) {
		b := b
		t.Run(b.name, func(t *testing.T) {
			// Each case gets a fresh store so tests never see each other's data.
			run := func(name string, fn func(t *testing.T, st Store)) {
				t.Run(name, func(t *testing.T) {
					st := b.newStore(t)
					t.Cleanup(func() { _ = st.Close() })
					fn(t, st)
				})
			}

			run("SessionCRUD", testSessionCRUD)
			run("SessionNotFound", testSessionNotFound)
			run("CoreSession", testCoreSession)
			run("Users", testUsers)
			run("Messages", testMessagesOrdering)
			run("OpenedFiles", testOpenedFiles)
			run("UserFiles", testUserFiles)
			run("ToolCalls", testToolCalls)
			run("ToolCallNotFound", testToolCallNotFound)
			run("SummarizationLogs", testSummarizationLogs)
			run("RouteTraces", testRouteTraces)
			run("VisitedNodes", testVisitedNodes)
			run("DeleteUserData", testDeleteUserData)
			run("BackendInfo", testBackendInfo)
			run("Validation", testValidation)
			run("MessagePagination", testMessagePagination)
			run("PutMessagesBatch", testPutMessagesBatch)
			run("Quotas", testQuotas)
			run("Reviews", testReviews)
			run("TaskSchedules", testTaskSchedules)
			run("Maintainer", testMaintainer)
			run("VerifyDetectsOrphans", testVerifyDetectsOrphans)
			run("MessageSeqRestore", testMessageSeqRestore)
			run("DeletionAudit", testDeletionAudit)
			run("ConcurrentSessionWrites", testConcurrentSessionWrites)
			run("ConcurrentDeleteAndPut", testConcurrentDeleteUserDataAndPut)
		})
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func sameSecond(a, b time.Time) bool { return a.Unix() == b.Unix() }

func newSession(userID string, agentType model.AgentType) *model.Session {
	return model.NewSessionWithType(userID, agentType)
}

func mustPutSession(t *testing.T, st Store, s *model.Session) {
	t.Helper()
	if err := st.Put(s); err != nil {
		t.Fatalf("Put session: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Contract tests (identical for every backend)
// ---------------------------------------------------------------------------

func testSessionCRUD(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeLow)
	s.Title = "hello"
	mustPutSession(t, st, s)

	got, err := st.Get(s.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionID != s.SessionID || got.UserID != "user-1" || got.Title != "hello" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if !sameSecond(got.CreatedAt, s.CreatedAt) {
		t.Errorf("CreatedAt not preserved to the second: got %v want %v", got.CreatedAt, s.CreatedAt)
	}

	// List returns the user's sessions.
	list, err := st.List("user-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}

	// Put is an upsert (replace, not duplicate).
	s.Title = "updated"
	mustPutSession(t, st, s)
	if list, _ := st.List("user-1"); len(list) != 1 {
		t.Fatalf("after update List len = %d, want 1", len(list))
	}
	if got, _ := st.Get(s.SessionID); got.Title != "updated" {
		t.Errorf("update not applied: title = %q", got.Title)
	}

	// Delete removes it; Get then errors (session is a required entity).
	if err := st.Delete(s.SessionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.Get(s.SessionID); err == nil {
		t.Error("Get after Delete should error")
	}
}

func testSessionNotFound(t *testing.T, st Store) {
	// Contract: Get of a missing session returns an error (not nil,nil).
	if _, err := st.Get("does-not-exist"); err == nil {
		t.Error("Get(missing) should return an error")
	}
}

func testCoreSession(t *testing.T, st Store) {
	// Contract: missing core session => (nil, nil).
	got, err := st.GetCoreSession("user-1")
	if err != nil {
		t.Fatalf("GetCoreSession(missing): unexpected err %v", err)
	}
	if got != nil {
		t.Fatalf("GetCoreSession(missing) = %+v, want nil", got)
	}

	core := newSession("user-1", model.AgentTypeCore)
	if err := st.PutCoreSession(core); err != nil {
		t.Fatalf("PutCoreSession: %v", err)
	}
	got, err = st.GetCoreSession("user-1")
	if err != nil || got == nil {
		t.Fatalf("GetCoreSession after put: got=%v err=%v", got, err)
	}
	if got.SessionID != core.SessionID {
		t.Errorf("core session id mismatch: got %s want %s", got.SessionID, core.SessionID)
	}

	// Only one core session per user: putting another replaces it.
	core2 := newSession("user-1", model.AgentTypeCore)
	if err := st.PutCoreSession(core2); err != nil {
		t.Fatalf("PutCoreSession 2: %v", err)
	}
	got, _ = st.GetCoreSession("user-1")
	if got == nil || got.SessionID != core2.SessionID {
		t.Errorf("core session not replaced: got %v want %s", got, core2.SessionID)
	}
}

func testUsers(t *testing.T, st Store) {
	// Contract: missing user => (nil, nil).
	if u, err := st.GetUser("nobody"); err != nil || u != nil {
		t.Fatalf("GetUser(missing) = (%v, %v), want (nil, nil)", u, err)
	}

	u := model.NewUser("user-1")
	u.Name = "Ada"
	if err := st.PutUser(u); err != nil {
		t.Fatalf("PutUser: %v", err)
	}
	got, err := st.GetUser("user-1")
	if err != nil || got == nil || got.Name != "Ada" {
		t.Fatalf("GetUser: got=%v err=%v", got, err)
	}

	// GetOrCreateUser creates on demand and is idempotent.
	created, err := st.GetOrCreateUser("user-2")
	if err != nil || created == nil || created.UserID != "user-2" {
		t.Fatalf("GetOrCreateUser(new): got=%v err=%v", created, err)
	}
	again, _ := st.GetOrCreateUser("user-1")
	if again == nil || again.Name != "Ada" {
		t.Errorf("GetOrCreateUser(existing) lost data: %+v", again)
	}
}

func testMessagesOrdering(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)

	base := time.Now().Add(-time.Hour)
	for i := 1; i <= 3; i++ {
		m := model.NewUserMessage(fmt.Sprintf("%s-m%04d", s.SessionID, i), i, "user-1", s.SessionID, fmt.Sprintf("msg-%d", i), model.ContentTypeText)
		m.CreatedAt = base.Add(time.Duration(i) * time.Second)
		if err := st.PutMessage(m); err != nil {
			t.Fatalf("PutMessage %d: %v", i, err)
		}
	}

	// Contract: GetMessagesBySession is newest-first.
	msgs, err := st.GetMessagesBySession(s.SessionID)
	if err != nil {
		t.Fatalf("GetMessagesBySession: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	if msgs[0].Content != "msg-3" || msgs[2].Content != "msg-1" {
		t.Errorf("messages not newest-first: [%s, %s, %s]", msgs[0].Content, msgs[1].Content, msgs[2].Content)
	}

	if byUser, _ := st.GetMessagesByUser("user-1"); len(byUser) != 3 {
		t.Errorf("GetMessagesByUser len = %d, want 3", len(byUser))
	}
	if all, _ := st.GetAllMessages(); len(all) != 3 {
		t.Errorf("GetAllMessages len = %d, want 3", len(all))
	}
}

func testOpenedFiles(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)

	f1 := model.NewOpenedFile(s, "/a.md", "a")
	f1.OpenedAt = time.Now().Add(-2 * time.Minute)
	f2 := model.NewOpenedFile(s, "/b.md", "b")
	f2.OpenedAt = time.Now().Add(-1 * time.Minute)
	if err := st.AddOpenedFile(f1); err != nil {
		t.Fatalf("AddOpenedFile f1: %v", err)
	}
	if err := st.AddOpenedFile(f2); err != nil {
		t.Fatalf("AddOpenedFile f2: %v", err)
	}

	// Contract: oldest-first ordering.
	got, err := st.GetOpenedFilesBySession(s.SessionID)
	if err != nil {
		t.Fatalf("GetOpenedFilesBySession: %v", err)
	}
	if len(got) != 2 || got[0].FilePath != "/a.md" || got[1].FilePath != "/b.md" {
		t.Fatalf("opened files not oldest-first: %+v", got)
	}

	// Both currently open.
	if cur, _ := st.GetCurrentlyOpenedFilesBySession(s.SessionID); len(cur) != 2 {
		t.Fatalf("currently-open = %d, want 2", len(cur))
	}

	// Close one; it must drop from currently-open and reflect closed state.
	if err := st.CloseOpenedFile(s.SessionID, "/a.md"); err != nil {
		t.Fatalf("CloseOpenedFile: %v", err)
	}
	cur, _ := st.GetCurrentlyOpenedFilesBySession(s.SessionID)
	if len(cur) != 1 || cur[0].FilePath != "/b.md" {
		t.Fatalf("after close, currently-open = %+v, want only /b.md", cur)
	}
	all, _ := st.GetOpenedFilesBySession(s.SessionID)
	for _, f := range all {
		if f.FilePath == "/a.md" {
			if f.IsOpen {
				t.Error("closed file still reports IsOpen=true")
			}
			if f.ClosedAt.IsZero() {
				t.Error("closed file has zero ClosedAt")
			}
		}
	}

	// Closing again is a no-op (currently-open count unchanged).
	if err := st.CloseOpenedFile(s.SessionID, "/a.md"); err != nil {
		t.Fatalf("CloseOpenedFile (repeat): %v", err)
	}
	if cur, _ := st.GetCurrentlyOpenedFilesBySession(s.SessionID); len(cur) != 1 {
		t.Errorf("repeat close changed open count: %d", len(cur))
	}

	if byUser, _ := st.GetOpenedFilesByUser("user-1"); len(byUser) != 2 {
		t.Errorf("GetOpenedFilesByUser = %d, want 2", len(byUser))
	}
}

func testUserFiles(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)

	uf := model.NewUserFile(s, "notes.txt", "text/plain", 11, "user-1/notes.txt", model.FileSourceUploaded)
	if err := st.PutUserFile(uf); err != nil {
		t.Fatalf("PutUserFile: %v", err)
	}

	got, err := st.GetUserFile(uf.FileID)
	if err != nil || got == nil {
		t.Fatalf("GetUserFile: got=%v err=%v", got, err)
	}
	if got.Name != "notes.txt" || got.Source != model.FileSourceUploaded || got.Size != 11 {
		t.Errorf("user file round-trip mismatch: %+v", got)
	}

	// Contract: missing file => (nil, nil).
	if mf, err := st.GetUserFile("nope"); err != nil || mf != nil {
		t.Fatalf("GetUserFile(missing) = (%v, %v), want (nil, nil)", mf, err)
	}

	if byUser, _ := st.GetUserFilesByUser("user-1"); len(byUser) != 1 {
		t.Errorf("GetUserFilesByUser = %d, want 1", len(byUser))
	}
	if bySession, _ := st.GetUserFilesBySession(s.SessionID); len(bySession) != 1 {
		t.Errorf("GetUserFilesBySession = %d, want 1", len(bySession))
	}
	if all, _ := st.GetAllUserFiles(); len(all) != 1 {
		t.Errorf("GetAllUserFiles = %d, want 1", len(all))
	}

	if err := st.DeleteUserFile(uf.FileID); err != nil {
		t.Fatalf("DeleteUserFile: %v", err)
	}
	if mf, _ := st.GetUserFile(uf.FileID); mf != nil {
		t.Error("file still present after delete")
	}
}

func newToolCall(s *model.Session, toolID, toolCallID, fn string) *model.ToolCall {
	return &model.ToolCall{
		ToolID:       toolID,
		ToolCallID:   toolCallID,
		MessageID:    s.SessionID + "-m0001",
		SessionID:    s.SessionID,
		UserID:       s.UserID,
		AgentType:    s.AgentType,
		FunctionName: fn,
		Arguments:    `{"x":1}`,
		Status:       "pending",
		CreatedAt:    time.Now(),
	}
}

func testToolCalls(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)

	tc := newToolCall(s, s.SessionID+"-t0001", "call_abc", "do_thing")
	if err := st.PutToolCall(tc); err != nil {
		t.Fatalf("PutToolCall: %v", err)
	}

	byCallID, err := st.GetToolCallByID("call_abc")
	if err != nil || byCallID == nil || byCallID.FunctionName != "do_thing" {
		t.Fatalf("GetToolCallByID: got=%v err=%v", byCallID, err)
	}
	byToolID, err := st.GetToolCallByToolID(s.SessionID + "-t0001")
	if err != nil || byToolID == nil {
		t.Fatalf("GetToolCallByToolID: got=%v err=%v", byToolID, err)
	}

	if err := st.UpdateToolCallResponse(s.SessionID+"-t0001", "the-result", nil); err != nil {
		t.Fatalf("UpdateToolCallResponse: %v", err)
	}
	updated, _ := st.GetToolCallByToolID(s.SessionID + "-t0001")
	if updated == nil || updated.Response != "the-result" || updated.Status != "success" {
		t.Errorf("update not applied: %+v", updated)
	}

	if bySession, _ := st.GetToolCallsBySession(s.SessionID); len(bySession) != 1 {
		t.Errorf("GetToolCallsBySession = %d, want 1", len(bySession))
	}
	if all, _ := st.GetAllToolCalls(); len(all) != 1 {
		t.Errorf("GetAllToolCalls = %d, want 1", len(all))
	}
}

func testToolCallNotFound(t *testing.T, st Store) {
	// Contract: optional lookups return (nil, nil) when not found on EVERY backend.
	if tc, err := st.GetToolCallByID("missing"); err != nil || tc != nil {
		t.Errorf("GetToolCallByID(missing) = (%v, %v), want (nil, nil)", tc, err)
	}
	if tc, err := st.GetToolCallByToolID("missing"); err != nil || tc != nil {
		t.Errorf("GetToolCallByToolID(missing) = (%v, %v), want (nil, nil)", tc, err)
	}
}

func testSummarizationLogs(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeLow)
	mustPutSession(t, st, s)

	base := time.Now().Add(-time.Hour)
	var firstID string
	for i := 1; i <= 3; i++ {
		lg := model.NewSummarizationLog(s)
		lg.CreatedAt = base.Add(time.Duration(i) * time.Second)
		lg.GeneratedSummary = fmt.Sprintf("summary-%d", i)
		if i == 1 {
			firstID = lg.LogID
		}
		if err := st.PutSummarizationLog(lg); err != nil {
			t.Fatalf("PutSummarizationLog %d: %v", i, err)
		}
	}

	// Contract: newest-first ordering.
	logs, err := st.GetSummarizationLogsBySession(s.SessionID)
	if err != nil {
		t.Fatalf("GetSummarizationLogsBySession: %v", err)
	}
	if len(logs) != 3 {
		t.Fatalf("got %d logs, want 3", len(logs))
	}
	if logs[0].GeneratedSummary != "summary-3" || logs[2].GeneratedSummary != "summary-1" {
		t.Errorf("logs not newest-first: [%s, %s, %s]", logs[0].GeneratedSummary, logs[1].GeneratedSummary, logs[2].GeneratedSummary)
	}

	// Contract: re-putting the same LogID upserts (no duplicate).
	dup := model.NewSummarizationLog(s)
	dup.LogID = firstID
	dup.GeneratedSummary = "summary-1-edited"
	if err := st.PutSummarizationLog(dup); err != nil {
		t.Fatalf("PutSummarizationLog (upsert): %v", err)
	}
	if logs, _ := st.GetSummarizationLogsBySession(s.SessionID); len(logs) != 3 {
		t.Errorf("upsert created a duplicate: len = %d, want 3", len(logs))
	}
	if all, _ := st.GetAllSummarizationLogs(); len(all) != 3 {
		t.Errorf("GetAllSummarizationLogs = %d, want 3", len(all))
	}
}

// buildRouteTrace assembles a small dispatch trace via the model builder so the
// test exercises the same DAG shape the Core produces in production.
func buildRouteTrace(s *model.Session, msg, agent string, createdAt time.Time) *model.RouteTrace {
	b := model.NewRouteTraceBuilder(s, msg)
	b.Decision("Decision", "test-model", 42, 10, model.RouteStatusOK, "finish_reason=tool_calls")
	b.Dispatch(agent, "Agent "+agent, msg, model.RouteStatusOK, 100)
	b.Response("final answer", true, model.RouteStatusOK)
	tr := b.Build(0)
	tr.CreatedAt = createdAt
	return tr
}

func testRouteTraces(t *testing.T, st Store) {
	s := newSession("user-1", model.AgentTypeCore)
	mustPutSession(t, st, s)

	base := time.Now().Add(-time.Hour)
	t1 := buildRouteTrace(s, "first", "low", base.Add(1*time.Second))
	t2 := buildRouteTrace(s, "second", "high", base.Add(2*time.Second))
	for _, tr := range []*model.RouteTrace{t1, t2} {
		if err := st.PutRouteTrace(tr); err != nil {
			t.Fatalf("PutRouteTrace %s: %v", tr.TraceID, err)
		}
	}

	// Get-by-id round-trips the full DAG (nodes + edges + dispatched agent).
	got, err := st.GetRouteTraceByID(t1.TraceID)
	if err != nil || got == nil {
		t.Fatalf("GetRouteTraceByID: got=%v err=%v", got, err)
	}
	if len(got.Nodes) != len(t1.Nodes) || len(got.Edges) != len(t1.Edges) {
		t.Errorf("DAG not preserved: nodes=%d/%d edges=%d/%d", len(got.Nodes), len(t1.Nodes), len(got.Edges), len(t1.Edges))
	}
	if agents := got.DispatchedAgents(); len(agents) != 1 || agents[0] != "low" {
		t.Errorf("DispatchedAgents = %v, want [low]", agents)
	}
	if got.TotalTokens != 42 {
		t.Errorf("TotalTokens = %d, want 42", got.TotalTokens)
	}

	// Not found -> (nil, nil), never an error.
	if missing, err := st.GetRouteTraceByID("nope-rt9999"); err != nil || missing != nil {
		t.Errorf("GetRouteTraceByID(missing) = %v, %v; want nil, nil", missing, err)
	}

	// Contract: newest-first ordering.
	bySession, err := st.GetRouteTracesBySession(s.SessionID)
	if err != nil {
		t.Fatalf("GetRouteTracesBySession: %v", err)
	}
	if len(bySession) != 2 {
		t.Fatalf("GetRouteTracesBySession = %d, want 2", len(bySession))
	}
	if bySession[0].TraceID != t2.TraceID || bySession[1].TraceID != t1.TraceID {
		t.Errorf("not newest-first: [%s, %s]", bySession[0].TraceID, bySession[1].TraceID)
	}
	if byUser, _ := st.GetRouteTracesByUser("user-1"); len(byUser) != 2 {
		t.Errorf("GetRouteTracesByUser = %d, want 2", len(byUser))
	}
	if all, _ := st.GetAllRouteTraces(); len(all) != 2 {
		t.Errorf("GetAllRouteTraces = %d, want 2", len(all))
	}

	// Contract: re-putting the same TraceID upserts (no duplicate).
	t1.Status = "error"
	if err := st.PutRouteTrace(t1); err != nil {
		t.Fatalf("PutRouteTrace (upsert): %v", err)
	}
	if all, _ := st.GetAllRouteTraces(); len(all) != 2 {
		t.Errorf("upsert created a duplicate: len = %d, want 2", len(all))
	}
}

func testVisitedNodes(t *testing.T, st Store) {
	st.AddVisitedNode("user-1", &model.NodeDigest{Path: "root", ID: "n1", Title: "Root"})
	st.AddVisitedNode("user-1", &model.NodeDigest{Path: "root/a", ID: "n2", Title: "A"})

	nodes := st.GetVisitedNodes("user-1")
	if len(nodes) != 2 {
		t.Fatalf("GetVisitedNodes = %d, want 2", len(nodes))
	}
	if !st.HasVisitedNode("user-1", "root") {
		t.Error("HasVisitedNode(root) = false, want true")
	}
	if st.HasVisitedNode("user-1", "root/missing") {
		t.Error("HasVisitedNode(missing) = true, want false")
	}
	if paths := st.GetVisitedNodePaths("user-1"); len(paths) != 2 {
		t.Errorf("GetVisitedNodePaths = %d, want 2", len(paths))
	}

	st.ClearVisitedNodes("user-1")
	if nodes := st.GetVisitedNodes("user-1"); len(nodes) != 0 {
		t.Errorf("after clear GetVisitedNodes = %d, want 0", len(nodes))
	}
}

func testDeleteUserData(t *testing.T, st Store) {
	s := newSession("user-del", model.AgentTypeLow)
	mustPutSession(t, st, s)
	m := model.NewUserMessage(s.SessionID+"-m0001", 1, "user-del", s.SessionID, "hi", model.ContentTypeText)
	if err := st.PutMessage(m); err != nil {
		t.Fatalf("PutMessage: %v", err)
	}
	uf := model.NewUserFile(s, "f.txt", "text/plain", 2, "user-del/f.txt", model.FileSourceUploaded)
	if err := st.PutUserFile(uf); err != nil {
		t.Fatalf("PutUserFile: %v", err)
	}
	tr := buildRouteTrace(s, "hi", "low", time.Now())
	if err := st.PutRouteTrace(tr); err != nil {
		t.Fatalf("PutRouteTrace: %v", err)
	}
	now := time.Now()
	schedule := &model.TaskSchedule{
		ScheduleID: "sch-user-del", UserID: "user-del", SessionID: s.SessionID,
		AgentType: s.AgentType, Name: "delete me", Prompt: "work",
		IntervalSeconds: 60, Status: model.TaskScheduleActive,
		NextRunAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	if err := st.PutTaskSchedule(schedule); err != nil {
		t.Fatalf("PutTaskSchedule: %v", err)
	}
	if err := st.PutTaskScheduleRun(&model.TaskScheduleRun{
		RunID: "run-user-del", ScheduleID: schedule.ScheduleID,
		UserID: "user-del", SessionID: s.SessionID,
		Status: model.TaskRunSucceeded, StartedAt: now, CompletedAt: now,
	}); err != nil {
		t.Fatalf("PutTaskScheduleRun: %v", err)
	}

	if err := st.DeleteUserData("user-del"); err != nil {
		t.Fatalf("DeleteUserData: %v", err)
	}

	if list, _ := st.List("user-del"); len(list) != 0 {
		t.Errorf("sessions remain after DeleteUserData: %d", len(list))
	}
	if msgs, _ := st.GetMessagesByUser("user-del"); len(msgs) != 0 {
		t.Errorf("messages remain after DeleteUserData: %d", len(msgs))
	}
	if files, _ := st.GetUserFilesByUser("user-del"); len(files) != 0 {
		t.Errorf("user files remain after DeleteUserData: %d", len(files))
	}
	if traces, _ := st.GetRouteTracesByUser("user-del"); len(traces) != 0 {
		t.Errorf("route traces remain after DeleteUserData: %d", len(traces))
	}
	if schedules, _ := st.ListTaskSchedules("user-del"); len(schedules) != 0 {
		t.Errorf("task schedules remain after DeleteUserData: %d", len(schedules))
	}
	if runs, _ := st.ListTaskScheduleRuns(schedule.ScheduleID, 10); len(runs) != 0 {
		t.Errorf("task schedule runs remain after DeleteUserData: %d", len(runs))
	}
}

func testBackendInfo(t *testing.T, st Store) {
	info := st.BackendInfo()
	if info.Type == "" {
		t.Error("BackendInfo().Type is empty")
	}
}
