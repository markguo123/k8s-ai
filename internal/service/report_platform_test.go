package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// platformRef 造一个内容寻址的 Ref（key 一律经 BuildKey，绕过它等于测不到 key 校验）。
func platformRef(t *testing.T, seed string, scope []string, ext string) objectstore.Ref {
	t.Helper()
	sum := sha256.Sum256([]byte(seed))
	hexSum := hex.EncodeToString(sum[:])
	key, err := objectstore.BuildKey(objectstore.DefaultPrefix, objectstore.CategoryReports, scope, hexSum, ext)
	if err != nil {
		t.Fatalf("build key: %v", err)
	}
	return objectstore.Ref{Key: key, SHA256: hexSum, Size: int64(len(seed)),
		MediaType: objectstore.ResolveMediaType("", ext), UpdatedAt: time.Now().UTC()}
}

// seedPlatformReport 经唯一合法写路径落一行报告 + 一个对象引用（同事务）。
func seedPlatformReport(t *testing.T, st store.Store, id, actor, clusterID, driver string, ref objectstore.Ref) *store.Report {
	t.Helper()
	ctx := context.Background()
	row := &store.Report{
		ID: id, ClusterID: clusterID, ScanID: "scan-" + id, Kind: ReportKindLatestMD,
		BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
		Findings: 1, CreatedBy: actor, Available: true, CreatedAt: time.Now().UTC(),
	}
	err := st.WithTx(ctx, func(tx *store.Tx) error {
		if err := tx.InsertReport(ctx, row); err != nil {
			return err
		}
		return tx.Acquire(ctx, ref, driver, actor)
	})
	if err != nil {
		t.Fatalf("seed report %s: %v", id, err)
	}
	return row
}

// putPlatformContent 先写正文再由后端返回 Ref：内容寻址下 key 由后端算，
// 自己拼 key 再去写正文等于把「行指向的对象真的存在」这条前提跳过了。
func putPlatformContent(t *testing.T, backend objectstore.Store, body string, scope []string, ext, actor string) objectstore.Ref {
	t.Helper()
	ref, err := backend.Put(context.Background(), objectstore.Spec{
		Category:  objectstore.CategoryReports,
		Scope:     scope,
		Ext:       ext,
		MediaType: objectstore.ResolveMediaType("", ext),
		CreatedBy: actor,
	}, strings.NewReader(body))
	if err != nil {
		t.Fatalf("put content: %v", err)
	}
	return ref
}

