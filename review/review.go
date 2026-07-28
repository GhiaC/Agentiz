// Package review is a generic, UI-agnostic human-approval primitive. Any code
// can raise "a human must decide X" (a ReviewRequest) and any frontend — a
// Telegram bot, the debug dashboard, an HTTP API — can present and resolve it
// through the single Resolve entry point.
//
// Design: depends on model (and a narrow Store interface) only — never on a
// specific UI or web framework.
package review

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ghiac/agentize/log"
	"github.com/ghiac/agentize/model"
)

// Store persists review requests. store.Store satisfies it structurally on every
// backend (SQLite + MongoDB), so the same review mechanism is durable across
// restarts and works regardless of the chosen database.
type Store interface {
	PutReviewRequest(r *model.ReviewRequest) error
	// GetReviewRequest returns the request by id, or (nil, nil) when absent.
	GetReviewRequest(id string) (*model.ReviewRequest, error)
	// ListPendingReviews returns pending requests for a user, newest first;
	// userID == "" returns pending requests for all users.
	ListPendingReviews(userID string) ([]*model.ReviewRequest, error)
}

// Notifier is fired once when a review is created so a frontend can present it
// (a Telegram message with approve/reject buttons, a push, an email…). It is
// UI-agnostic and best-effort: a notifier error never fails the request (the
// review is already persisted and resolvable from any other surface).
type Notifier func(ctx context.Context, r *model.ReviewRequest) error

// ResolveListener is invoked after a review is resolved (from any UI). The
// Callers may use it to react to decisions. Listeners run synchronously inside
// Resolve, so they should
// be cheap or hand off to a goroutine.
type ResolveListener func(ctx context.Context, r *model.ReviewRequest)

// Manager is the one object every frontend talks to. It persists requests via
// the Store, fires the Notifier on creation, wakes in-process Await callers and
// ResolveListeners on resolution, and keeps decisions idempotent.
type Manager struct {
	store Store

	// resolveMu serializes the load→check→persist sequence in Resolve so two
	// concurrent decisions on the same review cannot both pass the
	// already-resolved check and lose one of the writes. It is separate from mu
	// (which guards the waiter/listener/notifier state) so wake/fireListeners,
	// which take mu, never deadlock against it.
	resolveMu sync.Mutex

	mu        sync.Mutex
	notifier  Notifier
	listeners []ResolveListener
	waiters   map[string][]chan struct{} // review id -> Await wake channels
}

// New returns a Manager backed by store. notifier may be nil (no announcement);
// wire one later with SetNotifier.
func New(store Store, notifier Notifier) *Manager {
	return &Manager{
		store:    store,
		notifier: notifier,
		waiters:  make(map[string][]chan struct{}),
	}
}

// SetNotifier sets (or replaces) the creation notifier.
func (m *Manager) SetNotifier(n Notifier) {
	m.mu.Lock()
	m.notifier = n
	m.mu.Unlock()
}

// OnResolve registers a listener invoked whenever a review is resolved.
func (m *Manager) OnResolve(l ResolveListener) {
	if l == nil {
		return
	}
	m.mu.Lock()
	m.listeners = append(m.listeners, l)
	m.mu.Unlock()
}

