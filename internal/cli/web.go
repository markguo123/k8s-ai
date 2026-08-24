package cli

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/webui"
)

// newWebCmd 启动 Web chat UI（k8s-ai web）。
func newWebCmd(deps Dependencies) *cobra.Command {
	var addr string
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
			srv := &http.Server{
				Addr:              addr,
				Handler:           webui.New(deps.NewChat(cfg), cfg.LLM.Model).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			slog.Info("k8s-ai web listening", "addr", addr, "url", "http://"+addr)
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
