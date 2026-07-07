package syntax

import "gopkg.in/yaml.v3"

// Kind is the YAML node kind for a raw syntax value. It aliases yaml.v3's
// node kind without exposing yaml.Node outside this package.
type Kind int

const (
	DocumentNode Kind = Kind(yaml.DocumentNode)
	SequenceNode Kind = Kind(yaml.SequenceNode)
	MappingNode  Kind = Kind(yaml.MappingNode)
	ScalarNode   Kind = Kind(yaml.ScalarNode)
	AliasNode    Kind = Kind(yaml.AliasNode)
)

// Value is an uninterpreted YAML value captured by the syntax layer for fields
// whose semantic shape is validated by pkg/workflow. It lets the semantic
// parser inspect node kind, decode typed leaves, and iterate mappings/sequences
// without depending on yaml.Node directly.
type Value struct {
	present bool
	node    yaml.Node
}

// UnmarshalYAML captures the raw YAML node for later semantic analysis.
func (v *Value) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		*v = Value{}
		return nil
	}
	v.present = true
	v.node = *node
	return nil
}

// Present reports whether the YAML field was present in the decoded document.
func (v Value) Present() bool { return v.present }

// Kind reports the YAML node kind. The zero value is returned for absent fields.
func (v Value) Kind() Kind {
	if !v.present {
		return 0
	}
	return Kind(v.node.Kind)
}

// Scalar returns the raw scalar text. It is empty for absent or non-scalar
// values, matching yaml.Node.Value's behavior.
func (v Value) Scalar() string {
	if !v.present {
		return ""
	}
	return v.node.Value
}

// Tag returns the YAML tag (for example "!!null") for the captured value.
func (v Value) Tag() string {
	if !v.present {
		return ""
	}
	return v.node.Tag
}

// Decode decodes the captured value into out using yaml.v3's standard decoder.
func (v Value) Decode(out any) error {
	return v.node.Decode(out)
}

// EachMapEntry iterates mapping entries in declaration order. It assumes the
// caller already checked Kind() == MappingNode; if a key is not scalar, ctx is
// used to produce the same contextual error the old workflow parser emitted.
func (v Value) EachMapEntry(ctx string, fn func(key string, value Value) error) error {
	for i := 0; i+1 < len(v.node.Content); i += 2 {
		k, child := v.node.Content[i], v.node.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return &MapKeyError{Context: ctx}
		}
		if err := fn(k.Value, valueFromNode(child)); err != nil {
			return err
		}
	}
	return nil
}

// SequenceValues returns the sequence items in declaration order. It assumes the
// caller already checked Kind() == SequenceNode.
func (v Value) SequenceValues() []Value {
	out := make([]Value, len(v.node.Content))
	for i, child := range v.node.Content {
		out[i] = valueFromNode(child)
	}
	return out
}

func valueFromNode(node *yaml.Node) Value {
	if node == nil {
		return Value{}
	}
	return Value{present: true, node: *node}
}

// MapKeyError reports a non-scalar mapping key in a raw syntax value.
type MapKeyError struct {
	Context string
}

func (e *MapKeyError) Error() string {
	return e.Context + " key must be a scalar"
}
