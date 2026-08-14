package diagnosis

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/llm"
	"github.com/k8s-ai/k8s-ai/internal/model"
)

// fakeLLM 脚本化响应/错误，用于诊断编排测试（线程安全，支持延迟模拟）。
type fakeLLM struct {
	mu        sync.Mutex
	responses []string
	errs      []error
	delay     time.Duration
	calls     int
	lastMsgs  []llm.Message
}

func (f *fakeLLM) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	f.mu.Lock()
	f.lastMsgs = req.Messages
	i := f.calls
	f.calls++
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	if i < len(f.responses) {
		return &llm.ChatResponse{Content: f.responses[i], Usage: llm.TokenUsage{TotalTokens: 10}}, nil
	}
	return nil, errors.New("no scripted response")
}

func diagOpts() model.LLMOptions {
	return model.LLMOptions{Enabled: true, MaxInputTokens: 8192, MaxTotalTokens: 32768, MaxFindings: 10}
}

func TestDiagnoseSuccess(t *testing.T) {
	f := &fakeLLM{responses: []string{validOutput}}
	ds, summary, err := New(diagOpts()).Diagnose(context.Background(), []model.Finding{testFinding()}, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || !ds[0].LLMUsed || ds[0].RootCause == "" {
		t.Fatalf("diagnoses = %+v", ds)
	}
	if ds[0].EvidenceChain[0] != "E1" {
		t.Fatalf("evidence chain = %v", ds[0].EvidenceChain)
	}
	if summary.Calls != 1 || summary.Failed != 0 || summary.Degraded != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	if len(f.lastMsgs) != 3 || !strings.Contains(f.lastMsgs[1].Content, "<untrusted_k8s_data>") {
		t.Fatalf("消息未走注入防护: %+v", f.lastMsgs)
	}
	if !strings.Contains(f.lastMsgs[2].Content, "rootCause") {
		t.Fatalf("缺少输出 Schema 指令: %+v", f.lastMsgs)
	}
}

func TestDiagnoseLLMFailure(t *testing.T) {
	f := &fakeLLM{errs: []error{errors.New("boom")}}
	ds, summary, err := New(diagOpts()).Diagnose(context.Background(), []model.Finding{testFinding()}, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].LLMUsed || !strings.Contains(ds[0].Error, "调用失败") {
		t.Fatalf("diagnoses = %+v", ds)
	}
	if summary.Calls != 1 || summary.Failed != 1 || summary.Degraded != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestDiagnoseRepairOnBadJSON(t *testing.T) {
	f := &fakeLLM{responses: []string{"not json", validOutput}}
	ds, summary, err := New(diagOpts()).Diagnose(context.Background(), []model.Finding{testFinding()}, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || !ds[0].LLMUsed || ds[0].RootCause == "" {
		t.Fatalf("修复重试应成功: %+v", ds)
	}
	if summary.Calls != 2 || summary.Degraded != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestDiagnoseAllFail(t *testing.T) {
	f := &fakeLLM{responses: []string{"bad", "still bad"}}
	ds, summary, err := New(diagOpts()).Diagnose(context.Background(), []model.Finding{testFinding()}, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 1 || ds[0].LLMUsed || !strings.Contains(ds[0].Error, "解析失败") {
		t.Fatalf("diagnoses = %+v", ds)
	}
	if summary.Calls != 2 || summary.Degraded != 1 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestDiagnoseContextCancel(t *testing.T) {
	f := &fakeLLM{responses: []string{validOutput}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := New(diagOpts()).Diagnose(ctx, []model.Finding{testFinding()}, f); err == nil {
		t.Fatal("ctx 取消应返回错误")
	}
}

// TestDiagnoseConcurrentOrder 验证并发送诊结果按入参顺序返回。
func TestDiagnoseConcurrentOrder(t *testing.T) {
	findings := []model.Finding{
		{ID: "f1", Rule: "R", Severity: model.SeverityCritical, Title: "a", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
		{ID: "f2", Rule: "R", Severity: model.SeverityHigh, Title: "b", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
		{ID: "f3", Rule: "R", Severity: model.SeverityMedium, Title: "c", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
	}
	f := &fakeLLM{responses: []string{validOutput, validOutput, validOutput}, delay: 30 * time.Millisecond}
	ds, summary, err := New(diagOpts()).Diagnose(context.Background(), findings, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 || ds[0].FindingID != "f1" || ds[1].FindingID != "f2" || ds[2].FindingID != "f3" {
		t.Fatalf("顺序异常: %+v", ds)
	}
	if summary.Calls != 3 || summary.Degraded != 0 {
		t.Fatalf("summary = %+v", summary)
	}
}

// TestDiagnoseDeadlineDegrades 验证整体超时后剩余问题降级而不是整次失败。
func TestDiagnoseDeadlineDegrades(t *testing.T) {
	findings := []model.Finding{
		{ID: "f1", Rule: "R", Severity: model.SeverityHigh, Title: "a", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
		{ID: "f2", Rule: "R", Severity: model.SeverityHigh, Title: "b", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
		{ID: "f3", Rule: "R", Severity: model.SeverityHigh, Title: "c", Evidence: []model.Evidence{{ID: "E1", Key: "k", Value: "v"}}},
	}
	f := &fakeLLM{responses: []string{validOutput, validOutput, validOutput}, delay: 500 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ds, summary, err := New(diagOpts()).Diagnose(ctx, findings, f)
	if err != nil {
		t.Fatalf("超时应降级而不是失败: %v", err)
	}
	if len(ds) != 3 {
		t.Fatalf("diagnoses = %d, want 3", len(ds))
	}
	for _, d := range ds {
		if d.LLMUsed || !strings.Contains(d.Error, "超时") {
			t.Fatalf("应降级并标注超时: %+v", d)
		}
	}
	if summary.Degraded != 3 {
		t.Fatalf("degraded = %d, want 3", summary.Degraded)
	}
}

// TestDegradedRuleBased 验证降级时基于证据生成初步判断并提取日志关键行。
func TestDegradedRuleBased(t *testing.T) {
	f := model.Finding{
		ID: "f1", Rule: "CrashLoopBackOff", Severity: model.SeverityHigh,
		Title:    "容器反复崩溃重启（CrashLoopBackOff）",
		Resource: model.ResourceRef{Kind: "Pod", Namespace: "agentops", Name: "app-0"},
		Evidence: []model.Evidence{
			{ID: "E1", Kind: model.EvObjectField, Key: "restartCount", Value: "1117"},
			{ID: "E2", Kind: model.EvObjectField, Key: "lastState.exitCode", Value: "2"},
			{ID: "E3", Kind: model.EvLog, Value: "INFO start\npanic: StartConsumer fail, topic not found\n"},
		},
	}
	d := degraded(f, "LLM 调用超时")
	if d.LLMUsed {
		t.Fatal("降级应标记 LLMUsed=false")
	}
	if !strings.Contains(d.RootCause, "规则判定") || !strings.Contains(d.RootCause, "restartCount=1117") {
		t.Fatalf("初步判断缺少规则依据: %q", d.RootCause)
	}
	if !strings.Contains(d.RootCause, "panic: StartConsumer fail") {
		t.Fatalf("未提取日志关键行: %q", d.RootCause)
	}
	if len(d.Investigation) != 2 || !strings.Contains(d.Investigation[0].Text, "-n agentops logs app-0") {
		t.Fatalf("排查命令缺失: %+v", d.Investigation)
	}
}
