package objectstore_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// openDirBackend 构造并启用目录后端：Ensure 建根目录与 .tmp 并抢根目录锁，Close 在
// 用例结束时释放。
//
// 注册顺序是有讲究的：调用方先 t.TempDir()（清理先注册），这里再注册 Close，清理按 LIFO
// 执行，Close 一定先于删临时目录跑，Windows 上才不会因为锁句柄没放而删不掉根目录。
func openDirBackend(t *testing.T, root string, maxBytes int64) objectstore.Store {
	t.Helper()
	st, err := objectstore.NewDir(root, "", maxBytes)
	if err != nil {
		t.Fatalf("NewDir(%q): %v", root, err)
	}
	maintainer, ok := st.(objectstore.Maintainer)
	if !ok {
		t.Fatalf("dir backend must implement Maintainer, got %T", st)
	}
	if err := maintainer.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure(%q): %v", root, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// putDirObject 写入一个小对象并返回它的 Ref。
func putDirObject(t *testing.T, st objectstore.Store, category string, scope []string, ext, content string) objectstore.Ref {
	t.Helper()
	ref, err := st.Put(context.Background(), objectstore.Spec{
		Category: category, Scope: scope, Ext: ext,
	}, strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	return ref
}

// requireTempDirEmpty 断言 .tmp 里没有残留：Put 的每一条失败分支都必须自己收走临时文件，
// 否则长跑进程会把 PVC 塞满没人认领的半成品。
func requireTempDirEmpty(t *testing.T, root, when string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".tmp"))
	if err != nil {
		t.Fatalf("read temp dir %s: %v", when, err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("%s 之后 .tmp 残留 %v", when, names)
	}
}

// TestDirBackendContract 把 dir 后端注册进三后端共享的等价性用例集：dir 与 db/s3 必须
// 给出同一套语义，后端差异（media type 推导、幂等删除）单独在下面几个用例里钉住。
func TestDirBackendContract(t *testing.T) {
	const maxBytes = 1 << 20
	runContract(t, "dir", maxBytes, func(t *testing.T) objectstore.Store {
		t.Helper()
		// 每个子用例一个独立根目录：dir 后端要求单副本独占，共用一个根会互相抢锁。
		return openDirBackend(t, t.TempDir(), maxBytes)
	})
}

func TestDirNewDirValidation(t *testing.T) {
	ctx := context.Background()

	if _, err := objectstore.NewDir("  ", "", 0); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Errorf("NewDir 空 root = %v, want ErrKeyInvalid", err)
	}
	for _, prefix := range []string{"bad prefix", "../escape", "..", "k8sai/../other"} {
		if _, err := objectstore.NewDir(t.TempDir(), prefix, 0); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Errorf("NewDir prefix %q = %v, want ErrKeyInvalid", prefix, err)
		}
	}

	// NewDir 不许碰文件系统：配置校验阶段（还没决定要不要启用后端）不该产生目录副作用，
	// 建目录与抢锁统一推迟到 Ensure。
	root := filepath.Join(t.TempDir(), "fresh")
	st, err := objectstore.NewDir(root, "", 0)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NewDir 不应创建根目录，stat = %v", err)
	}
	if _, ok := st.(objectstore.Maintainer); !ok {
		t.Errorf("dir backend must implement Maintainer")
	}
	probe, ok := st.(objectstore.Probe)
	if !ok {
		t.Fatalf("dir backend must implement Probe")
	}
	// 未 Ensure 的根目录读不到，必须报「后端不可用」而不是「对象不存在」。
	if err := probe.Probe(ctx); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Errorf("Probe 未启用 = %v, want ErrUnavailable", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("未抢锁的 Close = %v, want nil", err)
	}
}

// TestDirEnsureLocksRootExclusively 钉住方案 15 §11.3 的部署前提：dir 后端只能单副本。
//
// 两个进程写同一个根目录时，「同内容同 key」的幂等假设会变成两个临时文件互相覆盖，GC
// 还会把对方刚写入的对象当成孤儿。所以第二个持有者必须在启动期就拿 ErrLocked 起不来，
// 而不是起来了悄悄写坏数据。
func TestDirEnsureLocksRootExclusively(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	first := openDirBackend(t, root, 1<<20)

	second, err := objectstore.NewDir(root, "", 1<<20)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	ensureErr := second.(objectstore.Maintainer).Ensure(ctx)
	if errors.Is(ensureErr, objectstore.ErrUnsupported) {
		t.Skipf("该平台没有建议锁原语: %v", ensureErr)
	}
	if !errors.Is(ensureErr, objectstore.ErrLocked) {
		t.Fatalf("第二个进程 Ensure = %v, want ErrLocked", ensureErr)
	}
	// 没抢到锁的一方不得留下半个持有者状态：Close 必须安全，且不能把第一个的锁一起放掉。
	if err := second.Close(); err != nil {
		t.Fatalf("未持锁的 Close = %v, want nil", err)
	}
	putDirObject(t, first, objectstore.CategoryReports, []string{"still-mine"}, "md", "# still writable")

	// Ensure 失败的后端也不能被当成「可用」：它的 Probe 与首个 Put 都要么可用要么明确失败。
	if _, err := second.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"intruder"}, Ext: "md",
	}, strings.NewReader("# nope")); err != nil && !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("未持锁后端的 Put = %v, want nil 或 ErrUnavailable", err)
	}
}

