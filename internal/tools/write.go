package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// WriteRunner 是写操作执行器接口（internal/executor 实现，架构测试守护写边界）。
type WriteRunner interface {
	RunKubectl(ctx context.Context, command string) (executor.Result, error)
	ScaleDeployment(ctx context.Context, namespace, name string, replicas int32) (executor.Result, error)
	PatchDeployment(ctx context.Context, namespace, name, patch string) (executor.Result, error)
	PatchConfigMap(ctx context.Context, namespace, name, patch string) (executor.Result, error)
	RestartRollout(ctx context.Context, namespace, name string) (executor.Result, error)
	LabelResource(ctx context.Context, kind, namespace, name, key, value string) (executor.Result, error)
	DeletePod(ctx context.Context, namespace, name string, gracePeriodSeconds *int64) (executor.Result, error)
}

// CommandDescriber 是写工具的可选接口：为审批卡片提供人类可读的命令与变更说明
// （代替原始 JSON 参数，用户能直接看懂将执行什么操作）。
type CommandDescriber interface {
	Describe(input json.RawMessage) (command, change string)
}

// NewWriteTools 创建写操作 Tool（通用 kubectl + 结构化写，全部走审批）。
func NewWriteTools(runner WriteRunner) []Tool {
	return []Tool{
		&kubectlTool{runner: runner},
		&scaleDeploymentTool{runner: runner},
		&patchDeploymentTool{runner: runner},
		&patchConfigMapTool{runner: runner},
		&restartRolloutTool{runner: runner},
		&labelTool{runner: runner},
		&deletePodTool{runner: runner},
	}
}

// kubectlTool：通用 kubectl 命令（只读自动执行，写操作审批后执行）。
type kubectlTool struct{ runner WriteRunner }

func (t *kubectlTool) Name() string { return "kubectl" }
func (t *kubectlTool) Description() string {
	return "执行 kubectl 命令。支持 shell 构造（pipe |、heredoc <<EOF、redirect >、&& / ||、command substitution $() 等）。\n只读命令（get/describe/list/logs/events，且不含 shell 语法）自动执行；写命令及所有含 shell 语法的命令必须走人工审批。\n禁止：交互式命令（edit/attach/port-forward/proxy/run/cp/debug）、读取 Secret 资源、--raw 参数。\n流式命令（logs -f/get -w）以固定超时采样部分输出。输出自动脱敏并截断。\n标准写操作（scale/patch/restart/label/delete pod）建议优先使用对应的结构化写工具（参数校验更严格、风险分级更精确）；\n当结构化工具无法表达操作时（完整 YAML 创建/重建、多 op JSON patch、复合命令），可直接用 kubectl + shell 构造，走审批即可。\n写命令注意（务必遵守）：(1) delete/删除类操作永远是最后选择，PVC/PV/Deployment/StatefulSet/Service/Namespace 的删除必须先给出更小范围的 patch/annotate/rollout 替代方案并征得用户同意，特别是 PVC 绑定失败必须先 patch storageClassName，只有被 immutable 拒绝+用户明确同意的情况下才允许提出 delete pvc；(2) 任何 delete pvc / delete pv / delete namespace 这种 CRITICAL 级别的删除，必须在同一个回答里同时列出 patch 替代方案给用户选择，不能只提删除。"
}
func (t *kubectlTool) InputSchema() any {
	return objSchema(map[string]any{
		"command": strProp("完整 kubectl 命令，如 kubectl get pods -n default 或 kubectl scale deployment/payment-api --replicas=5"),
	}, "command")
}
func (t *kubectlTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "command")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	res, err := t.runner.RunKubectl(ctx, args["command"])
	if err != nil {
		return ResultError("kubectl: " + err.Error()), nil
	}
	return ToolResult{Data: res}, nil
}
func (t *kubectlTool) IsReadOnly(input json.RawMessage) bool {
	args, err := ParseArgs(input, "command")
	if err != nil {
		return false
	}
	return approval.IsReadOnlyCommand(args["command"])
}
func (t *kubectlTool) Risk(input json.RawMessage) model.RiskLevel {
	args, err := ParseArgs(input, "command")
	if err != nil {
		return model.RiskCritical
	}
	return approval.ClassifyCommand(args["command"])
}

// scaleDeploymentTool。
type scaleDeploymentTool struct{ runner WriteRunner }

