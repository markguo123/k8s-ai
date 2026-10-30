package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// objTestMeta 造一行合法的对象元数据。key 一律经 BuildKey 生成、sha 用真实摘要：
// 否则测试会先撞在 key 校验闸门上，把「业务分支没走到」误报成「业务分支错了」。
func objTestMeta(t *testing.T, category string, scope []string, ext, seed string) *ObjectMeta {
	t.Helper()
	sum := sha256Hex([]byte(seed))
	key, err := objectstore.BuildKey(objectstore.DefaultPrefix, category, scope, sum, ext)
	if err != nil {
		t.Fatalf("build key: %v", err)
	}
	return &ObjectMeta{
		BlobKey:   key,
		SHA256:    sum,
		Size:      int64(len(seed)),
		Category:  category,
		Driver:    objectstore.DriverDB,
		State:     ObjectStateReady,
		RefCount:  1,
		CreatedBy: objectstore.SystemActor,
	}
}

func objTestRef(t *testing.T, category string, scope []string, ext, seed string) objectstore.Ref {
	t.Helper()
	m := objTestMeta(t, category, scope, ext, seed)
	return m.Ref()
}

func TestObjectRegisterDoesNotResetReferences(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m := objTestMeta(t, objectstore.CategoryReports, []string{"c1"}, ".md", "seed-a")
	m.RefCount = 2
	if err := s.Objects().Register(ctx, m); err != nil {
		t.Fatalf("register: %v", err)
	}
	first, err := s.Objects().Get(ctx, m.BlobKey)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// 幂等重放：体积/媒体类型要更新，ref_count / state / created_at 一个都不许动。
	// 这条断言守的是「Put 重放把已挂引用的对象打回孤儿」——引用计数方案最恶性的一类缺陷。
	replay := &ObjectMeta{
		BlobKey:   m.BlobKey,
		SHA256:    m.SHA256,
		Size:      999,
		MediaType: "text/markdown; charset=utf-8",
		Driver:    objectstore.DriverDB,
		RefCount:  0,
		State:     ObjectStateOrphaned,
		CreatedAt: first.CreatedAt.Add(24 * time.Hour),
	}
	if err := s.Objects().Register(ctx, replay); err != nil {
		t.Fatalf("replay register: %v", err)
	}
	got, err := s.Objects().Get(ctx, m.BlobKey)
	if err != nil {
		t.Fatalf("get after replay: %v", err)
	}
	if got.Size != 999 || got.MediaType != replay.MediaType {
		t.Fatalf("重放未更新可变量：size=%d media=%q", got.Size, got.MediaType)
	}
	if got.RefCount != 2 {
		t.Fatalf("重放把 ref_count 重置成 %d，期望保持 2", got.RefCount)
	}
	if got.State != ObjectStateReady {
		t.Fatalf("重放把 state 改成 %q，期望保持 %s", got.State, ObjectStateReady)
	}
	if !got.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("重放把 created_at 改成 %v，期望保持 %v", got.CreatedAt, first.CreatedAt)
	}
}

func TestObjectRegisterValidatesInput(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	objs := s.Objects()

	if err := objs.Register(ctx, nil); err == nil {
		t.Error("nil 元数据必须报错")
	}
	badKey := objTestMeta(t, objectstore.CategoryReports, []string{"c"}, ".md", "k")
	badKey.BlobKey = "../escape/" + badKey.SHA256
	if err := objs.Register(ctx, badKey); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Errorf("非法 key = %v, want ErrKeyInvalid", err)
	}
	negative := objTestMeta(t, objectstore.CategoryReports, []string{"neg"}, ".md", "n")
	negative.RefCount = -1
	if err := objs.Register(ctx, negative); err == nil {
		t.Error("负 ref_count 必须报错：计数变负说明调用方漏配对")
	}
	unknownState := objTestMeta(t, objectstore.CategoryReports, []string{"st"}, ".md", "s")
	unknownState.State = "recycling"
	if err := objs.Register(ctx, unknownState); err == nil {
		t.Error("未知 state 必须报错")
	}
}