func TestDirEnsureIsIdempotentAndCloseReleasesLock(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := objectstore.NewDir(root, "", 1<<20)
	if err != nil {
		t.Fatalf("NewDir: %v", err)
	}
	maintainer := st.(objectstore.Maintainer)
	// 重复 Ensure 必须直接返回成功：装配路径与启动重试都可能再调一次，把它做成 ErrLocked
	// 会让「自己和自己抢锁」变成起不来。
	if err := maintainer.Ensure(ctx); err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	if err := maintainer.Ensure(ctx); err != nil {
		t.Fatalf("重复 Ensure = %v, want nil", err)
	}
	if err := maintainer.Ensure(ctx); err != nil {
		t.Fatalf("Ensure after 重入 = %v, want nil", err)
	}
	probe := st.(objectstore.Probe)
	if err := probe.Probe(ctx); err != nil {
		t.Fatalf("Probe after Ensure = %v, want nil", err)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("重复 Close = %v, want nil", err)
	}
	// 释放后同根目录必须能被重新启用：滚动重启时进程退出→新进程启动不能卡在读锁上。
	reopened := openDirBackend(t, root, 1<<20)
	if _, err := reopened.Stat(ctx, objectstore.DefaultPrefix+"/reports/absent/"+strings.Repeat("a", 64)+".md"); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("重开后 Stat = %v, want ErrNotFound", err)
	}
}

// TestDirMissingRootReportsUnavailable 钉住 §11.1 与 §12 最贵的一个误判：整块盘/整个目录
// 不见了必须报 ErrUnavailable。报成 ErrNotFound 会让下载路径把健康对象的元数据行标成
// missing，让 GC 把全部对象判成孤儿。
func TestDirMissingRootReportsUnavailable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openDirBackend(t, root, 1<<20)
	ref := putDirObject(t, st, objectstore.CategoryReports, []string{"cluster-1"}, "md", "# report")

	// 先 Close 放掉根目录锁句柄，再真的把根目录删掉，模拟「盘掉了/PVC 被回收」。
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove root: %v", err)
	}

	notFound := func(err error) bool { return errors.Is(err, objectstore.ErrNotFound) }
	if _, err := st.Stat(ctx, ref.Key); !errors.Is(err, objectstore.ErrUnavailable) || notFound(err) {
		t.Errorf("Stat = %v, want ErrUnavailable 且不得是 ErrNotFound", err)
	}
	if _, _, err := st.Get(ctx, ref.Key); !errors.Is(err, objectstore.ErrUnavailable) || notFound(err) {
		t.Errorf("Get = %v, want ErrUnavailable 且不得是 ErrNotFound", err)
	}
	if err := st.Delete(ctx, ref.Key); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Errorf("Delete = %v, want ErrUnavailable", err)
	}
	if _, err := st.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryExports, Scope: []string{"u"}, Ext: "csv",
	}, strings.NewReader("a,b\n")); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Errorf("Put = %v, want ErrUnavailable", err)
	}
	// List 更关键：把「读不到盘」当成「盘里没有对象」，GC 就会把全部对象判成孤儿。
	listed := 0
	err := st.List(ctx, objectstore.DefaultPrefix+"/", func(objectstore.Ref) error {
		listed++
		return nil
	})
	if !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("List = %v, want ErrUnavailable", err)
	}
	if listed != 0 {
		t.Fatalf("List 在根目录缺失时回调了 %d 次", listed)
	}
}

func TestDirFailedPutLeavesNoTempFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openDirBackend(t, root, 1<<20)
	spec := objectstore.Spec{Category: objectstore.CategoryExports, Scope: []string{"u-1"}, Ext: "csv"}

	if _, err := st.Put(ctx, spec, strings.NewReader(strings.Repeat("a", 1<<20+1))); !errors.Is(err, objectstore.ErrTooLarge) {
		t.Fatalf("超限 Put = %v, want ErrTooLarge", err)
	}
	requireTempDirEmpty(t, root, "超限")

	sourceErr := errors.New("source exploded")
	if _, err := st.Put(ctx, spec, errReader{err: sourceErr}); !errors.Is(err, sourceErr) {
		t.Fatalf("读失败 Put = %v, want %v", err, sourceErr)
	}
	requireTempDirEmpty(t, root, "读失败")

	// 非法 spec 必须在碰到第一个字节之前就拒掉：先拷正文再报 key 非法等于白写一遍盘。
	if _, err := st.Put(ctx, objectstore.Spec{Category: "secrets", Scope: []string{"u"}}, strings.NewReader("x")); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Fatalf("非法 category = %v, want ErrKeyInvalid", err)
	}
	if _, err := st.Put(ctx, objectstore.Spec{Category: objectstore.CategoryReports}, strings.NewReader("x")); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Fatalf("空 scope = %v, want ErrKeyInvalid", err)
	}
	if _, err := st.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"ok"}, Ext: "..conf",
	}, strings.NewReader("x")); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Fatalf("非法 ext = %v, want ErrKeyInvalid", err)
	}
	requireTempDirEmpty(t, root, "非法 spec")
}

// errReader 第一次读就失败，模拟上游 OSS/HTTP 流断掉。
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

// TestDirPutHonoursContextCancellation 验证长拷贝在客户端断开/超时时立刻停手：Put 的每轮
// 循环都查一次 ctx，而不是把 32MiB 拷完再报错，且停手之后不留半成品。
func TestDirPutHonoursContextCancellation(t *testing.T) {
	root := t.TempDir()
	st := openDirBackend(t, root, 32<<20)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := st.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryAttachments, Scope: []string{"u"}, Ext: "bin",
	}, &cancelAfterRead{cancel: cancel, payload: bytes.Repeat([]byte("x"), 4096)}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消中途的 Put = %v, want context.Canceled", err)
	}
	requireTempDirEmpty(t, root, "取消")
}

// cancelAfterRead 在第一轮读出数据后立刻取消 ctx：Put 的下一轮循环必须看见取消。
type cancelAfterRead struct {
	cancel  context.CancelFunc
	payload []byte
	sent    bool
}

func (r *cancelAfterRead) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.EOF
	}
	r.sent = true
	n := copy(p, r.payload)
	r.cancel()
	return n, nil
}