func platformDBPair(t *testing.T, st store.Store) (objectstore.Store, *BlobGateway) {
	t.Helper()
	backend, err := st.ObjectContent("k8sai", 1<<20)
	if err != nil {
		t.Fatalf("db content backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend, NewBlobGateway(backend, st.Objects())
}

// TestReportPlatformOtherUsersReportLooksAbsent 是防越权下载的核心：越权与 id 不存在
// 必须是同一个错误（server 层同形 404）。回 403 就等于告诉试探者「这份报告存在」。
func TestReportPlatformOtherUsersReportLooksAbsent(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend, gw := platformDBPair(t, st)
	platform := NewReportPlatform(st, gw)
	scope := ReportScope{ViewerID: "alice"}

	mine := putPlatformContent(t, backend, "alice report body", []string{"c1", "scan-mine"}, ".md", "alice")
	seedPlatformReport(t, st, "r-mine", "alice", "c1", objectstore.DriverDB, mine)
	theirs := putPlatformContent(t, backend, "bob secret findings", []string{"c1", "scan-theirs"}, ".md", "bob")
	seedPlatformReport(t, st, "r-theirs", "bob", "c1", objectstore.DriverDB, theirs)

	_, _, _, errMine := platform.OpenContent(ctx, "r-mine", scope)
	if errMine != nil {
		t.Fatalf("读自己的报告失败：%v", errMine)
	}
	_, _, _, errTheirs := platform.OpenContent(ctx, "r-theirs", scope)
	_, _, _, errAbsent := platform.OpenContent(ctx, "r-does-not-exist", scope)
	if !errors.Is(errTheirs, store.ErrNotFound) || !errors.Is(errAbsent, store.ErrNotFound) {
		t.Fatalf("越权=%v / 不存在=%v，期望都是 store.ErrNotFound（同形 404）", errTheirs, errAbsent)
	}
	// 服务层只保证「同一个哨兵错误」；响应体同形由 server 层的固定文案保证
	// （见 internal/server/reports_test.go 的 404 同形用例）。
	for _, err := range []error{errTheirs, errAbsent} {
		if strings.Contains(err.Error(), "bob") {
			t.Fatalf("越权错误的内部文案泄出了归属：%v", err)
		}
	}

	// admin 跨归属可读（审计由 server 层负责）。
	view, rc, _, err := platform.OpenContent(ctx, "r-theirs", ReportScope{ViewerID: "admin", Admin: true})
	if err != nil {
		t.Fatalf("admin 读他人报告失败：%v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(raw) != "bob secret findings" || view.CreatedBy != "bob" {
		t.Fatalf("admin 读到 %q (owner=%s)", raw, view.CreatedBy)
	}
}

// TestReportPlatformSystemReportsFollowClusterGrants 覆盖系统巡检报告的可见性策略：
// 按集群授权放行；未绑定集群的一份都不给（没有可比对的授权对象时默认拒绝）。
func TestReportPlatformSystemReportsFollowClusterGrants(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	backend, gw := platformDBPair(t, st)
	platform := NewReportPlatform(st, gw)

	seedPlatformReport(t, st, "sys-c1", objectstore.SystemActor, "c1", objectstore.DriverDB,
		putPlatformContent(t, backend, "system scan of c1", []string{"c1", "s1"}, ".json", objectstore.SystemActor))
	seedPlatformReport(t, st, "sys-nocluster", objectstore.SystemActor, "", objectstore.DriverDB,
		putPlatformContent(t, backend, "legacy local scan", []string{"local"}, ".json", objectstore.SystemActor))

	granted := ReportScope{ViewerID: "alice", SystemClusterIDs: []string{"c1"}}
	if _, _, _, err := platform.OpenContent(ctx, "sys-c1", granted); err != nil {
		t.Fatalf("已授权集群读系统报告失败：%v", err)
	}
	if _, _, _, err := platform.OpenContent(ctx, "sys-nocluster", granted); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cluster_id 为空的系统报告 = %v，期望按不存在处理", err)
	}
	// 全域集群授权同样放开不了没有集群标识的那一份。
	allClusters := ReportScope{ViewerID: "alice", AllSystemClusters: true}
	if _, _, _, err := platform.OpenContent(ctx, "sys-nocluster", allClusters); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("AllSystemClusters 下仍应拒绝空集群报告，got %v", err)
	}
	// 未授权集群的用户一份系统报告都读不到。
	if _, _, _, err := platform.OpenContent(ctx, "sys-c1", ReportScope{ViewerID: "alice"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("零授权读系统报告 = %v，期望按不存在处理", err)
	}
	if _, _, _, err := platform.OpenContent(ctx, "sys-nocluster", ReportScope{Admin: true}); err != nil {
		t.Fatalf("admin 应能读未绑定集群的系统报告，got %v", err)
	}
}

// TestReportPlatformDriverMismatchIsNotLoss 是 §10.3 的行为闸门：换过 driver 没迁移时
// 报「去跑 blobs migrate」，绝不 MarkMissing、绝不打 available=false。一次配置切换把
// 历史报告判成永久丢失，是这份设计里最难看的一种自伤。
func TestReportPlatformDriverMismatchIsNotLoss(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	_, gw := platformDBPair(t, st) // 网关是 db
	platform := NewReportPlatform(st, gw)

	ref := platformRef(t, "written when driver was s3", []string{"c1", "s1"}, ".md")
	row := seedPlatformReport(t, st, "r-legacy", "alice", "c1", objectstore.DriverS3, ref)

	_, _, _, err := platform.OpenContent(ctx, "r-legacy", ReportScope{ViewerID: "alice"})
	if !errors.Is(err, ErrObjectDriverMismatch) {
		t.Fatalf("driver 不符 = %v，期望 ErrObjectDriverMismatch", err)
	}
	if !strings.Contains(err.Error(), "blobs migrate") {
		t.Fatalf("错误文案应给出迁移命令：%v", err)
	}

	meta, err := st.Objects().Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if meta.State != store.ObjectStateReady {
		t.Fatalf("driver 不符把对象打成 %q，期望保持 ready", meta.State)
	}
	got, err := st.Reports().Get(ctx, row.ID)
	if err != nil {
		t.Fatalf("get report: %v", err)
	}
	if !got.Available {
		t.Fatal("driver 不符把 reports.available 打成 false，迁移后这份报告就永远回不来了")
	}
}

// TestReportPlatformMissingContentIsGone：后端明确回答「没有这个对象」才标 missing 并回 410。
func TestReportPlatformMissingContentIsGone(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	gw := NewBlobGateway(&fakeBlobBackend{
		driver: objectstore.DriverDB,
		getErr: fmt.Errorf("%w: payload purged", objectstore.ErrNotFound),
	}, st.Objects())
	platform := NewReportPlatform(st, gw)

	ref := platformRef(t, "content already GC-ed", []string{"c1", "s1"}, ".md")
	seedPlatformReport(t, st, "r-gone", "alice", "c1", objectstore.DriverDB, ref)

	_, _, _, err := platform.OpenContent(ctx, "r-gone", ReportScope{ViewerID: "alice"})
	if !errors.Is(err, ErrReportUnavailable) {
		t.Fatalf("内容缺失 = %v，期望 ErrReportUnavailable（410）", err)
	}
	meta, err := st.Objects().Get(ctx, ref.Key)
	if err != nil {
		t.Fatalf("get object: %v", err)
	}
	if meta.State != store.ObjectStateMissing {
		t.Fatalf("对象状态 = %q，期望 missing", meta.State)
	}
}

// TestReportPlatformOutageIsNotMissing 与上一条成对：不可达时既不能标 missing 也不能改
// available，否则一次网络抖动就把好数据判成永久丢失，运维连补救的机会都没有。
func TestReportPlatformOutageIsNotMissing(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	gw := NewBlobGateway(&fakeBlobBackend{
		driver:    objectstore.DriverDB,
		ensureErr: fmt.Errorf("%w: bucket unreachable", objectstore.ErrUnavailable),
	}, st.Objects())
	platform := NewReportPlatform(st, gw)

	ref := platformRef(t, "still safe in the bucket", []string{"c1", "s1"}, ".md")
	seedPlatformReport(t, st, "r-ok", "alice", "c1", objectstore.DriverDB, ref)

	_, _, _, err := platform.OpenContent(ctx, "r-ok", ReportScope{ViewerID: "alice"})
	if !errors.Is(err, objectstore.ErrUnavailable) {
		t.Fatalf("不可达 = %v，期望原样抛 ErrUnavailable（503）", err)
	}
	if errors.Is(err, ErrReportUnavailable) {
		t.Fatal("不可达被误判成内容永久丢失")
	}
	meta, err := st.Objects().Get(ctx, ref.Key)
	if err != nil || meta.State != store.ObjectStateReady {
		t.Fatalf("一次不可达改变了对象状态：%+v (%v)", meta, err)
	}
	got, err := st.Reports().Get(ctx, "r-ok")
	if err != nil || !got.Available {
		t.Fatalf("一次不可达改变了 reports.available：%+v (%v)", got, err)
	}
}

// TestReportPlatformPresignRequiresOwnership 保证签名不会变成一次授权：
// 越权的 id 拿不到 URL，且内部 key 不出现在对外视图里。
func TestReportPlatformPresignRequiresOwnership(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	gw := NewBlobGateway(&fakePresignBackend{fakeBlobBackend: &fakeBlobBackend{driver: objectstore.DriverDB}}, st.Objects())
	platform := NewReportPlatform(st, gw)
	ref := platformRef(t, "signable content", []string{"c1", "s1"}, ".md")
	seedPlatformReport(t, st, "r-sign", "alice", "c1", objectstore.DriverDB, ref)

	if _, url, err := platform.Presign(ctx, "r-sign", ReportScope{ViewerID: "bob"}, time.Minute); !errors.Is(err, store.ErrNotFound) || url != "" {
		t.Fatalf("越权 presign = %q (%v)，期望无 URL 且按不存在处理", url, err)
	}
	view, url, err := platform.Presign(ctx, "r-sign", ReportScope{ViewerID: "alice"}, 0)
	if err != nil || url == "" {
		t.Fatalf("本人 presign = %q (%v)", url, err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	// key 里带集群 ID 与扫描 ID，属内部结构：视图类型必须把它挡在响应之外。
	if strings.Contains(string(raw), ref.Key) || strings.Contains(string(raw), "blobKey") || strings.Contains(string(raw), "blob_key") {
		t.Fatalf("对外视图泄漏了对象 key：%s", raw)
	}
}

// TestReportPlatformListDoesNotReadContent：列表只读业务行，不碰对象正文
// （方案 15 §6.5「报告列表不加载对象正文」）。这里用一个一取正文就炸的后端来证明。
func TestReportPlatformListDoesNotReadContent(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	gw := NewBlobGateway(&fakeBlobBackend{
		driver: objectstore.DriverDB,
		getErr: errors.New("content access must not happen on list"),
	}, st.Objects())
	platform := NewReportPlatform(st, gw)
	seedPlatformReport(t, st, "r-list", "alice", "c1", objectstore.DriverDB,
		platformRef(t, "listed", []string{"c1", "s1"}, ".md"))

	items, total, err := platform.List(ctx, ReportScope{ViewerID: "alice"}, 20, 0)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("列表 = %d/%d (%v)", len(items), total, err)
	}
	if items[0].ID != "r-list" || items[0].Size <= 0 {
		t.Fatalf("列表视图不完整：%+v", items[0])
	}
}
