package workflow

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// LoopID is a validated scoped-loop identifier: non-empty, [A-Za-z0-9_]+. It
// shares the alphabet with TaskID and ParamName because a loop id lives in the
// same global namespace as task ids and param names and must be distinct from
// every one of them.
type LoopID string

// NewLoopID validates s and returns it as a LoopID.
//
// Returns an error if s is empty or contains a character outside [A-Za-z0-9_].
func NewLoopID(s string) (LoopID, error) {
	if !identifierRe.MatchString(s) {
		return "", fmt.Errorf("invalid loop id %q: must match [A-Za-z0-9_]+", s)
	}
	return LoopID(s), nil
}

// LoopGroup is a scoped loop: a named subgraph of Workflow.Tasks that the run
// pipeline re-runs until a convergence target drains.
type LoopGroup struct {
	// ID names the loop; unique across loops and distinct from every task id and
	// param name.
	ID LoopID
	// Description is shown in plan output; not sent to the model.
	Description string
	// UntilEmpty names the member task whose empty trimmed output ends the loop.
	// Set when the loop converges by emptiness; "" when Until is used. Exactly
	// one of UntilEmpty / Until is set.
	UntilEmpty TaskID
	// Until holds the raw when-style convergence expression over member outputs.
	// "" when UntilEmpty is used.
	Until string
	// Cond is the compiled form of Until, nil when UntilEmpty is used.
	Cond *Condition
	// Max caps the number of iterations. Always >= 1 in a parsed Workflow.
	Max int
	// Members lists the loop's member task ids in declaration order.
	Members []TaskID
}

// rawLoop is the decoded-but-unvalidated form of a single `loops:` entry. The
// has* flags record key presence so the parser can enforce that exactly one of
// until_empty / until is set (a present-but-empty value still counts as set).
type rawLoop struct {
	id            LoopID
	description   string
	untilEmpty    string
	hasUntilEmpty bool
	until         string
	hasUntil      bool
	max           int
	tasks         []rawTask
}

// decodeLoopBody decodes a `loop:` block's fields from a mapping node into rl.
// The loop's id and description come from the wrapping task, not the block, so
// only the convergence/iteration fields (until_empty, until, max, tasks) are
// recognized here; any other key (including id or description) is an unknown
// field. Cross-task validation (collisions, uniqueness, connectivity,
// convergence) happens in Parse once the full task set is known.
func decodeLoopBody(entry *yaml.Node, rl *rawLoop) error {
	if entry.Kind != yaml.MappingNode {
		return errors.New("loop: must be a mapping")
	}
	for i := 0; i+1 < len(entry.Content); i += 2 {
		k, v := entry.Content[i], entry.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return errors.New("loop: key must be a scalar")
		}
		switch k.Value {
		case "until_empty":
			if err := v.Decode(&rl.untilEmpty); err != nil {
				return fmt.Errorf("loop: until_empty: %w", err)
			}
			rl.hasUntilEmpty = true
		case "until":
			if err := v.Decode(&rl.until); err != nil {
				return fmt.Errorf("loop: until: %w", err)
			}
			rl.hasUntil = true
		case "max":
			if err := v.Decode(&rl.max); err != nil {
				return fmt.Errorf("loop: max: %w", err)
			}
		case "tasks":
			if err := v.Decode(&rl.tasks); err != nil {
				return fmt.Errorf("loop: tasks: %w", err)
			}
		default:
			return &UnknownLoopGroupFieldError{Field: k.Value}
		}
	}
	return nil
}

