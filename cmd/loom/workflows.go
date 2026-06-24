package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/workflow"
)

// resolveWorkflowRef maps a `loom run`/`check` positional argument to a workflow
// YAML path. The classification is syntactic and cwd-independent: a path
// separator always wins (so a Windows drive path like `C:\wf.yaml` is a PATH
// despite its ':'); absent a separator, arg is a registry NAME when it contains
// ':' OR does not end in .yaml/.yml; otherwise it is a filesystem PATH and is
// returned verbatim
// without consulting $LOOM_HOME. A name resolves under $LOOM_HOME/workflows with
// ':' as the hierarchy separator: a trailing .yaml/.yml on the final component is
// stripped, then '.yaml' (fallback '.yml') is appended. Empty, '.', and '..'
// components are rejected, as is any name that would escape the workflows root.
func resolveWorkflowRef(arg string) (string, error) {
	if !isRegistryName(arg) {
		return arg, nil
	}
	home, err := loomHome()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, "workflows")

	parts := strings.Split(arg, ":")
	last := len(parts) - 1
	parts[last] = strings.TrimSuffix(parts[last], ".yaml")
	parts[last] = strings.TrimSuffix(parts[last], ".yml")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `/\`) {
			return "", fmt.Errorf("invalid workflow name %q: component %q not allowed", arg, p)
		}
	}

	// The per-component checks above already forbid any component that could
	// climb out of the root ("", ".", "..", or one containing a separator), so
	// the join cannot escape lexically and needs no further traversal guard.
	stem := filepath.Join(append([]string{root}, parts...)...)

	for _, ext := range []string{".yaml", ".yml"} {
		cand := stem + ext
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", fmt.Errorf("workflow %q not found under %s; run `loom workflows ls` to list available workflows", arg, root)
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
	home, err := loomHome()
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	refs, err := walkRegistry(filepath.Join(home, "workflows"))
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.name
	}
	return names, cobra.ShellCompDirectiveDefault
}

// newWorkflowsCmd is the parent for inspecting the $LOOM_HOME/workflows registry.
// Its `ls` subcommand lists the workflows runnable by name.
func newWorkflowsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Inspect the $LOOM_HOME/workflows registry",
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

// doWorkflowsList prints every registry workflow as its colon-name plus a
// best-effort description, sorted by name. A parse error blanks the description
// rather than failing the listing; an absent registry root lists nothing.
func doWorkflowsList(w io.Writer) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	refs, err := walkRegistry(filepath.Join(home, "workflows"))
	if err != nil {
		return err
	}
	for _, r := range refs {
		desc := ""
		if wf, perr := workflow.ParseFile(r.path); perr == nil {
			desc = firstLine(wf.Description)
		}
		if desc == "" {
			if _, err := fmt.Fprintln(w, r.name); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\n", r.name, desc); err != nil {
			return err
		}
	}
	return nil
}

// firstLine returns s up to its first newline, trimmed, so a multi-line
// description collapses to a single listing column.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
