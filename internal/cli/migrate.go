package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/cluster"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// newMigrateCmd 把三期遗留数据导入四期 Store（计划 P1.12）：
// config / session / audit / skill / report。
//
// 目标库默认是本地 SQLite（--db，三期单机场景）；上平台的机器加 --platform，
// 按 platform.store.* 连平台库（MySQL 主路径），这样清空容器卷后仍能把三期历史
// 会话导进真正的事实源（P8.0）。
func newMigrateCmd(deps Dependencies) *cobra.Command {
	var dbPath, sessionsDir, auditDir, reportsDir string
	var usePlatformStore, forceSessions bool
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Import Phase-3 config/session/audit/skill/report data",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// 三期旧配置一次性导入：源固定是平台静态 config.yaml，不走 CLI 直用面。
			cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd, false))
			if err != nil {
				return err
			}
			// 目标库：--platform 走 platform.store.*（平台部署的唯一口径），否则
			// 落到显式 --db 指定的 SQLite 文件。两者互斥，避免"以为在导平台库、
			// 其实写了本地文件"。
			if usePlatformStore && cmd.Flags().Changed("db") {
				return errors.New("migrate: --platform 与 --db 互斥，平台库目标由 platform.store.* 决定")
			}
			var st store.Store
			var target string
			if usePlatformStore {
				st, target, err = openPlatformStore(cfg)
				if err != nil {
					return err
				}
			} else {
				path, err := expandHomePath(dbPath)
				if err != nil {
					return err
				}
				st, err = store.OpenSQLite(path)
				if err != nil {
					return fmt.Errorf("open store: %w", err)
				}
				target = "sqlite:" + path
			}
			defer func() { _ = st.Close() }()

			opts := service.MigrateOptions{
				Store:      st,
				Config:     cfg,
				SessionDir: firstNonEmpty(sessionsDir, cfg.Agent.SessionDir),
				AuditDir:   firstNonEmpty(auditDir, cfg.Agent.AuditDir),
				ReportsDir: firstNonEmpty(reportsDir, cfg.Report.Directory),
				Force:      forceSessions,
			}
			// P3.9：config 迁移的敏感键（api_key/kubeconfig/mcp）同样走静态加密，
			// 与 cluster 共用 K8S_AI_ENC_KEY。未配置时 config 里若含非默认敏感键，
			// 导入整批报错，绝不落明文。
			if cipher, err := cluster.NewCipherFromEnv(); err == nil {
				opts.Cipher = cipher
			} else {
				slog.Warn("migrate: 凭据加密器未配置，含敏感键的 config 导入将被拒绝", "reason", err.Error())
			}
			rep, err := service.Migrate(cmd.Context(), opts)
			if err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
			writeMigrateReport(cmd, target, rep)

			if rep.UnmigratableCount() > 0 {
				return &ExitError{
					Code: 3,
					Err:  errors.New("部分三期数据未自动迁移，请查看上方明细"),
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "~/.k8s-ai/k8s-ai.db", "SQLite database path")
	cmd.Flags().StringVar(&sessionsDir, "sessions-dir", "", "Phase-3 session directory (default from config agent.session_dir)")
	cmd.Flags().StringVar(&auditDir, "audit-dir", "", "Phase-3 audit directory (default from config agent.audit_dir)")
	cmd.Flags().StringVar(&reportsDir, "reports-dir", "", "Phase-3 report directory (default from config report.directory)")
	cmd.Flags().BoolVar(&usePlatformStore, "platform", false, "Target the platform store from platform.store.* (MySQL/SQLite) instead of a local SQLite file")
	cmd.Flags().BoolVar(&forceSessions, "force", false, "Re-import Phase-3 sessions even if the directory was already imported (may resurrect sessions deleted in the platform)")
	return cmd
}

func writeMigrateReport(cmd *cobra.Command, target string, rep service.MigrateReport) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "migration report:")
	fmt.Fprintf(w, "  target      %s\n", target)
	fmt.Fprintf(w, "  config      imported=%d defaults=%d missing=%d\n",
		rep.Config.Imported, rep.Config.Defaults, rep.Config.Missing)
	reportSource(w, "sessions", rep.Sessions)
	reportSource(w, "audit", rep.Audit)
	reportSource(w, "skills", rep.Skills)
	reportSource(w, "reports", rep.Reports)
	if rep.UnmigratableCount() == 0 {
		fmt.Fprintln(w, "  all sources migrated (reports kept in place).")
		return
	}
	fmt.Fprintln(w, "  unmigratable items (not dropped, review manually):")
	for _, item := range unmigratableItems(rep) {
		fmt.Fprintf(w, "    - %s: %s\n", item.Source, item.Reason)
	}
}

func reportSource(out io.Writer, name string, r service.SourceReport) {
	fmt.Fprintf(out, "  %-9s found=%d imported=%d skipped=%d kept=%d\n",
		name, r.Found, r.Imported, r.Skipped, r.Kept)
	if r.Note != "" {
		fmt.Fprintf(out, "            %s\n", r.Note)
	}
}

func unmigratableItems(rep service.MigrateReport) []service.UnmigratedItem {
	var items []service.UnmigratedItem
	items = append(items, rep.Sessions.Unmigratable...)
	items = append(items, rep.Audit.Unmigratable...)
	items = append(items, rep.Skills.Unmigratable...)
	items = append(items, rep.Reports.Unmigratable...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Source == items[j].Source {
			return items[i].Reason < items[j].Reason
		}
		return items[i].Source < items[j].Source
	})
	return items
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
