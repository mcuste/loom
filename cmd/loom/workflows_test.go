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

// writeLocalRegistryWorkflow drops a workflow YAML at
// <dir>/.loom/workflows/<relpath> (creating parents) and returns its absolute
// path. It mirrors writeRegistryWorkflow but targets a project-local registry
// at an arbitrary directory rather than $LOOM_HOME/workflows, so a test can pin
// the upward `.loom/workflows` search.
func writeLocalRegistryWorkflow(t *testing.T, dir, relpath, body string) string {
	t.Helper()
	full := filepath.Join(dir, ".loom", "workflows", relpath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir local registry dir: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write local registry workflow: %v", err)
	}
	return full
}

// writeLocalRegistryWF is writeLocalRegistryWorkflow with the shared minimal
// body, for the local-resolution tests that do not care about the contents.
func writeLocalRegistryWF(t *testing.T, dir, relpath string) string {
	t.Helper()
	return writeLocalRegistryWorkflow(t, dir, relpath, registryBody)
}

// registryBodyWithDesc returns a minimal parseable workflow body carrying the
// given description, so a listing test can tell which of two same-named
// registry entries (local vs global) won the nearest-wins shadow.
func registryBodyWithDesc(desc string) string {
	return "name: x\ndescription: " + desc + "\nruntime: cmd-echo\nmodel: m1\ntasks:\n  - id: a\n    prompt: hi\n"
}

// projectTree returns a fresh temp dir with its symlinks resolved, so that the
// path it returns matches os.Getwd() after chdir (macOS routes t.TempDir()
// through /var -> /private/var). Registry resolution derives its local search
// roots from os.Getwd(), so string-comparing the resolved path against this
// root is stable.
func projectTree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	return root
}

// mkGitRoot marks dir as a git repo root by creating a `.git` directory under
// it, so a test can place the upward-walk stop boundary precisely.
func mkGitRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
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
	// Count rows whose name column is exactly "both"; the resolved-path column
	// also contains "both.yaml", so a raw substring count would over-count.
	rows := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "both" {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("listing should dedup colliding stem to one name; got %d rows:\n%s", rows, out)
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

// TestResolveWorkflowRef_LocalFromCwd pins that a name resolves against the
// project-local registry at <cwd>/.loom/workflows, with no $LOOM_HOME copy.
func TestResolveWorkflowRef_LocalFromCwd(t *testing.T) {
	loomHomeForTest(t)
	root := projectTree(t)
	want := writeLocalRegistryWF(t, root, "local.yaml")
	chdirTo(t, root)

	got, err := resolveWorkflowRef("local")
	if err != nil {
		t.Fatalf("resolveWorkflowRef(local): %v", err)
	}
	if got != want {
		t.Errorf("resolveWorkflowRef(local) = %q, want local registry path %q", got, want)
	}
}

// TestResolveWorkflowRef_WalksToGitRoot pins that resolution walks UP from a
// nested subdir and finds a workflow in the git repo root's .loom/workflows.
func TestResolveWorkflowRef_WalksToGitRoot(t *testing.T) {
	loomHomeForTest(t)
	root := projectTree(t)
	mkGitRoot(t, root)
	want := writeLocalRegistryWF(t, root, "rooted.yaml")
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested subdir: %v", err)
	}
	chdirTo(t, nested)

	got, err := resolveWorkflowRef("rooted")
	if err != nil {
		t.Fatalf("resolveWorkflowRef(rooted): %v", err)
	}
	if got != want {
		t.Errorf("resolveWorkflowRef(rooted) = %q, want git-root registry path %q", got, want)
	}
}

// TestResolveWorkflowRef_StopsAtGitRoot pins the upward-walk boundary: a
// .loom/workflows ABOVE the git root is not searched from inside the repo,
// while a registry at the git root itself still resolves.
func TestResolveWorkflowRef_StopsAtGitRoot(t *testing.T) {
	loomHomeForTest(t)
	above := projectTree(t)
	writeLocalRegistryWF(t, above, "aboveonly.yaml")
	repo := filepath.Join(above, "repo")
	mkGitRoot(t, repo)
	inRepo := writeLocalRegistryWF(t, repo, "inrepo.yaml")
	nested := filepath.Join(repo, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested subdir: %v", err)
	}
	chdirTo(t, nested)

	t.Run("above git root ignored", func(t *testing.T) {
		if got, err := resolveWorkflowRef("aboveonly"); err == nil {
			t.Errorf("resolveWorkflowRef(aboveonly) = %q, want not-found (above-root dir must not be searched)", got)
		}
	})
	t.Run("git root searched", func(t *testing.T) {
		got, err := resolveWorkflowRef("inrepo")
		if err != nil {
			t.Fatalf("resolveWorkflowRef(inrepo): %v", err)
		}
		if got != inRepo {
			t.Errorf("resolveWorkflowRef(inrepo) = %q, want git-root registry path %q", got, inRepo)
		}
	})
}

