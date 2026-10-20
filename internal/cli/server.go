package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/authz"
	"github.com/k8s-ai/k8s-ai/internal/cluster"
	"github.com/k8s-ai/k8s-ai/internal/server"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// newServerCmd 启动最小化 HTTP 服务（一期 1.2）。
func newServerCmd(deps Dependencies) *cobra.Command {
	var addr, dbPath string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the minimal HTTP server (healthz/version/async scans)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.Default()
			path, err := expandHomePath(dbPath)
			if err != nil {
				return err
			}
			st, err := store.OpenSQLite(path)
			if err != nil {
				return fmt.Errorf("open store: %w", err)
			}
			defer func() { _ = st.Close() }()
			if err := st.Migrate(cmd.Context()); err != nil {
				return fmt.Errorf("migrate store: %w", err)
			}
			authSvc := auth.New(st.Users(), st.Roles())
			if err := authSvc.Bootstrap(cmd.Context(), os.Getenv("K8S_AI_ADMIN_PASSWORD")); err != nil {
				return fmt.Errorf("bootstrap admin: %w (first start requires K8S_AI_ADMIN_PASSWORD)", err)
			}
			// 四期平台底座：配置中心与系统事件审计由同一 Store 装配（P1.10/P1.11）。
			settingsSvc := settings.New(st)
			auditSvc := service.NewAuditService(st)
			// P3.1-P3.3：凭据加密密钥只从环境变量读取（P6.12 约定）。
			// 未配置 K8S_AI_ENC_KEY 时集群端点返回 501，其余平台能力不受影响。
			var clusterSvc *cluster.Service
			if cipher, err := cluster.NewCipherFromEnv(); err == nil {
				clusterSvc = cluster.New(st.Clusters(), st.Credentials(), cipher,
					cluster.WithAudit(func(ctx context.Context, e store.AuditEvent) error {
						return auditSvc.Record(ctx, e)
					}))
			} else {
				logger.Warn("cluster endpoints disabled", "reason", err.Error())
			}
			// P3.5：Skill 平台装配——先写内置种子，再从 Store 重建内存 registry。
			skillPlat := service.NewSkillPlatform(st.Skills())
			if err := skillPlat.SeedBuiltins(cmd.Context()); err != nil {
				return fmt.Errorf("seed skills: %w", err)
			}
			if err := skillPlat.Reload(cmd.Context()); err != nil {
				return fmt.Errorf("reload skills: %w", err)
			}
			// P3.11：授权面（AuthzGrant / ApiToken）由同一 Store 装配。
			authzSvc := authz.New(st)
			srv := &http.Server{
				Addr:              addr,
				Handler:           server.NewWithPlatform(deps.NewScan(), authSvc, settingsSvc, auditSvc).WithClusters(clusterSvc).WithSkills(skillPlat).WithAuthz(authzSvc).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			logger.Info("k8s-ai server listening", "addr", addr, "db", path)
			errCh := make(chan error, 1)
			go func() { errCh <- srv.ListenAndServe() }()
			select {
			case <-cmd.Context().Done():
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = srv.Shutdown(shutdownCtx)
				return nil
			case err := <-errCh:
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			}
		},
	}
	cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address (e.g. :8080 or 0.0.0.0:8080)")
	cmd.Flags().StringVar(&dbPath, "db", "~/.k8s-ai/k8s-ai.db", "SQLite database path (first start reads K8S_AI_ADMIN_PASSWORD)")
	return cmd
}

func expandHomePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("store path must not be empty")
	}
	if path == "~" || !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, path[2:]), nil
}
