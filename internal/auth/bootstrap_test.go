package auth

import (
	"context"
	"strings"
	"testing"
)

// TestBootstrapRequiresPasswordOnlyOnFirstStart 锁 P6.1 / U-09：首启判定以 users 表为准。
// 空库缺口令必须启动失败；已有库重启不再要求 K8S_AI_ADMIN_PASSWORD。
func TestBootstrapRequiresPasswordOnlyOnFirstStart(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()

	// 空库 + 缺口令：拒绝启动，绝不悄悄建空口令管理员。
	svc := New(st.Users(), st.Roles())
	err := svc.Bootstrap(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "first start requires an admin password") {
		t.Fatalf("空库缺口令应启动失败, got %v", err)
	}
	if users, _ := st.Users().List(ctx); len(users) != 0 {
		t.Fatalf("启动失败时不应建任何用户, got %d", len(users))
	}
	if roles, _ := st.Roles().List(ctx); len(roles) != 0 {
		t.Fatalf("启动失败时不应落半成品角色, got %d", len(roles))
	}

	// 首次引导成功。
	if err := svc.Bootstrap(ctx, "first-pw"); err != nil {
		t.Fatalf("首次引导失败: %v", err)
	}

	// 已有库重启（口令为空）：不再要求口令，只幂等修复角色。
	if err := svc.Bootstrap(ctx, ""); err != nil {
		t.Fatalf("已有库重启不应要求口令: %v", err)
	}
	users, err := st.Users().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != DefaultAdminUsername {
		t.Fatalf("重启不应新增或改动管理员, got %+v", users)
	}
	if _, err := svc.Login(ctx, DefaultAdminUsername, "first-pw"); err != nil {
		t.Fatalf("原管理员口令应保持有效: %v", err)
	}
}

// TestBootstrapReconcilesRoles 锁角色修复：角色被误删后重启补回，用户不受影响。
func TestBootstrapReconcilesRoles(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	svc := New(st.Users(), st.Roles())
	if err := svc.BootstrapWithName(ctx, "k8sadmin", "pw-12345"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	roles, err := st.Roles().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != len(defaultRoles()) {
		t.Fatalf("应有 %d 个内置角色, got %d", len(defaultRoles()), len(roles))
	}
	for _, r := range roles {
		if r.Name == RoleOperator {
			if err := st.Roles().Delete(ctx, r.ID); err != nil {
				t.Fatalf("delete role: %v", err)
			}
		}
	}
	// 已有用户 + 缺角色 + 空口令：应补回角色且不要求口令。
	if err := svc.Bootstrap(ctx, ""); err != nil {
		t.Fatalf("reconcile bootstrap: %v", err)
	}
	after, err := st.Roles().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(defaultRoles()) {
		t.Fatalf("角色未修复, got %d", len(after))
	}
	users, err := st.Users().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != "k8sadmin" {
		t.Fatalf("自定义管理员用户名应保留, got %+v", users)
	}
}
