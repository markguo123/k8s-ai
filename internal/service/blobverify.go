package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// 本文件是 `blobs verify` 的实现（方案 15 §12.1、§12.2），也是恢复流程的准出闸门。
//
// 它守的不变量只有一句话：**每个 ready 的 objects 行，后端里有同 key、同 size、同 sha256
// 的内容**。破坏它的四种现实路径（桶侧被删、库恢复点晚于桶、库恢复点早于桶、写路径 bug）
// 里，只有「引用在、内容不在」是不可修复的，所以对账的全部设计都围绕一件事：
//
//	宁可不结论，也不能误判。
//
// 三条从这条原则推出的硬规矩：
//
//  1. **ErrUnavailable 一律中止**。后端没答上来时把行标成 missing，等于用一次网络抖动把
//     健康对象判成永久丢失（§4.3 区分两个哨兵的全部意义就在这）。因此「标记缺失」这件事
//     放在整个前向扫描成功之后才做，中途任何一次不可达都不会留下一行被误判的状态。
//  2. **driver 与当前后端不符的行只报「待迁移」**。那些内容躺在别的后端里，在这里看不见
//     内容是预期行为，不是丢失；判它们 missing 会直接毁掉 `blobs migrate` 唯一的线索。
//  3. **state=deleting 的行跳过**。GC 正在两段式回收它，内容此刻本来就可能已经没了，
//     对账没有资格把一次正常回收读成数据损坏。
//
// ETag 在这里没有位置：S3 单次 PUT 的 ETag 是 MD5、分片上传是 <md5>-N，都不等于我们的
// sha256，--deep 只能把正文下载下来重算（§12.2）。

// VerifyOptions 是一次对账的参数。
type VerifyOptions struct {
	// Category 只扫一类对象（空 = 全表）。它只影响**前向**扫描；反向差集始终对着全表求，
	// 否则 --category reports 会把 exports 的合法对象看成孤儿。
	Category string
	// Prefix 是反向扫描（桶 -> 表）的命名空间，同 gc，见 §9.3 第 4 条。
	Prefix string
	// Deep 额外下载正文重算 sha256。默认只 Stat 比 size：HeadObject 的代价允许日常全量跑，
	// 全量读流量只该出现在恢复之后或抽查时。
	Deep bool
}

// VerifyResult 是一次对账的账目。计数分组的方式就是运维的阅读顺序：先看行数与一致性，
// 再看「待迁移」这种合法差异，最后看桶侧孤儿。
type VerifyResult struct {
	Driver string `json:"driver"`
	Deep   bool   `json:"deep"`

	// Rows 是前向扫描检查过的元数据行数（含被跳过的待迁移/回收中行）。
	Rows int `json:"rows"`
	OK   int `json:"ok"`
	// SizeMismatch 是「内容在、但字节数和登记的不一样」；Missing 是后端明确回答
	// ErrNotFound。前者多半是写坏了的副本，后者才是 §12.1 说的悬空引用。
	SizeMismatch int `json:"sizeMismatch"`
	Missing      int `json:"missing"`
	// DeepChecked 是真正下载并重算过摘要的行数；ChecksumMismatch 是算出来的 sha256
	// 与登记值不符——size 相同、内容不同的静默损坏，只有 --deep 看得见。
	DeepChecked      int `json:"deepChecked"`
	ChecksumMismatch int `json:"checksumMismatch"`
	// AddressMismatch 是 objects.sha256 与 key 里那个摘要不一致：这一行不是本包写出来的，
	// 内容检查对它没有意义，因此单独计数并跳过后续判定。
	AddressMismatch int `json:"addressMismatch"`
	// ForeignDriver 是内容躺在别的后端里的行数：合法中间态，交给 blobs migrate。
	ForeignDriver int `json:"pendingMigration"`
	// Deleting 是正在被 GC 认领的行，本轮不判定。
	Deleting int `json:"deleting"`
	// RowsMarkedMissing 是这次被写成 state=missing 的行数（只在整轮扫描都成功后才落笔）。
	RowsMarkedMissing int `json:"rowsMarkedMissing"`

	// 反向扫描（与 gc 共用 BuildInventory 的同一份口径）。
	BucketKeys  int   `json:"bucketKeys"`
	Orphans     int   `json:"orphans"`
	OrphanBytes int64 `json:"orphanBytes"`
	Foreign     int   `json:"foreign"`

	// 每类问题最多留几个 key 供人直接上手，计数负责规模、样本负责下一步动作。
	SizeMismatchKeys     []string `json:"sizeMismatchKeys,omitempty"`
	MissingKeys          []string `json:"missingKeys,omitempty"`
	ChecksumMismatchKeys []string `json:"checksumMismatchKeys,omitempty"`
	AddressMismatchKeys  []string `json:"addressMismatchKeys,omitempty"`
}

// maxVerifySamples 限制样本条数：对账可能一次点出上千行，全打出来等于没打。
const maxVerifySamples = 5

// Inconsistent 给出「恢复准出」的答案：待迁移与回收中行是合法中间态，不计入。
// 反向孤儿计入——§12.2 明确要求 --strict 对它同样非零退出。
func (r VerifyResult) Inconsistent() int {
	return r.SizeMismatch + r.Missing + r.ChecksumMismatch + r.AddressMismatch + r.Orphans
}

// BlobVerifier 是对账器。和 BlobGC 一样只依赖内容后端 + 元数据登记表两个既有接口，
// 因此 CLI、恢复脚本、将来的定时巡检都能原样复用同一套判定。
type BlobVerifier struct {
	backend objectstore.Store
	objects store.ObjectStore
}

