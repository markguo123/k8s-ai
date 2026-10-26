// 平台能力回归：User/Role、Config、Skill、Cluster、Session 五类能力在
// 真实装配下可用，且权限边界（Role → Permission → Policy）按四期口径生效。
package platform_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/store"
)

// TestUserAndRoleCapability 覆盖 P3.11/P3.13：用户 CRUD 与三角色固定语义。
func TestUserAndRoleCapability(t *testing.T) {
	h := newHarness(t)
	admin := h.adminToken()

	var roles []struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	h.must(h.do(http.MethodGet, "/api/v1/roles", admin, ""), http.StatusOK).data(t, &roles)
	if len(roles) != 3 {
		t.Fatalf("内置角色应为 3 个，实得 %d: %+v", len(roles), roles)
	}
	byName := map[string][]string{}
	for _, r := range roles {
		byName[r.Name] = r.Permissions
	}
	for _, name := range []string{"admin", "operator", "user"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("缺少内置角色 %q", name)
		}
	}
	// 三角色权限口径：admin 全权，user/operator 零平台权限起步。
	if len(byName["admin"]) != 1 || byName["admin"][0] != "*" {
		t.Errorf("admin 角色权限应为 [*]，实得 %+v", byName["admin"])
	}
	for _, name := range []string{"user", "operator"} {
		if len(byName[name]) != 0 {
			t.Errorf("%s 角色应零平台权限起步，实得 %+v", name, byName[name])
		}
	}

	userID := h.createUser(admin, "alice", "alice-password-123", "operator")
	// 重名冲突要用合法 roleId，否则会先在角色校验上 400，撞不到唯一性判定。
	dupBody, _ := json.Marshal(map[string]string{
		"username": "alice",
		"password": "another-password-1",
		"roleId":   h.roleIDByName(admin, "user"),
	})
	if r := h.do(http.MethodPost, "/api/v1/users", admin, string(dupBody)); r.status != http.StatusConflict {
		t.Errorf("重名用户应 409，实得 %d: %s", r.status, r.body)
	}
	h.must(h.do(http.MethodPatch, "/api/v1/users/"+userID, admin, `{"displayName":"Alice Zhang"}`), http.StatusOK)

	var listed []struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"displayName"`
	}
	h.must(h.do(http.MethodGet, "/api/v1/users", admin, ""), http.StatusOK).data(t, &listed)
	var found bool
	for _, u := range listed {
		if u.ID == userID {
			found = true
			if u.Username != "alice" || u.DisplayName != "Alice Zhang" {
				t.Errorf("用户资料未按预期更新: %+v", u)
			}
		}
		if strings.Contains(u.ID, "$argon2") {
			t.Errorf("用户列表疑似混入密码哈希: %+v", u)
		}
	}
	if !found {
		t.Fatalf("列表中找不到新建用户 %s", userID)
	}
	h.must(h.do(http.MethodDelete, "/api/v1/users/"+userID, admin, ""), http.StatusOK)
	h.must(h.do(http.MethodGet, "/api/v1/users", admin, ""), http.StatusOK).data(t, &listed)
	for _, u := range listed {
		if u.ID == userID {
			t.Fatal("删除后用户仍在列表中")
		}
	}
}

// TestPermissionBoundariesOnZeroRole 覆盖「未授权 API 仍返回 403」：零权限角色
// 打不到任何平台管理面，未登记路由默认拒绝，匿名请求 401。
func TestPermissionBoundariesOnZeroRole(t *testing.T) {
	h := newHarness(t)
	admin := h.adminToken()
	h.createUser(admin, "bob", "bob-password-12345", "user")
	zero := h.login("bob", "bob-password-12345")

	denied := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/users", ""},
		{http.MethodPost, "/api/v1/users", `{"username":"x","password":"password123","roleId":"r"}`},
		{http.MethodGet, "/api/v1/roles", ""},
		{http.MethodGet, "/api/v1/grants", ""},
		{http.MethodGet, "/api/v1/settings/model", ""},
		{http.MethodPatch, "/api/v1/settings/model", `{"api_key":"sk-nope"}`},
		{http.MethodGet, "/api/v1/skills", ""},
		{http.MethodPost, "/api/v1/skills/import", "name: nope\n"},
		{http.MethodGet, "/api/v1/clusters", ""},
		{http.MethodPost, "/api/v1/clusters", `{"kubeconfig":"x"}`},
		{http.MethodDelete, "/api/v1/clusters/c-1", ""},
	}
	for _, tc := range denied {
		if r := h.do(tc.method, tc.path, zero, tc.body); r.status != http.StatusForbidden {
			t.Errorf("零权限角色 %s %s = %d, want 403；响应: %s", tc.method, tc.path, r.status, r.body)
		}
	}
	// 管理员可用同一批接口（对照组，排除「路径写错导致全 403」的假绿）。
	for _, path := range []string{"/api/v1/users", "/api/v1/roles", "/api/v1/settings/model", "/api/v1/skills", "/api/v1/clusters"} {
		if r := h.do(http.MethodGet, path, admin, ""); r.status != http.StatusOK {
			t.Errorf("管理员 GET %s = %d, want 200；响应: %s", path, r.status, r.body)
		}
	}
	// /api/v1 下未登记路由默认拒绝；未认证一律 401。
	if r := h.do(http.MethodGet, "/api/v1/not-registered", admin, ""); r.status != http.StatusForbidden {
		t.Errorf("未登记 API 路由 = %d, want 403", r.status)
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/users", ""},
		{http.MethodGet, "/api/v1/sessions", ""},
		{http.MethodGet, "/api/v1/auth/me", ""},
	} {
		if r := h.do(tc.method, tc.path, "", tc.body); r.status != http.StatusUnauthorized {
			t.Errorf("匿名 %s %s = %d, want 401", tc.method, tc.path, r.status)
		}
	}
}

// TestSettingsCenterMasksSensitiveValues 覆盖 P3.9/P3.10：分组可读写，敏感键
// 只写不读（value 为 null、hasValue=true），响应与审计都不带明文。
func TestSettingsCenterMasksSensitiveValues(t *testing.T) {
	const apiKey = "sk-live-SENTINEL-apikey-0001"
	const kubeconfigPath = "/etc/k8s-ai/SENTINEL-kubeconfig-path"
	h := newHarness(t)
	admin := h.adminToken()

	patch := func(group, body string) reply {
		return h.do(http.MethodPatch, "/api/v1/settings/"+group, admin, body)
	}
	h.must(patch("model", `{"api_key":"`+apiKey+`","model":"gpt-4o"}`), http.StatusOK)
	h.must(patch("ops", `{"kubeconfig":"`+kubeconfigPath+`"}`), http.StatusOK)

	if r := h.do(http.MethodGet, "/api/v1/settings/model", admin, ""); strings.Contains(r.body, apiKey) {
		t.Fatalf("GET settings/model 泄漏 api_key 明文: %s", r.body)
	} else {
		var view struct {
			Group               string `json:"group"`
			EncryptionAvailable bool   `json:"encryptionAvailable"`
			Items               []struct {
				Key       string          `json:"key"`
				Value     json.RawMessage `json:"value"`
				HasValue  bool            `json:"hasValue"`
				Sensitive bool            `json:"sensitive"`
			} `json:"items"`
		}
		h.must(r, http.StatusOK).data(t, &view)
		if !view.EncryptionAvailable {
			t.Error("已装配加密器时 encryptionAvailable 应为 true")
		}
		var keyItem, modelItem *bool
		for i := range view.Items {
			switch view.Items[i].Key {
			case "api_key":
				if !view.Items[i].Sensitive || !view.Items[i].HasValue {
					t.Errorf("api_key 应为 sensitive 且 hasValue=true: %+v", view.Items[i])
				}
				if string(view.Items[i].Value) != "null" {
					t.Errorf("api_key 读视图 value 应为 null，实得 %s", view.Items[i].Value)
				}
				keyItem = &view.Items[i].HasValue
			case "model":
				modelItem = &view.Items[i].HasValue
			}
		}
		if keyItem == nil || modelItem == nil {
			t.Fatal("model 分组缺少 api_key / model 键")
		}
	}
	if r := h.do(http.MethodGet, "/api/v1/settings/ops", admin, ""); strings.Contains(r.body, kubeconfigPath) {
		t.Errorf("GET settings/ops 泄漏 kubeconfig 路径: %s", r.body)
	}
	// 未登记键整批拒绝。
	if r := patch("model", `{"bogus_key":"x"}`); r.status != http.StatusBadRequest {
		t.Errorf("未登记配置键应 400，实得 %d: %s", r.status, r.body)
	}
	// CONFIG_CHANGE 审计不得携带值。
	var changes int
	for _, e := range h.auditEvents() {
		if e.Action != store.ActionConfigChange && e.EventType != store.AuditEventConfigChange {
			continue
		}
		changes++
		if strings.Contains(e.Payload, apiKey) || strings.Contains(e.Payload, kubeconfigPath) {
			t.Errorf("配置审计携带敏感值: %s", e.Payload)
		}
	}
	if changes == 0 {
		t.Error("配置变更未写审计")
	}
}

// TestSkillPlatformCapability 覆盖 P3.5-P3.7：内置 Skill 列表、custom 导入、
// 非法导入整批拒绝、内置不可覆盖、启停生效。
func TestSkillPlatformCapability(t *testing.T) {
	h := newHarness(t)
	admin := h.adminToken()

	var builtins []struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Enabled bool   `json:"enabled"`
	}
	h.must(h.do(http.MethodGet, "/api/v1/skills", admin, ""), http.StatusOK).data(t, &builtins)
	if len(builtins) == 0 {
		t.Fatal("内置 Skill 种子未生效")
	}
	var builtinName string
	for _, s := range builtins {
		if s.Source == "builtin" && builtinName == "" {
			builtinName = s.Name
		}
		if s.Name == "" {
			t.Errorf("Skill 记录缺少 name: %+v", s)
		}
	}
	if builtinName == "" {
		t.Fatal("没有 source=builtin 的 Skill 记录")
	}

	const customYAML = `name: platform-regression-skill
