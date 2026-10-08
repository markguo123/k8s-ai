package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// fakeScan 服务测试桩。
type fakeScan struct {
	result *model.ScanResult
	err    error
	delay  time.Duration
	start  chan struct{}
}

func (f *fakeScan) Run(ctx context.Context, opts model.ScanOptions) (*model.ScanResult, error) {
	if f.start != nil {
		close(f.start)
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.result, f.err
}

func (f *fakeScan) Validate(ctx context.Context, opts model.ScanOptions) (string, error) {
	return "v1.28.13", nil
}

// decodeData 解析统一响应信封，把 data 解到 dst 并返回信封元数据。
func decodeData(t *testing.T, resp *http.Response, dst any) *apiResponse {
	t.Helper()
	defer resp.Body.Close()
	var env apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("解析响应信封失败: %v", err)
	}
	if env.RequestID == "" {
		t.Fatal("响应缺少 request_id")
	}
	if env.Timestamp.IsZero() {
		t.Fatal("响应缺少 timestamp")
	}
	if env.Data != nil && dst != nil {
		raw, err := json.Marshal(env.Data)
		if err != nil {
			t.Fatalf("data 序列化失败: %v", err)
		}
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatalf("data 解析失败: %v", err)
		}
	}
	return &env
}

func TestHealthReadyVersion(t *testing.T) {
	srv := New(&fakeScan{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %v %v", path, resp, err)
		}
	}
	resp, err := http.Get(ts.URL + "/version")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatal(err)
	}
	var v map[string]string
	decodeData(t, resp, &v)
	if v["version"] == "" {
		t.Fatal("version 为空")
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("响应缺少 X-Request-ID 头")
	}
}

func TestCreateAndGetScan(t *testing.T) {
	ts, token, _, _ := newAuthedScanServer(t, &fakeScan{result: &model.ScanResult{}})

	resp, err := authedPost(ts, "/api/v1/scans", `{"namespace":"default"}`, token)
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %v %v", resp, err)
	}
	var job ScanJob
	decodeData(t, resp, &job)
	if job.ID == "" || (job.Status != StatusRunning && job.Status != StatusSucceeded) {
		t.Fatalf("job = %+v", job)
	}
	// 轮询直到 succeeded
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r2 := authedGet(t, ts, "/api/v1/scans/"+job.ID, token)
		var j2 ScanJob
		decodeData(t, r2, &j2)
		if j2.Status == StatusSucceeded {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("任务未在超时内完成")
}

func TestSingleConcurrency(t *testing.T) {
	start := make(chan struct{})
	ts, token, _, _ := newAuthedScanServer(t, &fakeScan{result: &model.ScanResult{}, delay: time.Second, start: start})

	r1, err := authedPost(ts, "/api/v1/scans", `{}`, token)
	if err != nil || r1.StatusCode != http.StatusAccepted {
		t.Fatalf("第一次创建 = %v %v", r1, err)
	}
	r2, err := authedPost(ts, "/api/v1/scans", `{}`, token)
	if err != nil || r2.StatusCode != http.StatusConflict {
		t.Fatalf("并发时第二次应 409: %v %v", r2, err)
	}
	<-start
}

func TestUnknownAndBadBody(t *testing.T) {
	ts, token, _, _ := newAuthedScanServer(t, &fakeScan{})
	if r := authedGet(t, ts, "/api/v1/scans/nope", token); r.StatusCode != http.StatusNotFound {
		t.Fatalf("404 expected, got %d", r.StatusCode)
	} else {
		env := decodeData(t, r, nil)
		if env.Error == nil || env.Error.Code != codeNotFound {
			t.Fatalf("404 响应缺少统一错误码: %+v", env.Error)
		}
	}
	r, _ := authedPost(ts, "/api/v1/scans", "{bad", token)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("400 expected, got %d", r.StatusCode)
	} else {
		env := decodeData(t, r, nil)
		if env.Error == nil || env.Error.Code != codeInvalidArgument {
			t.Fatalf("400 响应缺少统一错误码: %+v", env.Error)
		}
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "json") {
		t.Fatalf("响应应含 JSON Content-Type")
	}
}

func TestRequestIDPassThrough(t *testing.T) {
	srv := New(&fakeScan{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/version", nil)
	req.Header.Set("X-Request-ID", "req-abc-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Header.Get("X-Request-ID"); got != "req-abc-123" {
		t.Fatalf("应透传调用方 request_id, got %q", got)
	}
	var v map[string]string
	env := decodeData(t, resp, &v)
	if env.RequestID != "req-abc-123" {
		t.Fatalf("信封 request_id 不匹配, got %q", env.RequestID)
	}
}
