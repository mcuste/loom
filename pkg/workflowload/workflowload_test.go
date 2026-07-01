package workflowload

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const registryBody = `name: demo
runtime: cmd-echo
model: m1
tasks:
  - id: a
    command: echo hi
`

func TestLoadPathModeReturnsAbsolutePath(t *testing.T) {
	cwd := projectTree(t)
	home := t.TempDir()
	relPath := writeWorkflow(t, cwd, filepath.Join("nested", "wf.yaml"), registryBody)
	absPath := filepath.Join(cwd, "nested", "wf.yaml")

	tests := []struct {
		name string
		ref  string
		want string
	}{
		{name: "relative", ref: filepath.Join("nested", "wf.yaml"), want: absPath},
		{name: "absolute", ref: absPath, want: absPath},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wf, manifest, gotPath, err := Load(home, cwd, tc.ref)
			if err != nil {
				t.Fatalf("Load(%q): %v", tc.ref, err)
			}
			if wf == nil {
				t.Fatal("Load returned nil workflow")
			}
			if len(manifest) == 0 {
				t.Fatal("Load returned empty manifest")
			}
			if gotPath != tc.want {
				t.Fatalf("path = %q, want %q", gotPath, tc.want)
			}
		})
	}

	if relPath != absPath {
		t.Fatalf("helper returned %q, want %q", relPath, absPath)
	}
}

func TestLoadRegistryNamePrefersLocalAndReturnsAbsolutePath(t *testing.T) {
	home := t.TempDir()
	cwd := projectTree(t)
	global := writeRegistryWorkflow(t, home, "shadow.yaml", `name: global
runtime: cmd-echo
model: m1
tasks:
  - id: a
    command: echo global
`)
	local := writeLocalRegistryWorkflow(t, cwd, "shadow.yaml", `name: local
runtime: cmd-echo
model: m1
tasks:
  - id: a
    command: echo local
`)

	wf, _, gotPath, err := Load(home, cwd, "shadow")
	if err != nil {
		t.Fatalf("Load(shadow): %v", err)
	}
	if gotPath != local {
		t.Fatalf("path = %q, want local path %q", gotPath, local)
	}
	if string(wf.ID) != "local" {
		t.Fatalf("workflow id = %q, want local", wf.ID)
	}
	if gotPath == global {
		t.Fatal("global registry entry won, want local shadow")
	}
}

func TestListMergesLocalAndGlobalWithShadowing(t *testing.T) {
	home := t.TempDir()
	cwd := projectTree(t)
	writeRegistryWorkflow(t, home, "global.yaml", registryBody)
	writeRegistryWorkflow(t, home, "shadow.yaml", registryBody)
	local := writeLocalRegistryWorkflow(t, cwd, "shadow.yaml", registryBody)
	writeLocalRegistryWorkflow(t, cwd, "local.yaml", registryBody)

	refs, err := List(home, cwd)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
		if ref.Name == "shadow" && ref.Path != local {
			t.Fatalf("shadow path = %q, want local %q", ref.Path, local)
		}
	}
	want := []string{"global", "local", "shadow"}
	if !slices.Equal(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestLoadAnchorsSubWorkflowPathRefsToParentDir(t *testing.T) {
	home := t.TempDir()
	cwd := projectTree(t)
	parentPath := writeWorkflow(t, cwd, "parent.yaml", `name: parent
tasks:
  - id: child
    workflow: subs/child.yaml
`)
	writeWorkflow(t, cwd, filepath.Join("subs", "child.yaml"), `name: child
tasks:
  - id: a
    command: echo hi
`)

	wf, _, gotPath, err := Load(home, cwd, filepath.Base(parentPath))
	if err != nil {
		t.Fatalf("Load(parent): %v", err)
	}
	if gotPath != parentPath {
		t.Fatalf("parent path = %q, want %q", gotPath, parentPath)
	}
	child := wf.Subs["child"]
	if child == nil {
		t.Fatal("child workflow was not linked")
	}
	if string(child.ID) != "child" {
		t.Fatalf("child id = %q, want child", child.ID)
	}
}

func TestLoadMissingWorkflowReturnsNotFound(t *testing.T) {
	home := t.TempDir()
	cwd := projectTree(t)

	if _, _, _, err := Load(home, cwd, "ghost"); err == nil {
		t.Fatal("Load(ghost) = nil error, want not found")
	}
}

func writeWorkflow(t *testing.T, root, rel, body string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

func writeRegistryWorkflow(t *testing.T, home, rel, body string) string {
	t.Helper()
	return writeWorkflow(t, filepath.Join(home, "workflows"), rel, body)
}

func writeLocalRegistryWorkflow(t *testing.T, root, rel, body string) string {
	t.Helper()
	return writeWorkflow(t, filepath.Join(root, ".loom", "workflows"), rel, body)
}

func projectTree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("test"), 0o644); err != nil {
		t.Fatalf("write .git marker: %v", err)
	}
	return root
}
