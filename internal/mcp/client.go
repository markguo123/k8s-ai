// Package mcp 是二期 MCP（Model Context Protocol）预留：
// 定义 Client 接口与工具注册表适配器骨架，v1 不连接具体服务器（需求规格 §20）。
package mcp

import (
	"context"
)

// Tool 是 MCP 服务器暴露的工具描述。
type Tool struct {
	Name        string
	Description string
	InputSchema any
}

// Client 是 MCP 客户端接口（stdio/http 实现后续迭代接入）。
type Client interface {
	// Name 返回服务器名（用于工具唯一名前缀 <server>.<tool>）。
	Name() string
	Connect(ctx context.Context) error
	Close() error
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, arguments map[string]any) (string, error)
}

// Config 是 MCP 客户端配置（与 config.MCPConfig 对应；v1 只解析不连接）。
type Config struct {
	Enabled bool
	Servers []ServerConfig
}

// ServerConfig 描述一个 MCP 服务器（stdio 或 http）。
type ServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
}
