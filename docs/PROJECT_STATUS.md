# k8s-ai 项目状态

> 维护规则：每完成一个阶段（一期 P0–P12 / 二期 P0–P6），刷新下方"已完成模块"与"进行中"，并同步刷新 TECHNICAL_GUIDE.md 的第二章与第五章。

## 已完成模块

- [x] P0 工程与配置地基（go.mod / model / config / 只读客户端 / CLI 骨架）
- [x] P1 采集 Phase1（17 类资源 list + events 索引 + worker pool + 系统组件探测）
- [x] P2 采集 Phase2 + 采集边界脱敏（日志/历史日志上限、超时、并发、security 包）
- [x] P3 关联索引（Pod→Owner→Node / Pod→PVC→PV→SC / Service→EndpointSlice→Pod + 事件索引）
- [x] P4 规则引擎（23 条规则）+ Phase2 接入主流程 + 证据链（指纹/严重级/Correlated）
- [x] P5 健康评分 + 报告层（Markdown/JSON/YAML 渲染、latest/daily/none 模式、latest.json 记忆契约）
- [x] P6 LLM 客户端（OpenAI Compatible、重试/429/超时、prompt go:embed、api_key 脱敏）
- [x] P7 诊断编排（预算裁剪、JSON Schema/Evidence ID/命令校验、修复重试、失败降级）
- [x] P8 报告可读性优化（终端一屏摘要、日志压缩 -91%、严重级图标/健康条/组件状态、--verbose）
- [x] P9.1 提前实现：`scan pod <name> -n <ns>` 单 Pod 巡检报告（Scope 标注、证据链、日志）；命名空间/单 Pod 报告范围修复
- [x] P10 前体检与 P0 修复：JSON 契约字段小写化、证据链 E-ID 一致性（enrich 先于 result 拷贝）、Duration 含 LLM 耗时、Makefile lint 修正
- [x] P10 部署交付：Dockerfile（distroless 非 root）、deploy/ RBAC+ConfigMap+Secret 示例+PVC+CronJob、RBAC 清单测试、README、Makefile docker 目标
- [x] P11 加固收尾：大集群压测（PERFORMANCE.md）、四类架构测试确认（含 Secrets 禁访）、安全核对清单、-race 环境说明、INSTALLATION.md 安装部署手册
- [x] P12 一期 1.2：Server 最小化（healthz/version/异步扫描 API）+ 历史差异数据（新增/持续/恢复，二期 Agent 记忆契约）+ deployment/service 清单
- [x] 二期 P0–P2：Agent 基础（llm native/shim/auto Tool Calling、agent/tools/session 骨架、chat CLI 交互/单发/stdin/JSON/会话管理）
- [x] 二期 P3：诊断 Tool 化（scan_cluster / scan_pod / diagnose_*，复用一期 ScanService）
- [x] 二期 P4：写操作与审批执行（kubectl argv + policy-gated shell + client-go 写、SAFE..CRITICAL 风险分级、y/a/n 确认、--skip-permissions、执行后验证）
- [x] 二期 P5：Web UI（k8s-ai web：REST + SSE 流式、会话侧栏、Markdown 渲染、审批卡片、工具过程展示）
- [x] 二期 P6：收尾（审计落盘脱敏、MCP 接口预留、RBAC 写权限示例、CREDITS/E2E_SMOKE/文档、版本 v2.0.0）
- [x] 二期迭代 v2.0.1–v2.0.8：
  - v2.0.1：终端 Markdown/readline、流式命令超时采样、dry-run/rollout 风险分级修正
  - v2.0.2–v2.0.5：web 集群配置讨论、chat 横幅优化、会话重命名、Linux 静态编译
  - v2.0.6：四层意图决策树 + 砍 diagnose_* 双重 LLM + Secret 拒读 + kubectl 输出脱敏 + confidence/risk 归一化 + 新增 4 条规则至 23 条
  - v2.0.7：修复流程优化（多选方案必须先澄清 + 拒绝 2 次硬拦截 + 替代方案引导 + 四步标准化）
  - v2.0.8：放开 shell 构造 + 批量审批（借鉴 kubectl-ai）+ 拒绝闸门命令归一化 + prompt 决策树扩展 + 架构测试反转

## 进行中

- 无：一期（v1.0.x）与二期（v2.0.x）均已交付，当前版本 v2.0.8

## 下一步

- 迭代按 CHANGELOG / VERSIONING 推进；如需发布二进制或新版本，按 docs/VERSIONING.md 发布流程执行

## 已知事项

- go.mod 模块路径暂为 `github.com/k8s-ai/k8s-ai`（仓库地址确定后可改）
- `go test -race` 需要 CGO（本机暂无 gcc）：Makefile 已留 test-race 目标，Linux/CI 安装 gcc 后可用；一期 1.1 其余出口标准全部满足
- Phase2 深度采集已随 P4 接入主流程（规则命中自动取日志并挂证据）
- LLM 已与真实网关（Qwen3.5-397B 思考型模型）联调通过：单 Finding 约 1.5-2 分钟，建议 llm.timeout ≥ 120s、scan.timeout 10m；扫描过程有进度日志，LLM 超时自动降级不失败
- 诊断增强：关联异常 Pod 证据补全、LLM 降级时规则初步判断 + 日志关键行提取、日志证据保留尾部；实测可自动定位 RocketMQ topic 路由不存在导致的 panic 根因
- 一期 1.1 + 1.2 全部交付；LLM / 规则引擎 / 报告基础均已完成（P4/P5/P6-P8）
