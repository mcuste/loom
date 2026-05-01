package workflow

import (
	"fmt"
	"sync"
)

// RuntimeSpec describes a registered runtime's capabilities. Runtime
// implementation packages register a spec in init() so the workflow package
// can validate task configuration without depending on the runtime package.
type RuntimeSpec interface {
	// Accepts reports whether the runtime can run the given model. Bounded
	// providers (e.g. claude-api, openai-api) ship a fixed list; open
	// providers (e.g. ollama) typically return true for any non-empty Model.
	Accepts(Model) bool
	// AcceptsEffort reports whether the runtime accepts the given Effort.
	// Only called for non-empty Effort; empty Effort is always valid and
	// means "leave the runtime default in place". Bounded enums (e.g.
	// openai-api: low|medium|high) reject anything outside their set; open
	// runtimes (e.g. claude-api with raw token budgets) accept any parseable
	// value.
	AcceptsEffort(Effort) bool
}

var (
	registryMu sync.RWMutex
	registry   = map[Runtime]RuntimeSpec{}
)

// Register installs spec under name. Intended for init() of runtime packages.
// Panics on empty name, nil spec, or duplicate registration.
func Register(name Runtime, spec RuntimeSpec) {
	if name == "" {
		panic("workflow: Register called with empty runtime name")
	}
	if spec == nil {
		panic("workflow: Register called with nil spec for " + string(name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("workflow: runtime already registered: " + string(name))
	}
	registry[name] = spec
}

// Lookup returns the spec for name and whether it is registered.
func Lookup(name Runtime) (RuntimeSpec, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	spec, ok := registry[name]
	return spec, ok
}

// RegisteredRuntimes returns the names of all registered runtimes in
// unspecified order. Intended for diagnostics and help output.
func RegisteredRuntimes() []Runtime {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]Runtime, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// ValidateTaskConfig checks that the (runtime, model, effort) triple is
// internally consistent: runtime is registered, model is non-empty and
// accepted by the runtime, and effort is either empty or accepted by the
// runtime.
func ValidateTaskConfig(rt Runtime, model Model, effort Effort) error {
	if rt == "" {
		return &MissingRuntimeError{}
	}
	spec, ok := Lookup(rt)
	if !ok {
		return &UnknownRuntimeError{Runtime: rt}
	}
	if model == "" {
		return &MissingModelError{Runtime: rt}
	}
	if !spec.Accepts(model) {
		return &UnsupportedModelError{Runtime: rt, Model: model}
	}
	if effort != "" && !spec.AcceptsEffort(effort) {
		return &UnsupportedEffortError{Runtime: rt, Effort: effort}
	}
	return nil
}

// MissingRuntimeError reports a task with no runtime set and no workflow
// default to inherit.
type MissingRuntimeError struct{}

func (*MissingRuntimeError) Error() string {
	return "runtime is required (set workflow- or task-level runtime)"
}

// UnknownRuntimeError reports a reference to a runtime not in the registry.
type UnknownRuntimeError struct {
	Runtime Runtime
}

func (e *UnknownRuntimeError) Error() string {
	return fmt.Sprintf("unknown runtime %q", string(e.Runtime))
}

// MissingModelError reports a task with no model set and no workflow default.
type MissingModelError struct {
	Runtime Runtime
}

func (e *MissingModelError) Error() string {
	return fmt.Sprintf("model is required for runtime %q", string(e.Runtime))
}

// UnsupportedModelError reports a model the named runtime does not accept.
type UnsupportedModelError struct {
	Runtime Runtime
	Model   Model
}

func (e *UnsupportedModelError) Error() string {
	return fmt.Sprintf("runtime %q does not accept model %q", string(e.Runtime), string(e.Model))
}

// UnsupportedEffortError reports an effort value the named runtime does not
// accept.
type UnsupportedEffortError struct {
	Runtime Runtime
	Effort  Effort
}

func (e *UnsupportedEffortError) Error() string {
	return fmt.Sprintf("runtime %q does not accept effort %q", string(e.Runtime), string(e.Effort))
}
