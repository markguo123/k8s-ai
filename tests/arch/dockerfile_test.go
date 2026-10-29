// Package arch_test 的镜像基座红线（P6.3 / D-33 / ADR-044 v1.1）。
//
// 交付镜像分两档：默认档（Dockerfile，debian slim + bash + kubectl）与无 shell 变体
// （Dockerfile.distroless）。两档必须只差"运行阶段"：构建阶段逐字一致，否则同一版本号
// 会对应两个不同的二进制。另外镜像里的运行 uid、HOME 与 deploy 清单的 runAsUser、
// 可写挂载点必须互相对得上——只读根文件系统下 kubectl 发现缓存写不进去，是现场最容易
// 撞上的那类"看起来是应用 bug"的问题。
package arch_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	buildBlockBegin = "# ---- BUILD STAGES BEGIN ----"
	buildBlockEnd   = "# ---- BUILD STAGES END ----"
)

// readRepoFile 读仓库根下的文件。
func readRepoFile(t *testing.T, rel ...string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, rel...)...))
	if err != nil {
		t.Fatalf("read %v: %v", rel, err)
	}
	return string(raw)
}

// buildBlock 取出 BEGIN/END 之间的构建阶段（含标记行本身，顺带断言标记成对出现）。
func buildBlock(t *testing.T, content, name string) string {
	t.Helper()
	i := strings.Index(content, buildBlockBegin)
	j := strings.Index(content, buildBlockEnd)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("%s 必须成对包含构建阶段标记（%s / %s）", name, buildBlockBegin, buildBlockEnd)
	}
	return content[i : j+len(buildBlockEnd)]
}

// TestDockerfileBuildStagesAreIdentical 锁"两档镜像只差运行阶段"。
func TestDockerfileBuildStagesAreIdentical(t *testing.T) {
	defaultDF := readRepoFile(t, "Dockerfile")
	distrolessDF := readRepoFile(t, "Dockerfile.distroless")

	a, b := buildBlock(t, defaultDF, "Dockerfile"), buildBlock(t, distrolessDF, "Dockerfile.distroless")
	if a != b {
		t.Errorf("两个 Dockerfile 的构建阶段必须逐字一致（前端产物与二进制同源）\n--- Dockerfile ---\n%s\n--- Dockerfile.distroless ---\n%s", a, b)
	}
	for _, want := range []string{"FROM node:22-alpine AS webdist", "AS build", "go build", "./cmd/k8s-ai"} {
		if !strings.Contains(a, want) {
			t.Errorf("构建阶段应包含 %q", want)
		}
	}
	// 构建阶段里不能塞运行期依赖，否则两档镜像会带进不同的东西。
	if strings.Contains(a, "apt-get") || strings.Contains(a, "kubectl") {
		t.Error("构建阶段不应安装运行期工具（工具层属于运行阶段，两档差异要留在分叉点之后）")
	}
}

// TestDefaultImageRuntimeStageHasShellToolchain 锁默认档的交付内容：有 bash、有校验过的
// kubectl、非 root 65532、HOME 落在可写目录。少了任何一条，容器内排障能力就比本地 CLI 少。
func TestDefaultImageRuntimeStageHasShellToolchain(t *testing.T) {
	content := readRepoFile(t, "Dockerfile")
	_, runtime, _ := strings.Cut(content, buildBlockEnd)
	if strings.TrimSpace(runtime) == "" {
		t.Fatal("Dockerfile 构建阶段之后没有运行阶段定义")
	}
	for _, want := range []string{
		"FROM debian:bookworm-slim",
		"bash",
		"ARG KUBECTL_VERSION=",
		"ARG KUBECTL_DOWNLOAD_BASE=",
		"ARG TARGETARCH",
		"sha256sum -c",
		`ENTRYPOINT ["/usr/local/bin/k8s-ai"]`,
	} {
		if !strings.Contains(runtime, want) {
			t.Errorf("默认镜像运行阶段缺少 %q", want)
		}
	}
	if !regexp.MustCompile(`\bjq\b`).MatchString(runtime) {
		t.Error("默认镜像运行阶段应安装 jq（kubectl -o json | jq 是最常用的排障组合）")
	}
	if !regexp.MustCompile(`(?m)^USER\s+(\d+):(\d+)$`).MatchString(runtime) {
		t.Error("默认镜像必须显式 USER <uid>:<gid>（不能只靠基础镜像的默认用户）")
	}
	uid := regexp.MustCompile(`--uid\s+(\d+)`).FindStringSubmatch(runtime)
	userLine := regexp.MustCompile(`(?m)^USER\s+(\d+):\d+$`).FindStringSubmatch(runtime)
	if uid == nil || userLine == nil {
		t.Fatal("默认镜像必须用 useradd 建用户并与 USER 行一致")
	}
	if uid[1] != userLine[1] {
		t.Errorf("useradd uid=%s 与 USER 行 uid=%s 不一致", uid[1], userLine[1])
	}
	home := regexp.MustCompile(`(?m)^ENV\s+HOME=(\S+)$`).FindStringSubmatch(runtime)
	if home == nil {
		t.Fatal("默认镜像必须显式设置 ENV HOME（kubectl 服务发现缓存写 $HOME/.kube）")
	}
	// kubectl 只能在构建期落地：运行期下载等于把交付一致性交给外网。
	if strings.Contains(runtime, "RUN ") && !strings.Contains(runtime, "kubectl") {
		t.Error("运行阶段出现 RUN 但没有 kubectl 安装步骤，检查是否漏了内置 kubectl")
	}
}

