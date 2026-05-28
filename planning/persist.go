package planning

import "context"

// Persister loads and saves the full set of plans (e.g. to DB or file).
// Used by MemoryStore for initial load and graceful shutdown flush.
type Persister interface {
	LoadPlans(ctx context.Context) ([]*Plan, error)
	SavePlans(ctx context.Context, plans []*Plan) error
}
