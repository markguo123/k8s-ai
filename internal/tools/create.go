// 三期创建工具 create_resource（R3）。
//
// 与 kubectl 通道的分工：kubectl 仍负责改/删等既有写操作，创建走这条结构化通道，
// 因为创建需要「先服务端 dry run 出提案 → 人工确认 → 原样 apply」这一步，
// shell 命令无法表达这种一致性约束（设计文档 D-09/D-12）。
//
// 安全语义：
//   - mode=dry_run 只调用 apiserver 校验，不落盘 → 只读 + SAFE，自动执行；
//   - mode=apply 必须先有同指纹的 dry run 提案，否则拒绝；
//   - 风险按 Kind/scope/危险字段逐项判定（approval.CreateSubjectRisk），
//     工具描述里只给可机械验证的约束，不写「注意安全」这类空话；
//   - Secret 只接受占位符（manifestx.PlaceholderGenerate），值由执行器生成，永不进入 Data/Text。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/approval"
	"github.com/k8s-ai/k8s-ai/internal/executor"
	"github.com/k8s-ai/k8s-ai/internal/manifestx"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CreateRunner 是创建通道的执行面（executor.Runner 实现；写动作只在 executor 包内发生）。
type CreateRunner interface {
	// MappingFor 用集群 discovery 解析 apiVersion/kind → 资源名与 scope。
	MappingFor(apiVersion, kind string) (executor.ApplyMapping, error)
	// GetObjectStatus 判断对象是否已存在、目标命名空间是否存在。
	GetObjectStatus(ctx context.Context, ref executor.ObjectRef) (executor.ObjectStatus, error)
	// Apply 以 Server-Side Apply 提交单个对象（支持 dry run 与就绪校验）。
	Apply(ctx context.Context, req executor.ApplyRequest) (executor.ApplyResult, error)
}

// CreateOptions 是创建工具的装配参数（来自 agent.create.* 与 kubernetes.namespace）。
type CreateOptions struct {
	Policy           manifestx.Policy
	Limits           manifestx.Limits
	DefaultNamespace string
	SecretAccess     string
	// VerifyTimeout 是就绪等待的默认上限（LLM 可用 timeout_seconds 覆盖）。
	VerifyTimeout time.Duration
}

// 创建工具的入参取值与错误码（稳定契约：进入审计与提示词）。
const (
	CreateModeDryRun = "dry_run"
	CreateModeApply  = "apply"

	errCreateInvalidArguments = "invalid_arguments"
	errCreateManifestRejected = "manifest_rejected"
	errCreateValidationFailed = "validation_failed"
	errCreateProposalRequired = "proposal_required"
	errCreateAlreadyExists    = "already_exists"
	errCreateNamespaceMissing = "namespace_missing"
	errCreateApplyFailed      = "apply_failed"
	errCreateRunnerUnavail    = "runner_unavailable"

	statusValidated = "validated"
	statusApplied   = "applied"
	statusPartial   = "partial"
	statusRejected  = "rejected"
)

type createTool struct {
	opts      CreateOptions
	runner    CreateRunner
	proposals *ProposalStore
	sessionID string
}

// NewCreateTool 创建 create_resource 工具。
// store/sessionID 用于跨轮次校验「apply 的内容必须就是刚预演过的那份」。
func NewCreateTool(opts CreateOptions, runner CreateRunner, store *ProposalStore, sessionID string) Tool {
	if store == nil {
		store = NewProposalStore()
	}
	return &createTool{opts: opts, runner: runner, proposals: store, sessionID: sessionID}
}

func (t *createTool) Name() string { return "create_resource" }

func (t *createTool) SchemaMetadata() string {
	return "[READ/WRITE][RISK: MEDIUM-HIGH BY KIND/SCOPE/FIELDS][APPROVAL REQUIRED FOR APPLY]"
}

