package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 本文件是 `blobs migrate` 的实现（方案 15 §10.1）：把对象从当前配置的后端显式搬到另一个
// 后端，内容搬完并核对过，才改 objects.driver。
//
// 它存在的全部理由是一条被否掉的省事做法。工厂按配置只构造一个后端，改了 driver 之后旧对象
// 就没人去读了——想省掉迁移只有两条路，两条都不通：让三后端同时在线按 row.driver 路由（为
// 一次运维动作给常驻服务加一套多后端路由体系，§10.2 明确不做）；或者假装旧对象还在（历史报告
// 直接打不开，那是数据事故）。所以这里做的是**一次性、可重跑、可核对**的显式搬迁，
// 运行期的路由能力一行都没加。
//
// 四条硬规矩，顺序不能调（§10.1）：
//
//  1. **先内容后元数据**。目标端内容落定并核对过 sha256，才允许翻 objects.driver。反过来做
//     的中间态是「行说内容在 s3、s3 里什么都没有」，也就是 §12.1 里唯一修不好的那种损坏。
//     目标是 db 时更极端：正文与 driver 必须在同一条 UPDATE 里（见 store.Tx.AdoptObject），
//     因为 db 后端自己的 Put 对 row.driver != "db" 一律返回 ErrState——那道守卫是「顺手替
//     别的后端搬内容」的闸门，迁移必须从它上面走，而不是绕开它。
//  2. **绝不覆盖目标端**。目标端已有同 key 但体积不符就是异常，报失败跳过，不拿源端字节去盖。
//  3. **翻转成功前绝不删源**。--prune-source 只删本轮真正翻过 driver 的 key；中途被杀就两端
//     各留一份完整内容，重跑必然收敛（目标端命中已有内容，这一轮只补元数据翻转）。
//  4. **后端不可达立即中止**。源端不可达搬不出内容，目标端不可达一条也写不进去，继续数下一批
//     只会报出一屏假失败。中止时元数据一行都没改，状态可以原样重跑。
//
// 「没迁完不许切配置」的硬闸门不在这里：运行期只报警（启动期分组扫描 + storage/health 的
// driver_mismatch + 读路径 409，均已随 P8.5 落地），部署侧拒绝 apply 在 install.sh（P8.8）。

// maxMigrateSamples 限制失败样本条数：一轮可能点到上千个 key。
const maxMigrateSamples = 10

// BlobMigrateOptions 是一次迁移的参数。
type BlobMigrateOptions struct {
	// Category 只搬一类对象（空 = 全表）。它只是分批推进的节流阀，不改变判定。
	Category string
	// MaxBytes 是搬进 db 目标端时的正文天花板（0 取 objectstore.DefaultMaxObjectBytes）。
	// 只有目标端是 db 时用到：那条路要整段驻留正文才能与 driver 同语句提交，CLI 应传
	// store.ObjectContentLimit 的结果，别拿配置里的原值去撞 MEDIUMBLOB 上限。
	// 其余后端的体积闸门在各自 Put 里，本字段不参与。
	MaxBytes int64
	// DryRun 只统计不改任何东西：不写目标端、不翻元数据、不删源。生产首轮必须先跑它。
	DryRun bool
	// PruneSource 在 driver 翻转成功之后删掉源端内容。默认保留，那是回滚余量。
	PruneSource bool
}

// BlobMigrateResult 是一次迁移的账目。计数按三个动作阶段分组：内容有没有搬、元数据有没有翻、
// 源端有没有清。出问题时运维要能一眼看出卡在哪一段。
type BlobMigrateResult struct {
	DriverFrom string `json:"driverFrom"`
	DriverTo   string `json:"driverTo"`
	DryRun     bool   `json:"dryRun"`

	// Scanned 是读过的元数据行数，Candidates 是其中归属源后端的（真正要处理的）。
	Scanned    int `json:"scanned"`
	Candidates int `json:"candidates"`
	// SkippedOther 是躺在第三个后端上的行：一次迁移只搬一端，它们留给下一轮。
	SkippedOther int `json:"skippedOtherDriver"`
	// Deleting 是 GC 已认领的行，动它们等于和回收器抢同一个对象。
	Deleting int `json:"deleting"`

	// Present 是目标端已有同 key 同体积内容的行数（上一轮内容搬完没来得及翻元数据就被杀的
	// 残留）。它与 Copied 都随后计入 Flipped。
	Present int `json:"alreadyOnTarget"`
	Copied  int `json:"copied"`
	// Flipped 是真正改了 objects.driver 的行数——只有这个数才代表读路径已经换过去了。
	Flipped int `json:"flipped"`
	// Conflict 是内容已在目标端、但元数据 CAS 未命中（并发迁移或已被 GC 认领）：不是失败，
	// 重跑、或让它跟着 GC 走都是对的。
	Conflict int `json:"conflict"`
	// Unreadable 是源端明确回答「内容不在」的行数。这种行不是「放错了地方」而是本来就没有，
	// 归口是 blobs verify 与备份恢复，因此不计入 Failed——否则一次永久损坏会让迁移永远非零。
	Unreadable int `json:"unreadable"`
	// Failed 是「本该搬成却没搬成」：摘要对不上、目标端体积冲突、写入报错。
	Failed int `json:"failed"`
	// Pruned/PruneFailed 只统计开了 --prune-source 之后的删源动作。
	Pruned      int   `json:"pruned"`
	PruneFailed int   `json:"pruneFailed"`
	Bytes       int64 `json:"bytes"`

	// Failures 是问题样本（key + 人话）：计数管规模，样本管下一步动作。
	Failures []string `json:"failures,omitempty"`
}

