package registry_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuste/loom/pkg/registry"
)

// TestResolve_PathPassthrough pins that an arg classified as a filesystem PATH
// (contains a separator, or ends in .yaml/.yml with no ':') is returned
// verbatim and the registry is never consulted: resolution is root-independent
// for paths.
func TestResolve_PathPassthrough(t *testing.T) {
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
			got, err := registry.Resolve(nil, tc.arg)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.arg, err)
			}
			if got != tc.arg {
				t.Errorf("Resolve(%q) = %q, want verbatim %q", tc.arg, got, tc.arg)
			}
		})
	}
}

// TestResolve_NameResolution pins name-mode resolution against a single root:
// a flat name, a ':'-separated hierarchy, and a final component whose trailing
// .yaml suffix is stripped before '.yaml' is appended.
func TestResolve_NameResolution(t *testing.T) {
	root := t.TempDir()
	flat := writeWorkflowFile(t, root, "deploy.yaml")
	hier := writeWorkflowFile(t, root, filepath.Join("a", "b", "c.yaml"))
	suffix := writeWorkflowFile(t, root, filepath.Join("parent", "deploy.yaml"))

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
			got, err := registry.Resolve([]string{root}, tc.arg)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.arg, err)
			}
			if got != tc.want {
				t.Errorf("Resolve(%q) = %q, want %q", tc.arg, got, tc.want)
			}
		})
	}
}

// TestResolve_YmlFallback pins that when no '.yaml' exists for a name,
// resolution falls back to a '.yml' file with the same stem.
func TestResolve_YmlFallback(t *testing.T) {
	root := t.TempDir()
	want := writeWorkflowFile(t, root, "only.yml")

	got, err := registry.Resolve([]string{root}, "only")
	if err != nil {
		t.Fatalf("Resolve(only): %v", err)
	}
	if got != want {
		t.Errorf("Resolve(only) = %q, want %q (.yml fallback)", got, want)
	}
}

// TestResolve_YamlOverYml pins the documented preference: when both a '.yaml'
// and a '.yml' file share a stem, resolution returns the '.yaml' path.
func TestResolve_YamlOverYml(t *testing.T) {
	root := t.TempDir()
	wantYaml := writeWorkflowFile(t, root, "both.yaml")
	writeWorkflowFile(t, root, "both.yml")

	got, err := registry.Resolve([]string{root}, "both")
	if err != nil {
		t.Fatalf("Resolve(both): %v", err)
	}
	if got != wantYaml {
		t.Errorf("Resolve(both) = %q, want %q (.yaml over .yml)", got, wantYaml)
	}
}

// TestResolve_EponymousDir pins the dir-based workflow forms: a name resolves
// to `<...>/<cn>/<cn>.yaml` so a workflow can live in its own directory beside
// its prompt files. A flat name finds the eponymous-dir file, a nested name
// finds it under the colon hierarchy, and the '.yml' dir form is a fallback.
func TestResolve_EponymousDir(t *testing.T) {
	root := t.TempDir()
	flatDir := writeWorkflowFile(t, root, filepath.Join("someworkflow", "someworkflow.yaml"))
	nestedDir := writeWorkflowFile(t, root, filepath.Join("a", "b", "c", "c.yaml"))
	ymlDir := writeWorkflowFile(t, root, filepath.Join("ymlwf", "ymlwf.yml"))

	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"flat eponymous dir", "someworkflow", flatDir},
		{"nested eponymous dir", "a:b:c", nestedDir},
		{"yml dir fallback", "ymlwf", ymlDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := registry.Resolve([]string{root}, tc.arg)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.arg, err)
			}
			if got != tc.want {
				t.Errorf("Resolve(%q) = %q, want %q", tc.arg, got, tc.want)
			}
		})
	}
}

