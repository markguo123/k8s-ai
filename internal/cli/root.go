// Package cli is the presentation layer. It parses commands and flags and
// delegates all business logic to service.ScanService (AGENTS.md).
package cli

import (
	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/service"
)

// Dependencies wires the CLI to application services (test seams).
type Dependencies struct {
	Version    string
	LoadConfig func(config.LoadOptions) (*config.Config, error)
	NewScan    func() service.ScanService
}

// NewRootCmd builds the k8s-ai command tree.
func NewRootCmd(deps Dependencies) *cobra.Command {
	root := &cobra.Command{
		Use:           "k8s-ai",
		Short:         "Kubernetes AI inspection and diagnosis tool",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "config file (default ~/.k8s-ai/config.yaml)")
	root.AddCommand(newVersionCmd(deps))
	root.AddCommand(newConfigCmd(deps))
	root.AddCommand(newScanCmd(deps))
	return root
}

func loadOptionsFromFlags(cmd *cobra.Command) config.LoadOptions {
	overrides := map[string]any{}
	flagKeys := []struct{ flag, key string }{
		{"kubeconfig", "kubernetes.kubeconfig"},
		{"context", "kubernetes.context"},
		{"namespace", "kubernetes.namespace"},
	}
	for _, fk := range flagKeys {
		if cmd.Flags().Changed(fk.flag) {
			if v, err := cmd.Flags().GetString(fk.flag); err == nil {
				overrides[fk.key] = v
			}
		}
	}
	return config.LoadOptions{
		ConfigFile: cmd.Flag("config").Value.String(),
		Overrides:  overrides,
	}
}
