// Package arch_test 锁 deploy/install.sh 的交付红线（P6.4 / D-31 / ADR-045）。
//
// 一键安装脚本是产品交付的门面：现场只看它能不能一条命令装完。它同时也是一段
// 会在客户集群里写资源的 shell，所以这里钉死四件事：
//  1. 脚本存在且是严格 shell（set -Eeuo pipefail、LF 行尾），预演/轮换/无人值守三种模式齐备；
//  2. 密钥与 DSN 不得经命令行参数传递（--from-literal 会进 ps 与 shell history）；
//  3. 仓库清单里的每个 REPLACE_* 占位，要么由脚本渲染时替换，要么明确登记为
//     「手工模板、脚本不下发」，不允许出现没人负责的占位值；
//  4. 参数面自解释：PARAM_KEYS 里每个键都要在 --help 与 install.env.example 里出现，
//     脚本与任何交付清单里都不许混进环境专有值（真实内网 IP、公司域名等）。
package arch_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// installScriptPath 是被测脚本路径。
const installScriptPath = "deploy/install.sh"

// installEnvExamplePath 是参数模板，参数面变更必须同步它。
const installEnvExamplePath = "deploy/install.env.example"

// manualPlaceholderTemplates 登记「脚本不下发、由运维手工复制改写」的模板及其占位。
// 出现在这里的文件不会被 install.sh apply，因此不受渲染断言保护，只能靠人读注释填写。
var manualPlaceholderTemplates = map[string][]string{
	"base/secret.example.yaml":          {"REPLACE_ME"},
	"example/secret-mysql.example.yaml": {"REPLACE_ME"},
	// 扫描 CronJob 的模型接线属 P6.6，独立于 Web 服务的一键安装。
	"scan/configmap.yaml": {"REPLACE_WITH_LLM_HOST", "REPLACE_WITH_MODEL_NAME"},
}

// installParamKeys 解析 PARAM_KEYS 白名单（多行字符串，按空白切分）。
func installParamKeys(t *testing.T, script string) []string {
	t.Helper()
	start := strings.Index(script, "PARAM_KEYS=\"")
	if start < 0 {
		t.Fatal("install.sh 里找不到 PARAM_KEYS 参数白名单")
	}
	rest := script[start:]
	end := strings.Index(rest[len("PARAM_KEYS=\""):], "\"")
	if end < 0 {
		t.Fatal("PARAM_KEYS 字符串未闭合")
	}
	body := rest[len("PARAM_KEYS=\"") : len("PARAM_KEYS=\"")+end]
	fields := strings.Fields(body)
	if len(fields) < 20 {
		t.Fatalf("PARAM_KEYS 解析结果异常，只拿到 %d 个键", len(fields))
	}
	return fields
}

// usageBody 取 usage() 里 heredoc 的帮助正文。
func usageBody(t *testing.T, script string) string {
	t.Helper()
	const open = "usage() {"
	at := strings.Index(script, open)
	if at < 0 {
		t.Fatal("install.sh 里找不到 usage() 函数")
	}
	rest := script[at:]
	begin := strings.Index(rest, "cat <<'EOF'")
	if begin < 0 {
		t.Fatal("usage() 里没有 heredoc 帮助正文")
	}
	rest = rest[begin:]
	finish := strings.Index(rest, "\nEOF")
	if finish < 0 {
		t.Fatal("usage() 的 heredoc 没有结束标记")
	}
	return rest[:finish]
}

func TestInstallScriptIsStrictShell(t *testing.T) {
	body := readRepoFile(t, "deploy", "install.sh")
	if !strings.HasPrefix(body, "#!/usr/bin/env bash") {
		t.Errorf("%s 缺少 bash shebang", installScriptPath)
	}
	if !strings.Contains(body, "set -Eeuo pipefail") {
		t.Errorf("%s 必须启用 set -Eeuo pipefail，否则中途失败会静默继续", installScriptPath)
	}
	if strings.Contains(body, "\r") {
		t.Errorf("%s 含 CR（CRLF 行尾），在 Linux 上执行会报 bad interpreter；请确认 .gitattributes 生效", installScriptPath)
	}
}