// NewBlobVerifier 组装对账器，两侧都不能为 nil。
func NewBlobVerifier(backend objectstore.Store, objects store.ObjectStore) (*BlobVerifier, error) {
	if backend == nil {
		return nil, errors.New("service: verifier needs a content backend")
	}
	if objects == nil {
		return nil, errors.New("service: verifier needs the object registry")
	}
	return &BlobVerifier{backend: backend, objects: objects}, nil
}

// Run 跑一轮对账：前向「库 -> 桶」逐行核实，再反向「桶 -> 库」求差集。
//
// 返回值里的 error 只表示**这轮没跑完**（后端不可达、库读失败），不表示「发现不一致」——
// 后者是 VerifyResult 里的计数。调用方把两者混成一个错误，就没法区分「桶坏了」和
// 「数据坏了」，而这两种情况的处置动作完全相反。
func (v *BlobVerifier) Run(ctx context.Context, opts VerifyOptions) (VerifyResult, error) {
	if opts.Category != "" && !objectstore.ValidCategory(opts.Category) {
		return VerifyResult{}, fmt.Errorf("service: verify category %q not in whitelist %v",
			opts.Category, objectstore.Categories())
	}
	prefix, err := objectstore.NormalizePrefix(opts.Prefix)
	if err != nil {
		return VerifyResult{}, err
	}
	res := VerifyResult{Driver: v.backend.Driver(), Deep: opts.Deep}

	rows, err := v.objects.ListByCategory(ctx, opts.Category)
	if err != nil {
		return res, fmt.Errorf("list object registry: %w", err)
	}
	var toMark []string
	for _, row := range rows {
		res.Rows++
		// 内容不归本后端管：看不见是预期，交给 blobs migrate，不判 missing（§10.1）。
		if row.Driver != v.backend.Driver() {
			res.ForeignDriver++
			continue
		}
		if row.State == store.ObjectStateDeleting {
			res.Deleting++
			continue
		}
		// 登记值与 key 里的寻址摘要不符：这一行不是本包写的，内容判定对它毫无意义。
		if want := objectstore.SHA256OfKey(row.BlobKey); want != "" && row.SHA256 != want {
			res.AddressMismatch++
			res.AddressMismatchKeys = addSample(res.AddressMismatchKeys, row.BlobKey)
			continue
		}
		ref, err := v.backend.Stat(ctx, row.BlobKey)
		switch {
		case errors.Is(err, objectstore.ErrNotFound):
			res.Missing++
			res.MissingKeys = addSample(res.MissingKeys, row.BlobKey)
			toMark = append(toMark, row.BlobKey)
		case err != nil:
			// 含 ErrUnavailable：此刻后端不可达，任何「内容没了」的结论都没有资格下。
			return res, fmt.Errorf("stat object %s: %w", row.BlobKey, err)
		case ref.Size != row.Size:
			res.SizeMismatch++
			res.SizeMismatchKeys = addSample(res.SizeMismatchKeys, row.BlobKey)
		case !opts.Deep:
			res.OK++
		default:
			if err := v.deepCheck(ctx, row, &res); err != nil {
				return res, err
			}
		}
	}

	// 落笔必须在整轮扫描之后：中途因不可达返回时，上面只攒了样本、一行都没改。
	if len(toMark) > 0 {
		if err := v.objects.MarkMissing(ctx, toMark); err != nil {
			return res, fmt.Errorf("mark objects missing: %w", err)
		}
		res.RowsMarkedMissing = len(toMark)
	}

	inventory, err := BuildInventory(ctx, v.backend, v.objects, prefix)
	if err != nil {
		return res, err
	}
	res.BucketKeys = inventory.ListedKeys
	res.Foreign = len(inventory.Foreign)
	for _, ref := range inventory.Orphans {
		res.Orphans++
		res.OrphanBytes += ref.Size
	}
	return res, nil
}

// deepCheck 下载正文重算 sha256。流式算，绝不为了校验把 256MiB 报告读进内存。
func (v *BlobVerifier) deepCheck(ctx context.Context, row *store.ObjectMeta, res *VerifyResult) error {
	rc, _, err := v.backend.Get(ctx, row.BlobKey)
	if err != nil {
		if errors.Is(err, objectstore.ErrNotFound) {
			// Stat 与 Get 之间内容消失（并发 GC/人工删除）：按缺失处理，不算中止。
			res.Missing++
			res.MissingKeys = addSample(res.MissingKeys, row.BlobKey)
			return nil
		}
		return fmt.Errorf("read object %s: %w", row.BlobKey, err)
	}
	defer func() { _ = rc.Close() }()

	hasher := sha256.New()
	n, err := io.Copy(hasher, rc)
	if err != nil {
		return fmt.Errorf("hash object %s: %w", row.BlobKey, err)
	}
	res.DeepChecked++
	sum := hex.EncodeToString(hasher.Sum(nil))
	switch {
	case sum != row.SHA256:
		res.ChecksumMismatch++
		res.ChecksumMismatchKeys = addSample(res.ChecksumMismatchKeys, row.BlobKey)
	case n != row.Size:
		res.SizeMismatch++
		res.SizeMismatchKeys = addSample(res.SizeMismatchKeys, row.BlobKey)
	default:
		res.OK++
	}
	return nil
}

// addSample 收集样本 key，超过上限只丢不报错——它们是提示，不是清单。
func addSample(keys []string, key string) []string {
	if len(keys) >= maxVerifySamples {
		return keys
	}
	return append(keys, key)
}
