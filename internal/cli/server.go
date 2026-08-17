package cli

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/k8s-ai/k8s-ai/internal/server"
)

// newServerCmd 启动最小化 HTTP 服务（一期 1.2）。
func newServerCmd(deps Dependencies) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the minimal HTTP server (healthz/version/async scans)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			srv := &http.Server{
				Addr:              addr,
				Handler:           server.New(deps.NewScan()).Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}
			slog.Info("k8s-ai server listening", "addr", addr)
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
	return cmd
}
