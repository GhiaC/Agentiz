package engine

import (
	"fmt"
	"testing"
)

func TestProgressGuard_FirstMessage(t *testing.T) {
	pg := NewProgressGuard()
	queued := pg.TryQueue("u1", "hi")
	if queued {
		t.Error("first message should not be queued (no in-progress state)")
	}
}

func TestProgressGuard_QueueWhileInProgress(t *testing.T) {
	pg := NewProgressGuard()
	pg.SetInProgress("u1", true)

	queued := pg.TryQueue("u1", "msg1")
	if !queued {
		t.Fatal("expected message to be queued while in-progress")
	}

	queued2 := pg.TryQueue("u1", "msg2")
	if !queued2 {
		t.Fatal("expected second message to be queued too")
	}

	drained := pg.DrainQueue("u1")
	if len(drained) != 2 {
		t.Fatalf("expected 2 queued messages, got %d", len(drained))
	}
	if drained[0] != "msg1" || drained[1] != "msg2" {
		t.Errorf("unexpected queue contents: %v", drained)
	}

	// After drain, queue should be empty
	drained2 := pg.DrainQueue("u1")
	if len(drained2) != 0 {
		t.Errorf("expected empty queue after drain, got %d", len(drained2))
	}
}

// TestProgressGuard_QueueCapEnforced verifies the per-key queue is bounded at
// maxQueuedPerKey: a client hammering the endpoint while a request is in flight
// still gets the "in progress" response (TryQueue stays true), but messages past
// the cap are dropped and the oldest ones are preserved — so memory cannot grow
// without limit.
func TestProgressGuard_QueueCapEnforced(t *testing.T) {
	pg := NewProgressGuard()
	pg.SetInProgress("u1", true)

	over := maxQueuedPerKey + 5
	for i := 0; i < over; i++ {
		if !pg.TryQueue("u1", fmt.Sprintf("msg%d", i)) {
			t.Fatalf("TryQueue should stay true while in-progress (msg %d)", i)
		}
	}

	drained := pg.DrainQueue("u1")
	if len(drained) != maxQueuedPerKey {
		t.Fatalf("queue should be capped at %d, got %d", maxQueuedPerKey, len(drained))
	}
	// The oldest messages are the ones kept; the overflow is dropped.
	if drained[0] != "msg0" || drained[maxQueuedPerKey-1] != fmt.Sprintf("msg%d", maxQueuedPerKey-1) {
		t.Errorf("cap should keep the oldest %d messages, got first=%q last=%q",
			maxQueuedPerKey, drained[0], drained[len(drained)-1])
	}
}

func TestProgressGuard_DoneReleasesKey(t *testing.T) {
	pg := NewProgressGuard()
	pg.SetInProgress("u1", true)

	queued := pg.TryQueue("u1", "queued-msg")
	if !queued {
		t.Fatal("expected queued while in-progress")
	}

	pg.SetInProgress("u1", false)

	queued2 := pg.TryQueue("u1", "new-msg")
	if queued2 {
		t.Error("after SetInProgress(false), TryQueue should return false")
	}
}
