package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// maxDBPayloadBytes 是 objects.payload 的体积天花板：MySQL 侧 colBytes 渲染成
// MEDIUMBLOB（16MiB-1），再大就要改 LONGBLOB，而 db 后端的定位是「兼容/兜底」，不该
// 承担大对象。SQLite 的 BLOB 没有实际上限，因此这个值是两驱动的公共上界。
const maxDBPayloadBytes int64 = 1<<24 - 1

// ObjectContentLimit 把配置里的 platform.blobs.max_object_size 收敛成 db 后端实际
// 可用的上限：<=0 取默认 32MiB，超过 MEDIUMBLOB 上限的按 MEDIUMBLOB 收紧。
//
// 装配层拿它和配置值比一次，不相等时打 WARN，告诉运维「配置写的是 64MiB，但 db 后端
// 只吃 16MiB-1，要放大请换 dir/s3」——静默按更小的值执行是最难排查的那种配置失效。
func ObjectContentLimit(maxBytes int64) int64 {
	if maxBytes <= 0 {
		maxBytes = objectstore.DefaultMaxObjectBytes
	}
	if maxBytes > maxDBPayloadBytes {
		return maxDBPayloadBytes
	}
	return maxBytes
}

// objectContent 是 driver=db 的对象内容后端：正文放在 objects.payload。
//
// 它是唯一「内容与元数据同表」的后端，也因此是唯一能区分「行还在、正文没了」的后端：
// Delete 只把 payload 置 NULL，行留给 Forget。download 路径正是靠这一点把行标成
// missing（方案 15 §7），内容与元数据分离的 dir/s3 后端做不到，只能整对象消失。
type objectContent struct {
	s        *dbStore
	prefix   string
	maxBytes int64
}

// newObjectContent 装配 db 内容后端。prefix 必须已规范化（见 Store.ObjectContent 与
// objectstore.NormalizePrefix）。
func newObjectContent(s *dbStore, prefix string, maxBytes int64) *objectContent {
	if prefix == "" {
		prefix = objectstore.DefaultPrefix
	}
	return &objectContent{s: s, prefix: prefix, maxBytes: ObjectContentLimit(maxBytes)}
}

// Driver 返回 db。
func (c *objectContent) Driver() string { return objectstore.DriverDB }

// Close 不关库：连接由 Store 持有，后端只是它的一个视图。
func (c *objectContent) Close() error { return nil }

// contentRow 是内容后端关心的一行（含 payload 是否还在）。
type contentRow struct {
	sha256         string
	size           int64
	mediaType      string
	driver         string
	state          string
	updatedAt      time.Time
	payloadPresent bool
}

func (r contentRow) ref(key string) objectstore.Ref {
	return objectstore.Ref{Key: key, SHA256: r.sha256, Size: r.size, MediaType: r.mediaType, UpdatedAt: r.updatedAt}
}

func (c *objectContent) lookup(ctx context.Context, key string) (contentRow, bool, error) {
	var row contentRow
	err := c.s.db.QueryRowContext(ctx, `
		SELECT sha256, size, media_type, driver, state, updated_at,
		       CASE WHEN payload IS NULL THEN 0 ELSE 1 END
		FROM objects WHERE blob_key = ?`, key).
		Scan(&row.sha256, &row.size, &row.mediaType, &row.driver, &row.state, &row.updatedAt, &row.payloadPresent)
	if errors.Is(err, sql.ErrNoRows) {
		return contentRow{}, false, nil
	}
	if err != nil {
		return contentRow{}, false, fmt.Errorf("lookup object %s: %w", key, err)
	}
	return row, true, nil
}

