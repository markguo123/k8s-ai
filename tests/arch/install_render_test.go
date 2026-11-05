// Package arch_test 的渲染门禁：真跑一遍 install.sh --dry-run，把产物逐份喂给 YAML 解析器。
//
// 为什么非要真解析：install.sh 的锚点 7（extra_env_block + awk 注入）是按字面拼 YAML 的，
// env 项「- name:」落在 12 列，续行的 value / valueFrom 必须在 14 列、secretKeyRef 在 16 列。
// 续行缩进一旦写死成两空格，渲染出的 deployment.yaml 就是非法 YAML，而现场要一路跑到
// kubectl diff 才看见 "did not find expected key"，整条一键安装直接失败。字符串断言
// 看不出缩进，只有把产物解析一遍才拦得住这类回归（P8.8 接入 tmp_dir 时踩过）。
package arch_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderDirLineRE 匹配脚本退出时打印的「渲染产物保留在：<路径>」（可能带 ANSI 颜色码）。
var renderDirLineRE = regexp.MustCompile(`渲染产物保留在：(?:\x1b\[[0-9;]*m)?(/[^\s"']+)`)

// bashProbeMarker 用来确认「这个 bash 真的能跑命令」。Windows 上 PATH 里的 bash 常常是
// WSL 的占位程序：没有装发行版时它只会打印一句话就退出，不能当被测 shell 用。
const bashProbeMarker = "aegisops-bash-probe"

// findBash 找一个可用的 bash；一个都没有时返回空串，由调用方跳过测试。
func findBash() string {
	var candidates []string
	if custom := os.Getenv("AEGISOPS_TEST_BASH"); custom != "" {
		candidates = append(candidates, custom)
	}
	if p, err := exec.LookPath("bash"); err == nil {
		candidates = append(candidates, p)
	}
	if runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), `D:\Program Files`} {
			if root == "" {
				continue
			}
			candidates = append(candidates, filepath.Join(root, "Git", "bin", "bash.exe"))
		}
	}
	for _, c := range candidates {
		out, err := exec.Command(c, "-c", "printf "+bashProbeMarker).Output()
		if err == nil && strings.Contains(string(out), bashProbeMarker) {
			return c
		}
	}
	return ""
}

// bashHostPath 把 bash 侧的 POSIX 路径翻译成宿主可直接打开的路径：git bash 的 pwd -W
// 给出 C:\... 形式，Linux 上没有 -W 这个选项，退回普通 pwd。
func bashHostPath(t *testing.T, bash, posixPath string) string {
	t.Helper()
	out, err := exec.Command(bash, "-c", fmt.Sprintf("cd %q && { pwd -W 2>/dev/null || pwd; }", posixPath)).Output()
	if err != nil {
		t.Fatalf("换算渲染目录的宿主路径失败（%s）：%v", posixPath, err)
	}
	return strings.TrimSpace(string(out))
}

// renderManifests 跑一次 --dry-run（只渲染，不接触集群），返回渲染产物的宿主路径。
// 显式钉住 TMPDIR=/tmp：产物目录名里就不会混进空格，Linux 与 git bash 两侧都能定位。
func renderManifests(t *testing.T, bash string, args ...string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位仓库根目录失败：%v", err)
	}
	full := append([]string{"deploy/install.sh", "--dry-run", "--BUILD=false", "--PUSH=false", "--TAG=test"}, args...)
	cmd := exec.Command(bash, full...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "AEGISOPS_INSTALL_NO_TEE=1", "TMPDIR=/tmp")
	combined, err := cmd.CombinedOutput()
	output := string(combined)
	if err != nil {
		t.Fatalf("install.sh --dry-run 失败：%v\n参数：%v\n输出尾部：\n%s", err, args, tailLines(output, 25))
	}
	m := renderDirLineRE.FindStringSubmatch(output)
	if m == nil {
		t.Fatalf("没能从 --dry-run 输出里定位渲染目录：\n%s", tailLines(output, 25))
	}
	posixDir := m[1]
	t.Cleanup(func() {
		// 产物目录是 bash 侧路径，交回 bash 删除最省事；路径来自 mktemp，只可能是 / 开头的短名。
		_ = exec.Command(bash, "-c", fmt.Sprintf("rm -rf -- %q", posixDir)).Run()
	})
	return bashHostPath(t, bash, posixDir)
}

