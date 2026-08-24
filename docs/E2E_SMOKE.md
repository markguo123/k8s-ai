# k8s-ai 二期 E2E 冒烟清单

> 目的：每次改动后快速回归，不用再手动逐条问。前置：`~/.kube/config` 可达、LLM 网关可用、已启动 `k8s-ai web`。

| # | 问题 | 期望结果 |
|---|---|---|
| 1 | `介绍下自己` | 自称 k8s-ai，开发者：中电金信郭康虎；不出现其他厂商 |
| 2 | `查看下集群有多少 namespace` | 调用 get_resources 并给出数字 |
| 3 | `检查下集群健康状态` | 多维度报告（节点/资源/命名空间/Pod/组件/事件）+ 规则评分与 LLM 综合评分双轨 |
| 4 | `看下节点资源使用情况` | get_node_usage 输出各节点 CPU/内存使用率表格 |
| 5 | `为什么 <故障Pod> 一直重启` | 诊断收敛（工具调用 ≤6 次）、根因明确、只给结论+方案并询问"需要我修复吗？"，**不直接动手** |
| 6 | 注入故障后回答`修复吧` | 每个写工具弹审批卡片（显示 kubectl 命令 + 变更说明）→ 执行 → 验证 Ready |
| 7 | 审批卡片 | 显示人类可读命令与变更（无转义 JSON） |
| 8 | 工具卡片 | 执行中蓝点 → 已完成绿点/出错红点；Show output 可展开看结果；有耗时与复制按钮 |
| 9 | 会话恢复 | 刷新页面后历史消息与工具卡片保留，可继续对话 |
| 10 | 审计 | `~/.k8s-ai/audit/<session>.jsonl` 有用户输入/LLM/工具/审批事件，且无敏感值（api_key/password） |

## 回归命令

```bash
go test ./...
go vet ./...
```

## 常见故障注入样例（yanshou-nginx）

```bash
kubectl -n yanshou-nginx patch configmap nginx-config --type merge \
  --patch '{"data":{"default.conf":"server {\n    listen 80x;\n    server_name localhost;\n}"}}'
kubectl -n yanshou-nginx rollout restart deployment nginx-deployment
```
