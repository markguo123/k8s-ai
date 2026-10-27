package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/authz"
	"github.com/k8s-ai/k8s-ai/internal/cluster"
	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/server"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/sessionapi"
	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"github.com/k8s-ai/k8s-ai/web"
)

// newServerCmd 启动平台 HTTP 服务：健康探针、平台 API、会话 API，以及（形态 A）
// 内嵌前端产物。一期 1.2 的最小服务集合保持不变。
func newServerCmd(deps Dependencies) *cobra.Command {
	var addr, dbPath string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the platform HTTP server (API + embedded web UI)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := slog.Default()
			cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd, false))
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			listenAddr := cfg.Platform.Server.Addr
			if cmd.Flags().Changed("addr") {
				listenAddr = addr
			}
			// --db 只覆盖 sqlite 兜底路径；mysql 的连接信息始终来自 platform.store.dsn。
			if cmd.Flags().Changed("db") && !cfg.Platform.Store.IsMySQL() {
				cfg.Platform.Store.SQLitePath = dbPath
			}
			st, target, err := openPlatformStore(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()
			// 迁移主路径是 k8s-ai db migrate（升级 Job/initContainer，P6.5）；这里是
			// 幂等兜底，让单机 sqlite 部署一条命令即可启动。
			if err := st.Migrate(cmd.Context()); err != nil {
				return fmt.Errorf("migrate store: %w", err)
			}
			authSvc := auth.New(st.Users(), st.Roles())
			if err := authSvc.BootstrapWithName(cmd.Context(), cfg.Platform.Auth.AdminUsername, os.Getenv("K8S_AI_ADMIN_PASSWORD")); err != nil {
				return fmt.Errorf("bootstrap admin: %w", err)
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
			// D-29：每轮对话现取生效配置——配置中心是动态键的唯一权威源，
			// env 只作只读覆盖且优先级最高；解析失败时 Chat 直接报错，不回落静态配置。
			if binder, ok := chat.(service.RuntimeConfigBinder); ok {
				binder.SetRuntimeConfigResolver(func(ctx context.Context) (*config.Config, error) {
					return settingsSvc.ChatRuntimeConfig(ctx, cfg)
				})
			}
			platformHandler := server.NewWithPlatform(deps.NewScan(), authSvc, settingsSvc, auditSvc).
				WithClusters(clusterSvc).
				WithSkills(skillPlat).
				WithAuthz(authzSvc).
				WithReadiness(func(ctx context.Context) error { return store.IsReady(ctx, st) }).
				Handler()
			// admin 跨用户会话操作（读取/改名/置顶/删除/发消息/审批/取消）统一留痕（U-06）。
			chatAPI := sessionapi.New(chat, cfg.LLM.Model).
				WithAudit(auditSvc.Record).
				WithScopeResolver(sessionapi.AuthzScopeResolver(authzSvc))
			sessionHandler := server.CORS(server.DefaultCORS())(
				server.RequestID(chatAPI.AuthenticatedHandler(authSvc.UserByToken)),
			)
			staticEnabled := cfg.Platform.Server.StaticEnabled
			if staticEnabled && web.Empty() {
				logger.Warn("embedded front-end assets are missing; static routes will report the build hint",
					"hint", "run npm ci && npm run build in web/ before go build")
			}
			handler := newRootHandler(platformHandler, sessionHandler, server.NewStaticHandler(web.Dist()), staticEnabled)
			tls := cfg.Platform.Server.TLS
			srv := &http.Server{
				Addr:              listenAddr,
				Handler:           handler,
				ReadHeaderTimeout: 10 * time.Second,
			}
			logger.Info("k8s-ai server listening",
				"addr", listenAddr, "store", target, "static_enabled", staticEnabled, "tls", tls.Enabled)
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
	cmd.Flags().StringVar(&dbPath, "db", "", "SQLite database path override (sqlite driver only; first start reads K8S_AI_ADMIN_PASSWORD)")
	return cmd
}

// newRootHandler 组装顶层路由（P6.11 / D-26）：
//   - /api/v1/sessions* → 会话 API；
//   - 其余 /api/v1/* 与探针端点 → 平台 API（未登记路径统一 JSON 错误体）；
//   - 其余路径 → 内嵌前端（含 SPA history fallback）；静态关闭时回 JSON 404，
//     保证 API 消费方永远不会收到 HTML。
//
// staticHandler 由调用方注入（内嵌产物或测试替身），路由分派与产物来源解耦。
func newRootHandler(platformHandler, sessionHandler, staticHandler http.Handler, staticEnabled bool) http.Handler {
	notFound := server.CORS(server.DefaultCORS())(server.RequestID(server.NewNotFoundHandler()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/sessions" || strings.HasPrefix(r.URL.Path, "/api/v1/sessions/"):
			sessionHandler.ServeHTTP(w, r)
		case r.URL.Path == "/api/v1" || strings.HasPrefix(r.URL.Path, "/api/v1/"):
			platformHandler.ServeHTTP(w, r)
		case r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/version":
			platformHandler.ServeHTTP(w, r)
		case staticEnabled:
			staticHandler.ServeHTTP(w, r)
		default:
			notFound.ServeHTTP(w, r)
		}
	})
}
