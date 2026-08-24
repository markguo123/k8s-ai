package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/security"
)

// Recorder 是审计记录器。
type Recorder interface {
	// Write 追加一条事件（实现必须脱敏后落盘/输出）。
	Write(ctx context.Context, e Event) error
	Close() error
}

// redactor 统一对事件负载脱敏（Secret/Token/API Key 永不落审计）。
var redactor = security.NewRedactor()

// FileRecorder 把事件以 JSONL 追加写入文件（~/.k8s-ai/audit/<session>.jsonl）。
type FileRecorder struct {
	f *os.File
}

// NewFileRecorder 创建文件记录器（追加模式，目录不存在自动创建）。
func NewFileRecorder(path string) (*FileRecorder, error) {
	if dir := path[:len(path)-len(baseName(path))]; dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create audit dir: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open audit file: %w", err)
	}
	return &FileRecorder{f: f}, nil
}

// Write 序列化并脱敏后追加一行。
func (r *FileRecorder) Write(_ context.Context, e Event) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal audit event: %w", err)
	}
	line := redactor.Redact(string(raw))
	if _, err := r.f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

// Close 关闭文件。
func (r *FileRecorder) Close() error {
	return r.f.Close()
}

// LogRecorder 把事件输出到结构化日志（无文件场景的默认实现）。
type LogRecorder struct{}

// Write 以 slog 输出事件。
func (l *LogRecorder) Write(_ context.Context, e Event) error {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	slog.Info("audit", "action", e.Action, "payload", e.Payload)
	return nil
}

// Close 无操作。
func (l *LogRecorder) Close() error { return nil }

func baseName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[i+1:]
		}
	}
	return path
}