// loopConnected reports whether the induced subgraph over members is weakly
// connected: treating each member-to-member depends_on edge as undirected, a
// single BFS from the first member must reach every member. A loop with one
// member is trivially connected. depsByID maps each task id to its declared
// dependencies; edges to non-members are ignored.
func loopConnected(members []TaskID, depsByID map[TaskID][]TaskID) bool {
	if len(members) <= 1 {
		return true
	}
	memberSet := make(map[TaskID]bool, len(members))
	for _, m := range members {
		memberSet[m] = true
	}
	adj := make(map[TaskID][]TaskID, len(members))
	for _, m := range members {
		for _, d := range depsByID[m] {
			if memberSet[d] {
				adj[m] = append(adj[m], d)
				adj[d] = append(adj[d], m)
			}
		}
	}
	seen := make(map[TaskID]bool, len(members))
	queue := []TaskID{members[0]}
	seen[members[0]] = true
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, x := range adj[n] {
			if !seen[x] {
				seen[x] = true
				queue = append(queue, x)
			}
		}
	}
	return len(seen) == len(memberSet)
}

// buildLoopGroups validates each rawLoop against the task set and returns the
// resolved LoopGroups in declaration order, plus a per-loop member set used by
// the prev-placeholder check. Each loop must declare a non-empty body, set
// exactly one of until_empty / until, carry max >= 1, name a member as its
// convergence target, and induce a weakly connected member subgraph.
//
// Loop id collisions and uniqueness are checked by the caller (they need the
// full task and param sets); this function assumes ids are already distinct.
func buildLoopGroups(rawLoops []rawLoop, depsByID map[TaskID][]TaskID) ([]LoopGroup, map[LoopID]map[TaskID]bool, error) {
	loops := make([]LoopGroup, 0, len(rawLoops))
	memberByLoop := make(map[LoopID]map[TaskID]bool, len(rawLoops))
	for _, rl := range rawLoops {
		if len(rl.tasks) == 0 {
			return nil, nil, &EmptyLoopError{Loop: rl.id}
		}
		// Exactly one convergence key: both set or neither set is an error.
		if rl.hasUntilEmpty == rl.hasUntil {
			return nil, nil, &LoopConvergenceError{Loop: rl.id}
		}
		if rl.max < 1 {
			return nil, nil, &InvalidLoopGroupMaxError{Loop: rl.id, Max: rl.max}
		}

		members := make([]TaskID, 0, len(rl.tasks))
		memberSet := make(map[TaskID]bool, len(rl.tasks))
		for _, rt := range rl.tasks {
			tid := TaskID(rt.ID)
			members = append(members, tid)
			memberSet[tid] = true
		}
		memberByLoop[rl.id] = memberSet

		lg := LoopGroup{ID: rl.id, Description: rl.description, Max: rl.max, Members: members}
		if rl.hasUntilEmpty {
			target := TaskID(rl.untilEmpty)
			if !memberSet[target] {
				return nil, nil, &LoopTargetNotMemberError{Loop: rl.id, Task: target}
			}
			lg.UntilEmpty = target
		} else {
			// The until expression converges over member outputs only: bounding
			// ParseCondition by the member set rejects a reference to any task
			// outside the loop at load time.
			known := make(map[TaskID]bool, len(memberSet))
			for m := range memberSet {
				known[m] = true
			}
			cond, err := ParseCondition(rl.until, known)
			if err != nil {
				// A reference to a non-member surfaces as the same structured
				// LoopTargetNotMemberError the until_empty path uses; genuine
				// syntax errors stay wrapped as condition-parse failures.
				var unknownRef *UnknownConditionRefError
				if errors.As(err, &unknownRef) {
					return nil, nil, &LoopTargetNotMemberError{Loop: rl.id, Task: TaskID(unknownRef.Ref)}
				}
				return nil, nil, fmt.Errorf("loop %q: %w", rl.id, err)
			}
			lg.Until = rl.until
			lg.Cond = cond
		}

		if !loopConnected(members, depsByID) {
			return nil, nil, &DisconnectedLoopError{Loop: rl.id}
		}
		loops = append(loops, lg)
	}
	return loops, memberByLoop, nil
}

