package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPageOfDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/list", nil)
	page, size := PageOf(req)
	if page != defaultPage || size != defaultPageSize {
		t.Fatalf("默认分页应为 %d/%d, got %d/%d", defaultPage, defaultPageSize, page, size)
	}
}

func TestPageOfValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/list?page=3&page_size=50", nil)
	page, size := PageOf(req)
	if page != 3 || size != 50 {
		t.Fatalf("分页解析错误: got %d/%d", page, size)
	}
}

func TestPageOfPageSizeAlias(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/list?pageSize=7", nil)
	_, size := PageOf(req)
	if size != 7 {
		t.Fatalf("pageSize 别名未生效, got %d", size)
	}
}

func TestPageOfClampingAndInvalid(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/list?page=999999999999&page_size=5000", nil)
	page, size := PageOf(req)
	if size != maxPageSize {
		t.Fatalf("page_size 应收敛到 %d, got %d", maxPageSize, size)
	}
	if page < 1 {
		t.Fatalf("page 应至少为 1, got %d", page)
	}
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/list?page=abc&page_size=-3", nil)
	p2, s2 := PageOf(req2)
	if p2 != defaultPage || s2 != defaultPageSize {
		t.Fatalf("非法参数应回退默认值, got %d/%d", p2, s2)
	}
}

func TestTotalPages(t *testing.T) {
	cases := []struct {
		total  int64
		size   int
		expect int
	}{
		{0, 20, 1},
		{1, 20, 1},
		{20, 20, 1},
		{21, 20, 2},
		{100, 0, 0},
	}
	for _, c := range cases {
		if got := totalPages(c.total, c.size); got != c.expect {
			t.Fatalf("total totalPages(%d,%d)=%d, expect %d", c.total, c.size, got, c.expect)
		}
	}
}

func TestWritePageEnvelope(t *testing.T) {
	var handler http.Handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePage(w, r, http.StatusOK, PageResult[string]{Items: []string{"a", "b"}}, 12)
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/list?page=2&page_size=5", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyRequestID, "rid-page"))
	handler.ServeHTTP(rr, req)

	var env apiResponse
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.RequestID != "rid-page" {
		t.Fatalf("request_id 缺失, got %q", env.RequestID)
	}
	if env.Pagination == nil {
		t.Fatal("分页响应缺少 pagination")
	}
	if env.Pagination.Page != 2 || env.Pagination.PageSize != 5 || env.Pagination.Total != 12 || env.Pagination.TotalPages != 3 {
		t.Fatalf("分页字段不稳定: %+v", env.Pagination)
	}
	raw, _ := json.Marshal(env.Data)
	var result PageResult[string]
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Items) != 2 {
		t.Fatalf("data 应携带 items, got %s", raw)
	}
	if env.Error != nil {
		t.Fatalf("成功响应不应携带 error: %+v", env.Error)
	}
}

func TestWriteErrorEnvelope(t *testing.T) {
	var handler http.Handler
	handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusForbidden, codeForbidden, "permission denied")
	})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyRequestID, "rid-err"))
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("状态码错误, got %d", rr.Code)
	}
	var env apiResponse
	if err := json.NewDecoder(rr.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Error == nil || env.Error.Code != codeForbidden || env.Error.Message != "permission denied" {
		t.Fatalf("错误信封字段错误: %+v", env.Error)
	}
	if env.RequestID != "rid-err" || env.Timestamp.IsZero() {
		t.Fatalf("错误响应缺少 request_id/timestamp: %+v", env)
	}
	if env.Data != nil {
		t.Fatalf("错误响应不应携带 data: %+v", env.Data)
	}
	if env.Pagination != nil {
		t.Fatalf("错误响应不应携带 pagination: %+v", env.Pagination)
	}
}
