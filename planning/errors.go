package planning

import "errors"

var (
	ErrPlanNotFound     = errors.New("planning: plan not found")
	ErrStepFailed       = errors.New("planning: step failed")
	ErrCyclicDependency = errors.New("planning: cyclic dependency detected")
	ErrStepTimeout      = errors.New("planning: step timed out")
	ErrPlanCancelled    = errors.New("planning: plan cancelled")
	ErrNoPlanStore      = errors.New("planning: no plan store configured")
	ErrInvalidStep      = errors.New("planning: invalid step configuration")
	// ErrStepWaiting is returned by a step that suspended itself awaiting an async
	// human review. It is not a failure: the runner stops and the plan waits.
	ErrStepWaiting = errors.New("planning: step suspended awaiting review")
	// ErrPlanWaiting is returned by Run/Resume when the plan is suspended awaiting
	// a human review. Callers should treat it as "suspended, will resume on
	// resolve", not as a failure (it does NOT trigger replan-on-failure).
	ErrPlanWaiting = errors.New("planning: plan suspended awaiting review")
)
