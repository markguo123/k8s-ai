package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/objectstore"
)

// reportColumns 是 reports 表的列清单，单源在此处（与 objectColumns 同一理由：列序
// 在列表、计数、单读三处各写一遍是这个表最可能的漂移点）。
//
// 列表查询刻意复用同一串列名，包括 blob_key：它只在 service 层换取内容时用，不进
// JSON 视图（model.Report 上是 json:"-"），但读进内存本身不泄露给响应。
const reportColumns = "id, cluster_id, scan_id, kind, blob_key, media_type, size, sha256, " +
	"findings, created_by, available, created_at, scope_kind, scope_value"

// ReportQuery 是报告列表的过滤与归属条件（方案 15 §12.3）。
//
// 可见性必须下推到 SQL 而不是「先查一页再在内存里筛」：后者会让分页 total 把别人的
// 报告也算进去，用户看到「共 200 条」却只列出 3 条，而且越权行的存在性本身就走漏了
// 报告数量。归属三态由 service 层算好后交进来，本层只负责把它翻成一条 WHERE。
type ReportQuery struct {
	// ViewerID 是查看者用户 ID。Admin 为 false 时必填——空 ViewerID 意味着「谁都不该
	// 看到」，把它当成「无过滤条件」是最危险的一种默认值。
	ViewerID string
	// Admin 为 true 时不加归属过滤（跨用户读取由 service 层落审计）。
	Admin bool
	// AllSystemClusters 表示可查看者拥有全部集群授权，系统体检报告不按 cluster 收窄。
	AllSystemClusters bool
	// SystemClusterIDs 是可查看者被授权的集群 ID，决定能看到哪些系统体检报告。
	SystemClusterIDs []string
	// ClusterID / Kind 是可选过滤条件。
	ClusterID string
	Kind      string
	// ScanID 是按扫描任务反查产物（P8.13）：页面发起体检后靠它找回自己的报告。
	ScanID string
	// ScopeCategory 是「报告类别」过滤（P9.13）：集群体检 / 业务体检 / 其它。
	// 它是展示层的分组口径（namespace 与 pod 都归业务体检），不是授权条件——归属与集群
	// 授权仍由上面三段决定，类别只在已可见的行里再切一刀。
	ScopeCategory string
	// Limit / Offset 是分页；Limit<=0 表示不分页（仍按时间倒序全量返回）。
	Limit  int
	Offset int
}

// 报告类别取值（P9.13）：页面「报告类别」筛选与总览共用这一份口径。
//
// 口径放在 store 是因为它是 scope_kind 列的分组规则——列表过滤与总览统计必须由同一条
// 规则算出来，否则会出现「筛出 3 条、总览写着 5 条」这种自相矛盾的页面。
const (
	// ScopeCategoryCluster 是集群体检（scope_kind = cluster）。
	ScopeCategoryCluster = "cluster"
	// ScopeCategoryBusiness 是业务体检（scope_kind = namespace 或 pod）。
	ScopeCategoryBusiness = "business"
	// ScopeCategoryOther 是范围未记录：v8 之前的存量行与 CLI/CronJob 产物。
	ScopeCategoryOther = "other"
)

// ReportStats 是「同一归属条件」下的分类计数（P9.13 报告列表总览）。
//
// 它必须与列表共用 where()：总览说「集群体检 12 份」而点进去只有 3 份，比不给总览更糟。
// 同样，未授权用户的行既不计入 items 也不计入这些计数。
type ReportStats struct {
	Total     int64 `json:"total"`
	Cluster   int64 `json:"cluster"`
	Business  int64 `json:"business"`
	Other     int64 `json:"other"`
	Markdown  int64 `json:"markdown"`
	JSON      int64 `json:"json"`
	Available int64 `json:"available"`
}

