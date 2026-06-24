package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registryBody is a minimal parseable workflow body used to populate the
// $LOOM_HOME/workflows registry in these tests. Resolution does not read the
// body, but `loom workflows ls` parses it best-effort for the description.
const registryBody = `name: x
runtime: cmd-echo
model: m1
tasks:
  - id: a
    prompt: hi
`

// writeRegistryWorkflow drops a workflow YAML at $LOOM_HOME/workflows/<relpath>
// (creating parents) and returns its absolute path, so a test can assert that
// resolveWorkflowRef maps a registry name back to exactly this file.
func writeRegistryWorkflow(t *testing.T, home, relpath, body string) string {
	t.Helper()
	full := filepath.Join(home, "workflows", relpath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir registry dir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write registry workflow: %v", err)
	}
	return full
}

// writeRegistryWF is writeRegistryWorkflow with the shared minimal body, for
// the resolution and listing tests that do not care about the contents.
func writeRegistryWF(t *testing.T, home, relpath string) string {
	t.Helper()
	return writeRegistryWorkflow(t, home, relpath, registryBody)
}

// runWorkflowsLs executes `loom workflows ls` through the real root command and
// returns its combined output, failing the test if Execute errors.
func runWorkflowsLs(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"workflows", "ls"})
	if err := root.Execute(); err != nil {
		t.Fatalf("workflows ls: %v\noutput:\n%s", err, buf.String())
	}
	return buf.String()
}

// TestResolveWorkflowRef_PathPassthrough pins that an arg classified as a
// filesystem PATH (contains a separator, or ends in .yaml/.yml with no ':') is
// returned verbatim and the registry is never consulted: resolution is
// cwd- and $LOOM_HOME-independent for paths.
func TestResolveWorkflowRef_PathPassthrough(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"bare yaml", "x.yaml"},
		{"yaml in dir", "dir/x.yaml"},
		{"dot slash", "./x"},
		{"absolute", "/abs/wf.yaml"},
		// Pins isRegistryName's separator-first ordering: a Windows drive ref
		// is a PATH because of its '\', even though its ':' would otherwise
		// mark a name. A reorder that checked ':' first would break here.
		{"windows drive", `C:\wf.yaml`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := loomHomeForTest(t)

			got, err := resolveWorkflowRef(tc.arg)
			if err != nil {
				t.Fatalf("resolveWorkflowRef(%q): %v", tc.arg, err)
			}
			if got != tc.arg {
				t.Errorf("resolveWorkflowRef(%q) = %q, want verbatim %q", tc.arg, got, tc.arg)
			}
			// Path mode must not touch the registry: the workflows dir stays absent.
			if _, statErr := os.Stat(filepath.Join(home, "workflows")); !os.IsNotExist(statErr) {
				t.Errorf("path mode consulted the registry; workflows dir exists (statErr=%v)", statErr)
			}
		})
	}
}

// TestResolveWorkflowRef_NameResolution pins name-mode resolution under
// $LOOM_HOME/workflows: a flat name, a ':'-separated hierarchy, and a final
// component whose trailing .yaml suffix is stripped before '.yaml' is appended.
func TestResolveWorkflowRef_NameResolution(t *testing.T) {
	home := loomHomeForTest(t)
	flat := writeRegistryWF(t, home, "deploy.yaml")
	hier := writeRegistryWF(t, home, filepath.Join("a", "b", "c.yaml"))
	suffix := writeRegistryWF(t, home, filepath.Join("parent", "deploy.yaml"))

	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"flat name", "deploy", flat},
		{"colon hierarchy", "a:b:c", hier},
		{"suffix strip", "parent:deploy.yaml", suffix},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWorkflowRef(tc.arg)
			if err != nil {
				t.Fatalf("resolveWorkflowRef(%q): %v", tc.arg, err)
			}
			if got != tc.want {
				t.Errorf("resolveWorkflowRef(%q) = %q, want %q", tc.arg, got, tc.want)
			}
		})
	}
}

// TestResolveWorkflowRef_YmlFallback pins that when no '.yaml' exists for a
// name, resolution falls back to a '.yml' file with the same stem.
func TestResolveWorkflowRef_YmlFallback(t *testing.T) {
	home := loomHomeForTest(t)
	want := writeRegistryWF(t, home, "only.yml")

	got, err := resolveWorkflowRef("only")
	if err != nil {
		t.Fatalf("resolveWorkflowRef(only): %v", err)
	}
	if got != want {
		t.Errorf("resolveWorkflowRef(only) = %q, want %q (.yml fallback)", got, want)
	}
}

// TestResolveWorkflowRef_YamlOverYml pins the documented preference: when both a
// '.yaml' and a '.yml' file share a stem, resolution returns the '.yaml' path.
func TestResolveWorkflowRef_YamlOverYml(t *testing.T) {
	home := loomHomeForTest(t)
	wantYaml := writeRegistryWF(t, home, "both.yaml")
	writeRegistryWF(t, home, "both.yml")

	got, err := resolveWorkflowRef("both")
	if err != nil {
		t.Fatalf("resolveWorkflowRef(both): %v", err)
	}
	if got != wantYaml {
		t.Errorf("resolveWorkflowRef(both) = %q, want %q (.yaml over .yml)", got, wantYaml)
	}
}

