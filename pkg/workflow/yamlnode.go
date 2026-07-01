package workflow

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// eachMapEntry iterates the key/value pairs of a YAML mapping node in
// declaration order, calling fn for each pair. It returns an error if n is not
// a mapping node or if any key is not a scalar. ctx is used only for error
// messages so the caller's context (e.g. "loop key", "retry key") is preserved.
func eachMapEntry(n *yaml.Node, ctx string, fn func(key string, v *yaml.Node) error) error {
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return fmt.Errorf("%s key must be a scalar", ctx)
		}
		if err := fn(k.Value, v); err != nil {
			return err
		}
	}
	return nil
}
