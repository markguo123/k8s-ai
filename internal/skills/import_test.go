package skills

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
)

// zipFromFiles 用内存构建一个含指定文件的 zip（内容为明文文本）。
func zipFromFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// zipWithSymlink 构建一个含符号链接条目的 zip（P3.8.3 应整包拒绝）。
func zipWithSymlink(t *testing.T, linkName, target string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: linkName}
	h.SetMode(os.ModeSymlink)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatalf("zip symlink header: %v", err)
	}
	if _, err := w.Write([]byte(target)); err != nil {
		t.Fatalf("zip symlink write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

const proceduralSample = `---
name: mysql-troubleshoot
description: MySQL 故障排查方法论（Pod 无法启动/启动失败/慢查询等场景）
version: 1.0.0
---
# MySQL 故障排查

1. 检查 Pod 状态：kubectl get pods -n <ns> -l app=mysql
2. 查看事件：kubectl describe pod <pod> -n <ns>
3. 查看日志：kubectl logs <pod> -n <ns> -c mysql | grep -i error
`

// fencedProceduralSample 用 ```markdown 围栏包裹 frontmatter（真实 SKILL.md 常见
// 写法）：围栏闭行紧跟 frontmatter 闭合行之后，正文不参与围栏。
const fencedProceduralSample = "```markdown\n" +
	"---\n" +
	"name: mysql-troubleshoot\n" +
	"description: MySQL 故障排查方法论（Pod 无法启动/启动失败/慢查询等场景）\n" +
	"version: 1.0.0\n" +
	"---\n" +
	"```\n" +
	"# MySQL 故障排查\n\n" +
	"1. kubectl get pods -n <ns> -l app=mysql\n" +
	"2. kubectl logs <pod> -n <ns> -c mysql | grep -i error\n"

func TestParseProceduralSingleDoc(t *testing.T) {
	got, err := ParseProceduralDocs([]byte(proceduralSample))
	if err != nil {
		t.Fatalf("ParseProceduralDocs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("返回 %d 篇，want 1", len(got))
	}
	s := got[0]
	if s.Type != TypeProcedural || s.Source != SourceCustom {
		t.Fatalf("type/source = %q/%q", s.Type, s.Source)
	}
	if s.Name != "mysql-troubleshoot" {
		t.Fatalf("name = %q", s.Name)
	}
	if !strings.Contains(s.Description, "MySQL 故障排查") {
		t.Fatalf("description = %q", s.Description)
	}
	if !strings.Contains(s.Body, "kubectl get pods") {
		t.Fatalf("body 未保留正文: %q", s.Body)
	}
}

func TestParseProceduralFencedFrontmatter(t *testing.T) {
	got, err := ParseProceduralDocs([]byte(fencedProceduralSample))
	if err != nil {
		t.Fatalf("ParseProceduralDocs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("返回 %d 篇，want 1", len(got))
	}
	s := got[0]
	if s.Name != "mysql-troubleshoot" {
		t.Fatalf("name = %q", s.Name)
	}
	if !strings.Contains(s.Description, "MySQL 故障排查") {
		t.Fatalf("description = %q", s.Description)
	}
	if strings.Contains(s.Body, "```") {
		t.Fatalf("正文不应携带围栏: %q", s.Body)
	}
	if !strings.Contains(s.Body, "kubectl get pods") {
		t.Fatalf("正文未保留方法论内容: %q", s.Body)
	}
}

func TestParseProceduralMultiDocsWithFence(t *testing.T) {
	// 第二篇以 ```markdown 围栏包裹 frontmatter，切片边界应按围栏行识别，
	// 而非把前置分隔符 --- 误判为第二篇的 frontmatter 开界。
	fencedSecond := "```markdown\n" +
		"---\n" +
		"name: linux-sysadmin\n" +
		"description: Linux 系统管理员通用排查方法\n" +
		"---\n" +
		"```\n" +
		"1. 查看负载：uptime\n" +
		"2. 查看内存：free -h\n"
	raw := proceduralSample + "\n---\n" + fencedSecond
	got, err := ParseProceduralDocs([]byte(raw))
	if err != nil {
		t.Fatalf("ParseProceduralDocs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("返回 %d 篇，want 2", len(got))
	}
	if got[0].Name != "mysql-troubleshoot" || got[1].Name != "linux-sysadmin" {
		t.Fatalf("名称不符: %q, %q", got[0].Name, got[1].Name)
	}
	if strings.Contains(got[1].Body, "```") {
		t.Fatalf("第二篇正文不应携带围栏: %q", got[1].Body)
	}
}

func TestParseProceduralMultiDocs(t *testing.T) {
	second := `---
name: linux-sysadmin
description: Linux 系统管理员通用排查方法
---
1. 查看负载：uptime
2. 查看内存：free -h
`
	raw := proceduralSample + "\n---\n" + second
	got, err := ParseProceduralDocs([]byte(raw))
	if err != nil {
		t.Fatalf("ParseProceduralDocs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("返回 %d 篇，want 2", len(got))
	}
	if got[0].Name != "mysql-troubleshoot" || got[1].Name != "linux-sysadmin" {
		t.Fatalf("名称顺序不符: %s, %s", got[0].Name, got[1].Name)
	}
	if got[0].Body == got[1].Body {
		t.Fatal("多篇正文不应互相串扰")
	}
}

func TestParseProceduralZipWrappedDirAndFence(t *testing.T) {
	// 整包被单层目录包裹（压缩技能文件夹本身），根无 SKILL.md；且 SKILL.md
	// 以 ```markdown 围栏包裹 frontmatter。两条真实包形态应同时兼容。
	body := fencedProceduralSample
	data := zipFromFiles(t, map[string]string{
		"k8s-mysql-troubleshoot/SKILL.md":             body,
		"k8s-mysql-troubleshoot/references/schema.md": "references 资产",
		"k8s-mysql-troubleshoot/scripts/check.sh":     "#!/bin/sh\nkubectl get pods\n",
	})
	got, err := ParseProceduralZip(data)
	if err != nil {
		t.Fatalf("ParseProceduralZip: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("返回 %d 个 skill，want 1", len(got))
	}
	s := got[0]
	if s.Name != "mysql-troubleshoot" {
		t.Fatalf("name = %q", s.Name)
	}
	if strings.Contains(s.Body, "```") {
		t.Fatalf("正文不应携带围栏: %q", s.Body)
	}
	if len(s.Assets) != 2 {
		t.Fatalf("assets = %d，want 2: %+v", len(s.Assets), s.Assets)
	}
	for _, a := range s.Assets {
		if strings.HasPrefix(a.Path, "k8s-mysql-troubleshoot/") {
			t.Errorf("资产路径未剥离包裹目录: %q", a.Path)
		}
		switch a.Path {
		case "references/schema.md":
			if a.Inert {
				t.Errorf("references 资产不应 inert: %+v", a)
			}
		case "scripts/check.sh":
			if !a.Inert || a.Kind != "script" {
				t.Errorf("scripts 资产应 inert: %+v", a)
			}
		default:
			t.Errorf("意外资产路径 %q", a.Path)
		}
	}
}

func TestParseProceduralZipRejectsMultiTopDirs(t *testing.T) {
	// 两个不同顶层目录各带 SKILL.md：多 skill zip 明确延后，整包拒绝。
	data := zipFromFiles(t, map[string]string{
		"skill-a/SKILL.md": proceduralSample,
		"skill-b/SKILL.md": proceduralSample,
		"skill-a/ref.md":   "a",
		"skill-b/ref.md":   "b",
	})
	if _, err := ParseProceduralZip(data); err == nil {
		t.Fatal("多个顶层技能目录应整包拒绝")
	}
}

func TestParseProceduralRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"空内容", ""},
		{"非 frontmatter 开头", "name: x\n"},
		{"frontmatter 未闭合", "---\nname: x\n"},
		{"frontmatter 内空行", "---\nname: x\n\n---\nbody\n"},
		{"缺 description", "---\nname: x\n---\nbody\n"},
		{"重复名称", proceduralSample + "\n---\n" + proceduralSample},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseProceduralDocs([]byte(tc.raw)); err == nil {
				t.Fatal("应报错")
			}
		})
	}
}

func TestParseProceduralZipHappyPath(t *testing.T) {
	data := zipFromFiles(t, map[string]string{
		"SKILL.md":             proceduralSample,
		"README.md":            "package readme",
		"examples/sample.yaml": "apiVersion: v1\nkind: ConfigMap\n",
	})
	got, err := ParseProceduralZip(data)
	if err != nil {
		t.Fatalf("ParseProceduralZip: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("返回 %d 个 skill，want 1", len(got))
	}
	s := got[0]
	if s.Name != "mysql-troubleshoot" {
		t.Fatalf("name = %q", s.Name)
	}
	if len(s.Assets) != 2 {
		t.Fatalf("assets = %d，want 2: %+v", len(s.Assets), s.Assets)
	}
	found := map[string]bool{}
	for _, a := range s.Assets {
		if a.Inert {
			t.Errorf("非 scripts 资产不应 inert: %+v", a)
		}
		found[a.Name] = true
	}
	if !found["README.md"] || !found["sample.yaml"] {
		t.Fatalf("资产清单不符合: %+v", s.Assets)
	}
}

func TestParseProceduralZipRejectsTraversal(t *testing.T) {
	for name, entry := range map[string]string{
		"../escape.md":     "outside",
		"/abs/skill.md":    "absolute",
		"C:/evil/skill.md": "windows drive",
	} {
		t.Run(name, func(t *testing.T) {
			data := zipFromFiles(t, map[string]string{
				"SKILL.md": proceduralSample,
				name:       entry,
			})
			if _, err := ParseProceduralZip(data); err == nil {
				t.Fatal("路径越界应整包拒绝")
			}
		})
	}
}

func TestParseProceduralZipRejectsSymlink(t *testing.T) {
	data := zipWithSymlink(t, "config/secret.txt", "/etc/passwd")
	if _, err := ParseProceduralZip(data); err == nil {
		t.Fatal("符号链接应整包拒绝")
	}
}

func TestParseProceduralZipRejectsNoSkill(t *testing.T) {
	data := zipFromFiles(t, map[string]string{
		"README.md": "no skill here",
	})
	if _, err := ParseProceduralZip(data); err == nil {
		t.Fatal("无 SKILL.md 应报错")
	}
}

func TestParseProceduralZipRejectsAmbiguousSkill(t *testing.T) {
	// 根目录之外的 SKILL.md（不在 scripts/ 下）会造成主文歧义，整包拒绝。
	data := zipFromFiles(t, map[string]string{
		"SKILL.md":      proceduralSample,
		"docs/SKILL.md": "another skill body",
	})
	if _, err := ParseProceduralZip(data); err == nil {
		t.Fatal("多 SKILL.md 应整包拒绝")
	}
}

func TestParseProceduralZipRejectsUnsupportedAsset(t *testing.T) {
	data := zipFromFiles(t, map[string]string{
		"SKILL.md": proceduralSample,
		"evil.py":  "print('x')",
	})
	if _, err := ParseProceduralZip(data); err == nil {
		t.Fatal("非白名单资产应整包拒绝")
	}
}

func TestParseProceduralZipScriptsInert(t *testing.T) {
	data := zipFromFiles(t, map[string]string{
		"SKILL.md":         proceduralSample,
		"scripts/SKILL.md": "脚本目录下的说明，不作为主文",
		"scripts/check.sh": "#!/bin/sh\nkubectl get pods\n",
	})
	got, err := ParseProceduralZip(data)
	if err != nil {
		t.Fatalf("scripts/ 资产应作为惰性资产放行: %v", err)
	}
	s := got[0]
	if s.Name != "mysql-troubleshoot" {
		t.Fatalf("主文应来自根目录 SKILL.md，got %q", s.Name)
	}
	if s.Body == "脚本目录下的说明，不作为主文" {
		t.Fatal("scripts/SKILL.md 不应成为主文正文")
	}
	if len(s.Assets) != 2 {
		t.Fatalf("assets = %d，want 2: %+v", len(s.Assets), s.Assets)
	}
	for _, a := range s.Assets {
		if !a.Inert || a.Kind != "script" {
			t.Errorf("scripts/ 资产应 inert, got %+v", a)
		}
		if a.Size <= 0 || a.SHA256 == "" {
			t.Errorf("资产元数据缺失 size/sha256: %+v", a)
		}
	}
}

func TestParseProceduralZipRejectsHugeBody(t *testing.T) {
	body := strings.Repeat("x", proceduralMaxBodyBytes+1)
	doc := "---\nname: huge\ndescription: oversized\n---\n" + body
	data := zipFromFiles(t, map[string]string{"SKILL.md": doc})
	if _, err := ParseProceduralZip(data); err == nil {
		t.Fatal("SKILL.md 正文超限应拒绝")
	}
}
