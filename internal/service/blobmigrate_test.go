package service

// P8.12 的语义锁（方案 15 §10.1）。这批用例只盯一件事：迁移半途而废时，objects.driver 写的那个
// 后端到底读不读得出内容。所以全部拿真实后端跑，断言也只问两句——行上写着哪个 driver、那个
// driver 的 Get 成不成。
//
// objects.driver 只有 db|dir|s3 三个值，「把一个 dir 目录换到另一个 dir 目录」在元数据层面没有
// 意义（NewBlobMigrator 当场拒），因此可测方向只有 db 与 dir 之间两条；s3 的两个方向需要真机
// 凭据（AEGISOPS_TEST_S3_*），属人工门禁。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// migrateDBBackend 取 db 内容后端（正文就放在 objects.payload）。
func migrateDBBackend(t *testing.T, st store.Store) objectstore.Store {
	t.Helper()
	backend, err := st.ObjectContent(objectstore.DefaultPrefix, 1<<20)
	if err != nil {
		t.Fatalf("db content backend: %v", err)
	}
	return backend
}

// migrateObject 在指定后端写一个对象并建引用行，category 由调用方给（分类过滤用例要用）。
func migrateObject(t *testing.T, st store.Store, backend objectstore.Store, category, content string) objectstore.Ref {
	t.Helper()
	ctx := context.Background()
	ref, err := backend.Put(ctx, objectstore.Spec{
		Category:  category,
		Scope:     []string{"c1"},
		Ext:       ".md",
		CreatedBy: objectstore.SystemActor,
	}, strings.NewReader(content))
	if err != nil {
		t.Fatalf("put %s object: %v", category, err)
	}
	if err := st.WithTx(ctx, func(tx *store.Tx) error {
		return tx.Acquire(ctx, ref, backend.Driver(), "alice")
	}); err != nil {
		t.Fatalf("acquire %s object: %v", category, err)
	}
	return ref
}

// migrateRowOnOtherDriver 登记「内容在这个后端里、行上却写着别的 driver」的对象：
// 换完配置还没迁移的典型现场。
func migrateRowOnOtherDriver(t *testing.T, st store.Store, backend objectstore.Store, driver, content string) objectstore.Ref {
	t.Helper()
	ctx := context.Background()
	ref, err := backend.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"c9"}, Ext: ".md",
	}, strings.NewReader(content))
	if err != nil {
		t.Fatalf("put object owned by %s: %v", driver, err)
	}
	if err := st.Objects().Register(ctx, &store.ObjectMeta{
		BlobKey: ref.Key, SHA256: ref.SHA256, Size: ref.Size, MediaType: ref.MediaType,
		Category: objectstore.CategoryReports, Driver: driver, State: store.ObjectStateReady,
		CreatedBy: "alice",
	}); err != nil {
		t.Fatalf("register row owned by %s: %v", driver, err)
	}
	if err := st.WithTx(ctx, func(tx *store.Tx) error {
		return tx.Acquire(ctx, ref, driver, "alice")
	}); err != nil {
		t.Fatalf("acquire row owned by %s: %v", driver, err)
	}
	return ref
}

func TestBlobMigratorRefusesImpossiblePairs(t *testing.T) {
	st := newArchiveStore(t)
	src := gcDirBackend(t)
	other := gcDirBackend(t)
	db := migrateDBBackend(t, st)

	if _, err := NewBlobMigrator(nil, db, st); err == nil {
		t.Error("a migration without a source backend must be refused")
	}
	if _, err := NewBlobMigrator(src, nil, st); err == nil {
		t.Error("a migration without a target backend must be refused")
	}
	if _, err := NewBlobMigrator(src, db, nil); err == nil {
		t.Error("a migration without the platform store must be refused")
	}
	if _, err := NewBlobMigrator(objectstore.NewDisabled(), db, st); err == nil {
		t.Error("driver off is not a place to store content")
	}
	// 两端同名 = 配置写错了：objects.driver 翻不动，搬过去的只是一份没人认领的副本。
	if _, err := NewBlobMigrator(src, other, st); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("dir to dir = %v, want it refused", err)
	}
}

