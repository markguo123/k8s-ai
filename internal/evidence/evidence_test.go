package evidence

import (
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// TestAssignIDs 验证按权重排序并稳定编号。
func TestAssignIDs(t *testing.T) {
	evs := []model.Evidence{
		{Kind: model.EvLog, Source: "logs/current", Key: "logs", Value: "log", Rank: RankLog},
		{Kind: model.EvEvent, Source: "Event/Pod/x/y", Key: "BackOff", Value: "msg", Rank: RankEvent},
		{Kind: model.EvObjectField, Source: "Pod/p/status", Key: "restartCount", Value: "3", Rank: RankObjectField},
	}
	out := AssignIDs(evs)
	if out[0].ID != "E1" || out[0].Kind != model.EvObjectField {
		t.Fatalf("排序异常: %+v", out)
	}
	if out[1].ID != "E2" || out[1].Kind != model.EvEvent {
		t.Fatalf("排序异常: %+v", out)
	}
	if out[2].ID != "E3" || out[2].Kind != model.EvLog {
		t.Fatalf("排序异常: %+v", out)
	}
}

// TestAssignIDsStable 验证同权重按 Source 稳定排序（map 遍历顺序不影响编号）。
func TestAssignIDsStable(t *testing.T) {
	a := AssignIDs([]model.Evidence{
		{Kind: model.EvLog, Source: "logs/previous", Value: "p", Rank: RankLog},
		{Kind: model.EvLog, Source: "logs/current", Value: "c", Rank: RankLog},
	})
	if a[0].Source != "logs/current" || a[1].Source != "logs/previous" {
		t.Fatalf("Source 排序异常: %+v", a)
	}
}

// TestLogEvidence 验证日志证据生成（current + previous）。
func TestLogEvidence(t *testing.T) {
	evs := Log(model.CollectedLog{Current: []byte("cur"), Previous: []byte("prev"), Truncated: true})
	if len(evs) != 2 {
		t.Fatalf("证据数 = %d, want 2", len(evs))
	}
	if !evs[0].Truncated {
		t.Fatal("应标记 Truncated")
	}
	only := Log(model.CollectedLog{Current: []byte("cur")})
	if len(only) != 1 {
		t.Fatalf("证据数 = %d, want 1", len(only))
	}
}

// TestTruncateValue 验证截断标记。
func TestTruncateValue(t *testing.T) {
	e := ObjectField("s", "k", strings.Repeat("x", 100))
	out := TruncateValue(e, 10)
	if !out.Truncated || len(out.Value) <= 10 {
		t.Fatalf("截断异常: %+v", out)
	}
	short := TruncateValue(ObjectField("s", "k", "hi"), 10)
	if short.Truncated {
		t.Fatal("短值不应标记截断")
	}
}

// TestKeyLogLineEmerg 验证 nginx [emerg] 也能被识别为关键行。
func TestKeyLogLineEmerg(t *testing.T) {
	got := KeyLogLine("INFO start\nnginx: [emerg] host not found in upstream\n")
	if !strings.Contains(got, "[emerg]") {
		t.Fatalf("KeyLogLine 未命中 emerg: %q", got)
	}
}
