package workflow

import (
	"fmt"

	"github.com/mcuste/loom/pkg/syntax"
)

// buildSubWorkflowDeps computes a sub-workflow task's dependency list. It
// validates the explicit depends_on list and then resolves any task-id
// placeholders that appear in the with: values, adding implicit edges.
func buildSubWorkflowDeps(dc depsCtx, declared []string, withArgs []WithArg) ([]TaskID, error) {
	deps, seen, err := buildDeclaredDeps(dc, declared)
	if err != nil {
		return nil, err
	}
	rs := refScope(dc)
	for _, a := range withArgs {
		if err := rs.resolveRefs(a.Value, true, seen, &deps); err != nil {
			return nil, err
		}
	}
	return deps, nil
}

// decodeWith decodes a sub-workflow task's with: mapping into ordered
// WithArg entries.
func decodeWith(tid TaskID, node syntax.Value) ([]WithArg, error) {
	if !node.Present() {
		return nil, nil
	}
	if node.Kind() != syntax.MappingNode {
		return nil, fmt.Errorf("task %q: with must be a mapping", tid)
	}
	args := make([]WithArg, 0)
	if err := node.EachMapEntry(fmt.Sprintf("task %q: with", tid), func(key string, v syntax.Value) error {
		name, err := NewParamName(key)
		if err != nil {
			return fmt.Errorf("task %q: with: %w", tid, err)
		}
		var val string
		if err := v.Decode(&val); err != nil {
			return fmt.Errorf("task %q: with.%s: %w", tid, name, err)
		}
		args = append(args, WithArg{Name: name, Value: val})
		return nil
	}); err != nil {
		return nil, err
	}
	return args, nil
}