// TestResolve_FlatShadowsDir pins the documented collision precedence: when
// both a flat file `X.yaml` and the eponymous-dir form `X/X.yaml` exist in
// one root, the flat file wins.
func TestResolve_FlatShadowsDir(t *testing.T) {
	root := t.TempDir()
	flat := writeWorkflowFile(t, root, "dup.yaml")
	writeWorkflowFile(t, root, filepath.Join("dup", "dup.yaml"))

	got, err := registry.Resolve([]string{root}, "dup")
	if err != nil {
		t.Fatalf("Resolve(dup): %v", err)
	}
	if got != flat {
		t.Errorf("Resolve(dup) = %q, want flat file %q (flat shadows dir form)", got, flat)
	}
}

// TestResolve_NonEponymousUnchanged pins that a non-matching nested file keeps
// every path segment: `someworkflow/somedir/other.yaml` resolves only from
// `someworkflow:somedir:other`, not from a collapsed name.
func TestResolve_NonEponymousUnchanged(t *testing.T) {
	root := t.TempDir()
	want := writeWorkflowFile(t, root, filepath.Join("someworkflow", "somedir", "other.yaml"))

	got, err := registry.Resolve([]string{root}, "someworkflow:somedir:other")
	if err != nil {
		t.Fatalf("Resolve(someworkflow:somedir:other): %v", err)
	}
	if got != want {
		t.Errorf("Resolve(someworkflow:somedir:other) = %q, want %q", got, want)
	}
}

// TestResolve_RejectsBadComponents pins that empty, '.', and '..' name
// components are rejected before any filesystem lookup.
func TestResolve_RejectsBadComponents(t *testing.T) {
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
			got, err := registry.Resolve(nil, tc.arg)
			if err == nil {
				t.Fatalf("Resolve(%q) = %q, want rejection error", tc.arg, got)
			}
		})
	}
}

// TestResolve_RejectsTraversalEscape pins that a name whose components try to
// climb out of the workflows root is rejected rather than resolving to a path
// outside the registry.
func TestResolve_RejectsTraversalEscape(t *testing.T) {
	arg := "a:..:..:..:..:..:..:etc:passwd"
	got, err := registry.Resolve(nil, arg)
	if err == nil {
		t.Fatalf("Resolve(%q) = %q, want traversal-escape rejection", arg, got)
	}
}

// TestResolve_MissingFile pins that a well-formed name with no matching file
// errors, naming the resolved name and hinting `workflows ls`.
func TestResolve_MissingFile(t *testing.T) {
	root := t.TempDir()

	_, err := registry.Resolve([]string{root}, "ghost")
	if err == nil {
		t.Fatal("Resolve(ghost) = nil error, want not-found error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ghost") {
		t.Errorf("error %q does not name the missing workflow %q", msg, "ghost")
	}
	if !strings.Contains(msg, "workflows ls") {
		t.Errorf("error %q does not hint `loom workflows ls`", msg)
	}
}

// TestResolve_LocalFromStart pins that a name resolves against the
// project-local registry at <start>/.loom/workflows.
func TestResolve_LocalFromStart(t *testing.T) {
	root := projectTree(t)
	want := writeLocalWorkflowFile(t, root, "local.yaml")
	roots := registry.LocalDirs(root)

	got, err := registry.Resolve(roots, "local")
	if err != nil {
		t.Fatalf("Resolve(local): %v", err)
	}
	if got != want {
		t.Errorf("Resolve(local) = %q, want local registry path %q", got, want)
	}
}

// TestResolve_WalksToGitRoot pins that LocalDirs walks UP from a nested subdir
// and includes the git repo root's .loom/workflows.
func TestResolve_WalksToGitRoot(t *testing.T) {
	root := projectTree(t)
	mkGitRoot(t, root)
	want := writeLocalWorkflowFile(t, root, "rooted.yaml")
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested subdir: %v", err)
	}
	roots := registry.LocalDirs(nested)

	got, err := registry.Resolve(roots, "rooted")
	if err != nil {
		t.Fatalf("Resolve(rooted): %v", err)
	}
	if got != want {
		t.Errorf("Resolve(rooted) = %q, want git-root registry path %q", got, want)
	}
}

