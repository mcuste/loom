package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/workflow"
)

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
	refs, err := listRegistryWorkflows()
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

// descWidth caps the description column so a long first line does not push the
// resolved path far off to the right.
const descWidth = 60

// doWorkflowsList prints every registry workflow in three space-aligned columns
// (name, a best-effort truncated description, and the resolved file path so a
// shadowed name shows which root won), sorted by name. A parse error or absent
// description leaves the description column blank; an absent registry root lists
// nothing. Columns are aligned with a tabwriter so the output reads as a table.
func doWorkflowsList(w io.Writer) error {
	refs, err := listRegistryWorkflows()
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
