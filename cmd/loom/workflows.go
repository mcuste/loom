package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/workflow"
)

// resolveWorkflowRef maps a `loom run`/`check` positional argument to a workflow
// YAML path. The classification is syntactic and cwd-independent: a path
// separator always wins (so a Windows drive path like `C:\wf.yaml` is a PATH
// despite its ':'); absent a separator, arg is a registry NAME when it contains
// ':' OR does not end in .yaml/.yml; otherwise it is a filesystem PATH and is
// returned verbatim without consulting any registry. A name resolves against the
// ordered registry roots (project-local `.loom/workflows` walking up to the git
// root, then $LOOM_HOME/workflows) with ':' as the hierarchy separator: a
// trailing .yaml/.yml on the final component is stripped, then '.yaml' (fallback
// '.yml') is appended. The first existing file along the search order wins
// (nearest shadows global). Empty, '.', and '..' components are rejected, as is
// any name that would escape a workflows root.
func resolveWorkflowRef(arg string) (string, error) {
	if !isRegistryName(arg) {
		return arg, nil
	}
	parts, err := splitWorkflowName(arg)
	if err != nil {
		return "", err
	}
	roots, err := registrySearchRoots()
	if err != nil {
		return "", err
	}

	for _, root := range roots {
		// The per-component checks in splitWorkflowName already forbid any
		// component that could climb out of the root ("", ".", "..", or one
		// containing a separator), so the join cannot escape lexically and
		// needs no further traversal guard.
		stem := filepath.Join(append([]string{root}, parts...)...)
		for _, ext := range []string{".yaml", ".yml"} {
			cand := stem + ext
			if _, err := os.Stat(cand); err == nil {
				return cand, nil
			}
		}
	}
	return "", fmt.Errorf("workflow %q not found in any registry (searched %s); run `loom workflows ls` to list available workflows", arg, strings.Join(roots, ", "))
}

