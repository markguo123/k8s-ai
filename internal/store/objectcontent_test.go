package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

func newContentBackend(t *testing.T, s Store, maxBytes int64) objectstore.Store {
	t.Helper()
	c, err := s.ObjectContent(objectstore.DefaultPrefix, maxBytes)
	if err != nil {
		t.Fatalf("object content backend: %v", err)
	}
	return c
}

func TestObjectContentLimitClampsToMediumBlob(t *testing.T) {
	// 缺省值是 32MiB，比 MEDIUMBLOB 还大一档，因此缺省配置在 db 后端上一定会被收紧到
	// 16MiB-1：装配层据此打一条 WARN（要放大就换 dir/s3），而不是等到写入报 1406。
	for _, v := range []int64{0, -1, objectstore.DefaultMaxObjectBytes, 1 << 30} {
		if got := ObjectContentLimit(v); got != maxDBPayloadBytes {
			t.Errorf("ObjectContentLimit(%d) = %d, want %d", v, got, maxDBPayloadBytes)
		}
	}
	if got := ObjectContentLimit(4096); got != 4096 {
		t.Errorf("合理配置应原样生效，got %d", got)
	}
}

// TestObjectContentRefusesForeignDriver 守住「Put 不是搬迁」：同一个 key 的正文在桶里时，
// db 后端必须拒绝写入，否则改一次配置就会把对象内容偷换成库里的一份。
func TestObjectContentRefusesForeignDriver(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	content := []byte("lives in the bucket")
	sum := sha256Hex(content)
	scope := []string{"cluster-a"}
	key, err := objectstore.BuildKey(objectstore.DefaultPrefix, objectstore.CategoryReports, scope, sum, ".md")
	if err != nil {
		t.Fatalf("build key: %v", err)
	}
	if err := s.Objects().Register(ctx, &ObjectMeta{
		BlobKey: key, SHA256: sum, Size: int64(len(content)),
		MediaType: "text/markdown", Category: objectstore.CategoryReports, Driver: objectstore.DriverS3,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	backend := newContentBackend(t, s, 1<<20)
	_, err = backend.Put(ctx, objectstore.Spec{Category: objectstore.CategoryReports, Scope: scope, Ext: ".md"}, bytes.NewReader(content))
	if !errors.Is(err, objectstore.ErrState) {
		t.Fatalf("跨后端 Put = %v, want ErrState", err)
	}
	if _, _, err := backend.Get(ctx, key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("正文不在库里时 Get = %v, want ErrNotFound", err)
	}
}

// TestObjectContentDetectsCorruptPayload 制造库外改动（绕过应用直接 UPDATE 正文）：
// 这是 ErrChecksum 在 Put 之外唯一真实的来源，也是备份恢复只回来半张表时的检出口。
func TestObjectContentDetectsCorruptPayload(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	backend := newContentBackend(t, s, 1<<20)

	ref, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryAttachments, Scope: []string{"u-c", "s-c"}, Ext: ".txt",
	}, strings.NewReader("original bytes"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := rawDB(t, s).ExecContext(ctx, `UPDATE objects SET payload = ? WHERE blob_key = ?`,
		[]byte("tampered bytes"), ref.Key); err != nil {
		t.Fatalf("tamper payload: %v", err)
	}

	if _, err := backend.Stat(ctx, ref.Key); err != nil {
		t.Fatalf("Stat 只看元数据，不该被正文损坏影响：%v", err)
	}
	body, _, err := backend.Get(ctx, ref.Key)
	if !errors.Is(err, objectstore.ErrChecksum) {
		t.Fatalf("Get 必须查出正文损坏 = %v, want ErrChecksum", err)
	}
	if body != nil {
		body.Close()
	}
	// 未标记之前，同内容重传走快路径早退：热路径不做整段二次摘要校验，代价是坏正文原地
	// 留着。这条断言钉住「Put 不是万能修复」，别让运维以为重传一次就一定好了。
	if _, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryAttachments, Scope: []string{"u-c", "s-c"}, Ext: ".txt",
	}, strings.NewReader("original bytes")); err != nil {
		t.Fatalf("快路径重传: %v", err)
	}
	if _, _, err := backend.Get(ctx, ref.Key); !errors.Is(err, objectstore.ErrChecksum) {
		t.Fatalf("未标记 missing 时重传不该改正文 = %v, want ErrChecksum", err)
	}
	// 完整的修复链：Get 检出 → 对账把行标 missing → 重传按同内容重写正文并拉回 ready。
	if err := s.Objects().MarkMissing(ctx, []string{ref.Key}); err != nil {
		t.Fatalf("mark missing: %v", err)
	}
	if _, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryAttachments, Scope: []string{"u-c", "s-c"}, Ext: ".txt",
	}, strings.NewReader("original bytes")); err != nil {
		t.Fatalf("标记后重传应修复正文: %v", err)
	}
	body, _, err = backend.Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("修复后 Get: %v", err)
	}
	body.Close()
	if meta, err := s.Objects().Get(ctx, ref.Key); err != nil || meta.State != ObjectStateReady {
		t.Fatalf("修复后元数据 = %+v (%v)，期望 ready", meta, err)
	}
}

