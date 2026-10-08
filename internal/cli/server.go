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
	"github.com/k8s-ai/k8s-ai/internal/server"
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
			srv := &http.Server{
				Addr:              addr,
				Handler:           server.NewWithAuth(deps.NewScan(), authSvc).Handler(),
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
