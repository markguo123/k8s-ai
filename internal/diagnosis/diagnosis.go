// Package diagnosis 编排 LLM 诊断：预算裁剪 → DiagnosisContext → LLM →
// JSON 解析/校验 → 降级（FR-016，ADR-005）。
package diagnosis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/evidence"
	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/security"
)

var errRedactor = security.NewRedactor()

// llmConcurrency LLM 诊断并发上限（CONCURRENCY.md，默认 2）。
const llmConcurrency = 2

// Diagnoser 是诊断编排接口。
type Diagnoser interface {
	// Diagnose 对 Findings 送诊；LLM 不可用/超时降级为规则结论，scan 不失败。
	Diagnose(ctx context.Context, findings []model.Finding, client llm.LLMClient) ([]model.Diagnosis, model.LLMSummary, error)
	// DiagnoseIncidents 按 Incident（故障链）送诊：同一故障链只调用一次 LLM，
	// 派生症状仅作为影响范围上下文，不单独分析（system.md §5/§25）。
	DiagnoseIncidents(ctx context.Context, incidents []model.Incident, findingByID map[string]model.Finding, client llm.LLMClient) ([]model.Diagnosis, model.LLMSummary, error)
}

type diagnoser struct {
	opts   model.LLMOptions
	system string
}

// New 创建诊断编排器。
func New(opts model.LLMOptions) Diagnoser {
	return &diagnoser{opts: opts, system: llm.SystemPrompt()}
}

// summaryCounters 线程安全的 LLM 调用计数。
type summaryCounters struct {
	mu     sync.Mutex
	calls  int
	failed int
}

func (s *summaryCounters) call() {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
}

func (s *summaryCounters) fail() {
	s.mu.Lock()
	s.failed++
	s.mu.Unlock()
}

// Diagnose 按预算送诊（并发 2），结果保持入参顺序；超时把剩余问题降级而不是整次失败。
func (d *diagnoser) Diagnose(ctx context.Context, findings []model.Finding, client llm.LLMClient) ([]model.Diagnosis, model.LLMSummary, error) {
	summary := model.LLMSummary{Enabled: true}
	contexts := buildContexts(findings, d.opts)
	for _, c := range contexts {
		summary.TokensEstimated += c.tokens
	}
	counters := &summaryCounters{}

	out := make([]model.Diagnosis, len(contexts))
	var mu sync.Mutex // 保护 out 与 summary.Degraded
	sem := make(chan struct{}, llmConcurrency)
	var wg sync.WaitGroup
	for i, c := range contexts {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			slog.Info("llm: 诊断进行中", "finding", c.finding.ID, "index", i+1, "total", len(contexts))
			diag, err := d.diagnoseOne(ctx, c, client, counters)
			if err != nil {
				if ctx.Err() != nil {
					diag = degraded(c.finding, "LLM 诊断超时/预算不足")
				} else {
					return // 非预期错误：保持空槽，主流程统一降级
				}
			}
			mu.Lock()
			out[i] = diag
			if !diag.LLMUsed {
				summary.Degraded++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	// 超时处理：剩余空槽降级，scan 不失败；用户主动取消则返回错误。
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return out, summary, err
		}
		for i := range out {
			if out[i].FindingID == "" {
				out[i] = degraded(contexts[i].finding, "LLM 诊断超时/预算不足")
				summary.Degraded++
			}
		}
	}
	summary.Calls = counters.calls
	summary.Failed = counters.failed
	return out, summary, nil
}

