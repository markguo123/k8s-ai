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

// TestConfigInitWritesKindTemplates 锁 `config init` 与 `config init --cli`
// 写出的模板内容（P6.12 ③⑧）：两套模板互不串台。
func TestConfigInitWritesKindTemplates(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		args []string
		path string
		want string
	}{
		{"server", []string{"config", "init", "--config"}, filepath.Join(dir, "config.yaml"), config.ServerTemplate},
		{"cli", []string{"config", "init", "--cli", "--config"}, filepath.Join(dir, "cli.yaml"), config.CLITemplate},
	} {
		root := NewRootCmd(testDeps(defaultCfg(t), &fakeScan{}))
		var buf bytes.Buffer
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(append(tc.args, tc.path))
		if err := root.Execute(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		raw, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(raw) != tc.want {
			t.Errorf("%s: 写出的模板与预期不符（len=%d, want=%d）", tc.name, len(raw), len(tc.want))
		}
	}
}

// TestConfigInitCLIDefaultPath 锁 --cli 的默认落点 ~/.k8s-ai/cli.yaml（D-21）。
func TestConfigInitCLIDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	root := NewRootCmd(testDeps(defaultCfg(t), &fakeScan{}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"config", "init", "--cli"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".k8s-ai", "cli.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != config.CLITemplate {
		t.Error("--cli 默认路径应写 CLI 模板")
	}
	if !strings.Contains(buf.String(), "terminal config") {
		t.Errorf("应提示该文件仅终端读取，实际 %q", buf.String())
	}
}

func TestConfigValidateModeProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(filepath.Join(home, ".k8s-ai"), 0o755); err != nil {
		t.Fatal(err)
	}
	cliBody := "report:\n  directory: ./cli\nllm:\n  endpont: \"http://x/v1\"\n"
	if err := os.WriteFile(filepath.Join(home, ".k8s-ai", "cli.yaml"), []byte(cliBody), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		args     []string
		wantCLI  bool
		warnWant string
	}{
		{"默认 server 面", []string{"config", "validate"}, false, ""},
		{"cli 面", []string{"config", "validate", "--mode", "cli"}, true, "未识别的配置键 llm.endpont"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got config.LoadOptions
			deps := Dependencies{
				LoadConfig: func(opts config.LoadOptions) (*config.Config, error) {
					got = opts
					return defaultCfg(t), nil
				},
				NewScan: func() service.ScanService { return &fakeScan{version: "v1.28.13"} },
			}
			root := NewRootCmd(deps)
			var out, errBuf bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errBuf)
			root.SetArgs(tc.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if got.CLI != tc.wantCLI {
				t.Errorf("CLI 面 = %v, want %v", got.CLI, tc.wantCLI)
			}
			if !strings.Contains(out.String(), "config OK: server version v1.28.13") {
				t.Errorf("stdout = %q", out.String())
			}
			if tc.warnWant == "" {
				if errBuf.Len() != 0 {
					t.Errorf("不该有告警: %q", errBuf.String())
				}
				return
			}
			if !strings.Contains(errBuf.String(), tc.warnWant) {
				t.Errorf("告警缺少 %q: %q", tc.warnWant, errBuf.String())
			}
		})
	}
}

func TestConfigValidateRejectsUnknownMode(t *testing.T) {
	root := NewRootCmd(testDeps(defaultCfg(t), &fakeScan{version: "v1"}))
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"config", "validate", "--mode", "bogus"})
	if err := root.Execute(); err == nil {
		t.Fatal("非法 --mode 应报错")
	}
}

func TestConfigValidateSkipConnectivity(t *testing.T) {
	connectivityCalls := 0
	deps := Dependencies{
		LoadConfig: func(config.LoadOptions) (*config.Config, error) { return defaultCfg(t), nil },
		NewScan: func() service.ScanService {
			return &countingScan{valErr: errors.New("不应发起集群连通性检查"), calls: &connectivityCalls}
		},
	}
	root := NewRootCmd(deps)
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"config", "validate", "--mode", "server", "--skip-connectivity"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "config OK (server mode)") {
		t.Errorf("stdout = %q", out.String())
	}
	if connectivityCalls != 0 {
		t.Errorf("--skip-connectivity 仍发起了 %d 次连通性检查", connectivityCalls)
	}
}

// countingScan 记录 Validate 调用次数，用于断言 --skip-connectivity 生效。
type countingScan struct {
	valErr error
	calls  *int
}

func (c *countingScan) Run(context.Context, model.ScanOptions) (*model.ScanResult, error) {
	return &model.ScanResult{}, nil
}

func (c *countingScan) Validate(context.Context, model.ScanOptions) (string, error) {
	*c.calls++
	return "", c.valErr
}

// TestEngineCommandsUseCLIFace 锁默认加载路径的命令面区分（D-21）：
// chat/scan 走终端直用面，server/db 走平台静态面。
func TestEngineCommandsUseCLIFace(t *testing.T) {
	result := &model.ScanResult{
		Meta:        model.ScanMeta{ServerVersion: "v1.30.0"},
		HealthScore: model.HealthScore{Score: 100, Max: 100},
	}
	stub := newStubChatService([]service.ChatEvent{{Type: service.ChatEventAnswer, Text: "ok"}})

	cases := []struct {
		name    string
		args    []string
		deps    func(*testing.T, *bool) Dependencies
		wantCLI bool
	}{
		{"scan", []string{"scan"}, func(t *testing.T, got *bool) Dependencies {
			return captureLoad(testDeps(defaultCfg(t), &fakeScan{result: result}), got)
		}, true},
		{"chat", []string{"chat", "在吗", "--format", "json"}, func(t *testing.T, got *bool) Dependencies {
			return captureLoad(chatTestDeps(stub), got)
		}, true},
		{"config validate", []string{"config", "validate", "--skip-connectivity"}, func(t *testing.T, got *bool) Dependencies {
			return captureLoad(testDeps(defaultCfg(t), &fakeScan{version: "v1"}), got)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotCLI bool
			deps := tc.deps(t, &gotCLI)
			root := NewRootCmd(deps)
			var buf bytes.Buffer
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(tc.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if gotCLI != tc.wantCLI {
				t.Errorf("%s 的 CLI 面 = %v, want %v", tc.name, gotCLI, tc.wantCLI)
			}
		})
	}
}

// captureLoad 在测试依赖里旁听一次 LoadOptions，用于断言命令面（CLI/平台）。
func captureLoad(deps Dependencies, got *bool) Dependencies {
	inner := deps.LoadConfig
	deps.LoadConfig = func(opts config.LoadOptions) (*config.Config, error) {
		*got = opts.CLI
		return inner(opts)
	}
	return deps
}
