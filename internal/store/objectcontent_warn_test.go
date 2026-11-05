package store

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// TestDBBackendWarnsOnOversizedPayload 锁 P8.6 的「db 后端单行偏大 WARN」：硬闸门是
// max_object_size，这条只是提醒「该换 dir/s3 了」。它必须只在真的偏大时出声——每写一个
// 小对象都刷一条 WARN，等于把日志里真正有用的那两条淹掉。
func TestDBBackendWarnsOnOversizedPayload(t *testing.T) {
	restoreLimit := dbPayloadWarnBytes
	dbPayloadWarnBytes = 64
	t.Cleanup(func() { dbPayloadWarnBytes = restoreLimit })

	var buf bytes.Buffer
	restoreLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(restoreLogger) })

	ctx := context.Background()
	backend := newContentBackend(t, newTestStore(t), 1<<20)

	if _, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"u-warn", "scan-small"}, Ext: ".md",
	}, bytes.NewReader(bytes.Repeat([]byte("a"), 8))); err != nil {
		t.Fatalf("put small: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("阈值以下不该出声: %s", buf.String())
	}

	if _, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"u-warn", "scan-big"}, Ext: ".md",
	}, bytes.NewReader(bytes.Repeat([]byte("b"), 200))); err != nil {
		t.Fatalf("put large: %v", err)
	}
	logged := buf.String()
	for _, want := range []string{"db 后端仅作兼容兜底", "size_bytes=200", "warn_bytes=64"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("WARN 缺少 %q:\n%s", want, logged)
		}
	}
	// 点名替代后端，运维才知道下一步做什么（只说「太大了」等于没说）。
	if !strings.Contains(logged, "dir") || !strings.Contains(logged, "s3") {
		t.Fatalf("WARN 要给出改用 dir/s3 的指引:\n%s", logged)
	}
}