// TestDirRejectsTraversalKeys 是目录后端的穿越门禁：Get/Stat/Delete/List 每个入口都必须
// 在碰文件系统之前把非法 key 与非法前缀拒掉，报 ErrKeyInvalid（不是 ErrUnavailable，
// 否则攻击探测会污染存储健康指标）。
func TestDirRejectsTraversalKeys(t *testing.T) {
	ctx := context.Background()
	st := openDirBackend(t, t.TempDir(), 1<<20)
	hash := strings.Repeat("a", 64)
	badKeys := []string{
		"",
		"../etc/passwd",
		"k8sai/reports/../../etc/passwd",
		"k8sai/../windows/win.ini",
		"/k8sai/reports/" + hash,
		`k8sai\reports\` + hash,
		"k8sai/reports/C:" + hash,
		"k8sai/reports/%2e%2e/" + hash,
		"k8sai/secrets/" + hash,
		"k8sai/reports/" + strings.Repeat("A", 64) + ".md",
	}
	for _, key := range badKeys {
		if _, _, err := st.Get(ctx, key); !errors.Is(err, objectstore.ErrKeyInvalid) || errors.Is(err, objectstore.ErrUnavailable) {
			t.Errorf("Get(%q) = %v, want ErrKeyInvalid", key, err)
		}
		if _, err := st.Stat(ctx, key); !errors.Is(err, objectstore.ErrKeyInvalid) || errors.Is(err, objectstore.ErrUnavailable) {
			t.Errorf("Stat(%q) = %v, want ErrKeyInvalid", key, err)
		}
		if err := st.Delete(ctx, key); !errors.Is(err, objectstore.ErrKeyInvalid) || errors.Is(err, objectstore.ErrUnavailable) {
			t.Errorf("Delete(%q) = %v, want ErrKeyInvalid", key, err)
		}
	}
	for _, prefix := range []string{"", "../", "k8sai/../", "..", "/k8sai/", `k8sai\reports`} {
		if err := st.List(ctx, prefix, func(objectstore.Ref) error { return nil }); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Errorf("List(%q) = %v, want ErrKeyInvalid", prefix, err)
		}
	}
}

// TestDirListSkipsForeignFiles 钉住 GC 的扫描前提：List 只认「key 规则拼得出来的普通文件」。
// 崩溃残留的临时文件、别人塞进根目录的杂物、大写十六进制尾段一律不能进结果，否则 P8.9 的
// 差集会把它们当成孤儿对象删掉。
func TestDirListSkipsForeignFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openDirBackend(t, root, 1<<20)
	written := putDirObject(t, st, objectstore.CategoryReports, []string{"cluster-1"}, "md", "# keep me")

	foreign := map[string]string{
		".tmp/put-crashed-residue":                         "half written",
		"k8sai/reports/stray.txt":                          "别人放的杂物",
		"k8sai/reports/" + strings.Repeat("A", 64) + ".md": "大写摘要段",
		"k8sai/random/notes.md":                            "category 不在白名单",
	}
	for rel, content := range foreign {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	var listed []string
	if err := st.List(ctx, objectstore.DefaultPrefix+"/", func(ref objectstore.Ref) error {
		listed = append(listed, ref.Key)
		return nil
	}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0] != written.Key {
		t.Fatalf("List 结果 = %v, want 只有 %q", listed, written.Key)
	}

	// 锁文件是根目录里的常驻住户，永远不该被当成对象：直接按它自己的前缀列一次。
	// （不重写它的内容：Ensure 已经建好，重写会去动一个正被内核锁住的句柄。）
	lockObjects := 0
	if err := st.List(ctx, ".lock", func(objectstore.Ref) error {
		lockObjects++
		return nil
	}); err != nil {
		t.Fatalf("List(.lock): %v", err)
	}
	if lockObjects != 0 {
		t.Fatalf("List 把锁文件当成对象收录了 %d 条", lockObjects)
	}
}

// TestDirObjectPathOccupiedIsUnavailable 覆盖后端状态异常：合法 key 的路径上站着目录或
// 软链接时，那是「后端状态不对」而不是「对象不存在」——顺着 ErrNotFound 走下去，下载路径
// 会把健康的元数据行标成 missing（§7）。
func TestDirObjectPathOccupiedIsUnavailable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := openDirBackend(t, root, 1<<20)

	key, err := objectstore.BuildKey(objectstore.DefaultPrefix, objectstore.CategoryReports,
		[]string{"cluster-1"}, strings.Repeat("b", 64), ".md")
	if err != nil {
		t.Fatalf("BuildKey: %v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("占位目录: %v", err)
	}
	for _, op := range []struct {
		name string
		run  func() error
	}{
		{"Stat", func() error { _, err := st.Stat(ctx, key); return err }},
		{"Get", func() error { _, _, err := st.Get(ctx, key); return err }},
		{"Delete", func() error { return st.Delete(ctx, key) }},
	} {
		if err := op.run(); !errors.Is(err, objectstore.ErrUnavailable) || errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("%s(%s) = %v, want ErrUnavailable 且不得是 ErrNotFound", op.name, key, err)
		}
	}
	// 非普通文件也不许进 List：GC 不能拿它当孤儿对象去删。
	count := 0
	if err := st.List(ctx, objectstore.DefaultPrefix+"/", func(objectstore.Ref) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if count != 0 {
		t.Fatalf("List 收录了目录占位对象 %d 条", count)
	}

	// 软链接同理：正文只可能由 Put 的 rename 产生，顺着软链接读出去就是穿越。
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("清掉占位目录: %v", err)
	}
	outside := filepath.Join(root, "..", "outside-target.md")
	if err := os.WriteFile(outside, []byte("# outside"), 0o640); err != nil {
		t.Fatalf("写软链接目标: %v", err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("该平台不允许创建软链接: %v", err)
	}
	if _, err := st.Stat(ctx, key); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Errorf("软链接 Stat = %v, want ErrUnavailable", err)
	}
	if _, _, err := st.Get(ctx, key); !errors.Is(err, objectstore.ErrUnavailable) {
		t.Errorf("软链接 Get = %v, want ErrUnavailable", err)
	}
}

// TestDirMediaTypeComesFromExtension 记录 dir 后端的已知后端差异：除 key 之外不存任何旁路
// 元数据，因此 spec.MediaType 的声明值无处可存，Put 与 Stat 一律给扩展名推导值（db/s3 把
// 声明值存进元数据列）。差异允许，但同一后端内必须自洽，否则同一个报告的下载响应头会漂。
func TestDirMediaTypeComesFromExtension(t *testing.T) {
	ctx := context.Background()
	st := openDirBackend(t, t.TempDir(), 1<<20)
	const want = "text/markdown; charset=utf-8"

	put, err := st.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"u-5"}, Ext: "md", MediaType: "text/plain",
	}, strings.NewReader("# x\n"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if put.MediaType != want {
		t.Fatalf("Put media type = %q, want 扩展名推导值 %q", put.MediaType, want)
	}
	stat, err := st.Stat(ctx, put.Key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.MediaType != put.MediaType {
		t.Fatalf("media type 不自洽：Put %q vs Stat %q", put.MediaType, stat.MediaType)
	}
	body, ref, err := st.Get(ctx, put.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer body.Close()
	if ref.MediaType != want {
		t.Fatalf("Get media type = %q", ref.MediaType)
	}
}