func (t *createTool) Description() string {
	return "创建 Kubernetes 资源（YAML/JSON，支持一次多个对象）。固定两步：\n" +
		"1) mode=dry_run（默认）：服务端全量校验并返回创建提案（对象清单、风险等级与原因、待改字段），不改变集群；\n" +
		"2) 用户确认后用完全相同的 manifest 再调一次 mode=apply 才会真正创建；指纹不一致会被拒绝，必须重新 dry_run。\n" +
		"约束：manifest 必须是最终生效的纯 YAML/JSON，不接受 helm/kustomize 模板表达式；" +
		"每个对象必须有 apiVersion/kind/metadata.name；namespaced 资源省略 namespace 时按默认命名空间创建。\n" +
		"Secret 只能在 stringData 里写 " + manifestx.PlaceholderGenerate + " 由执行器生成随机值，禁止编造 Secret 值。\n" +
		"资源已存在时该对象会被拒绝并提示改用 patch/update；cluster-scoped 资源默认禁止创建。\n" +
		"verify=true 时 apply 后会等待对象就绪并附带最近事件。"
}

func (t *createTool) InputSchema() any {
	return objSchema(map[string]any{
		"manifest":        strProp("YAML/JSON 清单，多个对象用 --- 分隔；必须是最终生效内容，不能含模板表达式"),
		"mode":            strProp("dry_run＝服务端校验并产出提案（默认，不改变集群）；apply＝提交已确认的同一份清单"),
		"verify":          boolProp("apply 后是否等待对象就绪（Deployment 可用副本、Pod Running 等），默认 false"),
		"timeout_seconds": intProp("就绪等待上限秒数，仅 verify=true 时生效，默认 30"),
	}, "manifest")
}

// createArgs 是一次调用的入参视图。
type createArgs struct {
	manifest      string
	apply         bool
	verify        bool
	verifyTimeout time.Duration
}

func (t *createTool) parseInput(input json.RawMessage) (createArgs, error) {
	raw, err := ParseArgs(input, "manifest")
	if err != nil {
		return createArgs{}, err
	}
	mode := strings.ToLower(strings.TrimSpace(raw["mode"]))
	if mode == "" {
		mode = CreateModeDryRun // 缺省走安全侧
	}
	if mode != CreateModeDryRun && mode != CreateModeApply {
		return createArgs{}, fmt.Errorf("%s: mode 只能是 dry_run 或 apply，当前 %q", errCreateInvalidArguments, raw["mode"])
	}
	args := createArgs{
		manifest:      raw["manifest"],
		apply:         mode == CreateModeApply,
		verify:        strings.EqualFold(strings.TrimSpace(raw["verify"]), "true"),
		verifyTimeout: t.opts.VerifyTimeout,
	}
	if n, err := strconv.Atoi(strings.TrimSpace(raw["timeout_seconds"])); err == nil && n > 0 {
		args.verifyTimeout = time.Duration(n) * time.Second
	}
	return args, nil
}

func (t *createTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	args, err := t.parseInput(input)
	if err != nil {
		return resultWithCode(errCreateInvalidArguments, err.Error(), nil), nil
	}
	if t.runner == nil {
		return resultWithCode(errCreateRunnerUnavail, "创建通道未装配（缺少集群连接）", nil), nil
	}
	objs, err := manifestx.Parse(args.manifest, t.parseOptions())
	if err != nil {
		return resultWithCode(errCreateManifestRejected, err.Error(), nil), nil
	}
	if manifestx.HasErrors(objs) {
		return t.rejected(objs, errCreateValidationFailed, "清单校验未通过，未做任何变更"), nil
	}
	if args.apply {
		return t.apply(ctx, args, objs), nil
	}
	return t.dryRun(ctx, args, objs), nil
}

// dryRun 逐对象走 apiserver 校验；只有全部通过才登记提案指纹（D-09）。
func (t *createTool) dryRun(ctx context.Context, args createArgs, objs []manifestx.Object) ToolResult {
	outcomes := make([]map[string]any, 0, len(objs))
	failed := false
	for i := range objs {
		o := &objs[i]
		res, err := t.runner.Apply(ctx, executor.ApplyRequest{
			Object:    o.Content(),
			Namespace: o.Namespace,
			DryRun:    true,
		})
		item := t.baseOutcome(o)
		if err != nil {
			failed = true
			item["status"] = "rejected"
			item["error"] = err.Error()
		} else {
			item["status"] = statusValidated
			item["existed"] = res.Existed
			if res.Existed {
				item["note"] = "同名对象已存在，apply 会被拒绝；如需修改请改用 patch/update"
			}
		}
		outcomes = append(outcomes, item)
	}
	risk := t.riskFor(objs, false)
	data := map[string]any{
		"mode":        CreateModeDryRun,
		"status":      statusValidated,
		"risk":        risk.Level,
		"riskReasons": risk.Reasons,
		"objects":     outcomes,
	}
	if failed {
		data["status"] = statusRejected
		data["error"] = "服务端校验未通过，修正清单后重新 dry_run"
		return ToolResult{Data: data, Text: t.summaryText(data)}
	}
	// 提案登记：apply 阶段只接受这里记下的指纹序列。
	t.proposals.Record(t.sessionID, manifestx.Fingerprints(objs), describeObjects(objs))
	data["next"] = "把上面的提案（对象清单 + 风险原因）转述给用户确认；用户同意后，用完全相同的 manifest 调 mode=apply"
	return ToolResult{Data: data, Text: t.summaryText(data)}
}

