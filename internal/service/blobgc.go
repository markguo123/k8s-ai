package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 本文件是对象回收的唯一实现（方案 15 §9.3）：`blobs gc` 与将来 P5 Retention 的清理
// 执行器共用它，不允许出现第二套回收逻辑。
//
// 两条不可让的规矩都来自「先内容后引用」这条总原则（§3 原则 6）：
//
//  1. 引用侧回收是**两段式**：CAS 认领 → 删元数据行 → 删内容。反过来先删内容，就制造了
//     「引用在、内容没了」的悬空引用，那是唯一不可修复的损坏形态（§12.1）。
//  2. 差集回收**一律不按 driver 过滤**：`blobs migrate` 进行中会出现「内容已在目标后端、
//     objects.driver 还没翻转」的 key，按当前装配后端求差会把正在迁移的对象当孤儿删掉
//     （§9.3 #4）。表里有没有这一行，是孤儿判定的唯一依据。
//
// 引用侧还有一条从属规矩：`objects.driver` 与当前后端不符的候选**只跳过不处理**（计入
// SkippedForeign）。理由是 driver 是「内容躺在哪」这个事实，不是删除许可——在本后端删不到
// 那个对象，把行忘掉就等于宣称「内容已删」而它其实还在旧后端里，同时还抢在 migrate 前面把
// 迁移唯一的线索（这一行）擦掉了。让它留在表里，`blobs stat` 与 `storage/health` 的
// driver_mismatch 会一直点名，直到 `blobs migrate --to <当前 driver>` 把它接回来。

// GCOptions 是一次回收的参数。
type GCOptions struct {
	// Grace 是宽限期：只回收引用归零超过该时长的对象，避开未提交事务与备份窗口。
	// 必须 > 0 且由调用方从 platform.blobs.gc_grace_period 传入——默认值只允许存在于
	// 配置面一份，这里再写一个默认值就是给「改配置不生效」留后门。
	Grace time.Duration
	// Prefix 是本项目对象 key 的顶层命名空间；差集只在本 prefix 内动手，其余只报告。
	Prefix string
	// DryRun 只统计不动手。生产首轮必须 dry-run（§9.3 #2）。
	DryRun bool
}

// GCResult 是一次回收的账目。计数刻意分成「引用侧」与「差集侧」两组：它们是两条独立的
// 收敛路径，出问题时运维要能一眼看出是哪条在动。
type GCResult struct {
	Driver string    `json:"driver"`
	Cutoff time.Time `json:"cutoff"`
	DryRun bool      `json:"dryRun"`

	// Candidates 是过了宽限期的 orphaned 行数；Claimed/Revived 区分「真回收」与「被并发
	// Acquire 复活」，后者必须报出来：它是并发安全唯一的可见证据。
	Candidates int `json:"candidates"`
	Claimed    int `json:"claimed"`
	Revived    int `json:"revived"`
	// RowsRemoved 是删掉的元数据行数，ContentDeleted 是删掉内容的个数。两者不等是正常的
	// （内容可能早已缺失），差值就是下一轮差集要收拾的量。
	RowsRemoved    int   `json:"rowsRemoved"`
	ContentDeleted int   `json:"contentDeleted"`
	ReclaimedBytes int64 `json:"reclaimedBytes"`
	// SkippedForeign 是「行是死的、但内容不归本后端管」的候选数：不删、不改，只报告。
	// 这个数不为零就说明有一批对象等着迁移，见文件头那条从属规矩。
	SkippedForeign int `json:"skippedForeignDriver"`

	// BucketKeys 是后端在本 prefix 下列举到的对象数，Orphans/OrphansDeleted 是差集结果。
	// Foreign 是「列举到但不属于本项目」的对象数（只报告不删，桶可能与别的系统共用），
	// 其中首段是别人命名空间的记在 OtherRoot，连 key 都拼不出来的记在 RejectedKey。
	BucketKeys  int   `json:"bucketKeys"`
	Orphans     int   `json:"orphans"`
	OrphanBytes int64 `json:"orphanBytes"`
	OrphansDel  int   `json:"orphansDeleted"`
	Foreign     int   `json:"foreign"`
	OtherRoot   int   `json:"foreignOtherRoot"`
	RejectedKey int   `json:"foreignRejectedKey"`
}

