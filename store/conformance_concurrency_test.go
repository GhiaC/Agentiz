package store

import (
	"fmt"
	"sync"
	"testing"

	"github.com/ghiac/agentize/model"
)

// Concurrency edge cases, run UNCHANGED against every backend via the
// conformance matrix (see conformance_test.go). They exist mainly to be run
// under `go test -race`: they assert the store stays race-free and consistent
// under contention, beyond the happy-path single-goroutine contract tests.

// testConcurrentSessionWrites hammers Put + List for one user from many
// goroutines. Distinct session IDs mean every write should land; the read
// interleaving exercises the in-memory caches/indexes under the race detector.
func testConcurrentSessionWrites(t *testing.T, st Store) {
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			s := newSession("user-cc", model.AgentTypeLow)
			s.SessionID = fmt.Sprintf("user-cc-low-s%04d", i)
			s.Title = fmt.Sprintf("s-%d", i)
			_ = st.Put(s)
			// Interleave a read to exercise the cache/index under contention.
			_, _ = st.List("user-cc")
		}(i)
	}
	wg.Wait()

	// No data race (enforced by -race) and the user is readable and consistent.
	list, err := st.List("user-cc")
	if err != nil {
		t.Fatalf("List after concurrent writes: %v", err)
	}
	if len(list) != n {
		t.Errorf("expected %d sessions after concurrent writes, got %d", n, len(list))
	}
}

// testConcurrentDeleteUserDataAndPut interleaves a destructive DeleteUserData
// with concurrent writers for the same user — the canonical "wipe while busy"
// race. Transient contention errors are tolerated; what must hold is that the
// store never races/panics and that a final wipe leaves the user empty.
func testConcurrentDeleteUserDataAndPut(t *testing.T, st Store) {
	const writers = 8
	const iters = 12
	var wg sync.WaitGroup

	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				s := newSession("user-race", model.AgentTypeLow)
				s.SessionID = fmt.Sprintf("user-race-low-w%02d-s%04d", w, i)
				if err := st.Put(s); err != nil {
					continue // transient contention error — acceptable here
				}
				m := model.NewUserMessage(s.SessionID+"-m0001", 1, "user-race", s.SessionID, "hi", model.ContentTypeText)
				_ = st.PutMessage(m)
				// A tool call too: its parent table is cleaned by user_id, not by a
				// pre-fetched session list, so a session created mid-delete cannot
				// leave its tool calls orphaned (the S6 backend-parity fix).
				_ = st.PutToolCall(newToolCall(s, s.SessionID+"-t0001", "call_"+s.SessionID, "do"))
			}
		}(w)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_ = st.DeleteUserData("user-race")
		}
	}()

	wg.Wait()

	// A final wipe must leave the user fully empty and the store consistent.
	if err := st.DeleteUserData("user-race"); err != nil {
		t.Fatalf("final DeleteUserData: %v", err)
	}
	if list, _ := st.List("user-race"); len(list) != 0 {
		t.Errorf("sessions remain after final DeleteUserData: %d", len(list))
	}
	if msgs, _ := st.GetMessagesByUser("user-race"); len(msgs) != 0 {
		t.Errorf("messages remain after final DeleteUserData: %d", len(msgs))
	}
	// No child rows may dangle once the user is wiped. The final wipe runs with
	// an empty session list (all sessions already gone), so tool calls created
	// for a session born during the race are reachable only via their user_id —
	// which is exactly what the S6 fix deletes by. Verify catches any orphan.
	if m, ok := st.(Maintainer); ok {
		issues, err := m.Verify()
		if err != nil {
			t.Fatalf("Verify after wipe: %v", err)
		}
		for _, is := range issues {
			t.Errorf("orphan after DeleteUserData: %s id=%s (%s)", is.Type, is.ID, is.Detail)
		}
	}
}
