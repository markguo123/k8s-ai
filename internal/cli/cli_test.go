package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/service"
)

type fakeScan struct {
	result  *model.ScanResult
	runErr  error
	version string
	valErr  error
}

func (f *fakeScan) Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error) {
	return f.result, f.runErr
}

func (f *fakeScan) Validate(ctx context.Context, opts model.ScanOptions) (string, error) {
	return f.version, f.valErr
}

func testDeps(cfg *config.Config, s service.ScanService) Dependencies {
	return Dependencies{
		Version: "k8s-ai dev (test)",
		LoadConfig: func(config.LoadOptions) (*config.Config, error) {
			return cfg, nil
		},
		NewScan: func() service.ScanService { return s },
	}
}

func defaultCfg(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(config.LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestVersionCommand(t *testing.T) {
	root := NewRootCmd(testDeps(defaultCfg(t), &fakeScan{}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"version"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "k8s-ai dev (test)") {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

func TestConfigInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	root := NewRootCmd(testDeps(defaultCfg(t), &fakeScan{}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"config", "init", "--config", path})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not created: %v", err)
	}
}

func TestScanFailOnExit2(t *testing.T) {
	cfg := defaultCfg(t)
	result := &model.ScanResult{
		Findings: []model.Finding{{
			ID:       "f1",
			Rule:     "TestRule",
			Severity: model.SeverityCritical,
			Title:    "boom",
		}},
	}
	root := NewRootCmd(testDeps(cfg, &fakeScan{result: result}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"scan", "--fail-on", "HIGH"})
	err := root.Execute()
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.Code != 2 {
		t.Fatalf("exit code = %d, want 2", exitErr.Code)
	}
}

func TestScanOk(t *testing.T) {
	cfg := defaultCfg(t)
	result := &model.ScanResult{
		Meta:        model.ScanMeta{ServerVersion: "v1.30.0"},
		HealthScore: model.HealthScore{Score: 100, Max: 100},
	}
	root := NewRootCmd(testDeps(cfg, &fakeScan{result: result}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"scan"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "未发现异常") || !strings.Contains(buf.String(), "k8s-ai scan") {
		t.Fatalf("unexpected output: %q", buf.String())
	}
}

// TestScanTargetedTerminal 验证目标扫描（--namespace）默认只打印终端完整报告。
func TestScanTargetedTerminal(t *testing.T) {
	cfg := defaultCfg(t)
	result := &model.ScanResult{
		Meta: model.ScanMeta{ServerVersion: "v1.30.0", Namespace: "mysql"},
		Findings: []model.Finding{{
			ID: "f1", Rule: "PendingPod", Severity: model.SeverityMedium,
			Title:    "Pod 处于 Pending 状态",
			Resource: model.ResourceRef{Kind: "Pod", Namespace: "mysql", Name: "db-0"},
		}},
	}
	root := NewRootCmd(testDeps(cfg, &fakeScan{result: result}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"scan", "-n", "mysql", "--verbose"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Pod 处于 Pending 状态") {
		t.Fatalf("终端应输出完整报告: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "命名空间巡检报告：mysql") || !strings.Contains(buf.String(), "Scan Scope: namespace mysql") {
		t.Fatalf("目标扫描应标注命名空间范围: %q", buf.String())
	}
	if strings.Contains(buf.String(), "Report:") {
		t.Fatalf("目标扫描不应写报告文件: %q", buf.String())
	}
}

// TestScanPodCommand 验证 scan pod 子命令输出 Pod 巡检报告。
func TestScanPodCommand(t *testing.T) {
	cfg := defaultCfg(t)
	cfg.Kubernetes.Namespace = "mysql" // 测试用 fake LoadConfig 不应用 flag 覆盖，直接注入
	result := &model.ScanResult{
		Meta: model.ScanMeta{ServerVersion: "v1.30.0", Namespace: "mysql", Pod: "db-0"},
		Findings: []model.Finding{{
			ID: "f1", Rule: "OOMKilled", Severity: model.SeverityHigh,
			Title:    "容器 db 因 OOMKilled 被杀",
			Resource: model.ResourceRef{Kind: "Pod", Namespace: "mysql", Name: "db-0"},
		}},
	}
	root := NewRootCmd(testDeps(cfg, &fakeScan{result: result}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"scan", "pod", "db-0", "-n", "mysql", "--verbose"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Kubernetes Pod 巡检报告：mysql/db-0") {
		t.Fatalf("应输出 Pod 巡检报告: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "容器 db 因 OOMKilled 被杀") {
		t.Fatalf("应包含该 Pod 的 Finding: %q", buf.String())
	}
}

// TestScanPodRequiresNamespace 验证 scan pod 未指定 namespace 时报错。
func TestScanPodRequiresNamespace(t *testing.T) {
	cfg := defaultCfg(t)
	cfg.Kubernetes.Namespace = ""
	root := NewRootCmd(testDeps(cfg, &fakeScan{}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"scan", "pod", "db-0"})
	if err := root.Execute(); err == nil {
		t.Fatal("缺少 namespace 应报错")
	}
}
