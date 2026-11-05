package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// API 通用响应约束（四期需求 v1.2 §23）：
// - 每个响应都包含 request_id / timestamp；
// - 成功时携带 data（可选分页 pagination），error 省略；
// - 失败时携带 error.code / error.message。
const (
	defaultPage     = 1
	defaultPageSize = 20
	maxPageSize     = 100
)

// 通用错误码。
const (
	codeInvalidArgument = "invalid_argument"
	codeNotFound        = "not_found"
	codeConflict        = "conflict"
	codeUnauthorized    = "unauthorized"
	codeForbidden       = "forbidden"
	codeInternal        = "internal_error"
	// codeUnavailable 用于 /readyz 未就绪（P6.1）：依赖未到位，重试可能成功。
	codeUnavailable = "service_unavailable"
)

// APIError 统一的错误对象。
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Pagination 稳定的分页元数据。
type Pagination struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// PageResult 是带分页列表响应的 data 载体。
type PageResult[T any] struct {
	Items []T `json:"items"`
}

type apiResponse struct {
	RequestID  string      `json:"request_id"`
	Timestamp  time.Time   `json:"timestamp"`
	Data       any         `json:"data,omitempty"`
	Error      *APIError   `json:"error,omitempty"`
	Pagination *Pagination `json:"pagination,omitempty"`
}

// PageOf 从查询参数读取 page / page_size（兼容 pageSize），
// 返回收敛到合法区间后的值；无参数或非法参数用默认值。
func PageOf(r *http.Request) (page, pageSize int) {
	page = parseIntClamp(r.URL.Query().Get("page"), defaultPage, 1, 1<<30)
	rawSize := r.URL.Query().Get("page_size")
	if rawSize == "" {
		rawSize = r.URL.Query().Get("pageSize")
	}
	pageSize = parseIntClamp(rawSize, defaultPageSize, 1, maxPageSize)
	return page, pageSize
}

func parseIntClamp(raw string, def, min, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func totalPages(total int64, pageSize int) int {
	if pageSize <= 0 {
		return 0
	}
	pages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if pages < 1 {
		return 1
	}
	return pages
}

// writeOK 写成功响应。
func writeOK(w http.ResponseWriter, r *http.Request, status int, data any) {
	writeResponse(w, r, status, apiResponse{
		RequestID: RequestIDFrom(r.Context()),
		Timestamp: time.Now().UTC(),
		Data:      data,
	})
}

// writePage 写带分页的列表响应。
func writePage(w http.ResponseWriter, r *http.Request, status int, items any, total int64) {
	page, pageSize := PageOf(r)
	writeResponse(w, r, status, apiResponse{
		RequestID: RequestIDFrom(r.Context()),
		Timestamp: time.Now().UTC(),
		Data:      items,
		Pagination: &Pagination{
			Page:       page,
			PageSize:   pageSize,
			Total:      total,
			TotalPages: totalPages(total, pageSize),
		},
	})
}

// writeError 写统一错误响应。
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeResponse(w, r, status, apiResponse{
		RequestID: RequestIDFrom(r.Context()),
		Timestamp: time.Now().UTC(),
		Error:     &APIError{Code: code, Message: message},
	})
}

func writeResponse(w http.ResponseWriter, r *http.Request, status int, res apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(res)
}
