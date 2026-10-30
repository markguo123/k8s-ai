package objectstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// contractToken 给内存库 DSN 造唯一名字：同一包内多个用例共用一个内存库时，
// 名字重复会让后开的库直接复用前一个库的数据，断言就会互相污染。
var contractSeq atomic.Uint64

func contractToken() string {
	return strconv.FormatUint(contractSeq.Add(1), 36) + strconv.FormatInt(time.Now().UnixNano(), 36)
}

// runContract 是三后端（db/dir/s3）共用的等价性用例集：同一份语义只测一遍，后端各自
// 注册进来跑。新增后端只要在这里加一个 TestXxxContract，就不必重写断言，也不会出现
// 「db 后端测过的分支，s3 后端没测」。
//
// 这里刻意**不**断言两件事：
//   - 对不存在的 key 调 Delete 是 ErrNotFound 还是 nil（S3 的 DeleteObject 天生幂等，
//     强求多一次 HEAD 只是白花一次往返）；但重复删除同一个 key 必须幂等成功，GC 需要它。
//   - ErrUnavailable 的触发方式（只有 P8.4 的真机门禁能造出「桶不可达」）。
func runContract(t *testing.T, name string, maxBytes int64, newBackend func(*testing.T) objectstore.Store) {
	t.Helper()

	open := func(t *testing.T) objectstore.Store {
		t.Helper()
		st := newBackend(t)
		if st == nil {
			t.Fatal("backend factory returned nil")
		}
		return st
	}

	t.Run(name+" 写入返回自描述元数据", func(t *testing.T) {
		ctx := context.Background()
		st := open(t)
		content := []byte("contract payload")
		sum := sha256.Sum256(content)
		ref, err := st.Put(ctx, objectstore.Spec{
			Category: objectstore.CategoryReports,
			Scope:    []string{"cluster-1", "scan-1"},
			Ext:      "md",
		}, bytes.NewReader(content))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		if ref.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("Put digest = %s, want %s", ref.SHA256, hex.EncodeToString(sum[:]))
		}
		if ref.Size != int64(len(content)) {
			t.Fatalf("Put size = %d, want %d", ref.Size, len(content))
		}
		if ref.MediaType != "text/markdown; charset=utf-8" {
			t.Fatalf("Put media type = %q", ref.MediaType)
		}
		wantKey, err := objectstore.BuildKey(objectstore.DefaultPrefix, objectstore.CategoryReports,
			[]string{"cluster-1", "scan-1"}, ref.SHA256, ".md")
		if err != nil {
			t.Fatalf("BuildKey: %v", err)
		}
		if ref.Key != wantKey {
			t.Fatalf("Put key = %q, want %q（key 必须由实现端按实际摘要组装）", ref.Key, wantKey)
		}
		if err := objectstore.ValidateKey(ref.Key); err != nil {
			t.Fatalf("Put returned an invalid key: %v", err)
		}
	})

	t.Run(name+" 同内容幂等且异内容分key", func(t *testing.T) {
		ctx := context.Background()
		st := open(t)
		spec := objectstore.Spec{Category: objectstore.CategoryExports, Scope: []string{"u-1"}, Ext: ".json"}
		first, err := st.Put(ctx, spec, strings.NewReader("same bytes"))
		if err != nil {
			t.Fatalf("Put 1: %v", err)
		}
		second, err := st.Put(ctx, spec, strings.NewReader("same bytes"))
		if err != nil {
			t.Fatalf("Put 2: %v", err)
		}
		if first.Key != second.Key {
			t.Fatalf("同内容写出两个 key：%q vs %q", first.Key, second.Key)
		}
		other, err := st.Put(ctx, spec, strings.NewReader("other bytes"))
		if err != nil {
			t.Fatalf("Put 3: %v", err)
		}
		if other.Key == first.Key {
			t.Fatal("不同内容撞在同一个 key 上")
		}
	})

	t.Run(name+" Stat/Get 往返一致", func(t *testing.T) {
		ctx := context.Background()
		st := open(t)
		content := bytes.Repeat([]byte("payload-"), 5000)
		spec := objectstore.Spec{Category: objectstore.CategoryAttachments, Scope: []string{"u-2", "s-2"}, Ext: ".txt"}
		put, err := st.Put(ctx, spec, bytes.NewReader(content))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		stat, err := st.Stat(ctx, put.Key)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if stat.SHA256 != put.SHA256 || stat.Size != put.Size || stat.MediaType != put.MediaType {
			t.Fatalf("Stat %+v 与 Put %+v 不一致", stat, put)
		}
		body, ref, err := st.Get(ctx, put.Key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer body.Close()
		got, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("Get 读回 %d 字节，写入 %d 字节", len(got), len(content))
		}
		if ref.Key != put.Key || ref.SHA256 != put.SHA256 {
			t.Fatalf("Get ref %+v 与 Put %+v 不一致", ref, put)
		}
	})

	t.Run(name+" 未知 key 报 ErrNotFound", func(t *testing.T) {
		ctx := context.Background()
		st := open(t)
		// 结构合法但从未写入的 key：必须报「不存在」，不能报「不可达」，否则降级逻辑会把
		// 一次普通 404 当成后端故障。
		key := objectstore.DefaultPrefix + "/reports/absent/" + strings.Repeat("f", 64) + ".md"
		if _, err := st.Stat(ctx, key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("Stat = %v, want ErrNotFound", err)
		}
		if _, _, err := st.Get(ctx, key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Errorf("Get = %v, want ErrNotFound", err)
		}
	})

	t.Run(name+" 非法 key 与非法 spec 一律拒绝", func(t *testing.T) {
		ctx := context.Background()
		st := open(t)
		bad := "../etc/passwd"
		if _, _, err := st.Get(ctx, bad); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Errorf("Get(%q) = %v, want ErrKeyInvalid", bad, err)
		}
		if _, err := st.Stat(ctx, bad); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Errorf("Stat(%q) = %v, want ErrKeyInvalid", bad, err)
		}
		if err := st.Delete(ctx, bad); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Errorf("Delete(%q) = %v, want ErrKeyInvalid", bad, err)
		}
		if _, err := st.Put(ctx, objectstore.Spec{Category: "secrets", Scope: []string{"u"}}, strings.NewReader("x")); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Errorf("Put 白名单外 category = %v, want ErrKeyInvalid", err)
		}
		if _, err := st.Put(ctx, objectstore.Spec{Category: objectstore.CategoryReports}, strings.NewReader("x")); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Errorf("Put 空 scope = %v, want ErrKeyInvalid", err)
		}
	})

	t.Run(name+" 超限立即失败", func(t *testing.T) {
		ctx := context.Background()
		st := open(t)
		oversize := bytes.Repeat([]byte("a"), int(maxBytes)+1)
		_, err := st.Put(ctx, objectstore.Spec{Category: objectstore.CategoryExports, Scope: []string{"u-3"}}, bytes.NewReader(oversize))
		if !errors.Is(err, objectstore.ErrTooLarge) {
			t.Fatalf("Put 超限 = %v, want ErrTooLarge", err)
		}
	})

	t.Run(name+" 删除后读不到且重复删除幂等", func(t *testing.T) {
		ctx := context.Background()
		st := open(t)
		ref, err := st.Put(ctx, objectstore.Spec{Category: objectstore.CategoryReports, Scope: []string{"u-4"}, Ext: ".json"}, strings.NewReader("delete me"))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := st.Delete(ctx, ref.Key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, _, err := st.Get(ctx, ref.Key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Delete 后 Get = %v, want ErrNotFound", err)
		}
		if _, err := st.Stat(ctx, ref.Key); !errors.Is(err, objectstore.ErrNotFound) {
			t.Fatalf("Delete 后 Stat = %v, want ErrNotFound", err)
		}
		// GC 在「删内容」与「删元数据行」之间被杀，重跑必须能继续往下走。
		if err := st.Delete(ctx, ref.Key); err != nil {
			t.Fatalf("重复 Delete = %v, want nil", err)
		}
	})

	t.Run(name+" 前缀列举", func(t *testing.T) {
		ctx := context.Background()
		st := open(t)
		kept := map[string]bool{}
		for _, scope := range [][]string{{"tenant-a"}, {"tenant-b"}} {
			ref, err := st.Put(ctx, objectstore.Spec{Category: objectstore.CategoryExports, Scope: scope, Ext: ".csv"}, strings.NewReader("a,b\n1,2\n"))
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			kept[ref.Key] = true
		}
		other, err := st.Put(ctx, objectstore.Spec{Category: objectstore.CategoryReports, Scope: []string{"tenant-a"}, Ext: ".md"}, strings.NewReader("# report"))
		if err != nil {
			t.Fatalf("Put other: %v", err)
		}

		var listed []string
		if err := st.List(ctx, objectstore.DefaultPrefix+"/exports/", func(ref objectstore.Ref) error {
			listed = append(listed, ref.Key)
			return nil
		}); err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(listed) != len(kept) {
			t.Fatalf("List 返回 %d 条，期望 %d 条：%v", len(listed), len(kept), listed)
		}
		for _, key := range listed {
			if key == other.Key {
				t.Fatalf("List 前缀 %q 泄漏到了其它 category 的对象 %q", objectstore.DefaultPrefix+"/exports/", key)
			}
			if !kept[key] {
				t.Fatalf("List 混入了未写入的 key %q", key)
			}
		}

		// 前缀必须真的起到过滤作用：GC 的孤儿判定全靠它，漏一条就是把别人的对象当孤儿。
		single := 0
		if err := st.List(ctx, objectstore.DefaultPrefix+"/exports/tenant-a/", func(objectstore.Ref) error {
			single++
			return nil
		}); err != nil {
			t.Fatalf("List 子前缀: %v", err)
		}
		if single != 1 {
			t.Fatalf("子前缀列举命中 %d 条，期望 1 条", single)
		}

		sentinel := errors.New("stop listing")
		err = st.List(ctx, objectstore.DefaultPrefix+"/", func(objectstore.Ref) error { return sentinel })
		if !errors.Is(err, sentinel) {
			t.Fatalf("回调错误必须中止 List，got %v", err)
		}
		if err := st.List(ctx, "../", func(objectstore.Ref) error { return nil }); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Fatalf("List 穿越前缀 = %v, want ErrKeyInvalid", err)
		}
		if err := st.List(ctx, "", func(objectstore.Ref) error { return nil }); !errors.Is(err, objectstore.ErrKeyInvalid) {
			t.Fatalf("List 空前缀 = %v, want ErrKeyInvalid", err)
		}
	})
}

// TestDBBackendContract 跑 driver=db 后端（正文在 objects.payload）。
// dir/s3 后端在 P8.3/P8.4 各自加一个 TestXxxContract 注册进同一套用例。
func TestDBBackendContract(t *testing.T) {
	const maxBytes = 1 << 20
	runContract(t, "db", maxBytes, func(t *testing.T) objectstore.Store {
		t.Helper()
		dsn := "file:objectstore-contract-" + contractToken() + "?mode=memory&cache=shared"
		st, err := store.OpenSQLite(dsn)
		if err != nil {
			t.Fatalf("open sqlite: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		if err := st.Migrate(context.Background()); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		content, err := st.ObjectContent(objectstore.DefaultPrefix, maxBytes)
		if err != nil {
			t.Fatalf("object content: %v", err)
		}
		return content
	})
}
