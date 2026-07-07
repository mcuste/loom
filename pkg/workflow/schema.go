package workflow

import (
	"errors"
	"fmt"

	"github.com/mcuste/loom/pkg/syntax"
)

// Schema is a minimal JSON Schema subset attached to an LLM task via the
// per-task `schema:` block. After the task produces output, the executor
// validates that the output parses as JSON and conforms to this schema.
type Schema struct {
	// Type is the declared JSON type, e.g. "object".
	Type string
	// Required names the properties that must be present.
	Required []string
	// Properties maps each property name to its declared sub-schema.
	Properties map[string]Property
}

// Property describes a single entry in a Schema's Properties map.
type Property struct {
	// Type is the declared JSON type of the property, e.g. "string".
	Type string
}

// ErrShellTaskWithSchema reports a shell task (one with `command:`) that also
// sets a `schema:` block. Schema validation only applies to LLM output, so the
// field is rejected at the task level.
var ErrShellTaskWithSchema = errors.New("shell task must not set schema")

// rawSchema mirrors the per-task `schema:` block as decoded by yaml.v3. It
// exists only so parseSchema can validate the block; callers see the validated
// *Schema. Properties nest one level deep; each property carries only its
// declared type.
type rawSchema struct {
	Type       string                 `yaml:"type"`
	Required   []string               `yaml:"required"`
	Properties map[string]rawProperty `yaml:"properties"`
}

// rawProperty mirrors one entry in rawSchema's Properties map.
type rawProperty struct {
	Type string `yaml:"type"`
}

// parseSchema decodes a task's `schema:` node into a *Schema. An absent block
// (zero-value node) yields nil, the no-validation default.
func parseSchema(node syntax.Value) (*Schema, error) {
	if !node.Present() {
		return nil, nil
	}
	var rs rawSchema
	if err := node.Decode(&rs); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	s := &Schema{Type: rs.Type, Required: rs.Required}
	if len(rs.Properties) > 0 {
		s.Properties = make(map[string]Property, len(rs.Properties))
		for name, p := range rs.Properties {
			s.Properties[name] = Property(p)
		}
	}
	return s, nil
}
