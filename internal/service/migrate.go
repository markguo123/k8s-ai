package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/k8s-ai/k8s-ai/internal/audit"
	"github.com/k8s-ai/k8s-ai/internal/config"
	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/settings"
	"github.com/k8s-ai/k8s-ai/internal/skills"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// UnmigratedItem 是一条无法自动导入（或需人工确认保留）的三期数据项。
// 迁移绝不静默丢弃：凡解析失败、存储失败或没有目标表的数据都显式回填到这里，
// 保证"导入过程不丢数据"（P1.12 验收标准）。
type UnmigratedItem struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

// SourceReport 是某一类三期数据源的迁移统计。
type SourceReport struct {
	Found        int              `json:"found"`
	Imported     int              `json:"imported"`
	Skipped      int              `json:"skipped"` // 已存在/已迁移，不做重复写
	Kept         int              `json:"kept"`    // 显式保留在原位置（如报告文件）
	Unmigratable []UnmigratedItem `json:"unmigratable,omitempty"`
}

// MigrateReport 是一次 `k8s-ai migrate` 的完整结果。
type MigrateReport struct {
	Config   settings.ImportSummary `json:"config"`
	Sessions SourceReport           `json:"sessions"`
	Audit    SourceReport           `json:"audit"`
	Skills   SourceReport           `json:"skills"`
	Reports  SourceReport           `json:"reports"`
}

// UnmigratableCount 汇总各数据源的不可迁移项数量（CLI 据此决定退出码）。
func (r MigrateReport) UnmigratableCount() int {
	return len(r.Sessions.Unmigratable) + len(r.Audit.Unmigratable) +
		len(r.Skills.Unmigratable) + len(r.Reports.Unmigratable)
}

// MigrateOptions 是迁移命令的输入：目标 Store 与各三期数据源路径。
// 源路径为空时对应数据源整类跳过（不视为错误）。
type MigrateOptions struct {
	Store      store.Store
	Config     *config.Config
	SessionDir string
	AuditDir   string
	ReportsDir string
}

// Migrate 执行四期底座迁移（计划 P1.12）：
//   - 先做 Store schema 迁移（幂等）；
//   - config.yaml 复用配置中心 ImportConfig（逐键幂等）；
//   - 三期 session 文件逐文件导入，写库前整体脱敏；
//   - 三期 audit JSONL 逐行翻译为四期 AuditEvent 并落库（按文件记已迁移标记去重）；
//   - 内置 Skill 种子写入 SkillStore（Upsert 幂等 + 迁移标记）；
//   - 报告文件无存储表，显式报告保留，不删除不迁移。
//
// 红线（AGENTS.md）：Session/Audit 迁移输出里绝不出现 Secret 值、kubeconfig
// 明文或 API Key；脱敏由 store.RedactJSONPayload 与 auditStore.Write 双兜底。
func Migrate(ctx context.Context, opts MigrateOptions) (MigrateReport, error) {
	var rep MigrateReport
	if opts.Store == nil {
		return rep, errors.New("migrate: store not configured")
	}
	if err := opts.Store.Migrate(ctx); err != nil {
		return rep, fmt.Errorf("migrate schema: %w", err)
	}
	if opts.Config != nil {
		sum, err := migrateConfig(ctx, opts.Store, opts.Config)
		if err != nil {
			return rep, err
		}
		rep.Config = sum
	}
	var err error
	if rep.Sessions, err = migrateSessions(ctx, opts.Store, opts.SessionDir); err != nil {
		return rep, err
	}
	if rep.Audit, err = migrateAudit(ctx, opts.Store, opts.AuditDir); err != nil {
		return rep, err
	}
	if rep.Skills, err = migrateSkills(ctx, opts.Store); err != nil {
		return rep, err
	}
	if rep.Reports, err = migrateReports(ctx, opts.ReportsDir); err != nil {
		return rep, err
	}
	return rep, nil
}