// TestWorkflowsLsDedupsYamlOverYml pins that `loom workflows ls` collapses a
// colliding '.yaml'/'.yml' stem to a single registry name (the '.yaml'-over-'.yml'
// dedup), listing it exactly once.
func TestWorkflowsLsDedupsYamlOverYml(t *testing.T) {
	home := loomHomeForTest(t)
	writeRegistryWF(t, home, "both.yaml")
	writeRegistryWF(t, home, "both.yml")

	out := runWorkflowsLs(t)
	if n := strings.Count(out, "both"); n != 1 {
		t.Errorf("listing should dedup colliding stem to one name; got %d occurrences:\n%s", n, out)
	}
}

// TestResolveWorkflowRef_RejectsBadComponents pins that empty, '.', and '..'
// name components are rejected before any filesystem lookup.
func TestResolveWorkflowRef_RejectsBadComponents(t *testing.T) {
	loomHomeForTest(t)
	cases := []struct {
		name string
		arg  string
	}{
		{"empty middle component", "a::b"},
		{"leading empty component", ":b"},
		{"dotdot component", "a:..:b"},
		{"bare dot", "."},
		{"bare dotdot", ".."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveWorkflowRef(tc.arg)
			if err == nil {
				t.Fatalf("resolveWorkflowRef(%q) = %q, want rejection error", tc.arg, got)
			}
		})
	}
}

// TestResolveWorkflowRef_RejectsTraversalEscape pins that a name whose
// components try to climb out of the workflows root is rejected rather than
// resolving to a path outside the registry.
func TestResolveWorkflowRef_RejectsTraversalEscape(t *testing.T) {
	loomHomeForTest(t)
	arg := "a:..:..:..:..:..:..:etc:passwd"

	got, err := resolveWorkflowRef(arg)
	if err == nil {
		t.Fatalf("resolveWorkflowRef(%q) = %q, want traversal-escape rejection", arg, got)
	}
}

// TestResolveWorkflowRef_MissingFile pins that a well-formed name with no
// matching file errors, naming the resolved name and hinting `workflows ls`.
func TestResolveWorkflowRef_MissingFile(t *testing.T) {
	loomHomeForTest(t)

	_, err := resolveWorkflowRef("ghost")
	if err == nil {
		t.Fatal("resolveWorkflowRef(ghost) = nil error, want not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ghost") {
		t.Errorf("error %q does not name the missing workflow %q", msg, "ghost")
	}
	if !strings.Contains(msg, "workflows ls") {
		t.Errorf("error %q does not hint `loom workflows ls`", msg)
	}
}

// TestWorkflowsLsCommand pins `loom workflows ls`: flat names list with the
// extension stripped and sorted; nested files render with ':' joins, sorted.
func TestWorkflowsLsCommand(t *testing.T) {
	t.Run("flat", func(t *testing.T) {
		home := loomHomeForTest(t)
		writeRegistryWF(t, home, "deploy.yaml")
		writeRegistryWF(t, home, "build.yml")

		out := runWorkflowsLs(t)
		for _, want := range []string{"build", "deploy"} {
			if !strings.Contains(out, want) {
				t.Errorf("listing missing %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, ".yaml") || strings.Contains(out, ".yml") {
			t.Errorf("listing should strip extensions; got:\n%s", out)
		}
		if strings.Index(out, "build") > strings.Index(out, "deploy") {
			t.Errorf("listing not sorted (build should precede deploy); got:\n%s", out)
		}
	})

	t.Run("nested", func(t *testing.T) {
		home := loomHomeForTest(t)
		writeRegistryWF(t, home, filepath.Join("ci", "test.yaml"))
		writeRegistryWF(t, home, filepath.Join("a", "b", "c.yaml"))

		out := runWorkflowsLs(t)
		for _, want := range []string{"a:b:c", "ci:test"} {
			if !strings.Contains(out, want) {
				t.Errorf("listing missing nested name %q; got:\n%s", want, out)
			}
		}
		if strings.Contains(out, ".yaml") || strings.Contains(out, ".yml") {
			t.Errorf("listing should strip extensions; got:\n%s", out)
		}
		if strings.Index(out, "a:b:c") > strings.Index(out, "ci:test") {
			t.Errorf("nested listing not sorted; got:\n%s", out)
		}
	})
}

// TestRunCommandResolvesRegistryName pins the run-by-name e2e: `loom run <name>`
// resolves a registry workflow under $LOOM_HOME/workflows and executes it,
// leaving a run record under the runs root keyed by the workflow id.
func TestRunCommandResolvesRegistryName(t *testing.T) {
	home := loomHomeForTest(t)
	chdirTo(t, t.TempDir())
	writeRegistryWorkflow(t, home, "greetme.yaml", `name: greetme
runtime: cmd-echo
model: m1
tasks:
  - id: greet
    prompt: hello
`)

	var buf bytes.Buffer
	root := newRootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"run", "greetme"})
	if err := root.Execute(); err != nil {
		t.Fatalf("run by name: %v\noutput:\n%s", err, buf.String())
	}

	if _, err := os.Stat(filepath.Join(testRunsDir(t), "greetme", "latest.json")); err != nil {
		t.Errorf("run-by-name did not execute (no run record): %v", err)
	}
}
