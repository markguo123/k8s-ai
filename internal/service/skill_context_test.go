package service

import (
	"context"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/session"
	"github.com/k8s-ai/k8s-ai/internal/skills"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// newChatServiceWithProceduralSkill 装配带一个 procedural 自定义 Skill 的 chatService
// （Store + SkillPlatform 同款连接），用于 buildSkillContext 的路径测试。
func newChatServiceWithProceduralSkill(t *testing.T) *chatService {
	t.Helper()
	st := openSkillStore(t)
	plat := NewSkillPlatform(st.Skills())
	if err := plat.SeedBuiltins(context.Background()); err != nil {
		t.Fatalf("SeedBuiltins: %v", err)
	}
	sk := skills.Skill{
		Type:        skills.TypeProcedural,
		Source:      skills.SourceCustom,
		Name:        "k8s-mysql-troubleshoot",
		Domain:      "database",
		Version:     "1.0.0",
		Description: "MySQL 在 Kubernetes 上的故障排查方法论",
		Body:        "1. kubectl get pod 取证\n2. 查看 PVC 是否 Pending\n",
	}
	if err := plat.Upsert(context.Background(), &store.Skill{
		Name: sk.Name, Source: string(sk.Source), Enabled: true, Spec: sk,
	}); err != nil {
		t.Fatalf("Upsert procedural: %v", err)
	}
	return &chatService{store: st, skillsPlat: plat}
}

func TestBuildSkillContextExplicitOnly(t *testing.T) {
	s := newChatServiceWithProceduralSkill(t)
	sess := &session.Session{ID: "s1"}
	scope := &runScope{sessID: sess.ID}
	reg := s.liveSkillRegistry()

	ctx, hits, err := s.buildSkillContext(context.Background(), sess, scope, reg, "你看下 default 的 workload 有没有问题", []string{"  k8s-mysql-troubleshoot  "})
	if err != nil {
		t.Fatalf("explicit: %v", err)
	}
	if len(hits) != 1 || hits[0].mode != "explicit" || hits[0].name != "k8s-mysql-troubleshoot" {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(ctx, "1. kubectl get pod 取证") {
		t.Fatalf("应注入命中正文")
	}
}

func TestBuildSkillContextExplicitUnknownRejectsWholeBatch(t *testing.T) {
	s := newChatServiceWithProceduralSkill(t)
	sess := &session.Session{ID: "s1"}
	scope := &runScope{sessID: sess.ID}
	reg := s.liveSkillRegistry()

	ctx, hits, err := s.buildSkillContext(context.Background(), sess, scope, reg, "q", []string{"k8s-mysql-troubleshoot", "does-not-exist"})
	if err == nil {
		t.Fatal("含未知名字应整批拒绝")
	}
	if ctx != "" || hits != nil {
		t.Fatalf("拒绝时不应注入任何正文：ctx=%q hits=%v", ctx, hits)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("错误应点名未知 Skill：%v", err)
	}

	// 全部空白视为未指定，同样拒绝。
	_, _, err = s.buildSkillContext(context.Background(), sess, scope, reg, "q", []string{"  "})
	if err == nil {
		t.Fatal("空白显式列表应拒绝")
	}
}

func TestBuildSkillContextExplicitDisabledRejected(t *testing.T) {
	s := newChatServiceWithProceduralSkill(t)
	// 停用后 registry 不再包含该 Skill，显式指定也应整批拒绝。
	if err := s.skillsPlat.SetEnabled(context.Background(), "k8s-mysql-troubleshoot", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	sess := &session.Session{ID: "s1"}
	scope := &runScope{sessID: sess.ID}
	_, _, err := s.buildSkillContext(context.Background(), sess, scope, s.liveSkillRegistry(), "q", []string{"k8s-mysql-troubleshoot"})
	if err == nil {
		t.Fatal("停用 Skill 显式指定应被拒")
	}
}

func TestBuildSkillContextAutoIntent(t *testing.T) {
	s := newChatServiceWithProceduralSkill(t)
	sess := &session.Session{ID: "s1"}
	scope := &runScope{sessID: sess.ID}
	reg := s.liveSkillRegistry()

	ctx, hits, err := s.buildSkillContext(context.Background(), sess, scope, reg, "我这边有 mysql 的 pod 一直重启 帮我排查下", nil)
	if err != nil {
		t.Fatalf("auto: %v", err)
	}
	if len(hits) != 1 || hits[0].mode != "auto" || hits[0].name != "k8s-mysql-troubleshoot" {
		t.Fatalf("hits = %+v", hits)
	}
	if !strings.Contains(ctx, "k8s-mysql-troubleshoot") {
		t.Fatalf("auto 命中应注入 Skill 元数据")
	}

	// 未命中返回空串。
	ctx2, hits2, err2 := s.buildSkillContext(context.Background(), sess, scope, reg, "今天天气怎么样", nil)
	if err2 != nil || ctx2 != "" || hits2 != nil {
		t.Fatalf("未命中应返回空：ctx=%q hits=%v err=%v", ctx2, hits2, err2)
	}
}