func TestBlobMigrateDirToDBWritesPayloadThenFlipsDriver(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := gcDirBackend(t)
	dst := migrateDBBackend(t, st)
	content := "report body that has to survive the move"
	ref := migrateObject(t, st, src, objectstore.CategoryReports, content)

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20)})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	if res.Candidates != 1 || res.Copied != 1 || res.Flipped != 1 || res.Failed != 0 || res.Conflict != 0 {
		t.Fatalf("counts = candidates %d copied %d flipped %d failed %d conflict %d, want 1/1/1/0/0",
			res.Candidates, res.Copied, res.Flipped, res.Failed, res.Conflict)
	}
	if res.Bytes != ref.Size {
		t.Fatalf("moved bytes = %d, want %d", res.Bytes, ref.Size)
	}
	meta := gcMeta(t, st, ref.Key)
	if meta.Driver != objectstore.DriverDB {
		t.Fatalf("driver after migrate = %q, want db", meta.Driver)
	}
	// 引用计数与 state 不归迁移管：搬一个对象不该改它的生命周期。
	if meta.RefCount != 1 || meta.State != store.ObjectStateReady {
		t.Fatalf("ref_count/state = %d/%s, want 1/%s", meta.RefCount, meta.State, store.ObjectStateReady)
	}
	if !gcReadable(t, dst, ref.Key) {
		t.Fatal("content must be readable on the target backend")
	}
	// 默认留着源端：那是回滚余量，要清得显式说 --prune-source。
	if !gcReadable(t, src, ref.Key) {
		t.Fatal("the source copy must survive without --prune-source")
	}

	// 重跑必须收敛成零改动：行已经在新后端上，既不是候选也不报错。
	again, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20)})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again.Candidates != 0 || again.Flipped != 0 || again.Failed != 0 || again.SkippedOther != 1 {
		t.Fatalf("second run = candidates %d flipped %d failed %d other %d, want 0/0/0/1",
			again.Candidates, again.Flipped, again.Failed, again.SkippedOther)
	}
}

func TestBlobMigratePruneSourceOnlyTouchesFlippedRows(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := gcDirBackend(t)
	dst := migrateDBBackend(t, st)
	own := migrateObject(t, st, src, objectstore.CategoryReports, "flipped and therefore prunable")
	foreign := migrateRowOnOtherDriver(t, st, src, objectstore.DriverS3, "not this round")

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20), PruneSource: true})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	if res.Candidates != 1 || res.SkippedOther != 1 || res.Flipped != 1 || res.Pruned != 1 || res.PruneFailed != 0 {
		t.Fatalf("counts = candidates %d other %d flipped %d pruned %d prune-failed %d, want 1/1/1/1/0",
			res.Candidates, res.SkippedOther, res.Flipped, res.Pruned, res.PruneFailed)
	}
	if gcReadable(t, src, own.Key) {
		t.Fatal("a flipped object must have lost its source copy")
	}
	if !gcReadable(t, src, foreign.Key) {
		t.Fatal("--prune-source deleted content still owned by another driver")
	}
	if got := gcMeta(t, st, foreign.Key).Driver; got != objectstore.DriverS3 {
		t.Fatalf("other-driver row was rewritten to %q, want it left on s3", got)
	}
}

