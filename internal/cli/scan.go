package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/report"
)

// scanFlags 是 scan 与 scan pod 共用的命令参数。
type scanFlags struct {
	failOn       string
	since        time.Duration
	reportFormat string
	reportMode   string
	verbose      bool
}

func registerScanFlags(cmd *cobra.Command, f *scanFlags) {
	cmd.Flags().StringVar(&f.failOn, "fail-on", "", "exit 2 when a finding reaches severity (INFO|LOW|MEDIUM|HIGH|CRITICAL)")
	cmd.Flags().StringVar(&f.reportFormat, "format", "markdown", "terminal report format (markdown|json|yaml)")
	cmd.Flags().StringVar(&f.reportMode, "report-mode", "", "report destination: none|latest|daily (default: none for targeted, latest for full scan)")
	cmd.Flags().String("kubeconfig", "", "path to kubeconfig")
	cmd.Flags().String("context", "", "kubeconfig context")
	cmd.Flags().StringP("namespace", "n", "", "namespace to scan (default: all namespaces)")
	cmd.Flags().DurationVar(&f.since, "since", 0, "only fetch logs newer than this duration (e.g. 1h)")
	cmd.Flags().BoolVar(&f.verbose, "verbose", false, "print the full report instead of the one-screen summary")
}

// newScanCmd 全集群/命名空间扫描。
func newScanCmd(deps Dependencies) *cobra.Command {
	var f scanFlags
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan the Kubernetes cluster",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScan(deps, cmd, "", &f)
		},
	}
	registerScanFlags(cmd, &f)
	cmd.AddCommand(newScanPodCmd(deps))
	return cmd
}

// newScanPodCmd 单 Pod 目标扫描（P9.1 提前实现）。
func newScanPodCmd(deps Dependencies) *cobra.Command {
	var f scanFlags
	cmd := &cobra.Command{
		Use:   "pod <name>",
		Short: "Scan a single pod (requires --namespace/-n)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScan(deps, cmd, args[0], &f)
		},
	}
	registerScanFlags(cmd, &f)
	return cmd
}

// runScan 是 scan 与 scan pod 共用的执行逻辑。
func runScan(deps Dependencies, cmd *cobra.Command, podTarget string, f *scanFlags) error {
	switch f.reportFormat {
	case "markdown", "json", "yaml":
	default:
		return fmt.Errorf("invalid --format %q (markdown|json|yaml)", f.reportFormat)
	}
	switch f.reportMode {
	case "", "none", "latest", "daily":
	default:
		return fmt.Errorf("invalid --report-mode %q (none|latest|daily)", f.reportMode)
	}
	cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd))
	if err != nil {
		return err
	}
	opts := scanOptionsFromConfig(cfg)
	opts.Since = f.since
	opts.ReportFormat = f.reportFormat
	opts.PodTarget = podTarget
	if podTarget != "" && opts.Namespace == "" {
		return fmt.Errorf("scan pod requires --namespace/-n")
	}
	// 报告目的地默认值：单 Pod/命名空间目标扫描只打印终端，全集群写报告。
	switch {
	case f.reportMode != "":
		opts.ReportMode = f.reportMode
	case podTarget != "" || opts.Namespace != "":
		opts.ReportMode = "none"
	default:
		opts.ReportMode = "latest"
	}
	if f.failOn != "" {
		sev, err := model.ParseSeverity(f.failOn)
		if err != nil {
			return err
		}
		opts.FailOn = sev
	}
	result, err := deps.NewScan().Run(cmd.Context(), opts)
	if err != nil {
		return err
	}
	// 默认输出一屏摘要（markdown 且非 --verbose）；否则输出完整报告。
	if opts.ReportFormat == "markdown" && !f.verbose {
		fmt.Fprint(cmd.OutOrStdout(), report.RenderTerminal(result))
	} else {
		data, err := report.RendererFor(opts.ReportFormat).Render(result)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), string(data))
	}
	if len(result.ReportPaths) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nReport: %s\n", strings.Join(result.ReportPaths, ", "))
	}
	if opts.FailOn != "" && reachedSeverity(result.Findings, opts.FailOn) {
		return &ExitError{Code: 2, Err: fmt.Errorf("scan found issues at severity %s or higher", opts.FailOn)}
	}
	return nil
}

func scanOptionsFromConfig(cfg *config.Config) model.ScanOptions {
	return model.ScanOptions{
		Kubeconfig:          cfg.Kubernetes.Kubeconfig,
		Context:             cfg.Kubernetes.Context,
		Namespace:           cfg.Kubernetes.Namespace,
		RequestTimeout:      cfg.Kubernetes.Timeout,
		QPS:                 cfg.Kubernetes.QPS,
		Burst:               cfg.Kubernetes.Burst,
		Timeout:             cfg.Scan.Timeout,
		Concurrency:         cfg.Scan.Concurrency,
		Phase2Concurrency:   cfg.Scan.Phase2Concurrency,
		CollectLogs:         cfg.Scan.CollectLogs,
		CollectPreviousLogs: cfg.Scan.CollectPreviousLogs,
		CollectEvents:       cfg.Scan.CollectEvents,
		MaxLogLines:         cfg.Scan.MaxLogLines,
		MaxLogBytes:         cfg.Scan.MaxLogBytes,
		MaxLogLineBytes:     cfg.Scan.MaxLogLineBytes,
		PodLogsTimeout:      cfg.Scan.PodLogsTimeout,
		ReportDirectory:     cfg.Report.Directory,
		ReportFormat:        cfg.Report.Format,
		RulesEnabled:        cfg.Rules.Enabled,
		RulesDisabled:       cfg.Rules.Disabled,
		LLM: model.LLMOptions{
			Enabled:         cfg.LLM.Enabled,
			Endpoint:        cfg.LLM.Endpoint,
			APIKey:          cfg.LLM.APIKey,
			Model:           cfg.LLM.Model,
			Temperature:     cfg.LLM.Temperature,
			MaxTokens:       cfg.LLM.MaxTokens,
			MaxInputTokens:  cfg.LLM.MaxInputTokens,
			MaxTotalTokens:  cfg.LLM.MaxTotalTokens,
			MaxFindings:     cfg.LLM.MaxFindings,
			Timeout:         cfg.LLM.Timeout,
			DisableThinking: cfg.LLM.DisableThinking,
		},
	}
}

func reachedSeverity(findings []model.Finding, threshold model.Severity) bool {
	thresholdRank := model.SeverityRank(threshold)
	for _, f := range findings {
		if model.SeverityRank(f.Severity) >= thresholdRank {
			return true
		}
	}
	return false
}
