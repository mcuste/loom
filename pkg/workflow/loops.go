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

// LoopKind discriminates the two scoped-block loop forms: a while-style loop
// that re-runs its body until a convergence target drains (LoopWhile) and a
// for_each loop that iterates a finite list once per element (LoopForEach).
type LoopKind int

const (
	// LoopWhile is the until_empty/until convergence loop. Its zero value makes
	// it the default for a `loop:` block.
	LoopWhile LoopKind = iota
	// LoopForEach iterates a finite list (static List or dynamic ListSource)
	// once per element, binding each element to As. It has no convergence target
	// or iteration cap.
	LoopForEach
)

// LoopGroup is a scoped loop: a named subgraph of Workflow.Tasks. A LoopWhile
// group re-runs its body until a convergence target drains; a LoopForEach group
// runs its body once per element of a finite list.
type LoopGroup struct {
	// ID names the loop; unique across loops and distinct from every task id and
	// param name.
	ID LoopID
	// Description is shown in plan output; not sent to the model.
	Description string
	// Kind selects the loop form. UntilEmpty/Until/Cond/Max are meaningful only
	// for LoopWhile; List/ListSource/As only for LoopForEach.
	Kind LoopKind
	// Parallel runs a LoopForEach body once per element concurrently rather than
	// sequentially: every element's pass dispatches at the same time, each over
	// an isolated copy of the member outputs, so no element observes another's
	// body results. Always false for a LoopWhile (whose passes are inherently
	// ordered) and for a sequential for_each. A parallel for_each body may not
	// use `{{prev.id}}`: there is no prior iteration to read.
	Parallel bool
	// UntilEmpty names the member task whose empty trimmed output ends the loop.
	// Set when the loop converges by emptiness; "" when Until is used. Exactly
	// one of UntilEmpty / Until is set. LoopWhile only.
	UntilEmpty TaskID
	// Until holds the raw when-style convergence expression over member outputs.
	// "" when UntilEmpty is used. LoopWhile only.
	Until string
	// Cond is the compiled form of Until, nil when UntilEmpty is used. LoopWhile
	// only.
	Cond *Condition
	// Max caps the number of iterations. Always >= 1 in a parsed LoopWhile; 0 for
	// a LoopForEach (its pass count is len(List) / the resolved ListSource).
	Max int
	// List holds the literal values of a static for_each (`in: [a, b]`). Non-nil
	// (possibly empty) for a static LoopForEach; nil otherwise. Mutually
	// exclusive with ListSource. LoopForEach only.
	List []string
	// ListSource is the single `{{...}}` placeholder of a dynamic for_each
	// (`in: "{{discover}}"`); "" for a static or non-for_each loop. The executor
	// substitutes it, then parses the result as a list. LoopForEach only.
	ListSource string
	// As names the per-iteration loop variable bound to each element and
	// referenced as {{As}} in member bodies. Set for LoopForEach; "" otherwise.
	// Never collides with a task id or param name.
	As string
	// Members lists the loop's member task ids in declaration order.
	Members []TaskID
}

// rawLoop is the decoded-but-unvalidated form of a single loop block (`loop:`
// or `for_each:`). The has* flags record key presence so the parser can enforce
// that exactly one of until_empty / until is set (a present-but-empty value
// still counts as set), and whether `in` was a static list versus a dynamic
// source.
type rawLoop struct {
	id            LoopID
	description   string
	kind          LoopKind
	parallel      bool
	untilEmpty    string
	hasUntilEmpty bool
	until         string
	hasUntil      bool
	max           int
	list          []string
	hasList       bool
	listSource    string
	as            string
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
	return eachMapEntry(entry, "loop:", func(key string, v *yaml.Node) error {
		switch key {
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
			return &UnknownLoopGroupFieldError{Field: key}
		}
		return nil
	})
}

// decodeForEachBody decodes a `for_each:` block's fields from a mapping node
// into rl. The loop's id and description come from the wrapping task, not the
// block, so only in / as / tasks are recognized here; any other key is an
// unknown field. `in` is a sequence (static list -> List) or a scalar holding
// exactly one `{{...}}` placeholder (dynamic source -> ListSource). Cross-task
// validation (the `as` alphabet/collision, list-ref existence) happens in
// buildLoopGroups once the full task set is known.
func decodeForEachBody(entry *yaml.Node, rl *rawLoop) error {
	if entry.Kind != yaml.MappingNode {
		return errors.New("for_each: must be a mapping")
	}
	hasIn := false
	if err := eachMapEntry(entry, "for_each:", func(key string, v *yaml.Node) error {
		switch key {
		case "in":
			hasIn = true
			switch v.Kind {
			case yaml.SequenceNode:
				vals := make([]string, 0, len(v.Content))
				for _, item := range v.Content {
					if item.Kind != yaml.ScalarNode {
						return errors.New("for_each: in list entries must be scalars")
					}
					vals = append(vals, item.Value)
				}
				rl.list = vals
				rl.hasList = true
			case yaml.ScalarNode:
				taskRefs, paramRefs, stateRefs := scanPlaceholders(v.Value)
				if len(taskRefs)+len(paramRefs)+len(stateRefs) != 1 {
					return &InvalidForEachListError{Loop: rl.id, Source: v.Value}
				}
				rl.listSource = v.Value
			default:
				return &InvalidForEachListError{Loop: rl.id, Source: v.Value}
			}
		case "as":
			if err := v.Decode(&rl.as); err != nil {
				return fmt.Errorf("for_each: as: %w", err)
			}
		case "tasks":
			if err := v.Decode(&rl.tasks); err != nil {
				return fmt.Errorf("for_each: tasks: %w", err)
			}
		default:
			return &UnknownForEachFieldError{Field: key}
		}
		return nil
	}); err != nil {
		return err
	}
	if !hasIn {
		return &MissingForEachListError{Loop: rl.id}
	}
	return nil
}

