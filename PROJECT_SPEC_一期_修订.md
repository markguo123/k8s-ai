# k8s-ai 一期需求文档（修订版）

- 版本：v1.0
- 日期：2026-08-12
- 状态：技术评审通过，待开发
- 来源：基于 PROJECT_SPEC_一期.md 技术评审结论修订，与 AGENTS.md 约束一致

## 修订记录

| 版本 | 日期 | 修订人 | 说明 |
|---|---|---|---|
| v1.0 | 2026-08-12 | Codex（评审后修订） | 初版：吸收技术评审结论，修复 12 项需求冲突，一期拆分为 1.1/1.2 |

---

## 1. 文档范围

- 一期拆为 **1.1（MVP，必须交付）** 和 **1.2（可延）**。
- 本需求文档已吸收评审结论：删除应用内调度配置、两阶段扫描、Finding 指纹、LLM 降级与上下文预算、脱敏时点前移、RBAC 修正、报告持久化、退出码约定等。
- 所有需求以编号 FR-xxx 表示，验收标准可测，每项都对应开发计划中的任务。

## 2. 产品定位与安全原则

- 一次 `k8s-ai scan` 得到：集群哪里有问题、为什么、影响什么、怎么排查、怎么修。
- 一期绝对只读，LLM 只分析、只生成命令文本，程序永不执行。
- 四层只读保证：RBAC 只读动词 → 代码层只读门面 → 无 shell/exec 调用 → 只读行为测试。

## 3. 功能需求

### A. CLI 与配置

**FR-001 CLI 骨架**

`k8s-ai scan`、`scan pod <name> -n <ns>`、`config init`、`config validate`、`version`。

验收：
- `scan` 扫描全集群（`kubernetes.namespace` 为空时）或指定 namespace（非空时，系统组件探测除外）。
- 通用 flag：`--kubeconfig`、`--context`、`--namespace`（支持 `-n` 简写）、`--format markdown|json|yaml`、`--report-mode none|latest|daily`、`--max-log-lines`、`--since`、`--fail-on`。
- 报告目的地由场景默认：**全集群手动 scan 默认写 latest 报告；目标扫描（--namespace / scan pod）默认只打印终端不写文件；定时任务用 `--report-mode daily`**；均可显式覆盖。
- 单 Pod 目标：`k8s-ai scan pod <name> -n <namespace>`（P9.1 提前实现），输出 `Kubernetes Pod 巡检报告：ns/name`（Scope 标注），仅含该 Pod 及其直接关联资源（Workload/Node/PVC/Service）的 Finding，命中需要日志的规则时自动取日志/历史日志。
- CLI 只做解析与装配，业务逻辑全部在 service 层。

**FR-002 配置系统**

优先级：CLI > ENV > YAML > 默认；默认配置 `~/.k8s-ai/config.yaml`。

验收：
- 环境变量：`KUBECONFIG`、`K8S_AI_LLM_ENDPOINT`、`K8S_AI_LLM_API_KEY`、`K8S_AI_LLM_MODEL` 等 `K8S_AI_*` 均生效。
- 配置文件中的 `~` 路径自动展开。
- `config init` 生成完整模板；`config validate` 校验配置合法性并测试集群连通性。
- **删除原规范 `schedule:` 配置项**；调度由 Kubernetes CronJob 承担。

**FR-003 只读 Kubernetes Client**

验收：
- 自动判断 kubeconfig / InClusterConfig，支持 `--kubeconfig`、`--context`、`--namespace` 覆盖。
- rest.Config 设置 QPS（默认 20）/Burst（默认 40）、请求超时（默认 10–30s），均可配置。
- 只暴露 `Reader` 只读接口（get/list/get logs），代码中不存在任何 create/update/patch/delete/exec 调用。
- 启动时通过 `ServerVersion()` 校验连通性，失败立即报错退出。
- 一期只使用 list，不使用 watch/informer。

### B. 采集与关联

**FR-004 两阶段扫描**

- Phase1：轻量 list 全量资源（Pods/Nodes/Services/EndpointSlices/Workloads/Storage/Network/Namespaces/每 namespace 一次 Events），产出 ClusterSnapshot。
- Phase2：仅对异常对象深度采集（日志、previous logs、关联资源详情）。

验收：
- Phase1 请求量与 Pod 数量无关（无 N+1）；Events 每 namespace 只拉一次，本地按 involvedObject 建索引。
- 单资源失败（如日志权限拒绝）只记入 `collection_errors`，不中断整体扫描。
- `scan.concurrency` 控制 worker pool；每个请求都有独立超时。

**FR-005 Pod 分析**

验收：
- 检查 Pending/Running/Succeeded/Failed/Unknown，覆盖 CrashLoopBackOff、ImagePullBackOff、ErrImagePull、OOMKilled、CreateContainerConfigError、Evicted 等规范所列原因。
- 深度字段齐全：container status、restartCount、current/last state、exitCode、reason、message、image、resources、node、QoS、ownerReferences、labels、annotations。