// apply 提交已确认的清单：逐对象独立处理，不做批量回滚（与 kubectl apply 语义一致）。
func (t *createTool) apply(ctx context.Context, args createArgs, objs []manifestx.Object) ToolResult {
	fingerprints := manifestx.Fingerprints(objs)
	if ok, reason := t.proposals.Match(t.sessionID, fingerprints); !ok {
		return resultWithCode(errCreateProposalRequired,
			"未执行 dry run 或清单与提案不一致，拒绝创建："+reason+
				"；请先用同一份 manifest 调 mode=dry_run，把提案交给用户确认", nil)
	}
	risk := t.riskFor(objs, true)
	outcomes := make([]map[string]any, 0, len(objs))
	applied, refused := 0, 0
	for i := range objs {
		o := &objs[i]
		item, block := t.precheckObject(ctx, o)
		if block != "" {
			refused++
			item["status"] = "rejected"
			item["error"] = block
			outcomes = append(outcomes, item)
			continue
		}
		res, err := t.runner.Apply(ctx, executor.ApplyRequest{
			Object:        o.Content(),
			Namespace:     o.Namespace,
			Verify:        args.verify,
			VerifyTimeout: args.verifyTimeout,
		})
		if err != nil {
			refused++
			item["status"] = "failed"
			item["error"] = err.Error()
			outcomes = append(outcomes, item)
			continue
		}
		applied++
		item["status"] = statusApplied
		item["uid"] = res.UID
		if res.Summary != nil {
			item["summary"] = res.Summary
		}
		if len(res.SecretKeys) > 0 {
			item["secretKeys"] = res.SecretKeys // 只有 key 名与来源，没有值
		}
		if len(res.GeneratedKeys) > 0 {
			item["generatedKeys"] = res.GeneratedKeys
			item["secretNote"] = "Secret 值由执行器随机生成，不进入会话与模型上下文；如需原值请让用户从集群侧读取"
		}
		if res.Verify != nil {
			item["verify"] = res.Verify
		}
		outcomes = append(outcomes, item)
	}
	status := statusApplied
	switch {
	case applied == 0:
		status = statusRejected
	case refused > 0:
		status = statusPartial
	}
	data := map[string]any{
		"mode":        CreateModeApply,
		"status":      status,
		"risk":        risk.Level,
		"riskReasons": risk.Reasons,
		"applied":     applied,
		"refused":     refused,
		"objects":     outcomes,
	}
	if applied == 0 {
		data["error"] = errCreateApplyFailed + ": 所有对象都未创建成功，见 objects[].error"
	} else if refused > 0 {
		data["note"] = "部分对象未创建；失败对象不会回滚已成功部分（与 kubectl apply 一致），请按 objects[].error 逐个处理"
	}
	return ToolResult{Data: data, Text: t.summaryText(data)}
}

// precheckObject 在提交前拦住「命名空间不存在」与「同名对象已存在」，
// 把可预见的失败转成明确建议，而不是让 apiserver 报一句难懂的 404/409。
func (t *createTool) precheckObject(ctx context.Context, o *manifestx.Object) (map[string]any, string) {
	item := t.baseOutcome(o)
	if o.IsSecret() {
		// Secret 不查存在性：GetObjectStatus 会把已有对象整体读回进程，
		// 与「不读取 Secret 内容值」的红线冲突（executor 同样跳过该查询）。
		return item, ""
	}
	status, err := t.runner.GetObjectStatus(ctx, executor.ObjectRef{
		APIVersion: o.APIVersion, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name,
	})
	if err != nil {
		return item, "" // 存在性查不动就交给 apiserver 裁决
	}
	if status.NamespaceMissing {
		return item, fmt.Sprintf("%s: 命名空间 %s 不存在；请先创建命名空间或改用已有命名空间", errCreateNamespaceMissing, o.Namespace)
	}
	if status.Found {
		return item, fmt.Sprintf("%s: %s/%s 在命名空间 %s 已存在；本次调用是创建语义，不会覆盖，请改用 patch/update 或换名字",
			errCreateAlreadyExists, o.Kind, o.Name, o.Namespace)
	}
	return item, ""
}