// Put 把正文整段读进内存（db 后端要写单行），按实际摘要组装 key 后落库。
//
// 同内容重复 Put 走幂等分支直接返回既有 Ref；三种非幂等情形都明确报错而不是悄悄覆盖：
//   - 同 key 但库里 sha256 不同：内容寻址不变量被破坏（key 是摘要算出来的，正常路径
//     不可能撞出不同内容），继续写会把既有对象的内容偷换掉。
//   - 同 key 但 driver 不是 db：正文在 dir/桶里。跨后端搬迁是运维动作，不是 Put 的副作用。
//
// 两种情形会走到「重写正文」这一支（末段 UPDATE）：
//   - 正文为 NULL：被 Delete 摘掉但行还没 Forget，按同内容补写。
//   - state=missing：verify/下载路径已经用 Get 的摘要校验判定正文坏了。此刻 payload 可能
//     还非空（库外改动、备份只回来半张表），若只看「payload 在不在」就早退，运维照着提示
//     重传同一份文件会「成功但什么也没修」——这是引用式存储最难查的一类假成功。代价只是
//     被标记过的对象多写一次，热路径（ready 且正文在）仍然零额外读取。
func (c *objectContent) Put(ctx context.Context, spec objectstore.Spec, r io.Reader) (objectstore.Ref, error) {
	payload, sum, err := objectstore.ReadAllHashed(r, c.maxBytes)
	if err != nil {
		return objectstore.Ref{}, err
	}
	ext, err := objectstore.NormalizeExt(spec.Ext)
	if err != nil {
		return objectstore.Ref{}, err
	}
	key, err := objectstore.BuildKey(c.prefix, spec.Category, spec.Scope, sum, ext)
	if err != nil {
		return objectstore.Ref{}, err
	}
	mediaType := objectstore.ResolveMediaType(spec.MediaType, ext)
	size := int64(len(payload))

	row, found, err := c.lookup(ctx, key)
	if err != nil {
		return objectstore.Ref{}, err
	}
	if !found {
		meta := &ObjectMeta{
			BlobKey:   key,
			SHA256:    sum,
			Size:      size,
			MediaType: mediaType,
			Category:  spec.Category,
			Driver:    objectstore.DriverDB,
			State:     ObjectStateReady,
			CreatedBy: spec.CreatedBy,
		}
		insErr := insertObject(ctx, c.s.db, meta, payload)
		if insErr == nil {
			now := time.Now()
			return objectstore.Ref{Key: key, SHA256: sum, Size: size, MediaType: mediaType, UpdatedAt: now}, nil
		}
		if !errors.Is(insErr, ErrExists) {
			return objectstore.Ref{}, insErr
		}
		// 并发写入同一内容：另一笔事务抢先落了行，回到统一的「已存在」判定。
		if row, found, err = c.lookup(ctx, key); err != nil {
			return objectstore.Ref{}, err
		}
		if !found {
			return objectstore.Ref{}, fmt.Errorf("objectstore: object %s vanished after insert conflict", key)
		}
	}
	if row.sha256 != sum {
		return objectstore.Ref{}, fmt.Errorf("%w: object %s holds digest %s", objectstore.ErrChecksum, key, row.sha256)
	}
	if row.driver != objectstore.DriverDB {
		return objectstore.Ref{}, fmt.Errorf("%w: object %s is held by driver %q, db backend will not relocate it",
			objectstore.ErrState, key, row.driver)
	}
	if row.payloadPresent && row.state != ObjectStateMissing {
		return row.ref(key), nil
	}
	now := time.Now()
	if _, err := c.s.db.ExecContext(ctx, `
		UPDATE objects SET payload = ?, state = CASE WHEN state = ? THEN ? ELSE state END, updated_at = ?
		 WHERE blob_key = ?`, payload, ObjectStateMissing, ObjectStateReady, now, key); err != nil {
		return objectstore.Ref{}, fmt.Errorf("restore object payload %s: %w", key, err)
	}
	return objectstore.Ref{Key: key, SHA256: sum, Size: size, MediaType: row.mediaType, UpdatedAt: now}, nil
}

// Get 取正文，并做本后端唯一的正文级校验：实际摘要与登记摘要不一致即 ErrChecksum。
//
// 这是 ErrChecksum 在 Put 之外的真实可达点——Put 是同一次写入自算自存的，几乎不可能
// 不匹配；正文被库外手段改动（误 UPDATE、备份恢复回滚了半张表）只有读的时候才发现。
func (c *objectContent) Get(ctx context.Context, key string) (io.ReadCloser, objectstore.Ref, error) {
	if err := objectstore.ValidateKey(key); err != nil {
		return nil, objectstore.Ref{}, err
	}
	var (
		row     contentRow
		payload []byte
	)
	err := c.s.db.QueryRowContext(ctx, `
		SELECT sha256, size, media_type, updated_at, payload
		FROM objects WHERE blob_key = ?`, key).
		Scan(&row.sha256, &row.size, &row.mediaType, &row.updatedAt, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, objectstore.Ref{}, fmt.Errorf("%w: object %s", objectstore.ErrNotFound, key)
	}
	if err != nil {
		return nil, objectstore.Ref{}, fmt.Errorf("read object %s: %w", key, err)
	}
	if payload == nil {
		return nil, objectstore.Ref{}, fmt.Errorf("%w: object %s metadata remains but payload is gone",
			objectstore.ErrNotFound, key)
	}
	if sum := sha256Hex(payload); sum != row.sha256 {
		return nil, objectstore.Ref{}, fmt.Errorf("%w: object %s holds %s but digest is %s",
			objectstore.ErrChecksum, key, row.sha256, sum)
	}
	return io.NopCloser(bytes.NewReader(payload)), row.ref(key), nil
}