**FR-006 Node 分析**

验收：
- 检查 Ready/MemoryPressure/DiskPressure/PIDPressure/NetworkUnavailable/Unschedulable。
- 收集 capacity/allocatable、Taints、Labels。
- Metrics Server 读取**推迟到 1.2**；1.1 不依赖、不请求 metrics API。

**FR-007 Workload 分析**

验收：Deployment/ReplicaSet/StatefulSet/DaemonSet/Job/CronJob 的 desired/ready/available/updated 副本与 conditions 检查；Deployment unavailable、Job Failed 等场景可发现。

**FR-008 Storage 分析**

验收：PVC/PV/StorageClass/VolumeAttachment 检查；建立 Pod→PVC→PV→SC 关联链；发现 Pending PVC、Lost PV、FailedMount/FailedAttachVolume、容量与 CSI 异常。

**FR-009 Network 分析**

验收：Service/EndpointSlice/Ingress/NetworkPolicy；发现 Service 无 Endpoint、selector 不匹配、Pod Ready 但无 Endpoint、Ingress backend 异常。

**FR-010 系统组件动态发现**

验收：不假设 CoreDNS/kube-proxy/CNI/CSI/metrics-server/Ingress Controller 存在；用 Discovery + Labels + 资源元数据判断；缺失组件按"未部署"记录而非报错。

**FR-011 异常分级**

验收：
- 严重级仅由 Rule Engine 计算：INFO/LOW/MEDIUM/HIGH/CRITICAL。
- 分级必须考虑 namespace、资源类型、副本数、受影响 workload/service、生产标签、系统命名空间。
- LLM 输出的严重等级仅作展示参考，禁止回写 Finding。

**FR-012 关联索引**

验收：Phase2 前建立 Pod→owner→Node、Pod→PVC→PV→SC、Service→EndpointSlice→Pod 三类索引；用于证据补全、分级和评分去重。

### C. 规则引擎与证据

**FR-013 Rule Engine**

验收：
- 接口为 `Name() string` + `Evaluate(ctx) []*Finding`。
- 实现规范所列 13+ 规则（CrashLoopBackOff、OOMKilled、ImagePullBackOff、PendingPod、NodeNotReady、NodeDiskPressure、NodeMemoryPressure、PVCPending、FailedMount、ServiceNoEndpoint、DeploymentReplica、StatefulSetReplica、JobFailed）。
- 每条 Finding 有稳定指纹：`hash(kind+namespace+name+rule+关键证据)`。
- 规则注册表支持按需启停（`rules.enabled/disabled` 配置）。

**FR-014 Evidence**

验收：
- 每条 Finding 至少一条真实证据，格式：`E<n>` + Kind + Source（可追溯到对象和字段）+ Key + Value + Truncated。
- 证据排序：状态字段 > Events > 日志尾部 > 注解。
- 脱敏在采集边界执行（见 FR-018），截断必须标记 `Truncated=true`。
- 相关异常标记 `Correlated=true`，用于评分去重。

### D. LLM 与诊断

**FR-015 LLM 客户端**

验收：
- `LLMClient.Chat(ctx, ChatRequest) (*ChatResponse, error)` 接口，业务层不依赖任何 OpenAI SDK。
- 兼容 OpenAI/Qwen/DeepSeek/vLLM/Ollama 的 Chat Completions 协议。
- 单次调用超时（默认 120s）；429 按 Retry-After 退避重试；5xx 有限重试（最多 2 次）；4xx（除 429）不重试。
- api_key 不出现在日志、错误信息、报告中。

**FR-016 诊断编排**

验收：
- 流程：Findings → Evidence Ranking → 预算裁剪 → DiagnosisContext → LLM → 解析 → 校验 → Diagnosis。
- 上下文预算：单 Finding ≤ 6k tokens；单次诊断总预算可配置（`llm.max_input_tokens`）；最多送诊 top-N（默认 30）个 Finding，其余只保留规则结论。
- LLM 输出为结构化 JSON（摘要、根因、置信度、证据链引用、影响、可能原因、排查/修复/验证命令、风险），解析失败重试一次，仍失败则降级。
- 程序化校验：命令必须含 namespace/资源类型/名称，动词白名单；引用的 Evidence ID 必须真实存在；非法输出整条丢弃并标注。
- **降级策略**：LLM 不可用/超时/限流/校验失败时，报告仍完整输出规则引擎结论，对应 Diagnosis 标注 `LLM分析不可用`，scan 不失败。

**FR-017 kubectl 命令**

验收：LLM 生成的命令只显示不执行；修复命令必须标注风险等级；`CommandExecutor` 接口只声明不实现。

### E. 安全与脱敏

