package arch_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/k8s-ai/k8s-ai/internal/config"
)

// workload 是清单断言需要的最小 Deployment / Job / CronJob 结构。
type workload struct {
	Spec workloadSpec `yaml:"spec"`
}

type workloadSpec struct {
	Template    podTemplateHolder `yaml:"template"`
	JobTemplate jobTemplateHolder `yaml:"jobTemplate"`
}

type podTemplateHolder struct {
	Spec podTemplateSpec `yaml:"spec"`
}

type jobTemplateHolder struct {
	Spec struct {
		Template podTemplateHolder `yaml:"template"`
	} `yaml:"spec"`
}

type podTemplateSpec struct {
	Containers []containerSpec `yaml:"containers"`
	Volumes    []volumeSpec    `yaml:"volumes"`
}

type containerSpec struct {
	Name         string            `yaml:"name"`
	Image        string            `yaml:"image"`
	VolumeMounts []volumeMountSpec `yaml:"volumeMounts"`
}

type volumeMountSpec struct {
	Name      string `yaml:"name"`
	MountPath string `yaml:"mountPath"`
}

type volumeSpec struct {
	Name      string `yaml:"name"`
	ConfigMap struct {
		Name string `yaml:"name"`
	} `yaml:"configMap"`
	PersistentVolumeClaim struct {
		ClaimName string `yaml:"claimName"`
	} `yaml:"persistentVolumeClaim"`
}

// podSpec 返回工作负载的 Pod 模板：Deployment/Job 与 CronJob 层级不同，取非空的那个。
func (w workload) podSpec() podTemplateSpec {
	if len(w.Spec.Template.Spec.Containers) > 0 || len(w.Spec.Template.Spec.Volumes) > 0 {
		return w.Spec.Template.Spec
	}
	return w.Spec.JobTemplate.Spec.Template.Spec
}

func readWorkload(t *testing.T, rel ...string) workload {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, rel...)...))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var w workload
	if err := yaml.Unmarshal(raw, &w); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return w
}

// TestDeployConfigMapsValidateUnderOwnProfile 锁定 P6.2 清单与 P6.12 校验剖面的对齐：
// 平台清单里的 ConfigMap 必须在 server 剖面零告警通过（含真实 Load + Validate），
// 巡检 CronJob 的 ConfigMap 必须在 CLI 剖面零告警通过。清单与模板/校验规则一旦漂移，
// 这条会立刻挡住，避免「按文档装完才发现 config 有问题」。
func TestDeployConfigMapsValidateUnderOwnProfile(t *testing.T) {
	// DSN 只经 Secret 注入，清单里恒空：先模拟运行时注入。
	t.Setenv("K8S_AI_PLATFORM_STORE_DSN", "k8s_ai:s3cret@tcp(db.internal:3306)/k8s_ai?charset=utf8mb4&parseTime=true&loc=Local")

	cases := []struct {
		manifest string
		mode     config.ValidateMode
	}{
		{filepath.Join("..", "..", "deploy", "base", "configmap.yaml"), config.ValidateModeServer},
		{filepath.Join("..", "..", "deploy", "scan", "configmap.yaml"), config.ValidateModeCLI},
	}
	for _, tc := range cases {
		raw, err := os.ReadFile(tc.manifest)
		if err != nil {
			t.Fatalf("%s: read: %v", tc.manifest, err)
		}
		var cm struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data map[string]string `yaml:"data"`
		}
		if err := yaml.Unmarshal(raw, &cm); err != nil {
			t.Fatalf("%s: parse: %v", tc.manifest, err)
		}
		body, ok := cm.Data["config.yaml"]
		if !ok {
			t.Fatalf("%s: 缺 data.config.yaml", cm.Metadata.Name)
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		warnings, err := config.Diagnostics(path, tc.mode)
		if err != nil {
			t.Fatalf("%s: diagnostics: %v", cm.Metadata.Name, err)
		}
		if len(warnings) != 0 {
			t.Errorf("%s 在 %s 剖面应零告警，实际 %v", cm.Metadata.Name, tc.mode, warnings)
		}

		cfg, err := config.Load(config.LoadOptions{ConfigFile: path, CLI: tc.mode == config.ValidateModeCLI})
		if err != nil {
			t.Errorf("%s: 按 %s 剖面加载失败: %v", cm.Metadata.Name, tc.mode, err)
			continue
		}
		if tc.mode == config.ValidateModeServer && !cfg.Platform.Store.IsMySQL() {
			t.Errorf("%s: 平台清单应以 mysql 为主存储，实际 %q", cm.Metadata.Name, cfg.Platform.Store.Driver)
		}
	}
}