// migrateConfig 把旧 config.Config 导入配置中心。ImportConfig 按目录逐项
// 非默认值入库、默认值不落库，因此天然幂等；完成后记一条迁移标记供审计/排查。
func migrateConfig(ctx context.Context, st store.Store, cfg *config.Config) (settings.ImportSummary, error) {
	sum, err := settings.New(st).ImportConfig(ctx, cfg)
	if err != nil {
		return sum, err
	}
	if err := markMigration(ctx, st, "config"); err != nil {
		return sum, err
	}
	return sum, nil
}

// migrateSessions 扫描 <dir>/*.json 导入历史会话。已存在于 Store 的会话跳过
// （幂等）；解析/存储失败的条目进 Unmigratable 并附原因，绝不静默丢弃。
func migrateSessions(ctx context.Context, st store.Store, dir string) (SourceReport, error) {
	rep := SourceReport{}
	if dir == "" {
		return rep, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, fmt.Errorf("read sessions dir %s: %w", dir, err)
	}
	existing, err := knownSessions(ctx, st)
	if err != nil {
		return rep, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rep.Found++
		path := filepath.Join(dir, e.Name())
		id := strings.TrimSuffix(e.Name(), ".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			rep.Unmigratable = append(rep.Unmigratable, UnmigratedItem{Source: path, Reason: "读取失败: " + err.Error()})
			continue
		}
		redacted, err := redactSession(raw)
		if err != nil {
			rep.Unmigratable = append(rep.Unmigratable, UnmigratedItem{Source: path, Reason: "解析失败: " + err.Error()})
			continue
		}
		if redacted.ID == "" {
			redacted.ID = id
		}
		if existing[redacted.ID] {
			rep.Skipped++
			continue
		}
		if err := st.Sessions().Create(ctx, redacted); err != nil {
			if errors.Is(err, store.ErrExists) {
				rep.Skipped++
				existing[redacted.ID] = true
				continue
			}
			rep.Unmigratable = append(rep.Unmigratable, UnmigratedItem{Source: path, Reason: "存储失败: " + err.Error()})
			continue
		}
		rep.Imported++
		existing[redacted.ID] = true
	}
	return rep, nil
}

// knownSessions 装载 Store 中已存在的会话 ID，用于迁移幂等。
func knownSessions(ctx context.Context, st store.Store) (map[string]bool, error) {
	list, err := st.Sessions().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list existing sessions: %w", err)
	}
	out := make(map[string]bool, len(list))
	for _, s := range list {
		out[s.ID] = true
	}
	return out, nil
}

// redactSession 在写库前对整个 Session JSON 脱敏（sessionStore.Create 不脱敏，
// 而旧会话的 Messages/Arguments/Data 可能携带凭据，因此迁移必须先整体脱敏再入库）。
func redactSession(raw []byte) (*session.Session, error) {
	redacted := store.RedactJSONPayload(string(raw))
	var s session.Session
	if err := json.Unmarshal([]byte(redacted), &s); err != nil {
		return nil, fmt.Errorf("重解析脱敏后会话失败: %w", err)
	}
	return &s, nil
}

