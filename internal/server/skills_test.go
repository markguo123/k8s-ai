package server

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/k8s-ai/k8s-ai/internal/auth"
	"github.com/k8s-ai/k8s-ai/internal/skills"
	"github.com/k8s-ai/k8s-ai/internal/store"
)

// SkillView 与服务端返回的 Skill 记录对齐（只读管理视图，无执行动作字段）。
type SkillView struct {
	Name             string `json:"name"`
	Source           string `json:"source"`
	Enabled          bool   `json:"enabled"`
	ValidationStatus string `json:"validationStatus,omitempty"`
	HitCount         int64  `json:"hitCount"`
	Spec             struct {
		Type   string   `json:"type"`
		Domain string   `json:"domain"`
		Kinds  []string `json:"kinds"`
		Body   string   `json:"body,omitempty"`
		Assets []struct {
			Name  string `json:"name"`
			Path  string `json:"path"`
			Kind  string `json:"kind"`
			Inert bool   `json:"inert"`
		} `json:"assets,omitempty"`
	} `json:"spec"`
}

// customSkillYAML 是 P3.6 导入用例的合法 custom Skill：
// 只携带方法论/校验/风险升档，不含任何执行动作字段。
const customSkillYAML = `name: my-custom-skill
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
    message: 未固定镜像 tag，建议钉到具体版本
risk:
  elevateWhen:
    - name: host_network
      path: spec.hostNetwork
      condition: true
      to: HIGH
      message: 启用了宿主机网络，风险升档为 HIGH
preChecks:
  - name: namespace_exists
`

// createUserWithRole 建一个指定默认角色的用户并返回其登录 token。
func createUserWithRole(t *testing.T, p *platformServer, username string, role string) string {
	t.Helper()
	var roleID string
	roles, err := p.store.Roles().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		if r.Name == role {
			roleID = r.ID
		}
	}
	if roleID == "" {
		t.Fatalf("默认角色 %q 不存在", role)
	}
	hash, err := auth.HashPassword("pw-test")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	err = p.store.Users().Create(context.Background(), &store.User{
		ID: "u-" + username, Username: username, PasswordHash: hash, RoleID: roleID,
		Status: "active", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return p.tokenAs(t, username, "pw-test")
}

func findSkillName(list []skills.Skill, name string) *skills.Skill {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
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

// TestImportSkillAndGetSingle 是 P3.6 主路径：custom Skill 导入 201，
// 随后可通过单条与列表接口读到（含启用状态）。
func TestImportSkillAndGetSingle(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	resp, raw := p.do(t, http.MethodPost, "/api/v1/skills/import", token, customSkillYAML)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("custom 导入应 201, got %d: %s", resp.StatusCode, raw)
	}
	var imported []SkillView
	decodeData(t, mustReplay(t, raw), &imported)
	if len(imported) != 1 || imported[0].Name != "my-custom-skill" ||
		imported[0].Source != "custom" || !imported[0].Enabled {
		t.Fatalf("导入返回记录不符: %+v", imported)
	}

	resp, raw = p.do(t, http.MethodGet, "/api/v1/skills/my-custom-skill", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET 单条应 200, got %d: %s", resp.StatusCode, raw)
	}
	var one SkillView
	decodeData(t, mustReplay(t, raw), &one)
	if one.Name != "my-custom-skill" || !one.Enabled {
		t.Fatalf("单条读取不符: %+v", one)
	}

	resp, raw = p.do(t, http.MethodGet, "/api/v1/skills", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET 列表应 200, got %d: %s", resp.StatusCode, raw)
	}
	var list []SkillView
	decodeData(t, mustReplay(t, raw), &list)
	found := false
	for _, v := range list {
		if v.Name == "my-custom-skill" {
			found = true
		}
	}
	if !found {
		t.Fatal("导入的 Skill 未出现在列表中")
	}
}

// TestImportSkillRejectsInvalid 锁定非法导入整批拒绝且不落库：
// 非法 YAML / 空内容 / third-party 缺作者 / builtin 覆盖 / 未知字段。
func TestImportSkillRejectsInvalid(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	cases := []struct {
		name string
		body string
	}{
		{"非法 YAML", "[unclosed"},
		{"空内容", ""},
		{"third-party 缺作者", "name: vendor-skill\ndomain: config\nversion: 1.0.0\nsource: third-party\nkinds:\n  - v1/ConfigMap\n"},
		{"builtin 覆盖拒绝", "name: create-config\ndomain: config\nversion: 3.0.0\nsource: builtin\nkinds:\n  - v1/ConfigMap\n"},
		{"未知字段", "name: x\nsource: custom\nkinds:\n  - v1/ConfigMap\nbogus_field: 1\n"},
	}
	for _, tc := range cases {
		resp, raw := p.do(t, http.MethodPost, "/api/v1/skills/import", token, tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: 应 400, got %d: %s", tc.name, resp.StatusCode, raw)
		}
	}
	recs, err := p.store.Skills().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Source != "builtin" {
			t.Fatalf("非法导入泄入 Store: %+v", r)
		}
	}
}

