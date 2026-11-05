package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// P8.9 的语义锁（方案 15 §9.3）。这批用例刻意用**真实的 dir 后端**而不是测试替身：
// GC 最贵的三类误判——把邻居的文件当孤儿删、把迁移中的对象当孤儿删、把「盘读不到」当
// 「盘里没有」——全都只在真后端 + 真目录布局上才暴露得出来。
//
// 宽限期取毫秒级：deleted_at 记的是 Release 那一刻，等一小会儿 cutoff 必然越过它。
// 存储层的时间精度即便削到秒，削出来的存量值只会更早，不会反过来让用例时好时坏。

const gcGrace = 20 * time.Millisecond

func gcWaitPastGrace() { time.Sleep(80 * time.Millisecond) }

func gcDirBackendAt(t *testing.T, root string) objectstore.Store {
	t.Helper()
	backend, err := objectstore.NewDir(root, objectstore.DefaultPrefix, 1<<20)
	if err != nil {
		t.Fatalf("new dir backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	m, ok := backend.(objectstore.Maintainer)
	if !ok {
		t.Fatal("dir backend must implement Maintainer")
	}
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("ensure dir backend: %v", err)
	}
	return backend
}

func gcDirBackend(t *testing.T) objectstore.Store {
	t.Helper()
	return gcDirBackendAt(t, t.TempDir())
}

// gcPutObject 走真实写入面：Put 内容 + 同事务 Acquire 建引用行。
func gcPutObject(t *testing.T, st store.Store, backend objectstore.Store, content string) objectstore.Ref {
	t.Helper()
	ctx := context.Background()
	ref, err := backend.Put(ctx, objectstore.Spec{
		Category:  objectstore.CategoryReports,
		Scope:     []string{"c1"},
		Ext:       ".md",
		CreatedBy: objectstore.SystemActor,
	}, strings.NewReader(content))
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	err = st.WithTx(ctx, func(tx *store.Tx) error {
		return tx.Acquire(ctx, ref, backend.Driver(), "alice")
	})
	if err != nil {
		t.Fatalf("acquire %s: %v", ref.Key, err)
	}
	return ref
}

// gcOrphan 释放引用：行进 orphaned 并落下 deleted_at，从这里开始算宽限期。
func gcOrphan(t *testing.T, st store.Store, key string) {
	t.Helper()
	ctx := context.Background()
	if err := st.WithTx(ctx, func(tx *store.Tx) error { return tx.Release(ctx, key) }); err != nil {
		t.Fatalf("release %s: %v", key, err)
	}
}

func gcMeta(t *testing.T, st store.Store, key string) *store.ObjectMeta {
	t.Helper()
	meta, err := st.Objects().Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get meta %s: %v", key, err)
	}
	return meta
}

func gcReadable(t *testing.T, backend objectstore.Store, key string) bool {
	t.Helper()
	rc, _, err := backend.Get(context.Background(), key)
	if errors.Is(err, objectstore.ErrNotFound) {
		return false
	}
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	_ = rc.Close()
	return true
}

// gcBrokenDelete 模拟「行删得掉、内容删不掉」的窗口：桶在两步之间抖了一下。
type gcBrokenDelete struct{ objectstore.Store }

func (gcBrokenDelete) Delete(context.Context, string) error {
	return fmt.Errorf("%w: simulated bucket outage", objectstore.ErrUnavailable)
}

// gcRevivingRegistry 在 GC 读完候选之后、认领之前插一次 Acquire，把对象复活。
// 这是「先 SELECT 再 UPDATE」那个窗口的可执行版本：CAS 没做好，这条用例立刻红。
type gcRevivingRegistry struct {
	store.ObjectStore
	st   store.Store
	done bool
}

