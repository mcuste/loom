package executor

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/mcuste/loom/pkg/workflow"
)

// SchemaError reports that an LLM task's output failed JSON-schema validation:
// it either did not parse as JSON or did not conform to the task's schema.
//
// runWithRetry treats a *SchemaError as retryable whenever the task's retry
// policy is enabled, independent of the policy's `on:` classes, so a mismatch
// retries iff Retry.Max > 0.
type SchemaError struct {
	// TaskID is the task whose output failed validation.
	TaskID workflow.TaskID
	// Reason is a short, lowercase description of the mismatch.
	Reason string
}

func (e *SchemaError) Error() string {
	return fmt.Sprintf("task %q: schema validation failed: %s", e.TaskID, e.Reason)
}

// validateSchema checks that output parses as JSON and conforms to t.Schema.
// It is a no-op (nil) when the task declares no schema. A mismatch is returned
// as a *SchemaError carrying t.ID so callers can branch on it via errors.As.
func validateSchema(t *workflow.Task, output string) error {
	if t.Schema == nil {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(output), &v); err != nil {
		return &SchemaError{TaskID: t.ID, Reason: "output is not valid JSON: " + err.Error()}
	}
	if reason := checkSchema(t.Schema, v); reason != "" {
		return &SchemaError{TaskID: t.ID, Reason: reason}
	}
	return nil
}

// checkSchema validates v against the JSON Schema subset s and returns a short
// lowercase reason on mismatch, or "" when v conforms. It checks the declared
// type, then (for objects) required-property presence and each declared
// property's type.
func checkSchema(s *workflow.Schema, v any) string {
	if s.Type != "" && !jsonTypeMatches(s.Type, v) {
		return fmt.Sprintf("expected type %q", s.Type)
	}
	// Required/Properties constrain object members. They apply whenever the
	// schema declares either, independent of an explicit `type: object`, so a
	// schema carrying only `required:` still demands an object.
	if len(s.Required) == 0 && len(s.Properties) == 0 {
		return ""
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return "expected an object"
	}
	for _, name := range s.Required {
		if _, present := obj[name]; !present {
			return fmt.Sprintf("missing required property %q", name)
		}
	}
	for name, prop := range s.Properties {
		pv, present := obj[name]
		if !present {
			continue
		}
		if prop.Type != "" && !jsonTypeMatches(prop.Type, pv) {
			return fmt.Sprintf("property %q: expected type %q", name, prop.Type)
		}
	}
	return ""
}

// jsonTypeMatches reports whether v, as decoded by encoding/json, matches the
// named JSON Schema type. An unrecognized type name is not constrained.
func jsonTypeMatches(typ string, v any) bool {
	switch typ {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	default:
		return true
	}
}
