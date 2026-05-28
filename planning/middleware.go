package planning

import (
	"context"
	"time"

	"github.com/ghiac/agentize/log"
)

// MetricsCollector records plan and step metrics for observability.
type MetricsCollector interface {
	RecordPlanDuration(planID string, d time.Duration)
	RecordStepDuration(planID, stepID string, d time.Duration)
	IncrementPlanCount(status PlanStatus)
}

type loggingRunner struct {
	Runner
	logger *log.Logger
}

// WithLogging wraps a Runner to log before and after Run and RunStep.
func WithLogging(r Runner, logger *log.Logger) Runner {
	if logger == nil {
		logger = log.Log
	}
	return &loggingRunner{Runner: r, logger: logger}
}

func (l *loggingRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	l.logger.Infof("planning: run plan %s", plan.ID)
	result, err := l.Runner.Run(ctx, plan, opts...)
	if err != nil {
		l.logger.Errorf("planning: run plan %s failed: %v", plan.ID, err)
		return nil, err
	}
	l.logger.Infof("planning: run plan %s completed", plan.ID)
	return result, nil
}

func (l *loggingRunner) RunStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	l.logger.Infof("planning: run step %s of plan %s", step.ID, plan.ID)
	result, err := l.Runner.RunStep(ctx, plan, step)
	if err != nil {
		l.logger.Errorf("planning: run step %s failed: %v", step.ID, err)
		return nil, err
	}
	return result, nil
}

func (l *loggingRunner) Cancel(ctx context.Context, planID string) error {
	l.logger.Infof("planning: cancel plan %s", planID)
	return l.Runner.Cancel(ctx, planID)
}

func (l *loggingRunner) Resume(ctx context.Context, planID string) (*PlanResult, error) {
	l.logger.Infof("planning: resume plan %s", planID)
	return l.Runner.Resume(ctx, planID)
}

type retryRunner struct {
	Runner
	maxRetries int
}

// WithRetry wraps a Runner to retry Run on failure up to maxRetries times.
func WithRetry(r Runner, maxRetries int) Runner {
	return &retryRunner{Runner: r, maxRetries: maxRetries}
}

func (r *retryRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	var lastErr error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		result, err := r.Runner.Run(ctx, plan, opts...)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

type metricsRunner struct {
	Runner
	collector MetricsCollector
}

// WithMetrics wraps a Runner to record duration and plan count metrics.
func WithMetrics(r Runner, collector MetricsCollector) Runner {
	return &metricsRunner{Runner: r, collector: collector}
}

func (m *metricsRunner) Run(ctx context.Context, plan *Plan, opts ...RunOption) (*PlanResult, error) {
	start := time.Now()
	result, err := m.Runner.Run(ctx, plan, opts...)
	d := time.Since(start)
	if m.collector != nil {
		m.collector.RecordPlanDuration(plan.ID, d)
		if err != nil {
			m.collector.IncrementPlanCount(PlanFailed)
		} else {
			m.collector.IncrementPlanCount(PlanCompleted)
		}
	}
	return result, err
}

func (m *metricsRunner) RunStep(ctx context.Context, plan *Plan, step *Step) (*StepResult, error) {
	start := time.Now()
	result, err := m.Runner.RunStep(ctx, plan, step)
	if m.collector != nil {
		m.collector.RecordStepDuration(plan.ID, step.ID, time.Since(start))
	}
	return result, err
}
