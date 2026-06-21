package engine

import (
	"strings"
	"testing"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/store"
)

// TestProcessToolResult_OversizedPersistsAndCollectible is the regression test
// for the collect_result bug: an oversized tool result was stored on a fresh
// session clone inside saveToolResult and then clobbered by the ProcessMessage
// loop's single Put(session), so collect_result later failed with
// "result not found in session". The fix stores the result on the live session
// the loop persists. DBStore is used because it clones on every Get (the exact
// condition that triggered the bug).
func TestProcessToolResult_OversizedPersistsAndCollectible(t *testing.T) {
	st, err := store.NewDBStoreWithPath(":memory:")
	if err != nil {
		t.Fatalf("NewDBStoreWithPath: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := &Engine{Sessions: st, llmConfig: LLMConfig{MaxToolResultLength: 50}}

	const sessionID = "u1-low-s0001"
	if err := st.Put(model.NewSessionWithID("u1", sessionID, model.AgentTypeLow)); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Mirror ProcessMessage: load the session (store returns a clone), process an
	// oversized tool result onto it, then persist with the loop's single Put.
	loaded, err := eng.Sessions.Get(sessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	big := strings.Repeat("namespace-", 50) // 500 bytes > 50
	msg := eng.processToolResult(loaded, big)
	if !strings.Contains(msg, "collect_result") {
		t.Fatalf("expected a collect_result hint, got %q", msg)
	}
	if len(loaded.ToolResults) != 1 {
		t.Fatalf("oversized result should be stored on the session, got %d entries", len(loaded.ToolResults))
	}
	var resultID string
	for k := range loaded.ToolResults {
		resultID = k
	}

	// The single Put the loop performs after executing the iteration's tools.
	if err := eng.Sessions.Put(loaded); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// collect_result retrieval goes through a fresh Get (clone). It must find it.
	got, ok := eng.GetToolResult(sessionID, resultID)
	if !ok {
		t.Fatalf("regression: stored tool result not found after Put — collect_result would fail")
	}
	if got != big {
		t.Fatalf("result mismatch after round-trip")
	}

	// The full collect_result entry point must parse the id and locate it (no
	// "result not found in session" error).
	if sid, ok := parseResultID(resultID); !ok || sid != sessionID {
		t.Fatalf("parseResultID(%q) = %q,%v; want %q,true", resultID, sid, ok, sessionID)
	}
	if _, ok := eng.GetToolResult(sessionID, resultID); !ok {
		t.Fatalf("GetToolResult should locate the stored result")
	}
}

func TestProcessToolResult_SmallResultNotStored(t *testing.T) {
	st, err := store.NewDBStoreWithPath(":memory:")
	if err != nil {
		t.Fatalf("NewDBStoreWithPath: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	eng := &Engine{Sessions: st, llmConfig: LLMConfig{MaxToolResultLength: 50}}
	sess := model.NewSessionWithID("u1", "u1-low-s0002", model.AgentTypeLow)

	out := eng.processToolResult(sess, "short result")
	if out != "short result" {
		t.Fatalf("small result should be returned unchanged, got %q", out)
	}
	if len(sess.ToolResults) != 0 {
		t.Fatalf("small result must not be stored, got %d entries", len(sess.ToolResults))
	}
}
