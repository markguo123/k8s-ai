package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// serverTemplateKeys 锁定 `config init` 输出的平台静态模板键集合（P6.12 ③）。
// 只允许 platform: 静态段 + 落盘目录白名单键（platformStaticEngineKeys）：
// 7 组动态键唯一落 DB settings，其余引擎键属终端直用。
var serverTemplateKeys = []string{
	"agent.audit_dir",
	"agent.audit_file_copy",
	"agent.session_dir",
	"platform.auth.admin_username",
	"platform.auth.session_store",
	"platform.auth.token_ttl",
	"platform.blobs.access_key",
	"platform.blobs.bucket",
	"platform.blobs.create_bucket",
	"platform.blobs.dir",
	"platform.blobs.driver",
	"platform.blobs.endpoint",
	"platform.blobs.gc_grace_period",
	"platform.blobs.max_object_size",
	"platform.blobs.path_style",
	"platform.blobs.prefix",
	"platform.blobs.presign_ttl",
	"platform.blobs.region",
	"platform.blobs.secret_key",
	"platform.blobs.session_data",
	"platform.blobs.tmp_dir",
	"platform.blobs.use_tls",
	"platform.exec.shell_enabled",
	"platform.server.addr",
	"platform.server.static_enabled",
	"platform.server.tls.cert_file",
	"platform.server.tls.enabled",
	"platform.server.tls.key_file",
	"platform.store.driver",
	"platform.store.dsn",
	"platform.store.sqlite_path",
	"report.directory",
	"report.format",
}

// cliTemplateKeys 锁定 `config init --cli` 输出的终端直用模板键集合（P6.12 ⑧）。
// 覆盖面与部署手册 §3.2 的 CLI 引擎键清单一致，不含 platform/webui/mcp 段。
var cliTemplateKeys = []string{
	"agent.approval_mode",
	"agent.audit_dir",
	"agent.create.allow_kinds",
	"agent.create.cluster_kinds",
	"agent.create.deny_kinds",
	"agent.create.enabled",
	"agent.create.max_manifest_bytes",
	"agent.create.max_objects_per_call",
	"agent.max_iterations",
	"agent.max_tool_calls",
	"agent.require_approval",
	"agent.secret.access",
	"agent.session_dir",
	"agent.skip_permissions",
	"agent.timeout",
	"agent.tool_calling",
	"kubernetes.burst",
	"kubernetes.context",
	"kubernetes.kubeconfig",
	"kubernetes.kubectl_path",
	"kubernetes.namespace",
	"kubernetes.qps",
	"kubernetes.timeout",
	"llm.api_key",
	"llm.enabled",
	"llm.endpoint",
	"llm.max_findings",
	"llm.max_input_tokens",
	"llm.max_tokens",
	"llm.max_total_tokens",
	"llm.model",
	"llm.reasoning_fields",
	"llm.stream",
	"llm.temperature",
	"llm.thinking.type",
	"llm.timeout",
	// CLI 只消费 blobs 的本地目录两键（off|dir），其余 platform: 键属平台进程。
	"platform.blobs.dir",
	"platform.blobs.driver",
	"report.directory",
	"report.format",
	"rules.disabled",
	"rules.enabled",
	"scan.collect_events",
	"scan.collect_logs",
	"scan.collect_previous_logs",
	"scan.concurrency",
	"scan.max_log_bytes",
	"scan.max_log_line_bytes",
	"scan.max_log_lines",
	"scan.phase2_concurrency",
	"scan.pod_logs_timeout",
	"scan.thinking.type",
	"scan.timeout",
}

// templateKeys 解析模板并压成排序后的点分叶子键集合。
func templateKeys(t *testing.T, tmpl string) []string {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(tmpl), &doc); err != nil {
		t.Fatalf("parse template: %v", err)
	}
	leaves := map[string]any{}
	flattenKeys(doc, "", leaves)
	keys := make([]string, 0, len(leaves))
	for k := range leaves {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func templateSections(t *testing.T, tmpl string) []string {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(tmpl), &doc); err != nil {
		t.Fatalf("parse template: %v", err)
	}
	sections := make([]string, 0, len(doc))
	for k := range doc {
		sections = append(sections, k)
	}
	sort.Strings(sections)
	return sections
}

