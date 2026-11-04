package deterministic

import (
	"fmt"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// cmd 构造一条展示用命令（ADR-014：报告里的命令永远只展示，不被本工具执行）。
func cmd(text string, cat model.CommandCategory, risk model.RiskLevel) model.Command {
	return model.Command{Category: cat, Text: text, Risk: risk}
}

// inv 是只读排查命令（一律 SAFE）。
func inv(text string) model.Command { return cmd(text, model.CmdInvestigation, model.RiskSafe) }

// rem 是修复建议命令（风险由调用方按动作真实影响标注）。
func rem(text string, risk model.RiskLevel) model.Command {
	return cmd(text, model.CmdRemediation, risk)
}

// ver 是修复后的验证命令。
func ver(text string) model.Command { return cmd(text, model.CmdVerification, model.RiskSafe) }

// nsArg 返回 "-n xxx " 或空串（集群级资源不带命名空间）。
func nsArg(ns string) string {
	if ns == "" {
		return ""
	}
	return "-n " + ns + " "
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func matchAny(s string, subs ...string) bool {
	low := strings.ToLower(s)
	for _, sub := range subs {
		if sub != "" && strings.Contains(low, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func ptrVal(p *int32) (int32, bool) {
	if p == nil {
		return 0, false
	}
	return *p, true
}

// replicasText 渲染 "就绪/期望" 副本描述。
func replicasText(desired, ready *int32) string {
	d, okD := ptrVal(desired)
	r, okR := ptrVal(ready)
	if !okD && !okR {
		return "副本状态未知"
	}
	if !okD {
		return fmt.Sprintf("就绪 %d（期望副本未声明）", r)
	}
	return fmt.Sprintf("就绪 %d / 期望 %d", r, d)
}

// evIDs 返回这些 Finding 的全部证据 ID（诊断结论的证据引用只能来自这里）。
func evIDs(fs ...*model.Finding) []string {
	var out []string
	for _, f := range fs {
		if f == nil {
			continue
		}
		for _, ev := range f.Evidence {
			if ev.ID != "" {
				out = append(out, model.GlobalEvidenceRef(f.ID, ev.ID))
			}
		}
	}
	return dedupe(out)
}

// evOfKind 只取指定来源类型的证据 ID（例如只引用事件类证据，避免拿日志证明事件）。
func evOfKind(f *model.Finding, kinds ...model.EvidenceKind) []string {
	if f == nil {
		return nil
	}
	var out []string
	for _, ev := range f.Evidence {
		for _, k := range kinds {
			if ev.Kind == k && ev.ID != "" {
				out = append(out, model.GlobalEvidenceRef(f.ID, ev.ID))
			}
		}
	}
	return dedupe(out)
}

// resLabel 是资源的可读标识（Node/StorageClass 等集群级资源不带命名空间）。
func resLabel(ref model.ResourceRef) string {
	if ref.Namespace == "" {
		return ref.Kind + "/" + ref.Name
	}
	return ref.Kind + "/" + ref.Namespace + "/" + ref.Name
}

// condOf 返回指定类型的 Condition。
func condOf(conds []model.ConditionInfo, typ string) (model.ConditionInfo, bool) {
	for _, c := range conds {
		if c.Type == typ {
			return c, true
		}
	}
	return model.ConditionInfo{}, false
}

// condTrue 判断某 Condition 是否为 True。
func condTrue(conds []model.ConditionInfo, typ string) (model.ConditionInfo, bool) {
	c, ok := condOf(conds, typ)
	return c, ok && strings.EqualFold(c.Status, "True")
}

// containersOf 返回 Pod 的业务容器与 initContainer（含指针，便于回写读值）。
func containersOf(p *model.PodInfo) []*model.ContainerInfo {
	if p == nil {
		return nil
	}
	var out []*model.ContainerInfo
	for i := range p.Containers {
		out = append(out, &p.Containers[i])
	}
	for i := range p.InitContainers {
		out = append(out, &p.InitContainers[i])
	}
	return out
}

// podReady 判断 Pod 是否所有容器就绪。
func podReady(p *model.PodInfo) bool {
	cs := containersOf(p)
	if len(cs) == 0 {
		return strings.EqualFold(p.Phase, "Running")
	}
	for _, c := range cs {
		if !c.Ready {
			return false
		}
	}
	return true
}

// hasLogs 判断该 Pod 是否采集到了容器日志。
func hasLogs(p *model.PodInfo) bool {
	if p == nil || len(p.Logs) == 0 {
		return false
	}
	for _, lg := range p.Logs {
		if len(lg.Current) > 0 || len(lg.Previous) > 0 {
			return true
		}
	}
	return false
}

// refList 渲染资源清单（超出 max 时给出总数，避免长报告撑爆版面）。
func refList(refs []model.ResourceRef, max int) string {
	if len(refs) == 0 {
		return "无"
	}
	if max <= 0 || len(refs) <= max {
		parts := make([]string, 0, len(refs))
		for _, r := range refs {
			parts = append(parts, resLabel(r))
		}
		return strings.Join(parts, "、")
	}
	parts := make([]string, 0, max)
	for _, r := range refs[:max] {
		parts = append(parts, resLabel(r))
	}
	return fmt.Sprintf("%s 等 %d 个", strings.Join(parts, "、"), len(refs))
}

func podRefs(pods []*model.PodInfo) []model.ResourceRef {
	out := make([]model.ResourceRef, 0, len(pods))
	for _, p := range pods {
		out = append(out, p.Ref)
	}
	return out
}

// impactChainForPod 沿真实关系拼影响链：Pod → owner 链 → 关联 Service → 引用它的 Ingress。
func impactChainForPod(c *Case, p *model.PodInfo) []string {
	if p == nil {
		return nil
	}
	chain := []string{resLabel(p.Ref) + "（" + orDash(p.Phase) + "）"}
	for _, w := range c.E.index().OwnerChain(p) {
		chain = append(chain, resLabel(w.Ref)+"（"+replicasText(w.DesiredReplicas, w.ReadyReplicas)+"）")
	}
	svcs := c.E.index().ServicesOfPod(p.Ref.Key())
	for _, s := range svcs {
		chain = append(chain, resLabel(s.Ref))
	}
	if snap := c.E.Snapshot; snap != nil {
		for _, ing := range snap.Ingresses {
			for _, be := range ing.Backends {
				for _, s := range svcs {
					if be == s.Ref.Name && ing.Ref.Namespace == s.Ref.Namespace {
						chain = append(chain, resLabel(ing.Ref))
					}
				}
			}
		}
	}
	return dedupe(chain)
}

// exitCodeHints 是常见退出码的确定性解释（不猜应用代码，只说这一类退出的含义）。
var exitCodeHints = map[int32]string{
	1:   "应用自身异常退出（未捕获异常、启动期配置校验失败等）",
	2:   "命令参数或配置用法错误",
	126: "入口命令不可执行（权限不足或文件不是可执行文件）",
	127: "入口命令不存在（镜像内命令名拼写或 PATH 问题）",
	137: "进程被 SIGKILL 终止（OOM Kill 或超时强杀）",
	139: "进程段错误崩溃",
	143: "进程收到 SIGTERM 后退出（多为被删除/驱逐/滚动更新）",
}

// pendingSchedulingReason 从 FailedScheduling 事件消息里抽取可行动原因。
func pendingSchedulingReason(msg string) (string, string) {
	switch {
	case matchAny(msg, "insufficient cpu", "insufficient memory", "insufficient ephemeral-storage", "insufficient pods"):
		return "节点可分配资源不足", "调度器在所有候选节点上都找不到足够资源，Pod 停留在 Pending"
	case matchAny(msg, "untolerated taint", "tolerations"):
		return "节点污点未被容忍", "候选节点带有该 Pod 未容忍的污点，调度器不敢放上去"
	case matchAny(msg, "were unschedulable", "node(s) were not ready", "not ready"):
		return "节点不可调度", "候选节点处于 NotReady 或被 cordon 标记为不可调度"
	case matchAny(msg, "no persistent volumes available", "persistentvolumeclaim", "unbound"):
		return "存储卷未就绪", "Pod 等待的 PersistentVolumeClaim 尚未绑定可用 PV"
	case matchAny(msg, "node(s) didn't match", "node affinity", "node selector", "pod affinity", "pod anti-affinity"):
		return "亲和性/选择器约束不满足", "节点的标签或已有 Pod 不满足该 Pod 的亲和性与反亲和约束"
	default:
		return "", ""
	}
}
