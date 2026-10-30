package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/session"
)

// Tx 是一个业务写事务句柄（方案 15 §6.5）。
//
// 它刻意只暴露四类动作：会话更新、附件/报告业务行插入、对象引用递增/递减。构造未导出
// 且只能从 WithTx 的回调里拿到，因此「改引用计数」与「写业务行」在类型层面就不可分离——
// 这是本方案的一致性保证：附件行提交了，ref_count 一定同步加过；行回滚了，引用也不存在。
// 反过来任何脱离事务的 Acquire 都拿不到句柄，编译期即挡掉。
//
// Tx 不提供 ExecContext 之类的逃生口：一旦开了口子，「事务里随便写」就等于没有边界。
type Tx struct {
	tx *sql.Tx
}

// WithTx 在一个数据库事务内执行 fn：fn 返回错误即回滚，返回 nil 即提交。
//
// SQLite 侧连接池被限成单连接（见 OpenSQLite），事务天然串行；MySQL 侧依赖 InnoDB 行锁
// 与默认隔离级别。两种驱动都用默认事务选项（nil），不引入方言专有的隔离级别设置。
//
// 调用方必须遵守一条硬约束：回调内只准用 Tx 上的方法读写，不得再调 Store 的任何非事务
// 入口（Sessions().Get、Objects().Get 之类）。SQLite 驱动下事务占用了池里唯一的连接，
// 回调里再要一次连接会一直等下去，表现为整个请求挂死而不是报错；MySQL 下同样是自等待。
// 需要事务前的读取（幂等判据、归属校验）请在 WithTx 之前做完，把结果带进回调。
func (s *dbStore) WithTx(ctx context.Context, fn func(*Tx) error) error {
	if fn == nil {
		return fmt.Errorf("begin tx: nil callback")
	}
	sqlTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	t := &Tx{tx: sqlTx}
	if err := fn(t); err != nil {
		if rbErr := sqlTx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w (rollback failed: %v)", err, rbErr)
		}
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// UpdateSession 更新会话（与 Sessions().Update 共用 updateSession 这一条语句）。
func (t *Tx) UpdateSession(ctx context.Context, sess *session.Session) error {
	return updateSession(ctx, t.tx, sess)
}

// InsertAttachment 插入附件业务行。
//
// 幂等判据是唯一索引 uk_attachments_session_sha_name(session_id, sha256, name)：同一会话
// 内「同内容 + 同展示名」只有一行。命中已有行时把权威 ID 写回 a.ID 并返回 inserted=false，
// 调用方必须据此跳过 Acquire——否则 HTTP 重试一次就把 ref_count 虚增一格，那个对象
// 永远回收不掉（引用计数方案最常见的泄漏点）。
func (t *Tx) InsertAttachment(ctx context.Context, a *Attachment) (bool, error) {
	switch {
	case a == nil:
		return false, fmt.Errorf("insert attachment: nil")
	case a.ID == "":
		return false, fmt.Errorf("insert attachment: empty id")
	case a.SessionID == "":
		return false, fmt.Errorf("insert attachment: empty session id")
	case strings.TrimSpace(a.Name) == "":
		return false, fmt.Errorf("insert attachment: empty display name")
	case !objectstore.IsSHA256(a.SHA256):
		return false, fmt.Errorf("%w: attachment sha256 %q is not a %d-hex digest",
			objectstore.ErrKeyInvalid, a.SHA256, objectstore.SHA256HexLen)
	}
	if err := objectstore.ValidateKey(a.BlobKey); err != nil {
		return false, err
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO attachments (id, session_id, turn_id, user_id, name, blob_key, media_type, size, sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.SessionID, a.TurnID, a.UserID, a.Name, a.BlobKey, a.MediaType, a.Size, a.SHA256, a.CreatedAt)
	if err == nil {
		return true, nil
	}
	mapped := mapErr(err, "insert attachment")
	if !errors.Is(mapped, ErrExists) {
		return false, mapped
	}
	existing, err := getAttachment(ctx, t.tx, a.SessionID, a.SHA256, a.Name)
	if err != nil {
		return false, err
	}
	a.ID = existing.ID
	return false, nil
}

// InsertReport 插入报告业务行。
//
// 报告没有幂等唯一索引（同一次扫描重跑是合法的历史记录），去重靠内容寻址：同一份内容
// 只会对应一个 blob_key，重跑写出的新行与旧行引用同一个对象，ref_count 自然到 2。
func (t *Tx) InsertReport(ctx context.Context, r *Report) error {
	switch {
	case r == nil:
		return fmt.Errorf("insert report: nil")
	case r.ID == "":
		return fmt.Errorf("insert report: empty id")
	case strings.TrimSpace(r.Kind) == "":
		return fmt.Errorf("insert report: empty kind")
	case !objectstore.IsSHA256(r.SHA256):
		return fmt.Errorf("%w: report sha256 %q is not a %d-hex digest",
			objectstore.ErrKeyInvalid, r.SHA256, objectstore.SHA256HexLen)
	}
	if err := objectstore.ValidateKey(r.BlobKey); err != nil {
		return err
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO reports (id, cluster_id, scan_id, kind, blob_key, media_type, size, sha256, findings, created_by, available, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ClusterID, r.ScanID, r.Kind, r.BlobKey, r.MediaType, r.Size, r.SHA256,
		r.Findings, r.CreatedBy, boolInt(r.Available), r.CreatedAt)
	return mapErr(err, "insert report")
}

// Acquire 给对象加一次引用，必须在写下引用它的业务行的同一事务里调用。
//
// 三条规则：
//  1. 已有行且不在 deleting：ref_count+1，orphaned 顺带复活成 ready（先上传后挂引用的
//     宽限期内被第二次上传命中的场景）。
//  2. 没有行：插入一行 ref_count=1、state=ready。与并发写入撞主键时重试一次。
//  3. 行在 deleting：返回包装了 objectstore.ErrState 的错误——GC 已认领这个对象，
//     此刻挂新引用等于让调用方拿到一个即将消失的 key。
//
// driver 是内容写入方的标识（db/dir/s3），只在插入新行时落库：混合后端下 objects.driver
// 记的是首次写入者，改配置不清库也不搬迁（方案 15 §10）。
func (t *Tx) Acquire(ctx context.Context, ref objectstore.Ref, driver, createdBy string) error {
	if err := objectstore.ValidateKey(ref.Key); err != nil {
		return err
	}
	if !objectstore.IsSHA256(ref.SHA256) {
		return fmt.Errorf("%w: sha256 %q is not a %d-hex digest",
			objectstore.ErrKeyInvalid, ref.SHA256, objectstore.SHA256HexLen)
	}
	// 两次尝试覆盖两个并发窗口：插入撞主键（别人刚插同一行）、UPDATE 未命中（别人刚把
	// 行推进到 deleting 或删掉）。同事务快照里看不到对方提交也是正常分支，兜底一律
	// ErrState，由调用方回滚整笔事务重试，绝不静默放行。
	for attempt := 0; attempt < 2; attempt++ {
		changed, err := incrRefCount(ctx, t.tx, ref.Key)
		if err != nil {
			return err
		}
		if changed {
			return nil
		}
		meta, err := getObject(ctx, t.tx, ref.Key)
		switch {
		case errors.Is(err, ErrNotFound):
			m := &ObjectMeta{
				BlobKey:   ref.Key,
				SHA256:    ref.SHA256,
				Size:      ref.Size,
				MediaType: ref.MediaType,
				Driver:    driver,
				RefCount:  1,
				State:     ObjectStateReady,
				CreatedBy: createdBy,
			}
			insErr := insertObject(ctx, t.tx, m, nil)
			if insErr == nil {
				return nil
			}
			if !errors.Is(insErr, ErrExists) {
				return insErr
			}
			continue
		case err != nil:
			return err
		}
		if meta.State == ObjectStateDeleting {
			return fmt.Errorf("%w: object %s is being garbage collected", objectstore.ErrState, ref.Key)
		}
	}
	return fmt.Errorf("%w: object %s changed underneath the transaction", objectstore.ErrState, ref.Key)
}

// Release 释放一次引用，必须在删掉引用它的业务行的同一事务里调用。
//
// 引用归零时把 state 打成 orphaned 并记 deleted_at，进入 gc_grace_period 宽限期等 GC
// 物理回收；行本身与 ref_count 都保留，事后追溯得到「这个对象曾经被谁引用过」。
//
// 计数下溢不静默钳零：调用方漏配对（少 Acquire 或多 Release）是真实缺陷，把它报出来比
// 让计数变成一个没人敢信的数更有用。
func (t *Tx) Release(ctx context.Context, key string) error {
	if err := objectstore.ValidateKey(key); err != nil {
		return err
	}
	changed, err := decrRefCount(ctx, t.tx, key)
	if err != nil {
		return err
	}
	if changed {
		return markOrphan(ctx, t.tx, key)
	}
	meta, err := getObject(ctx, t.tx, key)
	if err != nil {
		return err
	}
	if meta.RefCount == 0 {
		return fmt.Errorf("%w: object %s has no reference left to release (state %s)",
			errRefUnderflow, key, meta.State)
	}
	return fmt.Errorf("%w: object %s reference count changed underneath the transaction",
		objectstore.ErrState, key)
}

// getAttachment 按唯一索引三列取回附件行（幂等重放分支用）。
//
// 附件的列表/下载读面属 P8.7（附件通道延后 v4.2），这里只保留幂等所需的最小读。
func getAttachment(ctx context.Context, ex execer, sessionID, sha256Hex, name string) (*Attachment, error) {
	a := &Attachment{}
	err := ex.QueryRowContext(ctx, `
		SELECT id, session_id, turn_id, user_id, name, blob_key, media_type, size, sha256, created_at
		FROM attachments WHERE session_id = ? AND sha256 = ? AND name = ?`,
		sessionID, sha256Hex, name).
		Scan(&a.ID, &a.SessionID, &a.TurnID, &a.UserID, &a.Name, &a.BlobKey,
			&a.MediaType, &a.Size, &a.SHA256, &a.CreatedAt)
	if err != nil {
		return nil, mapErr(err, "get attachment")
	}
	return a, nil
}
