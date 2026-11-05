package deterministic_test

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/deterministic"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// checkIncidentInvariants 核对需求 §3.3 与 §7 的六条底线。
//
// 这组断言与具体场景无关，任何一条故障链都必须满足，因此每个场景的每次运行都跑一遍：
//  1. 根因状态只有 confirmed/probable/unknown 三种，且必须有判定来源；
//  2. confirmed 必须挂到本次扫描里真实存在的证据 ID；
//  3. 非 confirmed 必须写清证据缺口（unknown 是合法结论，但不能是哑结论）；
//  4. 因果边两端必须是本故障链里的 Finding，且带关系类型与传导机制，
//     且两个资源在关系图上真的有路径；
//  5. 置信度只能落在离散阶梯上，防止「0.83」这类假精确数字；
//  6. 由关联层建立的因果边一律 probable。
func checkIncidentInvariants(t *testing.T, c *chain, inc *model.Incident) {
	t.Helper()

	switch inc.RootCauseStatus {
	case model.RootCauseConfirmed, model.RootCauseProbable, model.RootCauseUnknown:
	default:
		t.Fatalf("故障链 %s 根因状态非法: %q", inc.ID, inc.RootCauseStatus)
	}
	if inc.RootCauseMethod == "" {
		t.Errorf("故障链 %s 没有说明根因判定来源", inc.ID)
	}
	if inc.RootCauseStatus != model.RootCauseUnknown && inc.RootCause == "" {
		t.Errorf("故障链 %s 状态为 %s 却没有根因陈述", inc.ID, inc.RootCauseStatus)
	}

	universe := c.evidenceUniverse(inc)
	if inc.RootCauseStatus == model.RootCauseConfirmed && len(inc.EvidenceRefs) == 0 {
		t.Errorf("故障链 %s 判为 confirmed 但没有任何证据引用", inc.ID)
	}
	for _, id := range inc.EvidenceRefs {
		if !universe[id] {
			t.Errorf("故障链 %s 引用了不属于本链的证据 ID %s", inc.ID, id)
		}
	}
	if inc.RootCauseStatus != model.RootCauseConfirmed && len(inc.EvidenceGaps) == 0 && len(inc.Members) > 0 {
		t.Errorf("故障链 %s 状态为 %s，但未说明证据缺口", inc.ID, inc.RootCauseStatus)
	}

	inChain := map[string]bool{inc.Root.ID: true}
	for _, m := range inc.Members {
		inChain[m.ID] = true
	}
	for _, e := range inc.CausalEdges {
		if !inChain[e.SourceFinding] || !inChain[e.TargetFinding] {
			t.Errorf("因果边 %s->%s 有一端不在本故障链里", e.SourceFinding, e.TargetFinding)
		}
		if e.RelationType == "" {
			t.Errorf("因果边 %s->%s 缺少资源关系类型（无法自证传播路径）", e.SourceFinding, e.TargetFinding)
		}
		if e.Mechanism == "" {
			t.Errorf("因果边 %s->%s 缺少传导机制说明", e.SourceFinding, e.TargetFinding)
		}
		if e.Method == "correlation" && e.Confidence != model.RootCauseProbable {
			t.Errorf("因果边 %s->%s 置信度 = %s，关联层不得自行升格为 %s",
				e.SourceFinding, e.TargetFinding, e.Confidence, model.RootCauseConfirmed)
		}
		if !c.Graph.Connected(e.SourceResource, e.TargetResource) {
			t.Errorf("因果边 %s->%s 的两个资源在关系图上不连通：%v %v",
				e.SourceFinding, e.TargetFinding, e.SourceResource.Key(), e.TargetResource.Key())
		}
	}

	if inc.Confidence != 0 {
		assertLadderConfidence(t, "incident", inc.Confidence)
	}
	for _, d := range c.Diags {
		if d.IncidentID != inc.ID {
			continue
		}
		assertLadderConfidence(t, "diagnosis:"+d.Diagnoser, d.Confidence)
		if d.Source != "deterministic" || d.LLMUsed {
			t.Errorf("诊断器 %s 的结论来源被标成 %s/llmUsed=%v", d.Diagnoser, d.Source, d.LLMUsed)
		}
		for _, id := range d.EvidenceChain {
			if !universe[id] {
				t.Errorf("诊断器 %s 引用了不属于本链的证据 ID %s", d.Diagnoser, id)
			}
		}
	}
}