func TestBlobMigrateDBToDirPruneClearsPayloadWithoutMarkingMissing(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := migrateDBBackend(t, st)
	dst := gcDirBackend(t)
	ref := migrateObject(t, st, src, objectstore.CategoryReports, "legacy rows moving out of mysql")

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20), PruneSource: true})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	if res.Flipped != 1 || res.Pruned != 1 || res.Failed != 0 {
		t.Fatalf("counts = flipped %d pruned %d failed %d, want 1/1/0", res.Flipped, res.Pruned, res.Failed)
	}
	// 关键一条：清 payload 与翻 driver 同语句，state 不能被顺手打成 missing。
	// 那块牌子写的是「内容丢了」，而这里内容只是搬到了 dir，读路径完全正常。
	meta := gcMeta(t, st, ref.Key)
	if meta.Driver != objectstore.DriverDir || meta.State != store.ObjectStateReady {
		t.Fatalf("driver/state = %q/%s, want %s/%s",
			meta.Driver, meta.State, objectstore.DriverDir, store.ObjectStateReady)
	}
	if !gcReadable(t, dst, ref.Key) {
		t.Fatal("content must be readable on the dir backend")
	}
	if gcReadable(t, src, ref.Key) {
		t.Fatal("the mysql payload survived --prune-source")
	}
}

func TestBlobMigrateConvergesAfterAnInterruptedRun(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := migrateDBBackend(t, st)
	dst := gcDirBackend(t)
	content := "content landed on the target, the metadata flip did not"
	ref := migrateObject(t, st, src, objectstore.CategoryReports, content)
	// 复刻「上一轮在两步之间被杀」：目标端内容齐了，行还写着旧 driver。
	if _, err := dst.Put(ctx, objectstore.Spec{
		Category: objectstore.CategoryReports, Scope: []string{"c1"}, Ext: ".md",
	}, strings.NewReader(content)); err != nil {
		t.Fatalf("stage content on the target: %v", err)
	}

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20)})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	// 重跑只补那笔没落的元数据：不重写内容、不报失败。
	if res.Present != 1 || res.Copied != 0 || res.Flipped != 1 || res.Failed != 0 {
		t.Fatalf("counts = present %d copied %d flipped %d failed %d, want 1/0/1/0",
			res.Present, res.Copied, res.Flipped, res.Failed)
	}
	if got := gcMeta(t, st, ref.Key).Driver; got != objectstore.DriverDir {
		t.Fatalf("driver = %q, want dir", got)
	}
	if !gcReadable(t, src, ref.Key) {
		t.Fatal("the source copy must still be whole until it is pruned on purpose")
	}
}

func TestBlobMigrateNeverOverwritesTheTarget(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := migrateDBBackend(t, st)
	dstRoot := t.TempDir()
	dst := gcDirBackendAt(t, dstRoot)
	content := "the registry says exactly this many bytes"
	ref := migrateObject(t, st, src, objectstore.CategoryReports, content)

	// 目标端同 key 下躺着别的字节：内容寻址下这事没有合理解释，只能当异常报，绝不覆盖。
	stray := "somebody put a different file here and it is longer on purpose"
	victim := filepath.Join(dstRoot, filepath.FromSlash(ref.Key))
	if err := os.MkdirAll(filepath.Dir(victim), 0o755); err != nil {
		t.Fatalf("create target dir: %v", err)
	}
	if err := os.WriteFile(victim, []byte(stray), 0o644); err != nil {
		t.Fatalf("write conflicting target object: %v", err)
	}

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20)})
	if err != nil {
		t.Fatalf("a single bad object must be counted, not aborted: %v", err)
	}
	if res.Failed != 1 || res.Flipped != 0 || res.Copied != 0 {
		t.Fatalf("counts = failed %d flipped %d copied %d, want 1/0/0", res.Failed, res.Flipped, res.Copied)
	}
	if !strings.Contains(strings.Join(res.Failures, "\n"), "refusing to overwrite") {
		t.Fatalf("failures = %v, want the refusal spelled out", res.Failures)
	}
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatalf("read the target object back: %v", err)
	}
	if string(got) != stray {
		t.Fatal("migration overwrote content on the target backend")
	}
	// 原对象一个字节都不能动：失败的那一行还得靠源端去修。
	if !gcReadable(t, src, ref.Key) {
		t.Fatal("a failed move must not damage the source object")
	}
	if got := gcMeta(t, st, ref.Key).Driver; got != objectstore.DriverDB {
		t.Fatalf("driver after a failed move = %q, want it left on db", got)
	}
}

