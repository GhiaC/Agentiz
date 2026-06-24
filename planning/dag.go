package planning

import (
	"fmt"
	"strings"
)

// normalizeOperator canonicalizes a condition operator for lookup/comparison.
func normalizeOperator(op string) string {
	return strings.ToLower(strings.TrimSpace(op))
}

// ErrDuplicateStepID is returned when two steps share the same ID.
var ErrDuplicateStepID = fmt.Errorf("planning: duplicate step ID")

// knownConditionOperators is the comparison DSL accepted by conditional steps
// (see compareValues). The empty operator and "truthy" mean a truthiness check
// on the operand.
var knownConditionOperators = map[string]bool{
	"": true, "truthy": true,
	"eq": true, "==": true, "equals": true,
	"ne": true, "!=": true, "not_equals": true,
	"contains": true, "not_contains": true,
	"empty": true, "not_empty": true,
	"gt": true, ">": true, "lt": true, "<": true,
	"gte": true, ">=": true, "lte": true, "<=": true,
}

// ValidatePlan validates a plan before execution: DAG shape (via ValidateDAG)
// plus per-step-type required fields — tool_call needs a tool_name and
// conditional steps must use a known operator. A conditional MAY run inside a
// parallel block (the runner gives each branch an isolated plan-slice view, see
// runParallelStep), but only when its branch targets stay inside that block,
// depend on it, and are gated by a single conditional — validateParallelBranches
// enforces those invariants. Both planners and LocalRunner.Run call this so
// invalid plans fail before any step executes.
func ValidatePlan(plan *Plan) error {
	if plan == nil {
		return fmt.Errorf("%w: nil plan", ErrInvalidStep)
	}
	if err := ValidateDAG(plan.Steps); err != nil {
		return err
	}
	return validateStepConfigs(plan.Steps)
}

func validateStepConfigs(steps []*Step) error {
	for _, s := range steps {
		if s == nil {
			continue
		}
		switch s.Type {
		case StepToolCall:
			if s.Config.ToolName == "" {
				return fmt.Errorf("%w: tool_call step %q has empty tool_name", ErrInvalidStep, s.ID)
			}
		case StepConditional:
			if s.Condition != nil && !knownConditionOperators[normalizeOperator(s.Condition.Operator)] {
				return fmt.Errorf("%w: conditional step %q has unknown operator %q", ErrInvalidStep, s.ID, s.Condition.Operator)
			}
		case StepParallel:
			if err := validateParallelBranches(s.ID, s.Config.SubSteps); err != nil {
				return err
			}
			if err := validateStepConfigs(s.Config.SubSteps); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateParallelBranches enforces the invariants that let a parallel block run
// as an isolated mini-plan over its own sub-step slice (runParallelStep). Because
// each block resolves dependencies and propagates skips only within that slice:
//
//   - every sub-step's DependsOn must reference another sub-step of the SAME
//     block — a dependency outside the block could never resolve, so the
//     sub-step would silently never run (and the block would report success
//     while dropping its output);
//
// and additionally, for every step a conditional sub-step branches to:
//
//  1. in-block: the target is one of the block's own sub-steps, so the
//     conditional can never reach across to a sibling branch or the parent plan;
//  2. gated by a dependency: the target depends on the conditional, so it is
//     never ready — and thus never running in another goroutine — at the same
//     time the conditional flips its status to skipped;
//  3. single owner: no target is gated by two different conditionals, so no two
//     goroutines ever write the same step's status.
//
// Together these make conditional-in-parallel race-free by construction rather
// than by locking.
func validateParallelBranches(parallelID string, subs []*Step) error {
	inBlock := make(map[string]*Step, len(subs))
	for _, s := range subs {
		if s != nil && s.ID != "" {
			inBlock[s.ID] = s
		}
	}
	// Every sub-step's dependencies must stay inside the block (see above).
	for _, s := range subs {
		if s == nil {
			continue
		}
		for _, depID := range s.DependsOn {
			if _, ok := inBlock[depID]; !ok {
				return fmt.Errorf("%w: sub-step %q in parallel %q depends on %q, which is not a sub-step of the same parallel block",
					ErrInvalidStep, s.ID, parallelID, depID)
			}
		}
	}
	gatedBy := make(map[string]string) // target ID -> conditional ID that gates it
	for _, s := range subs {
		if s == nil || s.Type != StepConditional {
			continue
		}
		for _, ids := range s.Branches {
			for _, id := range ids {
				target, ok := inBlock[id]
				if !ok {
					return fmt.Errorf("%w: conditional sub-step %q in parallel %q branches to %q, which is not a sub-step of the same parallel block",
						ErrInvalidStep, s.ID, parallelID, id)
				}
				if owner, dup := gatedBy[id]; dup && owner != s.ID {
					return fmt.Errorf("%w: sub-step %q in parallel %q is gated by two conditionals (%q and %q)",
						ErrInvalidStep, id, parallelID, owner, s.ID)
				}
				gatedBy[id] = s.ID
				if !stepDependsOn(target, s.ID) {
					return fmt.Errorf("%w: sub-step %q in parallel %q is a branch of conditional %q but does not depend on it",
						ErrInvalidStep, id, parallelID, s.ID)
				}
			}
		}
	}
	return nil
}

// stepDependsOn reports whether s lists id in its DependsOn.
func stepDependsOn(s *Step, id string) bool {
	for _, d := range s.DependsOn {
		if d == id {
			return true
		}
	}
	return false
}

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

// ReadySteps returns steps that are pending and whose dependencies are all
// resolved. A dependency counts as resolved when it has completed OR was
// skipped (e.g. by a conditional that did not select its branch); a skipped
// dependency simply contributes no output to the dependent step.
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
			if dep == nil || (dep.Status != StepCompleted && dep.Status != StepSkipped) {
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

// PropagateSkips marks as skipped any pending step that has at least one
// dependency and whose dependencies were ALL skipped — it has no surviving input
// path, so it cannot meaningfully run. A step that still has a completed
// dependency is left runnable (e.g. a collect/join after a conditional, which
// aggregates whichever branches survived). It iterates to a fixed point so skips
// cascade through a non-selected sub-DAG, and returns the number skipped.
func PropagateSkips(steps []*Step) int {
	idToStep := make(map[string]*Step)
	for _, s := range steps {
		if s != nil && s.ID != "" {
			idToStep[s.ID] = s
		}
	}
	total := 0
	for {
		changed := 0
		for _, s := range steps {
			if s == nil || s.Status != StepPending || len(s.DependsOn) == 0 {
				continue
			}
			allSkipped := true
			for _, depID := range s.DependsOn {
				dep := idToStep[depID]
				if dep == nil || dep.Status != StepSkipped {
					allSkipped = false
					break
				}
			}
			if allSkipped {
				s.Status = StepSkipped
				changed++
			}
		}
		total += changed
		if changed == 0 {
			break
		}
	}
	return total
}