func (t *scaleDeploymentTool) Name() string { return "scale_deployment" }
func (t *scaleDeploymentTool) Description() string {
	return "调整 Deployment 副本数（client-go UpdateScale），需用户确认。"
}
func (t *scaleDeploymentTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
		"name":      strProp("Deployment 名称"),
		"replicas":  intProp("目标副本数"),
	}, "namespace", "name", "replicas")
}
func (t *scaleDeploymentTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "namespace", "name", "replicas")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	replicas, err := strconv.ParseInt(args["replicas"], 10, 32)
	if err != nil || replicas < 0 {
		return ResultError("invalid replicas: " + args["replicas"]), nil
	}
	res, err := t.runner.ScaleDeployment(ctx, args["namespace"], args["name"], int32(replicas))
	if err != nil {
		return ResultError("scale_deployment: " + err.Error()), nil
	}
	return ToolResult{Data: res}, nil
}
func (t *scaleDeploymentTool) IsReadOnly(json.RawMessage) bool { return false }
func (t *scaleDeploymentTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskMedium
}
func (t *scaleDeploymentTool) Describe(input json.RawMessage) (string, string) {
	args, _ := ParseArgs(input, "namespace", "name", "replicas")
	return fmt.Sprintf("kubectl -n %s scale deployment/%s --replicas=%s", args["namespace"], args["name"], args["replicas"]),
		fmt.Sprintf("调整 Deployment %s/%s 副本数为 %s", args["namespace"], args["name"], args["replicas"])
}

// patchDeploymentTool。
type patchDeploymentTool struct{ runner WriteRunner }

func (t *patchDeploymentTool) Name() string { return "patch_deployment" }
func (t *patchDeploymentTool) Description() string {
	return "对 Deployment 应用 strategic merge patch（如调整资源限制/镜像），需用户确认。patch 示例：{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"app\",\"resources\":{\"limits\":{\"memory\":\"512Mi\"}}}]}}}}"
}
func (t *patchDeploymentTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
		"name":      strProp("Deployment 名称"),
		"patch":     strProp("strategic merge patch JSON"),
	}, "namespace", "name", "patch")
}
func (t *patchDeploymentTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "namespace", "name", "patch")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	res, err := t.runner.PatchDeployment(ctx, args["namespace"], args["name"], args["patch"])
	if err != nil {
		return ResultError("patch_deployment: " + err.Error()), nil
	}
	return ToolResult{Data: res}, nil
}
func (t *patchDeploymentTool) IsReadOnly(json.RawMessage) bool { return false }
func (t *patchDeploymentTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskHigh
}
func (t *patchDeploymentTool) Describe(input json.RawMessage) (string, string) {
	args, _ := ParseArgs(input, "namespace", "name", "patch")
	return fmt.Sprintf("kubectl -n %s patch deployment/%s --patch '<patch>'", args["namespace"], args["name"]),
		fmt.Sprintf("修改 Deployment %s/%s：\n%s", args["namespace"], args["name"], prettyJSON(args["patch"]))
}

// patchConfigMapTool。
type patchConfigMapTool struct{ runner WriteRunner }

func (t *patchConfigMapTool) Name() string { return "patch_configmap" }
func (t *patchConfigMapTool) Description() string {
	return "对 ConfigMap 应用 strategic merge patch（修复配置错误，如 nginx 端口/状态码），需用户确认。"
}
func (t *patchConfigMapTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
		"name":      strProp("ConfigMap 名称"),
		"patch":     strProp("strategic merge patch JSON，如 {\"data\":{\"default.conf\":\"...\"}}"),
	}, "namespace", "name", "patch")
}
func (t *patchConfigMapTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "namespace", "name", "patch")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	res, err := t.runner.PatchConfigMap(ctx, args["namespace"], args["name"], args["patch"])
	if err != nil {
		return ResultError("patch_configmap: " + err.Error()), nil
	}
	return ToolResult{Data: res}, nil
}
func (t *patchConfigMapTool) IsReadOnly(json.RawMessage) bool { return false }
func (t *patchConfigMapTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskHigh
}
func (t *patchConfigMapTool) Describe(input json.RawMessage) (string, string) {
	args, _ := ParseArgs(input, "namespace", "name", "patch")
	return fmt.Sprintf("kubectl -n %s patch configmap/%s --patch '<patch>'", args["namespace"], args["name"]),
		fmt.Sprintf("修改 ConfigMap %s/%s：\n%s", args["namespace"], args["name"], configMapChanges(args["patch"]))
}

// restartRolloutTool。
type restartRolloutTool struct{ runner WriteRunner }

