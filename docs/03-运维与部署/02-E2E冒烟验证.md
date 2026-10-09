# k8s-ai 二期 + 三期 E2E 冒烟清单

> 目的：每次改动后快速回归，不用再手动逐条问。前置：`~/.kube/config` 可达、LLM 网关可用、已启动 `k8s-ai web`。
> 三期创建能力：`create_resource` 采用 `dry_run` → 提案确认 → `apply` → `verify` 两阶段协议；Secret 值只能通过 `$GENERATE`（执行器生成）或 `$ASK`（审批时用户输入）进入集群，明文值永不进入 LLM/会话/审计。

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
| 11 | `帮我在 default 命名空间创建 ConfigMap web-config，data.mode=prod` | 先输出 `create_resource` dry_run 提案（含 manifest 与风险），人工确认后 apply，返回 applied + verify；对象可 `kubectl -n default get cm web-config` 验证 |
| 12 | `帮我在 k8s-ai-test 命名空间用镜像 rvm:5100/dbaas-system/mysql:8.0.20 创建 MySQL（数据库 mysql，根密码走管理员密码）` | Agent 先收集缺失信息，再 dry_run 提案；Secret 密码用 `$ASK` 由审批环节输入，集群侧拿到用户值，工具结果/会话/审计无明文 |
| 13 | 审批卡 | 创建提案卡展示对象清单、manifest、风险等级、Secret 脱敏清单；Secret 值输入不回显 |
| 14 | Secret 审计 | 审计含 `secret.value_requested`（键名/来源）与 `secret.touched`（键名/长度/SHA-256/来源），不含明文值 |
| 15 | 失败场景：直接要求 apply、跳过 dry_run | Agent 仍先走 dry_run；任何绕过预演的 apply 被 `CODE_GUARD` 拒绝，返回 `dry_run_required` 或 `namespace_missing` 等明确原因 |
| 16 | 失败场景：创建 Secret 时模型自填明文 | `create_resource` 拒绝 `data` 显式值或非占位符的 `stringData`，提示改用 `$GENERATE` / `$ASK` |

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
