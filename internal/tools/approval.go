package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/executor"
)

// ApprovalTarget / ApprovalChange / ApprovalCheck 是审批卡的结构化事实，
// 不依赖前端文本解析。它们是决策辅助信息，不是权限边界。
type ApprovalTarget struct {
	Cluster   string `json:"cluster,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Group     string `json:"group,omitempty"`
	Version   string `json:"version,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
}

type ApprovalChange struct {
	Path        string `json:"path"`
	Operation   string `json:"operation,omitempty"`
	Before      any    `json:"before,omitempty"`
	After       any    `json:"after,omitempty"`
	Description string `json:"description,omitempty"`
}

type ApprovalCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// ApprovalPlan 是工具能提供的审批预览。
// Tool/Risk 可由 Agent 补齐；Changes 无法解析时保留原命令和摘要作为兜底。
type ApprovalPlan struct {
	Title        string           `json:"title,omitempty"`
	Tool         string           `json:"tool,omitempty"`
	Operation    string           `json:"operation,omitempty"`
	Risk         string           `json:"risk,omitempty"`
	Target       ApprovalTarget   `json:"target,omitempty"`
	Summary      string           `json:"summary,omitempty"`
	Command      string           `json:"command,omitempty"`
	Changes      []ApprovalChange `json:"changes,omitempty"`
	Checks       []ApprovalCheck  `json:"checks,omitempty"`
	Verification []string         `json:"verification,omitempty"`
}

// ApprovalPlanner 是写工具的可选接口：把命令转成可审阅的目标、变更和验证条件。
type ApprovalPlanner interface {
	PreviewApproval(input json.RawMessage) ApprovalPlan
}

func (t *kubectlTool) PreviewApproval(input json.RawMessage) ApprovalPlan {
	args, err := ParseArgs(input, "command")
	if err != nil {
		return ApprovalPlan{Tool: t.Name(), Summary: "参数无法解析：" + err.Error()}
	}
	command := strings.TrimSpace(args["command"])
	if command == "" {
		return ApprovalPlan{Tool: t.Name(), Summary: "缺少 kubectl 命令"}
	}
	target := approval.ParseCommandTarget(command)
	plan := ApprovalPlan{
		Tool:      t.Name(),
		Operation: target.Operation,
		Command:   command,
		Target: ApprovalTarget{
			Namespace: target.Namespace,
			Kind:      target.Kind,
			Name:      target.Name,
		},
		Changes:      kubectlChanges(command),
		Verification: verificationForKind(target.Kind),
	}
	if target.Kind != "" && target.Operation != "" {
		plan.Title = "修改 " + target.Kind
	}
	if len(plan.Changes) > 0 {
		plan.Summary = fmt.Sprintf("%d 项变更", len(plan.Changes))
	}
	if plan.Title == "" {
		plan.Title = "Kubernetes 操作"
	}
	return plan
}

func kubectlChanges(command string) []ApprovalChange {
	pc, err := executor.ParseCommandDetail(command)
	if err != nil || pc.IsShell {
		return nil
	}
	payload := flagValue(pc.Argv, "-p", "--patch")
	if payload == "" {
		return nil
	}
	patchType := strings.ToLower(flagValue(pc.Argv, "--type"))
	if patchType == "json" || strings.HasPrefix(strings.TrimSpace(payload), "[") {
		return jsonPatchChanges(payload)
	}
	return mergePatchChanges(payload)
}

func flagValue(argv []string, names ...string) string {
	for i := 0; i < len(argv); i++ {
		for _, name := range names {
			if argv[i] == name && i+1 < len(argv) {
				return argv[i+1]
			}
			if strings.HasPrefix(argv[i], name+"=") {
				return argv[i][len(name)+1:]
			}
		}
	}
	return ""
}

func jsonPatchChanges(payload string) []ApprovalChange {
	var operations []map[string]any
	if err := json.Unmarshal([]byte(payload), &operations); err != nil {
		return nil
	}
	changes := make([]ApprovalChange, 0, len(operations))
	for _, op := range operations {
		path, _ := op["path"].(string)
		if path == "" {
			continue
		}
		operation, _ := op["op"].(string)
		change := ApprovalChange{
			Path:      humanizeJSONPointer(path),
			Operation: operation,
			After:     op["value"],
		}
		if change.After == nil {
			change.After = op["from"]
		}
		changes = append(changes, change)
	}
	return changes
}

func mergePatchChanges(payload string) []ApprovalChange {
	var object map[string]any
	if err := json.Unmarshal([]byte(payload), &object); err != nil {
		return nil
	}
	changes := make([]ApprovalChange, 0)
	flattenPatch("", object, &changes)
	return changes
}

func flattenPatch(prefix string, object map[string]any, changes *[]ApprovalChange) {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		switch value := object[key].(type) {
		case map[string]any:
			flattenPatch(path, value, changes)
		default:
			*changes = append(*changes, ApprovalChange{Path: path, Operation: "set", After: value})
		}
	}
}

func humanizeJSONPointer(pointer string) string {
	parts := strings.Split(strings.Trim(pointer, "/"), "/")
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		if _, err := strconv.Atoi(part); err == nil {
			fmt.Fprintf(&b, "[%s]", part)
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(part)
	}
	return b.String()
}

func verificationForKind(kind string) []string {
	switch strings.ToLower(kind) {
	case "deployment", "deployments", "statefulset", "statefulsets", "replicaset", "replicasets":
		return []string{"工作负载可用副本达到期望值", "Pod 全部 Ready，重启次数不再增长", "检查最近 Events 是否出现新的 Warning"}
	case "service", "services", "svc":
		return []string{"Service 已获得 ClusterIP", "Endpoints/EndpointSlice 出现可用后端", "从目标 namespace 内验证端口连通性"}
	case "ingress", "ingresses":
		return []string{"Ingress 状态地址已就绪", "后端 Service 可解析", "检查 Ingress Controller Events"}
	case "configmap", "configmaps":
		return []string{"Pod 已重新加载配置", "相关 Deployment 完成滚动更新", "检查 Pod Ready 与最近 Events"}
	default:
		return []string{"目标对象状态符合预期", "检查相关 Events 是否出现新的 Warning"}
	}
}
