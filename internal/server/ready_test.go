package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// TestReadyzReflectsReadinessCheck 锁 P6.1 探针口径：/readyz 反映依赖状态，
// /healthz 保持进程级（依赖故障时 liveness 不能把容器杀成重启风暴）。
func TestReadyzReflectsReadinessCheck(t *testing.T) {
	notReady := func(context.Context) error {
		return fmt.Errorf("%w: schema version 0 < expected 4 (run k8s-ai db migrate)", store.ErrNotReady)
	}
	srv := New(&fakeScan{}).WithReadiness(notReady)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未就绪应 503, got %d", resp.StatusCode)
	}
	var env apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("解析 readyz 响应失败: %v", err)
	}
	resp.Body.Close()
	if env.Error == nil || env.Error.Code != codeUnavailable {
		t.Fatalf("未就绪应回统一错误码 %s, got %+v", codeUnavailable, env.Error)
	}
	if !strings.Contains(env.Error.Message, "db migrate") {
		t.Fatalf("探针响应应暴露可操作的未就绪原因, got %q", env.Error.Message)
	}

	liveness, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer liveness.Body.Close()
	if liveness.StatusCode != http.StatusOK {
		t.Fatalf("依赖故障时 /healthz 仍应 200, got %d", liveness.StatusCode)
	}

	// 依赖就绪后 /readyz 转 200（迁移 Job 跑完即可接活）。
	readySrv := New(&fakeScan{}).WithReadiness(func(context.Context) error { return nil })
	readyTs := httptest.NewServer(readySrv.Handler())
	defer readyTs.Close()
	readyResp, err := http.Get(readyTs.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer readyResp.Body.Close()
	if readyResp.StatusCode != http.StatusOK {
		t.Fatalf("就绪应 200, got %d", readyResp.StatusCode)
	}
}

// TestReadyzTimesOut 锁探针不会因依赖挂死而长时间阻塞 kubelet。
func TestReadyzTimesOut(t *testing.T) {
	srv := New(&fakeScan{}).WithReadiness(func(ctx context.Context) error {
		<-ctx.Done()
		return fmt.Errorf("%w: %v", store.ErrNotReady, ctx.Err())
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("探针超时应 503, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestUnregisteredAPIRoutesReturnJSONError 锁 P6.11 的实质约束：未登记的 /api/v1/*
// 必须在网关处回统一 JSON 错误体，绝不能被 SPA fallback 吞成 index.html。
// 状态码沿用三期能力基线（tests/platform 的零权限/未登记路由 = 403，
// deny-by-default），本批不放宽也不改语义。
func TestUnregisteredAPIRoutesReturnJSONError(t *testing.T) {
	ts, token, _, _ := newAuthedScanServer(t, &fakeScan{})
	paths := []string{"/api/v1/nope", "/api/v1/audit/events", "/api/v1/skills/missing/preview"}
	for _, p := range paths {
		r := authedGet(t, ts, p, token)
		if r.StatusCode != http.StatusForbidden {
			t.Fatalf("%s 未登记路由应 403（deny-by-default 基线）, got %d", p, r.StatusCode)
		}
		assertJSONError(t, r, codeForbidden)
	}
	// 写方法同样不能落到 SPA fallback。
	resp, err := authedPost(ts, "/api/v1/nope", `{}`, token)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST 未登记路径应 403, got %d", resp.StatusCode)
	}
	assertJSONError(t, resp, codeForbidden)

	// 未认证访问不存在的路由同样必须是 JSON 错误体，不能回前端页面。
	bare, err := http.Get(ts.URL + "/api/v1/nope")
	if err != nil {
		t.Fatal(err)
	}
	if bare.StatusCode == http.StatusOK {
		t.Fatalf("未认证的未知 API 路径不能放行")
	}
	assertJSONError(t, bare, "")
}

// assertJSONError 断言响应是统一 JSON 错误信封（且不是 HTML 页面）；wantCode 为空时
// 只校验信封结构，不校验具体错误码。
func assertJSONError(t *testing.T, resp *http.Response, wantCode string) {
	t.Helper()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Fatalf("必须是 JSON 响应（不能吐 SPA 页面）, got Content-Type=%q", ct)
	}
	var env apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("响应体不是合法 JSON 信封: %v", err)
	}
	if env.Error == nil || env.Error.Code == "" {
		t.Fatalf("应回统一错误信封, got %+v", env)
	}
	if wantCode != "" && env.Error.Code != wantCode {
		t.Fatalf("错误码应为 %s, got %+v", wantCode, env.Error)
	}
}

// TestNewNotFoundHandlerIsJSON 锁形态 B（static_enabled=false）：非 API 路径同样回 JSON。
func TestNewNotFoundHandlerIsJSON(t *testing.T) {
	ts := httptest.NewServer(NewNotFoundHandler())
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/any/spa/path")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("应 404, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "json") {
		t.Fatalf("非 API 路径也必须回 JSON, got %q", resp.Header.Get("Content-Type"))
	}
	var env apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if env.Error == nil || env.Error.Code != codeNotFound {
		t.Fatalf("错误码异常, got %+v", env.Error)
	}
}
