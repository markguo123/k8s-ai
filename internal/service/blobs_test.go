package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// fakeBlobBackend 是对象存储后端的测试替身：可注入 Ensure/Probe/Get/Put 的失败，
// 并统计探测次数（节流与单飞的断言全靠它）。
type fakeBlobBackend struct {
	driver string

	mu          sync.Mutex
	ensureErr   error
	probeErr    error
	getErr      error
	putErr      error
	ensureCalls int
	probeCalls  int
	closeCalls  int
	gate        chan struct{} // 非 nil 时 Ensure 在此阻塞（单飞用例）
	data        map[string][]byte
}

func (b *fakeBlobBackend) Driver() string { return b.driver }

func (b *fakeBlobBackend) snapshot() (ensureErr, probeErr, getErr, putErr error, calls int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ensureErr, b.probeErr, b.getErr, b.putErr, b.ensureCalls
}

func (b *fakeBlobBackend) ensureCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ensureCalls
}

func (b *fakeBlobBackend) closeCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeCalls
}

func (b *fakeBlobBackend) setEnsureErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ensureErr = err
}

func (b *fakeBlobBackend) Ensure(ctx context.Context) error {
	b.mu.Lock()
	b.ensureCalls++
	err, gate := b.ensureErr, b.gate
	b.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (b *fakeBlobBackend) Probe(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probeCalls++
	return b.probeErr
}

func (b *fakeBlobBackend) Put(ctx context.Context, spec objectstore.Spec, r io.Reader) (objectstore.Ref, error) {
	b.mu.Lock()
	err := b.putErr
	b.mu.Unlock()
	if err != nil {
		return objectstore.Ref{}, err
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		return objectstore.Ref{}, err
	}
	sum := sha256.Sum256(raw)
	hexSum := hex.EncodeToString(sum[:])
	key, err := objectstore.BuildKey(objectstore.DefaultPrefix, spec.Category, spec.Scope, hexSum, spec.Ext)
	if err != nil {
		return objectstore.Ref{}, err
	}
	mt := objectstore.ResolveMediaType(spec.MediaType, spec.Ext)
	b.mu.Lock()
	if b.data == nil {
		b.data = map[string][]byte{}
	}
	b.data[key] = raw
	b.mu.Unlock()
	return objectstore.Ref{Key: key, SHA256: hexSum, Size: int64(len(raw)), MediaType: mt, UpdatedAt: time.Now().UTC()}, nil
}

func (b *fakeBlobBackend) Get(ctx context.Context, key string) (io.ReadCloser, objectstore.Ref, error) {
	b.mu.Lock()
	err, raw := b.getErr, b.data[key]
	b.mu.Unlock()
	if err != nil {
		return nil, objectstore.Ref{}, err
	}
	if raw == nil {
		return nil, objectstore.Ref{}, fmt.Errorf("%w: %s", objectstore.ErrNotFound, key)
	}
	return io.NopCloser(bytes.NewReader(raw)), objectstore.Ref{Key: key, Size: int64(len(raw))}, nil
}

func (b *fakeBlobBackend) Stat(ctx context.Context, key string) (objectstore.Ref, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.getErr != nil {
		return objectstore.Ref{}, b.getErr
	}
	raw, ok := b.data[key]
	if !ok {
		return objectstore.Ref{}, fmt.Errorf("%w: %s", objectstore.ErrNotFound, key)
	}
	return objectstore.Ref{Key: key, Size: int64(len(raw))}, nil
}

func (b *fakeBlobBackend) Delete(ctx context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.data, key)
	return nil
}

