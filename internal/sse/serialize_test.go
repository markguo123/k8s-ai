package sse

import (
	"strings"
	"testing"
)

func TestFrameIncludesEventAndData(t *testing.T) {
	e := New("s1", "t1", "st1", EventToolStarted, StepData{
		Kind: StepKindTool, Status: "running", Tool: "get_resources",
	})
	f := Frame(e)
	s := string(f)
	if !strings.Contains(s, "event: tool.started") {
		t.Errorf("帧缺少 event 行: %q", s)
	}
	// data 行以 data: 前缀、以空行收尾。
	if !strings.Contains(s, "data: {") {
		t.Errorf("帧缺少 data 行: %q", s)
	}
	if !strings.HasSuffix(s, "\n\n") {
		t.Errorf("帧未以空行收尾: %q", s)
	}
}

func TestFrameDecodeRoundTrip(t *testing.T) {
	e := New("s1", "t1", "st1", EventMessageDelta, MessageData{Role: "assistant", Delta: "多行\n内容"})
	got, err := DecodeFrame(Frame(e))
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != "s1" || got.TurnID != "t1" || got.StepID != "st1" || got.EventType != EventMessageDelta {
		t.Errorf("round-trip 字段不一致: %+v", got)
	}
	d := messageDataOf(got.Data)
	if d.Delta != "多行\n内容" {
		t.Errorf("多行 delta 未还原: %q", d.Delta)
	}
}

func TestDecodeBareJSON(t *testing.T) {
	e := New("s9", "", "", EventTurnStarted, TurnData{Status: "running"})
	raw, err := Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventType != EventTurnStarted || got.SessionID != "s9" {
		t.Errorf("裸 JSON 解析失败: %+v", got)
	}
}
