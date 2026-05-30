package runtime

import "sync"

var (
	registryMu sync.RWMutex
	registry   = map[Name]RuntimeSpec{}
)

// Register installs spec under name. Intended for init() of runtime packages
// and for the future loom-config extension point that lets users declare
// custom runtimes. Panics on empty name, nil spec, or duplicate registration.
func Register(name Name, spec RuntimeSpec) {
	if name == "" {
		panic("runtime: Register called with empty name")
	}
	if spec == nil {
		panic("runtime: Register called with nil spec for " + string(name))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[name]; dup {
		panic("runtime: already registered: " + string(name))
	}
	registry[name] = spec
}

// Lookup returns the spec for name and whether it is registered.
func Lookup(name Name) (RuntimeSpec, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	spec, ok := registry[name]
	return spec, ok
}

// Registered returns the names of all registered runtimes in unspecified
// order. Intended for diagnostics and help output.
func Registered() []Name {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]Name, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