// TestSkillManageRequiresPermission 验证 skill.manage 权限门禁：
// operator 不持 skill.manage，导入与启停都 403 且不产生副作用。
func TestSkillManageRequiresPermission(t *testing.T) {
	p := newPlatformServer(t)
	token := createUserWithRole(t, p, "op-manage", auth.RoleOperator)

	if resp, raw := p.do(t, http.MethodPost, "/api/v1/skills/import", token, customSkillYAML); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator 导入应 403, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/skills/create-config/enabled", token, `{"enabled":false}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator 启停应 403, got %d: %s", resp.StatusCode, raw)
	}
	rec, err := p.store.Skills().Get(context.Background(), "create-config")
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Enabled {
		t.Fatal("被拒绝的启停不得改动 Store")
	}
	recs, err := p.store.Skills().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Source != "builtin" {
			t.Fatalf("越权导入泄入 Store: %+v", r)
		}
	}
}

// TestSkillEnableDisable 是 P3.7 主路径：PATCH 停用后内存 registry 立即移除，
// 持久化记录保留；重新启用后恢复进 registry。
func TestSkillEnableDisable(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	if resp, raw := p.do(t, http.MethodPost, "/api/v1/skills/import", token, customSkillYAML); resp.StatusCode != http.StatusCreated {
		t.Fatalf("导入失败: %d %s", resp.StatusCode, raw)
	}
	resp, raw := p.do(t, http.MethodPatch, "/api/v1/skills/my-custom-skill/enabled", token, `{"enabled":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("停用应 200, got %d: %s", resp.StatusCode, raw)
	}
	var off SkillView
	decodeData(t, mustReplay(t, raw), &off)
	if off.Enabled {
		t.Fatal("停用响应应 enabled=false")
	}
	if findSkillName(p.skillPlat.Registry().List(), "my-custom-skill") != nil {
		t.Fatal("停用后 registry 仍含该 Skill")
	}
	rec, err := p.store.Skills().Get(context.Background(), "my-custom-skill")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Enabled {
		t.Fatal("Store 里应 enabled=false")
	}

	resp, raw = p.do(t, http.MethodPatch, "/api/v1/skills/my-custom-skill/enabled", token, `{"enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重新启用应 200, got %d: %s", resp.StatusCode, raw)
	}
	if findSkillName(p.skillPlat.Registry().List(), "my-custom-skill") == nil {
		t.Fatal("启用后 registry 应恢复该 Skill")
	}
}