// TestResolve_StopsAtGitRoot pins the upward-walk boundary: a
// .loom/workflows ABOVE the git root is not searched from inside the repo,
// while a registry at the git root itself still resolves.
func TestResolve_StopsAtGitRoot(t *testing.T) {
	above := projectTree(t)
	writeLocalWorkflowFile(t, above, "aboveonly.yaml")
	repo := filepath.Join(above, "repo")
	mkGitRoot(t, repo)
	inRepo := writeLocalWorkflowFile(t, repo, "inrepo.yaml")
	nested := filepath.Join(repo, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested subdir: %v", err)
	}
	roots := registry.LocalDirs(nested)

	t.Run("above git root ignored", func(t *testing.T) {
		if got, err := registry.Resolve(roots, "aboveonly"); err == nil {
			t.Errorf("Resolve(aboveonly) = %q, want not-found (above-root dir must not be searched)", got)
		}
	})
	t.Run("git root searched", func(t *testing.T) {
		got, err := registry.Resolve(roots, "inrepo")
		if err != nil {
			t.Fatalf("Resolve(inrepo): %v", err)
		}
		if got != inRepo {
			t.Errorf("Resolve(inrepo) = %q, want git-root registry path %q", got, inRepo)
		}
	})
}

// TestResolve_NotInRepoIgnoresAncestor pins that with no .git up the chain,
// only <start>/.loom/workflows is searched.
func TestResolve_NotInRepoIgnoresAncestor(t *testing.T) {
	base := projectTree(t)
	writeLocalWorkflowFile(t, base, "ancestoronly.yaml")
	child := filepath.Join(base, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}
	wantChild := writeLocalWorkflowFile(t, child, "childlocal.yaml")
	roots := registry.LocalDirs(child)

	t.Run("ancestor ignored", func(t *testing.T) {
		if got, err := registry.Resolve(roots, "ancestoronly"); err == nil {
			t.Errorf("Resolve(ancestoronly) = %q, want not-found (ancestor must not be searched outside a repo)", got)
		}
	})
	t.Run("cwd local resolves", func(t *testing.T) {
		got, err := registry.Resolve(roots, "childlocal")
		if err != nil {
			t.Fatalf("Resolve(childlocal): %v", err)
		}
		if got != wantChild {
			t.Errorf("Resolve(childlocal) = %q, want cwd-local registry path %q", got, wantChild)
		}
	})
}

// TestResolve_NearestWins pins override semantics: a name present in both a
// local root and a global root resolves to the local one (nearest shadows).
func TestResolve_NearestWins(t *testing.T) {
	globalRoot := t.TempDir()
	global := writeWorkflowFile(t, globalRoot, "dup.yaml")
	localRoot := projectTree(t)
	local := writeLocalWorkflowFile(t, localRoot, "dup.yaml")
	roots := append(registry.LocalDirs(localRoot), globalRoot)

	got, err := registry.Resolve(roots, "dup")
	if err != nil {
		t.Fatalf("Resolve(dup): %v", err)
	}
	if got == global {
		t.Errorf("Resolve(dup) = %q (global); local registry should shadow it", got)
	}
	if got != local {
		t.Errorf("Resolve(dup) = %q, want local registry path %q", got, local)
	}
}

// TestResolve_GlobalFallback pins that a name present only in the global root
// still resolves after the local search misses.
func TestResolve_GlobalFallback(t *testing.T) {
	globalRoot := t.TempDir()
	want := writeWorkflowFile(t, globalRoot, "onlyglobal.yaml")
	localRoot := projectTree(t)
	roots := append(registry.LocalDirs(localRoot), globalRoot)

	got, err := registry.Resolve(roots, "onlyglobal")
	if err != nil {
		t.Fatalf("Resolve(onlyglobal): %v", err)
	}
	if got != want {
		t.Errorf("Resolve(onlyglobal) = %q, want global registry path %q", got, want)
	}
}
