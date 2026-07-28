package review_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghiac/agentize/model"
	"github.com/ghiac/agentize/review"
)

// fakeStore is an in-memory review.Store so the package's tests stay
// self-contained (the package depends only on model + a narrow Store).
type fakeStore struct {
	mu sync.Mutex
	m  map[string]*model.ReviewRequest
}

func newFakeStore() *fakeStore { return &fakeStore{m: map[string]*model.ReviewRequest{}} }

func (f *fakeStore) PutReviewRequest(r *model.ReviewRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *r
	f.m[r.ID] = &cp
	return nil
}

func (f *fakeStore) GetReviewRequest(id string) (*model.ReviewRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.m[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (f *fakeStore) ListPendingReviews(userID string) ([]*model.ReviewRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*model.ReviewRequest
	for _, r := range f.m {
		if r.Status == model.ReviewPending && (userID == "" || r.UserID == userID) {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out, nil
}

func TestManager_RequestNormalizesAndNotifies(t *testing.T) {
	var notified int32
	var seen *model.ReviewRequest
	m := review.New(newFakeStore(), func(_ context.Context, r *model.ReviewRequest) error {
		atomic.AddInt32(&notified, 1)
		seen = r
		return nil
	})

	id, err := m.Request(context.Background(), &model.ReviewRequest{Kind: "tool_call", RefID: "s1-t1", UserID: "u1"})
	if err != nil || id == "" {
		t.Fatalf("Request: err=%v id=%q", err, id)
	}
	if atomic.LoadInt32(&notified) != 1 {
		t.Errorf("notifier should fire exactly once, got %d", notified)
	}
	if seen == nil || seen.ID != id || seen.Status != model.ReviewPending || len(seen.Options) != 2 || seen.CreatedAt.IsZero() {
		t.Errorf("Request did not normalize defaults: %+v", seen)
	}
}

func TestManager_AwaitWokenByResolve(t *testing.T) {
	m := review.New(newFakeStore(), nil)
	id, _ := m.Request(context.Background(), &model.ReviewRequest{Kind: "tool_call", RefID: "s1-t1", UserID: "u1"})

	done := make(chan *model.ReviewRequest, 1)
	go func() {
		r, _ := m.Await(context.Background(), id)
		done <- r
	}()

	// Resolve from a different caller (e.g. a Telegram button handler).
	if _, err := m.Resolve(context.Background(), id, "approve", "lgtm", "admin"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	select {
	case r := <-done:
		if !r.IsApproved() || r.Decision != "approve" || r.Note != "lgtm" || r.DecidedBy != "admin" || r.DecidedAt.IsZero() {
			t.Errorf("awaited review wrong: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await did not return after Resolve")
	}
}

func TestManager_AwaitFastPathWhenAlreadyResolved(t *testing.T) {
	m := review.New(newFakeStore(), nil)
	id, _ := m.Request(context.Background(), &model.ReviewRequest{Kind: "custom", RefID: "x", UserID: "u1"})
	if _, err := m.Resolve(context.Background(), id, "reject", "no", "u9"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r, err := m.Await(context.Background(), id)
	if err != nil {
		t.Fatalf("Await: %v", err)
	}
	if r.Status != model.ReviewRejected || r.Decision != "reject" {
		t.Errorf("expected rejected, got %+v", r)
	}
}

func TestManager_ResolveIdempotent(t *testing.T) {
	m := review.New(newFakeStore(), nil)
	id, _ := m.Request(context.Background(), &model.ReviewRequest{Kind: "custom", RefID: "x", UserID: "u1"})
	first, err := m.Resolve(context.Background(), id, "approve", "ok", "a")
	if err != nil {
		t.Fatalf("Resolve 1: %v", err)
	}
	second, err := m.Resolve(context.Background(), id, "reject", "changed mind", "b")
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if second.Decision != first.Decision || second.DecidedBy != "a" || second.Status != model.ReviewApproved {
		t.Errorf("second resolve must be a no-op returning the first decision, got %+v", second)
	}
}

func TestManager_ResolveErrors(t *testing.T) {
	m := review.New(newFakeStore(), nil)
	if _, err := m.Resolve(context.Background(), "missing", "approve", "", ""); err == nil {
		t.Error("resolving an unknown id should error")
	}
	id, _ := m.Request(context.Background(), &model.ReviewRequest{Kind: "custom", RefID: "x", UserID: "u1"})
	if _, err := m.Resolve(context.Background(), id, "  ", "", ""); err == nil {
		t.Error("resolving with an empty decision should error")
	}
}

func TestManager_OnResolveListenerFires(t *testing.T) {
	m := review.New(newFakeStore(), nil)
	var got *model.ReviewRequest
	m.OnResolve(func(_ context.Context, r *model.ReviewRequest) { got = r })
	id, _ := m.Request(context.Background(), &model.ReviewRequest{Kind: "tool_call", RefID: "s1-t1", UserID: "u1"})
	if _, err := m.Resolve(context.Background(), id, "approve", "", "admin"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got == nil || got.ID != id || !got.IsApproved() {
		t.Errorf("OnResolve listener not fired with the resolved review: %+v", got)
	}
}

func TestManager_ListPending(t *testing.T) {
	m := review.New(newFakeStore(), nil)
	a, _ := m.Request(context.Background(), &model.ReviewRequest{Kind: "k", RefID: "1", UserID: "u1"})
	_, _ = m.Request(context.Background(), &model.ReviewRequest{Kind: "k", RefID: "2", UserID: "u2"})
	if pend, _ := m.ListPending(context.Background(), "u1"); len(pend) != 1 || pend[0].ID != a {
		t.Errorf("ListPending(u1) wrong: %+v", pend)
	}
	if pend, _ := m.ListPending(context.Background(), ""); len(pend) != 2 {
		t.Errorf("ListPending(all) = %d, want 2", len(pend))
	}
	if _, err := m.Resolve(context.Background(), a, "approve", "", "x"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if pend, _ := m.ListPending(context.Background(), "u1"); len(pend) != 0 {
		t.Errorf("ListPending(u1) after resolve = %d, want 0", len(pend))
	}
}

func TestManager_AwaitResolveRace(t *testing.T) {
	// This test attempts to trigger the TOCTOU race by executing Await and Resolve
	// concurrently with tight timing. The race window is:
	// 1. Await reads review as pending (line 160)
	// 2. Before Await registers (line 172), Resolve calls wake() and deletes the entry
	// 3. Await registers its channel after the deletion
	// 4. The channel is never woken, causing indefinite blockage

	store := newFakeStore()
	m := review.New(store, nil)

	// Run multiple iterations to increase chance of hitting the race window
	for attempt := 0; attempt < 50; attempt++ {
		id, _ := m.Request(context.Background(), &model.ReviewRequest{Kind: "test", RefID: "x", UserID: "u1"})

		done := make(chan struct{})
		var raceDetected bool

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			_, err := m.Await(ctx, id)
			// If we got a timeout error, the race was triggered
			if err != nil && err == context.DeadlineExceeded {
				raceDetected = true
			}
			close(done)
		}()

		// Very small delay to create a race window
		time.Sleep(100 * time.Microsecond)

		// Resolve while Await might be in the critical window
		m.Resolve(context.Background(), id, "approve", "", "test")

		// Wait for Await to return or timeout
		<-done

		if raceDetected {
			t.Fatalf("Attempt %d: TOCTOU race detected - Await timed out after Resolve", attempt)
		}
	}
}

// TestManager_ConcurrentResolveNoLostUpdate fires many concurrent resolutions of
// the SAME review with conflicting decisions. Resolve serializes the
// load→check→persist, so exactly one decision wins and every caller observes
// that same terminal decision — a lost update would let callers diverge from the
// stored value.
func TestManager_ConcurrentResolveNoLostUpdate(t *testing.T) {
	m := review.New(newFakeStore(), nil)
	id, _ := m.Request(context.Background(), &model.ReviewRequest{Kind: "k", RefID: "r", UserID: "u1"})

	const n = 16
	var wg sync.WaitGroup
	results := make([]*model.ReviewRequest, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			decision := "approve"
			if i%2 == 1 {
				decision = "reject"
			}
			if r, err := m.Resolve(context.Background(), id, decision, "", "u"); err == nil {
				results[i] = r
			}
		}(i)
	}
	wg.Wait()

	final, _ := m.Get(context.Background(), id)
	if final == nil || !final.IsResolved() {
		t.Fatalf("review not resolved: %+v", final)
	}
	for i, r := range results {
		if r == nil {
			t.Fatalf("resolve %d returned an error", i)
		}
		if r.Status != final.Status || r.Decision != final.Decision {
			t.Errorf("caller %d saw %s/%q but the store has %s/%q (lost update)",
				i, r.Status, r.Decision, final.Status, final.Decision)
		}
	}
}
