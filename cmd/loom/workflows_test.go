package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// registryBodyWithDesc returns a minimal parseable workflow body carrying the
// given description, so a listing test can tell which of two same-named
// registry entries (local vs global) won the nearest-wins shadow.
func registryBodyWithDesc(desc string) string {
	return "name: x\ndescription: " + desc + "\nruntime: cmd-echo\nmodel: m1\ntasks:\n  - id: a\n    prompt: hi\n"
}

// assertNamesExtensionless checks that the name column (the first whitespace-
// delimited field of each listing row) carries no .yaml/.yml extension. The path
// column legitimately contains the extension, so the check is scoped to the name.
func assertNamesExtensionless(t *testing.T, out string) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if name := fields[0]; strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml") {
			t.Errorf("name column should strip extensions; got %q in:\n%s", name, out)
		}
	}
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

// rowWithName reports whether any listing row's name column (first field) equals
// name exactly, so a test can assert presence without matching the path column.
func rowWithName(out, name string) bool {
	return countRowsWithName(out, name) > 0
}

// countRowsWithName counts the listing rows whose name column (first field)
// equals name exactly. Scoping to the name column matters because the resolved-
// path column also contains the stem (e.g. "both.yaml"), so a raw substring
// count would over-count.
func countRowsWithName(out, name string) int {
	rows := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == name {
			rows++
		}
	}
	return rows
}

// TestWorkflowsLsDedupsYamlOverYml pins that `loom workflows ls` collapses a
// colliding '.yaml'/'.yml' stem to a single registry name (the '.yaml'-over-'.yml'
// dedup), listing it exactly once.
func TestWorkflowsLsDedupsYamlOverYml(t *testing.T) {
	home := loomHomeForTest(t)
	writeRegistryWF(t, home, "both.yaml")
	writeRegistryWF(t, home, "both.yml")

	out := runWorkflowsLs(t)
	if rows := countRowsWithName(out, "both"); rows != 1 {
		t.Errorf("listing should dedup colliding stem to one name; got %d rows:\n%s", rows, out)
	}
}

// TestWorkflowsLsEponymousDir pins that `loom workflows ls` lists a dir-based
// workflow under its collapsed name (`someworkflow`, not `someworkflow:someworkflow`)
// while a non-matching nested file keeps every segment.
func TestWorkflowsLsEponymousDir(t *testing.T) {
	home := loomHomeForTest(t)
	writeRegistryWF(t, home, filepath.Join("someworkflow", "someworkflow.yaml"))
	writeRegistryWF(t, home, filepath.Join("someworkflow", "somedir", "other.yaml"))

	out := runWorkflowsLs(t)
	for _, want := range []string{"someworkflow", "someworkflow:somedir:other"} {
		if !rowWithName(out, want) {
			t.Errorf("listing missing name %q; got:\n%s", want, out)
		}
	}
	if rowWithName(out, "someworkflow:someworkflow") {
		t.Errorf("dir-based workflow should collapse to `someworkflow`, not list redundant `someworkflow:someworkflow`; got:\n%s", out)
	}
	assertNamesExtensionless(t, out)
}

// TestWorkflowsLsFlatShadowsDir pins that when a flat file and the eponymous-dir
// form collide on one name, listing shows the flat file's path (flat wins),
// matching resolveWorkflowRef.
func TestWorkflowsLsFlatShadowsDir(t *testing.T) {
	home := loomHomeForTest(t)
	flat := writeRegistryWF(t, home, "dup.yaml")
	dir := writeRegistryWF(t, home, filepath.Join("dup", "dup.yaml"))

	out := runWorkflowsLs(t)
	if !strings.Contains(out, flat) {
		t.Errorf("listing should show flat path %q (flat wins); got:\n%s", flat, out)
	}
	if strings.Contains(out, dir) {
		t.Errorf("listing should not show shadowed dir path %q; got:\n%s", dir, out)
	}
}

// TestWorkflowsLsCommand pins `loom workflows ls`: flat names list with the
// extension stripped and sorted; nested files render with ':' joins, sorted.
func TestWorkflowsLsCommand(t *testing.T) {
	t.Run("flat", func(t *testing.T) {
		home := loomHomeForTest(t)
		deploy := writeRegistryWF(t, home, "deploy.yaml")
		build := writeRegistryWF(t, home, "build.yml")

		out := runWorkflowsLs(t)
		for _, want := range []string{"build", "deploy"} {
			if !strings.Contains(out, want) {
				t.Errorf("listing missing %q; got:\n%s", want, out)
			}
		}
		assertNamesExtensionless(t, out)
		// The resolved path is shown so a shadowed name reveals which root won.
		for _, want := range []string{deploy, build} {
			if !strings.Contains(out, want) {
				t.Errorf("listing missing resolved path %q; got:\n%s", want, out)
			}
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
		assertNamesExtensionless(t, out)
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

	if out, err := runCLI(t, "run", "greetme"); err != nil {
		t.Fatalf("run by name: %v\noutput:\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(testRunsDir(t), "greetme", "latest.json")); err != nil {
		t.Errorf("run-by-name did not execute (no run record): %v", err)
	}
}

// TestWorkflowsLsMergesLocalAndGlobal pins that `loom workflows ls` lists the
// merged local+global set, and that a same-named local entry shadows the global
// one (nearest wins), appearing once with the local description.
func TestWorkflowsLsMergesLocalAndGlobal(t *testing.T) {
	home := loomHomeForTest(t)
	writeRegistryWF(t, home, "globalwf.yaml")
	writeRegistryWorkflow(t, home, "shadow.yaml", registryBodyWithDesc("GLOBAL-DESC"))
	root := projectTree(t)
	writeLocalRegistryWF(t, root, "localwf.yaml")
	writeLocalRegistryWorkflow(t, root, "shadow.yaml", registryBodyWithDesc("LOCAL-DESC"))
	chdirTo(t, root)

	out := runWorkflowsLs(t)
	for _, want := range []string{"globalwf", "localwf", "shadow"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing merged name %q; got:\n%s", want, out)
		}
	}
	if shadowRows := countRowsWithName(out, "shadow"); shadowRows != 1 {
		t.Errorf("shadowed name should list once; got %d rows:\n%s", shadowRows, out)
	}
	if !strings.Contains(out, "LOCAL-DESC") {
		t.Errorf("local entry should shadow global; want local description, got:\n%s", out)
	}
	if strings.Contains(out, "GLOBAL-DESC") {
		t.Errorf("global entry should be shadowed by local; got:\n%s", out)
	}
}

// TestCompleteWorkflowRefMergesLocalAndGlobal pins that shell completion offers
// the merged local+global name set, with a local name shadowing the global one.
func TestCompleteWorkflowRefMergesLocalAndGlobal(t *testing.T) {
	home := loomHomeForTest(t)
	writeRegistryWF(t, home, "gcomp.yaml")
	writeRegistryWF(t, home, "shadowcomp.yaml")
	root := projectTree(t)
	writeLocalRegistryWF(t, root, "lcomp.yaml")
	writeLocalRegistryWF(t, root, "shadowcomp.yaml")
	chdirTo(t, root)

	names, _ := completeWorkflowRef(nil, nil, "")
	have := make(map[string]int, len(names))
	for _, n := range names {
		have[n]++
	}
	for _, want := range []string{"gcomp", "lcomp", "shadowcomp"} {
		if have[want] == 0 {
			t.Errorf("completion missing merged name %q; got %v", want, names)
		}
	}
	if have["shadowcomp"] > 1 {
		t.Errorf("shadowed completion name should appear once; got %d in %v", have["shadowcomp"], names)
	}
}