func (r *gcRevivingRegistry) ListCollectable(ctx context.Context, olderThan time.Time) ([]*store.ObjectMeta, error) {
	rows, err := r.ObjectStore.ListCollectable(ctx, olderThan)
	if err != nil {
		return nil, err
	}
	if !r.done && len(rows) > 0 {
		r.done = true
		row := rows[0]
		if err := r.st.WithTx(ctx, func(tx *store.Tx) error {
			return tx.Acquire(ctx, row.Ref(), row.Driver, "alice")
		}); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func TestBlobGCRequiresPositiveGrace(t *testing.T) {
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	if _, err := NewBlobGC(nil, st.Objects()); err == nil {
		t.Error("GC without a content backend must be refused")
	}
	if _, err := NewBlobGC(backend, nil); err == nil {
		t.Error("GC without the object registry must be refused")
	}
	g, err := NewBlobGC(backend, st.Objects())
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	if _, err := g.Run(context.Background(), GCOptions{Prefix: objectstore.DefaultPrefix}); err == nil {
		t.Error("grace period 0 must be refused: the default value belongs to the config layer only")
	}
}

func TestBlobGCGracePeriodDecidesCollectability(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	g, err := NewBlobGC(backend, st.Objects())
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	ref := gcPutObject(t, st, backend, "crashlooping web pod")
	gcOrphan(t, st, ref.Key)

	// 宽限期未到：一行都不能动。168h 的生产默认值靠的就是这条判定。
	res, err := g.Run(ctx, GCOptions{Grace: 24 * time.Hour, Prefix: objectstore.DefaultPrefix})
	if err != nil {
		t.Fatalf("run GC: %v", err)
	}
	if res.Candidates != 0 || res.Claimed != 0 || res.RowsRemoved != 0 {
		t.Fatalf("object inside the grace period was collected: %+v", res)
	}
	if got := gcMeta(t, st, ref.Key).State; got != store.ObjectStateOrphaned {
		t.Fatalf("state inside grace = %s, want %s", got, store.ObjectStateOrphaned)
	}
	if !gcReadable(t, backend, ref.Key) {
		t.Fatal("content inside the grace period must still be downloadable")
	}

	gcWaitPastGrace()
	res, err = g.Run(ctx, GCOptions{Grace: gcGrace, Prefix: objectstore.DefaultPrefix})
	if err != nil {
		t.Fatalf("run GC after grace: %v", err)
	}
	if res.Candidates != 1 || res.Claimed != 1 || res.RowsRemoved != 1 || res.ContentDeleted != 1 {
		t.Fatalf("collect accounting = %+v, want 1/1/1/1", res)
	}
	if res.ReclaimedBytes != ref.Size {
		t.Fatalf("reclaimed %d bytes, want %d", res.ReclaimedBytes, ref.Size)
	}
	if _, err := st.Objects().Get(ctx, ref.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("registry row after collect: %v, want store.ErrNotFound", err)
	}
	if gcReadable(t, backend, ref.Key) {
		t.Fatal("content of a collected object must be gone")
	}
}

func TestBlobGCDryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	g, err := NewBlobGC(backend, st.Objects())
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	ref := gcPutObject(t, st, backend, "dry run candidate")
	// 桶里有内容而表里没有行 = 差集意义上的孤儿，dry-run 连它也只能报不能删。
	orphan, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"c2"}, Ext: ".md",
	}, strings.NewReader("row was lost by a crash"))
	if err != nil {
		t.Fatalf("put orphan: %v", err)
	}
	gcOrphan(t, st, ref.Key)
	gcWaitPastGrace()

	res, err := g.Run(ctx, GCOptions{Grace: gcGrace, Prefix: objectstore.DefaultPrefix, DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.Candidates != 1 || res.Orphans != 1 {
		t.Fatalf("dry-run counts = candidates %d orphans %d, want 1/1", res.Candidates, res.Orphans)
	}
	if res.Claimed != 0 || res.RowsRemoved != 0 || res.ContentDeleted != 0 || res.OrphansDel != 0 {
		t.Fatalf("dry run changed something: %+v", res)
	}
	if res.ReclaimedBytes != ref.Size || res.OrphanBytes != orphan.Size {
		t.Fatalf("dry-run byte accounting: %+v", res)
	}
	if got := gcMeta(t, st, ref.Key).State; got != store.ObjectStateOrphaned {
		t.Fatalf("dry run touched the row: state %s", got)
	}
	if !gcReadable(t, backend, ref.Key) || !gcReadable(t, backend, orphan.Key) {
		t.Fatal("dry run deleted content")
	}
}

// TestBlobGCRecoversFromCrashBetweenSteps 锁 §9.2 的失败补偿：进程在「行已忘掉」与
// 「内容已删」之间被杀，留下的必须是可回收的孤儿，而不是悬空引用。
func TestBlobGCRecoversFromCrashBetweenSteps(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	ref := gcPutObject(t, st, backend, "crash between row and content")
	gcOrphan(t, st, ref.Key)
	gcWaitPastGrace()

	broken, err := NewBlobGC(gcBrokenDelete{backend}, st.Objects())
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	if _, err := broken.Run(ctx, GCOptions{Grace: gcGrace, Prefix: objectstore.DefaultPrefix}); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("run with a dead bucket = %v, want objectstore.ErrUnavailable", err)
	}
	if _, err := st.Objects().Get(ctx, ref.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row after the crash: %v, want it forgotten", err)
	}
	if !gcReadable(t, backend, ref.Key) {
		t.Fatal("the crash must not have taken the content with it")
	}

	recovered, err := NewBlobGC(backend, st.Objects())
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	res, err := recovered.Run(ctx, GCOptions{Grace: gcGrace, Prefix: objectstore.DefaultPrefix})
	if err != nil {
		t.Fatalf("second round: %v", err)
	}
	if res.Orphans != 1 || res.OrphansDel != 1 || res.RowsRemoved != 0 {
		t.Fatalf("second round must close it out via the difference set: %+v", res)
	}
	if gcReadable(t, backend, ref.Key) {
		t.Fatal("orphan left by the crash was not reclaimed")
	}
}

