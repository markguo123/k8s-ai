package tools

import (
	"fmt"
	"sort"

	"github.com/k8s-ai/k8s-ai/internal/llm"
)

// Registry 是工具注册表。
type Registry struct {
	tools map[string]Tool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register 注册工具；重名报错。
func (r *Registry) Register(t Tool) error {
	name := t.Name()
	if name == "" {
		return fmt.Errorf("tool name must not be empty")
	}
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.tools[name] = t
	return nil
}

// Lookup 按名查询工具。
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Remove 删除指定名字的工具；不存在时静默忽略。
func (r *Registry) Remove(name string) {
	delete(r.tools, name)
}

// All 返回全部工具（名字排序，顺序稳定）。
func (r *Registry) All() []Tool {
	names := make([]string, 0, len(r.tools))
	for n := range r.tools {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.tools[n])
	}
	return out
}

// Names 返回全部工具名（排序）。
func (r *Registry) Names() []string {
	all := r.All()
	out := make([]string, 0, len(all))
	for _, t := range all {
		out = append(out, t.Name())
	}
	return out
}

// Schemas 渲染给 LLM 的工具定义（native 模式 tools 数组）。
func (r *Registry) Schemas() []llm.ToolSpec {
	all := r.All()
	out := make([]llm.ToolSpec, 0, len(all))
	for _, t := range all {
		out = append(out, llm.ToolSpec{
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.InputSchema(),
		})
	}
	return out
}
