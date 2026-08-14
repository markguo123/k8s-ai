package scanner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/k8s-ai/k8s-ai/internal/model"
)

// fakeLogFetcher 模拟 GetPodLogs，支持错误注入、阻塞与并发计数。
type fakeLogFetcher struct {
	mu        sync.Mutex
	current   map[string][]byte
	previous  map[string][]byte
	err       error // current 日志的固定错误
	prevErr   error // previous 日志的固定错误
	block     chan struct{}
	active    int
	maxActive int
	calls     int
}

func (f *fakeLogFetcher) GetPodLogs(ctx context.Context, ns, pod, container string, opts model.LogOptions) ([]byte, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	key := ns + "/" + pod + "/" + container
	f.mu.Lock()
	defer f.mu.Unlock()
	if opts.Previous {
		if f.prevErr != nil {
			return nil, f.prevErr
		}
		return f.previous[key], nil
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.current[key], nil
}

func snapshotWithPod(ns, name, container string) *model.ClusterSnapshot {
	return &model.ClusterSnapshot{
		Pods: []model.PodInfo{{
			Ref:        model.ResourceRef{Kind: "Pod", Namespace: ns, Name: name},
			Containers: []model.ContainerInfo{{Name: container}},
		}},
	}
}

func phase2Opts() model.ScanOptions {
	return model.ScanOptions{
		CollectLogs:         true,
		CollectPreviousLogs: true,
		MaxLogLines:         500,
		MaxLogBytes:         64 * 1024,
		MaxLogLineBytes:     1024,
		PodLogsTimeout:      5 * time.Second,
	}
}

// TestPhase2FetchesCurrentAndPrevious 验证 current/previous 日志采集与脱敏。
func TestPhase2FetchesCurrentAndPrevious(t *testing.T) {
	snap := snapshotWithPod("default", "web-0", "web")
	f := &fakeLogFetcher{
		current:  map[string][]byte{"default/web-0/web": []byte("hello world\ntoken=abc123")},
		previous: map[string][]byte{"default/web-0/web": []byte("previous line")},
	}
	c := &collector{logs: f}
	targets := []model.ResourceRef{{Kind: "Pod", Namespace: "default", Name: "web-0"}}
	if err := c.Phase2(context.Background(), snap, targets, phase2Opts()); err != nil {
		t.Fatal(err)
	}
	cl := snap.Pods[0].Logs["web"]
	if !strings.Contains(string(cl.Current), "hello world") {
		t.Fatalf("current 日志缺失: %q", cl.Current)
	}
	if strings.Contains(string(cl.Current), "abc123") || !strings.Contains(string(cl.Current), "[REDACTED]") {
		t.Fatalf("current 日志未脱敏: %q", cl.Current)
	}
	if string(cl.Previous) != "previous line" {
		t.Fatalf("previous 日志 = %q", cl.Previous)
	}
}

// TestPhase2PreviousNotFound 验证 previous logs 不存在时静默跳过（P2.2）。
func TestPhase2PreviousNotFound(t *testing.T) {
	snap := snapshotWithPod("default", "web-0", "web")
	f := &fakeLogFetcher{
		current: map[string][]byte{"default/web-0/web": []byte("current")},
		prevErr: apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "web-0"),
	}
	c := &collector{logs: f}
	if err := c.Phase2(context.Background(), snap, []model.ResourceRef{{Kind: "Pod", Namespace: "default", Name: "web-0"}}, phase2Opts()); err != nil {
		t.Fatal(err)
	}
	cl := snap.Pods[0].Logs["web"]
	if cl.Previous != nil {
		t.Fatal("previous 应为 nil")
	}
	if cl.Error != "" {
		t.Fatalf("not-found 不应记错: %s", cl.Error)
	}
	if len(snap.CollectionErrors) != 0 {
		t.Fatalf("不应产生 collection errors: %+v", snap.CollectionErrors)
	}
}

// TestPhase2Timeout 验证单 Pod 日志超时会记录 collection error 而不是挂死。
func TestPhase2Timeout(t *testing.T) {
	snap := snapshotWithPod("default", "web-0", "web")
	f := &fakeLogFetcher{
		current: map[string][]byte{"default/web-0/web": []byte("x")},
		block:   make(chan struct{}),
	}
	opts := phase2Opts()
	opts.CollectPreviousLogs = false
	opts.PodLogsTimeout = 100 * time.Millisecond
	c := &collector{logs: f}
	if err := c.Phase2(context.Background(), snap, []model.ResourceRef{{Kind: "Pod", Namespace: "default", Name: "web-0"}}, opts); err != nil {
		t.Fatalf("根 ctx 未取消，不应返回错误: %v", err)
	}
	if len(snap.CollectionErrors) == 0 {
		t.Fatal("期望超时记入 collection errors")
	}
}