// diagnoseOne 对单个 Finding 送诊；解析/校验失败带修复指令重试一次，
// 仍失败则降级为规则结论（FR-016）。
func (d *diagnoser) diagnoseOne(ctx context.Context, c findingContext, client llm.LLMClient, counters *summaryCounters) (model.Diagnosis, error) {
	msgs := d.diagnosisMessages(llm.BuildMessages(d.system, c.text))
	diag, err := d.tryDiagnose(ctx, client, msgs, c.finding, counters)
	if err == nil {
		return diag, nil // 成功或已降级
	}
	// 解析/校验失败：修复重试一次。
	repair := append(msgs, llm.Message{Role: "user", Content: "上次输出不符合要求的 JSON 格式。请只输出符合 schema 的 JSON，不要包含任何解释。"})
	counters.call()
	resp, chatErr := client.Chat(ctx, llm.ChatRequest{Messages: repair, MaxTokens: d.diagnosisMaxTokens(), Thinking: d.opts.Thinking})
	if chatErr != nil {
		counters.fail()
		if ctx.Err() != nil {
			return model.Diagnosis{}, ctx.Err()
		}
		return degraded(c.finding, "LLM 调用失败（重试）"), nil
	}
	diag2, err2 := d.parseAndBuild(resp.Content, c.finding)
	if err2 != nil {
		return degraded(c.finding, "LLM 输出解析失败"), nil
	}
	return diag2, nil
}

// tryDiagnose 调用一次 LLM；网络/服务端失败直接降级，解析失败返回错误由上层重试。
func (d *diagnoser) tryDiagnose(ctx context.Context, client llm.LLMClient, msgs []llm.Message, f model.Finding, counters *summaryCounters) (model.Diagnosis, error) {
	counters.call()
	resp, err := client.Chat(ctx, llm.ChatRequest{Messages: msgs, MaxTokens: d.diagnosisMaxTokens(), Thinking: d.opts.Thinking})
	if err != nil {
		counters.fail()
		if ctx.Err() != nil {
			return model.Diagnosis{}, ctx.Err()
		}
		return degraded(f, "LLM 调用失败："+errRedactor.Redact(err.Error())), nil
	}
	return d.parseAndBuild(resp.Content, f)
}

// parseAndBuild 解析 LLM 输出并构建 Diagnosis；schema 错误返回 error 触发修复重试。
func (d *diagnoser) parseAndBuild(content string, f model.Finding) (model.Diagnosis, error) {
	out, err := parseLLMOutput(content)
	if err != nil {
		// 记录脱敏后的输出预览，便于排查模型输出格式问题。
		preview := content
		if len(preview) > 300 {
			preview = preview[:300]
		}
		slog.Warn("llm: 输出解析失败", "finding", f.ID, "error", err.Error(), "preview", errRedactor.Redact(preview))
		return model.Diagnosis{}, err
	}
	diag := buildDiagnosis(f, out)
	if strings.TrimSpace(diag.RootCause) == "" || strings.TrimSpace(diag.Summary) == "" {
		return degraded(f, "LLM 输出校验失败"), nil
	}
	ensureRemediation(&diag, f)
	return diag, nil
}

// degraded 构造降级 Diagnosis：LLM 不可用时仍基于已采证据生成"规则判定初步判断"，
// 并自动提取日志关键行（panic/fatal/error），避免"分析不可用"浪费已有证据。
func degraded(f model.Finding, reason string) model.Diagnosis {
	d := model.Diagnosis{
		FindingID:            f.ID,
		Summary:              f.Title,
		LLMUsed:              false,
		Error:                reason,
		RootCause:            ruleBasedRootCause(f),
		RemediationDirection: remediationDirection(f.Rule),
	}
	if ref := f.Resource; ref.Kind == "Pod" {
		d.Investigation = []model.Command{
			{Category: model.CmdInvestigation, Text: fmt.Sprintf("kubectl -n %s logs %s --tail=200 --previous", ref.Namespace, ref.Name), Risk: model.RiskSafe},
			{Category: model.CmdInvestigation, Text: fmt.Sprintf("kubectl -n %s describe pod %s", ref.Namespace, ref.Name), Risk: model.RiskSafe},
		}
	}
	// 降级路径也至少保证"一条可执行修复 + 文字说明"（与 LLM 路径的 ensureRemediation
	// 一致），避免 degraded diagnosis 只能排查、无法给出下一步动作。
	ensureRemediation(&d, f)
	return d
}