func TestInstallScriptCoversRequiredBehavior(t *testing.T) {
	body := readRepoFile(t, "deploy", "install.sh")
	required := []struct{ what, needle string }{
		{"预演模式（只渲染不碰集群）", "--dry-run)"},
		{"差异预演", "--diff)"},
		{"密钥轮换入口", "--rotate)"},
		{"无人值守开关", "-y|--yes)"},
		{"参数文件", "--env-file)"},
		{"参数文件默认探测 install.env", "install.env"},
		{"权限预检", "auth can-i"},
		{"默认存储类探测", "is-default-class"},
		{"PVC 等待绑定", "wait --for=condition=bound"},
		{"PVC 延迟绑定模式识别（WaitForFirstConsumer）", "WaitForFirstConsumer"},
		{"延迟绑定推迟到工作负载后校验", "pvc_verify_deferred_bind"},
		{"PVC 存储类不可变对齐", "pvc_align_class"},
		{"迁移 Job 等待完成", "wait --for=condition=complete job/k8s-ai-db-migrate"},
		{"滚动升级等待", "rollout status deployment/k8s-ai-server"},
		{"NodePort 形态", "type: NodePort"},
		{"Ingress 可选", "ingressClassName"},
		{"ENC_KEY 复用优先", "secret_exists"},
		{"密钥本地备份", "backup_key"},
		{"渲染产物占位残留断言", "assert_rendered_clean"},
		{"失败三段式提示", "下一步"},
		{"写权限 RBAC 需显式开启", "rbac-write.example.yaml"},
	}
	for _, item := range required {
		if !strings.Contains(body, item.needle) {
			t.Errorf("%s 缺少「%s」应有的实现标记 %q", installScriptPath, item.what, item.needle)
		}
	}
}

func TestInstallScriptSecretsNeverGoViaCommandLine(t *testing.T) {
	body := readRepoFile(t, "deploy", "install.sh")
	if strings.Contains(body, "--from-literal") {
		t.Errorf("%s 用了 --from-literal：密钥会出现在 ps 输出与 shell history 里，改走 --from-file 或 stdin", installScriptPath)
	}
	for _, needle := range []string{`"--from-file=${key}=/dev/stdin"`, "create_secret_from_stdin", "create secret generic"} {
		if !strings.Contains(body, needle) {
			t.Errorf("%s 缺少密钥注入实现 %q（必须经 stdin 落 Secret）", installScriptPath, needle)
		}
	}
	// DSN 与管理员口令只能进 Secret，不能渲染进任何下发清单。
	for _, needle := range []string{"$SEC_STORE", "$SEC_LLM", "$SEC_BLOBS"} {
		if !strings.Contains(body, needle) {
			t.Errorf("%s 缺少 Secret 名 %q，确认敏感值仍走 Secret 而非 ConfigMap", installScriptPath, needle)
		}
	}
	if strings.Contains(body, `-v MYSQL_DSN`) || strings.Contains(body, `-v ADMIN_PASSWORD`) {
		t.Errorf("%s 把 DSN 或口令传给了渲染层，渲染产物是明文清单，禁止携带敏感值", installScriptPath)
	}
}