// Empty 报告这一轮是否什么都没发现（命令层用它决定要不要打印明细）。
func (r GCResult) Empty() bool {
	return r.Candidates == 0 && r.Orphans == 0 && r.Foreign == 0 && r.SkippedForeign == 0
}

// BlobGC 是引用计数归零对象的回收器。它只依赖两个既有接口——内容后端与元数据登记表——
// 因此能在 server 之外（CLI、将来的清理 Job）原样复用，不必装配网关与降级状态机：
// 离线运维命令没有「降级继续服务」这回事，坏了就报错退出。
type BlobGC struct {
	backend objectstore.Store
	objects store.ObjectStore
}

// NewBlobGC 组装回收器。两侧都不能为 nil：宁可在这里就把调用方拦住，也不要在回收跑到一半
// 时才发现少了一侧——那正是最容易留下半截状态的位置。
func NewBlobGC(backend objectstore.Store, objects store.ObjectStore) (*BlobGC, error) {
	if backend == nil {
		return nil, errors.New("service: blob GC needs a content backend")
	}
	if objects == nil {
		return nil, errors.New("service: blob GC needs the object registry")
	}
	return &BlobGC{backend: backend, objects: objects}, nil
}

// Run 跑一轮回收：先收引用归零的对象，再用差集兜住「行没了、内容还在」的残留（§9.2 末行）。
//
// 后端不可达时**立即中止**而不是继续标记：行已经删了一半，把剩下的也标记掉只会把一次可恢复
// 的故障变成一大批「行没了内容还在」的孤儿，白等一轮差集。
func (g *BlobGC) Run(ctx context.Context, opts GCOptions) (GCResult, error) {
	if opts.Grace <= 0 {
		return GCResult{}, fmt.Errorf("service: blob GC needs a positive grace period (platform.blobs.gc_grace_period)")
	}
	prefix, err := objectstore.NormalizePrefix(opts.Prefix)
	if err != nil {
		return GCResult{}, err
	}
	res := GCResult{Driver: g.backend.Driver(), DryRun: opts.DryRun, Cutoff: time.Now().Add(-opts.Grace)}

	candidates, err := g.objects.ListCollectable(ctx, res.Cutoff)
	if err != nil {
		return res, fmt.Errorf("list collectable objects: %w", err)
	}
	for _, meta := range candidates {
		res.Candidates++
		// 内容不归本后端管：删不到就别声称删到了，也别抢 migrate 的线索。
		if meta.Driver != g.backend.Driver() {
			res.SkippedForeign++
			continue
		}
		if opts.DryRun {
			res.ReclaimedBytes += meta.Size
			continue
		}
		// CAS 认领：state/ref_count/deleted_at 三个条件全中才置 deleting。没命中就是这一行在
		// 本轮读取之后被 Acquire 复活了，跳过它——这是 GC 与业务写并发唯一的正确姿势，
		// 「先 SELECT 再 UPDATE」在这里就是实打实的丢引用窗口。
		claimed, err := g.objects.ClaimDeleting(ctx, meta.BlobKey, meta.RefCount, res.Cutoff)
		if err != nil {
			return res, err
		}
		if !claimed {
			res.Revived++
			continue
		}
		res.Claimed++
		removed, err := g.objects.Forget(ctx, []string{meta.BlobKey})
		if err != nil {
			return res, err
		}
		res.RowsRemoved += removed
		if err := g.deleteContent(ctx, meta.BlobKey); err != nil {
			return res, err
		}
		res.ContentDeleted++
		res.ReclaimedBytes += meta.Size
	}

	inventory, err := BuildInventory(ctx, g.backend, g.objects, prefix)
	if err != nil {
		return res, err
	}
	res.BucketKeys = inventory.ListedKeys
	res.Foreign = len(inventory.Foreign)
	res.OtherRoot = inventory.OtherRootKeys
	res.RejectedKey = inventory.RejectedKeys
	for _, ref := range inventory.Orphans {
		res.Orphans++
		res.OrphanBytes += ref.Size
		if opts.DryRun {
			continue
		}
		if err := g.deleteContent(ctx, ref.Key); err != nil {
			return res, err
		}
		res.OrphansDel++
	}
	return res, nil
}