// ruleBasedRootCause 基于规则标题、关键证据与日志关键行生成初步判断。
func ruleBasedRootCause(f model.Finding) string {
	var facts []string
	keyLine := ""
	for _, e := range f.Evidence {
		if e.Kind == model.EvLog {
			if keyLine == "" {
				keyLine = findKeyLogLine(e.Value)
			}
			continue
		}
		if len(facts) < 3 {
			facts = append(facts, e.Key+"="+e.Value)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "规则判定：%s", f.Title)
	if len(facts) > 0 {
		fmt.Fprintf(&b, "（%s）", strings.Join(facts, "；"))
	}
	if keyLine != "" {
		fmt.Fprintf(&b, "。日志关键行：%s", keyLine)
	}
	return b.String()
}

// findKeyLogLine 委托给 evidence.KeyLogLine（panic/fatal/error 关键行提取）。
func findKeyLogLine(log string) string {
	return evidence.KeyLogLine(log)
}

// diagnosisMaxTokens 限制诊断输出长度（JSON schema 字段有界，2048 足够；
// 限制生成长度可显著降低大模型的单次响应时间）。
func (d *diagnoser) diagnosisMaxTokens() int {
	// 思考型模型需要给"思考 + 最终答案"留足空间；使用配置值（默认 4096）。
	if d.opts.MaxTokens > 0 {
		return d.opts.MaxTokens
	}
	return 4096
}

// outputSchemaHint 显式告知模型必须输出的 JSON 结构（多数网关不支持
// response_format，只能靠 prompt 约束；此前模型自由发挥导致解析失败）。
const outputSchemaHint = `请只输出一个 JSON 对象，不要输出任何其他内容。保持简洁：summary/rootCause 各 1-2 句，可能原因最多 2 条，命令每条一行不要解释。
本次输入可能包含"根因 Finding + 关联派生问题"：诊断必须围绕根因（Incident）展开；派生问题只用于影响评估，不单独给修复命令。
禁止输出无明确修改目标的 kubectl edit；修复命令必须直接针对根因。如果当前证据不足以确定精确修改内容：
- remediationText 必须用 1-2 句说明修复内容：做什么、为什么（对应根因）、预期结果；文字是主体，命令只是辅助，禁止为空；
- remediation 至少一条可执行命令（含 -n/资源名/完整参数）；无法确定精确修改时放只读确认命令；remediationDirection 作为命令注释/方向；
- investigation 给只读确认命令。
字段如下：
{
  "summary": "Incident 问题摘要",
  "rootCause": "确认根因（有明显错误证据时直接给出；只有证据确实不足才写：当前证据不足，无法确认根因）",
  "confidence": 0.0到1.0之间的数字,
  "confidenceLevel": "CONFIRMED|HIGH_CONFIDENCE|POSSIBLE|UNKNOWN",
  "causalChain": "因果链解释（根因→直接症状→派生影响）",
  "evidenceChain": ["E1", "E2"],
  "impact": "影响范围（区分直接影响与派生影响）",
  "possibleCauses": ["可能原因1", "可能原因2"],
  "investigation": ["kubectl 只读确认命令"],
  "remediation": ["可执行 kubectl 命令序列（每条含 -n/资源名/完整参数；必须直接针对根因；无法确定精确修改时放只读确认命令如 kubectl get pvc ...；禁止空数组；命令只是文字说明的配套）"],
  "remediationText": "修复文字说明（1-2 句：做什么、为什么、预期结果；禁止为空；用户先读这段再执行命令）",
  "remediationDirection": "修复方向与前置条件（remediation 为空时必须给出，如：确认 PVC 是否应存在、检查 Deployment claimName 引用，人工确认后重建或修正；不要编造具体 YAML）",
  "verification": ["kubectl 验证命令"],
  "risk": "SAFE|LOW|MEDIUM|HIGH|CRITICAL",
  "uncertainty": "不确定性说明（没有则留空字符串）"
}`

// remediationDirection 按根因规则给出确定性修复方向（工具侧兜底，保证修复指导永不缺失）。
func remediationDirection(rule string) string {
	switch rule {
	case "CrashLoopBackOff", "OOMKilled":
		return "优先排查应用日志与资源限制：确认内存/CPU limit 是否合理、最近是否有变更；OOM 场景在人工确认后可 `kubectl set resources` 调整，同时定位应用侧内存增长根因。"
	case "ImagePullBackOff":
		return "检查镜像名/tag 是否存在、仓库是否可达、认证是否有效（imagePullSecrets）；修正镜像引用或补充凭据后人工确认重试。"
	case "PendingPod":
		return "根据调度事件定位：资源不足→调整 request 或节点；PVC 缺失→确认 claimName 引用或重建 PVC；节点不可调度→处理 taint/节点状态。需人工确认具体修改。"
	case "ContainerCreateError":
		return "检查 Pod 引用的 ConfigMap/Secret 是否存在、名称与字段是否拼写正确；补齐或修正引用后人工确认重试。"
	case "FailedMount":
		return "检查 PVC/PV/StorageClass 状态、挂载权限与 claimName 引用；确认卷是否应存在及配置是否匹配。"
	case "PVCPending":
		return "检查 StorageClass 是否存在、容量与节点可用性；确认 PVC 是否应存在、引用是否正确；人工确认后再创建或修正。"
	case "NodeNotReady", "NodeDiskPressure", "NodeMemoryPressure":
		return "检查节点 kubelet/容器运行时状态与磁盘/内存使用；处理节点问题后确认其上 Pod 恢复。"
	case "ServiceNoEndpoint":
		return "检查 Service selector 与 Pod 标签是否匹配、Pod 是否 Ready；修正 selector 或恢复后端 Pod。"
	case "DeploymentReplica", "StatefulSetReplica":
		return "检查其 Pod 状态（Pending/CrashLoop/ImagePull 等）定位下层根因；先修复 Pod 层问题，副本数随之恢复。"
	case "JobFailed":
		return "查看 Job Pod 日志与事件，定位失败原因（代码/参数/依赖）；修正后重新提交 Job。"
	case "Unhealthy":
		return "检查探针配置（liveness/readiness 的路径、端口、超时）与应用就绪路径；修正探针或应用后人工确认。"
	case "IngressBackend":
		return "检查 Ingress backend 引用的 Service 是否存在及名称拼写；修正引用后人工确认。"
	default:
		return "结合上方排查命令人工确认根因后制定修复方案；如证据不足，先补齐证据再执行修改。"
	}
}

// diagnosisMessages 在消息末尾附加输出 Schema 指令。
func (d *diagnoser) diagnosisMessages(msgs []llm.Message) []llm.Message {
	return append(msgs, llm.Message{Role: "user", Content: outputSchemaHint})
}

// incidentContext 是 Incident 的诊断上下文（根因 + 派生影响摘要）。
type incidentContext struct {
	inc    model.Incident
	root   model.Finding
	text   string
	tokens int
}

// DiagnoseIncidents 按 Incident 送诊（并发 2），结果按入参顺序；超时降级不失败。
func (d *diagnoser) DiagnoseIncidents(ctx context.Context, incidents []model.Incident, findingByID map[string]model.Finding, client llm.LLMClient) ([]model.Diagnosis, model.LLMSummary, error) {
	summary := model.LLMSummary{Enabled: true}
	ctxs := buildIncidentContexts(incidents, findingByID, d.opts)
	for _, c := range ctxs {
		summary.TokensEstimated += c.tokens
	}
	counters := &summaryCounters{}
	out := make([]model.Diagnosis, len(ctxs))
	var mu sync.Mutex
	sem := make(chan struct{}, llmConcurrency)
	var wg sync.WaitGroup

	// 长任务抚慰：为每个 Incident 启动一个独立 ticker，
	// 超过 longTaskThreshold 未完成时周期性触发 LLMProgress.LongTask
	// （经验 1073789：避免用户感觉"卡死"）。
	const longTaskThreshold = 10 * time.Second
	const longTaskTick = 10 * time.Second
	activeStart := make([]time.Time, len(ctxs))
	activeMu := &sync.Mutex{}
	startTimer := func(i int) {
		if d.opts.Progress == nil {
			return
		}
		start := time.Now()
		activeMu.Lock()
		activeStart[i] = start
		activeMu.Unlock()
		incID := ctxs[i].inc.ID
		total := len(ctxs)
		idx := i + 1
		go func() {
			t := time.NewTicker(longTaskTick)
			defer t.Stop()
			first := true
			for range t.C {
				activeMu.Lock()
				cur := activeStart[i]
				activeMu.Unlock()
				if cur.IsZero() || !cur.Equal(start) {
					return // 已完成或被覆盖
				}
				elapsed := time.Since(start)
				if elapsed < longTaskThreshold {
					if !first {
						return
					}
					first = false
					continue
				}
				d.opts.Progress.LongTask(incID, idx, total, elapsed)
			}
		}()
	}
	stopTimer := func(i int) {
		activeMu.Lock()
		activeStart[i] = time.Time{}
		activeMu.Unlock()
	}

	for i, c := range ctxs {
		i, c := i, c
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			slog.Info("llm: 诊断进行中", "incident", c.inc.ID, "index", i+1, "total", len(ctxs))
			if d.opts.Progress != nil {
				d.opts.Progress.IncidentStarted(c.inc.ID, i+1, len(ctxs))
			}
			startTimer(i)
			diag, err := d.diagnoseOneIncident(ctx, c, client, counters)
			stopTimer(i)
			if err != nil {
				if ctx.Err() != nil {
					diag = degraded(c.root, "LLM 诊断超时/预算不足")
				} else {
					if d.opts.Progress != nil {
						d.opts.Progress.IncidentDone(c.inc.ID, i+1, len(ctxs), diag.LLMUsed, err)
					}
					return
				}
			}
			if d.opts.Progress != nil {
				d.opts.Progress.IncidentDone(c.inc.ID, i+1, len(ctxs), diag.LLMUsed, err)
			}
			mu.Lock()
			out[i] = diag
			if !diag.LLMUsed {
				summary.Degraded++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return out, summary, err
		}
		for i := range out {
			if out[i].FindingID == "" {
				out[i] = degraded(ctxs[i].root, "LLM 诊断超时/预算不足")
				summary.Degraded++
			}
		}
	}
	summary.Calls = counters.calls
	summary.Failed = counters.failed
	return out, summary, nil
}

// diagnoseOneIncident 复用 diagnoseOne：把 Incident 的根因 Finding 作为诊断主体。
func (d *diagnoser) diagnoseOneIncident(ctx context.Context, c incidentContext, client llm.LLMClient, counters *summaryCounters) (model.Diagnosis, error) {
	fc := findingContext{finding: c.root, text: c.text, tokens: c.tokens}
	return d.diagnoseOne(ctx, fc, client, counters)
}

// buildIncidentContexts 构造 Incident 上下文并做预算裁剪。
func buildIncidentContexts(incidents []model.Incident, findingByID map[string]model.Finding, opts model.LLMOptions) []incidentContext {
	per := opts.MaxInputTokens
	if per <= 0 {
		per = 8192
	}
	var out []incidentContext
	for _, inc := range incidents {
		root, ok := findingByID[inc.Root.ID]
		if !ok {
			continue
		}
		text := buildIncidentText(inc, root, findingByID)
		est := estimateTokens(text)
		if est > per && len(inc.Members) > 0 {
			text = dropMemberSection(text)
			est = estimateTokens(text)
		}
		for est > per && strings.Count(text, "\n- E") > 1 {
			text = dropLastEvidence(text)
			est = estimateTokens(text)
		}
		out = append(out, incidentContext{inc: inc, root: root, text: text, tokens: est})
	}
	return out
}

// buildIncidentText 根因完整上下文 + 派生问题摘要（用于影响评估，不单独诊断）。
func buildIncidentText(inc model.Incident, root model.Finding, findingByID map[string]model.Finding) string {
	text := buildContextText(root)
	if len(inc.Members) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString("\n关联问题（派生症状，不单独分析，请合并到 impact 的派生影响部分）：\n")
	for _, m := range inc.Members {
		fmt.Fprintf(&b, "- %s %s/%s：%s", m.Severity, m.Resource.Namespace, m.Resource.Name, m.Title)
		if mf, ok := findingByID[m.ID]; ok {
			if facts := topFacts(mf.Evidence, 2); len(facts) > 0 {
				fmt.Fprintf(&b, "（%s）", strings.Join(facts, "；"))
			}
		}
		b.WriteString("\n")
	}
	return text + b.String()
}

// topFacts 取前 n 条非日志证据（派生摘要用）。
func topFacts(evs []model.Evidence, n int) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == model.EvLog {
			continue
		}
		if len(out) >= n {
			break
		}
		out = append(out, e.Key+"="+e.Value)
	}
	return out
}

