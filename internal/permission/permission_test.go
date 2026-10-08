package permission

import "testing"

func TestHas(t *testing.T) {
	tests := []struct {
		name     string
		perms    []string
		required string
		want     bool
	}{
		{"required empty means granted", []string{}, "", true},
		{"exact match", []string{"chat.read"}, "chat.read", true},
		{"no match", []string{"chat.read"}, "chat.write", false},
		{"wildcard all", []string{"*"}, "user.manage", true},
		{"segment wildcard exact", []string{"cluster.*"}, "cluster.read", true},
		{"segment wildcard manage implies read", []string{"cluster.*"}, "cluster.manage", true},
		{"segment wildcard not prefix", []string{"cluster.*"}, "clusterx.read", false},
		{"segment wildcard requires dot", []string{"cluster.*"}, "cluster", false},
		{"empty perms deny", []string{}, "chat.read", false},
	}
	e := NewEnforcer(nil)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := e.Has(tc.perms, tc.required); got != tc.want {
				t.Fatalf("Has(%v, %q) = %v, want %v", tc.perms, tc.required, got, tc.want)
			}
		})
	}
}

func TestPolicyRequire(t *testing.T) {
	p := NewPolicy(
		Rule{Method: "GET", Path: "/healthz", Access: AccessPublic},
		Rule{Method: "GET", Path: "/api/v1/scans/{id}", Access: AccessPermission, Permission: ClusterRead},
		Rule{Method: "POST", Path: "/api/v1/scans", Access: AccessPermission, Permission: ChatWrite},
		Rule{Method: "ANY", Path: "/api/v1/auth/*", Access: AccessAuthenticated},
		Rule{Method: "GET", Path: "/api/v1/auth/*", Access: AccessPublic}, // 后登记覆盖 default
	)

	tests := []struct {
		name           string
		method         string
		path           string
		wantAccess     Access
		wantPermission string
		wantOK         bool
	}{
		{"public", "GET", "/healthz", AccessPublic, "", true},
		{"scan get via placeholder", "GET", "/api/v1/scans/abc", AccessPermission, ClusterRead, true},
		{"scan create", "POST", "/api/v1/scans", AccessPermission, ChatWrite, true},
		{"auth any overridden by later public", "GET", "/api/v1/auth/login", AccessPublic, "", true},
		{"unknown method", "DELETE", "/healthz", AccessPermission, "", false},
		{"unknown path", "GET", "/api/v1/unknown", AccessPermission, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			access, perm, ok := p.RequireAccess(tc.method, tc.path)
			if ok != tc.wantOK {
				t.Fatalf("RequireAccess(%s, %s) ok = %v, want %v", tc.method, tc.path, ok, tc.wantOK)
			}
			if ok && (access != tc.wantAccess || perm != tc.wantPermission) {
				t.Fatalf("RequireAccess(%s, %s) = (%v, %q), want (%v, %q)",
					tc.method, tc.path, access, perm, tc.wantAccess, tc.wantPermission)
			}
		})
	}
}

func TestAuthorize(t *testing.T) {
	e := NewEnforcer(NewPolicy(
		Rule{Method: "GET", Path: "/api/v1/scans/{id}", Access: AccessPermission, Permission: ClusterRead},
		Rule{Method: "GET", Path: "/api/v1/auth/me", Access: AccessAuthenticated},
	))

	if _, allowed, registered := e.Authorize([]string{"cluster.read"}, "GET", "/api/v1/scans/1"); !allowed || !registered {
		t.Fatal("viewer with cluster.read 应允许读取 scan")
	}
	if _, allowed, _ := e.Authorize([]string{"*"}, "GET", "/api/v1/scans/1"); !allowed {
		t.Fatal("admin 通配应允许")
	}
	if _, allowed, _ := e.Authorize([]string{"*"}, "GET", "/api/v1/auth/me"); !allowed {
		t.Fatal("已登录即可访问 /auth/me")
	}
	if _, allowed, _ := e.Authorize([]string{"chat.read"}, "GET", "/api/v1/scans/1"); allowed {
		t.Fatal("无 cluster.read 应拒绝")
	}
	if _, _, registered := e.Authorize([]string{"*"}, "GET", "/api/v1/unregistered"); registered {
		t.Fatal("未登记路由应 registered=false")
	}
}

func TestRouteMatches(t *testing.T) {
	tests := []struct {
		pattern string
		target  string
		want    bool
	}{
		{"/healthz", "/healthz", true},
		{"/healthz", "/health", false},
		{"/api/v1/scans/{id}", "/api/v1/scans/abc", true},
		{"/api/v1/scans/{id}", "/api/v1/scans/", false},
		{"/api/v1/scans/{id}", "/api/v1/scans/a/b", false},
		{"/api/v1/files/{dir}/*", "/api/v1/files/x/y/z", true},
		{"/api/v1/auth/*", "/api/v1/auth/login", true},
		{"/api/v1/scans/{id}", "/api/v1/scans/abc/x", false},
	}
	for _, tc := range tests {
		if got := routeMatches(tc.pattern, tc.target); got != tc.want {
			t.Fatalf("routeMatches(%q, %q) = %v, want %v", tc.pattern, tc.target, got, tc.want)
		}
	}
}