// Request creates a pending review: it fills in defaults (random id, pending
// status, approve/reject options, CreatedAt), persists it, fires the notifier
// (best-effort), and returns the review id. The caller may pass a partial
// request — only Kind/RefID and the human-facing fields are required.
func (m *Manager) Request(ctx context.Context, r *model.ReviewRequest) (string, error) {
	if r == nil {
		return "", fmt.Errorf("review: request cannot be nil")
	}
	if r.ID == "" {
		r.ID = model.NewReviewID()
	}
	if r.Status == "" {
		r.Status = model.ReviewPending
	}
	if len(r.Options) == 0 {
		r.Options = []string{"approve", "reject"}
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	if err := m.store.PutReviewRequest(r); err != nil {
		return "", fmt.Errorf("review: persist request: %w", err)
	}

	m.mu.Lock()
	notifier := m.notifier
	m.mu.Unlock()
	if notifier != nil {
		if err := notifier(ctx, r); err != nil {
			// Best-effort: the review is persisted and resolvable elsewhere.
			log.Log.Warnf("review: notifier for %s failed: %v", r.ID, err)
		}
	}
	return r.ID, nil
}

// Resolve records a decision and wakes any Await callers and ResolveListeners.
// It is the single entry point every UI uses (a Telegram button handler, a
// dashboard POST, an API call). Idempotent: resolving an already-resolved review
// returns the existing decision unchanged.
func (m *Manager) Resolve(ctx context.Context, id, decision, note, by string) (*model.ReviewRequest, error) {
	// Serialize the load→check→persist so concurrent resolutions of the same
	// review cannot lose an update (the store layer locks each call, but not the
	// whole read-modify-write). wake/fireListeners below use mu, not resolveMu.
	m.resolveMu.Lock()
	defer m.resolveMu.Unlock()

	r, err := m.store.GetReviewRequest(id)
	if err != nil {
		return nil, fmt.Errorf("review: load %s: %w", id, err)
	}
	if r == nil {
		return nil, fmt.Errorf("review: request %q not found", id)
	}
	if r.IsResolved() {
		return r, nil // idempotent
	}
	if strings.TrimSpace(decision) == "" {
		return nil, fmt.Errorf("review: a non-empty decision is required to resolve %q", id)
	}

	r.Status = decisionToStatus(decision)
	r.Decision = decision
	r.Note = note
	r.DecidedBy = by
	r.DecidedAt = time.Now()
	if err := m.store.PutReviewRequest(r); err != nil {
		return nil, fmt.Errorf("review: persist decision for %s: %w", id, err)
	}

	m.wake(id)
	m.fireListeners(ctx, r)
	return r, nil
}

// Await blocks until the review is resolved, ctx is done, or ExpiresAt passes.
// It is the synchronous bridge's mechanism; durable suspend/resume does not use
// it. A nil error and a resolved request mean a decision was made.
func (m *Manager) Await(ctx context.Context, id string) (*model.ReviewRequest, error) {
	// Fast path: already resolved.
	if r, err := m.store.GetReviewRequest(id); err != nil {
		return nil, err
	} else if r == nil {
		return nil, fmt.Errorf("review: request %q not found", id)
	} else if r.IsResolved() {
		return r, nil
	} else if !r.ExpiresAt.IsZero() && !time.Now().Before(r.ExpiresAt) {
		return r, nil // already past expiry; caller decides what that means
	}

	ch := make(chan struct{})
	m.mu.Lock()
	m.waiters[id] = append(m.waiters[id], ch)
	m.mu.Unlock()
	defer m.removeWaiter(id, ch)

	// Re-check after registering: a Resolve that landed between the fast-path
	// read above and this registration would have woken no waiter (ours did not
	// exist yet). Catch it now so Await never blocks on an already-resolved
	// review. This single read also arms the expiry timer.
	var expiry <-chan time.Time
	if r, err := m.store.GetReviewRequest(id); err != nil {
		return nil, err
	} else if r != nil {
		if r.IsResolved() {
			return r, nil
		}
		if !r.ExpiresAt.IsZero() {
			t := time.NewTimer(time.Until(r.ExpiresAt))
			defer t.Stop()
			expiry = t.C
		}
	}

	select {
	case <-ch:
	case <-expiry:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return m.store.GetReviewRequest(id)
}

// Get returns a review request by id (nil, nil when absent).
func (m *Manager) Get(_ context.Context, id string) (*model.ReviewRequest, error) {
	return m.store.GetReviewRequest(id)
}

// ListPending returns pending reviews for a user (userID == "" = all users).
func (m *Manager) ListPending(_ context.Context, userID string) ([]*model.ReviewRequest, error) {
	return m.store.ListPendingReviews(userID)
}

func (m *Manager) wake(id string) {
	m.mu.Lock()
	chans := m.waiters[id]
	delete(m.waiters, id)
	m.mu.Unlock()
	for _, ch := range chans {
		close(ch)
	}
}

func (m *Manager) removeWaiter(id string, ch chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chans := m.waiters[id]
	for i, c := range chans {
		if c == ch {
			m.waiters[id] = append(chans[:i], chans[i+1:]...)
			break
		}
	}
	if len(m.waiters[id]) == 0 {
		delete(m.waiters, id)
	}
}

func (m *Manager) fireListeners(ctx context.Context, r *model.ReviewRequest) {
	m.mu.Lock()
	ls := append([]ResolveListener(nil), m.listeners...)
	m.mu.Unlock()
	for _, l := range ls {
		l(ctx, r)
	}
}

// decisionToStatus maps a free-form decision string to a terminal status. Known
// rejection words map to rejected; any other non-empty decision is treated as
// approval (so custom proceed-style options work), while the raw decision is
// preserved on the request.
func decisionToStatus(decision string) model.ReviewStatus {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "reject", "rejected", "no", "deny", "decline", "cancel", "canceled":
		return model.ReviewRejected
	default:
		return model.ReviewApproved
	}
}