func TestInstallScriptPlaceholderCoverage(t *testing.T) {
	script := readRepoFile(t, "deploy", "install.sh")
	tokens := map[string][]string{}
	err := filepath.Walk(filepath.Join("..", "..", "deploy"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(filepath.Join("..", "..", "deploy"), path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		for _, tok := range regexp.MustCompile(`REPLACE_[A-Z0-9_]+`).FindAllString(string(raw), -1) {
			tokens[rel] = append(tokens[rel], tok)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历 deploy 清单失败：%v", err)
	}
	if len(tokens) == 0 {
		t.Fatal("deploy 清单里一个占位符都没有，确认清单是否被整体写死了环境专有值")
	}
	for rel, found := range tokens {
		allowed := manualPlaceholderTemplates[rel]
		for _, tok := range found {
			if containsString(allowed, tok) {
				continue
			}
			if !strings.Contains(script, tok) {
				t.Errorf("%s 里的占位 %s 没人负责：既不在 install.sh 的渲染锚点里，也没登记进 manualPlaceholderTemplates", rel, tok)
			}
		}
	}
	// 反向检查：登记成手工模板的文件必须真的存在，防止清单改名后白名单失效。
	for rel := range manualPlaceholderTemplates {
		if _, statErr := os.Stat(filepath.Join("..", "..", "deploy", filepath.FromSlash(rel))); statErr != nil {
			t.Errorf("manualPlaceholderTemplates 登记了不存在的清单 %s：%v", rel, statErr)
		}
	}
}

func TestInstallScriptParamsDocumentedAndTemplated(t *testing.T) {
	script := readRepoFile(t, "deploy", "install.sh")
	example := readRepoFile(t, "deploy", "install.env.example")
	usage := usageBody(t, script)
	keys := installParamKeys(t, script)

	usageLines := make([]string, 0, 64)
	for _, line := range strings.Split(usage, "\n") {
		usageLines = append(usageLines, strings.TrimLeft(line, " \t"))
	}
	exampleLines := make([]string, 0, 96)
	for _, line := range strings.Split(example, "\n") {
		exampleLines = append(exampleLines, strings.TrimSpace(line))
	}

	for _, key := range keys {
		if !hasParamDocLine(usageLines, key) {
			t.Errorf("--help 未说明参数 %s：现场只能靠猜", key)
		}
		if !hasExampleLine(exampleLines, key) {
			t.Errorf("%s 未收录参数 %s：参数模板与脚本必须同步", installEnvExamplePath, key)
		}
	}
	// 白名单是唯一入口：不在 PARAM_KEYS 里的 KEY= 行会让 env 文件直接被拒，属于模板笔误。
	for _, line := range exampleLines {
		if !strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key := strings.TrimPrefix(line, "#")
		key = strings.TrimSpace(strings.SplitN(key, "=", 2)[0])
		if key == "" || !regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`).MatchString(key) {
			continue
		}
		if !containsString(keys, key) {
			t.Errorf("%s 列了脚本不认识的参数 %s", installEnvExamplePath, key)
		}
	}
}

func TestInstallScriptCarriesNoEnvironmentSpecificValues(t *testing.T) {
	// 环境专有值（客户内网 IP、内部主机名、公司域名）一律不进仓库，见 AGENTS.md 与手册 §4.10。
	needles := []string{"10.62.", "172.16.", "172.17.", "192.168.", "gientech", "Gientech", "gitlab", "GitLab"}
	privateIP := regexp.MustCompile(`(^|[^0-9.])(10\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3})([^0-9.]|$)`)
	for _, parts := range [][]string{{"deploy", "install.sh"}, {"deploy", "install.env.example"}} {
		body := readRepoFile(t, parts...)
		for _, needle := range needles {
			if strings.Contains(strings.ToLower(body), strings.ToLower(needle)) {
				t.Errorf("%v 出现环境专有值 %q", parts, needle)
			}
		}
		if loc := privateIP.FindStringIndex(body); loc != nil {
			t.Errorf("%v 出现内网 IP 字面量：%s", parts, snippet(body, loc[0], loc[1]))
		}
	}
}

func hasParamDocLine(lines []string, key string) bool {
	for _, line := range lines {
		if !strings.HasPrefix(line, key) {
			continue
		}
		rest := line[len(key):]
		if rest == "" || strings.HasPrefix(rest, "★") || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t") {
			return true
		}
	}
	return false
}

func hasExampleLine(lines []string, key string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, "#"+key+"=") || strings.HasPrefix(line, key+"=") {
			return true
		}
	}
	return false
}

func containsString(hay []string, needle string) bool {
	for _, v := range hay {
		if v == needle {
			return true
		}
	}
	return false
}

func snippet(body string, from, to int) string {
	lo := from - 8
	if lo < 0 {
		lo = 0
	}
	hi := to + 8
	if hi > len(body) {
		hi = len(body)
	}
	return fmt.Sprintf("...%s...", strings.ReplaceAll(body[lo:hi], "\n", " "))
}
