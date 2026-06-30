package main

import (
	"io"

	"github.com/spf13/cobra"
)

func newCheckCmd() *cobra.Command {
	var paramArgs []string
	cmd := &cobra.Command{
		Use:   "check <workflow>",
		Short: "Validate a workflow and print its execution plan, without running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return doCheck(cmd.OutOrStdout(), args[0], paramArgs)
		},
	}
	addParamFlags(cmd, &paramArgs)
	return cmd
}

// doCheck runs the shared validation phase only: validate and print the plan,
// then stop without executing.
func doCheck(w io.Writer, path string, paramArgs []string) (err error) {
	wf, _, _, err := loadWorkflow(path)
	if err != nil {
		return err
	}
	r, finish := newRenderer(w)
	defer finish(&err)
	_, err = validateAndPlan(r, wf, paramInputs{cli: paramArgs}, true, nil)
	return err
}