// renderedManifest 只取「这份 YAML 是不是一个像样的 K8s 对象」需要的字段。
type renderedManifest struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Name string           `yaml:"name"`
					Env  []renderedEnvVar `yaml:"env"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

type renderedEnvVar struct {
	Name      string `yaml:"name"`
	Value     string `yaml:"value"`
	ValueFrom struct {
		SecretKeyRef struct {
			Name string `yaml:"name"`
			Key  string `yaml:"key"`
		} `yaml:"secretKeyRef"`
	} `yaml:"valueFrom"`
}

// serverEnv 从渲染目录里取出平台 Deployment（两形态同名 aegisops-server）的容器 env。
func serverEnv(t *testing.T, dir string) []renderedEnvVar {
	t.Helper()
	path := filepath.Join(dir, "separated", "deployment.yaml")
	if _, err := os.Stat(path); err != nil {
		path = filepath.Join(dir, "allinone", "deployment.yaml")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取渲染出的 Deployment 失败：%v", err)
	}
	var doc renderedManifest
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("渲染出的 Deployment 不是合法 YAML（kubectl apply 会直接失败）：%v\n%s", err, tailLines(string(raw), 12))
	}
	if doc.Kind != "Deployment" {
		t.Fatalf("渲染出的 %s kind=%s，期望 Deployment", path, doc.Kind)
	}
	containers := doc.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		t.Fatalf("渲染出的 Deployment 没有容器")
	}
	return containers[0].Env
}

// assertEnv 断言注入的 env：wantValue 是「键=字面值」，wantSecret 是「键=Secret 名/键名」，
// forbid 是这份后端根本不该出现的键（例如 db / off 下不得下发 S3 端点与凭据）。
func assertEnv(t *testing.T, env []renderedEnvVar, wantValue, wantSecret, forbid map[string]string) {
	t.Helper()
	seen := map[string]int{}
	for _, e := range env {
		seen[e.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("env %s 被注入了 %d 次，重复键会让容器起不来", name, n)
		}
	}
	byName := map[string]renderedEnvVar{}
	for _, e := range env {
		byName[e.Name] = e
	}
	for name, want := range wantValue {
		got, ok := byName[name]
		switch {
		case !ok:
			t.Errorf("缺少注入的 env %s（期望值 %q）", name, want)
		case got.Value != want:
			t.Errorf("env %s = %q，期望 %q", name, got.Value, want)
		case got.ValueFrom.SecretKeyRef.Name != "":
			t.Errorf("env %s 同时带 value 与 secretKeyRef，YAML 语义冲突", name)
		}
	}
	for name, want := range wantSecret {
		got, ok := byName[name]
		ref := got.ValueFrom.SecretKeyRef
		switch {
		case !ok:
			t.Errorf("缺少注入的 env %s（期望来自 Secret %s）", name, want)
		case ref.Name == "" || ref.Key == "":
			t.Errorf("env %s 必须走 secretKeyRef（密钥不落清单明文），实际 %+v", name, ref)
		case ref.Name+"/"+ref.Key != want:
			t.Errorf("env %s 取自 %s/%s，期望 %s", name, ref.Name, ref.Key, want)
		}
	}
	for name, why := range forbid {
		if _, ok := byName[name]; ok {
			t.Errorf("不该注入 env %s：%s", name, why)
		}
	}
}

// TestInstallScriptRendersParseableManifests 是 install.sh 的产物门禁：四种对象存储后端
// 加两种部署形态各渲染一次，产物必须全部可解析，且注入的 env 与后端语义一致。
func TestInstallScriptRendersParseableManifests(t *testing.T) {
	bash := findBash()
	if bash == "" {
		t.Skip("本机没有可用的 bash（Windows 需 Git for Windows，或设 AEGISOPS_TEST_BASH 指定），跳过渲染门禁")
	}

	const (
		prefix = "AEGISOPS_PLATFORM_BLOBS_"
		admin  = "AEGISOPS_PLATFORM_AUTH_ADMIN_USERNAME"
	)
	mysql := []string{
		"--STORE_DRIVER=mysql",
		"--MYSQL_DSN=user:pass@tcp(mysql:3306)/k8s_ai",
	}
	separated := append([]string{
		"--FORM=separated",
		"--IMAGE=registry.local/k8s-ai/k8s-ai",
		"--IMAGE_WEB=registry.local/k8s-ai/aegisops-web",
		"--BLOBS_PREFIX=team-a",
		"--ADMIN_USERNAME=admin",
	}, mysql...)
	allinone := append([]string{
		"--FORM=allinone",
		"--IMAGE=registry.local/k8s-ai/k8s-ai",
	}, mysql...)
	s3 := []string{
		"--BLOBS_BACKEND=s3",
		"--BLOBS_ENDPOINT=s3.local:9000",
		"--BLOBS_BUCKET=k8s-ai",
		"--BLOBS_REGION=cn-north-1",
		"--BLOBS_USE_TLS=false",
		"--BLOBS_PATH_STYLE=true",
		"--BLOBS_ACCESS_KEY=ak",
		"--BLOBS_SECRET_KEY=sk",
	}
	merge := func(groups ...[]string) []string {
		var out []string
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}

	cases := []struct {
		name       string
		args       []string
		wantValue  map[string]string
		wantSecret map[string]string
		forbid     map[string]string
	}{
		{
			// db 也要显式下发 driver：换后端闸门靠 Deployment env 认「集群现在跑在哪个后端」。
			name:      "separated-db",
			args:      merge(separated, []string{"--BLOBS_BACKEND=db"}),
			wantValue: map[string]string{prefix + "DRIVER": "db", admin: "admin", prefix + "PREFIX": "team-a"},
			forbid: map[string]string{
				prefix + "ENDPOINT":   "db 后端不碰 S3",
				prefix + "ACCESS_KEY": "db 后端没有对象凭据",
			},
		},
		{
			name:      "separated-off",
			args:      merge(separated, []string{"--BLOBS_BACKEND=off"}),
			wantValue: map[string]string{prefix + "DRIVER": "off", admin: "admin"},
			forbid:    map[string]string{prefix + "DIR": "off 后端不落地对象", prefix + "ENDPOINT": "off 后端不碰 S3"},
		},
		{
			name:      "separated-dir",
			args:      merge(separated, []string{"--BLOBS_BACKEND=dir"}),
			wantValue: map[string]string{prefix + "DRIVER": "dir", prefix + "DIR": "/data/blobs", admin: "admin"},
			forbid:    map[string]string{prefix + "ENDPOINT": "dir 后端不碰 S3", prefix + "ACCESS_KEY": "dir 后端没有凭据"},
		},
		{
			// CREATE_BUCKET 未显式给值时必须渲染成 false：生产桶由平台组预建，启动期不建桶。
			name: "separated-s3",
			args: merge(separated, s3),
			wantValue: map[string]string{
				prefix + "DRIVER":        "s3",
				prefix + "ENDPOINT":      "s3.local:9000",
				prefix + "BUCKET":        "k8s-ai",
				prefix + "REGION":        "cn-north-1",
				prefix + "USE_TLS":       "false",
				prefix + "PATH_STYLE":    "true",
				prefix + "CREATE_BUCKET": "false",
				prefix + "TMP_DIR":       "/tmp/aegisops-blobs",
				prefix + "PREFIX":        "team-a",
			},
			wantSecret: map[string]string{prefix + "ACCESS_KEY": "aegisops-blobs/access_key", prefix + "SECRET_KEY": "aegisops-blobs/secret_key"},
		},
		{
			name:       "allinone-s3",
			args:       merge(allinone, s3),
			wantValue:  map[string]string{prefix + "DRIVER": "s3", prefix + "CREATE_BUCKET": "false", prefix + "TMP_DIR": "/tmp/aegisops-blobs"},
			wantSecret: map[string]string{prefix + "ACCESS_KEY": "aegisops-blobs/access_key", prefix + "SECRET_KEY": "aegisops-blobs/secret_key"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := renderManifests(t, bash, tc.args...)
			var files int
			err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
					return nil
				}
				files++
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				var doc map[string]interface{}
				if err := yaml.Unmarshal(raw, &doc); err != nil {
					t.Errorf("渲染产物 %s 解析失败（现场要跑到 kubectl diff 才报，整条安装卡死）：%v",
						filepath.Base(path), err)
					return nil
				}
				var head renderedManifest
				if err := yaml.Unmarshal(raw, &head); err != nil {
					return nil
				}
				if head.APIVersion == "" || head.Kind == "" || head.Metadata.Name == "" {
					t.Errorf("渲染产物 %s 缺 apiVersion/kind/metadata.name", filepath.Base(path))
				}
				return nil
			})
			if err != nil {
				t.Fatalf("遍历渲染目录失败：%v", err)
			}
			if files < 8 {
				t.Fatalf("渲染出 %d 份清单，少于预期（≥8 份），渲染目录=%s", files, dir)
			}
			assertEnv(t, serverEnv(t, dir), tc.wantValue, tc.wantSecret, tc.forbid)
		})
	}
}

// renderedPullSecrets 只取 Pod 级 imagePullSecrets。Deployment 与 Job 的 Pod 模板挂载点
// 不同（Job 多一层 jobTemplate），所以两种形状都收着，取非空的那个。
type renderedPullSecrets struct {
	Spec struct {
		Template struct {
			Spec struct {
				ImagePullSecrets []struct {
					Name string `yaml:"name"`
				} `yaml:"imagePullSecrets"`
			} `yaml:"spec"`
		} `yaml:"template"`
		JobTemplate struct {
			Spec struct {
				Template struct {
					Spec struct {
						ImagePullSecrets []struct {
							Name string `yaml:"name"`
						} `yaml:"imagePullSecrets"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		} `yaml:"jobTemplate"`
	} `yaml:"spec"`
}

