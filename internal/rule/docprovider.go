// Package rule 实现规则引擎（FR-013）。
// docprovider.go：按需加载规则关联的 K8s API 字段文档（P7.7，SPEC §9.5）。
package rule

import (
	"embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed docdata/fields.json
var fieldDocFS embed.FS

// DocEntry 描述单个 API 字段路径的说明与官方文档链接。
type DocEntry struct {
	Field       string `json:"field"`
	Description string `json:"description"`
	Link        string `json:"link"`
}

// Text 字段文档的单行可展示文本。
func (d DocEntry) Text() string { return d.Description }

// DocProvider 按 docRef（API 字段路径）解析字段文档。
type DocProvider interface {
	// Lookup 返回 docRef 对应的字段文档；未收录返回 false。
	Lookup(docRef string) (DocEntry, bool)
}

// staticDocProvider 惰性加载 + 内存缓存 + 内嵌静态 JSON 兜底。
type staticDocProvider struct {
	mu   sync.Mutex
	data map[string]DocEntry
}

var (
	docProviderOnce sync.Once
	docProviderInst *staticDocProvider
)

// NewDocumentProvider 返回惰性加载的静态字段文档提供者（单例）。
// 文档来自内嵌静态 JSON，不发起任何 API 请求（SPEC §9.5：默认不增加 API 请求）。
func NewDocumentProvider() DocProvider {
	docProviderOnce.Do(func() {
		inst, err := loadFieldDocs()
		if err != nil {
			// 内嵌资源缺失属编程错误；置空 map 保证不 panic，仅降级为无文档。
			inst = &staticDocProvider{data: map[string]DocEntry{}}
		}
		docProviderInst = inst
	})
	return docProviderInst
}

func loadFieldDocs() (*staticDocProvider, error) {
	b, err := fieldDocFS.ReadFile("docdata/fields.json")
	if err != nil {
		return nil, err
	}
	var entries []DocEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, err
	}
	m := make(map[string]DocEntry, len(entries))
	for _, e := range entries {
		if e.Field != "" {
			m[e.Field] = e
		}
	}
	return &staticDocProvider{data: m}, nil
}

// lookupKey 归一化 docRef：去掉 "=Type" 之类的条件后缀作为稳定键。
func lookupKey(docRef string) string {
	if i := strings.IndexByte(docRef, '='); i >= 0 {
		return docRef[:i]
	}
	return docRef
}

func (p *staticDocProvider) Lookup(docRef string) (DocEntry, bool) {
	if p == nil || docRef == "" {
		return DocEntry{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.data[lookupKey(docRef)]
	return e, ok
}
