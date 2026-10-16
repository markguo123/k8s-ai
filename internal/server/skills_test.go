package server

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// SkillView 与服务端返回的 Skill 记录对齐（只读管理视图，无执行动作字段）。
type SkillView struct {
	Name             string `json:"name"`
	Source           string `json:"source"`
	Enabled          bool   `json:"enabled"`
	ValidationStatus string `json:"validationStatus,omitempty"`
	HitCount         int64  `json:"hitCount"`
}

func TestListSkillsRequiresAuth(t *testing.T) {
	p := newPlatformServer(t)
	resp, raw := p.do(t, http.MethodGet, "/api/v1/skills", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, got %d: %s", resp.StatusCode, raw)
	}
}

func TestListSkillsReturnsBuiltins(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	resp, raw := p.do(t, http.MethodGet, "/api/v1/skills", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /skills 应 200, got %d: %s", resp.StatusCode, raw)
	}
	var list []SkillView
	decodeData(t, mustReplay(t, raw), &list)
	names := map[string]bool{}
	for _, v := range list {
		names[v.Name] = true
		if v.Source != "builtin" {
			t.Errorf("skill %s source=%q, want builtin", v.Name, v.Source)
		}
		if !v.Enabled {
			t.Errorf("skill %s 默认应 enabled", v.Name)
		}
	}
	for _, want := range []string{"create-config", "create-network", "create-rbac", "create-storage", "create-workload"} {
		if !names[want] {
			t.Errorf("缺少内置 skill %s", want)
		}
	}
	// 红线：Skill 管理视图绝不含任何执行动作字段。按键名精确匹配，
	// 避免合法字段名误报（例如 description 内含 "script" 子串）。
	execField := regexp.MustCompile(`"(script|command|exec|shell)"\s*:`)
	if m := execField.FindString(strings.ToLower(raw)); m != "" {
		t.Fatalf("Skill 视图出现禁止执行字段 %s", m)
	}
}

// TestListSkillsPermissionDenied 验证权限判定落在 Permission 层：
// operator 不持 skill.read，读取应 403。
func TestListSkillsPermissionDenied(t *testing.T) {
	p := newPlatformServer(t)
	hash, err := auth.HashPassword("op-pw")
	if err != nil {
		t.Fatal(err)
	}
	var opRole string
	roles, err := p.store.Roles().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		if role.Name == auth.RoleOperator {
			opRole = role.ID
		}
	}
	if opRole == "" {
		t.Fatal("默认角色里应有 operator")
	}
	err = p.store.Users().Create(context.Background(), &store.User{
		ID: "u-skill-op", Username: "op-skill1", PasswordHash: hash, RoleID: opRole,
		Status: "active", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create operator: %v", err)
	}
	token := p.tokenAs(t, "op-skill1", "op-pw")
	resp, raw := p.do(t, http.MethodGet, "/api/v1/skills", token, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("无 skill.read 应 403, got %d: %s", resp.StatusCode, raw)
	}
}