// TestInstallScriptInjectsImagePullSecretsInEveryPod 锁私有仓库拉取凭据的覆盖面。
// 形态 B 要拉三种镜像：后端 Deployment、前端 web-deployment、迁移 Job。早期锚点只认后端
// deployment.yaml，还拿 serviceAccountName 当锚点 —— 而前端和 Job 压根没有这一行，结果
// IMAGE_PULL_SECRET 只对后端生效，私有仓库里前端与迁移 Job 在未缓存镜像的节点上直接
// ImagePullBackOff，现场只能手改清单绕过。
func TestInstallScriptInjectsImagePullSecretsInEveryPod(t *testing.T) {
	bash := findBash()
	if bash == "" {
		t.Skip("本机没有可用的 bash（Windows 需 Git for Windows，或设 AEGISOPS_TEST_BASH 指定），跳过渲染门禁")
	}
	const secret = "registry-private"
	base := []string{
		"--IMAGE=registry.local/k8s-ai/k8s-ai",
		"--STORE_DRIVER=mysql",
		"--MYSQL_DSN=user:pass@tcp(mysql:3306)/k8s_ai",
	}
	separated := []string{"--FORM=separated", "--IMAGE_WEB=registry.local/k8s-ai/aegisops-web"}
	separated = append(separated, base...)
	withPull := append([]string{"--IMAGE_PULL_SECRET=" + secret}, separated...)
	allinone := append([]string{"--FORM=allinone", "--IMAGE_PULL_SECRET=" + secret}, base...)

	pullSecrets := func(t *testing.T, dir string, rel ...string) []string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(append([]string{dir}, rel...)...))
		if err != nil {
			t.Fatalf("读取渲染产物 %v 失败：%v", rel, err)
		}
		var doc renderedPullSecrets
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("渲染产物 %v 不是合法 YAML：%v", rel, err)
		}
		names := doc.Spec.Template.Spec.ImagePullSecrets
		if len(names) == 0 {
			names = doc.Spec.JobTemplate.Spec.Template.Spec.ImagePullSecrets
		}
		var out []string
		for _, s := range names {
			out = append(out, s.Name)
		}
		return out
	}

	// 形态 B：后端、前端、迁移 Job 都要拿到同一个 Secret，且只出现一次。
	dir := renderManifests(t, bash, withPull...)
	for _, rel := range [][]string{
		{"separated", "deployment.yaml"},
		{"separated", "web-deployment.yaml"},
		{"base", "db-migrate-job.yaml"},
	} {
		if got := pullSecrets(t, dir, rel...); len(got) != 1 || got[0] != secret {
			t.Errorf("形态 B 的 %v imagePullSecrets=%v，期望恰好一个 %q", rel, got, secret)
		}
	}

	// 形态 A：后端 Deployment 与迁移 Job。
	dir = renderManifests(t, bash, allinone...)
	for _, rel := range [][]string{{"allinone", "deployment.yaml"}, {"base", "db-migrate-job.yaml"}} {
		if got := pullSecrets(t, dir, rel...); len(got) != 1 || got[0] != secret {
			t.Errorf("形态 A 的 %v imagePullSecrets=%v，期望恰好一个 %q", rel, got, secret)
		}
	}

	// 没给 IMAGE_PULL_SECRET 时必须保持清单原样，不能凭空多出这个键。
	dir = renderManifests(t, bash, separated...)
	if got := pullSecrets(t, dir, "separated", "web-deployment.yaml"); len(got) != 0 {
		t.Errorf("未给 IMAGE_PULL_SECRET 却注入了 imagePullSecrets=%v", got)
	}
}

// tailLines 取输出尾部：失败时给现场看的是结尾几行，而不是整段安装日志。
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