func (b *fakeBlobBackend) List(ctx context.Context, prefix string, fn func(objectstore.Ref) error) error {
	b.mu.Lock()
	keys := make([]string, 0, len(b.data))
	for k := range b.data {
		keys = append(keys, k)
	}
	b.mu.Unlock()
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			if err := fn(objectstore.Ref{Key: k}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *fakeBlobBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closeCalls++
	return nil
}

// fakePresignBackend 只多一个 Presigner：用它验证「presign 能力按后端能力探测」而不是按 driver 名字猜。
type fakePresignBackend struct{ *fakeBlobBackend }

func (b *fakePresignBackend) PresignGET(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return "https://blob.example/" + key + "?ttl=" + ttl.String(), nil
}

// fakeCounter 是对象统计的替身（健康端点的第二个数据源）。
type fakeCounter struct {
	stats []store.ObjectDriverStat
	err   error
}

func (c *fakeCounter) ObjectStats(ctx context.Context) ([]store.ObjectDriverStat, error) {
	return c.stats, c.err
}

// newClockGateway 造一个带可注入时钟的网关：节流窗口是 30 秒量级，
// 用真时钟测等于把测试写成白等半分钟。
func newClockGateway(b objectstore.Store, c ObjectCounter) (*BlobGateway, func(time.Duration)) {
	now := time.Unix(1_700_000_000, 0).UTC()
	gw := NewBlobGateway(b, c)
	gw.now = func() time.Time { return now }
	return gw, func(d time.Duration) { now = now.Add(d) }
}

func blobSpec() objectstore.Spec {
	return objectstore.Spec{Category: objectstore.CategoryReports, Scope: []string{"c1", "s1"}, Ext: ".md"}
}

// TestBlobGatewayDegradesThenRecovers 是 P8.5 两段式启动的行为面：桶坏时对象类调用
// 拿到原始哨兵错误（server 层据此回 503），桶修好之后**不重启进程**也能自动恢复。
func TestBlobGatewayDegradesThenRecovers(t *testing.T) {
	ctx := context.Background()
	b := &fakeBlobBackend{driver: objectstore.DriverDir,
		ensureErr: fmt.Errorf("%w: bucket unreachable", objectstore.ErrUnavailable)}
	gw, advance := newClockGateway(b, nil)

	if err := gw.EnsureReady(ctx); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("Ensure 失败应原样抛 ErrUnavailable，got %v", err)
	}
	if gw.Available() {
		t.Fatal("Ensure 未通过却报就绪")
	}
	if _, err := gw.Put(ctx, blobSpec(), strings.NewReader("x")); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("降级期 Put = %v，期望 ErrUnavailable", err)
	}
	h := gw.Health(ctx)
	if h.Status != BlobStatusDegraded || h.Ready || h.ProbeError == "" || h.Driver != objectstore.DriverDir {
		t.Fatalf("降级期健康 = %+v，期望 degraded 且带 probeError", h)
	}

	// 桶修好，但还在重试窗口内：不该每个请求都去探一次一个刚坏掉的桶。
	b.setEnsureErr(nil)
	advance(time.Second)
	if _, _, err := gw.Get(ctx, "k"); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("节流窗口内 Get = %v，期望仍按不可用处理", err)
	}
	if got := b.ensureCallCount(); got != 1 {
		t.Fatalf("节流窗口内探测了 %d 次，期望 1 次", got)
	}

	// 窗口过后自动恢复：不需要重启进程，也不需要在别处「手动重载」。
	advance(defaultBlobRetryInterval)
	ref, err := gw.Put(ctx, blobSpec(), strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("恢复后 Put 失败：%v", err)
	}
	if ref.Key == "" || ref.Size != 5 {
		t.Fatalf("Put 返回的 Ref 不完整：%+v", ref)
	}
	if !gw.Available() {
		t.Fatal("恢复后仍报未就绪")
	}
	if h := gw.Health(ctx); h.Status != BlobStatusReady || !h.Ready {
		t.Fatalf("恢复后健康 = %+v，期望 ready", h)
	}
}