// ListSourceTaskRef returns the task id a dynamic for_each `in:` source
// references, or ("", false) when source is empty or references a param or
// state key (which create no DAG edge). The source holds at most one
// placeholder, validated at parse time, so the executor can use this to add the
// source task as a loop entry dependency.
func ListSourceTaskRef(source string) (TaskID, bool) {
	if source == "" {
		return "", false
	}
	if taskRefs, _, _ := scanPlaceholders(source); len(taskRefs) == 1 {
		return TaskID(taskRefs[0]), true
	}
	return "", false
}

// buildLoopGroups validates each rawLoop against the task set and returns the
// resolved LoopGroups in declaration order, plus a per-loop member set used by
// the prev-placeholder check. Every loop must declare a non-empty body. The body
// is otherwise an ordinary DAG: members carry their own depends_on edges and
// need not be connected to one another, so independent members run in parallel
// within a pass exactly like independent top-level tasks. A LoopWhile must
// additionally set exactly one of until_empty / until, carry max >= 1, and name
// a member as its convergence target. A LoopForEach must declare a valid `as`
// loop variable (alphabet-clean, not colliding with a task id or param name) and
// an `in` that resolves to a known task/param when dynamic.
//
// Loop id collisions and uniqueness are checked by the caller (they need the
// full task and param sets); this function assumes ids are already distinct.
// ids and params are used only for the for_each `as`/`in` collision and
// existence checks.
func buildLoopGroups(rawLoops []rawLoop, ids map[TaskID]struct{}, params map[ParamName]struct{}) ([]LoopGroup, map[LoopID]map[TaskID]bool, error) {
	loops := make([]LoopGroup, 0, len(rawLoops))
	memberByLoop := make(map[LoopID]map[TaskID]bool, len(rawLoops))
	for _, rl := range rawLoops {
		if len(rl.tasks) == 0 {
			return nil, nil, &EmptyLoopError{Loop: rl.id}
		}

		members := make([]TaskID, 0, len(rl.tasks))
		memberSet := make(map[TaskID]bool, len(rl.tasks))
		for _, rt := range rl.tasks {
			tid := TaskID(rt.ID)
			members = append(members, tid)
			memberSet[tid] = true
		}
		memberByLoop[rl.id] = memberSet

		lg := LoopGroup{ID: rl.id, Description: rl.description, Kind: rl.kind, Parallel: rl.parallel, Members: members}
		switch rl.kind {
		case LoopForEach:
			if err := resolveForEach(&lg, rl, ids, params); err != nil {
				return nil, nil, err
			}
		default:
			if err := resolveWhile(&lg, rl, memberSet); err != nil {
				return nil, nil, err
			}
		}

		loops = append(loops, lg)
	}
	return loops, memberByLoop, nil
}

// resolveWhile validates and fills the LoopWhile-only fields of lg: exactly one
// of until_empty / until, max >= 1, and a convergence target that names a
// member.
func resolveWhile(lg *LoopGroup, rl rawLoop, memberSet map[TaskID]bool) error {
	// Exactly one convergence key: both set or neither set is an error.
	if rl.hasUntilEmpty == rl.hasUntil {
		return &LoopConvergenceError{Loop: rl.id}
	}
	if rl.max < 1 {
		return &InvalidLoopGroupMaxError{Loop: rl.id, Max: rl.max}
	}
	lg.Max = rl.max
	if rl.hasUntilEmpty {
		target := TaskID(rl.untilEmpty)
		if !memberSet[target] {
			return &LoopTargetNotMemberError{Loop: rl.id, Task: target}
		}
		lg.UntilEmpty = target
		return nil
	}
	// The until expression converges over member outputs only: bounding
	// ParseCondition by the member set rejects a reference to any task outside
	// the loop at load time.
	known := make(map[TaskID]bool, len(memberSet))
	for m := range memberSet {
		known[m] = true
	}
	cond, err := ParseCondition(rl.until, known)
	if err != nil {
		// A reference to a non-member surfaces as the same structured
		// LoopTargetNotMemberError the until_empty path uses; genuine syntax
		// errors stay wrapped as condition-parse failures.
		var unknownRef *UnknownConditionRefError
		if errors.As(err, &unknownRef) {
			return &LoopTargetNotMemberError{Loop: rl.id, Task: TaskID(unknownRef.Ref)}
		}
		return fmt.Errorf("loop %q: %w", rl.id, err)
	}
	lg.Until = rl.until
	lg.Cond = cond
	return nil
}

