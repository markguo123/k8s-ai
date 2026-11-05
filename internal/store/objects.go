package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// objects.state 取值（方案 15 §7）。四个状态只回答一件事：MySQL 这一行还是不是活的。
//
// ready    活对象：至少一个业务行引用它，或刚写入尚未挂引用
// orphaned 逻辑删除：引用归零，进入宽限期等 GC（deleted_at 是宽限期基准）
// missing  诊断态：引用还在但内容读不到，业务行与引用关系全部保留
// deleting GC 已 CAS 认领，即将物理删除；此时 Acquire 必须返回 ErrState
const (
	ObjectStateReady    = "ready"
	ObjectStateOrphaned = "orphaned"
	ObjectStateMissing  = "missing"
	ObjectStateDeleting = "deleting"
)

// objectColumns 是 objects 表的列清单，刻意不含 payload：元数据读写一律不碰正文。
// 单源在此处，所有 objects 表的 SELECT 复用同一串列名——列序在三处各写一遍是这个表
// 最可能出现的漂移点。
const objectColumns = "blob_key, sha256, size, media_type, category, driver, " +
	"ref_count, state, deleted_at, created_by, created_at, updated_at"

// ObjectMeta 是 objects 表的一行（对象元数据登记表，不含正文）。
type ObjectMeta struct {
	BlobKey   string     `json:"blobKey"`
	SHA256    string     `json:"sha256"`
	Size      int64      `json:"size"`
	MediaType string     `json:"mediaType,omitempty"`
	Category  string     `json:"category"`
	Driver    string     `json:"driver"`
	RefCount  int64      `json:"refCount"`
	State     string     `json:"state"`
	DeletedAt *time.Time `json:"deletedAt,omitempty"`
	CreatedBy string     `json:"createdBy,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// Ref 返回可直接交给内容后端/下载端点的自描述元数据。
func (m *ObjectMeta) Ref() objectstore.Ref {
	return objectstore.Ref{
		Key:       m.BlobKey,
		SHA256:    m.SHA256,
		Size:      m.Size,
		MediaType: m.MediaType,
		UpdatedAt: m.UpdatedAt,
	}
}

// ValidObjectState 报告状态取值是否合法（写入方与对账共用）。
func ValidObjectState(state string) bool {
	switch state {
	case ObjectStateReady, ObjectStateOrphaned, ObjectStateMissing, ObjectStateDeleting:
		return true
	}
	return false
}

// ObjectStore 是对象元数据登记表的读取与生命周期入口（方案 15 §6.5）。
//
// Acquire/Release 刻意不在这里：它们必须与业务行同事务，只能从 WithTx 拿到的 Tx 上调
// （Tx 的构造未导出，编译期就挡掉了「脱离事务改引用计数」）。
type ObjectStore interface {
	// Register 登记一个刚写入的对象。同 key 重复登记只刷新体积/媒体类型/updated_at，
	// 不重置 ref_count 与 state——Put 幂等重放不得把已挂引用的对象打回孤儿。
	Register(ctx context.Context, m *ObjectMeta) error
	// Get 按 key 取元数据（不含正文）。
	Get(ctx context.Context, key string) (*ObjectMeta, error)
	// ListByCategory 列出某类对象元数据；category 为空列出全部。按 key 排序，
	// 不按时间排序：objects 上没有时间索引，按 blob_key 走主键序是确定且免排序的。
	ListByCategory(ctx context.Context, category string) ([]*ObjectMeta, error)
	// ListCollectable 返回 deleted_at 早于 olderThan 且已无引用的 GC 候选。
	//
	// 两种状态都算候选：`orphaned`（引用归零，等宽限期到期物理回收）与 `missing` 且
	// ref_count=0（内容已确认不在，只剩一行墓碑）。后者必须一起收：`blobs verify` 会把
	// 读不到内容的行标成 missing，若候选只认 orphaned，这些行就永远收不掉，objects 表
	// 只会越积越厚。仍被引用的 missing 行不在候选里——那是待修的损坏，不是待清的垃圾。
	ListCollectable(ctx context.Context, olderThan time.Time) ([]*ObjectMeta, error)
	// ClaimDeleting 是 GC 的两段式认领：只有 state/ref_count/deleted_at 三个条件全中
	// 才置 deleting，返回 false 表示本轮跳过。候选状态与 ListCollectable 严格一致，两边
	// 条件不一样就会出现「扫得到、认领不了」的空转轮次。禁止先 SELECT 再 UPDATE——GC 与
	// Acquire 并发时那是个真实的丢引用窗口。
	ClaimDeleting(ctx context.Context, key string, refCount int64, olderThan time.Time) (bool, error)
	// MarkMissing 把读不到内容的对象标成诊断态（只接受 ready/orphaned 的行）。
	MarkMissing(ctx context.Context, keys []string) error
	// Forget 删除元数据行（只删 state=deleting 的行），返回实际删除数。
	// 行没了之后内容若还在，由桶扫描差集回收（§9.2）。
	Forget(ctx context.Context, keys []string) (int, error)
	// ObjectStats 按 driver 聚合存活对象的个数与字节数（P8.5，方案 15 §10.3/§13.4）：
	// 启动期用它核对「库里还有没有别的后端的对象」，健康端点用它报体量。
	ObjectStats(ctx context.Context) ([]ObjectDriverStat, error)
}

// ObjectDriverStat 是单个后端的对象体量聚合（不含正在 GC 的行）。
type ObjectDriverStat struct {
	Driver  string `json:"driver"`
	Objects int64  `json:"objects"`
	Bytes   int64  `json:"bytes"`
}

type objectStore struct{ s *dbStore }

func (st *objectStore) Register(ctx context.Context, m *ObjectMeta) error {
	return registerObject(ctx, st.s.db, st.s.dialect, m)
}

func (st *objectStore) Get(ctx context.Context, key string) (*ObjectMeta, error) {
	return getObject(ctx, st.s.db, key)
}

func (st *objectStore) ListByCategory(ctx context.Context, category string) ([]*ObjectMeta, error) {
	query := `SELECT ` + objectColumns + ` FROM objects`
	var args []any
	if category != "" {
		if !objectstore.ValidCategory(category) {
			return nil, fmt.Errorf("%w: category %q", objectstore.ErrKeyInvalid, category)
		}
		query += ` WHERE category = ?`
		args = append(args, category)
	}
	query += ` ORDER BY blob_key`
	return listObjects(ctx, st.s.db, query, args)
}

func (st *objectStore) ListCollectable(ctx context.Context, olderThan time.Time) ([]*ObjectMeta, error) {
	return listObjects(ctx, st.s.db,
		`SELECT `+objectColumns+` FROM objects
		 WHERE ref_count = 0 AND deleted_at IS NOT NULL AND deleted_at < ?
		   AND (state = ? OR (state = ? AND ref_count = 0))
		 ORDER BY deleted_at`,
		[]any{olderThan, ObjectStateOrphaned, ObjectStateMissing})
}

func (st *objectStore) ClaimDeleting(ctx context.Context, key string, refCount int64, olderThan time.Time) (bool, error) {
	if err := objectstore.ValidateKey(key); err != nil {
		return false, err
	}
	res, err := st.s.db.ExecContext(ctx,
		`UPDATE objects SET state = ?, updated_at = ?
		 WHERE blob_key = ? AND ref_count = ? AND deleted_at IS NOT NULL AND deleted_at < ?
		   AND (state = ? OR (state = ? AND ref_count = 0))`,
		ObjectStateDeleting, time.Now(), key, refCount, olderThan,
		ObjectStateOrphaned, ObjectStateMissing)
	if err != nil {
		return false, fmt.Errorf("claim object %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claim object %s: %w", key, err)
	}
	return n > 0, nil
}

func (st *objectStore) MarkMissing(ctx context.Context, keys []string) error {
	for _, key := range keys {
		if err := objectstore.ValidateKey(key); err != nil {
			return err
		}
		res, err := st.s.db.ExecContext(ctx,
			`UPDATE objects SET state = ?, updated_at = ?
			 WHERE blob_key = ? AND state IN (?, ?)`,
			ObjectStateMissing, time.Now(), key, ObjectStateReady, ObjectStateOrphaned)
		if err != nil {
			return fmt.Errorf("mark object %s missing: %w", key, err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("mark object %s missing: %w", key, err)
		} else if n == 0 {
			// 行不存在，或已在 deleting/missing：对账是可重复的，逐条报错会让整轮 verify
			// 卡在一个已经处理过的 key 上，因此这里只把「行确实不存在」当成错误上报。
			if _, err := getObject(ctx, st.s.db, key); err != nil {
				return err
			}
		}
	}
	return nil
}

func (st *objectStore) Forget(ctx context.Context, keys []string) (int, error) {
	var done int
	for _, key := range keys {
		if err := objectstore.ValidateKey(key); err != nil {
			return done, err
		}
		res, err := st.s.db.ExecContext(ctx,
			`DELETE FROM objects WHERE blob_key = ? AND state = ?`, key, ObjectStateDeleting)
		if err != nil {
			return done, fmt.Errorf("forget object %s: %w", key, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return done, fmt.Errorf("forget object %s: %w", key, err)
		}
		done += int(n)
	}
	return done, nil
}

// ---- 共用实现（非事务入口与 Tx 复用同一批 SQL） ----

// execer 是 *sql.DB 与 *sql.Tx 的共同面。objects 的读写只写一份，事务路径与非事务
// 路径共用同一条语句。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// scanner 覆盖 *sql.Row 与 *sql.Rows 的 Scan。
type scanner interface {
	Scan(dest ...any) error
}

func registerObject(ctx context.Context, ex execer, d dialect, m *ObjectMeta) error {
	if m == nil {
		return fmt.Errorf("register object: nil metadata")
	}
	if err := objectstore.ValidateKey(m.BlobKey); err != nil {
		return err
	}
	if m.Category == "" {
		m.Category = objectstore.CategoryOf(m.BlobKey)
	}
	if m.State == "" {
		m.State = ObjectStateReady
	}
	if !ValidObjectState(m.State) {
		return fmt.Errorf("register object %s: unknown state %q", m.BlobKey, m.State)
	}
	if m.CreatedBy == "" {
		m.CreatedBy = objectstore.SystemActor
	}
	if m.RefCount < 0 {
		return fmt.Errorf("register object %s: negative ref_count %d", m.BlobKey, m.RefCount)
	}
	now := time.Now()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	cols := []string{"blob_key", "sha256", "size", "media_type", "category", "driver",
		"ref_count", "state", "deleted_at", "created_by", "created_at", "updated_at"}
	// 冲突时更新的列里刻意没有 ref_count / state / created_at：重复登记是幂等重放，
	// 不能把已挂引用的对象重置。
	query := d.upsert("objects", cols, []string{"blob_key"},
		[]string{"sha256", "size", "media_type", "category", "driver", "updated_at"})
	_, err := ex.ExecContext(ctx, query,
		m.BlobKey, m.SHA256, m.Size, m.MediaType, m.Category, m.Driver,
		m.RefCount, m.State, nilTime(m.DeletedAt), m.CreatedBy, m.CreatedAt, m.UpdatedAt)
	return mapErr(err, "register object")
}

// insertObject 插入 objects 行。它是这张表 INSERT 语句的唯一出处：带不带正文只差
// payload 一个实参（db 内容后端传正文，Acquire 传 nil），拆成两条语句迟早一条忘了改。
func insertObject(ctx context.Context, ex execer, m *ObjectMeta, payload []byte) error {
	if m.Category == "" {
		m.Category = objectstore.CategoryOf(m.BlobKey)
	}
	if m.State == "" {
		m.State = ObjectStateReady
	}
	if m.CreatedBy == "" {
		m.CreatedBy = objectstore.SystemActor
	}
	now := time.Now()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	_, err := ex.ExecContext(ctx, `
		INSERT INTO objects (blob_key, sha256, size, media_type, category, driver,
		                     ref_count, state, deleted_at, created_by, payload, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.BlobKey, m.SHA256, m.Size, m.MediaType, m.Category, m.Driver,
		m.RefCount, m.State, nilTime(m.DeletedAt), m.CreatedBy, payload, m.CreatedAt, m.UpdatedAt)
	return mapErr(err, "insert object")
}

