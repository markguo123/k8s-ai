# Changelog

本项目遵循 docs/VERSIONING.md 的里程碑阶梯约定。

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