// TestObjectReferenceCountLifecycle 走完整条引用链：两次 Acquire → 两次 Release（归零
// 转孤儿）→ 再 Acquire 复活。中间每一步都同时校验 state 与 deleted_at，因为 GC 的宽限期
// 判定只看这两个字段。
func TestObjectReferenceCountLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ref := objTestRef(t, objectstore.CategoryAttachments, []string{"u1", "s1"}, ".txt", "shared bytes")

	load := func() *ObjectMeta {
		t.Helper()
		m, err := s.Objects().Get(ctx, ref.Key)
		if err != nil {
			t.Fatalf("get %s: %v", ref.Key, err)
		}
		return m
	}
	acquire := func() {
		t.Helper()
		if err := s.WithTx(ctx, func(tx *Tx) error { return tx.Acquire(ctx, ref, objectstore.DriverDB, "tester") }); err != nil {
			t.Fatalf("acquire: %v", err)
		}
	}
	release := func() {
		t.Helper()
		if err := s.WithTx(ctx, func(tx *Tx) error { return tx.Release(ctx, ref.Key) }); err != nil {
			t.Fatalf("release: %v", err)
		}
	}

	acquire()
	got := load()
	if got.RefCount != 1 || got.State != ObjectStateReady || got.DeletedAt != nil {
		t.Fatalf("首次 Acquire 后 %+v，期望 ref=1 state=ready deleted_at=nil", got)
	}
	if got.CreatedBy != "tester" || got.Driver != objectstore.DriverDB {
		t.Fatalf("插入新行时未记录归属/后端：%+v", got)
	}

	acquire()
	if got = load(); got.RefCount != 2 {
		t.Fatalf("两次 Acquire 后 ref_count=%d，期望 2", got.RefCount)
	}

	release()
	if got = load(); got.RefCount != 1 || got.State != ObjectStateReady {
		t.Fatalf("一次 Release 后 %+v，期望 ref=1 且仍 ready（还有引用不能进宽限期）", got)
	}

	release()
	if got = load(); got.RefCount != 0 || got.State != ObjectStateOrphaned || got.DeletedAt == nil {
		t.Fatalf("引用归零后 %+v，期望 ref=0 state=orphaned 且记 deleted_at", got)
	}

	// 宽限期内第二次上传同一内容：孤儿必须复活，否则 GC 会把新挂上引用的对象删掉。
	acquire()
	if got = load(); got.RefCount != 1 || got.State != ObjectStateReady || got.DeletedAt != nil {
		t.Fatalf("孤儿复活后 %+v，期望 ref=1 state=ready deleted_at=nil", got)
	}
}

func TestAcquireRejectsDeletingObject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := objTestMeta(t, objectstore.CategoryExports, []string{"u-del"}, ".csv", "collect me")
	past := time.Now().Add(-time.Hour)
	m.State = ObjectStateOrphaned
	m.DeletedAt = &past
	if err := s.Objects().Register(ctx, m); err != nil {
		t.Fatalf("register: %v", err)
	}
	claimed, err := s.Objects().ClaimDeleting(ctx, m.BlobKey, m.RefCount, time.Now())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("orphaned + ref_count 相符 + 过宽限期，认领应成功")
	}

	ref := m.Ref()
	want := s.WithTx(ctx, func(tx *Tx) error { return tx.Acquire(ctx, ref, objectstore.DriverDB, "tester") })
	if !errors.Is(want, objectstore.ErrState) {
		t.Fatalf("对 deleting 对象 Acquire = %v, want ErrState", want)
	}
	after, err := s.Objects().Get(ctx, m.BlobKey)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.RefCount != m.RefCount || after.State != ObjectStateDeleting {
		t.Fatalf("被拒的 Acquire 留下了副作用：%+v", after)
	}
}

func TestClaimDeletingGuardConditions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	objs := s.Objects()
	now := time.Now()
	past := now.Add(-time.Hour)

	// 认领条件三选二不满足就必须跳过：GC 与 Acquire 并发时，这正是「不许丢引用」的那道闸。
	cases := []struct {
		name      string
		state     string
		deleted   *time.Time
		refCount  int64
		olderThan time.Time
	}{
		{"ref_count 已变", ObjectStateOrphaned, &past, 7, now},
		{"还在被引用", ObjectStateReady, &past, 0, now},
		{"宽限期未满", ObjectStateOrphaned, &now, 0, now.Add(-time.Minute)},
	}
	for i, tc := range cases {
		m := objTestMeta(t, objectstore.CategoryExports, []string{"guard-" + string(rune('a'+i))}, ".csv", tc.name)
		m.State = tc.state
		m.DeletedAt = tc.deleted
		m.RefCount = 0
		if err := objs.Register(ctx, m); err != nil {
			t.Fatalf("%s: register: %v", tc.name, err)
		}
		claimed, err := objs.ClaimDeleting(ctx, m.BlobKey, tc.refCount, tc.olderThan)
		if err != nil {
			t.Fatalf("%s: claim: %v", tc.name, err)
		}
		if claimed {
			t.Errorf("%s: 不满足认领条件却置成了 deleting", tc.name)
		}
		if got, err := objs.Get(ctx, m.BlobKey); err != nil || got.State != tc.state {
			t.Errorf("%s: 失败认领改动了 state：%+v %v", tc.name, got, err)
		}
	}

	if _, err := objs.ClaimDeleting(ctx, "not/a/key", 0, now); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Errorf("非法 key = %v, want ErrKeyInvalid", err)
	}
}