// splitWorkflowName parses a registry name into its colon-separated path
// components, stripping a trailing .yaml/.yml on the final component. It rejects
// empty, '.', and '..' components and any component containing a path separator,
// so the result can be joined under a registry root without escaping it.
func splitWorkflowName(arg string) ([]string, error) {
	parts := strings.Split(arg, ":")
	last := len(parts) - 1
	parts[last] = strings.TrimSuffix(parts[last], ".yaml")
	parts[last] = strings.TrimSuffix(parts[last], ".yml")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `/\`) {
			return nil, fmt.Errorf("invalid workflow name %q: component %q not allowed", arg, p)
		}
	}
	return parts, nil
}

// registrySearchRoots returns the ordered list of registry roots searched for a
// name: the project-local `.loom/workflows` dirs walking up from the current
// working directory to the git root, then the global $LOOM_HOME/workflows last.
func registrySearchRoots() ([]string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	roots := localRegistryDirs(cwd)
	home, err := loomHome()
	if err != nil {
		return nil, err
	}
	return append(roots, filepath.Join(home, "workflows")), nil
}

// localRegistryDirs returns the `.loom/workflows` dirs from start walking up to
// and including the git repo root (the first ancestor containing a `.git`). If
// no git root is found up to the filesystem root, only start's local dir is
// returned, so resolution never scans the whole filesystem.
func localRegistryDirs(start string) []string {
	var dirs []string
	dir := start
	for {
		dirs = append(dirs, filepath.Join(dir, ".loom", "workflows"))
		if isGitRoot(dir) {
			return dirs
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root with no git root: only the start
			// dir's local registry is searched.
			return dirs[:1]
		}
		dir = parent
	}
}

// isGitRoot reports whether dir contains a `.git` entry, marking it a git repo
// root. Both a directory (normal repo) and a file (worktree or submodule) count.
func isGitRoot(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// isRegistryName reports whether arg is a registry name rather than a filesystem
// path. A path separator marks a path (so a Windows drive path like `C:\wf.yaml`
// is a path despite its ':'); otherwise a ':' means a name, a .yaml/.yml suffix
// marks a path, and anything else is a flat name.
func isRegistryName(arg string) bool {
	if strings.ContainsAny(arg, `/\`) {
		return false
	}
	if strings.Contains(arg, ":") {
		return true
	}
	return !strings.HasSuffix(arg, ".yaml") && !strings.HasSuffix(arg, ".yml")
}

// completeWorkflowRef is the shell-completion function for the workflow
// positional argument of `loom run`/`run check`. It offers every registry
// workflow's colon-name so `loom run tui<TAB>` expands to `tui_demo`, and
// returns ShellCompDirectiveDefault so the shell still falls back to file-path
// completion for path-mode invocations (`loom run workflows/foo.yaml`). Only the
// first positional arg is a workflow, so later positions complete nothing; any
// registry error degrades silently to plain file completion.
func completeWorkflowRef(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	roots, err := registrySearchRoots()
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	refs, err := walkRegistries(roots)
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.name
	}
	return names, cobra.ShellCompDirectiveDefault
}

// newWorkflowsCmd is the parent for inspecting the workflow registries.
// Its `ls` subcommand lists the workflows runnable by name, merged from
// the local .loom/workflows and global $LOOM_HOME/workflows roots.
func newWorkflowsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Inspect the local and global workflow registries",
	}
	cmd.AddCommand(newWorkflowsListCmd())
	return cmd
}

func newWorkflowsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List registry workflows by name",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doWorkflowsList(cmd.OutOrStdout())
		},
	}
	return cmd
}

// workflowRef is a single registry workflow: its colon-joined name and the
// file it resolves to.
type workflowRef struct {
	name string
	path string
}

// walkRegistry walks root, returning every *.yaml/*.yml file as a workflowRef
// whose name is the path relative to root with '/'->':' and the extension
// stripped, sorted by name. A <stem>.yaml and <stem>.yml collide on one name;
// WalkDir visits lexically (.yaml before .yml), so the first wins and the rest
// are dropped, mirroring the '.yaml'-over-'.yml' preference in
// resolveWorkflowRef. An absent registry root yields no refs.
func walkRegistry(root string) ([]workflowRef, error) {
	var refs []workflowRef
	seen := make(map[string]bool)
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // empty registry: nothing to list
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		ext := filepath.Ext(path)
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := strings.ReplaceAll(strings.TrimSuffix(rel, ext), string(filepath.Separator), ":")
		if seen[name] {
			return nil
		}
		seen[name] = true
		refs = append(refs, workflowRef{name: name, path: path})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].name < refs[j].name })
	return refs, nil
}

// walkRegistries walks each root in order via walkRegistry, merging the results
// into one set keyed by colon-name where the first occurrence wins (nearest root
// shadows the rest), then re-sorts by name. It lets `workflows ls` and
// completion present the effective local+global registry.
func walkRegistries(roots []string) ([]workflowRef, error) {
	var refs []workflowRef
	seen := make(map[string]bool)
	for _, root := range roots {
		rs, err := walkRegistry(root)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			if seen[r.name] {
				continue
			}
			seen[r.name] = true
			refs = append(refs, r)
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].name < refs[j].name })
	return refs, nil
}

// descWidth caps the description column so a long first line does not push the
// resolved path far off to the right.
const descWidth = 60

// doWorkflowsList prints every registry workflow in three space-aligned columns
// (name, a best-effort truncated description, and the resolved file path so a
// shadowed name shows which root won), sorted by name. A parse error or absent
// description leaves the description column blank; an absent registry root lists
// nothing. Columns are aligned with a tabwriter so the output reads as a table.
func doWorkflowsList(w io.Writer) error {
	roots, err := registrySearchRoots()
	if err != nil {
		return err
	}
	refs, err := walkRegistries(roots)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range refs {
		desc := ""
		if wf, perr := workflow.ParseFile(r.path); perr == nil {
			desc = truncate(firstLine(wf.Description), descWidth)
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", r.name, desc, r.path); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// truncate shortens s to at most max runes, appending "..." when it cuts, so a
// long description stays within its column without splitting a multibyte rune.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 3 {
		return string(r[:max])
	}
	return string(r[:max-3]) + "..."
}

// firstLine returns s up to its first newline, trimmed, so a multi-line
// description collapses to a single listing column.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
