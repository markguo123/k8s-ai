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
	"github.com/k8s-ai/k8s-ai/internal/sessionapi"
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
			cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd))
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			listenAddr := cfg.Platform.Server.Addr
			if cmd.Flags().Changed("addr") {
				listenAddr = addr
			}
			path := cfg.Platform.Store.SQLitePath
			if cmd.Flags().Changed("db") {
				path = dbPath
			}
			path, err = expandHomePath(path)
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
			if err := authSvc.BootstrapWithName(cmd.Context(), cfg.Platform.Auth.AdminUsername, os.Getenv("K8S_AI_ADMIN_PASSWORD")); err != nil {
				return fmt.Errorf("bootstrap admin: %w (first start requires K8S_AI_ADMIN_PASSWORD)", err)
			}
			// 四期平台底座：配置中心与系统事件审计由同一 Store 装配（P1.10/P1.11）。
			auditSvc := service.NewAuditService(st)
			// P3.1-P3.9：凭据加密密钥只从环境变量读取（P6.12 约定），
			// 集群与配置中心共用同一 cipher。未配置 K8S_AI_ENC_KEY 时：
			// 集群端点返回 501；配置中心包含敏感键的写入整批拒绝（501），
			// 非敏感配置仍可正常读写，其余平台能力不受影响。
			var clusterSvc *cluster.Service
			var settingsOpts []settings.Option
			if cipher, err := cluster.NewCipherFromEnv(); err == nil {
				settingsOpts = append(settingsOpts, settings.WithCipher(cipher))
				clusterSvc = cluster.New(st.Clusters(), st.Credentials(), cipher,
					cluster.WithAudit(func(ctx context.Context, e store.AuditEvent) error {
						return auditSvc.Record(ctx, e)
					}))
			} else {
				logger.Warn("cluster endpoints disabled; settings sensitive keys will be rejected",
					"reason", err.Error())
			}
			settingsSvc := settings.New(st, settingsOpts...)
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
			// Chat 会话与平台 API 使用同一认证服务和 Store；会话归属由
			// sessionapi.AuthenticatedHandler 统一校验。
			chat := service.NewChatWithStoreAndConfig(cfg, st)
			if binder, ok := chat.(service.SkillPlatformBinder); ok {
				binder.SetSkillPlatform(skillPlat)
			}
			platformHandler := server.NewWithPlatform(deps.NewScan(), authSvc, settingsSvc, auditSvc).
				WithClusters(clusterSvc).
				WithSkills(skillPlat).
				WithAuthz(authzSvc).
				Handler()
			// admin 跨用户会话操作（读取/改名/置顶/删除/发消息/审批/取消）统一留痕（U-06）。
			chatAPI := sessionapi.New(chat, cfg.LLM.Model).
				WithAudit(auditSvc.Record).
				WithScopeResolver(sessionapi.AuthzScopeResolver(authzSvc))
			sessionHandler := server.CORS(server.DefaultCORS())(
				server.RequestID(chatAPI.AuthenticatedHandler(authSvc.UserByToken)),
			)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/sessions" || strings.HasPrefix(r.URL.Path, "/api/v1/sessions/") {
					sessionHandler.ServeHTTP(w, r)
					return
				}
				platformHandler.ServeHTTP(w, r)
			})
			tls := cfg.Platform.Server.TLS
			srv := &http.Server{
				Addr:              listenAddr,
				Handler:           handler,
				ReadHeaderTimeout: 10 * time.Second,
			}
			logger.Info("k8s-ai server listening", "addr", listenAddr, "db", path, "tls", tls.Enabled)
			errCh := make(chan error, 1)
			go func() {
				if tls.Enabled {
					errCh <- srv.ListenAndServeTLS(tls.CertFile, tls.KeyFile)
					return
				}
				errCh <- srv.ListenAndServe()
			}()
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
	cmd.Flags().StringVar(&addr, "addr", "", "listen address override (default from config platform.server.addr, e.g. :8080)")
	cmd.Flags().StringVar(&dbPath, "db", "", "SQLite database path override (default from config platform.store.sqlite_path; first start reads K8S_AI_ADMIN_PASSWORD)")
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
