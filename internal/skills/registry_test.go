package skills

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestNewBuiltinLoadsFiveDomains(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatalf("NewBuiltin: %v", err)
	}
	got := r.List()
	if len(got) != 5 {
		t.Fatalf("加载到 %d 个 skill，want 5", len(got))
	}
	want := []string{
		"create-config",
		"create-network",
		"create-rbac",
		"create-storage",
		"create-workload",
	}
	names := make([]string, 0, len(got))
	for _, s := range got {
		if s.Source != SourceBuiltin {
			t.Errorf("skill %s source = %q, want %q", s.Name, s.Source, SourceBuiltin)
		}
		if s.Path == "" {
			t.Errorf("skill %s 未带来源路径", s.Name)
		}
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("skills = %v, want %v", names, want)
	}
}

func TestBuiltinForGVK(t *testing.T) {
	r, err := NewBuiltin()
	if err != nil {
		t.Fatalf("NewBuiltin: %v", err)
	}
	cases := []struct {
		gvk  schema.GroupVersionKind
		want string
	}{
		{schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}, "create-workload"},
		{schema.GroupVersionKind{Group: "", Version: "v1", Kind: "ConfigMap"}, "create-config"},
		{schema.GroupVersionKind{Group: "", Version: "v1", Kind: "PersistentVolumeClaim"}, "create-storage"},
		{schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}, "create-network"},
		{schema.GroupVersionKind{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}, "create-rbac"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			skills := r.ForGVK(tc.gvk)
			for _, s := range skills {
				if s.Name == tc.want {
					return
				}
			}
			t.Fatalf("ForGVK(%v) 未命中 %s: %+v", tc.gvk, tc.want, skills)
		})
	}
	if got := r.ForGVK(schema.GroupVersionKind{Group: "example.io", Version: "v1", Kind: "Widget"}); len(got) != 0 {
		t.Fatalf("未知 Kind 不应命中内置 skill: %+v", got)
	}
}

func TestMatchIntent(t *testing.T) {
	r := New()
	registerProcedural(t, r, "mysql-troubleshoot", "MySQL 故障排查方法论", "database")
	registerProcedural(t, r, "mysql-backup", "MySQL 定期备份", "database")
	registerProcedural(t, r, "linux-sysadmin", "Linux 系统管理员排查", "linux")

	if got := r.MatchIntent(""); got != nil {
		t.Fatalf("空意图应返回 nil，实际 %v", got)
	}
	// 精确名称命中（大小写不敏感，P3.8.4）。
	got := r.MatchIntent("MySQL-TROUBLESHOOT")
	if len(got) != 1 || got[0].Name != "mysql-troubleshoot" {
		t.Fatalf("名称命中 = %+v", got)
	}
	// Description 子串命中。
	got = r.MatchIntent("定期备份")
	if len(got) != 1 || got[0].Name != "mysql-backup" {
		t.Fatalf("描述子串命中 = %+v", got)
	}
	// Name 子串可同时命中多个 procedural Skill。
	got = r.MatchIntent("mysql")
	if len(got) != 2 {
		t.Fatalf("名称子串命中 %d 个：%+v", len(got), got)
	}
	// Domain 子串命中。
	got = r.MatchIntent("database")
	if len(got) != 2 {
		t.Fatalf("domain 子串命中 %d 个：%+v", len(got), got)
	}
	// structured Skill 不参与意图检索。
	b, err := NewBuiltin()
	if err != nil {
		t.Fatalf("NewBuiltin: %v", err)
	}
	if n := len(b.MatchIntent("workload")); n != 0 {
		t.Fatalf("structured skill 不应被意图命中，实际 %d", n)
	}
	// procedural Skill 不走资源创建 GVK 命中。
	if n := len(r.ForGVK(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})); n != 0 {
		t.Fatalf("procedural skill 不应走 GVK 命中，实际 %d", n)
	}
}