// dropMemberSection 移除"关联问题（派生症状..."段（预算裁剪）。
func dropMemberSection(text string) string {
	i := strings.Index(text, "\n关联问题（派生症状")
	if i < 0 {
		return text
	}
	return text[:i]
}

// ensureRemediation 保证修复方案"文字 + 命令"永不缺失（系统提示词 §三十五）：
// remediationText 非空（LLM 漏写时用修复方向兜底），remediation 至少一条
// 真正的写入命令（create/patch/apply/delete/set/scale/rollout 等）；若 LLM 只给了
// 只读命令（get/describe 作确认），用规则级兜底写入命令补上，始终满足 §三十五
// "至少一条写入操作命令"的硬性要求（即使 validateCommand 过滤掉 edit 等交互写法，
// 也会在这里补上，避免 remediation 退化为纯只读）。
func ensureRemediation(d *model.Diagnosis, f model.Finding) {
	if d.RemediationDirection == "" {
		d.RemediationDirection = remediationDirection(f.Rule)
	}
	if strings.TrimSpace(d.RemediationText) == "" {
		d.RemediationText = d.RemediationDirection
	}
	// 判断 remediation 是否已经包含写入动词（写命令）。
	writePresent := false
	for _, c := range d.Remediation {
		if commandHasWriteVerb(c.Text) {
			writePresent = true
			break
		}
	}
	if !writePresent {
		if extra := fallbackWriteCommands(f); len(extra) > 0 {
			// 兜底写入命令放在只读确认命令之后，保持"先确认 → 再执行"的习惯顺序。
			d.Remediation = append(d.Remediation, extra...)
		} else if len(d.Remediation) == 0 && len(d.Investigation) > 0 {
			// 规则级也拿不到写入兜底的情况，至少给一条 investigation 作占位，
			// 避免 remediation 空列表。
			first := d.Investigation[0]
			first.Category = model.CmdRemediation
			first.Risk = model.RiskSafe
			d.Remediation = append(d.Remediation, first)
		}
	}
}

