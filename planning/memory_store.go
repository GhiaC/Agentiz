package planning

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// MemoryStoreOption configures a MemoryStore.
type MemoryStoreOption func(*MemoryStore)

// WithPersister sets a persister for load on startup and save on Shutdown.
func WithPersister(p Persister) MemoryStoreOption {
	return func(m *MemoryStore) { m.persister = p }
}

// MemoryStore is an in-memory implementation of PlanStore.
// Optionally uses a Persister for initial load and graceful shutdown flush.
type MemoryStore struct {
	mu        sync.RWMutex
	plans     map[string]*Plan
	persister Persister
	closed    bool
}

// NewMemoryStore returns a new in-memory plan store. Use WithPersister for DB/file load and shutdown save.
func NewMemoryStore(opts ...MemoryStoreOption) *MemoryStore {
	m := &MemoryStore{plans: make(map[string]*Plan)}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// LoadFromPersister loads all plans from the persister into memory. Call once after NewMemoryStore when using WithPersister.
func (m *MemoryStore) LoadFromPersister(ctx context.Context) error {
	if m.persister == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	plans, err := m.persister.LoadPlans(ctx)
	if err != nil {
		return err
	}
	for _, p := range plans {
		if p != nil && p.ID != "" {
			m.plans[p.ID] = p
		}
	}
	return nil
}

// Shutdown flushes all plans to the persister (if set) and marks the store closed. Implements PlanStoreWithShutdown.
func (m *MemoryStore) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	var plans []*Plan
	for _, p := range m.plans {
		if p != nil {
			copy, err := m.deepCopy(p)
			if err != nil {
				m.mu.Unlock()
				return err
			}
			plans = append(plans, copy)
		}
	}
	m.mu.Unlock()
	if m.persister != nil && len(plans) > 0 {
		return m.persister.SavePlans(ctx, plans)
	}
	return nil
}

func (m *MemoryStore) deepCopy(p *Plan) (*Plan, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var out Plan
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *MemoryStore) checkClosed() bool {
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	return closed
}

// Save stores a new plan. Returns an error if a plan with the same ID already exists.
func (m *MemoryStore) Save(ctx context.Context, plan *Plan) error {
	if plan == nil {
		return nil
	}
	if m.checkClosed() {
		return fmt.Errorf("planning: store is closed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("planning: store is closed")
	}
	if _, exists := m.plans[plan.ID]; exists {
		return fmt.Errorf("planning: plan already exists: %s", plan.ID)
	}
	copy, err := m.deepCopy(plan)
	if err != nil {
		return err
	}
	m.plans[plan.ID] = copy
	return nil
}

// Get returns a plan by ID, or ErrPlanNotFound if not found.
func (m *MemoryStore) Get(ctx context.Context, planID string) (*Plan, error) {
	if m.checkClosed() {
		return nil, ErrPlanNotFound
	}
	m.mu.RLock()
	p, ok := m.plans[planID]
	m.mu.RUnlock()
	if !ok || p == nil {
		return nil, ErrPlanNotFound
	}
	return m.deepCopy(p)
}

// Update updates an existing plan. Returns ErrPlanNotFound if the plan does not exist.
func (m *MemoryStore) Update(ctx context.Context, plan *Plan) error {
	if plan == nil {
		return nil
	}
	if m.checkClosed() {
		return fmt.Errorf("planning: store is closed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("planning: store is closed")
	}
	if _, exists := m.plans[plan.ID]; !exists {
		return ErrPlanNotFound
	}
	copy, err := m.deepCopy(plan)
	if err != nil {
		return err
	}
	copy.UpdatedAt = time.Now()
	m.plans[plan.ID] = copy
	return nil
}

// List returns plans for the given userID. If userID is empty, returns all plans.
func (m *MemoryStore) List(ctx context.Context, userID string) ([]*Plan, error) {
	if m.checkClosed() {
		return nil, nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*Plan
	for _, p := range m.plans {
		if p == nil {
			continue
		}
		if userID == "" || p.UserID == userID {
			copy, err := m.deepCopy(p)
			if err != nil {
				return nil, err
			}
			out = append(out, copy)
		}
	}
	return out, nil
}

// Delete removes a plan. Returns ErrPlanNotFound if the plan does not exist.
func (m *MemoryStore) Delete(ctx context.Context, planID string) error {
	if m.checkClosed() {
		return fmt.Errorf("planning: store is closed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("planning: store is closed")
	}
	if _, exists := m.plans[planID]; !exists {
		return ErrPlanNotFound
	}
	delete(m.plans, planID)
	return nil
}