// resolveForEach validates and fills the LoopForEach-only fields of lg: a valid
// `as` loop variable that does not collide with a task id or param name, and an
// `in` that is either a static list or a dynamic single-placeholder source
// referencing a known task/param.
func resolveForEach(lg *LoopGroup, rl rawLoop, ids map[TaskID]struct{}, params map[ParamName]struct{}) error {
	if rl.as == "" {
		return &MissingForEachVarError{Loop: rl.id}
	}
	if !identifierRe.MatchString(rl.as) {
		return &InvalidForEachVarError{Loop: rl.id, As: rl.as}
	}
	if _, ok := ids[TaskID(rl.as)]; ok {
		return &ForEachVarCollisionError{Loop: rl.id, As: rl.as, Kind: "task"}
	}
	if _, ok := params[ParamName(rl.as)]; ok {
		return &ForEachVarCollisionError{Loop: rl.id, As: rl.as, Kind: "param"}
	}
	lg.As = rl.as
	if rl.hasList {
		lg.List = rl.list
		return nil
	}
	// Dynamic source: a `{{id}}` source must name a known task and a
	// `{{params.x}}` source a declared param; a `{{state.x}}` source needs
	// neither (it resolves against cross-run state at run time).
	taskRefs, paramRefs, _ := scanPlaceholders(rl.listSource)
	if len(taskRefs) == 1 {
		if _, ok := ids[TaskID(taskRefs[0])]; !ok {
			return &UnknownForEachListRefError{Loop: rl.id, Ref: taskRefs[0]}
		}
	}
	if len(paramRefs) == 1 {
		if _, ok := params[ParamName(paramRefs[0])]; !ok {
			return &UnknownForEachListRefError{Loop: rl.id, Ref: paramRefs[0]}
		}
	}
	lg.ListSource = rl.listSource
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

// UnknownForEachFieldError reports a key inside a `for_each:` block that is not
// one of in|as|tasks (id and description come from the wrapping task).
type UnknownForEachFieldError struct{ Field string }

func (e *UnknownForEachFieldError) Error() string {
	return fmt.Sprintf("for_each: unknown field %q", e.Field)
}

// MissingForEachListError reports a `for_each:` block that omits the required
// `in:` list.
type MissingForEachListError struct{ Loop LoopID }

func (e *MissingForEachListError) Error() string {
	return fmt.Sprintf("for_each %q: requires in", e.Loop)
}

// InvalidForEachListError reports a `for_each:` `in:` value that is neither a
// list nor a single `{{...}}` placeholder scalar (the dynamic-source shape).
type InvalidForEachListError struct {
	Loop   LoopID
	Source string
}

func (e *InvalidForEachListError) Error() string {
	return fmt.Sprintf("for_each %q: in %q must be a list or a single {{...}} placeholder", e.Loop, e.Source)
}

// UnknownForEachListRefError reports a dynamic `in:` placeholder that names a
// task or param the workflow does not declare.
type UnknownForEachListRefError struct {
	Loop LoopID
	Ref  string
}

func (e *UnknownForEachListRefError) Error() string {
	return fmt.Sprintf("for_each %q: in references unknown task or param %q", e.Loop, e.Ref)
}

// MissingForEachVarError reports a `for_each:` block that omits the required
// `as:` loop variable.
type MissingForEachVarError struct{ Loop LoopID }

func (e *MissingForEachVarError) Error() string {
	return fmt.Sprintf("for_each %q: requires as", e.Loop)
}

// InvalidForEachVarError reports a `for_each:` `as:` value that fails the
// `[A-Za-z0-9_]+` rule, the same alphabet as a placeholder name.
type InvalidForEachVarError struct {
	Loop LoopID
	As   string
}

func (e *InvalidForEachVarError) Error() string {
	return fmt.Sprintf("for_each %q: invalid as %q: must match [A-Za-z0-9_]+", e.Loop, e.As)
}

// ForEachVarCollisionError reports a `for_each:` `as:` loop variable whose name
// collides with a task id or param name; Kind is "task" or "param".
type ForEachVarCollisionError struct {
	Loop LoopID
	As   string
	Kind string
}

func (e *ForEachVarCollisionError) Error() string {
	return fmt.Sprintf("for_each %q: as %q collides with a %s name", e.Loop, e.As, e.Kind)
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

// PrevInParallelLoopError reports a `{{prev.id}}` placeholder inside a parallel
// for_each body. Its passes run concurrently with no ordering, so there is no
// prior iteration to read; prev is only meaningful in a sequential loop.
type PrevInParallelLoopError struct {
	Task TaskID
	Loop LoopID
	Name string
}

func (e *PrevInParallelLoopError) Error() string {
	return fmt.Sprintf("task %q: placeholder {{prev.%s}} is not allowed in parallel for_each %q", e.Task, e.Name, e.Loop)
}
