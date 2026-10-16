package service

import (
	"context"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/skills"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// openSkillStore 打开内存 SQLite 并迁移（与 chat_persist_test 同款装配）。
func openSkillStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.OpenSQLite("file:skill-test-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func TestSkillPlatformSeedAndReload(t *testing.T) {
	st := openSkillStore(t)
	p := NewSkillPlatform(st.Skills())
	if err := p.SeedBuiltins(context.Background()); err != nil {
		t.Fatalf("SeedBuiltins: %v", err)
	}
	if err := p.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	recs, err := st.Skills().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 5 {
		t.Fatalf("seeded skills = %d, want 5", len(recs))
	}
	reg := p.Registry()
	if len(reg.List()) != 5 {
		t.Fatalf("registry skills = %d, want 5", len(reg.List()))
	}
}

func TestSkillPlatformDisableExcludesFromRegistry(t *testing.T) {
	st := openSkillStore(t)
	p := NewSkillPlatform(st.Skills())
	if err := p.SeedBuiltins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.SetEnabled(context.Background(), "create-config", false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	if len(p.Registry().List()) != 4 {
		t.Fatalf("disabled skill 仍留在 registry: %d", len(p.Registry().List()))
	}
	// 记录仍持久化，仅运行态被摘除。
	rec, err := st.Skills().Get(context.Background(), "create-config")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Enabled {
		t.Fatal("create-config 应已被停用")
	}
	// 重新启用回到 registry。
	if err := p.SetEnabled(context.Background(), "create-config", true); err != nil {
		t.Fatal(err)
	}
	if len(p.Registry().List()) != 5 {
		t.Fatalf("enable 后 registry 应为 5，实际 %d", len(p.Registry().List()))
	}
}

func TestSkillPlatformRecordHit(t *testing.T) {
	st := openSkillStore(t)
	p := NewSkillPlatform(st.Skills())
	if err := p.SeedBuiltins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.RecordHit(context.Background(), "create-workload"); err != nil {
		t.Fatalf("RecordHit: %v", err)
	}
	if err := p.RecordHit(context.Background(), "create-workload"); err != nil {
		t.Fatalf("RecordHit: %v", err)
	}
	rec, err := st.Skills().Get(context.Background(), "create-workload")
	if err != nil {
		t.Fatal(err)
	}
	if rec.HitCount != 2 {
		t.Fatalf("hit_count = %d, want 2", rec.HitCount)
	}
	if rec.LastHit == nil {
		t.Fatal("last_hit 应为非空")
	}
}

func TestSkillPlatformSetValidation(t *testing.T) {
	st := openSkillStore(t)
	p := NewSkillPlatform(st.Skills())
	if err := p.SeedBuiltins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.SetValidation(context.Background(), "create-network", "ok"); err != nil {
		t.Fatalf("SetValidation: %v", err)
	}
	rec, err := st.Skills().Get(context.Background(), "create-network")
	if err != nil {
		t.Fatal(err)
	}
	if rec.ValidationStatus != "ok" || rec.LastValidation == nil {
		t.Fatalf("validation_status=%q last=%v, want ok 且非空", rec.ValidationStatus, rec.LastValidation)
	}
}

func TestSkillPlatformUpsertValidatesAndPersists(t *testing.T) {
	st := openSkillStore(t)
	p := NewSkillPlatform(st.Skills())
	// 非法来源被拒绝：execution 语义字段不应进入 Skill。
	bad := &store.Skill{
		Name: "evil", Source: string(skills.SourceThirdParty),
		Spec: skills.Skill{Name: "evil", Source: skills.SourceThirdParty, Kinds: []string{"v1/Pod"}},
	}
	if err := p.Upsert(context.Background(), bad); err == nil {
		t.Fatal("缺 version 的 third-party skill 应被拒")
	}
	// 合法 custom Skill 可落库并进入 registry。
	good := &store.Skill{
		Name: "team-scan", Source: string(skills.SourceCustom), Enabled: true,
		Spec: skills.Skill{
			Name: "team-scan", Domain: "workload", Version: "1.0.0",
			Author:      "platform-team",
			Description: "团队自定义工作量校验方法论",
			Source:      skills.SourceCustom,
			Kinds:       []string{"v1/ConfigMap"},
			Examples: []skills.Example{{
				Name:    "example-cm",
				Purpose: "展示 ConfigMap 产出形态",
				Content: "data:\n  key: value",
			}},
			Risk: skills.RiskRule{ElevateWhen: []skills.RiskClause{{
				Name: "danger", Path: "spec.data", Condition: "exists", To: skills.RiskHigh, Message: "team raises",
			}}},
			Validation: []skills.Rule{{
				Name: "no_data", Path: "spec.data", Condition: "missing", Severity: skills.SeveritySuggest, Message: "data required",
			}},
		},
	}
	if err := p.Upsert(context.Background(), good); err != nil {
		t.Fatalf("Upsert(custom): %v", err)
	}
	recs, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		if r.Name == "team-scan" {
			found = true
		}
	}
	if !found {
		t.Fatal("team-scan 未出现在 List 中")
	}
	// §13.1 元数据（author/description/examples）应随 spec 完整落库。
	rec, err := st.Skills().Get(context.Background(), "team-scan")
	if err != nil {
		t.Fatalf("get team-scan: %v", err)
	}
	if rec.Spec.Author != "platform-team" || rec.Spec.Description == "" || len(rec.Spec.Examples) != 1 {
		t.Fatalf("§13 元数据未往返: %+v", rec.Spec)
	}
	// 校验 Upsert 后 registry 重建把新 Skill 带上（含来源绑定）。
	regList := p.Registry().List()
	names := make([]string, 0, len(regList))
	for _, skr := range regList {
		names = append(names, skr.Name)
	}
	t.Logf("registry names=%v", names)
	if got := findRegSkill(regList, "team-scan"); got == nil {
		t.Fatalf("team-scan 未装配进 registry (len=%d)", len(regList))
	} else if got.Source != skills.SourceCustom {
		t.Fatalf("team-scan source=%q, want custom", got.Source)
	}
}

func findRegSkill(list []skills.Skill, name string) *skills.Skill {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}