// commandHasWriteVerb 判断 kubectl 命令是否属于写入类动词（用于 ensureRemediation
// 兜底判定）。与 commands.go writeVerbs 保持语义一致，但避免跨包共享内部 map。
func commandHasWriteVerb(text string) bool {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) < 2 || fields[0] != "kubectl" {
		return false
	}
	verb, _, _ := extractVerb(fields[1:])
	switch verb {
	case "create", "patch", "apply", "delete", "set", "scale", "rollout",
		"annotate", "label", "replace", "expose", "autoscale", "taint", "drain",
		"uncordon", "cordon", "cp": // cp 也算写（向容器复制文件），常见修复手段
		return true
	}
	return false
}

// fallbackWriteCommands 给不同 Finding 类型的规则级写入命令兜底。
// 这些命令"不读 Secret 数据、不执行任何 API 写操作"，只作为输出展示（ADR-014）。
// 对依赖应用侧具体值的字段用 `<待确认>` 明确占位，提示人工确认后执行。
func fallbackWriteCommands(f model.Finding) []model.Command {
	ns := f.Resource.Namespace
	name := f.Resource.Name
	switch f.Rule {
	case "CrashLoopBackOff":
		// CrashLoopBackOff 根因多样（配置错误/资源不足/依赖缺失/代码 bug），
		// 一期无法精确定位具体根因，只给 rollout restart 作为"修复后重启生效"的
		// 通用步骤；不硬塞 set resources 等具体写法，避免误导（如配置错误时
		// set resources 完全不相关）。具体修复值留待二期 chat 交互确认。
		if owner, wlKind, wlName := detectWorkloadOwner(f); owner {
			return []model.Command{{
				Category: model.CmdRemediation,
				Risk:     model.RiskLow,
				Text:     fmt.Sprintf("kubectl -n %s rollout restart %s %s", ns, wlKind, wlName),
			}}
		}
		// 无 Workload owner（裸 Pod 或 Job）：直接用 delete 重建（一般由上层控制器再拉起）。
		return []model.Command{{
			Category: model.CmdRemediation,
			Risk:     model.RiskLow,
			Text:     fmt.Sprintf("kubectl -n %s delete pod %s", ns, name),
		}}
	case "ImagePullBackOff":
		if owner, wlKind, wlName := detectWorkloadOwner(f); owner {
			return []model.Command{{
				Category: model.CmdRemediation,
				Risk:     model.RiskMedium,
				Text:     fmt.Sprintf("kubectl -n %s set image %s %s <容器名>=<正确镜像:tag>", ns, wlKind, wlName),
			}}
		}
	case "ContainerCreateError":
		// 多为 ConfigMap/Secret 缺失：给出创建占位 + Pod 重建。
		out := []model.Command{
			{
				Category: model.CmdRemediation,
				Risk:     model.RiskMedium,
				Text:     fmt.Sprintf("kubectl -n %s create configmap <缺失的ConfigMap名> --from-literal=<key>=<待确认的值> -o yaml --dry-run=client | kubectl apply -f -", ns),
			},
		}
		if owner, wlKind, wlName := detectWorkloadOwner(f); owner {
			out = append(out, model.Command{
				Category: model.CmdRemediation,
				Risk:     model.RiskLow,
				Text:     fmt.Sprintf("kubectl -n %s rollout restart %s %s", ns, wlKind, wlName),
			})
		}
		return out
	case "FailedMount":
		out := []model.Command{
			{
				Category: model.CmdRemediation,
				Risk:     model.RiskMedium,
				Text:     fmt.Sprintf("kubectl -n %s create -f - <<'EOF'\napiVersion: v1\nkind: PersistentVolumeClaim\nmetadata:\n  name: <待确认的PVC名>\n  namespace: %s\nspec:\n  accessModes: [\"ReadWriteOnce\"]\n  resources:\n    requests:\n      storage: <待补容量，如 1Gi>\n  storageClassName: <待补StorageClass名>\nEOF", ns, ns),
			},
		}
		if owner, wlKind, wlName := detectWorkloadOwner(f); owner {
			out = append(out, model.Command{
				Category: model.CmdRemediation,
				Risk:     model.RiskMedium,
				Text:     fmt.Sprintf("kubectl -n %s patch %s %s --type=merge -p '{\"spec\":{\"volumes\":[{\"name\":\"<卷名>\",\"persistentVolumeClaim\":{\"claimName\":\"<正确的PVC名>\"}}]}}'", ns, wlKind, wlName),
			})
		}
		return out
	case "Unhealthy":
		if owner, wlKind, wlName := detectWorkloadOwner(f); owner {
			return []model.Command{{
				Category: model.CmdRemediation,
				Risk:     model.RiskMedium,
				Text:     fmt.Sprintf("kubectl -n %s patch %s %s --type=merge -p '{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"<容器名>\",\"livenessProbe\":{\"httpGet\":{\"path\":\"<待确认路径>\",\"port\":<待确认端口>},\"initialDelaySeconds\":5,\"periodSeconds\":10}}]}}}}'", ns, wlKind, wlName),
			}}
		}
	case "ServiceNoEndpoint":
		return []model.Command{{
			Category: model.CmdRemediation,
			Risk:     model.RiskLow,
			Text:     fmt.Sprintf("kubectl -n %s patch service %s --type=merge -p '{\"spec\":{\"selector\":{\"<正确标签>\":\"<值>\"}}}'", ns, name),
		}}
	case "DeploymentReplica", "StatefulSetReplica":
		wlKind := strings.ToLower(strings.TrimSuffix(f.Rule, "Replica"))
		if wlKind == "deployment" || wlKind == "statefulset" {
			return []model.Command{{
				Category: model.CmdRemediation,
				Risk:     model.RiskMedium,
				Text:     fmt.Sprintf("kubectl -n %s scale %s %s --replicas=<预期副本数>", ns, wlKind, name),
			}}
		}
	case "IngressBackend":
		return []model.Command{{
			Category: model.CmdRemediation,
			Risk:     model.RiskLow,
			Text:     fmt.Sprintf("kubectl -n %s patch ingress %s --type=json -p='[{\"op\":\"replace\",\"path\":\"/spec/rules/0/http/paths/0/backend/service/name\",\"value\":\"<正确的Service名>\"}]'", ns, name),
		}}
	case "PendingPod":
		if owner, wlKind, wlName := detectWorkloadOwner(f); owner {
			return []model.Command{{
				Category: model.CmdRemediation,
				Risk:     model.RiskMedium,
				Text:     fmt.Sprintf("kubectl -n %s set resources %s %s --containers=<容器名> --requests=cpu=<待确认>,memory=<待确认> --limits=cpu=<待确认>,memory=<待确认>", ns, wlKind, wlName),
			}}
		}
	}
	// 默认：能识别到 Workload owner 就给 rollout restart（修改配置后最通用的重启生效手段）。
	if owner, wlKind, wlName := detectWorkloadOwner(f); owner {
		return []model.Command{{
			Category: model.CmdRemediation,
			Risk:     model.RiskLow,
			Text:     fmt.Sprintf("kubectl -n %s rollout restart %s %s", ns, wlKind, wlName),
		}}
	}
	return nil
}

