package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
	"github.com/k8s-ai/k8s-ai/internal/session"
)

// rawDB 取底层连接：测试必须能在 Store 的读面之外数行、改正文，否则分不清「事务没提交」
// 和「读面写错了」。
func rawDB(t *testing.T, s Store) *sql.DB {
	t.Helper()
	db, ok := s.(*dbStore)
	if !ok {
		t.Fatalf("这些用例需要 *dbStore，实际 %T", s)
	}
	return db.db
}

func countRows(t *testing.T, s Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := rawDB(t, s).QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func refCountOf(t *testing.T, s Store, key string) int64 {
	t.Helper()
	m, err := s.Objects().Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return m.RefCount
}

// TestWithTxRollsBackAttachmentAndReference 是本批次最关键的一条断言：附件行与
// ref_count 必须同生共死。回滚后残留引用就是永久回收不掉的孤儿对象，残留正文则是脏读。
func TestWithTxRollsBackAttachmentAndReference(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ref := objTestRef(t, objectstore.CategoryAttachments, []string{"u1", "s1"}, ".txt", "payload bytes")

	if err := s.WithTx(ctx, func(tx *Tx) error {
		inserted, err := tx.InsertAttachment(ctx, &Attachment{
			ID: "att-1", SessionID: "s1", Name: "note.txt", UserID: "u1",
			BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
		})
		if err != nil {
			return err
		}
		if !inserted {
			return errors.New("首次插入应返回 inserted=true")
		}
		return tx.Acquire(ctx, ref, objectstore.DriverDB, "u1")
	}); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM attachments`); n != 1 {
		t.Fatalf("附件行数 = %d, want 1", n)
	}
	if n := refCountOf(t, s, ref.Key); n != 1 {
		t.Fatalf("ref_count = %d, want 1", n)
	}

	boom := errors.New("业务在事务后半段失败")
	err := s.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.InsertAttachment(ctx, &Attachment{
			ID: "att-2", SessionID: "s1", Name: "other.txt", UserID: "u1",
			BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
		}); err != nil {
			return err
		}
		if err := tx.Acquire(ctx, ref, objectstore.DriverDB, "u1"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx 应原样返回业务错误，got %v", err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM attachments`); n != 1 {
		t.Fatalf("回滚后附件行数 = %d, want 1", n)
	}
	if n := refCountOf(t, s, ref.Key); n != 1 {
		t.Fatalf("回滚后 ref_count = %d, want 1（引用泄漏会让对象永远回收不掉）", n)
	}
}

func TestWithTxRejectsNilCallback(t *testing.T) {
	s := newTestStore(t)
	if err := s.WithTx(context.Background(), nil); err == nil {
		t.Fatal("nil 回调必须报错，不能当成空事务提交")
	}
}

func TestTxUpdateSessionCommitsAndRollsBack(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	sess := &session.Session{ID: "sess-tx", Name: "原名", CreatedAt: now, LastModified: now}
	if err := s.Sessions().Create(ctx, sess); err != nil {
		t.Fatalf("create session: %v", err)
	}

	rename := func(name string, fail bool) error {
		// 事务前的读取必须在 WithTx 之外做完：SQLite 只有一个连接，回调里再调 Store 读面
		// 会等不到连接（这条约束写在 WithTx 的注释里）。
		current, err := s.Sessions().Get(ctx, sess.ID)
		if err != nil {
			return err
		}
		current.Name = name
		return s.WithTx(ctx, func(tx *Tx) error {
			if err := tx.UpdateSession(ctx, current); err != nil {
				return err
			}
			if fail {
				return errors.New("rollback requested")
			}
			return nil
		})
	}

	if err := rename("新名", false); err != nil {
		t.Fatalf("commit rename: %v", err)
	}
	got, err := s.Sessions().Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Name != "新名" {
		t.Fatalf("事务内更新未提交：name = %q", got.Name)
	}
	if err := rename("回滚名", true); err == nil {
		t.Fatal("期望事务回滚")
	}
	if got, err = s.Sessions().Get(ctx, sess.ID); err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Name != "新名" {
		t.Fatalf("回滚未生效：name = %q", got.Name)
	}
}

