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
	resp, chatErr := client.Chat(ctx, llm.ChatRequest{Messages: repair, MaxTokens: d.diagnosisMaxTokens(), DisableThinking: d.opts.DisableThinking})
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
	resp, err := client.Chat(ctx, llm.ChatRequest{Messages: msgs, MaxTokens: d.diagnosisMaxTokens(), DisableThinking: d.opts.DisableThinking})
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
	return diag, nil
}

// degraded 构造降级 Diagnosis：LLM 不可用时仍基于已采证据生成"规则判定初步判断"，
// 并自动提取日志关键行（panic/fatal/error），避免"分析不可用"浪费已有证据。
func degraded(f model.Finding, reason string) model.Diagnosis {
	d := model.Diagnosis{
		FindingID: f.ID,
		Summary:   f.Title,
		LLMUsed:   false,
		Error:     reason,
		RootCause: ruleBasedRootCause(f),
	}
	if ref := f.Resource; ref.Kind == "Pod" {
		d.Investigation = []model.Command{
			{Category: model.CmdInvestigation, Text: fmt.Sprintf("kubectl -n %s logs %s --tail=200 --previous", ref.Namespace, ref.Name), Risk: model.RiskSafe},
			{Category: model.CmdInvestigation, Text: fmt.Sprintf("kubectl -n %s describe pod %s", ref.Namespace, ref.Name), Risk: model.RiskSafe},
		}
	}
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
const outputSchemaHint = `请只输出一个 JSON 对象，不要输出任何其他内容。保持简洁：summary/rootCause 各 1-2 句，可能原因最多 2 条，命令每条一行不要解释。字段如下：
{
  "summary": "问题摘要",
  "rootCause": "确认根因（有明显错误证据时直接给出；只有证据确实不足才写：当前证据不足，无法确认根因）",
  "confidence": 0.0到1.0之间的数字,
  "evidenceChain": ["E1", "E2"],
  "impact": "影响范围",
  "possibleCauses": ["可能原因1", "可能原因2"],
  "investigation": ["kubectl 排查命令"],
  "remediation": ["kubectl 修复命令"],
  "verification": ["kubectl 验证命令"],
  "risk": "SAFE|LOW|MEDIUM|HIGH|CRITICAL"
}`

// diagnosisMessages 在消息末尾附加输出 Schema 指令。
func (d *diagnoser) diagnosisMessages(msgs []llm.Message) []llm.Message {
	return append(msgs, llm.Message{Role: "user", Content: outputSchemaHint})
}