// detectWorkloadOwner 在 Finding 资源为 Pod 时，按 Pod 命名规律识别其 Workload
// 拥有者（Deployment/StatefulSet），便于生成"改 Workload 而非 Pod"的修复命令；
// 识别不了时返回 (false, "", "")，调用方退化为"重建 Pod"或仅给排查命令。
// 注意：Finding 目前不携带集群侧 owner 元信息（correlation index 有，但未回写到
// Finding），因此这里只用命名启发式；两段式 Pod 名（DaemonSet/ReplicaSet/Job）
// 无法仅凭名称可靠区分，不猜测，避免生成错误的 rollout/scale 目标。
func detectWorkloadOwner(f model.Finding) (has bool, kind, name string) {
	// 先按资源 Kind 直接返回 Workload 级。
	switch f.Resource.Kind {
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob":
		return true, strings.ToLower(f.Resource.Kind), f.Resource.Name
	}
	if f.Resource.Kind != "Pod" {
		return false, "", ""
	}
	parts := strings.Split(f.Resource.Name, "-")
	n := len(parts)
	// StatefulSet Pod：<sts>-<序号>（如 web-0），序号为纯数字。
	if n >= 2 && allDigits(parts[n-1]) {
		return true, "statefulset", strings.Join(parts[:n-1], "-")
	}
	// Deployment Pod：<deploy>-<replicaset hash>-<pod hash>（三段及以上），
	// 去掉最后两段哈希即可逼近 Deployment 名；若与实际不符，命令中名称可人工修正。
	if n >= 3 {
		return true, "deployment", strings.Join(parts[:n-2], "-")
	}
	return false, "", ""
}

// allDigits 判断字符串是否全为数字（StatefulSet Pod 序号特征）。
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