// Stat 只取元数据；正文为 NULL 时返回 ErrNotFound，让调用方去把行标 missing。
func (c *objectContent) Stat(ctx context.Context, key string) (objectstore.Ref, error) {
	if err := objectstore.ValidateKey(key); err != nil {
		return objectstore.Ref{}, err
	}
	row, found, err := c.lookup(ctx, key)
	if err != nil {
		return objectstore.Ref{}, err
	}
	if !found || !row.payloadPresent {
		return objectstore.Ref{}, fmt.Errorf("%w: object %s", objectstore.ErrNotFound, key)
	}
	return row.ref(key), nil
}

// Delete 只把正文置 NULL 并打逻辑删除标记，元数据行留给 Forget。
//
// state 只在 ready 时改成 missing：ready 意味着还有活引用、正文却没了，那正是 missing 的
// 定义；orphaned/deleting 是 GC 的地盘，改回去会让已认领的对象重新变成回收候选、
// 或者把诊断态抹平成「等回收」。deleted_at 用 COALESCE 保住第一次的宽限期基准。
//
// 幂等：行在、正文已经没了，就直接成功。GC 在「删正文」与「Forget 行」之间被杀，
// 重跑必须能继续往下走。
func (c *objectContent) Delete(ctx context.Context, key string) error {
	if err := objectstore.ValidateKey(key); err != nil {
		return err
	}
	row, found, err := c.lookup(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: object %s", objectstore.ErrNotFound, key)
	}
	if !row.payloadPresent {
		return nil
	}
	now := time.Now()
	_, err = c.s.db.ExecContext(ctx, `
		UPDATE objects SET payload = NULL, deleted_at = COALESCE(deleted_at, ?), updated_at = ?,
		                   state = CASE WHEN state = ? THEN ? ELSE state END
		 WHERE blob_key = ?`, now, now, ObjectStateReady, ObjectStateMissing, key)
	if err != nil {
		return fmt.Errorf("delete object payload %s: %w", key, err)
	}
	return nil
}

// List 按 key 前缀流式回调，只列正文还在的行。
//
// 刻意不用 LIKE：两驱动的 LIKE 转义与默认 collation 都不一样（ESCAPE 子句、大小写敏感
// 性），而 blob_key 是主键，`>= prefix` 走主键序扫描，再用 Go 侧 HasPrefix 收尾，
// 既免转义又跨驱动确定。命中第一条不属于该前缀的 key 即停，不整表扫完。
func (c *objectContent) List(ctx context.Context, prefix string, fn func(objectstore.Ref) error) error {
	if err := objectstore.ValidateListPrefix(prefix); err != nil {
		return err
	}
	rows, err := c.s.db.QueryContext(ctx, `
		SELECT blob_key, sha256, size, media_type, updated_at
		FROM objects WHERE blob_key >= ? AND payload IS NOT NULL ORDER BY blob_key`, prefix)
	if err != nil {
		return fmt.Errorf("list objects under %s: %w", prefix, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key string
			row contentRow
		)
		if err := rows.Scan(&key, &row.sha256, &row.size, &row.mediaType, &row.updatedAt); err != nil {
			return fmt.Errorf("scan object list: %w", err)
		}
		if !strings.HasPrefix(key, prefix) {
			break
		}
		if err := fn(row.ref(key)); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Ensure 是启动期就绪保障：db 后端要的结构只有 objects 表，它在 schema v6 里建。
// 表不在说明没跑迁移，直接失败并把命令写进错误里，不带病启动。
func (c *objectContent) Ensure(ctx context.Context) error {
	if err := c.pingTable(ctx); err != nil {
		return fmt.Errorf("object content backend (driver=db): %w", err)
	}
	return nil
}

// Probe 只读探测，供健康端点用；不进 /readyz（方案 15 §11.1）。
func (c *objectContent) Probe(ctx context.Context) error {
	if err := c.pingTable(ctx); err != nil {
		return fmt.Errorf("object content backend (driver=db): %w", err)
	}
	return nil
}

func (c *objectContent) pingTable(ctx context.Context) error {
	if _, err := c.s.db.ExecContext(ctx, `SELECT blob_key FROM objects LIMIT 1`); err != nil {
		if isMissingTable(err) {
			return fmt.Errorf("objects table missing: run k8s-ai db migrate (%w)", err)
		}
		return fmt.Errorf("probe objects table: %w", err)
	}
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// 编译期确认 db 后端三个接口都满足（Presigner 刻意不实现：库里存的东西没有可签发的
// 外部 URL，下载一律走服务端流式端点）。
var (
	_ objectstore.Store      = (*objectContent)(nil)
	_ objectstore.Maintainer = (*objectContent)(nil)
	_ objectstore.Probe      = (*objectContent)(nil)
)