// migrateAudit 扫描 <dir>/*.jsonl 逐行导入三期审计。审计行在 Store 无唯一约束，
// 因此以"文件已迁移"标记去重，保证二次运行不重复写。
func migrateAudit(ctx context.Context, st store.Store, dir string) (SourceReport, error) {
	rep := SourceReport{}
	if dir == "" {
		return rep, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, fmt.Errorf("read audit dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		rep.Found++
		path := filepath.Join(dir, e.Name())
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = e.Name()
		}
		marker := "audit:" + filepath.ToSlash(rel)
		done, err := migrationDone(ctx, st, marker)
		if err != nil {
			return rep, err
		}
		if done {
			rep.Skipped++
			continue
		}
		sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
		if err := importAuditFile(ctx, st, path, sessionID, &rep); err != nil {
			return rep, err
		}
		if err := markMigration(ctx, st, marker); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// importAuditFile 逐行解析 <session>.jsonl 并把每条事件翻译为四期 AuditEvent。
// 坏行按文件聚合进 Unmigratable；Store 写入失败视为整体中断（避免半文件重复）。
func importAuditFile(ctx context.Context, st store.Store, path, sessionID string, rep *SourceReport) error {
	f, err := os.Open(path)
	if err != nil {
		rep.Unmigratable = append(rep.Unmigratable, UnmigratedItem{Source: path, Reason: "打开失败: " + err.Error()})
		return nil
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	lineNo := 0
	bad := 0
	var firstErr error
	for sc.Scan() {
		lineNo++
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var ev audit.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			bad++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ev.Action == "" {
			bad++
			if firstErr == nil {
				firstErr = errors.New("action 为空")
			}
			continue
		}
		if err := st.Audit().Write(ctx, toStoreAuditEvent(ev, AuditScope{SessionID: sessionID})); err != nil {
			return fmt.Errorf("import audit %s（line %d）: %w", path, lineNo, err)
		}
		rep.Imported++
	}
	if err := sc.Err(); err != nil {
		rep.Unmigratable = append(rep.Unmigratable, UnmigratedItem{Source: path, Reason: "读取中断: " + err.Error()})
	}
	if bad > 0 {
		rep.Unmigratable = append(rep.Unmigratable, UnmigratedItem{
			Source: path,
			Reason: fmt.Sprintf("%d 行解析失败（首个错误：%v）", bad, firstErr),
		})
	}
	return nil
}

// migrateSkills 把内置 Skill 清单写入 SkillStore（Upsert 幂等），再用迁移标记
// 保证重复运行时不再计入 Imported。
func migrateSkills(ctx context.Context, st store.Store) (SourceReport, error) {
	rep := SourceReport{}
	done, err := migrationDone(ctx, st, "skills")
	if err != nil {
		return rep, err
	}
	if done {
		rep.Skipped = 1
		return rep, nil
	}
	reg, err := skills.NewBuiltin()
	if err != nil {
		return rep, fmt.Errorf("load builtin skills: %w", err)
	}
	list := reg.List()
	rep.Found = len(list)
	for _, sk := range list {
		if err := st.Skills().Upsert(ctx, &store.Skill{
			Name: sk.Name, Spec: sk, Source: string(sk.Source), Enabled: true,
		}); err != nil {
			return rep, fmt.Errorf("upsert skill %s: %w", sk.Name, err)
		}
		rep.Imported++
	}
	if err := markMigration(ctx, st, "skills"); err != nil {
		return rep, err
	}
	return rep, nil
}

// migrateReports 报告文件没有四期存储表，迁移只做"显式保留"：原文件不删除、
// 不搬移，并把每条文件计入 Kept 与 Unmigratable（原因固定），让用户看到明确结论。
func migrateReports(ctx context.Context, dir string) (SourceReport, error) {
	rep := SourceReport{}
	if dir == "" {
		return rep, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return rep, nil
		}
		return rep, fmt.Errorf("read reports dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		rep.Found++
		rep.Kept++
		rep.Unmigratable = append(rep.Unmigratable, UnmigratedItem{
			Source: filepath.Join(dir, e.Name()),
			Reason: "报告文件保留在原目录，未迁移（四期暂无报告存储表）",
		})
	}
	return rep, nil
}

// migrationDone 检查一类三期数据是否已迁移（幂等标记）。标记存放在
// settings 的 GroupMigration 内部分组，不对外部 API 暴露。
func migrationDone(ctx context.Context, st store.Store, key string) (bool, error) {
	set, err := st.Settings().Get(ctx, settings.GroupMigration, key)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return set.Value == "1", nil
}

// markMigration 写入"已迁移"标记。
func markMigration(ctx context.Context, st store.Store, key string) error {
	if err := st.Settings().Set(ctx, &store.Settings{
		Group: settings.GroupMigration, Key: key, Value: "1",
	}); err != nil {
		return fmt.Errorf("mark migration %s: %w", key, err)
	}
	return nil
}