func (t *restartRolloutTool) Name() string { return "restart_rollout" }
func (t *restartRolloutTool) Description() string {
	return "触发 Deployment 滚动重启（注入 restartedAt 注解），需用户确认。"
}
func (t *restartRolloutTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace": strProp("命名空间"),
		"name":      strProp("Deployment 名称"),
	}, "namespace", "name")
}
func (t *restartRolloutTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "namespace", "name")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	res, err := t.runner.RestartRollout(ctx, args["namespace"], args["name"])
	if err != nil {
		return ResultError("restart_rollout: " + err.Error()), nil
	}
	return ToolResult{Data: res}, nil
}
func (t *restartRolloutTool) IsReadOnly(json.RawMessage) bool { return false }
func (t *restartRolloutTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskMedium
}
func (t *restartRolloutTool) Describe(input json.RawMessage) (string, string) {
	args, _ := ParseArgs(input, "namespace", "name")
	return fmt.Sprintf("kubectl -n %s rollout restart deployment/%s", args["namespace"], args["name"]),
		fmt.Sprintf("触发 Deployment %s/%s 滚动重启（新建 Pod 应用新配置）", args["namespace"], args["name"])
}

// labelTool。
type labelTool struct{ runner WriteRunner }

func (t *labelTool) Name() string { return "label" }
func (t *labelTool) Description() string {
	return "给资源打标签（deployment/statefulset/daemonset/pod/service），需用户确认。"
}
func (t *labelTool) InputSchema() any {
	return objSchema(map[string]any{
		"kind":      strProp("资源类型：deployment/statefulset/daemonset/pod/service"),
		"namespace": strProp("命名空间"),
		"name":      strProp("资源名称"),
		"key":       strProp("标签键"),
		"value":     strProp("标签值"),
	}, "kind", "namespace", "name", "key", "value")
}
func (t *labelTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "kind", "namespace", "name", "key", "value")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	res, err := t.runner.LabelResource(ctx, args["kind"], args["namespace"], args["name"], args["key"], args["value"])
	if err != nil {
		return ResultError("label: " + err.Error()), nil
	}
	return ToolResult{Data: res}, nil
}
func (t *labelTool) IsReadOnly(json.RawMessage) bool { return false }
func (t *labelTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskMedium
}
func (t *labelTool) Describe(input json.RawMessage) (string, string) {
	args, _ := ParseArgs(input, "kind", "namespace", "name", "key", "value")
	return fmt.Sprintf("kubectl -n %s label %s/%s %s=%s", args["namespace"], args["kind"], args["name"], args["key"], args["value"]),
		fmt.Sprintf("给 %s %s/%s 打标签 %s=%s", args["kind"], args["namespace"], args["name"], args["key"], args["value"])
}

// deletePodTool。
type deletePodTool struct{ runner WriteRunner }

func (t *deletePodTool) Name() string { return "delete_pod" }
func (t *deletePodTool) Description() string {
	return "删除 Pod（Deployment 会自动重建，用于应用新配置/清理异常实例），需用户确认。"
}
func (t *deletePodTool) InputSchema() any {
	return objSchema(map[string]any{
		"namespace":          strProp("命名空间"),
		"name":               strProp("Pod 名称"),
		"gracePeriodSeconds": intProp("优雅终止秒数（可选，默认继承对象设置）"),
	}, "namespace", "name")
}
func (t *deletePodTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := ParseArgs(input, "namespace", "name")
	if err != nil {
		return ResultError(err.Error()), nil
	}
	var grace *int64
	if v := args["gracePeriodSeconds"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return ResultError("invalid gracePeriodSeconds"), nil
		}
		grace = &n
	}
	res, err := t.runner.DeletePod(ctx, args["namespace"], args["name"], grace)
	if err != nil {
		return ResultError("delete_pod: " + err.Error()), nil
	}
	return ToolResult{Data: res}, nil
}
func (t *deletePodTool) IsReadOnly(json.RawMessage) bool { return false }
func (t *deletePodTool) Risk(json.RawMessage) model.RiskLevel {
	return model.RiskHigh
}
func (t *deletePodTool) Describe(input json.RawMessage) (string, string) {
	args, _ := ParseArgs(input, "namespace", "name")
	return fmt.Sprintf("kubectl -n %s delete pod/%s", args["namespace"], args["name"]),
		fmt.Sprintf("删除 Pod %s/%s（Deployment 会自动重建）", args["namespace"], args["name"])
}

// prettyJSON 把 patch JSON 缩进美化（无法解析时原样返回）。
func prettyJSON(s string) string {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return s
	}
	return string(b)
}

// configMapChanges 从 patch 中提取 data 键并解码为可读配置内容。
func configMapChanges(patch string) string {
	var m struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(patch), &m); err != nil || len(m.Data) == 0 {
		return prettyJSON(patch)
	}
	parts := make([]string, 0, len(m.Data))
	for k, v := range m.Data {
		parts = append(parts, fmt.Sprintf("data.%s:\n%s", k, v))
	}
	return strings.Join(parts, "\n\n")
}
