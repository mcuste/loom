package runtime

import (
	"fmt"
	"sync"
)

// Registry is a thread-safe map from runtime Name to Runner. The process-wide
// default is accessed via [Default]; callers that need an isolated set of
// runtimes (tests, plugins) can create their own.
type Registry struct {
	mu      sync.RWMutex
	runners map[Name]Runner
}

// Resolver maps a runtime name to the Runner that executes it.
type Resolver interface {
	Resolve(name Name) (Runner, bool)
}

// Validator checks whether a runtime accepts a request.
type Validator interface {
	Validate(name Name, req Request) error
}

// Catalog is the combined lookup + validation seam threaded through the run
// pipeline. *Registry satisfies it directly.
type Catalog interface {
	Resolver
	Validator
}

// Default returns the process-wide runtime registry. The global Register,
// Lookup, Registered, and Validate package-level functions delegate to it.
func Default() *Registry { return &defaultReg }

var defaultReg Registry

// Register installs r under name. Intended for init() of runtime packages.
// Panics on empty name, nil runner, or duplicate registration.
func (reg *Registry) Register(name Name, r Runner) {
	if name == "" {
		panic("runtime: Register called with empty name")
	}
	if r == nil {
		panic("runtime: Register called with nil runner for " + string(name))
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.runners == nil {
		reg.runners = make(map[Name]Runner)
	}
	if _, dup := reg.runners[name]; dup {
		panic("runtime: already registered: " + string(name))
	}
	reg.runners[name] = r
}

// Lookup returns the runner for name and whether it is registered.
func (reg *Registry) Lookup(name Name) (Runner, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	r, ok := reg.runners[name]
	return r, ok
}

// Resolve satisfies [Resolver] so a *Registry can be injected directly into
// callers that only need runtime lookup.
func (reg *Registry) Resolve(name Name) (Runner, bool) {
	return reg.Lookup(name)
}

// Registered returns the names of all registered runtimes in unspecified
// order. Intended for diagnostics and help output.
func (reg *Registry) Registered() []Name {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	names := make([]Name, 0, len(reg.runners))
	for name := range reg.runners {
		names = append(names, name)
	}
	return names
}

// Validate looks name up in reg and delegates to its Runner. Use it from the
// workflow parser and from any caller building a Request outside the parser
// path. Per-runtime errors are wrapped with the runtime name so the caller's
// error message reads "<name>: <field> <value>: <sentinel>".
func (reg *Registry) Validate(name Name, req Request) error {
	if name == "" {
		return ErrMissingRuntime
	}
	r, ok := reg.Lookup(name)
	if !ok {
		return fmt.Errorf("%q: %w", name, ErrUnknownRuntime)
	}
	if err := r.Validate(req); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// Register installs r in the default runtime registry under name.
func Register(name Name, r Runner) { Default().Register(name, r) }

// Lookup returns the runner for name from the default registry.
func Lookup(name Name) (Runner, bool) { return Default().Lookup(name) }

// Registered returns the names of all runtimes in the default registry.
func Registered() []Name { return Default().Registered() }

// Validate looks name up in the default registry and dispatches to its Runner.
func Validate(name Name, req Request) error { return Default().Validate(name, req) }
