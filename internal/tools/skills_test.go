package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/k8s-ai/k8s-ai/internal/model"
	"github.com/k8s-ai/k8s-ai/internal/skills"
)

// newTestProceduralReg 构造一个同时含 procedural 与 structured Skill 的 registry。
func newTestProceduralReg(t *testing.T) skills.Registry {
	t.Helper()
	r := skills.New()
	procedural := skills.Skill{
		Type:        skills.TypeProcedural,
		Source:      skills.SourceCustom,
		Name:        "k8s-mysql-troubleshoot",
		Domain:      "database",
		Version:     "1.0.0",
		Description: "MySQL 在 Kubernetes 上的故障排查方法论",
		Body:        "# 步骤\n1. kubectl get pod 取证\n",
		Assets: []skills.SkillAsset{
			{Name: "scripts/check.sh", Path: "scripts/check.sh", Kind: "script", Inert: true},
		},
	}
	if err := r.Register(procedural); err != nil {
		t.Fatalf("register procedural: %v", err)
	}
	structured := skills.Skill{
		Type:        skills.TypeStructured,
		Source:      skills.SourceBuiltin,
		Name:        "create-workload",
		Domain:      "workload",
		Version:     "1.0.0",
		Kinds:       []string{"v1/Pod"},
		Description: "工作负载创建校验",
		Risk:        skills.RiskRule{Default: skills.RiskMedium},
	}
	if err := r.Register(structured); err != nil {
		t.Fatalf("register structured: %v", err)
	}
	return r
}

func TestSkillsToolsRegisterAndReadOnly(t *testing.T) {
	regs := NewSkillsTools(newTestProceduralReg(t))
	if len(regs) != 2 {
		t.Fatalf("应装配 2 个 skill 工具，实际 %d", len(regs))
	}
	for _, tl := range regs {
		meta, ok := tl.(MetadataDescriber)
		if !ok || strings.TrimSpace(meta.SchemaMetadata()) == "" {
			t.Fatalf("工具 %s 缺 SchemaMetadata", tl.Name())
		}
		if !tl.IsReadOnly(nil) {
			t.Fatalf("工具 %s 应只读", tl.Name())
		}
		if tl.Risk(nil) != model.RiskSafe {
			t.Fatalf("工具 %s 风险应为 SAFE", tl.Name())
		}
	}
	if got := NewSkillsTools(nil); got != nil {
		t.Fatalf("nil registry 应返回 nil，实际 %v", got)
	}
}

func TestListSkillsTool(t *testing.T) {
	regs := NewSkillsTools(newTestProceduralReg(t))
	var list *listSkillsTool
	for _, tl := range regs {
		if tl.Name() == "list_skills" {
			list = tl.(*listSkillsTool)
		}
	}
	if list == nil {
		t.Fatal("list_skills 未装配")
	}
	res, err := list.Execute(context.Background(), json.RawMessage("{}"))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 类型 = %T", res.Data)
	}
	items, ok := data["items"].([]map[string]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %#v", data["items"])
	}
	if items[0]["name"] != "create-workload" || items[1]["name"] != "k8s-mysql-troubleshoot" {
		t.Fatalf("应按名称排序：%+v", items)
	}
}

func TestGetSkillTool(t *testing.T) {
	regs := NewSkillsTools(newTestProceduralReg(t))
	var get *getSkillTool
	for _, tl := range regs {
		if tl.Name() == "get_skill" {
			get = tl.(*getSkillTool)
		}
	}
	if get == nil {
		t.Fatal("get_skill 未装配")
	}

	// 命中并返回正文与不可信资产元数据。
	res, err := get.Execute(context.Background(), json.RawMessage(`{"name":"k8s-mysql-troubleshoot"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	data, ok := res.Data.(map[string]any)
	if !ok {
		t.Fatalf("Data 类型 = %T", res.Data)
	}
	skill, ok := data["skill"].(map[string]any)
	if !ok {
		t.Fatalf("skill 字段缺失")
	}
	if !strings.Contains(skill["body"].(string), "kubectl get pod") {
		t.Fatalf("应返回正文，实际 %v", skill["body"])
	}
	if skill["truncated"] != false {
		t.Fatalf("短文不应截断，实际 %v", skill["truncated"])
	}
	assets, ok := skill["assets"].([]map[string]any)
	if !ok || len(assets) != 1 {
		t.Fatalf("assets = %#v", skill["assets"])
	}
	if assets[0]["inert"] != true || assets[0]["kind"] != "script" {
		t.Fatalf("脚本资产必须标记 inert：%+v", assets[0])
	}

	// 未知名报错（回传 LLM 继续调查，不中断）。
	res, err = get.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`))
	if err != nil {
		t.Fatalf("unknown execute: %v", err)
	}
	if !strings.Contains(res.Text, "不存在或未启用") {
		t.Fatalf("未知名称应报错：%s", res.Text)
	}
}