// TestDeployPathsMatchVolumeMounts 锁定落盘目录与挂载点不脱节：镜像根文件系统只读，
// report.directory / agent.session_dir / agent.audit_dir 必须落在某个可写卷挂载点之下，
// 否则进程会在运行期写失败。
func TestDeployPathsMatchVolumeMounts(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "base", "configmap.yaml"))
	if err != nil {
		t.Fatalf("read configmap: %v", err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatalf("parse configmap: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(cm.Data["config.yaml"]), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("K8S_AI_PLATFORM_STORE_DSN", "k8s_ai:s3cret@tcp(db.internal:3306)/k8s_ai?charset=utf8mb4&parseTime=true&loc=Local")
	cfg, err := config.Load(config.LoadOptions{ConfigFile: path})
	if err != nil {
		t.Fatalf("load platform config: %v", err)
	}

	var mounts []string
	for _, c := range readWorkload(t, "deploy", "allinone", "deployment.yaml").podSpec().Containers {
		for _, m := range c.VolumeMounts {
			mounts = append(mounts, m.MountPath)
		}
	}
	if len(mounts) == 0 {
		t.Fatal("deploy/allinone/deployment.yaml 没解析出任何 volumeMounts")
	}
	for label, dir := range map[string]string{
		"report.directory":  cfg.Report.Directory,
		"agent.session_dir": cfg.Agent.SessionDir,
		"agent.audit_dir":   cfg.Agent.AuditDir,
	} {
		if dir == "" {
			t.Errorf("%s 为空", label)
			continue
		}
		if !coveredByAnyMount(dir, mounts) {
			t.Errorf("%s=%s 不在可写卷挂载点 %v 之下", label, dir, mounts)
		}
	}
}

func coveredByAnyMount(dir string, mounts []string) bool {
	for _, m := range mounts {
		if dir == m || strings.HasPrefix(dir, strings.TrimSuffix(m, "/")+"/") {
			return true
		}
	}
	return false
}

// TestDeployConfigVolumePointsAtExpectedConfigMap 挡住「清单改名后挂错配置」这类漂移：
// 平台清单（server + 迁移 Job）只挂 k8s-ai-config，巡检 CronJob 只挂 k8s-ai-scan-config。
func TestDeployConfigVolumePointsAtExpectedConfigMap(t *testing.T) {
	for _, tc := range []struct {
		manifest []string
		want     string
	}{
		{[]string{"deploy", "allinone", "deployment.yaml"}, "k8s-ai-config"},
		{[]string{"deploy", "separated", "deployment.yaml"}, "k8s-ai-config"},
		{[]string{"deploy", "base", "db-migrate-job.yaml"}, "k8s-ai-config"},
		{[]string{"deploy", "scan", "cronjob.yaml"}, "k8s-ai-scan-config"},
	} {
		got := ""
		for _, v := range readWorkload(t, tc.manifest...).podSpec().Volumes {
			if v.Name == "config" {
				got = v.ConfigMap.Name
			}
		}
		if got != tc.want {
			t.Errorf("%s 的 config 卷应指向 %s，实际 %q", filepath.Join(tc.manifest...), tc.want, got)
		}
	}
}

// staticEnabledEnvKey 是两种部署形态之间唯一允许差异的键：形态 B 用它关掉后端内嵌的 SPA
// 静态托管，让同一个后端镜像只对外提供 API。
const staticEnabledEnvKey = "K8S_AI_PLATFORM_SERVER_STATIC_ENABLED"

// TestDeployFormsDifferOnlyByStaticSwitch 锁住「两种形态只差一个开关」这个约定。
// 两份 Deployment 各自成文（deploy/allinone 与 deploy/separated），升级时很容易只改一份：
// 形态 B 漏掉开关就会静默地把内嵌 SPA 又跑起来，或者两边的镜像、探针、安全上下文悄悄分叉。
// 这里按结构逐字段比对——把形态 B 的该 env 项摘掉之后，两份清单必须完全等价。
func TestDeployFormsDifferOnlyByStaticSwitch(t *testing.T) {
	allInOne := readManifestDoc(t, "deploy", "allinone", "deployment.yaml")
	separated := readManifestDoc(t, "deploy", "separated", "deployment.yaml")

	if _, ok := envVarValue(t, allInOne, staticEnabledEnvKey); ok {
		t.Errorf("形态 A 不应设置 %s：默认就该内嵌托管 SPA", staticEnabledEnvKey)
	}
	switchValue, ok := envVarValue(t, separated, staticEnabledEnvKey)
	if !ok {
		t.Fatalf("形态 B 必须设置 %s，否则这份清单与形态 A 完全重复", staticEnabledEnvKey)
	}
	if switchValue != "false" {
		t.Errorf("形态 B 的 %s 应为 \"false\"，实际 %q", staticEnabledEnvKey, switchValue)
	}
	dropEnvVar(t, separated, staticEnabledEnvKey)

	if !reflect.DeepEqual(allInOne, separated) {
		t.Errorf("两种形态的 Deployment 除 %s 之外必须逐字段一致\n形态 A:\n%s\n形态 B:\n%s",
			staticEnabledEnvKey, marshalManifest(t, allInOne), marshalManifest(t, separated))
	}
}

// readManifestDoc 把清单读成通用文档，用于「两份清单是否等价」这类结构比对。
func readManifestDoc(t *testing.T, rel ...string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, rel...)...))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return doc
}

func marshalManifest(t *testing.T, doc map[string]any) string {
	t.Helper()
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return string(out)
}

// containerEnv 定位 Pod 模板里唯一容器的 env 列表；清单只有一个容器，失败时直接把原因摊开。
func containerEnv(t *testing.T, doc map[string]any) []any {
	t.Helper()
	spec, _ := doc["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	if len(containers) != 1 {
		t.Fatalf("Pod 模板应只有一个容器，实际 %d", len(containers))
	}
	first, _ := containers[0].(map[string]any)
	env, ok := first["env"].([]any)
	if !ok {
		t.Fatal("容器没有 env 段，无法比对形态差异")
	}
	return env
}

func envVarValue(t *testing.T, doc map[string]any, key string) (string, bool) {
	for _, item := range containerEnv(t, doc) {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := entry["name"].(string); name == key {
			value, _ := entry["value"].(string)
			return value, true
		}
	}
	return "", false
}

func dropEnvVar(t *testing.T, doc map[string]any, key string) {
	env := containerEnv(t, doc)
	kept := make([]any, 0, len(env))
	for _, item := range env {
		if entry, ok := item.(map[string]any); ok {
			if name, _ := entry["name"].(string); name == key {
				continue
			}
		}
		kept = append(kept, item)
	}
	spec, _ := doc["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	first, _ := containers[0].(map[string]any)
	first["env"] = kept
}
