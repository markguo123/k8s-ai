package deterministic_test

import (
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/deterministic"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// scenario 是需求 §10 要求的一个故障场景回归单元。
//
// 每个场景固定跑六条判据：正常情况 / 单一故障 / 多个关联故障 / 证据缺失 /
// 采集失败 / 容易误判的边界条件。夹具全部是内存快照，不依赖任何真实集群。
type scenario struct {
	name  string                        // 场景名（对应需求 §10 清单）
	id    string                        // 期望命中的诊断器 ID
	rule  string                        // 承载该场景的规则名
	fault func() *model.ClusterSnapshot // 单一故障

	// normal 是正常对照：必须不命中 rule，否则说明规则在好集群上误报。
	normal func() *model.ClusterSnapshot
	// boundary 是容易误判的边界条件；boundaryHits 说明它该不该命中。
	boundary     func() *model.ClusterSnapshot
	boundaryHits bool
	// boundaryNoDiagnoser 用于「规则本身成立、但结论不该升级」的边界：
	// 例如 Pod 确实在 CrashLoop（规则命中），但事件里没有任何解析失败信号，
	// 此时 DNS 诊断器不能凭猜测出结论。
	boundaryNoDiagnoser bool
	drop                func(*model.ClusterSnapshot)  // 采集失败模拟：把本场景的主体对象采丢
	multi               func() *model.ClusterSnapshot // 多个关联故障（可空）
	multiRootRule       string                        // 关联故障里期望的根因规则
	wantStatus          string                        // 单一故障期望的根因状态（可空）
}

// scenarios 由各场景夹具文件在 init 里追加（与诊断器按域分文件保持一致）。
var scenarios []scenario

func TestScenarios(t *testing.T) {
	if len(scenarios) == 0 {
		t.Fatal("没有加载到任何故障场景夹具")
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Run("正常情况", func(t *testing.T) {
				if sc.normal == nil {
					t.Fatalf("场景 %s 缺少正常对照夹具", sc.name)
				}
				runChain(t, sc.normal()).requireNoFinding(t, sc.rule)
			})

			t.Run("单一故障", func(t *testing.T) {
				c := runChain(t, sc.fault())
				inc := c.incidentOf(t, sc.rule)
				d := c.diagOf(t, inc.ID, sc.id)
				if d.RootCause == "" {
					t.Fatal("结论没有根因")
				}
				if d.LLMUsed || d.Source != "deterministic" {
					t.Fatalf("确定性结论被标记为 LLM 产物: %+v", d)
				}
				if len(d.EvidenceChain) == 0 {
					t.Errorf("快照里有证据可用，结论却没挂证据")
				}
				if d.RemediationDirection == "" {
					t.Errorf("缺少修复方向")
				}
				switch d.RootCauseStatus {
				case model.RootCauseConfirmed, model.RootCauseProbable, model.RootCauseUnknown:
				default:
					t.Fatalf("根因状态非法: %q", d.RootCauseStatus)
				}
				if sc.wantStatus != "" && d.RootCauseStatus != sc.wantStatus {
					t.Errorf("根因状态 = %s, want %s（根因：%s）", d.RootCauseStatus, sc.wantStatus, d.RootCause)
				}
				t.Logf("%s status=%s conf=%.2f level=%s 根因=%s",
					sc.id, d.RootCauseStatus, d.Confidence, d.ConfidenceLevel, d.RootCause)
				checkIncidentInvariants(t, c, inc)
			})

			t.Run("证据缺失", func(t *testing.T) {
				c := runChainWith(t, sc.fault(), chainOpts{stripEvidence: true})
				if len(c.Diags) == 0 {
					t.Fatal("证据被剥掉后诊断器直接不工作了，应降级而不是静默")
				}
				for _, d := range c.Diags {
					if d.RootCauseStatus == model.RootCauseConfirmed {
						t.Errorf("%s 在没有证据的情况下仍判 confirmed", d.Diagnoser)
					}
					if d.Confidence > deterministic.ConfClue {
						t.Errorf("%s 无证据时置信度 %.2f 超过线索档 %v", d.Diagnoser, d.Confidence, deterministic.ConfClue)
					}
					if len(d.EvidenceChain) != 0 {
						t.Errorf("%s 引用了不存在的证据: %v", d.Diagnoser, d.EvidenceChain)
					}
					if len(d.EvidenceGaps) == 0 {
						t.Errorf("%s 没有说明证据缺口", d.Diagnoser)
					}
				}
			})

			// 采集失败语义：主体资源没采到时，本场景既不能出 Finding 也不能出结论。
			// 注意不断言「整次扫描零产出」——同快照里其他真实故障仍应照常上报，
			// 需求要求的是「采集失败不得被读成资源正常」，而不是「一处失败全集群闭嘴」。
			t.Run("采集失败", func(t *testing.T) {
				snap := sc.fault()
				if sc.drop != nil {
					sc.drop(snap)
				}
				c := runChain(t, snap)
				c.requireNoFinding(t, sc.rule)
				if c.hasDiagnoser(sc.id) {
					t.Errorf("主体资源/事件没采到，诊断器 %s 仍给出了结论", sc.id)
				}
			})

			t.Run("易误判边界", func(t *testing.T) {
				if sc.boundary == nil {
					t.Fatalf("场景 %s 缺少边界条件夹具", sc.name)
				}
				c := runChain(t, sc.boundary())
				if sc.boundaryHits {
					c.mustFinding(t, sc.rule)
				} else {
					c.requireNoFinding(t, sc.rule)
				}
				if sc.boundaryNoDiagnoser && c.hasDiagnoser(sc.id) {
					t.Errorf("边界条件下诊断器 %s 不该出结论，但它给了", sc.id)
				}
			})

			if sc.multi != nil {
				t.Run("多个关联故障", func(t *testing.T) {
					c := runChain(t, sc.multi())
					root := c.incidentOf(t, sc.rule)
					if sc.multiRootRule != "" && root.Root.Rule != sc.multiRootRule {
						t.Errorf("故障链根因 = %s，应为 %s（症状不能顶掉根因）", root.Root.Rule, sc.multiRootRule)
					}
					if !c.hasDiagnoser(sc.id) {
						t.Errorf("关联故障里诊断器 %s 没有参与", sc.id)
					}
					for i := range c.Incs {
						checkIncidentInvariants(t, c, &c.Incs[i])
					}
				})
			}
		})
	}
}

// TestScenarioCoverage 核对需求 §10 列出的 19 个场景一个都不能少。
func TestScenarioCoverage(t *testing.T) {
	required := []string{
		"CrashLoopBackOff", "ImagePullBackOff", "Pending", "FailedScheduling",
		"FailedMount", "Node NotReady", "Node Pressure", "Deployment 副本不足",
		"StatefulSet 异常", "Service 无 Endpoint", "EndpointSlice 异常",
		"Ingress 后端异常", "PVC Pending", "PV 异常", "StorageClass 异常",
		"HPA 异常", "PDB 阻断驱逐", "DNS 异常", "NetworkPolicy 潜在通信问题",
	}
	have := map[string]bool{}
	for _, sc := range scenarios {
		have[sc.name] = true
	}
	for _, name := range required {
		if !have[name] {
			t.Errorf("需求 §10 场景 %q 没有回归夹具", name)
		}
	}
}