// addFailure 收集失败样本，超上限只丢不报错——它们是提示，不是清单。
func (r *BlobMigrateResult) addFailure(msg string) {
	if len(r.Failures) >= maxMigrateSamples {
		return
	}
	r.Failures = append(r.Failures, msg)
}

// BlobMigrator 是后端之间的对象搬运工。它只依赖两个内容后端加平台库，因此不碰服务装配、
// 不碰降级状态机，也不给常驻进程增加任何路由能力。
type BlobMigrator struct {
	src objectstore.Store
	dst objectstore.Store
	st  store.Store
}

// NewBlobMigrator 组装搬运器。两端必须是**不同**的、能存放内容的后端：off 不是一个地点，
// 同端互迁是配置写错了。两者都在这里当场拒掉，而不是跑到一半报「读不到」。
//
// 两端还必须是彼此分开的存放地：dir 后端打开根目录时持独占锁，同一个目录构造两次会直接
// ErrLocked，这正是我们想要的——同一份内容不存在「自己迁给自己」。
func NewBlobMigrator(src, dst objectstore.Store, st store.Store) (*BlobMigrator, error) {
	switch {
	case src == nil || dst == nil:
		return nil, errors.New("service: migration needs both a source and a target backend")
	case st == nil:
		return nil, errors.New("service: migration needs the platform store")
	case !objectstore.ValidDriver(src.Driver()) || !objectstore.ValidDriver(dst.Driver()):
		return nil, fmt.Errorf("service: migration runs between %s and %s, but both ends must be one of db, dir, s3",
			src.Driver(), dst.Driver())
	case src.Driver() == dst.Driver():
		return nil, fmt.Errorf("service: source and target backend are both %q, nothing to migrate",
			src.Driver())
	}
	return &BlobMigrator{src: src, dst: dst, st: st}, nil
}

