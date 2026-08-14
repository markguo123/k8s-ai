package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd(deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), deps.Version)
			return nil
		},
	}
}