func TestListCollectableHonoursGracePeriod(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// 边界留出小时级余量：宽限期本来就是小时量级，测试若贴着 now 比大小，就把时间精度
	// 当成了被测语义（两驱动的 TIMESTAMP 精度并不完全一致）。
	now := time.Now()
	old := objTestMeta(t, objectstore.CategoryExports, []string{"old"}, ".csv", "old orphan")
	longAgo := now.Add(-72 * time.Hour)
	old.State, old.DeletedAt = ObjectStateOrphaned, &longAgo
	fresh := objTestMeta(t, objectstore.CategoryExports, []string{"fresh"}, ".csv", "fresh orphan")
	justNow := now
	fresh.State, fresh.DeletedAt = ObjectStateOrphaned, &justNow
	for _, m := range []*ObjectMeta{old, fresh} {
		if err := s.Objects().Register(ctx, m); err != nil {
			t.Fatalf("register %s: %v", m.BlobKey, err)
		}
	}

	// 模拟 gc_grace_period=24h：只有三天前逻辑删除的对象进入候选。
	got, err := s.Objects().ListCollectable(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("list collectable: %v", err)
	}
	if len(got) != 1 || got[0].BlobKey != old.BlobKey {
		t.Fatalf("GC 候选 = %v，期望只有过了宽限期的那一个", keysOf(got))
	}
	// 反向确认：放宽到 7 天就两个都在，说明过滤真的来自时间而不是别的原因。
	if all, err := s.Objects().ListCollectable(ctx, now.Add(24*time.Hour)); err != nil || len(all) != 2 {
		t.Fatalf("超过宽限期后候选 = %v (%v)，期望 2 条", keysOf(all), err)
	}
}

func TestListByCategory(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	report := objTestMeta(t, objectstore.CategoryReports, []string{"c1"}, ".md", "rpt")
	attach := objTestMeta(t, objectstore.CategoryAttachments, []string{"u1"}, ".txt", "att")
	for _, m := range []*ObjectMeta{report, attach} {
		if err := s.Objects().Register(ctx, m); err != nil {
			t.Fatalf("register: %v", err)
		}
	}

	only, err := s.Objects().ListByCategory(ctx, objectstore.CategoryReports)
	if err != nil {
		t.Fatalf("list by category: %v", err)
	}
	if len(only) != 1 || only[0].BlobKey != report.BlobKey {
		t.Fatalf("按 category 列举 = %v", keysOf(only))
	}
	if only[0].Category != objectstore.CategoryReports {
		t.Fatalf("Register 未按 key 推导 category：%+v", only[0])
	}
	all, err := s.Objects().ListByCategory(ctx, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("空 category 应列出全部，得到 %v", keysOf(all))
	}
	if _, err := s.Objects().ListByCategory(ctx, "secrets"); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Errorf("白名单外 category = %v, want ErrKeyInvalid", err)
	}
}

func TestMarkMissingIsRepeatable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	objs := s.Objects()

	m := objTestMeta(t, objectstore.CategoryReports, []string{"c-mm"}, ".md", "missing me")
	if err := objs.Register(ctx, m); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := objs.MarkMissing(ctx, []string{m.BlobKey}); err != nil {
		t.Fatalf("mark missing: %v", err)
	}
	if got, err := objs.Get(ctx, m.BlobKey); err != nil || got.State != ObjectStateMissing {
		t.Fatalf("标记后 = %+v (%v)，期望 missing", got, err)
	}
	// verify 是可重跑的：同一批 key 再跑一遍不能报错，否则整轮对账卡在已处理过的对象上。
	if err := objs.MarkMissing(ctx, []string{m.BlobKey}); err != nil {
		t.Errorf("重复标记 = %v, want nil", err)
	}
	if err := objs.MarkMissing(ctx, []string{objectstore.DefaultPrefix + "/reports/absent/" + m.SHA256}); !errors.Is(err, ErrNotFound) {
		t.Errorf("未知 key = %v, want ErrNotFound", err)
	}
}

// TestForgetCOnlyDeletingRows 走真实路径进 deleting：Release 归零转孤儿 → ClaimDeleting
// 认领 → Forget 清行。Register 在冲突时不改 state（见 DoesNotResetReferences），所以这里
// 不能靠重复登记把行推进状态机。
func TestForgetCOnlyDeletingRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	objs := s.Objects()

	ready := objTestMeta(t, objectstore.CategoryExports, []string{"keep"}, ".csv", "still referenced")
	doomed := objTestMeta(t, objectstore.CategoryExports, []string{"drop"}, ".csv", "collected")
	for _, m := range []*ObjectMeta{ready, doomed} {
		if err := objs.Register(ctx, m); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	// 引用归零 → 孤儿；再把宽限期基准当成已过，认领成功。
	if err := s.WithTx(ctx, func(tx *Tx) error { return tx.Release(ctx, doomed.BlobKey) }); err != nil {
		t.Fatalf("release: %v", err)
	}
	claimed, err := objs.ClaimDeleting(ctx, doomed.BlobKey, 0, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("过宽限期的孤儿应被认领")
	}

	if n, err := objs.Forget(ctx, []string{ready.BlobKey, doomed.BlobKey}); err != nil {
		t.Fatalf("forget: %v", err)
	} else if n != 1 {
		t.Fatalf("Forget 删了 %d 行，期望只删 deleting 的那一行", n)
	}
	if _, err := objs.Get(ctx, ready.BlobKey); err != nil {
		t.Errorf("活引用行被误删：%v", err)
	}
	if _, err := objs.Get(ctx, doomed.BlobKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting 行未清掉：%v", err)
	}
}

func keysOf(metas []*ObjectMeta) []string {
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.BlobKey)
	}
	return out
}