// parseOptions 组装 manifestx 解析参数：门禁、上限、默认命名空间与 discovery 映射。
func (t *createTool) parseOptions() manifestx.Options {
	return manifestx.Options{
		Policy:           t.opts.Policy,
		Limits:           t.opts.Limits,
		Mapper:           createMapper{runner: t.runner},
		DefaultNamespace: t.opts.DefaultNamespace,
		SecretAccess:     t.opts.SecretAccess,
	}
}

func (t *createTool) baseOutcome(o *manifestx.Object) map[string]any {
	item := map[string]any{
		"index":    o.Index,
		"kind":     o.Kind,
		"name":     o.Name,
		"manifest": o.Manifest, // 已脱敏的展示版（Secret 值不在此处）
	}
	if o.Namespace != "" {
		item["namespace"] = o.Namespace
	}
	if warns := o.Warnings(); len(warns) > 0 {
		item["findings"] = warns
	}
	return item
}

// riskFor 给出风险结论。解析失败已在前面拒绝，这里只处理合法清单。
func (t *createTool) riskFor(objs []manifestx.Object, apply bool) approval.CreateRisk {
	subjects := make([]approval.CreateSubject, 0, len(objs))
	for i := range objs {
		o := &objs[i]
		subjects = append(subjects, approval.CreateSubject{
			Group:     o.GVK.Group,
			Kind:      o.Kind,
			Scope:     scopeString(o.Scope),
			Dangerous: o.Dangerous,
			Unknown:   o.UnknownKind,
			Secret:    o.IsSecret(),
			DryRun:    !apply,
		})
	}
	return approval.CreateSubjectRisk(subjects)
}

func (t *createTool) IsReadOnly(input json.RawMessage) bool {
	args, err := t.parseInput(input)
	return err == nil && !args.apply
}

func (t *createTool) Risk(input json.RawMessage) model.RiskLevel {
	args, err := t.parseInput(input)
	if err != nil {
		return model.RiskHigh // 入参都解析不动时按高风险走人工审批
	}
	if !args.apply {
		return model.RiskSafe
	}
	objs, err := manifestx.Parse(args.manifest, t.parseOptions())
	if err != nil {
		return model.RiskHigh
	}
	return t.riskFor(objs, true).Level
}

// Describe 给审批卡片提供命令与变更说明（manifest 已是脱敏展示版）。
func (t *createTool) Describe(input json.RawMessage) (command, change string) {
	args, err := t.parseInput(input)
	if err != nil {
		return "create_resource", "入参解析失败：" + err.Error()
	}
	mode := CreateModeDryRun
	if args.apply {
		mode = CreateModeApply
	}
	objs, perr := manifestx.Parse(args.manifest, t.parseOptions())
	if perr != nil {
		return "create_resource mode=" + mode, "清单无法解析：" + perr.Error()
	}
	command = "create_resource mode=" + mode + " objects=" + strings.Join(describeObjects(objs), ", ")
	var sb strings.Builder
	risk := t.riskFor(objs, args.apply)
	fmt.Fprintf(&sb, "风险等级 %s", risk.Level)
	for _, reason := range risk.Reasons {
		sb.WriteString("\n· " + reason)
	}
	if len(objs) > 0 {
		sb.WriteString("\n清单：\n")
		sb.WriteString(truncateText(strings.Join(manifestBlocks(objs), "---\n"), describeManifestBytes))
	}
	return command, sb.String()
}

// GrantTarget 限定「本次会话不再询问」的可复用范围：
// 单对象创建授权到具体 kind/name，多对象不产生可复用授权。
func (t *createTool) GrantTarget(input json.RawMessage) (kind, name, namespace string, ok bool) {
	args, err := t.parseInput(input)
	if err != nil || !args.apply {
		return "", "", "", false
	}
	objs, perr := manifestx.Parse(args.manifest, t.parseOptions())
	if perr != nil || len(objs) != 1 {
		return "", "", "", false
	}
	return objs[0].Kind, objs[0].Name, objs[0].Namespace, true
}

