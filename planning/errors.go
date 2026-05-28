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
)