// deleteContent 删内容。两种「算成功」的情形必须吞掉：内容本就不在（ErrNotFound），以及
// db 后端「行已删、正文随行同灭」——后者必然回 ErrNotFound，把它当失败会让 driver=db 的
// 每一次正常回收都报错。
func (g *BlobGC) deleteContent(ctx context.Context, key string) error {
	err := g.backend.Delete(ctx, key)
	if err == nil || errors.Is(err, objectstore.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("delete object %s: %w", key, err)
}

// Inventory 是「后端实存内容」与「元数据登记表」的一次对账快照（§12.1）。
//
// 它是 `blobs gc` 的差集与 `blobs verify` 的反向扫描共用的同一份口径。之所以做成一个函数
// 而不是各自的循环：两处一旦对「什么算孤儿」有不同理解，就会出现 verify 说干净、gc 却在删
// 东西的荒谬局面，而这恰恰是最该一致的地方。
type Inventory struct {
	// Prefix 是本次对账的命名空间。
	Prefix string
	// Registry 是 objects 全表（键为 blob_key），**包含 driver 与当前后端不一致的行**——
	// 那些是「迁移中」的合法状态，不是孤儿，更不是缺失。
	Registry map[string]*store.ObjectMeta
	// Present 是后端里真实有内容的对象（本项目 prefix 下、key 合法）。
	Present map[string]objectstore.Ref
	// Orphans 是后端有内容、表里没有行的对象：只有它们允许被删。
	Orphans []objectstore.Ref
	// Foreign 是列举到但不属于本项目的对象：只报告，绝不删。
	Foreign []objectstore.Ref

	// ListedKeys 是后端列举到的总条数（含 Foreign）。
	ListedKeys int
	// RejectedKeys 是连 ValidateKey 都没过的条目数（后端一般会自己跳过，这里兜底）。
	RejectedKeys int
	// OtherRootKeys 是 key 合法但首段不是本项目 prefix 的条数（共用桶的真实形态）。
	OtherRootKeys int
}

// BuildInventory 列举后端内容并与元数据表求差。
//
// 元数据表**全表读**、不按 driver 过滤（§9.3 #4）；后端只在本项目 prefix 下扫描，且只有
// 「key 规则拼得出来 + 首段属于本项目 prefix」的对象才进入可删集合，其余一律记成 Foreign：
// 桶可能是共用的，删别人文件的代价远大于多留几个孤儿。
func BuildInventory(ctx context.Context, backend objectstore.Store, objects store.ObjectStore, prefix string) (*Inventory, error) {
	if backend == nil || objects == nil {
		return nil, errors.New("service: inventory needs both the backend and the object registry")
	}
	root, err := objectstore.NormalizePrefix(prefix)
	if err != nil {
		return nil, err
	}
	inv := &Inventory{
		Prefix:   root,
		Registry: map[string]*store.ObjectMeta{},
		Present:  map[string]objectstore.Ref{},
	}
	rows, err := objects.ListByCategory(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list object registry: %w", err)
	}
	for _, row := range rows {
		inv.Registry[row.BlobKey] = row
	}

	err = backend.List(ctx, root, func(ref objectstore.Ref) error {
		inv.ListedKeys++
		if err := objectstore.ValidateKey(ref.Key); err != nil {
			inv.RejectedKeys++
			inv.Foreign = append(inv.Foreign, ref)
			return nil
		}
		if keyRootSegment(ref.Key) != root {
			inv.OtherRootKeys++
			inv.Foreign = append(inv.Foreign, ref)
			return nil
		}
		inv.Present[ref.Key] = ref
		if _, ok := inv.Registry[ref.Key]; !ok {
			inv.Orphans = append(inv.Orphans, ref)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan object backend: %w", err)
	}
	return inv, nil
}

// keyRootSegment 取 key 的首段（本项目 prefix）。调用方须保证 key 已过 ValidateKey。
func keyRootSegment(key string) string {
	if i := strings.IndexByte(key, '/'); i > 0 {
		return key[:i]
	}
	return key
}