func (t *createTool) summaryText(data map[string]any) string {
	mode, _ := data["mode"].(string)
	status, _ := data["status"].(string)
	objects, _ := data["objects"].([]map[string]any)
	var sb strings.Builder
	fmt.Fprintf(&sb, "create_resource %s：%s（%d 个对象，风险 %s）", mode, status, len(objects), data["risk"])
	for _, item := range objects {
		line := fmt.Sprintf("\n- %v/%v", item["kind"], item["name"])
		if ns, okk := item["namespace"].(string); okk && ns != "" {
			line += " ns=" + ns
		}
		line += " " + stringOf(item["status"])
		if errText := stringOf(item["error"]); errText != "" {
			line += "：" + errText
		}
		sb.WriteString(line)
	}
	if reasons, ok := data["riskReasons"].([]string); ok {
		for _, reason := range reasons {
			sb.WriteString("\n风险原因：" + reason)
		}
	}
	return sb.String()
}

// describeObjects 渲染对象标识（Kind/ns/name）。
func describeObjects(objs []manifestx.Object) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		s := o.Kind + "/" + o.Name
		if o.Namespace != "" {
			s = o.Kind + "/" + o.Namespace + "/" + o.Name
		}
		out = append(out, s)
	}
	return out
}

func manifestBlocks(objs []manifestx.Object) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.Manifest)
	}
	return out
}

// describeManifestBytes 限制审批卡片里清单的长度，避免长清单把提示冲垮。
const describeManifestBytes = 2048

func truncateText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…[TRUNCATED]"
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}

func scopeString(s manifestx.Scope) string {
	switch s {
	case manifestx.ScopeNamespaced:
		return approval.CreateScopeNamespaced
	case manifestx.ScopeCluster:
		return approval.CreateScopeCluster
	default:
		return approval.CreateScopeUnknown
	}
}

// resultWithCode 产出带错误码的工具结果（Data.error 存在即被 Agent 判定为失败）。
func resultWithCode(code, message string, extra map[string]any) ToolResult {
	data := map[string]any{"error": code + ": " + message, "code": code, "message": message}
	for k, v := range extra {
		data[k] = v
	}
	return ToolResult{Data: data, Text: "错误：" + code + ": " + message}
}

// rejected 把阻断级校验结论一次性摊开给模型（不逐个试错）。
func (t *createTool) rejected(objs []manifestx.Object, code, headline string) ToolResult {
	items := make([]map[string]any, 0, len(objs))
	var lines []string
	for i := range objs {
		o := &objs[i]
		if !o.HasErrors() {
			continue
		}
		errs := o.Errors()
		items = append(items, map[string]any{
			"index": o.Index, "kind": o.Kind, "name": o.Name,
			"namespace": o.Namespace, "findings": errs,
		})
		for _, f := range errs {
			target := f.Field
			if target == "" {
				target = fmt.Sprintf("objects[%d]", o.Index)
			}
			fix := ""
			if f.Fix != "" {
				fix = "；建议：" + f.Fix
			}
			lines = append(lines, fmt.Sprintf("- %s %s: %s%s", target, f.Code, f.Message, fix))
		}
	}
	data := map[string]any{
		"error":   code + ": " + headline,
		"code":    code,
		"objects": items,
	}
	text := "错误：" + code + ": " + headline + "\n" + strings.Join(lines, "\n")
	return ToolResult{Data: data, Text: text}
}

// createMapper 把 executor 的 discovery 结论适配成 manifestx.Mapper。
type createMapper struct{ runner CreateRunner }

func (m createMapper) MappingFor(gvk schema.GroupVersionKind) (manifestx.Mapping, error) {
	apiVersion := gvk.Version
	if gvk.Group != "" {
		apiVersion = gvk.Group + "/" + gvk.Version
	}
	mapping, err := m.runner.MappingFor(apiVersion, gvk.Kind)
	if err != nil {
		return manifestx.Mapping{}, err
	}
	scope := manifestx.ScopeCluster
	if mapping.Namespaced {
		scope = manifestx.ScopeNamespaced
	}
	return manifestx.Mapping{
		Resource: schema.GroupVersionResource{Group: gvk.Group, Version: gvk.Version, Resource: mapping.Resource},
		Scope:    scope,
	}, nil
}
