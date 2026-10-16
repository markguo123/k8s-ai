package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/store"
	"github.com/k8s-ai/k8s-ai/internal/webui"
)

// newWebCmd 启动 Web chat UI（k8s-ai web）。
func newWebCmd(deps Dependencies) *cobra.Command {
	var addr, dbPath string
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Start the k8s-ai web chat UI",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := deps.LoadConfig(loadOptionsFromFlags(cmd))
			if err != nil {
				return err
			}
			if addr == "" {
				addr = cfg.WebUI.Addr
			}
			if !isLoopbackAddr(addr) {
				slog.Warn("web: 监听非回环地址，请确认网络访问控制", "addr", addr)
			}
			chat := deps.NewChat(cfg)
			// 四期 P2.13：装配 SQLite Store 后 Session/Turn/Step 与 token 用量才可落库，
			// 刷新/重开会话仍可读。数据库不可用时仅告警并退回三期内存态，不阻塞 Web 启动。
			if path, err := expandHomePath(dbPath); err != nil {
				slog.Warn("web: 会话持久化未启用（路径非法）", "db", dbPath, "error", err)
			} else if st, err := store.OpenSQLite(path); err != nil {
				slog.Warn("web: 会话持久化未启用（打开数据库失败）", "db", path, "error", err)
			} else {
				defer func() { _ = st.Close() }()
				if err := st.Migrate(cmd.Context()); err != nil {
					slog.Warn("web: 会话持久化未启用（迁移失败）", "db", path, "error", err)
				} else {
					chat = service.NewChatWithStoreAndConfig(cfg, st)
					// P3.8：Skill 平台与聊天同源装配——导入/启停后 create_resource
					// 的 dry-run 实时消费内存 registry；任一步失败只回退内置 Skill。
					skillPlat := service.NewSkillPlatform(st.Skills())
					if err := skillPlat.SeedBuiltins(cmd.Context()); err != nil {
						slog.Warn("web: Skill 平台种子写入失败，回退内置", "error", err)
					} else if err := skillPlat.Reload(cmd.Context()); err != nil {
						slog.Warn("web: Skill 平台重建失败，回退内置", "error", err)
					} else if binder, ok := chat.(service.SkillPlatformBinder); ok {
						binder.SetSkillPlatform(skillPlat)
					}
					slog.Info("web: 会话持久化已启用", "db", path)
				}
			}
			srv := &http.Server{
				Addr:              addr,
				Handler:           webui.New(chat, cfg.LLM.Model).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			slog.Info("k8s-ai web listening", "addr", addr, "url", "http://"+addr,
				"config_stream", cfg.LLM.Stream,
				"model", cfg.LLM.Model)
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
	cmd.Flags().StringVar(&addr, "addr", "", "listen address (default from config webui.addr, e.g. 127.0.0.1:8081)")
	cmd.Flags().StringVar(&dbPath, "db", "~/.k8s-ai/k8s-ai.db", "SQLite path for session/token persistence (empty disables it)")
	return cmd
}

// isLoopbackAddr 判断监听地址是否仅本机可访问。
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