// TestIsGitRoot pins that isGitRoot treats both a `.git` directory (normal repo)
// and a `.git` file (worktree/submodule) as a root, and a dir with no `.git` as
// not a root. The file case guards the documented worktree behavior so a refactor
// to Stat+IsDir cannot silently break git-worktree users.
func TestIsGitRoot(t *testing.T) {
	t.Run("dir is root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir .git: %v", err)
		}
		if !isGitRoot(dir) {
			t.Errorf("isGitRoot(%q with .git dir) = false, want true", dir)
		}
	})
	t.Run("file is root", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatalf("write .git file: %v", err)
		}
		if !isGitRoot(dir) {
			t.Errorf("isGitRoot(%q with .git file) = false, want true", dir)
		}
	})
	t.Run("no .git is not root", func(t *testing.T) {
		dir := t.TempDir()
		if isGitRoot(dir) {
			t.Errorf("isGitRoot(%q without .git) = true, want false", dir)
		}
	})
}

// TestResolveWorkflowRef_NotInRepoIgnoresAncestor pins that with no .git up the
// chain, only <cwd>/.loom/workflows is searched: an ancestor's registry is
// ignored (no whole-filesystem scan), while the cwd's local dir still resolves.
func TestResolveWorkflowRef_NotInRepoIgnoresAncestor(t *testing.T) {
	loomHomeForTest(t)
	base := projectTree(t)
	writeLocalRegistryWF(t, base, "ancestoronly.yaml")
	child := filepath.Join(base, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}
	wantChild := writeLocalRegistryWF(t, child, "childlocal.yaml")
	chdirTo(t, child)

	t.Run("ancestor ignored", func(t *testing.T) {
		if got, err := resolveWorkflowRef("ancestoronly"); err == nil {
			t.Errorf("resolveWorkflowRef(ancestoronly) = %q, want not-found (ancestor must not be searched outside a repo)", got)
		}
	})
	t.Run("cwd local resolves", func(t *testing.T) {
		got, err := resolveWorkflowRef("childlocal")
		if err != nil {
			t.Fatalf("resolveWorkflowRef(childlocal): %v", err)
		}
		if got != wantChild {
			t.Errorf("resolveWorkflowRef(childlocal) = %q, want cwd-local registry path %q", got, wantChild)
		}
	})
}

// TestResolveWorkflowRef_NearestWins pins override semantics: a name present in
// both <cwd>/.loom/workflows and $LOOM_HOME/workflows resolves to the local one.
func TestResolveWorkflowRef_NearestWins(t *testing.T) {
	home := loomHomeForTest(t)
	global := writeRegistryWF(t, home, "dup.yaml")
	root := projectTree(t)
	local := writeLocalRegistryWF(t, root, "dup.yaml")
	chdirTo(t, root)

	got, err := resolveWorkflowRef("dup")
	if err != nil {
		t.Fatalf("resolveWorkflowRef(dup): %v", err)
	}
	if got == global {
		t.Errorf("resolveWorkflowRef(dup) = %q (global); local registry should shadow it", got)
	}
	if got != local {
		t.Errorf("resolveWorkflowRef(dup) = %q, want local registry path %q", got, local)
	}
}

// TestResolveWorkflowRef_GlobalFallback pins that a name present only in
// $LOOM_HOME/workflows still resolves after the local search misses.
func TestResolveWorkflowRef_GlobalFallback(t *testing.T) {
	home := loomHomeForTest(t)
	want := writeRegistryWF(t, home, "onlyglobal.yaml")
	chdirTo(t, projectTree(t))

	got, err := resolveWorkflowRef("onlyglobal")
	if err != nil {
		t.Fatalf("resolveWorkflowRef(onlyglobal): %v", err)
	}
	if got != want {
		t.Errorf("resolveWorkflowRef(onlyglobal) = %q, want global registry path %q", got, want)
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
	// Count rows whose name column is exactly "shadow"; the resolved-path column
	// now also contains "shadow.yaml", so a raw substring count would double it.
	shadowRows := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "shadow" {
			shadowRows++
		}
	}
	if shadowRows != 1 {
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
