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
	var (
		force  bool
		useCLI bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate a default config file (server static config, or CLI config with --cli)",
		Long: "Write a config template.\n\n" +
			"Default: the platform static config (config.yaml), which carries only the\n" +
			"platform: launch section; the seven dynamic settings groups live in the\n" +
			"database and are maintained in the web settings page.\n\n" +
			"With --cli: the terminal config (~/.k8s-ai/cli.yaml) with the full engine\n" +
			"keys for k8s-ai chat / k8s-ai scan. k8s-ai server never reads or writes it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kind := config.TemplateServer
			defaultPath := config.DefaultConfigFile
			if useCLI {
				kind = config.TemplateCLI
				defaultPath = config.DefaultCLIConfigFile
			}
			path := cmd.Flag("config").Value.String()
			if path == "" {
				p, err := defaultPath()
				if err != nil {
					return err
				}
				path = p
			}
			written, err := config.Init(path, kind, force)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "config written to %s\n", written)
			if kind == config.TemplateCLI {
				fmt.Fprintln(cmd.OutOrStdout(), "terminal config: read only by k8s-ai chat / k8s-ai scan; k8s-ai server neither reads nor writes it")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	cmd.Flags().BoolVar(&useCLI, "cli", false, "write the terminal CLI config template (default path ~/.k8s-ai/cli.yaml)")
	return cmd
}

func newConfigValidateCmd(deps Dependencies) *cobra.Command {
	var (
		modeStr          string
		skipConnectivity bool
	)
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate configuration and cluster connectivity",
		Long: "Validate the resolved configuration file and warn about keys this build does\n" +
			"not recognise.\n\n" +
			"--mode server (default): platform static config (config.yaml); keys of the\n" +
			"terminal engine sections are reported as not consumed by the platform.\n" +
			"--mode cli: terminal config (cli.yaml, falling back to config.yaml) and the\n" +
			"full engine keys are validated.\n\n" +
			"Cluster connectivity is checked last; use --skip-connectivity to validate\n" +
			"configuration only.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := config.ParseValidateMode(modeStr)
			if err != nil {
				return err
			}
			opts := loadOptionsFromFlags(cmd, mode == config.ValidateModeCLI)
			path, err := config.ResolveConfigFile(opts.ConfigFile, opts.CLI)
			if err != nil {
				return err
			}
			warnings, err := config.Diagnostics(path, mode)
			if err != nil {
				return err
			}
			for _, w := range warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			cfg, err := deps.LoadConfig(opts)
			if err != nil {
				return err
			}
			if skipConnectivity {
				fmt.Fprintf(cmd.OutOrStdout(), "config OK (%s mode)\n", mode)
				return nil
			}
			version, err := deps.NewScan().Validate(cmd.Context(), scanOptionsFromConfig(cfg))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "config OK: server version %s\n", version)
			return nil
		},
	}
	cmd.Flags().StringVar(&modeStr, "mode", string(config.ValidateModeServer), "validation profile (server|cli)")
	cmd.Flags().BoolVar(&skipConnectivity, "skip-connectivity", false, "validate configuration only, do not contact the cluster")
	return cmd
}
