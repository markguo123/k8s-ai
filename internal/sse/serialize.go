package sse

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Marshal 返回统一事件的 JSON 编码（服务端广播负载，幂等可复用）。
func Marshal(e Event) ([]byte, error) {
	return json.Marshal(e)
}

// Frame 把统一事件序列化为完整 SSE 帧：
//
//	event: <event_type>
//	data: <json>
//	(空行)
//
// JSON 中可能包含多行字符串，逐行前缀 data: 保证 SSE 协议数据完整。
func Frame(e Event) []byte {
	raw, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	var b bytes.Buffer
	b.WriteString("event: ")
	b.WriteString(string(e.EventType))
	b.WriteString("\n")
	for _, line := range strings.Split(string(raw), "\n") {
		b.WriteString("data: ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.Bytes()
}

// MarshalLegacy 返回三期旧事件帧的 JSON 负载（与现有 SSE `data:` 线格式一致）。
func MarshalLegacy(l LegacyEvent) ([]byte, error) {
	return json.Marshal(l)
}

// Decode 从裸 JSON 解析统一事件。
func Decode(b []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return e, err
	}
	return e, nil
}

// DecodeFrame 从完整 SSE 帧（或裸 JSON）解析统一事件，供测试与诊断复用。
func DecodeFrame(b []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(b, &e); err == nil && e.EventType != "" {
		return e, nil
	}
	var dataLines []string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(dataLines) == 0 {
		return e, errors.New("sse: no data line in frame")
	}
	if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &e); err != nil {
		return e, err
	}
	return e, nil
}
