package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// blockingScan 是一个只会挂在 ctx 上的假扫描面：一进 Run 就推一版「资源采集进行中」的阶段账，
// 然后一直不返回，直到 ctx 被取消。停止体检要验的三件事——状态立刻停下、取消真的靠 ctx 传播、
// 单次闸门等 goroutine 真退出才放行——都得有一个「真的还在跑」的任务，一个立刻返回的 stub
// 什么都测不出来。
type blockingScan struct {
	entered  chan struct{} // 每次进入 Run 投一枚
	canceled chan struct{} // ctx 取消后才投：证明是取消让采集停下的，不是我们替它编了个终态
}

func (c *blockingScan) Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error) {
	if sink, ok := opts.Progress.(model.StageSink); ok {
		sink.StagesUpdated([]model.StageRecord{{Name: "collect", Title: "资源采集", Status: "running"}})
	}
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	select {
	case c.canceled <- struct{}{}:
	default:
	}
	return nil, ctx.Err()
}

func (c *blockingScan) Validate(context.Context, model.ScanOptions) (string, error) { return "ok", nil }

// startBlockedScan 发起一次体检并等它真的进了扫描面。「停止」停一个还没起跑的任务是另一种
// 情形（409），混在一起测就等于没测停止。第二次发起要等上一次 goroutine 退出释放单次闸门，
// 所以这里轮询到 202 而不是要求一次成功。
func startBlockedScan(t *testing.T, f *reportFixture, token, body string) string {
	t.Helper()
	scan, ok := f.server.svc.(*blockingScan)
	if !ok {
		t.Fatal("测试没有换上 blocking 扫描面")
	}
	deadline := time.Now().Add(5 * time.Second)
	last := 0
	for time.Now().Before(deadline) {
		var id string
		last, id = f.postScan(t, token, body)
		if last == http.StatusAccepted {
			select {
			case <-scan.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("任务 202 了但扫描面没被调用")
			}
			return id
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("发起体检始终没拿到 202，最后一次 %d", last)
	return ""
}

func cancelScanReq(t *testing.T, f *reportFixture, token, id string) (int, string) {
	t.Helper()
	status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans/"+id+"/cancel", token, "")
	return status, raw
}

func cancelScanData(t *testing.T, raw string) ScanJob {
	t.Helper()
	var env struct {
		Data ScanJob `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("解析停止响应失败：%v / %s", err, raw)
	}
	return env.Data
}

// TestCancelScanStopsRunningJob 锁 P9.11 的三条主链路：状态立刻落 cancelled 且阶段账不再
// 转圈；取消沿 ctx 传到扫描面；单次闸门等 goroutine 退出才放开（否则两个采集器同时打同一个
// API Server，比不允许多跑更糟）。
func TestCancelScanStopsRunningJob(t *testing.T) {
	f := newReportFixture(t, nil)
	admin := f.adminToken(t)
	scan := &blockingScan{entered: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}
	f.server.svc = scan

	id := startBlockedScan(t, f, admin, `{"scope":"namespace","namespace":"kube-system","preset":"quick"}`)

	status, raw := cancelScanReq(t, f, admin, id)
	if status != http.StatusOK {
		t.Fatalf("发起人停止自己的任务应 200，got %d：%s", status, raw)
	}
	job := cancelScanData(t, raw)
	if job.Status != StatusCancelled {
		t.Fatalf("停止后状态 = %s：%s", job.Status, raw)
	}
	if !strings.Contains(job.Error, "停止") {
		t.Fatalf("停止原因没写清楚：%q", job.Error)
	}
	if job.EndedAt == nil {
		t.Fatal("停止应写结束时间，否则页面轮询不会停")
	}
	if len(job.Stages) == 0 {
		t.Fatal("停止响应应带阶段账")
	}
	for _, st := range job.Stages {
		if st.Status == "running" {
			t.Fatalf("阶段账里留了个永远转圈的步骤：%+v", job.Stages)
		}
	}
	if job.Stages[0].Detail != "已停止" {
		t.Fatalf("被中断的阶段要写明是停止造成的：%+v", job.Stages[0])
	}

	// 取消必须真的传到扫描面：只改状态不取消 ctx，就是页面上停了、集群还在被采。
	select {
	case <-scan.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 未传播到扫描面：停止只改了状态")
	}

	// goroutine 收尾时不得把 cancelled 改写成 failed（context canceled 不是体检失败）。
	after, rawAfter := scanJobOf(t, f, admin, id)
	if after.Status != StatusCancelled {
		t.Fatalf("任务退出后状态被改写成 %s：%s", after.Status, rawAfter)
	}

	// 已结束的任务再停一次：409 + 说清「无需停止」。
	if status, raw = cancelScanReq(t, f, admin, id); status != http.StatusConflict {
		t.Fatalf("停止已结束的任务应 409，got %d：%s", status, raw)
	} else if !strings.Contains(raw, "该次体检已经结束") {
		t.Fatalf("409 文案不含可解释的原因：%s", raw)
	}

	// 闸门释放后能再发起一次（等上一次 runJob 真正退出，所以这里是轮询）。
	second := startBlockedScan(t, f, admin, `{"scope":"namespace","namespace":"app","preset":"quick"}`)
	if status, _ = cancelScanReq(t, f, admin, second); status != http.StatusOK {
		t.Fatalf("清理第二个任务失败，got %d", status)
	}
}

// TestCancelScanOwnerBoundary 锁停止面的鉴权形状：别人按不动你的任务，且 job id 不因为加了
// 这个端点就变成存在性探针；admin 例外（他要能收口现场）；每一次停止都留审计。
func TestCancelScanOwnerBoundary(t *testing.T) {
	f := newReportFixture(t, nil)
	admin := f.adminToken(t)
	owner := f.userToken(t, "op-cancel", "operator")
	f.grantNamespaces(t, "u-op-cancel", "app")
	other := f.userToken(t, "op-other", "operator")

	scan := &blockingScan{entered: make(chan struct{}, 1), canceled: make(chan struct{}, 1)}
	f.server.svc = scan

	id := startBlockedScan(t, f, owner, `{"scope":"namespace","namespace":"app","preset":"quick"}`)

	// 未登录：401。
	if status, _ := cancelScanReq(t, f, "", id); status != http.StatusUnauthorized {
		t.Fatalf("未登录停止应 401，got %d", status)
	}

	// 他人任务与不存在的任务同形 404。
	stOther, rawOther := cancelScanReq(t, f, other, id)
	stGhost, rawGhost := cancelScanReq(t, f, other, "nosuchjob")
	if stOther != http.StatusNotFound || stGhost != http.StatusNotFound {
		t.Fatalf("他人停止应 404：他人=%d 不存在=%d", stOther, stGhost)
	}
	if got, want := errorOf(t, rawOther), errorOf(t, rawGhost); got != want {
		t.Fatalf("他人任务与不存在的任务响应不同形：\n他人 %s\n不存在 %s", got, want)
	}

	// 别人按不动它：任务仍在跑。
	mid, rawMid := scanJobOf(t, f, owner, id)
	if mid.Status != StatusRunning {
		t.Fatalf("他人越权后任务状态变成 %s：%s", mid.Status, rawMid)
	}

	// admin 可以停别人的任务（收口现场的能力）。
	status, raw := cancelScanReq(t, f, admin, id)
	if status != http.StatusOK {
		t.Fatalf("admin 停他人任务应 200，got %d：%s", status, raw)
	}
	if got := cancelScanData(t, raw); got.Status != StatusCancelled {
		t.Fatalf("admin 停止后状态 = %s", got.Status)
	}
	select {
	case <-scan.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("admin 停止后 ctx 未传播到扫描面")
	}

	// 每一次停止都留痕：谁中断了对哪个范围的采集。
	rows, err := f.st.Audit().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found *store.AuditEvent
	for _, e := range rows {
		if e.Action == store.ActionScanCancel {
			copied := *e
			found = &copied
		}
	}
	if found == nil {
		t.Fatal("停止体检没有落审计")
	}
	if found.ResourceName != id {
		t.Fatalf("审计没指向被停的那次体检：%+v", found)
	}
	if !strings.Contains(found.ResultSummary, "namespace") || !strings.Contains(found.ResultSummary, "app") {
		t.Fatalf("审计应记下体检范围：%s", found.ResultSummary)
	}
}
