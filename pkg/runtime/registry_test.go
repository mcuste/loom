package runtime_test

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/mcuste/loom/pkg/runtime"
)

func TestRegisterAndLookup(t *testing.T) {
	name := runtime.Name("test-register-and-lookup")
	want := &fakeSpec{}

	runtime.Register(name, want)

	got, ok := runtime.Lookup(name)
	if !ok {
		t.Fatalf("Lookup(%q) ok = false, want true", name)
	}
	if got != want {
		t.Fatalf("Lookup returned %p, want the registered spec %p", got, want)
	}
}

func TestLookupUnknownReturnsFalse(t *testing.T) {
	if _, ok := runtime.Lookup("never-registered-runtime"); ok {
		t.Fatalf("Lookup of unknown runtime returned ok = true")
	}
}

func TestRegisterPanicsOnEmptyName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("Register(\"\", spec) did not panic")
		}
	}()
	runtime.Register("", fakeSpec{})
}

func TestRegisterPanicsOnNilSpec(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("Register(name, nil) did not panic")
		}
	}()
	runtime.Register("test-nil-spec", nil)
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	name := runtime.Name("test-duplicate")
	runtime.Register(name, fakeSpec{})

	defer func() {
		if recover() == nil {
			t.Fatalf("second Register(%q, ...) did not panic", name)
		}
	}()
	runtime.Register(name, fakeSpec{})
}

func TestRegisteredIncludesRegistered(t *testing.T) {
	name := runtime.Name("test-registered-list")
	runtime.Register(name, fakeSpec{})

	if !slices.Contains(runtime.Registered(), name) {
		t.Fatalf("Registered() does not contain %q", name)
	}
}

// TestValidateDispatch covers the top-level runtime.Validate dispatch path:
// empty/unknown names raise registry-flavored sentinels, known names delegate
// to the spec, and any spec error is wrapped with the runtime name. The full
// matrix of spec-level rejections lives in TestSpecValidate (runtime_test.go).
func TestValidateDispatch(t *testing.T) {
	name := runtime.Name("test-validate-dispatch")
	runtime.Register(name, newFake([]runtime.Model{"m1"}, nil, true))

	tests := []struct {
		name    string
		rt      runtime.Name
		req     runtime.Request
		wantErr error // nil for success
	}{
		{"ok delegates to spec", name, runtime.Request{Model: "m1"}, nil},
		{"missing runtime", "", runtime.Request{Model: "m1"}, runtime.ErrMissingRuntime},
		{"unknown runtime", "never-registered", runtime.Request{Model: "m1"}, runtime.ErrUnknownRuntime},
		{"spec rejection propagates", name, runtime.Request{Model: "m9"}, runtime.ErrUnsupportedModel},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := runtime.Validate(tc.rt, tc.req)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate returned %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate returned %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}

// TestValidateWrapsRuntimeName pins the user-facing error format on the
// dispatch path: "<runtime>: <field> <value>: <sentinel>". Lets callers
// identify which task's runtime rejected the request.
func TestValidateWrapsRuntimeName(t *testing.T) {
	name := runtime.Name("test-wraps-name")
	runtime.Register(name, newFake([]runtime.Model{"m1"}, nil, true))

	err := runtime.Validate(name, runtime.Request{Model: "m9"})
	if err == nil {
		t.Fatalf("Validate returned nil, want error")
	}
	if !errors.Is(err, runtime.ErrUnsupportedModel) {
		t.Fatalf("errors.Is(_, ErrUnsupportedModel) = false; err = %v", err)
	}
	want := `test-wraps-name: model "m9": unsupported model`
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestRegisterConcurrent exercises the registry under concurrent Register and
// Lookup calls. Run with -race to catch lock-protocol violations.
func TestRegisterConcurrent(t *testing.T) {
	const N = 50
	var wg sync.WaitGroup
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := runtime.Name(fmt.Sprintf("test-concurrent-%d", i))
			runtime.Register(name, fakeSpec{})
			if _, ok := runtime.Lookup(name); !ok {
				t.Errorf("Lookup(%q) after Register returned ok=false", name)
			}
		}(i)
	}
	wg.Wait()
}

// TestRegisteredReturnsFreshSlice verifies that mutating the slice returned
// by Registered does not corrupt the registry's view of itself.
func TestRegisteredReturnsFreshSlice(t *testing.T) {
	name := runtime.Name("test-fresh-slice")
	runtime.Register(name, fakeSpec{})

	first := runtime.Registered()
	if !slices.Contains(first, name) {
		t.Fatalf("first call missing registered name; got %v", first)
	}
	for i := range first {
		first[i] = "mutated-by-caller"
	}

	second := runtime.Registered()
	if !slices.Contains(second, name) {
		t.Fatalf("second call missing registered name after caller mutation; got %v", second)
	}
}