// assertLadderConfidence 断言置信度落在 deterministic 包的离散阶梯上。
func assertLadderConfidence(t *testing.T, what string, conf float64) {
	t.Helper()
	for _, v := range []float64{
		deterministic.ConfObjectAndEvent, deterministic.ConfObjectStatus,
		deterministic.ConfEventMessage, deterministic.ConfInferred,
		deterministic.ConfClue, deterministic.ConfUnknown,
	} {
		if conf == v {
			return
		}
	}
	t.Errorf("%s 置信度 %.4f 不在可信度阶梯上", what, conf)
}

// TestNilTolerantEngine 锁住「缺数据不 panic」：关系图、索引、快照都可能整块缺失。
func TestNilTolerantEngine(t *testing.T) {
	if got := deterministic.NewEngine(nil, nil, nil, nil).Apply(nil); len(got) != 0 {
		t.Errorf("空引擎不应产出结论，实际 %d 条", len(got))
	}
	inc := []model.Incident{{
		ID:   "F1",
		Root: model.FindingRef{ID: "F1", Resource: objRef("Pod", "prod", "web-1")},
	}}
	got := deterministic.NewEngine(nil, nil, nil, nil).Apply(inc)
	for _, d := range got {
		if d.RootCauseStatus == model.RootCauseConfirmed {
			t.Errorf("没有任何数据的情况下判了 confirmed: %+v", d)
		}
	}
	if inc[0].RootCauseStatus != model.RootCauseUnknown {
		t.Errorf("无诊断器覆盖时应给出 unknown，实际 %q", inc[0].RootCauseStatus)
	}
	if len(inc[0].EvidenceGaps) == 0 {
		t.Error("unknown 结论必须说明缺口")
	}
}

// TestDiagnoserRegistry 核对注册表：诊断器数量、ID 唯一、场景描述非空。
func TestDiagnoserRegistry(t *testing.T) {
	ds := deterministic.Diagnosers()
	if len(ds) < 24 {
		t.Fatalf("已注册诊断器 %d 个，少于需求 §4.2 要求的 24 个", len(ds))
	}
	seen := map[string]bool{}
	for _, d := range ds {
		if d.ID() == "" || d.Scenario() == "" {
			t.Errorf("诊断器缺少 ID 或场景描述: %s", d.ID())
		}
		if seen[d.ID()] {
			t.Errorf("诊断器 ID 重复: %s", d.ID())
		}
		seen[d.ID()] = true
	}
	for _, want := range []string{
		"POD_CRASHLOOP", "POD_OOMKILLED", "POD_IMAGEPULL", "POD_UNSCHEDULABLE",
		"POD_FAILEDMOUNT", "POD_EVICTED", "POD_PROBE_FAILING", "WORKLOAD_CONFIG_REF_MISSING",
		"NODE_NOTREADY", "NODE_PRESSURE", "WORKLOAD_REPLICAS_UNAVAILABLE",
		"WORKLOAD_DAEMONSET_GAP", "WORKLOAD_JOB_FAILED", "WORKLOAD_HPA_NOT_SCALING",
		"WORKLOAD_PDB_BLOCKING", "NETWORK_SERVICE_NO_ENDPOINT",
		"NETWORK_ENDPOINTSLICE_INCONSISTENT", "NETWORK_INGRESS_BACKEND_UNAVAILABLE",
		"PLATFORM_DNS_ABNORMAL", "NETWORK_POLICY_POSTURE", "STORAGE_PVC_PENDING",
		"STORAGE_PV_UNAVAILABLE", "STORAGE_CLASS_INVALID", "STORAGE_VOLUME_ATTACHMENT_STUCK",
	} {
		if !seen[want] {
			t.Errorf("缺少诊断器 %s", want)
		}
	}
	if got := len(deterministic.Scenarios()); got != len(ds) {
		t.Errorf("场景清单 %d 条与诊断器数 %d 不一致", got, len(ds))
	}
}