func equalKeys(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestServerTemplateKeySet(t *testing.T) {
	got := templateKeys(t, ServerTemplate)
	if !equalKeys(got, serverTemplateKeys) {
		t.Errorf("server 模板键集合漂移\n got: %v\nwant: %v", got, serverTemplateKeys)
	}
	secs := templateSections(t, ServerTemplate)
	if want := []string{"agent", "platform", "report"}; !equalKeys(secs, want) {
		t.Errorf("server 模板段应为 %v（platform + 落盘目录），实际 %v", want, secs)
	}
}

func TestCLITemplateKeySet(t *testing.T) {
	got := templateKeys(t, CLITemplate)
	if !equalKeys(got, cliTemplateKeys) {
		t.Errorf("cli 模板键集合漂移\n got: %v\nwant: %v", got, cliTemplateKeys)
	}
	secs := templateSections(t, CLITemplate)
	want := []string{"agent", "kubernetes", "llm", "platform", "report", "rules", "scan"}
	if !equalKeys(secs, want) {
		t.Errorf("cli 模板段应为 %v，实际 %v", want, secs)
	}
}

// TestTemplatesUseKnownKeys 保证两套模板里的每个键都被 Config 真实消费，
// 不会出现「模板教用户写一个没人读的键」。
func TestTemplatesUseKnownKeys(t *testing.T) {
	idx := buildKeyIndex()
	for name, tmpl := range map[string]string{"server": ServerTemplate, "cli": CLITemplate} {
		for _, key := range templateKeys(t, tmpl) {
			if !idx.known(key) {
				t.Errorf("%s 模板键 %s 不在 Config 可识别键集合内", name, key)
			}
		}
	}
}

// TestServerTemplateExcludesDynamicAndEngineKeys 是「文件 vs 页面」双权威防回归：
// 平台静态模板只允许 platform: 段与落盘目录白名单键，其余引擎键与 7 组动态键
// 的键名一个都不能出现。
func TestServerTemplateExcludesDynamicAndEngineKeys(t *testing.T) {
	for _, key := range templateKeys(t, ServerTemplate) {
		if strings.HasPrefix(key, "platform.") || platformStaticEngineKeys[key] {
			continue
		}
		t.Errorf("server 模板出现越界键 %s（只允许 platform.* 与落盘目录键）", key)
	}
	got := strings.Join(templateKeys(t, ServerTemplate), "\n")
	for _, dynamic := range []string{"approval_mode", "max_iterations", "base_url", "api_key", "kubeconfig", "session_store_ttl", "create.", "secret."} {
		if strings.Contains(got, dynamic) {
			t.Errorf("server 模板不应含动态业务键 %s", dynamic)
		}
	}
}

func TestTemplateFor(t *testing.T) {
	if tmpl, err := TemplateFor(TemplateServer); err != nil || tmpl != ServerTemplate {
		t.Fatalf("TemplateFor(server) = %v, %v", tmpl, err)
	}
	if tmpl, err := TemplateFor(TemplateCLI); err != nil || tmpl != CLITemplate {
		t.Fatalf("TemplateFor(cli) = %v, %v", tmpl, err)
	}
	if _, err := TemplateFor(TemplateKind("bogus")); err == nil {
		t.Fatal("未知模板种类应报错")
	}
	if DefaultTemplate != ServerTemplate {
		t.Fatal("DefaultTemplate 必须保持为 server 静态模板（三期兼容别名）")
	}
}

func TestInitWritesKindTemplate(t *testing.T) {
	for _, kind := range []TemplateKind{TemplateServer, TemplateCLI} {
		path := filepath.Join(t.TempDir(), "sub", string(kind)+".yaml")
		written, err := Init(path, kind, false)
		if err != nil {
			t.Fatalf("init %s: %v", kind, err)
		}
		if written != path {
			t.Fatalf("written = %q, want %q", written, path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want, err := TemplateFor(kind)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want {
			t.Errorf("%s 模板内容不一致", kind)
		}
		if _, err := Init(path, kind, false); err == nil {
			t.Errorf("%s 二次 init 未加 --force 应报错", kind)
		}
	}
	if _, err := Init(filepath.Join(t.TempDir(), "x.yaml"), TemplateKind("bogus"), false); err == nil {
		t.Fatal("未知模板种类应报错")
	}
}

// TestInitTemplatesLoad 锁两套模板的加载语义：CLI 模板开箱即可加载；server 静态
// 模板按四期主口径声明 MySQL，DSN 只经 env/Secret 注入，未注入时报错必须直指
// platform.store.dsn（而不是莫名连本地库或静默降级 sqlite）。
func TestInitTemplatesLoad(t *testing.T) {
	cliPath := filepath.Join(t.TempDir(), "cli.yaml")
	if _, err := Init(cliPath, TemplateCLI, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(LoadOptions{ConfigFile: cliPath, NoEnv: true}); err != nil {
		t.Errorf("cli 模板应开箱可加载: %v", err)
	}

	serverPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := Init(serverPath, TemplateServer, false); err != nil {
		t.Fatal(err)
	}
	_, err := Load(LoadOptions{ConfigFile: serverPath, NoEnv: true})
	if err == nil || !strings.Contains(err.Error(), "platform.store.dsn") {
		t.Fatalf("缺 DSN 时应报 platform.store.dsn 错误，实际 %v", err)
	}
	t.Setenv("K8S_AI_PLATFORM_STORE_DSN", "k8sai:secret@tcp(db.internal:3306)/k8s_ai?charset=utf8mb4&parseTime=true")
	cfg, err := Load(LoadOptions{ConfigFile: serverPath})
	if err != nil {
		t.Fatalf("注入 Secret DSN 后应加载成功: %v", err)
	}
	if !cfg.Platform.Store.IsMySQL() {
		t.Errorf("server 模板应声明 mysql 主存储，实际 driver=%q", cfg.Platform.Store.Driver)
	}
}

func TestDiagnosticsReportsUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "" +
		"llm:\n" +
		"  endpont: \"http://x/v1\"\n" +
		"platform:\n" +
		"  server:\n" +
		"    addr2: \":9\"\n" +
		"webui2:\n" +
		"  addr: \":1\"\n" +
		"mystery: true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings, err := Diagnostics(path, ValidateModeServer)
	if err != nil {
		t.Fatal(err)
	}
	got := unknownWarnKeys(warnings)
	want := []string{"llm.endpont", "mystery", "platform.server.addr2", "webui2"}
	if !equalKeys(got, want) {
		t.Errorf("未识别键告警 = %v, want %v", got, want)
	}
}

func TestDiagnosticsModeProfile(t *testing.T) {
	// 模板与自身剖面零告警；换到另一剖面会给出「本进程不消费」提示。
	for _, tc := range []struct {
		name      string
		tmpl      string
		mode      ValidateMode
		wantCount int
	}{
		{"server-in-server", ServerTemplate, ValidateModeServer, 0},
		{"cli-in-cli", CLITemplate, ValidateModeCLI, 0},
		{"server-in-cli", ServerTemplate, ValidateModeCLI, 1},
		// cli 模板放进平台文件：kubernetes/llm/scan/rules 各一条段级告警，agent 段
		// 越界键合成一条；report 段只有 directory/format，属落盘白名单，不告警。
		{"cli-in-server", CLITemplate, ValidateModeServer, 5},
	} {
		path := filepath.Join(t.TempDir(), tc.name+".yaml")
		if err := os.WriteFile(path, []byte(tc.tmpl), 0o600); err != nil {
			t.Fatal(err)
		}
		warnings, err := Diagnostics(path, tc.mode)
		if err != nil {
			t.Fatal(err)
		}
		if len(warnings) != tc.wantCount {
			t.Errorf("%s: 告警 = %v, want %d 条", tc.name, warnings, tc.wantCount)
		}
	}
}

func TestDiagnosticsWarnsPlaintextDSN(t *testing.T) {
	dir := t.TempDir()
	withDSN := filepath.Join(dir, "dsn.yaml")
	if err := os.WriteFile(withDSN, []byte("platform:\n  store:\n    driver: mysql\n    dsn: \"u:p@tcp(h:3306)/db?parseTime=true\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings, err := Diagnostics(withDSN, ValidateModeServer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "platform.store.dsn") {
		t.Errorf("明文 DSN 应告警，实际 %v", warnings)
	}

	empty := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(empty, []byte("platform:\n  store:\n    dsn: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings, err = Diagnostics(empty, ValidateModeServer)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("DSN 留空不应告警，实际 %v", warnings)
	}

	if warnings, err := Diagnostics("", ValidateModeServer); err != nil || len(warnings) != 0 {
		t.Errorf("无配置文件不应有告警: %v %v", warnings, err)
	}
}

// TestDiagnosticsServerPathWhitelist 锁定平台静态文件的落盘目录口径（P6.2 / deploy 清单）：
// report.directory/format 与 agent.session_dir/audit_dir 是合法静态键，零告警；
// 同段里的其他引擎键会与配置中心形成双源，必须告警并点名。
func TestDiagnosticsServerPathWhitelist(t *testing.T) {
	dir := t.TempDir()

	head := "platform:\n  store:\n    driver: sqlite\n" +
		"report:\n  directory: /reports\n  format: markdown\n"
	paths := "agent:\n  session_dir: /data/sessions\n  audit_dir: /data/audit\n"

	platform := filepath.Join(dir, "platform.yaml")
	body := head + paths
	if err := os.WriteFile(platform, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings, err := Diagnostics(platform, ValidateModeServer)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("落盘目录键不该告警，实际 %v", warnings)
	}

	over := filepath.Join(dir, "over.yaml")
	if err := os.WriteFile(over, []byte(head+"agent:\n  session_dir: /data/sessions\n  approval_mode: strict\n  create:\n    enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings, err = Diagnostics(over, ValidateModeServer)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 ||
		!strings.Contains(warnings[0], "agent.approval_mode") ||
		!strings.Contains(warnings[0], "agent.create.enabled") {
		t.Errorf("越界引擎键应点名告警，实际 %v", warnings)
	}
	// CLI 剖面只对 platform: 段敏感，引擎键本来就是 CLI 要的（见 TestDiagnosticsModeProfile）。
}

func TestParseValidateMode(t *testing.T) {
	for _, in := range []string{"", "server", "SERVER"} {
		if mode, err := ParseValidateMode(in); err != nil || mode != ValidateModeServer {
			t.Errorf("ParseValidateMode(%q) = %v, %v", in, mode, err)
		}
	}
	if mode, err := ParseValidateMode("cli"); err != nil || mode != ValidateModeCLI {
		t.Errorf("ParseValidateMode(cli) = %v, %v", mode, err)
	}
	if _, err := ParseValidateMode("bogus"); err == nil {
		t.Fatal("非法 --mode 取值应报错")
	}
}

// TestResolveConfigFileCLIFallback 锁 D-21 的默认加载路径：
// 显式 --config > cli.yaml（仅终端直用面）> config.yaml > 无文件。
func TestResolveConfigFileCLIFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".k8s-ai")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	serverPath := filepath.Join(dir, "config.yaml")
	cliPath := filepath.Join(dir, "cli.yaml")

	if got, err := ResolveConfigFile("", false); err != nil || got != "" {
		t.Fatalf("两份文件都不存在时应回退默认值: %q %v", got, err)
	}
	if err := os.WriteFile(serverPath, []byte("report:\n  directory: ./srv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveConfigFile("", false); err != nil || got != serverPath {
		t.Fatalf("server 面应取 config.yaml: %q %v", got, err)
	}
	if got, err := ResolveConfigFile("", true); err != nil || got != serverPath {
		t.Fatalf("cli.yaml 缺失应回退 config.yaml: %q %v", got, err)
	}
	if err := os.WriteFile(cliPath, []byte("report:\n  directory: ./cli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveConfigFile("", true); err != nil || got != cliPath {
		t.Fatalf("cli 面应取 cli.yaml: %q %v", got, err)
	}
	if got, err := ResolveConfigFile("", false); err != nil || got != serverPath {
		t.Fatalf("server 面必须忽略 cli.yaml: %q %v", got, err)
	}
	explicit := filepath.Join(home, "custom.yaml")
	if got, err := ResolveConfigFile(explicit, true); err != nil || got != explicit {
		t.Fatalf("--config 必须最高优先: %q %v", got, err)
	}
}

// TestLoadCLIUsesCLIFileWithFallback 端到端确认 CLI:true 走 cli.yaml，
// 缺失时回退 config.yaml（存量部署不受影响）。
func TestLoadCLIUsesCLIFileWithFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".k8s-ai")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("report:\n  directory: ./from-server\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(LoadOptions{NoEnv: true, CLI: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Report.Directory != "./from-server" {
		t.Fatalf("cli.yaml 缺失时应回退 config.yaml，实际 %q", cfg.Report.Directory)
	}
	if err := os.WriteFile(filepath.Join(dir, "cli.yaml"), []byte("report:\n  directory: ./from-cli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(LoadOptions{NoEnv: true, CLI: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Report.Directory != "./from-cli" {
		t.Fatalf("CLI:true 应读 cli.yaml，实际 %q", cfg.Report.Directory)
	}
	cfg, err = Load(LoadOptions{NoEnv: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Report.Directory != "./from-server" {
		t.Fatalf("平台面必须读 config.yaml，实际 %q", cfg.Report.Directory)
	}
}

// unknownWarnKeys 从告警文本里取出未识别键名（按告警顺序）。
func unknownWarnKeys(warnings []string) []string {
	var keys []string
	for _, w := range warnings {
		if !strings.HasPrefix(w, unknownKeyPrefix) {
			continue
		}
		rest := strings.TrimPrefix(w, unknownKeyPrefix)
		keys = append(keys, strings.SplitN(rest, "：", 2)[0])
	}
	sort.Strings(keys)
	return keys
}
