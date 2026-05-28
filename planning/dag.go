package planning

import (
	"fmt"
)

// ErrDuplicateStepID is returned when two steps share the same ID.
var ErrDuplicateStepID = fmt.Errorf("planning: duplicate step ID")

// ValidateDAG checks that all step dependencies exist and that the graph has no cycles.
// Returns ErrCyclicDependency if a cycle is detected, ErrDuplicateStepID for duplicate IDs,
// or an error for missing dependencies.
func ValidateDAG(steps []*Step) error {
	if len(steps) == 0 {
		return nil
	}
	idToStep := make(map[string]*Step)
	for _, s := range steps {
		if s == nil || s.ID == "" {
			continue
		}
		if _, exists := idToStep[s.ID]; exists {
			return fmt.Errorf("%w: %q", ErrDuplicateStepID, s.ID)
		}
		idToStep[s.ID] = s
	}
	for _, s := range steps {
		if s == nil {
			continue
		}
		for _, depID := range s.DependsOn {
			if _, ok := idToStep[depID]; !ok {
				return fmt.Errorf("planning: step %q depends on unknown step %q", s.ID, depID)
			}
		}
	}
	// DFS cycle detection: white=unvisited, gray=in stack, black=done
	color := make(map[string]int) // 0=white, 1=gray, 2=black
	var visit func(id string) error
	visit = func(id string) error {
		if color[id] == 1 {
			return ErrCyclicDependency
		}
		if color[id] == 2 {
			return nil
		}
		color[id] = 1
		s := idToStep[id]
		if s != nil {
			for _, depID := range s.DependsOn {
				if err := visit(depID); err != nil {
					return err
				}
			}
		}
		color[id] = 2
		return nil
	}
	for id := range idToStep {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

// TopologicalSort returns steps in execution order (dependencies before dependents).
// Calls ValidateDAG first and returns its error if invalid.
func TopologicalSort(steps []*Step) ([]*Step, error) {
	if err := ValidateDAG(steps); err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, nil
	}
	idToStep := make(map[string]*Step)
	for _, s := range steps {
		if s == nil || s.ID == "" {
			continue
		}
		idToStep[s.ID] = s
	}
	// inDegree[id] = number of dependencies (edges pointing to id)
	inDegree := make(map[string]int)
	for id := range idToStep {
		inDegree[id] = 0
	}
	for _, s := range steps {
		if s == nil {
			continue
		}
		inDegree[s.ID] = len(s.DependsOn)
	}
	// reverse[depID] = step IDs that depend on depID (successors of depID)
	reverse := make(map[string][]string)
	for _, s := range steps {
		if s == nil {
			continue
		}
		for _, depID := range s.DependsOn {
			reverse[depID] = append(reverse[depID], s.ID)
		}
	}
	var queue []string
	for id, d := range inDegree {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	var sorted []*Step
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		sorted = append(sorted, idToStep[id])
		for _, nextID := range reverse[id] {
			inDegree[nextID]--
			if inDegree[nextID] == 0 {
				queue = append(queue, nextID)
			}
		}
	}
	return sorted, nil
}

// ReadySteps returns steps that are pending and have all dependencies completed.
func ReadySteps(steps []*Step) []*Step {
	idToStep := make(map[string]*Step)
	for _, s := range steps {
		if s == nil || s.ID == "" {
			continue
		}
		idToStep[s.ID] = s
	}
	var ready []*Step
	for _, s := range steps {
		if s == nil || s.Status != StepPending {
			continue
		}
		allDone := true
		for _, depID := range s.DependsOn {
			dep := idToStep[depID]
			if dep == nil || dep.Status != StepCompleted {
				allDone = false
				break
			}
		}
		if allDone {
			ready = append(ready, s)
		}
	}
	return ready
}
