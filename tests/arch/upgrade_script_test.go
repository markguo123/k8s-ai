// Package arch_test 的发布/升级驱动器门禁：deploy/upgrade.sh。
//
// 为什么要单独设门禁：install.sh 是幂等调谐器，「升级」= 换 TAG 重跑一遍。一旦有人把
// upgrade.sh 写成第二个发布器（自己 apply 清单、自己 set image、自己 rollout undo），
// 就会出现两套发布逻辑各自漂移，现场排障时看哪一份都不对。这里用两条硬约束钉住分工：
//  1. 真正的写动作必须全部委托 install.sh（脚本里不允许出现 apply/set image/rollout undo/构建推送）；
//  2. 镜像 TAG 必须由 git 派生（同一个 TAG 不对应两个 commit），参数文件与发布记录必须
//     落在源码目录之外（否则每次换目录重部署都要重抄 install.env）。
//
// 可执行用例走真 bash：dry-run 会调起真 install.sh 渲染清单，能顺带证明两脚本的
// 参数接缝（--env-file/--no-build/--no-push/环境变量 TAG）没有走偏。
package arch_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func readUpgradeScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "upgrade.sh"))
	if err != nil {
		t.Fatalf("读不到 deploy/upgrade.sh：%v", err)
	}
	return string(data)
}

// upgradeUsageBody 取 usage() 里的帮助正文，用于「解析了什么就要写什么」。
func upgradeUsageBody(t *testing.T, script string) string {
	t.Helper()
	start := strings.Index(script, "usage() {")
	if start < 0 {
		t.Fatal("deploy/upgrade.sh 里没有 usage()")
	}
	rest := script[start:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		t.Fatal("usage() 没有正常收尾")
	}
	return rest[:end]
}

func TestUpgradeScriptIsStrictShell(t *testing.T) {
	script := readUpgradeScript(t)
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Error("deploy/upgrade.sh 必须以 #!/usr/bin/env bash 开头")
	}
	if !strings.Contains(script, "set -Eeuo pipefail") {
		t.Error("deploy/upgrade.sh 必须 set -Eeuo pipefail")
	}
	// 与 install.sh 同一条红线：脚本本体零 LLM、零外呼到业务系统。git fetch 取码是设计内
	// 行为，但绝对不允许在这里碰模型或把凭据发出去。
	for _, forbidden := range []string{"curl ", "wget ", "kubectl exec -i", "OPENAI", "api_key"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("deploy/upgrade.sh 不该出现 %q", forbidden)
		}
	}
}

// TestUpgradeScriptDelegatesReleaseToInstall 钉住「一个发布器」原则。
func TestUpgradeScriptDelegatesReleaseToInstall(t *testing.T) {
	script := readUpgradeScript(t)
	if !strings.Contains(script, `INSTALL_SH="${SCRIPT_DIR}/install.sh"`) {
		t.Error("deploy/upgrade.sh 必须通过同目录 install.sh 发布")
	}
	if !strings.Contains(script, `"$INSTALL_SH"`) {
		t.Error("deploy/upgrade.sh 必须真的调用 \"$INSTALL_SH\"，不许自己另写一套发布流程")
	}
	for _, forbidden := range []string{
		"kubectl apply", "$KUBECTL_BIN\" apply", "set image", "rollout undo",
		"docker build", "docker push", "podman build", "helm ",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("deploy/upgrade.sh 里不该出现 %q：写动作归 install.sh，回滚靠重发上一个 TAG", forbidden)
		}
	}
}

// TestUpgradeScriptParsesTagFromGit 钉住不可变 TAG：默认由 git 派生，显式口令才允许沿用固定发布号。
func TestUpgradeScriptParsesTagFromGit(t *testing.T) {
	script := readUpgradeScript(t)
	for _, want := range []string{"compute_tag()", "git -C \"$SRC_DIR\" rev-parse --short HEAD", "follow-env-tag", "-dirty"} {
		if !strings.Contains(script, want) {
			t.Errorf("deploy/upgrade.sh 缺少 TAG 派生逻辑要素 %q：同一个 TAG 不能对应两个 commit", want)
		}
	}
}

// TestUpgradeScriptKeepsStateOutsideSourceTree 钉住「参数与发布记录不跟着源码目录一起丢」。
func TestUpgradeScriptKeepsStateOutsideSourceTree(t *testing.T) {
	script := readUpgradeScript(t)
	for _, want := range []string{"AEGISOPS_DEPLOY_STATE", "STATE_DIR", "history-", "LOG_DIR", "state_load", "state_record_ok"} {
		if !strings.Contains(script, want) {
			t.Errorf("deploy/upgrade.sh 缺少发布记录/参数解耦要素 %q", want)
		}
	}
}

