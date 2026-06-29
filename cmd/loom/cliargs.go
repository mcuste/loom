package main

import (
	"github.com/spf13/cobra"
)

// addParamFlags uses StringArrayVarP (not StringSliceVarP) so commas inside
// values are preserved verbatim.
func addParamFlags(cmd *cobra.Command, params *[]string) {
	cmd.Flags().StringArrayVarP(params, "param", "p", nil,
		"set a workflow parameter (repeatable), e.g. -p env=prod")
}

// firstArg returns the optional single positional argument (a workflow filter
// for `runs`/`schedule ls`/`schedule sync`), or "" when absent.
func firstArg(args []string) string {
	if len(args) == 1 {
		return args[0]
	}
	return ""
}
