package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/config"
)

// newDBCmd 聚合持久化运维子命令（P6.5）。与顶层 k8s-ai migrate 分工明确：
// db migrate 只升级 schema，migrate 负责把三期历史数据导入四期 Store。
func newDBCmd(deps Dependencies) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Platform database operations (upgrade Job / initContainer entrypoint)",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newDBMigrateCmd(deps))
	return cmd
}

// newDBMigrateCmd 把 schema 升到当前版本（幂等、只动 schema）。
func newDBMigrateCmd(deps Dependencies) *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply the platform database schema (idempotent, schema only)",
		Long: "Upgrade the configured store (platform.store.driver) to the current schema version.\n" +
			"\n" +
			"Scope: schema only. It does not import Phase-3 data (use the top-level\n" +
			"'k8s-ai migrate') and it never creates the database itself; the schema and\n" +
			"its character set must already exist. Re-running is a no-op, so this command\n" +
			"is meant to be used as a pre-upgrade Job or initContainer.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd))
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			// --db 只是 sqlite 兜底路径的便捷覆盖，mysql 走 DSN。
			if cmd.Flags().Changed("db") && !cfg.Platform.Store.IsMySQL() {
				cfg.Platform.Store.SQLitePath = dbPath
			}
			st, target, err := openPlatformStore(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			w := cmd.OutOrStdout()
			before, expected, err := st.MigrationStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("read schema version: %w", err)
			}
			if err := st.Migrate(cmd.Context()); err != nil {
				return fmt.Errorf("migrate schema: %w", err)
			}
			after, expected, err := st.MigrationStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("read schema version: %w", err)
			}
			if after < expected {
				return fmt.Errorf("schema still at version %d, expected %d", after, expected)
			}
			fmt.Fprintf(w, "db migrate ok: driver=%s target=%s schema_version=%d->%d (expected %d)\n",
				driverName(cfg), target, before, after, expected)
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "SQLite database path override (sqlite driver only; default from config platform.store.sqlite_path)")
	return cmd
}

func driverName(cfg *config.Config) string {
	if cfg.Platform.Store.IsMySQL() {
		return config.StoreDriverMySQL
	}
	return config.StoreDriverSQLite
}