// TestStorageFailurePropagatesToWorkload 验证跨域传播：存储根因不会被上层症状顶掉，
// 且传播链上的每条边都有资源关系出处（需求 §3.3「聚合不等于因果」）。
func TestStorageFailurePropagatesToWorkload(t *testing.T) {
	s := newSnapshot()
	nodeOK(s)
	addWorkload(s, "StatefulSet", "prod", "db", func(w *model.WorkloadInfo) {
		w.Selector = map[string]string{"app": "db"}
		w.DesiredReplicas, w.ReadyReplicas = int32p(2), int32p(0)
	})
	addStorage(s, "StorageClass", "", "slow-disk", func(st *model.StorageInfo) { st.Status = ""; st.Driver = "" })
	addStorage(s, "PVC", "prod", "data-db-0", func(st *model.StorageInfo) {
		st.Status, st.Reason = "Pending", "provisioning"
		st.StorageClassName = "slow-disk"
	})
	addPod(s, "prod", "db-0", func(p *model.PodInfo) {
		p.Phase, p.CreationTimestamp = "Pending", oldTimestamp
		p.Labels = map[string]string{"app": "db"}
		p.OwnerRefs = []model.ResourceRef{objRef("StatefulSet", "prod", "db")}
		p.PVCRefs = []model.ResourceRef{objRef("PVC", "prod", "data-db-0")}
	})
	addEvent(s, s.Pods[0].Ref, "Warning", "FailedMount",
		"Unable to attach or mount volumes: unmounted volumes=[data], unattached volumes=[data]")
	addService(s, "prod", "db", map[string]string{"app": "db"})
	s.EndpointSlices = []model.EndpointSliceInfo{{
		Ref: objRef("EndpointSlice", "prod", "db-abc"), ServiceName: "db", Ready: 0, NotReady: 2,
	}}

	c := runChain(t, s)
	rootInc := c.incidentOf(t, "PVCPending")
	// 根因必须一路探到最底层：StorageClass 没有 provisioner 才是 PVC 起不来的因，
	// PVC Pending / Pod Pending / 副本不足 / 无 Endpoint 都只是它的下游症状。
	if rootInc.Root.Rule != "StorageClassNoProvisioner" {
		t.Errorf("故障链根因 = %s，应为 StorageClassNoProvisioner（存储底座优先于上层症状）", rootInc.Root.Rule)
	}
	// 症状必须在同一条链上，而不是各写一条独立结论。
	for _, rule := range []string{"FailedMount", "StatefulSetReplica", "ServiceNoEndpoint", "PendingPod", "PVCPending"} {
		if c.incidentOf(t, rule).ID != rootInc.ID {
			t.Errorf("规则 %s 没有和存储根因聚到同一条故障链", rule)
		}
	}
	if len(rootInc.CausalEdges) < 2 {
		t.Errorf("只建立了 %d 条因果边，跨域传播链没有串起来: %+v", len(rootInc.CausalEdges), rootInc.CausalEdges)
	}
	if len(rootInc.ImpactChain) < 2 {
		t.Errorf("影响链过短: %v", rootInc.ImpactChain)
	}
	// 资源关系必须真实存在（挂载 / 归属 / selector）。
	pvc := objRef("PVC", "prod", "data-db-0")
	pod := objRef("Pod", "prod", "db-0")
	svc := objRef("Service", "prod", "db")
	if !c.Graph.Connected(pvc, pod) {
		t.Error("PVC 与 Pod 之间没有真实挂载关系")
	}
	if !c.Graph.Connected(pod, svc) {
		t.Error("Pod 与 Service 之间没有真实 selector 关系")
	}
	for _, e := range rootInc.CausalEdges {
		t.Logf("因果边 %s -> %s 关系=%s 机制=%s",
			e.SourceResource.Key(), e.TargetResource.Key(), e.RelationType, e.Mechanism)
	}
	checkIncidentInvariants(t, c, rootInc)

	// 存储根因必须在结论里胜出：不能把「Service 没有 Endpoint」写成根因。
	d, _ := c.requireDiagnoser(t, "PVCPending", "STORAGE_PVC_PENDING")
	if d.RootCauseStatus != model.RootCauseConfirmed {
		t.Errorf("PVC 状态字段写明 Pending，应为 confirmed，实际 %s（依据：%s）", d.RootCauseStatus, d.ConfidenceBasis)
	}
	if !c.hasDiagnoser("STORAGE_CLASS_INVALID") {
		t.Error("同一条链上的 StorageClass 根因没有参与诊断")
	}
}

func TestProbeDiagnosisUsesCapturedProbeConfiguration(t *testing.T) {
	s := newSnapshot()
	nodeOK(s)
	addPod(s, "prod", "web-1", func(p *model.PodInfo) {
		p.NodeName = "node-1"
		p.Containers = []model.ContainerInfo{{
			Name: "web", State: "Running", Ready: false,
			Probes: []model.ProbeInfo{{
				Kind: "readiness", Action: "HTTP GET", Path: "/ready", Port: "80",
			}},
		}}
	})
	addEvent(s, s.Pods[0].Ref, "Warning", "Unhealthy",
		"Readiness probe failed: HTTP probe failed with statuscode: 404")

	c := runChain(t, s)
	diag, _ := c.requireDiagnoser(t, "Unhealthy", "POD_PROBE_FAILING")
	if !strings.Contains(diag.RootCause, "/ready") || !strings.Contains(diag.RootCause, "404") {
		t.Fatalf("根因必须带出已采集的探针路径与失败状态：%s", diag.RootCause)
	}
}
