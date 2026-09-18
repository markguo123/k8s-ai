// Package rule 实现规则引擎（FR-013）。
package rule

import (
	"context"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/correlation"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestDocProviderLookup 已知字段路径能解析出文档；未知字段返回 false。
func TestDocProviderLookup(t *testing.T) {
	p := NewDocumentProvider()
	// 已知字段。
	e, ok := p.Lookup("status.containerStatuses[].state.waiting")
	if !ok {
		t.Fatal("已知字段应能解析")
	}
	if e.Field != "status.containerStatuses[].state.waiting" || e.Description == "" || e.Link == "" {
		t.Fatalf("文档条目不完整: %+v", e)
	}
	// 未知字段。
	if _, ok := p.Lookup("no.such.field"); ok {
		t.Fatal("未知字段不应解析")
	}
	// 空 docRef。
	if _, ok := p.Lookup(""); ok {
		t.Fatal("空 docRef 不应解析")
	}
}

// TestDocProviderNormalizeKey "=Type" 条件后缀应归一化到基础字段键。
func TestDocProviderNormalizeKey(t *testing.T) {
	if lookupKey("status.conditions[].type=Ready") != "status.conditions[].type" {
		t.Fatalf("lookupKey 归一化异常: %q", lookupKey("status.conditions[].type=Ready"))
	}
	if lookupKey("status.phase") != "status.phase" {
		t.Fatalf("无条件的键不应被改变")
	}
	// 带条件后缀的 docRef 能命中基础字段文档。
	p := NewDocumentProvider()
	e, ok := p.Lookup("status.conditions[].type=DiskPressure")
	if !ok || e.Field != "status.conditions[].type" {
		t.Fatalf("条件后缀应归一化命中: ok=%v e=%+v", ok, e)
	}
}

// TestEngineWithDoc 开启 --with-doc 时 Finding 附带文档；默认不带。
func TestEngineWithDoc(t *testing.T) {
	snap := &model.ClusterSnapshot{
		Namespaces: []model.NamespaceInfo{{Ref: model.ResourceRef{Kind: "Namespace", Name: "default"}}},
		Pods:       []model.PodInfo{testPod("default", "web-abc", crashContainer())},
	}
	reg := NewRegistry()
	if err := reg.RegisterWithMeta(CrashLoopBackOffRule{}, RuleMeta{
		RuleID: "WORKLOAD_CRASHLOOPBACKOFF", Category: "Workload", Kind: "Pod",
		RuleType: RuleTypeDetection, SeverityBase: model.SeverityMedium,
		NeedsLogs: true, DocRef: "status.containerStatuses[].state.waiting",
	}); err != nil {
		t.Fatalf("注册失败: %v", err)
	}
	idx := correlation.Build(snap)
	// WithDoc=true。
	withDoc := NewEngine(reg).Evaluate(context.Background(), snap, idx, model.ScanOptions{WithDoc: true})
	if len(withDoc) != 1 || withDoc[0].Doc == "" {
		t.Fatalf("开启 with-doc 应附带文档: %+v", withDoc)
	}
	// 默认不带。
	noDoc := NewEngine(reg).Evaluate(context.Background(), snap, idx, model.ScanOptions{})
	if len(noDoc) != 1 || noDoc[0].Doc != "" {
		t.Fatalf("默认不应附带文档: %+v", noDoc)
	}
}
