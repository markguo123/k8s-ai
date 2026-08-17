package server

import (
	"bytes"
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
	_ = json.NewDecoder(resp.Body).Decode(&v)
	if v["version"] == "" {
		t.Fatal("version 为空")
	}
}

func TestCreateAndGetScan(t *testing.T) {
	srv := New(&fakeScan{result: &model.ScanResult{}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/scans", "application/json", bytes.NewBufferString(`{"namespace":"default"}`))
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("create = %v %v", resp, err)
	}
	var job ScanJob
	_ = json.NewDecoder(resp.Body).Decode(&job)
	if job.ID == "" || (job.Status != StatusRunning && job.Status != StatusSucceeded) {
		t.Fatalf("job = %+v", job)
	}
	// 轮询直到 succeeded
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r2, _ := http.Get(ts.URL + "/api/v1/scans/" + job.ID)
		var j2 ScanJob
		_ = json.NewDecoder(r2.Body).Decode(&j2)
		if j2.Status == StatusSucceeded {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("任务未在超时内完成")
}

func TestSingleConcurrency(t *testing.T) {
	start := make(chan struct{})
	srv := New(&fakeScan{result: &model.ScanResult{}, delay: time.Second, start: start})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	r1, err := http.Post(ts.URL+"/api/v1/scans", "application/json", bytes.NewBufferString(`{}`))
	if err != nil || r1.StatusCode != http.StatusAccepted {
		t.Fatalf("第一次创建 = %v %v", r1, err)
	}
	r2, err := http.Post(ts.URL+"/api/v1/scans", "application/json", bytes.NewBufferString(`{}`))
	if err != nil || r2.StatusCode != http.StatusConflict {
		t.Fatalf("并发时第二次应 409: %v %v", r2, err)
	}
	<-start
}

func TestUnknownAndBadBody(t *testing.T) {
	srv := New(&fakeScan{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	if r, _ := http.Get(ts.URL + "/api/v1/scans/nope"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("404 expected, got %d", r.StatusCode)
	}
	r, _ := http.Post(ts.URL+"/api/v1/scans", "application/json", bytes.NewBufferString("{bad"))
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("400 expected, got %d", r.StatusCode)
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "json") {
		t.Fatalf("响应应含 JSON Content-Type")
	}
}
