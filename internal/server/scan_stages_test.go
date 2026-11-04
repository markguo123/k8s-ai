package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// stagedScan 是一个按真实流水线行事的假扫描面：它通过 model.StageSink 推阶段账（与
// service 内部的阶段账同一入口），并在中途停住，让测试能在「扫描进行中」读任务。
type stagedScan struct {
	reached  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (s *stagedScan) Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error) {
	sink, ok := opts.Progress.(model.StageSink)
	if !ok {
		return nil, errors.New("页面体检没有接阶段账回调")
	}
	sink.StagesUpdated([]model.StageRecord{{Name: "collect", Title: "资源采集", Status: "running"}})
	close(s.reached)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	sink.StagesUpdated([]model.StageRecord{
		{Name: "collect", Title: "资源采集", Status: "done", Elapsed: "1s", Millis: 1000},
		{Name: "rules", Title: "规则引擎", Status: "running"},
	})
	return &model.ScanResult{
		Stages: []model.StageRecord{
			{Name: "collect", Title: "资源采集", Status: "done", Elapsed: "1s", Millis: 1000},
			{Name: "rules", Title: "规则引擎", Status: "done", Detail: "命中 2 条", Elapsed: "10ms", Millis: 10},
			{Name: "llm", Title: "LLM 增强诊断", Status: "skipped", Detail: "未配置模型"},
		},
	}, nil
}

func (s *stagedScan) Validate(context.Context, model.ScanOptions) (string, error) { return "ok", nil }

func scanJobOf(t *testing.T, f *reportFixture, token, id string) (ScanJob, string) {
	t.Helper()
	status, _, raw := f.req(t, http.MethodGet, "/api/v1/scans/"+id, token, "")
	if status != http.StatusOK {
		t.Fatalf("查任务应 200：%d %s", status, raw)
	}
	var env struct {
		Data ScanJob `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("解析任务响应失败：%v / %s", err, raw)
	}
	return env.Data, raw
}

// TestScanJobExposesStagesWhileRunning 锁 P9.4 的页面契约：体检还在跑，轮询就能拿到
// 阶段账（running 中的那一步也在里面）；跑完后阶段账换成报告里那份完整的。
func TestScanJobExposesStagesWhileRunning(t *testing.T) {
	f := newReportFixture(t, nil)
	admin := f.adminToken(t)
	scan := &stagedScan{reached: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	f.server.svc = scan

	status, _, raw := f.req(t, http.MethodPost, "/api/v1/scans", admin,
		`{"scope":"namespace","namespace":"kube-system","preset":"quick"}`)
	if status != http.StatusAccepted {
		t.Fatalf("发起体检应 202：%d %s", status, raw)
	}
	var accepted struct {
		Data ScanJob `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &accepted); err != nil {
		t.Fatal(err)
	}

	select {
	case <-scan.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("扫描未被调用")
	}

	mid, rawMid := scanJobOf(t, f, admin, accepted.Data.ID)
	if mid.Status != StatusRunning {
		t.Fatalf("进行中的任务状态 = %s：%s", mid.Status, rawMid)
	}
	if len(mid.Stages) != 1 || mid.Stages[0].Status != "running" || mid.Stages[0].Title != "资源采集" {
		t.Fatalf("扫描中应看到进行中的阶段：%+v", mid.Stages)
	}

	close(scan.release)
	deadline := time.Now().Add(5 * time.Second)
	var final ScanJob
	for time.Now().Before(deadline) {
		final, _ = scanJobOf(t, f, admin, accepted.Data.ID)
		if final.Status == StatusSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != StatusSucceeded {
		t.Fatalf("任务没跑完：%+v", final)
	}
	// 收尾阶段（写报告/归档）只在 Run 返回时进阶段账，页面必须看到最后一版而不是中途那版。
	if len(final.Stages) != 3 || final.Stages[2].Name != "llm" || final.Stages[2].Status != "skipped" {
		t.Fatalf("最终阶段账不是报告里那份：%+v", final.Stages)
	}
}
