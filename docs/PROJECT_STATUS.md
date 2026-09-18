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
- [x] P11 加固收尾：大集群压测（PERFORMANCE.md）、四类架构测试确认（含 Secrets 仅元数据级）、安全核对清单、-race 环境说明、INSTALLATION.md 安装部署手册
- [x] P12 一期 1.2：Server 最小化（healthz/version/异步扫描 API）+ 历史差异数据（新增/持续/恢复，二期 Agent 记忆契约）+ deployment/service 清单
- [x] 二期 P0–P2：Agent 基础（llm native/shim/auto Tool Calling、agent/tools/session 骨架、chat CLI 交互/单发/stdin/JSON/会话管理）
- [x] 二期 P3：诊断 Tool 化（scan*cluster / scan_pod / diagnose*\*，复用一期 ScanService）
- [x] 二期 P4：写操作与审批执行（kubectl argv + policy-gated shell + client-go 写、SAFE..CRITICAL 风险分级、y/a/n 确认、--skip-permissions、执行后验证）
- [x] 二期 P5：Web UI（k8s-ai web：REST + SSE 流式、会话侧栏、Markdown 渲染、审批卡片、工具过程展示）
- [x] 二期 P6：收尾（审计落盘脱敏、MCP 接口预留、RBAC 写权限示例、CREDITS/E2E_SMOKE/文档、版本 v2.0.0）
- [x] 二期迭代 v2.0.1–v2.0.5：web 集群配置讨论、终端 Markdown/readline、chat 横幅、会话重命名、Linux 静态编译、dry-run/rollout 风险分级修正、流式命令超时采样
- [x] 二期迭代 v2.0.6–v2.0.8：四层意图决策树 + 砍 diagnose\_\* 双重 LLM + Secret 拒读 + 修复流程三拦截 + 放开 shell 构造 + 批量审批 + 拒绝闸门归一化 + 架构测试反转
- [x] 二期迭代 v2.0.9：写工具从 7→1（删除 6 个结构化写工具，写操作统一走 kubectl 唯一路径）；kubectlTool 补 Text 字段修复 LLM 展示；Web UI 交互优化（输入框 disabled+spinner、空会话欢迎页、删除确认、会话时间戳、AI 思考 typing indicator）
- [x] 二期迭代 v2.1.0：提示词移除已删除结构化写工具；kubectl 动词解析支持全局 flag 独立值；shell 模式交互命令硬拒绝；Secret/--raw 红线改用 mvdan shell 解析器加固；delete 风险分级补 Service；配置项 skip_permissions/disabled_tools 生效；kubectl 子进程注入 KUBECONFIG
- [x] 二期迭代 v2.2.1：诊断报告与流式稳定性修复（保留真实 content/reasoning 流事件、progress/answer-delta/tool-call/tool-response/answer 分阶段事件、工具前诊断摘要不过度过滤、最终报告完整性约束、证据不足时优先只读取证、Web 分析过程折叠、失败工具调用去重、流式参数归一化）
- [x] 二期迭代 v2.3.0–v2.3.2：Web UI 流程可视化与会话体验优化；OpenAI 标准思考参数重构（`llm.thinking` / `scan.thinking`）、`reasoning-delta` 事件、Web 思考展示增强、终端轻量状态提示、配置模板修复
- [x] 二期迭代 v2.4.0：Web 纯前端产品化升级（暗色主题、顶部状态条与耗时计数、横向阶段步进器、诊断报告视觉升级）；思考框全程动态展开、最终报告完成后自动折叠；工具卡取消固定 1.4s 延时 peek、拿到结果即展开并保持
- [x] 二期迭代 v2.5.0：P7 扫描能力补全与规则池扩展（A/B/C 12 条规则 + 规则元数据体系 + DocProvider/--with-doc + 报告聚合）

## 进行中

- 无：一期（v1.0.x）与二期（v2.0.x–v2.3.x）均已交付，当前版本 v2.5.0

## 下一步

- 迭代按 CHANGELOG / VERSIONING 推进；如需发布二进制或新版本，按 docs/VERSIONING.md 发布流程执行

## 已知事项

- go.mod 模块路径暂为 `github.com/k8s-ai/k8s-ai`（仓库地址确定后可改）
- `go test -race` 需要 CGO（本机暂无 gcc）：Makefile 已留 test-race 目标，Linux/CI 安装 gcc 后可用；一期 1.1 其余出口标准全部满足
- Phase2 深度采集已随 P4 接入主流程（规则命中自动取日志并挂证据）
- LLM 已与真实网关（Qwen3.5-397B 思考型模型）联调通过：单 Finding 约 1.5-2 分钟，建议 llm.timeout ≥ 120s、scan.timeout 10m；扫描过程有进度日志，LLM 超时自动降级不失败
- 诊断增强：关联异常 Pod 证据补全、LLM 降级时规则初步判断 + 日志关键行提取、日志证据保留尾部；实测可自动定位 RocketMQ topic 路由不存在导致的 panic 根因
- 一期 1.1 + 1.2 全部交付；LLM / 规则引擎 / 报告基础均已完成（P4/P5/P6-P8）