// checkPrevPlaceholders enforces that every `{{prev.id}}` placeholder appears
// only inside a loop body task and references a member of that same loop. A
// prev reference names the prior iteration's output of a sibling member, so it
// is meaningless outside a loop and may never cross a loop boundary.
func checkPrevPlaceholders(wf *Workflow, memberByLoop map[LoopID]map[TaskID]bool) error {
	for i := range wf.Tasks {
		t := &wf.Tasks[i]
		body := t.Prompt
		if t.IsShell() {
			body = t.Command
		}
		for _, m := range prevPlaceholderRe.FindAllStringSubmatch(body, -1) {
			name := m[1]
			if t.Loop == "" {
				return &PrevOutsideLoopError{Task: t.ID, Name: name}
			}
			if !memberByLoop[t.Loop][TaskID(name)] {
				return &PrevNotMemberError{Task: t.ID, Loop: t.Loop, Name: name}
			}
		}
	}
	return nil
}

// LoopIDCollisionError reports a loop id that collides with a task id or param
// name. Kind is "task" or "param".
type LoopIDCollisionError struct {
	Loop LoopID
	Kind string
}

func (e *LoopIDCollisionError) Error() string {
	return fmt.Sprintf("loop %q: id collides with a %s name", e.Loop, e.Kind)
}

// DuplicateLoopIDError reports two loops declaring the same id.
type DuplicateLoopIDError struct{ Loop LoopID }

func (e *DuplicateLoopIDError) Error() string {
	return fmt.Sprintf("duplicate loop id %q", e.Loop)
}

// EmptyLoopError reports a loop that declares no body tasks.
type EmptyLoopError struct{ Loop LoopID }

func (e *EmptyLoopError) Error() string {
	return fmt.Sprintf("loop %q: has no tasks", e.Loop)
}

// DisconnectedLoopError reports a loop whose member subgraph is not weakly
// connected: the induced graph over the loop's members splits into more than
// one component.
type DisconnectedLoopError struct{ Loop LoopID }

func (e *DisconnectedLoopError) Error() string {
	return fmt.Sprintf("loop %q: member subgraph is not weakly connected", e.Loop)
}

// LoopTargetNotMemberError reports a loop convergence target (until_empty task
// or until reference) that is not a member of the loop.
type LoopTargetNotMemberError struct {
	Loop LoopID
	Task TaskID
}

func (e *LoopTargetNotMemberError) Error() string {
	return fmt.Sprintf("loop %q: convergence target %q is not a member", e.Loop, e.Task)
}

// InvalidLoopGroupMaxError reports a loop `max` below 1. Max caps the iteration
// count and must permit at least one pass.
type InvalidLoopGroupMaxError struct {
	Loop LoopID
	Max  int
}

func (e *InvalidLoopGroupMaxError) Error() string {
	return fmt.Sprintf("loop %q: invalid max %d: must be >= 1", e.Loop, e.Max)
}

// LoopConvergenceError reports a loop that does not set exactly one of
// until_empty or until (it sets both, or neither).
type LoopConvergenceError struct{ Loop LoopID }

func (e *LoopConvergenceError) Error() string {
	return fmt.Sprintf("loop %q: must set exactly one of until_empty or until", e.Loop)
}

// UnknownLoopGroupFieldError reports a key inside a `loop:` block that is not
// one of until_empty|until|max|tasks (id and description come from the wrapping
// task, not the block).
type UnknownLoopGroupFieldError struct{ Field string }

func (e *UnknownLoopGroupFieldError) Error() string {
	return fmt.Sprintf("loop: unknown field %q", e.Field)
}

// PrevOutsideLoopError reports a `{{prev.id}}` placeholder in a task that is not
// a loop body task; prev references the prior iteration of a scoped loop and is
// meaningless outside one.
type PrevOutsideLoopError struct {
	Task TaskID
	Name string
}

func (e *PrevOutsideLoopError) Error() string {
	return fmt.Sprintf("task %q: placeholder {{prev.%s}} is only allowed inside a loop body task", e.Task, e.Name)
}

// PrevNotMemberError reports a `{{prev.id}}` placeholder that references a task
// which is not a member of the body task's own loop.
type PrevNotMemberError struct {
	Task TaskID
	Loop LoopID
	Name string
}

func (e *PrevNotMemberError) Error() string {
	return fmt.Sprintf("task %q: placeholder {{prev.%s}} does not reference a member of loop %q", e.Task, e.Name, e.Loop)
}
