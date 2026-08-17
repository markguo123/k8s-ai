# k8s-ai 项目状态

> 维护规则：每完成一个阶段（P3/P4/P5/一期终版），刷新下方"已完成模块"与"进行中"，并同步刷新 TECHNICAL_GUIDE.md 的第二章与第五章。

## 已完成模块

- [x] P0 工程与配置地基（go.mod / model / config / 只读客户端 / CLI 骨架）
- [x] P1 采集 Phase1（17 类资源 list + events 索引 + worker pool + 系统组件探测）
- [x] P2 采集 Phase2 + 采集边界脱敏（日志/历史日志上限、超时、并发、security 包）
- [x] P3 关联索引（Pod→Owner→Node / Pod→PVC→PV→SC / Service→EndpointSlice→Pod + 事件索引）
- [x] P4 规则引擎（13 条规则）+ Phase2 接入主流程 + 证据链（指纹/严重级/Correlated）
- [x] P5 健康评分 + 报告层（Markdown/JSON/YAML 渲染、latest/daily/none 模式、latest.json 记忆契约）
- [x] P6 LLM 客户端（OpenAI Compatible、重试/429/超时、prompt go:embed、api_key 脱敏）
- [x] P7 诊断编排（预算裁剪、JSON Schema/Evidence ID/命令校验、修复重试、失败降级）
- [x] P8 报告可读性优化（终端一屏摘要、日志压缩 -91%、严重级图标/健康条/组件状态、--verbose）
- [x] P9.1 提前实现：`scan pod <name> -n <ns>` 单 Pod 巡检报告（Scope 标注、证据链、日志）；命名空间/单 Pod 报告范围修复
- [x] P10 前体检与 P0 修复：JSON 契约字段小写化、证据链 E-ID 一致性（enrich 先于 result 拷贝）、Duration 含 LLM 耗时、Makefile lint 修正
- [x] P10 部署交付：Dockerfile（distroless 非 root）、deploy/ RBAC+ConfigMap+Secret 示例+PVC+CronJob、RBAC 清单测试、README、Makefile docker 目标
- [x] P11 加固收尾：大集群压测（PERFORMANCE.md）、四类架构测试确认（含 Secrets 禁访）、安全核对清单、-race 环境说明、INSTALLATION.md 安装部署手册
- [x] P12 一期 1.2：Server 最小化（healthz/version/异步扫描 API）+ 历史差异数据（新增/持续/恢复，二期 Agent 记忆契约）+ deployment/service 清单

## 进行中

- [ ] 二期（chat Agent / Tool Calling / 会话记忆 / 审批执行）：按 PROJECT_SPEC_二期.md 启动

## 下一步

P12（1.2）已完成 → 二期（chat Agent）

## 已知事项

- go.mod 模块路径暂为 `github.com/k8s-ai/k8s-ai`（仓库地址确定后可改）
- `go test -race` 需要 CGO（本机暂无 gcc）：Makefile 已留 test-race 目标，Linux/CI 安装 gcc 后可用；一期 1.1 其余出口标准全部满足
- Phase2 深度采集已随 P4 接入主流程（规则命中自动取日志并挂证据）
- LLM 已与真实网关（Qwen3.5-397B 思考型模型）联调通过：单 Finding 约 1.5-2 分钟，建议 llm.timeout ≥ 120s、scan.timeout 10m；扫描过程有进度日志，LLM 超时自动降级不失败
- 诊断增强：关联异常 Pod 证据补全、LLM 降级时规则初步判断 + 日志关键行提取、日志证据保留尾部；实测可自动定位 RocketMQ topic 路由不存在导致的 panic 根因
- 一期 1.1 + 1.2 全部交付；LLM / 规则引擎 / 报告基础均已完成（P4/P5/P6-P8）