# Changelog

本项目遵循 docs/VERSIONING.md 的里程碑阶梯约定。

## [v1.0.1] - 2026-08-18

一期迭代：Incident 聚合诊断落地 + 诊断体验优化（修复方案文字+命令化、日志完整保留、通用信号规则）。

### Added

- Incident 聚合诊断正式入库：Finding → Correlation → Incident → LLM，同一故障链只调用一次 LLM（之前是每个 Finding 一次）；派生症状（Deployment 副本不足/Service 无 Endpoint）折叠为影响范围，不单独分析/重复扣分；健康评分只统计根因
- `Incident` 模型与 `internal/incident` 聚合包（并查集按关联资源合并 + 根因选择）；Incident 诊断/报告渲染（Markdown 关联问题段、终端派生折叠）
- LLM 输出新增 `remediationText`（修复文字说明：做什么/为什么/预期结果，文字是主体）及 `confidenceLevel`（CONFIRMED/HIGH_CONFIDENCE/POSSIBLE/UNKNOWN）、`causalChain`、`uncertainty`、`remediationDirection` 字段，对齐 system.md 诊断协议
- 通用信号规则：`ContainerCreateError`（Secret/ConfigMap 缺失等启动失败）、`Unhealthy`（探针失败）、`IngressBackend`（backend 指向不存在 Service）

### Changed

- 修复方案"文字+命令化"（system.md §三十五 重写）：remediationText 必填文字说明，remediation 配套可执行 kubectl 命令（含 -n/资源名/完整参数）；报告与终端渲染"建议=文字说明+命令（含风险）"，禁止"只有命令没有文字"或"只有文字没有命令"
- **修复建议永不缺失**：LLM 漏写文字/空命令时，工具按根因规则兜底生成"修复方向 + 前置确认"（remediationDirection），报告/终端始终有修复指导
- 日志按行完整保留（TailLines 500、单行不截断仅 >1MiB 防 OOM、总字节按行边界）；Incident 上下文只送根因，派生症状合并到 impact
- 诊断上下文补充 Pod 引用的 ConfigMap/Secret **名称**（仅名称不读数据），配置/凭据类根因优先给出针对性命令

### Fixed

- 修复方案回归：LLM 只输出命令、丢失文字说明 → 新增 remediationText 字段并同步 latest.json 契约，LLM 漏写时工具用修复方向兜底
- 终端一屏摘要最多展示 2 条修复命令，其余提示 `--verbose` 完整输出

### Security

- 一期严格只读不变：LLM 生成的 kubectl 命令仅展示不执行；Secret 数据永不读取/发送

## [v1.0.0] - 2026-08-17

一期 1.1 + 1.2 交付（当前版本基线）。

### Added

- 只读 Kubernetes 巡检：全集群 / 命名空间 / 单 Pod 三种扫描形态
- 13 条内置规则（CrashLoopBackOff、OOMKilled、ImagePullBackOff、PendingPod、Node 压力、PVC Pending、FailedMount、Service 无 Endpoint、副本不匹配、Job Failed 等）
- 两阶段采集（Phase1 全量 list 无 N+1 / Phase2 仅异常对象取日志）+ 关联索引（owner/PVC/Service）
- Evidence 证据链：对象字段 / Events / 日志（保留尾部 + 关键行提取）/ 注解，采集边界脱敏
- LLM 诊断（OpenAI Compatible）：预算裁剪、JSON Schema / Evidence ID / kubectl 命令校验、失败降级为规则初步判断
- 健康评分 + Markdown/JSON/YAML 报告 + 终端一屏摘要（--verbose 完整输出）
- 历史对比（v1.0.0 1.2）：按 Finding 指纹输出新增/持续/恢复，写入 latest.json history 字段
- HTTP Server（v1.0.0 1.2）：healthz/readyz/version + 异步扫描 API
- 部署：Dockerfile（distroless 非 root）、deploy/ RBAC/ConfigMap/Secret 示例/PVC/CronJob/Deployment/Service
- 文档：README、INSTALLATION.md（安装部署手册）、TECHNICAL_GUIDE.md、docs/design/ 系列、docs/VERSIONING.md

### Changed

- 诊断上下文补充 Pod 引用的 ConfigMap **名称**（仅名称不读数据）与关联 Pod 的 ConfigMaps 信息；系统提示词引导"配置类根因优先给出 `kubectl edit configmap`"——修复建议不再只会给 rollout restart（实测输出 `kubectl edit configmap nginx-config -n yanshou-nginx`）

- 报告可读性优化：日志证据压缩（体积 -91%）、严重级图标、健康条、组件状态
- JSON 契约字段小写化；证据链 E-ID 与报告一致性；Duration 含 LLM 耗时（P10 前体检修复）

### Fixed

- P10 前体检 3 个 P0：ComponentInfo JSON 字段名、enrich 顺序导致 E-ID 不一致、Duration 不含 LLM
- Makefile lint 目录参数失效（改为文件列表校验）

### Security

- 四层只读保证 + AST 架构测试（含 Secrets 禁访）；-race 需 CGO（本机无 gcc，CI/Linux 可跑）