func registerProcedural(t *testing.T, r Registry, name, desc, domain string) {
	t.Helper()
	s := Skill{
		Type:        TypeProcedural,
		Source:      SourceCustom,
		Name:        name,
		Description: desc,
		Domain:      domain,
		Version:     "1.0.0",
		Body:        "1. 取证步骤：kubectl describe\n",
	}
	if err := r.Register(s); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

func TestMatchIntentScoringNameHitOutranksDescOnly(t *testing.T) {
	r := New()
	// 名称带 mysql（名称词命中权重高）。
	registerProcedural(t, r, "mysql-troubleshoot", "MySQL 在 Kubernetes 上的故障排查与诊断方法论", "database")
	// 仅描述含“Pod/重启/排查”，无名称词重合。
	registerProcedural(t, r, "crash-loop", "Pod 反复重启的通用排查步骤", "workload")
	// 完全无关。
	registerProcedural(t, r, "linux-sysadmin", "Linux 系统管理员日常运维", "linux")

	// 长自然语言多关键词：mysql 命中名称词（3 分）+ 描述词，crash-loop 只命中描述词，
	// 因此 mysql-troubleshoot 排序第一，两者都在 Top2 注入（单轮上限内）。
	got := r.MatchIntent("我这边有个 mysql 服务的 pod 一直在重启 帮我排查看下是什么问题")
	if len(got) != 2 {
		t.Fatalf("应命中两个相关 skill（上限 %d），实际 %d：%+v", intentMaxHits, len(got), got)
	}
	if got[0].Name != "mysql-troubleshoot" {
		t.Fatalf("名称词命中应排在描述词命中前：%+v", got)
	}
	names := map[string]bool{got[0].Name: true, got[1].Name: true}
	if !names["mysql-troubleshoot"] || !names["crash-loop"] {
		t.Fatalf("Top2 应同时包含 mysql-troubleshoot 与 crash-loop：%+v", got)
	}
	// 无关提问不应误命中 mysql。
	if got := r.MatchIntent("你看下 linux 主机负载高怎么处理"); len(got) == 0 || got[0].Name != "linux-sysadmin" {
		t.Fatalf("linux 领域提问应命中 linux-sysadmin：%+v", got)
	}
}

func TestMatchIntentCapAtMostTwoByKeyword(t *testing.T) {
	r := New()
	for _, name := range []string{"mysql-alpha", "mysql-beta", "mysql-gamma"} {
		registerProcedural(t, r, name, "MySQL 相关方法论", "database")
	}
	got := r.MatchIntent("mysql")
	if len(got) != intentMaxHits {
		t.Fatalf("名称子串命中应被截到 %d 篇，实际 %d", intentMaxHits, len(got))
	}
	if got[0].Name != "mysql-alpha" || got[1].Name != "mysql-beta" {
		t.Fatalf("应按名称稳定排序：%+v", got)
	}
}

func TestMatchIntentLongPhraseWithoutKeywordDoesNotHit(t *testing.T) {
	r := New()
	registerProcedural(t, r, "k8s-mysql-troubleshoot", "MySQL 在 Kubernetes 上的故障排查方法论", "database")
	// 该句不含任何与方法论相关的关键词，关键词打分路径不应误命中；
	// 这类“看下某 namespace 数据”场景由 LLM 借助 get_skill 协议来判断。
	if got := r.MatchIntent("你看下 test-complex namespace 下面的数据是啥问题"); len(got) != 0 {
		t.Fatalf("无关键词长句不应自动命中：%+v", got)
	}
}

func TestFindByName(t *testing.T) {
	r := New()
	registerProcedural(t, r, "MySQL-Troubleshoot", "x", "database")
	if _, ok := FindByName(r, "mysql-troubleshoot"); !ok {
		t.Fatal("大小写不敏感精确查名应命中")
	}
	if _, ok := FindByName(r, "  MYSQL-TROUBLESHOOT  "); !ok {
		t.Fatal("空白裁剪后应命中")
	}
	if _, ok := FindByName(r, "not-exist"); ok {
		t.Fatal("未知名称不应命中")
	}
	if _, ok := FindByName(r, "  "); ok {
		t.Fatal("空名称不应命中")
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	r := New()
	s := Skill{
		Name:    "x",
		Source:  SourceBuiltin,
		Kinds:   []string{"v1/ConfigMap"},
		Risk:    RiskRule{Default: RiskMedium},
		Version: "1.0.0",
	}
	if err := r.Register(s); err != nil {
		t.Fatalf("首次注册: %v", err)
	}
	if err := r.Register(s); err == nil {
		t.Fatal("重复注册应报错")
	}
}

func TestRegisterDefaultsEmptySourceToBuiltin(t *testing.T) {
	r := New()
	s := Skill{
		Name:  "y",
		Kinds: []string{"v1/ConfigMap"},
		Risk:  RiskRule{Default: RiskMedium},
	}
	if err := r.Register(s); err != nil {
		t.Fatalf("注册: %v", err)
	}
	for _, got := range r.List() {
		if got.Source != SourceBuiltin {
			t.Fatalf("source = %q, want %q", got.Source, SourceBuiltin)
		}
	}
}