func TestBlobGCLeavesRevivedObjectsAlone(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	ref := gcPutObject(t, st, backend, "referenced again mid-GC")
	gcOrphan(t, st, ref.Key)
	gcWaitPastGrace()

	g, err := NewBlobGC(backend, &gcRevivingRegistry{ObjectStore: st.Objects(), st: st})
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	res, err := g.Run(ctx, GCOptions{Grace: gcGrace, Prefix: objectstore.DefaultPrefix})
	if err != nil {
		t.Fatalf("run GC: %v", err)
	}
	if res.Candidates != 1 || res.Revived != 1 {
		t.Fatalf("counts = %+v, want candidates 1 revived 1", res)
	}
	if res.Claimed != 0 || res.RowsRemoved != 0 || res.ContentDeleted != 0 {
		t.Fatalf("a revived object was touched: %+v", res)
	}
	meta := gcMeta(t, st, ref.Key)
	if meta.State != store.ObjectStateReady || meta.RefCount != 1 {
		t.Fatalf("revived row = state %s ref %d, want ready/1", meta.State, meta.RefCount)
	}
	if !gcReadable(t, backend, ref.Key) {
		t.Fatal("revived object lost its content")
	}
}

// TestBlobGCLeavesObjectsOnOtherDriver 是 §9.3 #4 的正面断言：内容躺在别的后端里，
// 既不是孤儿（表里有行），也不是本轮能删的东西（本后端删不到它）。
func TestBlobGCLeavesObjectsOnOtherDriver(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend := gcDirBackend(t)

	content := "written before the cluster moved to dir"
	ref, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"c3"}, Ext: ".md",
	}, strings.NewReader(content))
	if err != nil {
		t.Fatalf("put object: %v", err)
	}
	if err := st.Objects().Register(ctx, &store.ObjectMeta{
		BlobKey: ref.Key, SHA256: ref.SHA256, Size: ref.Size, MediaType: ref.MediaType,
		Category: objectstore.CategoryReports, Driver: "s3", State: store.ObjectStateReady,
		CreatedBy: "alice",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := st.WithTx(ctx, func(tx *store.Tx) error { return tx.Acquire(ctx, ref, "s3", "alice") }); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	gcOrphan(t, st, ref.Key)
	gcWaitPastGrace()

	g, err := NewBlobGC(backend, st.Objects())
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	res, err := g.Run(ctx, GCOptions{Grace: gcGrace, Prefix: objectstore.DefaultPrefix})
	if err != nil {
		t.Fatalf("run GC: %v", err)
	}
	if res.Candidates != 1 || res.SkippedForeign != 1 {
		t.Fatalf("counts = %+v, want candidates 1 skipped-foreign 1", res)
	}
	if res.Claimed != 0 || res.RowsRemoved != 0 || res.Orphans != 0 || res.OrphansDel != 0 {
		t.Fatalf("an object owned by another driver was touched: %+v", res)
	}
	if got := gcMeta(t, st, ref.Key).State; got != store.ObjectStateOrphaned {
		t.Fatalf("row state = %s, want it left orphaned for blobs migrate", got)
	}
	if !gcReadable(t, backend, ref.Key) {
		t.Fatal("content of an un-migrated object must survive")
	}
}

// TestBlobGCCollectsMissingTombstones 收掉 §12.2 的尾巴：verify 判定内容已丢的行
// （state=missing 且无引用）必须还能被 GC 清掉，否则 objects 表只增不减。
func TestBlobGCCollectsMissingTombstones(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend := gcDirBackend(t)
	ref := gcPutObject(t, st, backend, "content lost before backup rotation")
	gcOrphan(t, st, ref.Key)
	if err := backend.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("simulate content loss: %v", err)
	}
	if err := st.Objects().MarkMissing(ctx, []string{ref.Key}); err != nil {
		t.Fatalf("mark missing: %v", err)
	}
	gcWaitPastGrace()

	g, err := NewBlobGC(backend, st.Objects())
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	res, err := g.Run(ctx, GCOptions{Grace: gcGrace, Prefix: objectstore.DefaultPrefix})
	if err != nil {
		t.Fatalf("run GC: %v", err)
	}
	if res.Candidates != 1 || res.Claimed != 1 || res.RowsRemoved != 1 {
		t.Fatalf("tombstone not collected: %+v", res)
	}
	if _, err := st.Objects().Get(ctx, ref.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("tombstone row still there: %v", err)
	}
}