// ReportStore 是报告业务行的读面（方案 15 §6.2）。
//
// 写面刻意只有 WithTx 的 Tx.InsertReport：报告行必须与对象引用同事务提交，
// 这里开一个脱离事务的 Create 就等于允许「行提交了、引用没加」的悬空引用。
// 删除同理：只有 Tx.DeleteReport，且必须与 Tx.Release 同一事务（P8.14，方案 15 §13.1）——
// 脱离了 Release 的删行就是永久悬空引用，对象再也回收不掉。
type ReportStore interface {
	// List 按归属与过滤条件分页列出报告，返回值同时给出同条件下的总行数（分页 total）。
	List(ctx context.Context, q ReportQuery) ([]*Report, int64, error)
	Get(ctx context.Context, id string) (*Report, error)
	// Stats 给出与 List 同一套归属/过滤条件下的分类计数（不看分页）。
	Stats(ctx context.Context, q ReportQuery) (ReportStats, error)
}

type reportStore struct{ s *dbStore }

func (st *reportStore) List(ctx context.Context, q ReportQuery) ([]*Report, int64, error) {
	where, args, err := q.where()
	if err != nil {
		return nil, 0, err
	}

	var total int64
	// 计数与当页查询共用同一条 WHERE：total 与 items 口径不一致时，前端的分页控件会
	// 长出点不到的页码，这类问题只在数据多了之后才被发现。
	if err := st.s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reports `+where, args...).Scan(&total); err != nil {
		return nil, 0, mapErr(err, "count reports")
	}
	if total == 0 {
		return []*Report{}, 0, nil
	}

	query := `SELECT ` + reportColumns + ` FROM reports ` + where + ` ORDER BY created_at DESC, id DESC`
	pageArgs := append([]any{}, args...)
	if q.Limit > 0 {
		query += ` LIMIT ? OFFSET ?`
		pageArgs = append(pageArgs, q.Limit, q.Offset)
	}
	rows, err := st.s.db.QueryContext(ctx, query, pageArgs...)
	if err != nil {
		return nil, 0, mapErr(err, "list reports")
	}
	defer func() { _ = rows.Close() }()

	out := make([]*Report, 0, 32)
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list reports: %w", err)
	}
	return out, total, nil
}

func (st *reportStore) Get(ctx context.Context, id string) (*Report, error) {
	row := st.s.db.QueryRowContext(ctx, `SELECT `+reportColumns+` FROM reports WHERE id = ?`, id)
	r, err := scanReport(row)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// where 组装归属与过滤条件，返回的 args 顺序与 SQL 里 ? 的出现顺序严格一致。
func (q ReportQuery) where() (string, []any, error) {
	var clauses []string
	var args []any

	if !q.Admin {
		viewer := strings.TrimSpace(q.ViewerID)
		if viewer == "" {
			return "", nil, fmt.Errorf("list reports: non-admin query needs a viewer id")
		}
		// 归属谓词整体加括号：它和下面的 cluster/kind 过滤是 AND 关系，而自身是三段
		// OR，少了括号 kind 过滤就会被 OR 吞掉，变成「别人在该集群的系统报告也算命中」。
		own := []string{"created_by = ?"}
		ownArgs := []any{viewer}
		switch {
		case q.AllSystemClusters:
			// 拥有全部集群授权：系统报告按「有集群标识」放行。cluster_id 为空串的系统
			// 报告（未绑定已登记集群的历史/本地任务）仍然只有 admin 能读——没有可比对的
			// 授权对象时，默认拒绝比默认放行诚实。
			own = append(own, "created_by = ? AND cluster_id <> ''")
			ownArgs = append(ownArgs, objectstore.SystemActor)
		default:
			if ids := validClusterIDs(q.SystemClusterIDs); len(ids) > 0 {
				own = append(own, "created_by = ? AND cluster_id IN ("+placeholders(len(ids))+")")
				ownArgs = append(ownArgs, objectstore.SystemActor)
				for _, id := range ids {
					ownArgs = append(ownArgs, id)
				}
			}
		}
		clauses = append(clauses, "("+strings.Join(own, " OR ")+")")
		args = append(args, ownArgs...)
	}
	if clusterID := strings.TrimSpace(q.ClusterID); clusterID != "" {
		clauses = append(clauses, "cluster_id = ?")
		args = append(args, clusterID)
	}
	if kind := strings.TrimSpace(q.Kind); kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, kind)
	}
	if scanID := strings.TrimSpace(q.ScanID); scanID != "" {
		clauses = append(clauses, "scan_id = ?")
		args = append(args, scanID)
	}
	// 类别过滤用字面量而不是占位符：值域是上面三个常量，不来自请求体，
	// 拼进 SQL 不会引入注入面，反而让 IN 列表少两个参数、便于比对两处 SQL。
	switch strings.TrimSpace(q.ScopeCategory) {
	case ScopeCategoryCluster:
		clauses = append(clauses, "scope_kind = 'cluster'")
	case ScopeCategoryBusiness:
		clauses = append(clauses, "scope_kind IN ('namespace', 'pod')")
	case ScopeCategoryOther:
		// NOT IN 天然含空串：范围留痕之前的行与未知取值都归到这里，不会在页面上消失。
		clauses = append(clauses, "scope_kind NOT IN ('cluster', 'namespace', 'pod')")
	}
	if len(clauses) == 0 {
		return `WHERE 1 = 1`, nil, nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args, nil
}

// Stats 一次查询算完全部分类计数：列表页顶部要在同一时刻给出总数与各类别，
// 拆成多条查询会出现「刷新一次、三个数字来自三个瞬间」的错位。
func (st *reportStore) Stats(ctx context.Context, q ReportQuery) (ReportStats, error) {
	// 总览不关心分页字段：带着 LIMIT/OFFSET 统计出来的是一页的分布，不是列表的分布。
	q.Limit = 0
	q.Offset = 0
	where, args, err := q.where()
	if err != nil {
		return ReportStats{}, err
	}
	var stats ReportStats
	// COALESCE 是必需的：没有任何行时 SUM 返回 NULL，直接 Scan 进 int64 会报错，
	// 「一条报告都没有」绝不该变成一个数据库错误。
	err = st.s.db.QueryRowContext(ctx, `SELECT COUNT(*), `+
		`COALESCE(SUM(CASE WHEN scope_kind = 'cluster' THEN 1 ELSE 0 END), 0), `+
		`COALESCE(SUM(CASE WHEN scope_kind IN ('namespace', 'pod') THEN 1 ELSE 0 END), 0), `+
		`COALESCE(SUM(CASE WHEN scope_kind NOT IN ('cluster', 'namespace', 'pod') THEN 1 ELSE 0 END), 0), `+
		`COALESCE(SUM(CASE WHEN kind = 'latest-md' THEN 1 ELSE 0 END), 0), `+
		`COALESCE(SUM(CASE WHEN kind = 'latest-json' THEN 1 ELSE 0 END), 0), `+
		`COALESCE(SUM(CASE WHEN available = 1 THEN 1 ELSE 0 END), 0) `+
		`FROM reports `+where, args...).
		Scan(&stats.Total, &stats.Cluster, &stats.Business, &stats.Other,
			&stats.Markdown, &stats.JSON, &stats.Available)
	if err != nil {
		return ReportStats{}, mapErr(err, "stats reports")
	}
	return stats, nil
}

// validClusterIDs 去掉空白与重复项，保证 IN 子句不会因为一个空串而退化成全表匹配。
func validClusterIDs(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// scanReport 读一行报告（复用 objects.go 的 scanner：*sql.Row 与 *sql.Rows 的公共面）。
func scanReport(row scanner) (*Report, error) {
	r := &Report{}
	// available 在两个驱动下都是整数列（colBool → SQLite INTEGER / MySQL TINYINT），
	// 直接 Scan 进 bool 在 MySQL 上会因驱动返回类型不稳而报错，按 int 收再转换。
	var available int
	var createdAt time.Time
	// 顺序必须与 reportColumns 严格一致：列名单源在那边，这里靠位置对齐，多一列少一列
	// 都是运行期才炸的错。
	err := row.Scan(&r.ID, &r.ClusterID, &r.ScanID, &r.Kind, &r.BlobKey, &r.MediaType,
		&r.Size, &r.SHA256, &r.Findings, &r.CreatedBy, &available, &createdAt,
		&r.ScopeKind, &r.ScopeValue)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, mapErr(err, "get report")
		}
		return nil, mapErr(err, "scan report")
	}
	r.Available = available != 0
	r.CreatedAt = createdAt.UTC()
	return r, nil
}

// 编译期确认报告读面已装配进 dbStore。
var _ ReportStore = (*reportStore)(nil)