// TestKubectlOfflineDeliveryIsFirstPath 锁"气隙交付不依赖外网"（P6.3）：kubectl 必须先查
// deploy/kubectl/ 的离线投递，再考虑 mirror 下载。顺序即优先级——反过来的话，没有出口的
// 现场连镜像都构建不出来，而这类现场才是本产品的交付常态。
func TestKubectlOfflineDeliveryIsFirstPath(t *testing.T) {
	content := readRepoFile(t, "Dockerfile")
	_, runtime, _ := strings.Cut(content, buildBlockEnd)

	if !strings.Contains(runtime, "--mount=type=bind,source=deploy/kubectl") {
		t.Error("默认镜像必须用 bind mount 读 deploy/kubectl（COPY 会把投递的二进制固化成一整层镜像）")
	}
	// bind mount 的源目录不存在时构建直接失败，所以干净检出后这个目录必须在。
	if _, err := os.Stat(filepath.Join("..", "..", "deploy", "kubectl", ".gitkeep")); err != nil {
		t.Errorf("deploy/kubectl/ 必须存在（bind mount 源目录缺失会让所有构建失败）：%v", err)
	}
	if _, err := os.Stat(filepath.Join("..", "..", "deploy", "kubectl", "README.md")); err != nil {
		t.Errorf("deploy/kubectl/README.md 必须存在：离线投递是内网交付主路径，取二进制的步骤要写在旁边：%v", err)
	}

	vendor := strings.Index(runtime, "/kubectl-vendor/kubectl")
	download := strings.Index(runtime, "curl -fsSLo")
	if vendor < 0 || download < 0 {
		t.Fatalf("kubectl 投递必须同时有离线路径与下载路径（vendor=%d download=%d）", vendor, download)
	}
	if vendor > download {
		t.Error("必须先判离线投递再下载：优先下载会让无出口网络的环境无法构建")
	}
	if !strings.Contains(runtime, "sha256sum -c") {
		t.Error("kubectl 投递必须保留 sha256 校验")
	}

	// 投递目录只收二进制、不入库：否则仓库会被 50MB 的二进制撑爆。
	gitignore := readRepoFile(t, ".gitignore")
	// 但占位与说明文档必须放行：否则干净检出后 README 消失，上面两条断言反而变成自杀。
	if !strings.Contains(gitignore, "deploy/kubectl/*") ||
		!strings.Contains(gitignore, "!deploy/kubectl/.gitkeep") ||
		!strings.Contains(gitignore, "!deploy/kubectl/README.md") {
		t.Error(".gitignore 必须忽略 deploy/kubectl/ 下的投递二进制，同时放行 .gitkeep 与 README.md")
	}
}

// TestDistrolessImageRuntimeStageHasNoShell 锁无 shell 变体：运行阶段不得引入任何解释器
// 或工具层（这一档的存在意义就是"容器里没有可执行 shell"）。
func TestDistrolessImageRuntimeStageHasNoShell(t *testing.T) {
	content := readRepoFile(t, "Dockerfile.distroless")
	_, runtime, _ := strings.Cut(content, buildBlockEnd)
	if !strings.Contains(runtime, "gcr.io/distroless/static-debian12:nonroot") {
		t.Error("无 shell 变体应使用 distroless static nonroot 运行阶段")
	}
	for _, forbidden := range []string{"apt-get", "kubectl", "RUN ", "USER ", "/bin/sh", "/bin/bash"} {
		if strings.Contains(runtime, forbidden) {
			t.Errorf("无 shell 变体运行阶段不应出现 %q", forbidden)
		}
	}
	if !strings.Contains(content, "platform.exec.shell_enabled") {
		t.Error("无 shell 变体必须在文件头写明要配 platform.exec.shell_enabled=false（否则 shell 命令会在执行时才失败）")
	}
}

// imageUserUID 取默认镜像的运行 uid。
func imageUserUID(t *testing.T) int {
	t.Helper()
	m := regexp.MustCompile(`(?m)^USER\s+(\d+):\d+$`).FindStringSubmatch(readRepoFile(t, "Dockerfile"))
	if m == nil {
		t.Fatal("默认镜像没有 USER <uid>:<gid> 行")
	}
	uid, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("解析镜像 uid 失败: %v", err)
	}
	return uid
}

