package planning

// Observer receives lifecycle events during plan execution.
type Observer interface {
	OnPlanCreated(plan *Plan)
	OnStepStarted(plan *Plan, step *Step)
	OnStepCompleted(plan *Plan, step *Step, result *StepResult)
	OnStepFailed(plan *Plan, step *Step, err error)
	OnPlanCompleted(plan *Plan, result *PlanResult)
	OnPlanFailed(plan *Plan, err error)
}

type multiObserver struct {
	observers []Observer
}

// MultiObserver returns an Observer that forwards all events to the given observers.
func MultiObserver(observers ...Observer) Observer {
	return &multiObserver{observers: observers}
}

func (m *multiObserver) OnPlanCreated(plan *Plan) {
	for _, o := range m.observers {
		o.OnPlanCreated(plan)
	}
}

func (m *multiObserver) OnStepStarted(plan *Plan, step *Step) {
	for _, o := range m.observers {
		o.OnStepStarted(plan, step)
	}
}

func (m *multiObserver) OnStepCompleted(plan *Plan, step *Step, result *StepResult) {
	for _, o := range m.observers {
		o.OnStepCompleted(plan, step, result)
	}
}

func (m *multiObserver) OnStepFailed(plan *Plan, step *Step, err error) {
	for _, o := range m.observers {
		o.OnStepFailed(plan, step, err)
	}
}

func (m *multiObserver) OnPlanCompleted(plan *Plan, result *PlanResult) {
	for _, o := range m.observers {
		o.OnPlanCompleted(plan, result)
	}
}

func (m *multiObserver) OnPlanFailed(plan *Plan, err error) {
	for _, o := range m.observers {
		o.OnPlanFailed(plan, err)
	}
}