// TestBlobGCDifferenceSetSparesSharedBucket 是共用桶的红线：别人的命名空间只报告，
// 一个字节都不许删（代价不对称——多留几个孤儿的成本远低于删掉邻居的文件）。
func TestBlobGCDifferenceSetSparesSharedBucket(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	// 共用同一个根/同一个桶时，邻居只会多出一段兄弟命名空间：aegisops-export/...
	// dir 后端的 List 走字符串前缀匹配（与 S3 同语义），所以它一定会被扫到。
	root := t.TempDir()
	backend := gcDirBackendAt(t, root)

	neighbor := filepath.Join(root, filepath.FromSlash("aegisops-export/reports/"+strings.Repeat("a", 64)+".md"))
	if err := os.MkdirAll(filepath.Dir(neighbor), 0o755); err != nil {
		t.Fatalf("create neighbour dir: %v", err)
	}
	if err := os.WriteFile(neighbor, []byte("another tool lives in this bucket too"), 0o644); err != nil {
		t.Fatalf("write neighbour object: %v", err)
	}

	gcWaitPastGrace()
	orphan, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"c4"}, Ext: ".md",
	}, strings.NewReader("our own orphan"))
	if err != nil {
		t.Fatalf("put orphan: %v", err)
	}

	g, err := NewBlobGC(backend, st.Objects())
	if err != nil {
		t.Fatalf("new GC: %v", err)
	}
	res, err := g.Run(ctx, GCOptions{Grace: gcGrace, Prefix: objectstore.DefaultPrefix})
	if err != nil {
		t.Fatalf("run GC: %v", err)
	}
	if res.BucketKeys != 2 || res.Orphans != 1 || res.OrphansDel != 1 {
		t.Fatalf("own orphan not reclaimed: %+v", res)
	}
	if res.Foreign != 1 || res.OtherRoot != 1 || res.RejectedKey != 0 {
		t.Fatalf("shared-bucket neighbour miscounted: %+v", res)
	}
	if gcReadable(t, backend, orphan.Key) {
		t.Fatal("our own orphan must be collected")
	}
	if _, err := os.Stat(neighbor); err != nil {
		t.Fatalf("GC deleted a neighbour object in a shared bucket: %v", err)
	}
}