// TestObjectContentDeleteKeepsRowForDiagnostics 是 db 后端独有的语义：正文没了但行还在，
// download 路径据此把行标 missing、列表可见、下载 410。dir/s3 做不到，所以这份断言
// 只在这里测。
func TestObjectContentDeleteKeepsRowAndReuploadRestores(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	backend := newContentBackend(t, s, 1<<20)
	spec := objectstore.Spec{Category: objectstore.CategoryReports, Scope: []string{"c-del"}, Ext: ".md"}

	ref, err := backend.Put(ctx, spec, strings.NewReader("# gone"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := backend.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := backend.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("重复 delete = %v, want nil", err)
	}
	if _, _, err := backend.Get(ctx, ref.Key); !errors.Is(err, objectstore.ErrNotFound) {
		t.Fatalf("删正文后 Get = %v, want ErrNotFound", err)
	}
	meta, err := s.Objects().Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("元数据行必须保留: %v", err)
	}
	if meta.State != ObjectStateMissing || meta.DeletedAt == nil {
		t.Fatalf("删正文后元数据 = %+v，期望 missing 且记 deleted_at", meta)
	}

	listed := 0
	if err := backend.List(ctx, objectstore.DefaultPrefix+"/reports/", func(objectstore.Ref) error {
		listed++
		return nil
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if listed != 0 {
		t.Fatalf("List 只应列正文还在的行，得到 %d 条", listed)
	}

	if _, err := backend.Put(ctx, spec, strings.NewReader("# gone")); err != nil {
		t.Fatalf("宽限期内重新上传: %v", err)
	}
	if meta, err = s.Objects().Get(ctx, ref.Key); err != nil {
		t.Fatalf("get after restore: %v", err)
	}
	if meta.State != ObjectStateReady {
		t.Fatalf("补写正文后 state = %q, want %s", meta.State, ObjectStateReady)
	}
	body, _, err := backend.Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("恢复后 Get: %v", err)
	}
	body.Close()
}

func TestObjectContentRejectsUnnormalizedPrefix(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.ObjectContent("Bad Prefix", 0); err == nil {
		t.Fatal("非法前缀必须在装配期拒绝")
	}
	// 空前缀回落默认值：装配方漏配不该直接失败，但也不该把 key 拼成 "/reports/..."。
	backend, err := s.ObjectContent("", 0)
	if err != nil {
		t.Fatalf("空前缀应回落默认值: %v", err)
	}
	ref, err := backend.Put(context.Background(), objectstore.Spec{
		Category: objectstore.CategoryExports, Scope: []string{"u-p"}, Ext: ".csv",
	}, strings.NewReader("a,b\n"))
	if err != nil {
		t.Fatalf("put with default prefix: %v", err)
	}
	if !strings.HasPrefix(ref.Key, objectstore.DefaultPrefix+"/") {
		t.Fatalf("key %q 未落在默认前缀下", ref.Key)
	}
}

func TestObjectContentReportsMissingMigration(t *testing.T) {
	// 故意不跑 Migrate：启动期缺表必须把「去跑迁移」说清楚，而不是带病启动后在第一次
	// 上传时报一句看不懂的 no such table。
	s, err := OpenSQLite("file:objectcontent-nomigrate-" + randID() + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	backend := newContentBackend(t, s, 0)

	maintainer, ok := backend.(objectstore.Maintainer)
	if !ok {
		t.Fatal("db 后端应实现 Maintainer")
	}
	if err := maintainer.Ensure(context.Background()); err == nil || !strings.Contains(err.Error(), "db migrate") {
		t.Fatalf("Ensure 的错误要告诉运维跑迁移，got %v", err)
	}
	probe, ok := backend.(objectstore.Probe)
	if !ok {
		t.Fatal("db 后端应实现 Probe")
	}
	if err := probe.Probe(context.Background()); err == nil {
		t.Fatal("Probe 在表缺失时必须报错")
	}
}

// TestDBBackendIsNotPresigner 把「库里存的对象没有可签发的外部 URL」钉成断言：下载一律
// 走服务端流式端点，避免将来有人给 db 后端补一个假的签名地址（那等于把鉴权绕过一遍）。
func TestDBBackendIsNotPresigner(t *testing.T) {
	s := newTestStore(t)
	backend := newContentBackend(t, s, 0)
	if _, ok := backend.(objectstore.Presigner); ok {
		t.Fatal("db 后端不得实现 Presigner")
	}
	if backend.Driver() != objectstore.DriverDB {
		t.Fatalf("Driver = %q, want %q", backend.Driver(), objectstore.DriverDB)
	}
}