func TestUpgradeScriptRollbackNeverRebuilds(t *testing.T) {
	script := readUpgradeScript(t)
	body := shellFuncBody(t, script, "parse_args")
	if !strings.Contains(body, "--rollback) ROLLBACK=1; NO_BUILD=1") {
		t.Error("--rollback 必须隐含 --no-build：回滚要的是仓库里那个已经在跑过的镜像")
	}
	run := shellFuncBody(t, script, "run_install")
	if !strings.Contains(run, "--no-build") || !strings.Contains(run, "--no-push") {
		t.Error("--no-build 时必须默认 --no-push：本地没有该镜像时 push 会让回滚半途失败")
	}
}

func TestUpgradeScriptVerifiesDeployedCommit(t *testing.T) {
	script := readUpgradeScript(t)
	body := shellFuncBody(t, script, "verify_release")
	for _, want := range []string{"rollout status", "exec deploy/aegisops-server -- /usr/local/bin/aopsctl version", "commit", "separated"} {
		if !strings.Contains(body, want) {
			t.Errorf("verify_release 缺少 %q：发布完必须自己确认 Ready 与跑的是哪个 commit", want)
		}
	}
	// kubectl exec 是整条替换容器命令，ENTRYPOINT 不参与：漏写二进制名会让 exec 恒失败，
	// commit 核对就静默退化成「只核对了 Ready 状态」，验收门禁成了摆设。
	if strings.Contains(script, "exec deploy/aegisops-server -- version") {
		t.Error("exec 必须写全二进制名：-- version 会被当成执行一个叫 version 的程序")
	}
}

func TestUpgradeScriptDocumentsEveryFlag(t *testing.T) {
	script := readUpgradeScript(t)
	usage := upgradeUsageBody(t, script)
	body := shellFuncBody(t, script, "parse_args")
	flagRE := regexp.MustCompile(`--([a-z][a-z-]+)[)= ]`)
	for _, m := range flagRE.FindAllStringSubmatch(body, -1) {
		flag := "--" + m[1]
		if !strings.Contains(usage, flag) {
			t.Errorf("deploy/upgrade.sh 解析了 %s 但 --help 里没写", flag)
		}
	}
}

func TestUpgradeScriptCarriesNoEnvironmentSpecificValues(t *testing.T) {
	script := readUpgradeScript(t)
	ipRE := regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	if loc := ipRE.FindString(script); loc != "" {
		t.Errorf("deploy/upgrade.sh 里出现疑似真实 IP %q：环境专有值不进仓库", loc)
	}
	for _, forbidden := range []string{"REPLACE_", "example.com", "localhost:", "127.0.0.1:"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("deploy/upgrade.sh 里出现占位或环境专有值 %q", forbidden)
		}
	}
}

// --- 可执行用例（真 bash + 真 install.sh dry-run）----------------------------

// upgradeEnvFixture 是最小可用的参数文件：镜像仓库、DSN 全是假值，只为让 install.sh
// 的必填校验过掉，dry-run 不会真的连接它们。
func upgradeEnvFixture(t *testing.T, dir, tag string) string {
	t.Helper()
	path := filepath.Join(dir, "install.env")
	content := strings.Join([]string{
		"IMAGE=registry.invalid/platform/k8s-ai",
		"TAG=" + tag,
		"IMAGE_WEB=registry.invalid/platform/aegisops-web",
		"FORM=separated",
		"NAMESPACE=k8s-ai-archtest",
		"STORE_DRIVER=mysql",
		"MYSQL_DSN='user:pass@tcp(127.0.0.1:3306)/k8s_ai?parseTime=true'",
		"STORAGE_CLASS=standard",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写测试参数文件失败：%v", err)
	}
	return path
}

func runUpgrade(t *testing.T, bash string, env []string, args ...string) (string, error) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位仓库根目录失败：%v", err)
	}
	cmd := exec.Command(bash, append([]string{"deploy/upgrade.sh"}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), env...)
	// 默认关掉两个脚本的 tee：日志内容和 combined 输出重复，用例只读一份就够。
	// 用例自己给了 AEGISOPS_INSTALL_NO_TEE 就听用例的——有一条正要看日志是否落在 state/logs。
	if !hasEnvPrefix(env, "AEGISOPS_INSTALL_NO_TEE=") {
		cmd.Env = append(cmd.Env, "AEGISOPS_INSTALL_NO_TEE=1")
	}
	cmd.Env = append(cmd.Env, "TMPDIR=/tmp")
	combined, err := cmd.CombinedOutput()
	return string(combined), err
}