// TestInsertAttachmentIdempotent 覆盖 HTTP 重试路径：唯一索引命中时回写权威 ID 并返回
// inserted=false，调用方据此跳过 Acquire。少了这个信号，一次重试就把 ref_count 虚增一格。
func TestInsertAttachmentIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ref := objTestRef(t, objectstore.CategoryAttachments, []string{"u2", "s2"}, ".png", "same bytes")

	insert := func(id, name string) (bool, string, error) {
		var gotID string
		var inserted bool
		err := s.WithTx(ctx, func(tx *Tx) error {
			a := &Attachment{
				ID: id, SessionID: "s2", Name: name, UserID: "u2",
				BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
			}
			var err error
			inserted, err = tx.InsertAttachment(ctx, a)
			gotID = a.ID
			if err != nil || !inserted {
				return err
			}
			return tx.Acquire(ctx, ref, objectstore.DriverDB, "u2")
		})
		return inserted, gotID, err
	}

	inserted, firstID, err := insert("att-a", "pic.png")
	if err != nil || !inserted || firstID != "att-a" {
		t.Fatalf("首次插入 = (%v,%v,%v)", inserted, firstID, err)
	}
	inserted, secondID, err := insert("att-b", "pic.png")
	if err != nil {
		t.Fatalf("重放插入: %v", err)
	}
	if inserted {
		t.Fatal("同会话同内容同展示名应命中唯一索引")
	}
	if secondID != firstID {
		t.Fatalf("重放未回写权威 ID：%q, want %q", secondID, firstID)
	}
	if n := refCountOf(t, s, ref.Key); n != 1 {
		t.Fatalf("重放后 ref_count = %d, want 1", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM attachments`); n != 1 {
		t.Fatalf("重放后附件行数 = %d, want 1", n)
	}

	// 唯一性判据是三列联合：同内容换展示名是另一份附件（会话里两张同名图片要能并存）。
	inserted, _, err = insert("att-c", "pic-2.png")
	if err != nil || !inserted {
		t.Fatalf("换展示名应插入新行 = (%v,%v)", inserted, err)
	}
	if n := refCountOf(t, s, ref.Key); n != 2 {
		t.Fatalf("两份附件的 ref_count = %d, want 2", n)
	}
}

func TestInsertAttachmentValidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ref := objTestRef(t, objectstore.CategoryAttachments, []string{"u3", "s3"}, ".txt", "bytes")
	base := Attachment{ID: "a", SessionID: "s3", Name: "n.txt", BlobKey: ref.Key, SHA256: ref.SHA256}

	cases := []struct {
		name  string
		patch func(*Attachment)
	}{
		{"空主键", func(a *Attachment) { a.ID = "" }},
		{"空会话", func(a *Attachment) { a.SessionID = "" }},
		{"空白展示名", func(a *Attachment) { a.Name = "   " }},
		{"摘要不是 64 位十六进制", func(a *Attachment) { a.SHA256 = "deadbeef" }},
		{"非法 blob key", func(a *Attachment) { a.BlobKey = "../etc/passwd" }},
	}
	for _, tc := range cases {
		err := s.WithTx(ctx, func(tx *Tx) error {
			a := base
			tc.patch(&a)
			_, err := tx.InsertAttachment(ctx, &a)
			return err
		})
		if err == nil {
			t.Errorf("%s: 必须拒绝", tc.name)
		}
	}
	if err := s.WithTx(ctx, func(tx *Tx) error { _, err := tx.InsertAttachment(ctx, nil); return err }); err == nil {
		t.Error("nil 附件必须拒绝")
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM attachments`); n != 0 {
		t.Fatalf("校验失败不应留下任何行，得到 %d", n)
	}
}

