package main

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/mcuste/loom/pkg/store"
	"github.com/mcuste/loom/pkg/tui"
)

// newRunsCmd is the parent for inspecting past runs. Invoked bare (optionally
// with a workflow filter) it opens the interactive browser on a terminal and
// falls back to the plain table when piped; its `ls` and `show` subcommands are
// the explicit non-interactive views. A workflow name that is not a subcommand
// is treated as the browser's filter (cobra runs the parent when args[0] does
// not name a child), so `loom runs deploy` still works.
func newRunsCmd() *cobra.Command {
	var (
		plain bool
		limit int
	)
	cmd := &cobra.Command{
		Use:   "runs [workflow]",
		Short: "Browse past runs in an interactive TUI (a table when piped)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doRuns(cmd.OutOrStdout(), firstArg(args), plain, limit)
		},
	}
	cmd.Flags().BoolVar(&plain, "plain", false,
		"print a plain table instead of opening the interactive browser")
	cmd.Flags().IntVarP(&limit, "limit", "n", 0,
		"show at most N most-recent runs (0 = all)")
	cmd.AddCommand(newRunsListCmd(), newRunsShowCmd())
	return cmd
}

func newRunsListCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "ls [workflow]",
		Aliases: []string{"list"},
		Short:   "List past runs as a plain table (never opens the browser)",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doRuns(cmd.OutOrStdout(), firstArg(args), true, limit)
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 0,
		"show at most N most-recent runs (0 = all)")
	return cmd
}

func newRunsShowCmd() *cobra.Command {
	var (
		task    string
		summary bool
	)
	cmd := &cobra.Command{
		Use:   "show <run-id>",
		Short: "Print a stored run inline: header, per-task prompts and outputs",
		Long: "Print a stored run in full: a header, a per-task summary table, " +
			"and each task's dependencies, prompt or command, output, and error.\n\n" +
			"Use --summary for just the header and table, or --task <id> for a " +
			"single task's prompt and output. The run id may be the full id, the " +
			"short suffix shown by `loom runs ls`, or a leading timestamp prefix.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doShow(cmd.OutOrStdout(), args[0], task, summary)
		},
	}
	cmd.Flags().StringVarP(&task, "task", "t", "",
		"print only this task's prompt and output")
	cmd.Flags().BoolVarP(&summary, "summary", "s", false,
		"print only the header and per-task summary table (omit prompts and outputs)")
	return cmd
}

// firstArg returns the optional positional workflow filter, or "" when absent.
func firstArg(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return ""
}

// doRuns gathers the run index and renders it: the interactive browser on a
// rich terminal, a plain table otherwise (piped, --plain, or no runs yet). An
// optional workflow argument narrows the index to one workflow; limit > 0 keeps
// only the most-recent N runs (the index is already newest-first).
func doRuns(w io.Writer, workflowFilter string, plain bool, limit int) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	var headers []store.RunHeader
	if workflowFilter != "" {
		headers, err = store.ListRuns(home, workflowFilter)
	} else {
		headers, err = store.ListAllRuns(home)
	}
	if err != nil {
		return err
	}
	if limit > 0 && len(headers) > limit {
		headers = headers[:limit]
	}
	if plain || len(headers) == 0 || !tui.Rich(w) {
		return tui.RunsTable(w, headers)
	}
	return tui.Browse(w, headers)
}

// doShow resolves runID (the literal "latest" follows the most recent run) to
// a record and prints it: a single task's body when task is set, the header
// and summary table when summary is set, otherwise the full run. Run-id lookup
// is shared with `loom resume`.
func doShow(w io.Writer, runID, task string, summary bool) error {
	home, err := loomHome()
	if err != nil {
		return err
	}
	path, err := findRunRecord(home, runID)
	if err != nil {
		return err
	}
	rec, err := store.Load(path)
	if err != nil {
		return err
	}
	if task != "" {
		return tui.ShowTask(w, rec, task)
	}
	return tui.ShowRun(w, rec, !summary)
}