domain: workload
version: 1.0.0
source: custom
kinds:
  - apps/v1/Deployment
validation:
  - name: image_pinned
    path: spec.template.spec.containers[].image
    condition: latest_or_empty
    severity: suggest
    message: 未固定镜像 tag
risk:
  elevateWhen:
    - name: host_network
      path: spec.hostNetwork
      condition: "true"
      to: HIGH
      message: 启用宿主机网络，风险升档
`
	h.must(h.do(http.MethodPost, "/api/v1/skills/import", admin, customYAML), http.StatusCreated)
	var imported struct {
		Name    string `json:"name"`
		Source  string `json:"source"`
		Enabled bool   `json:"enabled"`
	}
	h.must(h.do(http.MethodGet, "/api/v1/skills/platform-regression-skill", admin, ""), http.StatusOK).data(t, &imported)
	if imported.Source != "custom" || !imported.Enabled {
		t.Errorf("导入结果异常: %+v", imported)
	}
	// 导入的 Skill 只有方法论，不得携带任何执行动作字段。
	if r := h.do(http.MethodGet, "/api/v1/skills/platform-regression-skill", admin, ""); strings.Contains(r.body, "command") || strings.Contains(r.body, "script") {
		t.Errorf("Skill 记录疑似含执行动作字段: %s", r.body)
	}

	cases := []struct{ name, yaml string }{
		{"内置覆盖", "name: " + builtinName + "\ndomain: config\nversion: 9.9.9\nsource: builtin\nkinds:\n  - v1/ConfigMap\n"},
		{"未知字段", "name: bogus-skill\nsource: custom\nkinds:\n  - v1/ConfigMap\nbogus_field: 1\n"},
		{"非法 YAML", "\tname: broken\n  bad indent\n"},
	}
	for _, tc := range cases {
		if r := h.do(http.MethodPost, "/api/v1/skills/import", admin, tc.yaml); r.status != http.StatusBadRequest {
			t.Errorf("%s 应 400，实得 %d: %s", tc.name, r.status, r.body)
		}
	}
	// 覆盖被拒后内置 Skill 仍在原位。
	if r := h.do(http.MethodGet, "/api/v1/skills/"+builtinName, admin, ""); r.status != http.StatusOK {
		t.Errorf("内置 Skill 读取 = %d, want 200", r.status)
	}

	disabled := h.do(http.MethodPatch, "/api/v1/skills/platform-regression-skill/enabled", admin, `{"enabled":false}`)
	var after struct {
		Enabled bool `json:"enabled"`
	}
	h.must(disabled, http.StatusOK).data(t, &after)
	if after.Enabled {
		t.Error("停用未生效")
	}
	h.must(h.do(http.MethodPatch, "/api/v1/skills/platform-regression-skill/enabled", admin, `{"enabled":true}`), http.StatusOK)
	if r := h.do(http.MethodPatch, "/api/v1/skills/nope-missing/enabled", admin, `{"enabled":false}`); r.status != http.StatusNotFound {
		t.Errorf("不存在的 Skill 启停应 404，实得 %d", r.status)
	}
}

// TestClusterLifecycleKeepsKubeconfigOpaque 覆盖 P3.1-P3.4：CREATE/TEST/LIST/GET/
// CURRENT/DELETE 可用，任何响应只回元数据，不回 kubeconfig。
func TestClusterLifecycleKeepsKubeconfigOpaque(t *testing.T) {
	const sentinel = "SENTINEL-KUBECONFIG-PRIVATE-TOKEN-7731"
	kubeconfig := "apiVersion: v1\nkind: Config\nclusters:\n- name: plat\n  cluster:\n    server: https://kube.example.internal:6443\n    certificate-authority-data: " + sentinel + "\n"
	h := newHarness(t)
	admin := h.adminToken()

	var info struct {
		Server  string `json:"server"`
		Version string `json:"version"`
		Health  string `json:"health"`
	}
	testBody, _ := json.Marshal(map[string]string{"kubeconfig": kubeconfig})
	h.must(h.do(http.MethodPost, "/api/v1/clusters/test", admin, string(testBody)), http.StatusOK).data(t, &info)
	if info.Version == "" {
		t.Error("连测未返回服务器版本")
	}

	createBody, _ := json.Marshal(map[string]string{"name": "prod-hangzhou", "kubeconfig": kubeconfig})
	var created struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Server string `json:"server"`
		Status string `json:"status"`
	}
	resp := h.must(h.do(http.MethodPost, "/api/v1/clusters", admin, string(createBody)), http.StatusCreated)
	if strings.Contains(resp.body, sentinel) {
		t.Fatalf("CREATE 响应回显了 kubeconfig 内容: %s", resp.body)
	}
	resp.data(t, &created)
	if created.ID == "" || created.Name != "prod-hangzhou" {
		t.Fatalf("建集群结果异常: %+v", created)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/clusters"},
		{http.MethodGet, "/api/v1/clusters/" + created.ID},
		{http.MethodGet, "/api/v1/clusters/current"},
	} {
		r := h.must(h.do(tc.method, tc.path, admin, ""), http.StatusOK)
		if strings.Contains(r.body, sentinel) {
			t.Errorf("GET %s 回显了 kubeconfig 内容", tc.path)
		}
	}
	if r := h.do(http.MethodPost, "/api/v1/clusters", admin, `{"name":"prod-hangzhou","kubeconfig":"x"}`); r.status == http.StatusOK || r.status == http.StatusCreated {
		t.Errorf("同名集群应被拒绝，实得 %d: %s", r.status, r.body)
	}
	if r := h.do(http.MethodPost, "/api/v1/clusters", admin, `{"name":"empty-cred"}`); r.status != http.StatusBadRequest {
		t.Errorf("缺少 kubeconfig 应 400，实得 %d: %s", r.status, r.body)
	}

	cred, err := h.st.Credentials().GetByCluster(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("读取集群凭据失败: %v", err)
	}
	if !strings.HasPrefix(cred.KubeconfigSecret, "v1:") {
		t.Errorf("凭据应以 v1: 版本前缀密文落库，实得前缀 %q", firstRunes(cred.KubeconfigSecret, 8))
	}
	if strings.Contains(cred.KubeconfigSecret, sentinel) {
		t.Error("凭据明文落库")
	}

	h.must(h.do(http.MethodDelete, "/api/v1/clusters/"+created.ID, admin, ""), http.StatusOK)
	if r := h.do(http.MethodGet, "/api/v1/clusters/"+created.ID, admin, ""); r.status != http.StatusNotFound {
		t.Errorf("删除后应 404，实得 %d", r.status)
	}
}

// firstRunes 安全截取前 n 个 rune，用于在断言里展示密文前缀而不暴露全文。
func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// TestSessionOwnerIsolationOnRealStack 用真实登录 Token 复现会话越权：owner 只见
// 自己的会话，他人一律 404，admin 可读可管。
func TestSessionOwnerIsolationOnRealStack(t *testing.T) {
	h := newUserHarness(t)
	sessions := "/api/v1/sessions"

	var owned struct {
		ID     string `json:"id"`
		UserID string `json:"userId"`
	}
	h.must(h.do(http.MethodPost, sessions, h.ownerToken, `{}`), http.StatusOK).plain(t, &owned)
	if owned.ID == "" || owned.UserID != h.ownerID {
		t.Fatalf("会话未按创建者归属: %+v", owned)
	}

	var listed []struct {
		ID string `json:"id"`
	}
	h.must(h.do(http.MethodGet, sessions, h.ownerToken, ""), http.StatusOK).plain(t, &listed)
	if len(listed) != 1 || listed[0].ID != owned.ID {
		t.Fatalf("owner 列表应只含自己的会话: %+v", listed)
	}
	h.must(h.do(http.MethodGet, sessions, h.outsiderToken, ""), http.StatusOK).plain(t, &listed)
	for _, s := range listed {
		if s.ID == owned.ID {
			t.Fatal("零权限用户列表中出现了他人会话")
		}
	}
	h.must(h.do(http.MethodGet, sessions, h.adminToken, ""), http.StatusOK).plain(t, &listed)
	var adminSees bool
	for _, s := range listed {
		if s.ID == owned.ID {
			adminSees = true
		}
	}
	if !adminSees {
		t.Error("admin 应能列出全部会话")
	}

	surfaces := []struct{ name, method, path, body string }{
		{"历史/SSE", http.MethodGet, sessions + "/" + owned.ID + "/stream", ""},
		{"续用会话", http.MethodPost, sessions, `{"id":"` + owned.ID + `"}`},
		{"发消息", http.MethodPost, sessions + "/" + owned.ID + "/send-message", `{"query":"hi"}`},
		{"审批", http.MethodPost, sessions + "/" + owned.ID + "/approve", `{"choice":"yes"}`},
		{"取消", http.MethodPost, sessions + "/" + owned.ID + "/cancel", `{}`},
		{"改名", http.MethodPost, sessions + "/" + owned.ID + "/rename", `{"name":"hijack"}`},
		{"置顶", http.MethodPost, sessions + "/" + owned.ID + "/pin", `{"pinned":true}`},
		{"删除", http.MethodDelete, sessions + "/" + owned.ID, ""},
	}
	for _, tc := range surfaces {
		r := h.do(tc.method, tc.path, h.outsiderToken, tc.body)
		// 越权不得泄漏资源存在性：404（SSE 读取同样按不存在处理）。
		if r.status != http.StatusNotFound {
			t.Errorf("外人%s他人会话 = %d, want 404；响应: %s", tc.name, r.status, r.body)
		}
	}
	// owner 自己的同一批动作不受影响。
	h.must(h.do(http.MethodPost, sessions+"/"+owned.ID+"/rename", h.ownerToken, `{"name":"mine now"}`), http.StatusOK)
	h.must(h.do(http.MethodPost, sessions+"/"+owned.ID+"/pin", h.ownerToken, `{"pinned":true}`), http.StatusOK)
	// admin 跨用户操作留痕（U-06）。
	h.must(h.do(http.MethodPost, sessions+"/"+owned.ID+"/rename", h.adminToken, `{"name":"admin touched"}`), http.StatusOK)
	var audited bool
	for _, e := range h.auditEvents() {
		if e.SessionID == owned.ID && strings.Contains(e.Action, "session.admin") {
			audited = true
			if e.UserID == h.ownerID {
				t.Errorf("跨用户留痕的操作者应是 admin，实得 %q", e.UserID)
			}
		}
	}
	if !audited {
		t.Error("admin 跨用户改名未写审计（U-06）")
	}
}