// TestInsertReportReRunsShareOneObject 说明报告为什么不需要唯一索引：同一次扫描重跑写出
// 两行历史记录，两行引用同一个 blob_key，ref_count 自然到 2。
func TestInsertReportReRunsShareOneObject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ref := objTestRef(t, objectstore.CategoryReports, []string{"cluster-1", "scan-1"}, ".md", "# report")

	insert := func(id string, fail bool) error {
		return s.WithTx(ctx, func(tx *Tx) error {
			r := &Report{
				ID: id, ClusterID: "cluster-1", ScanID: "scan-1", Kind: "latest-md",
				BlobKey: ref.Key, MediaType: ref.MediaType, Size: ref.Size, SHA256: ref.SHA256,
				Findings: 3, CreatedBy: "scanner", Available: true,
			}
			if err := tx.InsertReport(ctx, r); err != nil {
				return err
			}
			if err := tx.Acquire(ctx, ref, objectstore.DriverDB, "scanner"); err != nil {
				return err
			}
			if fail {
				return errors.New("rollback requested")
			}
			return nil
		})
	}

	if err := insert("rpt-1", false); err != nil {
		t.Fatalf("insert report: %v", err)
	}
	if err := insert("rpt-2", true); err == nil {
		t.Fatal("期望第二笔回滚")
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM reports`); n != 1 {
		t.Fatalf("回滚后报告行数 = %d, want 1", n)
	}
	if n := refCountOf(t, s, ref.Key); n != 1 {
		t.Fatalf("回滚后 ref_count = %d, want 1", n)
	}
	if err := insert("rpt-3", false); err != nil {
		t.Fatalf("insert second report: %v", err)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM reports`); n != 2 {
		t.Fatalf("重跑应留下两行历史，得到 %d", n)
	}
	if n := refCountOf(t, s, ref.Key); n != 2 {
		t.Fatalf("两行共享同一对象时 ref_count = %d, want 2", n)
	}
}

func TestInsertReportValidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	ref := objTestRef(t, objectstore.CategoryReports, []string{"c", "s"}, ".json", "{}")
	base := Report{ID: "r", Kind: "latest-json", BlobKey: ref.Key, SHA256: ref.SHA256}

	cases := []struct {
		name  string
		patch func(*Report)
	}{
		{"空主键", func(r *Report) { r.ID = "" }},
		{"空 kind", func(r *Report) { r.Kind = "  " }},
		{"摘要非法", func(r *Report) { r.SHA256 = "nope" }},
		{"非法 blob key", func(r *Report) { r.BlobKey = "k8sai/reports/../x" }},
	}
	for _, tc := range cases {
		err := s.WithTx(ctx, func(tx *Tx) error {
			r := base
			tc.patch(&r)
			return tx.InsertReport(ctx, &r)
		})
		if err == nil {
			t.Errorf("%s: 必须拒绝", tc.name)
		}
	}
	if err := s.WithTx(ctx, func(tx *Tx) error { return tx.InsertReport(ctx, nil) }); err == nil {
		t.Error("nil 报告必须拒绝")
	}
}

func TestReleaseGuards(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	release := func(key string) error {
		return s.WithTx(ctx, func(tx *Tx) error { return tx.Release(ctx, key) })
	}

	if err := release("k8sai/reports/" + strings.Repeat("a", 64) + ".md"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("释放未知对象 = %v, want ErrNotFound", err)
	}
	if err := release("bad/key"); !errors.Is(err, objectstore.ErrKeyInvalid) {
		t.Fatalf("非法 key = %v, want ErrKeyInvalid", err)
	}

	ref := objTestRef(t, objectstore.CategoryExports, []string{"u-rel"}, ".csv", "rel")
	if err := s.WithTx(ctx, func(tx *Tx) error { return tx.Acquire(ctx, ref, objectstore.DriverDB, "u") }); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := release(ref.Key); err != nil {
		t.Fatalf("release: %v", err)
	}
	// 计数已经归零再 Release，说明调用方 Acquire/Release 漏配对。这里必须报错而不是钳零：
	// 静默钳零会把一个没人敢信的数留在库里。
	if err := release(ref.Key); !errors.Is(err, errRefUnderflow) {
		t.Fatalf("重复释放 = %v, want errRefUnderflow", err)
	}
	if n := refCountOf(t, s, ref.Key); n != 0 {
		t.Fatalf("下溢后 ref_count = %d, want 0（不得被改成负数）", n)
	}
}