func TestBlobMigrateReportsMissingSourceContentAsBroken(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := gcDirBackend(t)
	dst := migrateDBBackend(t, st)
	ref := migrateObject(t, st, src, objectstore.CategoryReports, "content that a bad backup rotation deleted")
	if err := src.Delete(ctx, ref.Key); err != nil {
		t.Fatalf("simulate content loss: %v", err)
	}

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20)})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	// 这一行本来就坏了，不是「放错了地方」。它归 blobs verify 与备份恢复管，
	// 不能因为它让一次迁移永远非零退出。
	if res.Unreadable != 1 || res.Failed != 0 || res.Flipped != 0 {
		t.Fatalf("counts = unreadable %d failed %d flipped %d, want 1/0/0", res.Unreadable, res.Failed, res.Flipped)
	}
	if !strings.Contains(strings.Join(res.Failures, "\n"), "blobs verify") {
		t.Fatalf("failures = %v, want a pointer to blobs verify", res.Failures)
	}
	if got := gcMeta(t, st, ref.Key); got.Driver != objectstore.DriverDir {
		t.Fatalf("driver of a broken row = %q, want it left where the registry says", got.Driver)
	}
}

func TestBlobMigrateDryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := gcDirBackend(t)
	dst := migrateDBBackend(t, st)
	ref := migrateObject(t, st, src, objectstore.CategoryReports, "preview only please")

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20), DryRun: true, PruneSource: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun || res.Candidates != 1 || res.Copied != 0 || res.Flipped != 0 || res.Pruned != 0 || res.Unreadable != 0 {
		t.Fatalf("dry-run counts = %+v, want one candidate and no action", res)
	}
	if gcReadable(t, dst, ref.Key) {
		t.Fatal("dry run wrote content to the target")
	}
	if got := gcMeta(t, st, ref.Key); got.Driver != objectstore.DriverDir || got.State != store.ObjectStateReady {
		t.Fatalf("dry run touched the row: %q/%s", got.Driver, got.State)
	}
	if !gcReadable(t, src, ref.Key) {
		t.Fatal("dry run deleted source content")
	}
}

func TestBlobMigrateRefusesToMoveContradictoryRows(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := gcDirBackend(t)
	dst := migrateDBBackend(t, st)
	ref := migrateObject(t, st, src, objectstore.CategoryReports, "bytes with an honest hash")
	// 登记的摘要与 key 里嵌的摘要互相矛盾：这一行不是本包写进去的。
	if err := st.Objects().Register(ctx, &store.ObjectMeta{
		BlobKey: ref.Key, SHA256: strings.Repeat("a", 64), Size: ref.Size, MediaType: ref.MediaType,
		Category: objectstore.CategoryReports, Driver: objectstore.DriverDir,
		State: store.ObjectStateReady, CreatedBy: "alice",
	}); err != nil {
		t.Fatalf("rewrite the row with a contradictory digest: %v", err)
	}

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20)})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	if res.Failed != 1 || res.Copied != 0 || res.Flipped != 0 {
		t.Fatalf("counts = failed %d copied %d flipped %d, want 1/0/0", res.Failed, res.Copied, res.Flipped)
	}
	if !strings.Contains(strings.Join(res.Failures, "\n"), "contradicts its own key") {
		t.Fatalf("failures = %v, want the contradiction named", res.Failures)
	}
	if gcReadable(t, dst, ref.Key) {
		t.Fatal("a row nobody can vouch for must not be copied onto another backend")
	}
}