func hasEnvPrefix(env []string, prefix string) bool {
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return true
		}
	}
	return false
}

// bashTempDir 建一个 bash 与宿主都能可靠定位的临时 state 目录，返回 POSIX 路径与宿主路径。
//
// 为什么不用 t.TempDir()：git bash 会把 Windows 的 TEMP 重映射到自己的 /tmp，脚本按
// AEGISOPS_DEPLOY_STATE 写进去的日志，Go 侧拿原始宿主路径根本读不到，断言就会假失败。
// 统一在 bash 侧 mktemp，再用 pwd -W 换回宿主路径给 Go 用（install_render_test.go 同一套做法）。
func bashTempDir(t *testing.T, bash string) (posix, host string) {
	t.Helper()
	cmd := exec.Command(bash, "-c", "mktemp -d /tmp/aegisops-upgrade-XXXXXX")
	cmd.Env = append(os.Environ(), "TMPDIR=/tmp")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("建 bash 临时 state 目录失败：%v", err)
	}
	posix = strings.TrimSpace(string(out))
	t.Cleanup(func() {
		// 目录名来自 mktemp，只可能是 /tmp 下的短名，交回 bash 删除最省事。
		_ = exec.Command(bash, "-c", fmt.Sprintf("rm -rf -- %q", posix)).Run()
	})
	return posix, bashHostPath(t, bash, posix)
}

// hasInstallLog 判断 state/logs 下是否落了 install.sh 的日志（日志跟着发布记录走，不落源码目录）。
func hasInstallLog(t *testing.T, state string) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(state, "logs"))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "install-") && strings.HasSuffix(e.Name(), ".log") {
			return true
		}
	}
	return false
}

func TestUpgradeScriptDryRunDerivesTagAndKeepsLogsOutsideRepo(t *testing.T) {
	bash := findBash()
	if bash == "" {
		t.Skip("没有可用的 bash，跳过 upgrade.sh 可执行用例")
	}
	posixState, state := bashTempDir(t, bash)
	upgradeEnvFixture(t, state, "4.0.0-archtest")

	// 这条用例要验日志落盘位置，所以显式打开 tee（默认是关的）。
	out, err := runUpgrade(t, bash, []string{
		"AEGISOPS_DEPLOY_STATE=" + posixState,
		"AEGISOPS_INSTALL_NO_TEE=0",
	}, "--dry-run", "--yes")
	if err != nil {
		t.Fatalf("upgrade.sh --dry-run 失败：%v\n输出尾部：\n%s", err, tailLines(out, 30))
	}

	short := gitShortHead(t)
	if !strings.Contains(out, "4.0.0-archtest-"+short) {
		t.Errorf("镜像 TAG 应由 install.env 的 TAG 与 git 短 sha 派生（期望含 4.0.0-archtest-%s）：\n%s", short, tailLines(out, 30))
	}
	// 日志落在 state 目录下：换源码目录重部署时不用重抄参数，历史日志也还在。
	if !hasInstallLog(t, state) {
		t.Errorf("install.sh 日志应落在 %s/logs 下：\n%s", state, tailLines(out, 30))
	}
	if _, err := os.Stat(filepath.Join(state, "k8s-ai-archtest.env")); err == nil {
		t.Error("--dry-run 不该写发布记录：预演没有改变集群，记账会让 --rollback 指向一个没发过的版本")
	}
}

func TestUpgradeScriptFollowEnvTagUsesReleaseTag(t *testing.T) {
	bash := findBash()
	if bash == "" {
		t.Skip("没有可用的 bash，跳过 upgrade.sh 可执行用例")
	}
	posixState, state := bashTempDir(t, bash)
	upgradeEnvFixture(t, state, "4.0.0-release")

	out, err := runUpgrade(t, bash, []string{"AEGISOPS_DEPLOY_STATE=" + posixState}, "--dry-run", "--yes", "--follow-env-tag")
	if err != nil {
		t.Fatalf("upgrade.sh --dry-run --follow-env-tag 失败：%v\n输出尾部：\n%s", err, tailLines(out, 30))
	}
	if strings.Contains(out, "4.0.0-release-"+gitShortHead(t)) {
		t.Errorf("--follow-env-tag 应原样用 install.env 里的 TAG：\n%s", tailLines(out, 30))
	}
	if !strings.Contains(out, "k8s-ai:4.0.0-release") {
		t.Errorf("期望镜像为 ...aegisops:4.0.0-release：\n%s", tailLines(out, 30))
	}
}

