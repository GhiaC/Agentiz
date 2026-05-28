package planning

import "context"

// PlanStore persists and retrieves plans.
type PlanStore interface {
	Save(ctx context.Context, plan *Plan) error
	Get(ctx context.Context, planID string) (*Plan, error)
	Update(ctx context.Context, plan *Plan) error
	List(ctx context.Context, userID string) ([]*Plan, error)
	Delete(ctx context.Context, planID string) error
}

// PlanStoreWithShutdown is a PlanStore that can flush and close on shutdown.
// MemoryStore implements this when created with WithPersister.
type PlanStoreWithShutdown interface {
	PlanStore
	Shutdown(ctx context.Context) error
}