// Run 逐行处理登记表：判「要不要搬」→ 搬内容并核对 → 翻元数据 → 按需清源。
//
// 返回的 error 只表示**这轮没跑完**（后端不可达、平台库出错）；「有个别对象搬不成」是
// BlobMigrateResult.Failed，不是 error。调用方把两者混成一个错误，就分不清「桶断了」和「这个
// 行本来就是个坏行」，而前者修完环境重跑就好、后者重跑一万次也一样。
func (m *BlobMigrator) Run(ctx context.Context, opts BlobMigrateOptions) (BlobMigrateResult, error) {
	if opts.Category != "" && !objectstore.ValidCategory(opts.Category) {
		return BlobMigrateResult{}, fmt.Errorf("service: migrate category %q not in whitelist %v",
			opts.Category, objectstore.Categories())
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = objectstore.DefaultMaxObjectBytes
	}
	res := BlobMigrateResult{DriverFrom: m.src.Driver(), DriverTo: m.dst.Driver(), DryRun: opts.DryRun}

	rows, err := m.st.Objects().ListByCategory(ctx, opts.Category)
	if err != nil {
		return res, fmt.Errorf("list object registry: %w", err)
	}
	for _, row := range rows {
		// 每轮看一次 ctx：迁移是长任务，Ctrl-C 之后已经翻转的行保持原样即可（幂等）。
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res.Scanned++
		if row.State == store.ObjectStateDeleting {
			res.Deleting++
			continue
		}
		// 一次只搬一端。第三个后端上的行原样留着：objects.driver 是它们唯一的坐标。
		if row.Driver != res.DriverFrom {
			res.SkippedOther++
			continue
		}
		res.Candidates++
		if err := m.moveOne(ctx, row, maxBytes, opts, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// moveOne 处理一个候选对象。返回的 error 一律意味着「中止整轮」，其余分支自己写计数。
func (m *BlobMigrator) moveOne(ctx context.Context, row *store.ObjectMeta, maxBytes int64,
	opts BlobMigrateOptions, res *BlobMigrateResult) error {
	key := row.BlobKey

	// 登记值与 key 里嵌的摘要自相矛盾：这一行不是本包写进去的。搬到目标端只是把一份说不清
	// 来路的字节换个地方存，当场拒掉（与 blobs verify 的 address-mismatch 同口径）。
	if want := objectstore.SHA256OfKey(key); want != "" && row.SHA256 != want {
		res.Failed++
		res.addFailure(fmt.Sprintf("%s: registry digest %s contradicts its own key (%s)", key, row.SHA256, want))
		return nil
	}

	need, err := m.contentOnTarget(ctx, row, res)
	if err != nil {
		return err
	}
	if opts.DryRun {
		// 预览一轮不该产生全量读流量：要搬的行只用 Stat 确认源端读得到。
		if need == copyNeeded {
			return m.probeSource(ctx, key, res)
		}
		return nil
	}
	switch need {
	case copyBlocked:
		// 已经记过账（体积冲突等），元数据一行都不许动。
		return nil
	case copyPresent:
		// 内容已经在目标端了（上一轮就停在「内容已落、driver 未翻」这两步之间）：这一轮欠的
		// 只是那笔元数据翻转，跳过搬运正是幂等的全部含义。
		//
		// 目标是 db 时不能空手翻：正文必须与 driver 同语句提交，所以照样回源端读一份核对过
		// 摘要的字节重写一遍——多写一次同内容对象的代价，换的是「driver=db 而 payload 为空」
		// 永远不可能出现在已提交状态里（Stat 与翻转之间隔着事务，那份 payload 未必还在）。
		if m.dst.Driver() != objectstore.DriverDB {
			return m.commit(ctx, row, nil, opts, res)
		}
	}

	payload, staged, err := m.stageContent(ctx, row, maxBytes, res)
	if err != nil || !staged {
		return err
	}
	return m.commit(ctx, row, payload, opts, res)
}

// copyNeed 是「目标端要不要这份内容」的三态结论。三态而不是布尔，是因为中间那一态既不是
// 成功也不是失败：那一行已经在本函数里记过账了，调用方只要别再动元数据就行。
type copyNeed int

const (
	// copyNeeded 目标端没有这份内容，得真搬。
	copyNeeded copyNeed = iota
	// copyPresent 目标端已有同 key 同体积的内容，只欠元数据翻转。
	copyPresent
	// copyBlocked 已记账（体积冲突等），本行到此为止。
	copyBlocked
)

// contentOnTarget 问目标端「这个 key 你有了吗」。
//
// 已有且体积相符就是上一轮的残留，只欠翻转；已有但体积不符就是目标端被动过——内容寻址下
// 同 key 不同 size 没有合理解释，绝不覆盖，报失败让人来看。
func (m *BlobMigrator) contentOnTarget(ctx context.Context, row *store.ObjectMeta, res *BlobMigrateResult) (copyNeed, error) {
	ref, err := m.dst.Stat(ctx, row.BlobKey)
	switch {
	case err == nil && ref.Size != row.Size:
		res.Failed++
		res.addFailure(fmt.Sprintf("%s: %s already holds %d bytes under this key, registry says %d; refusing to overwrite",
			row.BlobKey, m.dst.Driver(), ref.Size, row.Size))
		return copyBlocked, nil
	case err == nil:
		res.Present++
		return copyPresent, nil
	case errors.Is(err, objectstore.ErrNotFound):
		return copyNeeded, nil
	default:
		// 含 ErrUnavailable：目标端没答上来，这一行到底要不要搬根本无从判断，中止。
		return copyBlocked, fmt.Errorf("stat target object %s on %s: %w", row.BlobKey, m.dst.Driver(), err)
	}
}

// stageContent 把内容落到目标端，并交出需要随元数据同语句提交的正文（非 db 目标恒为 nil）。
//
// staged=false 表示这一行已经记过账，调用方不许再动元数据。
func (m *BlobMigrator) stageContent(ctx context.Context, row *store.ObjectMeta, maxBytes int64,
	res *BlobMigrateResult) ([]byte, bool, error) {
	key := row.BlobKey
	rc, _, err := m.src.Get(ctx, key)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			res.Unreadable++
			res.addFailure(fmt.Sprintf("%s: %s has no content to move; this row is broken, not just misplaced (run k8s-ai blobs verify)",
				key, m.src.Driver()))
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read object %s from %s: %w", key, m.src.Driver(), err)
	}
	defer func() { _ = rc.Close() }()

	if m.dst.Driver() == objectstore.DriverDB {
		// db 目标的正文要进同一条 UPDATE，只能整段驻留，所以天花板就是 db 后端的上限。
		// 撞上限是配置判断错了（这批对象本该去 dir/s3），不是可重试故障，报失败继续下一行。
		payload, sum, err := objectstore.ReadAllHashed(rc, maxBytes)
		if err != nil {
			res.Failed++
			res.addFailure(fmt.Sprintf("%s: read for the %s target: %v", key, m.dst.Driver(), err))
			return nil, false, nil
		}
		// 三个后端里只有 db 会在 Get 里核摘要，dir/s3 交出来的字节必须在这儿自己算一遍。
		if sum != row.SHA256 {
			res.Failed++
			res.addFailure(fmt.Sprintf("%s: source content hashes to %s, registry says %s", key, sum, row.SHA256))
			return nil, false, nil
		}
		res.Copied++
		return payload, true, nil
	}

	// 目标端的 Put 按**实际收到的字节**算 key。ref.Key 与源 key 相等，就同时证明了 sha256、
	// prefix、scope、ext 四件事；不相等就是内容或命名空间不对。这份残留留着交给 GC 当孤儿收：
	// 要安全地删它，得先能证明「这一份确实是本次写入产生的」，为一行残留引入那套判据不值。
	spec, err := objectstore.SpecFromKey(key)
	if err != nil {
		res.Failed++
		res.addFailure(fmt.Sprintf("%s: cannot rebuild a write spec from this key: %v", key, err))
		return nil, false, nil
	}
	ref, err := m.dst.Put(ctx, spec, rc)
	if err != nil {
		res.Failed++
		res.addFailure(fmt.Sprintf("%s: write on %s: %v", key, m.dst.Driver(), err))
		return nil, false, nil
	}
	if ref.Key != key {
		res.Failed++
		res.addFailure(fmt.Sprintf("%s: %s stored those bytes as %s; content or prefix differs, left for gc",
			key, m.dst.Driver(), ref.Key))
		return nil, false, nil
	}
	res.Copied++
	return nil, true, nil
}

// commit 翻元数据（同事务），成功后才按需清源。这一笔是本文件唯一的写库动作。
func (m *BlobMigrator) commit(ctx context.Context, row *store.ObjectMeta, payload []byte,
	opts BlobMigrateOptions, res *BlobMigrateResult) error {
	key := row.BlobKey
	// 源端是 db 时正文就在这一行里，清源必须与翻转走同一条语句：让调用方再喊一次 db 后端
	// Delete，会把 ready 行顺手打成 state=missing——那块牌子写的是「内容丢了」，而这里内容
	// 只是搬到了目标端。
	dropSource := opts.PruneSource && res.DriverFrom == objectstore.DriverDB
	var flipped bool
	if err := m.st.WithTx(ctx, func(tx *store.Tx) error {
		var err error
		flipped, err = tx.AdoptObject(ctx, key, res.DriverFrom, res.DriverTo, payload, dropSource)
		return err
	}); err != nil {
		// 平台库是核心依赖：它出错时整轮都没有意义，立刻中止。此刻源端内容一定还完整在原处。
		return fmt.Errorf("adopt object %s (%s -> %s): %w", key, res.DriverFrom, res.DriverTo, err)
	}
	if !flipped {
		res.Conflict++
		return nil
	}
	res.Flipped++
	res.Bytes += row.Size
	if !opts.PruneSource {
		return nil
	}
	if dropSource {
		// 正文已经在那条 UPDATE 里置空了，源端这一笔就算清了。
		res.Pruned++
		return nil
	}
	return m.pruneSource(ctx, key, res)
}

// probeSource 是 dry-run 下的源端体检：用 Stat 而不是 Get，预览一轮迁移不该产生全量读流量。
func (m *BlobMigrator) probeSource(ctx context.Context, key string, res *BlobMigrateResult) error {
	_, err := m.src.Stat(ctx, key)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, objectstore.ErrNotFound):
		res.Unreadable++
		res.addFailure(fmt.Sprintf("%s: %s has no content to move", key, m.src.Driver()))
		return nil
	default:
		return fmt.Errorf("stat source object %s on %s: %w", key, m.src.Driver(), err)
	}
}

// pruneSource 删源端内容。前提已经是 driver 翻转成功，所以这一步失败不影响正确性，只影响
// 源端还占着多少空间——报个数就够，不中止整轮。
func (m *BlobMigrator) pruneSource(ctx context.Context, key string, res *BlobMigrateResult) error {
	err := m.src.Delete(ctx, key)
	switch {
	case err == nil || errors.Is(err, objectstore.ErrNotFound):
		res.Pruned++
		return nil
	case errors.Is(err, objectstore.ErrUnavailable):
		return fmt.Errorf("prune object %s on %s: %w", key, m.src.Driver(), err)
	default:
		res.PruneFailed++
		res.addFailure(fmt.Sprintf("%s: prune source: %v", key, err))
		return nil
	}
}