// TestSkillEndpointErrors 锁定错误信封：单条 404、非法/缺字段 PATCH body 400、
// 停用不存在 Skill 404。
func TestSkillEndpointErrors(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	if resp, raw := p.do(t, http.MethodGet, "/api/v1/skills/nope", token, ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET 不存在应 404, got %d: %s", resp.StatusCode, raw)
	}
	for name, body := range map[string]string{
		"非法 JSON":   "nope",
		"缺 enabled": "{}",
	} {
		resp, raw := p.do(t, http.MethodPatch, "/api/v1/skills/create-config/enabled", token, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: 应 400, got %d: %s", name, resp.StatusCode, raw)
		}
	}
	if resp, raw := p.do(t, http.MethodPatch, "/api/v1/skills/nope/enabled", token, `{"enabled":true}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("停用不存在应 404, got %d: %s", resp.StatusCode, raw)
	}
}

// proceduralSkillMD 是 P3.8.2 文本导入用例的合法标准 SKILL.md：
// frontmatter 只声明 name/description（版本缺省 1.0.0），正文作为不可信文本入库。
const proceduralSkillMD = `---
name: mysql-troubleshoot
description: MySQL 部署故障排查方法论，按 Pod/事件/日志/PVC 分层诊断。
---
# MySQL 故障排查

## 诊断步骤
1. 检查 Pod 状态：kubectl get pods -n <namespace> -l app=mysql
2. 查看事件：kubectl describe pod <pod> -n <namespace>
3. 查看日志：kubectl logs <pod> -n <namespace> -c mysql | grep -i error
`

// TestImportProceduralDocs 是 P3.8.2 主路径：原始 SKILL.md 文本导入 201，
// 记录为 custom/procedural 且带正文；单条与列表均可读到。
func TestImportProceduralDocs(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	resp, raw := p.do(t, http.MethodPost, "/api/v1/skills/import-procedural", token, proceduralSkillMD)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("SKILL.md 导入应 201, got %d: %s", resp.StatusCode, raw)
	}
	var imported []SkillView
	decodeData(t, mustReplay(t, raw), &imported)
	if len(imported) != 1 || imported[0].Name != "mysql-troubleshoot" ||
		imported[0].Source != "custom" || imported[0].Spec.Type != "procedural" ||
		!imported[0].Enabled || !strings.Contains(imported[0].Spec.Body, "kubectl get pods") {
		t.Fatalf("procedural 导入记录不符: %+v", imported)
	}

	resp, raw = p.do(t, http.MethodGet, "/api/v1/skills/mysql-troubleshoot", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET 单条应 200, got %d: %s", resp.StatusCode, raw)
	}
	var one SkillView
	decodeData(t, mustReplay(t, raw), &one)
	if one.Name != "mysql-troubleshoot" || one.Spec.Type != "procedural" {
		t.Fatalf("单条 procedural 读取不符: %+v", one)
	}
}

// addZipEntry 往 zip.Writer 写一个文本条目（测试构造技能包用）。
func addZipEntry(t *testing.T, zw *zip.Writer, name, content string) {
	t.Helper()
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
}

// TestImportProceduralZip 是 P3.8.3 主路径：multipart 上传单个技能包 zip 201，
// 白色名单文本资产入库，scripts/ 一律 inert=true。
func TestImportProceduralZip(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	addZipEntry(t, zw, "SKILL.md", "---\nname: mysql-troubleshoot\ndescription: zip 技能包示例\n---\n# 正文")
	addZipEntry(t, zw, "reference/tips.md", "# tips")
	addZipEntry(t, zw, "scripts/run.sh", "#!/bin/sh\necho hi")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "mysql-skill.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(zbuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodPost, p.ts.URL+"/api/v1/skills/import-zip", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("zip 导入应 201, got %d: %s", resp.StatusCode, raw)
	}
	var imported []SkillView
	decodeData(t, mustReplay(t, string(raw)), &imported)
	if len(imported) != 1 || imported[0].Name != "mysql-troubleshoot" ||
		imported[0].Spec.Type != "procedural" {
		t.Fatalf("zip 导入记录不符: %+v", imported)
	}
	assets := map[string]bool{}
	for _, a := range imported[0].Spec.Assets {
		assets[a.Path+"|"+a.Kind] = a.Inert
	}
	if seen, ok := assets["reference/tips.md|text"]; !ok || seen {
		t.Fatalf("缺少白名单文本资产: %+v", imported[0].Spec.Assets)
	}
	if seen, ok := assets["scripts/run.sh|script"]; !ok || !seen {
		t.Fatalf("scripts 资产应 inert=true: %+v", imported[0].Spec.Assets)
	}
}

// TestImportProceduralRejectsInvalid 锁定 P3.8.2/P3.8.3 非法导入整体拒绝：
// 非 frontmatter 开头 / 缺 name / 缺 description / 空内容均 400 且不落库。
func TestImportProceduralRejectsInvalid(t *testing.T) {
	p := newPlatformServer(t)
	token := p.tokenAs(t, "admin", "admin-pw")
	cases := []struct {
		name string
		path string
		body string
	}{
		{"非 frontmatter 开头", "/api/v1/skills/import-procedural", "# 没有 frontmatter 的正文"},
		{"缺 name", "/api/v1/skills/import-procedural", "---\ndescription: 缺 name\n---\n正文"},
		{"缺 description", "/api/v1/skills/import-procedural", "---\nname: no-desc\n---\n正文"},
		{"空内容", "/api/v1/skills/import-procedural", ""},
	}
	for _, tc := range cases {
		resp, raw := p.do(t, http.MethodPost, tc.path, token, tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: 应 400, got %d: %s", tc.name, resp.StatusCode, raw)
		}
	}
	// zip 缺 file 字段 → 400
	resp, raw := p.do(t, http.MethodPost, "/api/v1/skills/import-zip", token, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺失 file 应 400, got %d: %s", resp.StatusCode, raw)
	}
	recs, err := p.store.Skills().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Source != "builtin" {
			t.Fatalf("非法 procedural 导入泄入 Store: %+v", r)
		}
	}
}

// TestProceduralImportRequiresPermission 验证 P3.8 新端点的 permission 门禁：
// operator 不持 skill.manage，文本与 zip 导入均 403 且不产生副作用。
func TestProceduralImportRequiresPermission(t *testing.T) {
	p := newPlatformServer(t)
	token := createUserWithRole(t, p, "op-proc", auth.RoleOperator)

	if resp, raw := p.do(t, http.MethodPost, "/api/v1/skills/import-procedural", token, proceduralSkillMD); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator 文本导入应 403, got %d: %s", resp.StatusCode, raw)
	}
	if resp, raw := p.do(t, http.MethodPost, "/api/v1/skills/import-zip", token, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator zip 导入应 403, got %d: %s", resp.StatusCode, raw)
	}
	recs, err := p.store.Skills().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Source != "builtin" {
			t.Fatalf("越权 procedural 导入泄入 Store: %+v", r)
		}
	}
}