// 导出的 TAG 属于显式指定：既要盖过 install.env 里写死的 TAG，也不该再被追加 commit 后缀，
// 否则手册里「导出的环境变量 > env 文件」这条优先级在 TAG 上就是假的。
func TestUpgradeScriptExportedTagOverridesEnvFileTag(t *testing.T) {
	bash := findBash()
	if bash == "" {
		t.Skip("没有可用的 bash，跳过 upgrade.sh 可执行用例")
	}
	posixState, state := bashTempDir(t, bash)
	upgradeEnvFixture(t, state, "4.0.0-filetest")

	out, err := runUpgrade(t, bash, []string{
		"AEGISOPS_DEPLOY_STATE=" + posixState,
		"TAG=9.9.9-exported",
	}, "--dry-run", "--yes")
	if err != nil {
		t.Fatalf("带导出 TAG 的 --dry-run 失败：%v\n输出尾部：\n%s", err, tailLines(out, 30))
	}
	if !strings.Contains(out, "k8s-ai:9.9.9-exported") {
		t.Errorf("导出的 TAG 应盖过 install.env 的 TAG：\n%s", tailLines(out, 30))
	}
	if strings.Contains(out, "4.0.0-filetest") {
		t.Errorf("显式 TAG 生效时不该再用 install.env 的 TAG：\n%s", tailLines(out, 30))
	}
	if strings.Contains(out, "9.9.9-exported-"+gitShortHead(t)) {
		t.Errorf("显式指定的 TAG 不该再追加 commit 后缀：\n%s", tailLines(out, 30))
	}
}

func TestUpgradeScriptStatusShowsRecordedRelease(t *testing.T) {
	bash := findBash()
	if bash == "" {
		t.Skip("没有可用的 bash，跳过 upgrade.sh 可执行用例")
	}
	posixState, state := bashTempDir(t, bash)
	upgradeEnvFixture(t, state, "4.0.0-archtest")
	record := "# k8s-ai 发布记录\nLAST_GOOD_TS=2026-11-03T01:00:00Z\nLAST_GOOD_REF=DEV-v4.0.0\nLAST_GOOD_SHA=deadbee\nLAST_GOOD_TAG=4.0.0-archtest-deadbee\nLAST_GOOD_IMAGE=registry.invalid/platform/k8s-ai\n"
	if err := os.WriteFile(filepath.Join(state, "k8s-ai-archtest.env"), []byte(record), 0o600); err != nil {
		t.Fatalf("写发布记录失败：%v", err)
	}
	stub := filepath.Join(state, "kubectl-stub.sh")
	if err := os.WriteFile(stub, []byte("#!/usr/bin/env bash\nprintf 'k8s-ai 4.0.0 (commit deadbee, built 2026-11-03T01:00:00Z)\\n'\n"), 0o755); err != nil {
		t.Fatalf("写 kubectl 桩失败：%v", err)
	}

	out, err := runUpgrade(t, bash, []string{
		"AEGISOPS_DEPLOY_STATE=" + state,
		"KUBECTL_BIN=" + posixState + "/kubectl-stub.sh",
	}, "--status")
	if err != nil {
		t.Fatalf("upgrade.sh --status 失败：%v\n输出尾部：\n%s", err, tailLines(out, 30))
	}
	if !strings.Contains(out, "4.0.0-archtest-deadbee") {
		t.Errorf("--status 要显示上一次成功发布的 TAG：\n%s", tailLines(out, 30))
	}
	if !strings.Contains(out, "deadbee") || !strings.Contains(out, "DEV-v4.0.0") {
		t.Errorf("--status 要显示集群里跑的 commit 与记录里的 ref：\n%s", tailLines(out, 30))
	}
}

func TestUpgradeScriptRejectsMissingEnvFile(t *testing.T) {
	bash := findBash()
	if bash == "" {
		t.Skip("没有可用的 bash，跳过 upgrade.sh 可执行用例")
	}
	posixState, _ := bashTempDir(t, bash)
	out, err := runUpgrade(t, bash, []string{"AEGISOPS_DEPLOY_STATE=" + posixState}, "--dry-run", "--yes")
	if err == nil {
		t.Fatalf("缺参数文件必须失败，不能猜默认值：\n%s", tailLines(out, 30))
	}
	if !strings.Contains(out, "install.env") {
		t.Errorf("报错要指路参数文件放哪：\n%s", tailLines(out, 30))
	}
}

func gitShortHead(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位仓库根目录失败：%v", err)
	}
	out, err := exec.Command("git", "-C", root, "--no-pager", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Skipf("读不到 git 短 sha，跳过 TAG 派生断言：%v", err)
	}
	return strings.TrimSpace(string(out))
}
