package main

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/tui"
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
	home, err := loomHome()
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	refs, err := listRegistryWorkflows(home)
	if err != nil {
		return nil, cobra.ShellCompDirectiveDefault
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.Name
	}
	return names, cobra.ShellCompDirectiveDefault
}

// newWorkflowsCmd is the parent for inspecting the workflow registries.
// Its `ls` subcommand lists the workflows runnable by name, merged from
// the local .loom/workflows and global $LOOM_HOME/workflows roots.
func newWorkflowsCmd(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Inspect the local and global workflow registries",
	}
	cmd.AddCommand(newWorkflowsListCmd(env))
	return cmd
}

func newWorkflowsListCmd(env *cliEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ls",
		Aliases: []string{"list"},
		Short:   "List registry workflows by name",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return doWorkflowsList(cmd.OutOrStdout(), env.home)
		},
	}
	return cmd
}

// doWorkflowsList prints every registry workflow in three space-aligned columns
// (name, a best-effort truncated description, and the resolved file path so a
// shadowed name shows which root won), sorted by name. A parse error or absent
// description leaves the description column blank; an absent registry root lists
// nothing. Columns are aligned with a tabwriter so the output reads as a table.
func doWorkflowsList(w io.Writer, home string) error {
	refs, err := listRegistryWorkflows(home)
	if err != nil {
		return err
	}
	return tui.WorkflowsTable(w, refs)
}