// TestPhase2ConcurrencyLimit 验证 Phase2 并发不超过配置值。
func TestPhase2ConcurrencyLimit(t *testing.T) {
	snap := &model.ClusterSnapshot{}
	var targets []model.ResourceRef
	current := map[string][]byte{}
	for i := 0; i < 6; i++ {
		name := "p" + string(rune('0'+i))
		snap.Pods = append(snap.Pods, model.PodInfo{
			Ref:        model.ResourceRef{Kind: "Pod", Namespace: "default", Name: name},
			Containers: []model.ContainerInfo{{Name: "web"}},
		})
		targets = append(targets, model.ResourceRef{Kind: "Pod", Namespace: "default", Name: name})
		current["default/"+name+"/web"] = []byte("log")
	}
	f := &fakeLogFetcher{current: current, block: make(chan struct{})}
	opts := phase2Opts()
	opts.CollectPreviousLogs = false
	opts.Phase2Concurrency = 2
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(f.block)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := (&collector{logs: f}).Phase2(ctx, snap, targets, opts); err != nil {
		t.Fatal(err)
	}
	if f.maxActive > 2 {
		t.Fatalf("并发 = %d, 超过限制 2", f.maxActive)
	}
}

// TestPhase2Disabled 验证 CollectLogs=false 时不发任何日志请求。
func TestPhase2Disabled(t *testing.T) {
	snap := snapshotWithPod("default", "web-0", "web")
	f := &fakeLogFetcher{current: map[string][]byte{}}
	opts := phase2Opts()
	opts.CollectLogs = false
	if err := (&collector{logs: f}).Phase2(context.Background(), snap, []model.ResourceRef{{Kind: "Pod", Namespace: "default", Name: "web-0"}}, opts); err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatalf("calls = %d, want 0", f.calls)
	}
}

// TestPhase2TargetMissing 验证 target 不在快照中时静默跳过。
func TestPhase2TargetMissing(t *testing.T) {
	snap := snapshotWithPod("default", "web-0", "web")
	f := &fakeLogFetcher{current: map[string][]byte{}}
	if err := (&collector{logs: f}).Phase2(context.Background(), snap, []model.ResourceRef{{Kind: "Pod", Namespace: "default", Name: "gone"}}, phase2Opts()); err != nil {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatalf("calls = %d, want 0", f.calls)
	}
}

// TestNormalizeRedactsSensitiveFields 验证采集边界脱敏（注解键与事件消息）。
func TestNormalizeRedactsSensitiveFields(t *testing.T) {
	p := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "web-0",
			Namespace:   "default",
			Annotations: map[string]string{"mysql-password": "s3cr3t"},
		},
	}
	info := normalizePod(p)
	if info.Annotations["mysql-password"] != "[REDACTED]" {
		t.Fatalf("注解敏感键未脱敏: %q", info.Annotations["mysql-password"])
	}
	e := normalizeEvent(corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "default"},
		Message:        "login failed password=hunter2",
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "default", Name: "web-0", UID: "u1"},
		FirstTimestamp: metav1.Time{Time: time.Now()},
		LastTimestamp:  metav1.Time{Time: time.Now()},
	})
	if strings.Contains(e.Message, "hunter2") || !strings.Contains(e.Message, "[REDACTED]") {
		t.Fatalf("事件消息未脱敏: %q", e.Message)
	}
}

// TestTruncateLogsScanner 验证行/字节上限（与 kubernetes 包同策略）。
func TestTruncateLogsScanner(t *testing.T) {
	raw := []byte("line-one\nline-two-is-long\nthree")
	out := truncateLogs(raw, 64*1024, 8)
	lines := strings.Split(string(out), "\n")
	if lines[0] != "line-one" || lines[1] != "line-two" || lines[2] != "three" {
		t.Fatalf("行截断异常: %q", out)
	}
	capped := truncateLogs(raw, 10, 1024)
	if string(capped) != "line-one\nl" {
		t.Fatalf("字节截断异常: %q", capped)
	}
}