// TestBlobGatewaySingleFlightEnsure 保证并发请求不会在同一个坏桶上排出一串超时。
func TestBlobGatewaySingleFlightEnsure(t *testing.T) {
	ctx := context.Background()
	gate := make(chan struct{})
	// Ensure 本身会成功：这样「谁真正跑了 Ensure」在结果里一目了然——只有它拿到 nil，
	// 其余请求立刻拿到"探测进行中"的不可用错误，而不是排队等一个坏后端。
	b := &fakeBlobBackend{driver: objectstore.DriverS3, gate: gate}
	gw, _ := newClockGateway(b, nil)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = gw.EnsureReady(ctx)
		}(i)
	}
	time.Sleep(80 * time.Millisecond) // 让协程都堆到单飞闸门上
	close(gate)
	wg.Wait()

	if got := b.ensureCallCount(); got != 1 {
		t.Fatalf("%d 个并发请求触发了 %d 次 Ensure，期望单飞 1 次", n, got)
	}
	var ok, unavailable int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, objectstore.ErrUnavailable):
			if !strings.Contains(err.Error(), "in flight") {
				t.Errorf("并发请求拿到的应是「探测进行中」的即时答复，got %v", err)
			}
			unavailable++
		default:
			t.Fatalf("并发 Ensure 返回了预期外的错误：%v", err)
		}
	}
	if ok != 1 || unavailable != n-1 {
		t.Fatalf("单飞下应有且仅有一个请求真正跑完 Ensure，got ok=%d unavailable=%d", ok, unavailable)
	}
}

// TestBlobGatewayOffIsDisabledNotFault：driver=off 是「没开这个功能」，
// 报成 degraded 会让每套关着对象存储的部署在健康页面上永久红一次。
func TestBlobGatewayOffIsDisabledNotFault(t *testing.T) {
	ctx := context.Background()
	gw, _ := newClockGateway(&fakeBlobBackend{driver: objectstore.DriverOff}, nil)

	if err := gw.EnsureReady(ctx); !errors.Is(err, objectstore.ErrDisabled) {
		t.Fatalf("off 后端 = %v，期望 ErrDisabled", err)
	}
	h := gw.Health(ctx)
	if h.Status != BlobStatusDisabled || h.Ready {
		t.Fatalf("off 健康 = %+v，期望 disabled", h)
	}
}

// TestBlobGatewayOnlyOutageErrorsFlipReadiness：ErrLocked（dir 抢不到锁）算故障，
// ErrNotFound 是后端的正常答复——把后者也当成故障，一次正常的对象缺失就会把
// 整个对象面打成 503，把「这份报告没了」变成「所有报告都读不了」。
func TestBlobGatewayOnlyOutageErrorsFlipReadiness(t *testing.T) {
	ctx := context.Background()

	locked := &fakeBlobBackend{driver: objectstore.DriverDir,
		putErr: fmt.Errorf("%w: held by another process", objectstore.ErrLocked)}
	gwLocked, _ := newClockGateway(locked, nil)
	if _, err := gwLocked.Put(ctx, blobSpec(), strings.NewReader("x")); !errors.Is(err, objectstore.ErrLocked) {
		t.Fatalf("Put = %v，期望 ErrLocked", err)
	}
	if gwLocked.Available() {
		t.Fatal("抢锁失败后仍报就绪，等于下一个请求还会去撞锁")
	}

	missing := &fakeBlobBackend{driver: objectstore.DriverDB,
		getErr: fmt.Errorf("%w: gone", objectstore.ErrNotFound)}
	gwMissing, _ := newClockGateway(missing, nil)
	if err := gwMissing.EnsureReady(ctx); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if _, _, err := gwMissing.Get(ctx, "k"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("Get = %v，期望 ErrNotFound", err)
	}
	if !gwMissing.Available() {
		t.Fatal("对象不存在被判成后端故障：一次 ErrNotFound 不该把对象面整体降级")
	}
}