func TestBlobMigrateKeepsTheTargetSizeLimit(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := gcDirBackend(t)
	dst := migrateDBBackend(t, st)
	ref := migrateObject(t, st, src, objectstore.CategoryReports, strings.Repeat("m", 512))

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	// 目标是 db 时正文要整段驻留才能与 driver 同语句提交，所以这里必须撞上限。
	// 这是配置判断错了（这批对象本该去 dir/s3），报失败而不是硬塞进库里。
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: 64})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	if res.Failed != 1 || res.Flipped != 0 {
		t.Fatalf("counts = failed %d flipped %d, want 1/0", res.Failed, res.Flipped)
	}
	if gcReadable(t, dst, ref.Key) {
		t.Fatal("an object over the target limit must not have landed in the database")
	}
	if !gcReadable(t, src, ref.Key) {
		t.Fatal("a rejected move must leave the source object alone")
	}
}

// TestBlobMigrateScopesAndSkips 一次覆盖三个筛选面：category 只搬一类、GC 正在认领的行不动、
// 第三个后端的行留给下一轮。
func TestBlobMigrateScopesAndSkips(t *testing.T) {
	ctx := context.Background()
	st := newArchiveStore(t)
	src := gcDirBackend(t)
	dst := migrateDBBackend(t, st)
	report := migrateObject(t, st, src, objectstore.CategoryReports, "a scan report")
	export := migrateObject(t, st, src, objectstore.CategoryExports, "a csv export")
	if err := st.Objects().Register(ctx, &store.ObjectMeta{
		BlobKey: export.Key, SHA256: export.SHA256, Size: export.Size, MediaType: export.MediaType,
		Category: objectstore.CategoryExports, Driver: objectstore.DriverS3,
		State: store.ObjectStateReady, CreatedBy: "alice",
	}); err != nil {
		t.Fatalf("move the export row onto s3: %v", err)
	}
	garbage := migrateObject(t, st, src, objectstore.CategoryAttachments, "already being collected")
	gcOrphan(t, st, garbage.Key)
	claimed, err := st.Objects().ClaimDeleting(ctx, garbage.Key, 0, time.Now().Add(time.Hour))
	if err != nil || !claimed {
		t.Fatalf("claim deleting: claimed=%v err=%v", claimed, err)
	}

	m, err := NewBlobMigrator(src, dst, st)
	if err != nil {
		t.Fatalf("new migrator: %v", err)
	}
	res, err := m.Run(ctx, BlobMigrateOptions{MaxBytes: store.ObjectContentLimit(1 << 20)})
	if err != nil {
		t.Fatalf("run migrate: %v", err)
	}
	if res.Scanned != 3 || res.Candidates != 1 || res.Deleting != 1 || res.SkippedOther != 1 || res.Flipped != 1 {
		t.Fatalf("counts = scanned %d candidates %d deleting %d other %d flipped %d, want 3/1/1/1/1",
			res.Scanned, res.Candidates, res.Deleting, res.SkippedOther, res.Flipped)
	}
	if got := gcMeta(t, st, report.Key).Driver; got != objectstore.DriverDB {
		t.Fatalf("report driver = %q, want db", got)
	}
	if got := gcMeta(t, st, garbage.Key); got.State != store.ObjectStateDeleting || got.Driver != objectstore.DriverDir {
		t.Fatalf("row claimed by gc = %s/%q, want deleting/dir", got.State, got.Driver)
	}
	if got := gcMeta(t, st, export.Key); got.Driver != objectstore.DriverS3 {
		t.Fatalf("s3 row driver = %q, want it left for the next round", got.Driver)
	}

	// category 过滤：只剩 reports 一类可搬，导出的那行已经不在源端了。
	res, err = m.Run(ctx, BlobMigrateOptions{Category: objectstore.CategoryReports, MaxBytes: store.ObjectContentLimit(1 << 20)})
	if err != nil {
		t.Fatalf("filtered run: %v", err)
	}
	if res.Scanned != 1 || res.Candidates != 0 {
		t.Fatalf("filtered counts = scanned %d candidates %d, want 1/0", res.Scanned, res.Candidates)
	}
	if _, err := m.Run(ctx, BlobMigrateOptions{Category: "not-a-category"}); err == nil {
		t.Error("a category outside the whitelist must be refused")
	}
}