**FR-018 脱敏**

验收：
- 程序永不读取 Secret 对象（RBAC 不含 secrets，代码无对应调用）。
- 采集边界脱敏：token/password/api key/authorization/cookie/private key 等模式在进入 Evidence 前替换为 `[REDACTED]`。
- 关键词脱敏：ConfigMap 数据默认不采集；仅当明确与 Finding 相关时按敏感键（password/secret/token/key/credential）脱敏后使用。
- 报告渲染时二次脱敏；`api_key` 永不落日志。
- 测试：mock LLM 捕获请求体，断言敏感值不出现。

### F. 报告与评分

**FR-019 报告渲染**

验收：
- Markdown 按规范 §28 结构输出（概览、健康评分、异常摘要、分级问题详情、资源汇总、系统组件、Storage、Network、建议、collection_errors）。
- `--format json|yaml` 输出同一 ScanResult 的机器可读版本；JSON 同时作为 1.2 趋势对比数据源。
- 报告命名：`latest.md` + `latest.json`（Agent 记忆契约）；daily 模式追加时间戳文件；`--report-mode none` 时只渲染到终端不落盘。
- `--format` 决定终端完整报告的渲染格式（markdown/json/yaml），文件与终端共用渲染器。
- **报告可读性（P8）**：终端默认输出一屏摘要（健康条、严重级计数、Top 10 重点问题含现象/根因/建议、组件状态、报告路径），`--verbose` 输出完整报告；日志证据只保留前 8 行并统计行数/ERROR/WARN，防止倾倒；严重级用图标 🔴🟠🟡🔵⚪，健康分用文本进度条，组件状态用 ✅⚠️❌。

**FR-020 健康评分**

验收：
- 程序计算：`score = max(0, 100 − Σ罚分)`；基准罚分 CRITICAL 30 / HIGH 15 / MEDIUM 5 / LOW 1（权重集中配置）。
- Correlated Finding 不参与扣分；报告必须列出每条扣分原因。

**FR-021 日报与趋势（1.2）**

验收：读取上一份 JSON，按 Finding 指纹对比新增/持续/恢复；1.1 只预留指纹与 JSON 输出，不实现对比逻辑。

### G. 可靠性

**FR-022 日志采集上限**

验收：
- 默认 current + previous logs，`--max-log-lines` 默认 500。
- 硬上限：单行 ≤ 1 KiB、单容器 ≤ 64 KiB、单 Pod 日志获取超时（默认 30s）。
- previous logs 不存在时静默跳过；`--since` 与行数限制同时使用时以字节上限兜底。

**FR-023 Events 采集**

验收：仅核心组 `v1.Event`；每 namespace 一次 list；按 involvedObject UID 本地索引；输出 reason/message/type/count/firstTimestamp/lastTimestamp。

**FR-024 并发与限流**

验收：worker pool 并发可配置（默认 8）；Phase2 深度采集并发默认 4；客户端 QPS/Burst 生效；总扫描时长受 `scan.timeout` 约束。

### H. 部署交付

**FR-025 容器与部署**

验收：
- 多阶段 Dockerfile：非 root、小镜像、prompts 通过 go:embed 内置。
- deploy/ 含 namespace、serviceaccount、clusterrole（仅 get/list/watch + pods/log get）、rolebinding、configmap、cronjob；deployment/service 属 1.2（Server 落地时交付）。
- CronJob：`restartPolicy: OnFailure`、`concurrencyPolicy: Forbid`、时区明确、**reports 目录挂载 PVC**。

**FR-026 退出码与 Makefile**

验收：0=成功；1=执行错误；2=`--fail-on` 达到阈值；Makefile 提供 build/test/lint/docker/clean。

### I. 工程约束

**FR-027 测试**

验收：单测 + 只读/RBAC/泄密/请求量四类架构测试 + 可选 envtest 集成测试，详见开发计划。

## 4. 一期明确不做

自然语言 Chat、多轮会话、自动执行 kubectl、自动修改 Kubernetes、AI Agent、Tool Calling、MCP、自动修复/rollout/delete/scale、应用内 schedule 配置、Metrics Server 读取（1.2）、Server 异步任务（1.2）、趋势对比（1.2）。

## 5. 交付物清单

完整 Go 源码、go.mod/go.sum、Makefile、Dockerfile、configs/ 配置模板、prompts/（embed）、deploy/（含 RBAC 与 CronJob）、README、全部单元测试与架构测试。

## 6. 验收标准（Definition of Done）

- 每阶段 `go test ./...` 与 `go vet ./...` 全绿。
- 无 TODO、无伪代码、无假实现；每个重要模块有测试。
- 只读架构测试、RBAC 清单测试、泄密测试、请求量测试全部通过。
- 核心链路可用：`k8s-ai scan` 一次跑通 采集→规则→证据→LLM→报告。
