package deterministic_test

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

func TestHighRestartWithCleanExitIsNotReportedAsCrash(t *testing.T) {
	snap := newSnapshot()
	nodeOK(snap)
	addWorkload(snap, "Deployment", "prod", "nginx", func(w *model.WorkloadInfo) {
		w.DesiredReplicas = int32p(1)
		w.ReadyReplicas = int32p(1)
		w.Selector = map[string]string{"app": "nginx"}
	})
	addPod(snap, "prod", "nginx-0", func(p *model.PodInfo) {
		p.NodeName = "node-1"
		p.RestartPolicy = "Always"
		p.Labels = map[string]string{"app": "nginx"}
		p.OwnerRefs = []model.ResourceRef{objRef("Deployment", "prod", "nginx")}
		p.Containers = []model.ContainerInfo{{
			Name: "nginx", Image: "repo/nginx:1.27", State: "Running",
			RestartCount: 36, LastState: "Terminated", LastReason: "Completed", LastExitCode: 0,
		}}
	})

	c := runChain(t, snap)
	diag, _ := c.requireDiagnoser(t, "HighRestartCount", "POD_CRASHLOOP")
	if !strings.Contains(diag.RootCause, "退出码 0") || !strings.Contains(diag.RootCause, "正常结束") {
		t.Fatalf("退出码 0 不应被描述成笼统的异常退出: %q", diag.RootCause)
	}
	if strings.Contains(diag.RootCause, "反复异常退出") {
		t.Fatalf("正常退出被误判为崩溃: %q", diag.RootCause)
	}
	if !strings.Contains(strings.Join(diag.EvidenceGaps, "；"), "previous logs") {
		t.Fatalf("缺少退出原因时应明确证据缺口: %+v", diag.EvidenceGaps)
	}
	if len(diag.Remediation) != 0 {
		t.Fatalf("未确认退出原因时不应自动建议 rollout restart: %+v", diag.Remediation)
	}
}