// TestBlobGatewayPresignCapability：presign 只在后端真支持时才给，
// 且错误文案要把人指回 mode=stream，而不是让前端以为链接坏了。
func TestBlobGatewayPresignCapability(t *testing.T) {
	ctx := context.Background()

	gw, _ := newClockGateway(&fakeBlobBackend{driver: objectstore.DriverDB}, nil)
	url, err := gw.Presign(ctx, "some/key", time.Minute)
	if !errors.Is(err, objectstore.ErrUnsupported) {
		t.Fatalf("db 后端 presign = %v，期望 ErrUnsupported", err)
	}
	if url != "" || !strings.Contains(err.Error(), "mode=stream") {
		t.Fatalf("presign 错误文案应指回流式下载：%q %v", url, err)
	}

	gwS3, _ := newClockGateway(&fakePresignBackend{fakeBlobBackend: &fakeBlobBackend{driver: objectstore.DriverS3}}, nil)
	url, err = gwS3.Presign(ctx, "some/key", 5*time.Minute)
	if err != nil || !strings.Contains(url, "some/key") {
		t.Fatalf("s3 presign = %q (%v)，期望签名 URL", url, err)
	}
}

// TestBlobGatewayHealthCountsAndMismatch 对应方案 15 §10.3：换过 driver 没迁移时，
// 健康端点必须点名「有多少对象躺在别的后端」，否则历史报告读不到而无人知情。
func TestBlobGatewayHealthCountsAndMismatch(t *testing.T) {
	ctx := context.Background()
	counter := &fakeCounter{stats: []store.ObjectDriverStat{
		{Driver: objectstore.DriverDB, Objects: 2, Bytes: 100},
		{Driver: objectstore.DriverS3, Objects: 1, Bytes: 30},
	}}
	gw, _ := newClockGateway(&fakeBlobBackend{driver: objectstore.DriverDB}, counter)

	h := gw.Health(ctx)
	if h.Objects != 3 || h.Bytes != 130 {
		t.Fatalf("对象体量 = %d 个 %d 字节，期望合计 3 个 130 字节", h.Objects, h.Bytes)
	}
	if h.DriverMismatch[objectstore.DriverS3] != 1 {
		t.Fatalf("driverMismatch = %v，期望 s3=1", h.DriverMismatch)
	}
	if h.Status != BlobStatusReady {
		t.Fatalf("driver 不符不是故障，健康状态 = %q，期望 ready", h.Status)
	}

	// 统计失败折进 probeError，不编造数字：一个假的 0 比一句「统计不了」更容易误导决策。
	gwBroken, _ := newClockGateway(&fakeBlobBackend{driver: objectstore.DriverDB}, &fakeCounter{err: errors.New("db down")})
	broken := gwBroken.Health(ctx)
	if broken.Objects != 0 || broken.ProbeError == "" {
		t.Fatalf("统计失败时 = %+v，期望计数为 0 且 probeError 说明原因", broken)
	}
}

// TestBlobGatewayProbeFailureFlipsReadiness：响应报 degraded 而网关内部仍认为 ready，
// 比两边一起报 degraded 更难查——后续请求会跳过重试直接撞后端。
func TestBlobGatewayProbeFailureFlipsReadiness(t *testing.T) {
	ctx := context.Background()
	b := &fakeBlobBackend{driver: objectstore.DriverS3,
		probeErr: fmt.Errorf("%w: head bucket timeout", objectstore.ErrUnavailable)}
	gw, _ := newClockGateway(b, nil)
	if err := gw.EnsureReady(ctx); err != nil {
		t.Fatalf("EnsureReady: %v", err)
	}
	if !gw.Available() {
		t.Fatal("Ensure 成功后应就绪")
	}

	h := gw.Health(ctx)
	if h.Status != BlobStatusDegraded || h.Ready || h.ProbeError == "" {
		t.Fatalf("探针失败时健康 = %+v，期望 degraded 且带 probeError", h)
	}
	if gw.Available() {
		t.Fatal("探针失败后网关内部仍认为就绪")
	}
}

// TestBlobGatewayCloseReachesBackend：dir 后端的锁与文件句柄只在 Close 时释放，
// 网关不转调 Close 就会在重启期间留下一把没人认领的锁。
func TestBlobGatewayCloseReachesBackend(t *testing.T) {
	b := &fakeBlobBackend{driver: objectstore.DriverDir}
	gw, _ := newClockGateway(b, nil)
	if err := gw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if calls := b.closeCallCount(); calls != 1 {
		t.Fatalf("Close 触达后端 %d 次，期望 1 次", calls)
	}
}