func getObject(ctx context.Context, ex execer, key string) (*ObjectMeta, error) {
	if err := objectstore.ValidateKey(key); err != nil {
		return nil, err
	}
	row := ex.QueryRowContext(ctx, `SELECT `+objectColumns+` FROM objects WHERE blob_key = ?`, key)
	return scanObject(row)
}

func scanObject(row scanner) (*ObjectMeta, error) {
	m := &ObjectMeta{}
	var deletedAt sql.NullTime
	err := row.Scan(&m.BlobKey, &m.SHA256, &m.Size, &m.MediaType, &m.Category, &m.Driver,
		&m.RefCount, &m.State, &deletedAt, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, mapErr(err, "get object")
	}
	if deletedAt.Valid {
		m.DeletedAt = &deletedAt.Time
	}
	return m, nil
}

func listObjects(ctx context.Context, ex execer, query string, args []any) ([]*ObjectMeta, error) {
	rows, err := ex.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	defer rows.Close()
	var out []*ObjectMeta
	for rows.Next() {
		m, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list objects: %w", err)
	}
	return out, nil
}

// ObjectStats 按 driver 分组统计。排除 deleting：那些行已经被 GC 认领、马上要消失，
// 把它们算进「待迁移对象」会让 blobs migrate 多背一批根本读不到的 key。
// 按 driver 排序返回，保证日志与健康端点的输出顺序确定（同一条命令两次跑不该换个顺序）。
func (st *objectStore) ObjectStats(ctx context.Context) ([]ObjectDriverStat, error) {
	rows, err := st.s.db.QueryContext(ctx,
		`SELECT driver, COUNT(*), COALESCE(SUM(size), 0) FROM objects
		 WHERE state <> ? GROUP BY driver ORDER BY driver`,
		ObjectStateDeleting)
	if err != nil {
		return nil, fmt.Errorf("object stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]ObjectDriverStat, 0, 4)
	for rows.Next() {
		var s ObjectDriverStat
		if err := rows.Scan(&s.Driver, &s.Objects, &s.Bytes); err != nil {
			return nil, fmt.Errorf("object stats: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("object stats: %w", err)
	}
	return out, nil
}

// incrRefCount 递增引用计数并把对象拉回 ready（orphaned 复活走这里）。命中 deleting
// 的行时一条也不改，由调用方判成 ErrState。
func incrRefCount(ctx context.Context, ex execer, key string) (bool, error) {
	res, err := ex.ExecContext(ctx,
		`UPDATE objects SET ref_count = ref_count + 1, state = ?, deleted_at = NULL, updated_at = ?
		 WHERE blob_key = ? AND state <> ?`,
		ObjectStateReady, time.Now(), key, ObjectStateDeleting)
	if err != nil {
		return false, fmt.Errorf("acquire object %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("acquire object %s: %w", key, err)
	}
	return n > 0, nil
}

// decrRefCount 递减引用计数。返回 (changed, error)：changed=false 说明这一行不存在或
// ref_count 已为 0，调用方必须区分处理，不许静默钳零——计数变负是调用方漏配对的信号。
func decrRefCount(ctx context.Context, ex execer, key string) (bool, error) {
	res, err := ex.ExecContext(ctx,
		`UPDATE objects SET ref_count = ref_count - 1, updated_at = ?
		 WHERE blob_key = ? AND ref_count > 0`,
		time.Now(), key)
	if err != nil {
		return false, fmt.Errorf("release object %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("release object %s: %w", key, err)
	}
	return n > 0, nil
}

// markOrphan 在引用归零后打逻辑删除标记。state=ready 的条件让 missing 行保持诊断态
// 不变（引用关系仍在，等 verify 重新判定）。
func markOrphan(ctx context.Context, ex execer, key string) error {
	now := time.Now()
	_, err := ex.ExecContext(ctx,
		`UPDATE objects SET state = ?, deleted_at = ?, updated_at = ?
		 WHERE blob_key = ? AND ref_count = 0 AND state = ?`,
		ObjectStateOrphaned, now, now, key, ObjectStateReady)
	if err != nil {
		return fmt.Errorf("orphan object %s: %w", key, err)
	}
	return nil
}

// errRefUnderflow 是引用计数配对被破坏的错误（方案 15 §6.5：不静默钳零）。
var errRefUnderflow = errors.New("store: object reference underflow")

// adoptObject 翻转对象的后端归属（方案 15 §10.1 第 3 步）。它是这张表唯一一条会改
// driver 的 UPDATE，因此 CAS 条件必须写全：key + 原 driver + 不在 deleting。
//
// 「内容已经落到目标端」这件事不在本函数的职责里，也正因为不在，它才必须被调用方放在
// 前面做完——本函数一提交，objects.driver 就是权威读数，读路径立刻按新后端取内容。
// 先翻元数据再搬内容，中间那段窗口就是 §12.1 说的悬空引用。
//
// driver=db 的目标端特殊：正文与元数据同表，所以正文必须和 driver 翻转走在**同一条
// 语句**里。分成两条语句的话，「driver=db 但 payload 为 NULL」就是一个提交点之间存在
// 的合法状态，而它在语义上等于「内容丢了」——这正是本函数要避免的那个坏状态。
//
// state 只在 missing 时拉回 ready（verify 判过的损坏行，现在内容确实到位了）；
// orphaned/deleting 是 GC 的地盘，不在此处改写。
//
// dropSource 只服务一种组合（from=db）：源后端的正文就在这一行里，运维要求清源时必须在
// **同一条语句**里把它置 NULL。让调用方翻转之后再喊 db 后端 Delete 一遍，会把 ready 行打成
// state=missing——那是「行在、内容丢了」的专用标记，而这份内容只是搬到了目标端，牌子挂错
// 之下一句 `blobs stat` 就自相矛盾（state=missing 却 backend=present）。
func adoptObject(ctx context.Context, ex execer, key, from, to string, payload []byte, dropSource bool) (bool, error) {
	now := time.Now()
	var (
		res sql.Result
		err error
	)
	if to == objectstore.DriverDB {
		res, err = ex.ExecContext(ctx,
			`UPDATE objects SET driver = ?, payload = ?, updated_at = ?,
			            state = CASE WHEN state = ? THEN ? ELSE state END
			 WHERE blob_key = ? AND driver = ? AND state <> ?`,
			to, payload, now, ObjectStateMissing, ObjectStateReady, key, from, ObjectStateDeleting)
	} else if dropSource {
		res, err = ex.ExecContext(ctx,
			`UPDATE objects SET driver = ?, payload = NULL, updated_at = ?
			 WHERE blob_key = ? AND driver = ? AND state <> ?`,
			to, now, key, from, ObjectStateDeleting)
	} else {
		res, err = ex.ExecContext(ctx,
			`UPDATE objects SET driver = ?, updated_at = ?
			 WHERE blob_key = ? AND driver = ? AND state <> ?`,
			to, now, key, from, ObjectStateDeleting)
	}
	if err != nil {
		return false, fmt.Errorf("adopt object %s (%s -> %s): %w", key, from, to, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("adopt object %s (%s -> %s): %w", key, from, to, err)
	}
	return n > 0, nil
}

// 编译期确认元数据 Store 已装配进 dbStore。
var _ ObjectStore = (*objectStore)(nil)