// imageHome 取默认镜像的 HOME。
func imageHome(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^ENV\s+HOME=(\S+)$`).FindStringSubmatch(readRepoFile(t, "Dockerfile"))
	if m == nil {
		t.Fatal("默认镜像没有 ENV HOME")
	}
	return m[1]
}

// walkYAML 深度遍历清单文档，把每个 mapping 节点交给 fn。
func walkYAML(node any, fn func(map[string]any)) {
	switch v := node.(type) {
	case map[string]any:
		fn(v)
		for _, child := range v {
			walkYAML(child, fn)
		}
	case []any:
		for _, child := range v {
			walkYAML(child, fn)
		}
	}
}

// TestBackendManifestsMatchImageUser 锁镜像与清单的 uid 一致性：所有跑后端镜像的工作负载
// （两种形态的 Deployment、迁移 Job、巡检 CronJob）都必须以镜像内同一 uid 运行。
// 前端 Nginx 镜像是另一条构建线（web.Dockerfile），不在本断言范围内。
func TestBackendManifestsMatchImageUser(t *testing.T) {
	uid := imageUserUID(t)
	manifests := [][]string{
		{"deploy", "allinone", "deployment.yaml"},
		{"deploy", "separated", "deployment.yaml"},
		{"deploy", "base", "db-migrate-job.yaml"},
		{"deploy", "scan", "cronjob.yaml"},
	}
	for _, rel := range manifests {
		doc := readManifestDoc(t, rel...)
		seenUIDs := 0
		nonRoot := 0
		readOnlyRoot := 0
		walkYAML(doc, func(m map[string]any) {
			if v, ok := yamlInt(m["runAsUser"]); ok {
				seenUIDs++
				if v != uid {
					t.Errorf("%s: runAsUser=%v 与镜像 uid=%d 不一致", rel, v, uid)
				}
			}
			if v, ok := m["runAsNonRoot"].(bool); ok && v {
				nonRoot++
			}
			if v, ok := m["readOnlyRootFilesystem"].(bool); ok && v {
				readOnlyRoot++
			}
		})
		if seenUIDs == 0 {
			t.Errorf("%v: 没有任何 runAsUser，容器会以镜像默认用户之外的身份运行", rel)
		}
		if nonRoot == 0 || readOnlyRoot == 0 {
			t.Errorf("%v: 缺少 runAsNonRoot/readOnlyRootFilesystem 加固（nonRoot=%d readOnlyRoot=%d）", rel, nonRoot, readOnlyRoot)
		}
	}
}

// TestDeploymentsMountImageHome 锁"kubectl 发现缓存可写"：只读根文件系统下，镜像的 HOME
// 必须被一个可写卷（emptyDir 或 PVC）盖住，否则 kubectl 会在运行期报缓存写入失败。
func TestDeploymentsMountImageHome(t *testing.T) {
	home := imageHome(t)
	for _, rel := range [][]string{{"deploy", "allinone", "deployment.yaml"}, {"deploy", "separated", "deployment.yaml"}} {
		doc := readManifestDoc(t, rel...)
		podSpec, _ := nestedMap(doc, "spec", "template", "spec")
		if podSpec == nil {
			t.Fatalf("%v: 取不到 pod spec", rel)
		}
		writableVolumes := map[string]bool{}
		for _, item := range asList(podSpec["volumes"]) {
			vol, ok := item.(map[string]any)
			if !ok {
				continue
			}
			name, _ := vol["name"].(string)
			_, emptyDir := vol["emptyDir"]
			_, pvc := vol["persistentVolumeClaim"]
			if emptyDir || pvc {
				writableVolumes[name] = true
			}
		}
		mounted := false
		for _, item := range asList(podSpec["containers"]) {
			c, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, m := range asList(c["volumeMounts"]) {
				mount, ok := m.(map[string]any)
				if !ok {
					continue
				}
				if mount["mountPath"] != home {
					continue
				}
				mounted = true
				name, _ := mount["name"].(string)
				if !writableVolumes[name] {
					t.Errorf("%v: HOME %s 挂的不是可写卷（需 emptyDir 或 PVC）", rel, home)
				}
				if ro, ok := mount["readOnly"].(bool); ok && ro {
					t.Errorf("%v: HOME %s 挂成了只读，kubectl 缓存写不进去", rel, home)
				}
			}
		}
		if !mounted {
			t.Errorf("%v: 必须把镜像 HOME %s 挂成可写卷（只读根文件系统下 kubectl 发现缓存无处可写）", rel, home)
		}
	}
}

func nestedMap(doc map[string]any, keys ...string) (map[string]any, bool) {
	cur := doc
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

// yamlInt 兼容 yaml.v3 对整数的不同落地类型。
func yamlInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
