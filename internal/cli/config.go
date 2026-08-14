package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/config"
)

func newConfigCmd(deps Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}
	cmd.AddCommand(newConfigInitCmd())
	cmd.AddCommand(newConfigValidateCmd(deps))
	return cmd
}

func newConfigInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a default config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := cmd.Flag("config").Value.String()
			if path == "" {
				p, err := config.DefaultConfigFile()
				if err != nil {
					return err
				}
				path = p
			}
			written, err := config.Init(path, force)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "config written to %s\n", written)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

func newConfigValidateCmd(deps Dependencies) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration and cluster connectivity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd))
			if err != nil {
				return err
			}
			version, err := deps.NewScan().Validate(cmd.Context(), scanOptionsFromConfig(cfg))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "config OK: server version %s\n", version)
			return nil
		},
	}
}
