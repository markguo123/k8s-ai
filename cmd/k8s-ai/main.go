// Command k8s-ai is the entrypoint of the Kubernetes AI inspector.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	// 注册 client-go 认证插件（kubeconfig 可能使用 oidc/gcp/azure/exec 等）。
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/k8s-ai/k8s-ai/internal/cli"
	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/service"
	"github.com/k8s-ai/k8s-ai/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cli.NewRootCmd(cli.Dependencies{
		Version:    version.String(),
		LoadConfig: config.Load,
		NewScan:    service.New,
		NewChat:    service.NewChatWithConfig,
	})
	if err := root.ExecuteContext(ctx); err != nil {
		var exitErr *cli.ExitError
		if errors.As(err, &exitErr) {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(exitErr.Code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
