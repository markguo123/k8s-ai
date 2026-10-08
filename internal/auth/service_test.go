package auth

import (
	"context"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.OpenSQLite("file:auth-test-" + time.Now().Format("150405.000000000") + "?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestHashAndVerifyPassword(t *testing.T) {
	h1, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashPassword("s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("相同密码的两次哈希不应相同（随机盐）")
	}
	for _, h := range []string{h1, h2} {
		ok, err := verifyPassword(h, "s3cret")
		if err != nil || !ok {
			t.Fatalf("校验正确密码失败: ok=%v err=%v", ok, err)
		}
		bad, err := verifyPassword(h, "wrong")
		if err != nil || bad {
			t.Fatalf("错误密码不应通过: ok=%v err=%v", bad, err)
		}
	}
}

func TestBootstrapAndLogin(t *testing.T) {
	st := newStore(t)
	svc := New(st.Users(), st.Roles())
	ctx := context.Background()

	if err := svc.Bootstrap(ctx, "admin-pw"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	// 幂等：重复引导不报错、不重复建用户。
	if err := svc.Bootstrap(ctx, "admin-pw"); err != nil {
		t.Fatalf("repeat bootstrap: %v", err)
	}
	users, err := st.Users().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != DefaultAdminUsername {
		t.Fatalf("应有且仅有一个 admin 用户, got %d", len(users))
	}

	sess, err := svc.Login(ctx, DefaultAdminUsername, "admin-pw")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if sess.Token == "" || sess.User == nil || sess.User.Username != DefaultAdminUsername {
		t.Fatalf("会话不完整: %+v", sess)
	}
	if time.Until(sess.ExpiresAt) > SessionTTL || time.Until(sess.ExpiresAt) <= 0 {
		t.Fatalf("过期时间异常: %v", sess.ExpiresAt)
	}

	u, role, err := svc.UserByToken(ctx, sess.Token)
	if err != nil {
		t.Fatalf("user by token: %v", err)
	}
	if u.Username != DefaultAdminUsername || role == nil || role.Name != RoleAdmin {
		t.Fatalf("me 解析错误: user=%+v role=%+v", u, role)
	}
}

func TestLoginInvalidCredentials(t *testing.T) {
	st := newStore(t)
	svc := New(st.Users(), st.Roles())
	ctx := context.Background()
	if err := svc.Bootstrap(ctx, "pw"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ user, pass string }{
		{DefaultAdminUsername, "wrong"},
		{"ghost", "pw"},
		{"", "pw"},
		{DefaultAdminUsername, ""},
	} {
		if _, err := svc.Login(ctx, c.user, c.pass); err != ErrInvalidCredentials {
			t.Fatalf("凭据 %q/%q 应失败: %v", c.user, c.pass, err)
		}
	}
}

func TestUserByTokenInvalidAndExpired(t *testing.T) {
	st := newStore(t)
	svc := New(st.Users(), st.Roles())
	ctx := context.Background()
	if err := svc.Bootstrap(ctx, "pw"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.UserByToken(ctx, ""); err != ErrInvalidToken {
		t.Fatalf("空 token 应失败: %v", err)
	}
	if _, _, err := svc.UserByToken(ctx, "nope"); err != ErrInvalidToken {
		t.Fatalf("未知 token 应失败: %v", err)
	}

	sess, err := svc.Login(ctx, DefaultAdminUsername, "pw")
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Now().Add(SessionTTL + time.Hour) }
	if _, _, err := svc.UserByToken(ctx, sess.Token); err != ErrTokenExpired {
		t.Fatalf("过期 token 应返回 ErrTokenExpired: %v", err)
	}
}

func TestLogout(t *testing.T) {
	st := newStore(t)
	svc := New(st.Users(), st.Roles())
	ctx := context.Background()
	if err := svc.Bootstrap(ctx, "pw"); err != nil {
		t.Fatal(err)
	}
	sess, err := svc.Login(ctx, DefaultAdminUsername, "pw")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(sess.Token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := svc.UserByToken(ctx, sess.Token); err != ErrInvalidToken {
		t.Fatalf("登出后 token 应失效: %v", err)
	}
	if err := svc.Logout(""); err != ErrInvalidToken {
		t.Fatalf("空 token 登出应失败: %v", err)
	}
}
