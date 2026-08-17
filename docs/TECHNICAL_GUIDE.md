# k8s-ai 技术指导手册

> 面向读者：产品经理 / 运维负责人 / 项目决策者（不需要懂 Go）。
> 本文基于**代码实际状态（截至 P12 阶段，2026-08-17，一期 1.1 + 1.2 全部完成）**编写；每完成一个阶段（二期）都会刷新第二章与第五章。

---

## 第一章：一句话说清楚

**这个项目是什么？**

k8s-ai 是一个 Kubernetes 集群的"只读体检医生"：运维人员执行一条命令，它自动把集群里哪里不对劲、为什么不对劲、可能怎么修，整理成一份人能直接读的报告（包含可直接复制的排查/修复命令）。

- **一期目标**：一次 `k8s-ai scan`，自动发现集群异常 → 收集证据 → 规则引擎 + 私有大模型分析 → 输出 Markdown/JSON 巡检报告。
- **二期目标**：升级为自然语言对话 Agent（`k8s-ai chat`），像聊天一样查集群、做诊断，并在**人工逐条确认**后安全执行修改。
- **一期红线**：程序永远只读，绝不自动修改 Kubernetes、绝不自动执行任何命令；LLM 生成的 kubectl 命令只展示，由运维人员自己复制执行。

---

## 第二章：现阶段已完成功能（截至 P12）

> 说明：按代码实际状态编写；P3-P11（一期 1.1）与 P12（一期 1.2：Server + 历史差异数据）均已完成。

### P1 核心扫描（真实可用）

**能扫哪些资源：**

| 类别 | 覆盖内容 |
|---|---|
| 计算 | Pod、Node |
| 工作负载 | Deployment / ReplicaSet / StatefulSet / DaemonSet / Job / CronJob |
| 存储 | PVC / PV / StorageClass / VolumeAttachment |
| 网络 | Service / EndpointSlice / Ingress / NetworkPolicy |
| 基础 | Namespace、Events（每命名空间一次拉取） |
| 系统组件 | CoreDNS、kube-proxy、CNI、metrics-server、Ingress Controller、CSI（动态发现，不假设存在） |

**能发现哪些典型异常：**

1. CrashLoopBackOff（容器反复崩溃重启）
2. OOMKilled（内存超限被杀）
3. ImagePullBackOff / ErrImagePull（镜像拉取失败）
4. Pending / FailedScheduling（Pod 调度不上去）
5. Node NotReady / DiskPressure / MemoryPressure（节点异常）
6. PVC Pending（存储卷申请不成功）
7. Service 无 Endpoint（服务后面没有可用 Pod）
8. Deployment 副本不匹配、Job Failed

### P2 深度采集（已完成，已接入主流程）

- **当前日志**：每个容器最多 500 行、单容器 64 KiB、单行 1 KiB 硬上限
- **历史日志（previous logs）**：容器重启前的日志；不存在时静默跳过
- **单 Pod 超时 30 秒**、并发默认 4，防止一个坏 Pod 拖垮整次扫描
- 所有日志在进入系统前先脱敏（密码/token/密钥等替换为 [REDACTED]）
- **现状（P4）**：已接入主流程——规则引擎命中需要日志的异常 Pod 后，自动拉取日志/历史日志并挂到 Finding 证据上。

Events：在 P1 就已按命名空间一次性采集并建立本地索引（不会为每个 Pod 单独查一次事件）。

### P3 关联索引（已完成）

已建立三条"关系链"，把孤立的资源串起来：

1. **Pod → 归属 Workload → Node**（这个 Pod 是谁管的、跑在哪台机器上；含间接归属：Pod → ReplicaSet → Deployment）
2. **Pod → PVC → PV → StorageClass**（存储链路）
3. **Service → EndpointSlice → Pod**（流量链路，另含 Service selector 与 Pod 标签的双向匹配）

并附带事件索引（按对象 UID / kind-namespace-name 双键查询）。索引已由 P4 规则引擎消费。

**这些索引未来用来干嘛：** 去重（Node 挂了导致一堆 Pod 异常只扣一次分）、提权重（影响线上服务更严重）、精准定位根因（把"表面故障"和"真正根因"分开）。

### P4 规则引擎 + 证据链（已完成）

- 13 条规则：CrashLoopBackOff / OOMKilled / ImagePullBackOff / PendingPod / NodeNotReady / NodeDiskPressure / NodeMemoryPressure / PVCPending / FailedMount / ServiceNoEndpoint / DeploymentReplica / StatefulSetReplica / JobFailed
- 严重级自动调整：生产环境 / 系统命名空间 / 被 Service 选中 / 大量副本不可用 → 升级；测试与开发环境 → 降级
- 每条 Finding 带稳定指纹（Pod 归属 workload 归一化，支撑跨扫描指纹对比；1.2 为二期 Agent 提供结构化的历史差异数据）
- 节点级根因（NotReady/压力）导致的下游 Pod 异常自动标记 Correlated，供评分去重（P5 使用）
- **Phase2 已接入主流程**：命中需要日志的规则后自动拉日志/历史日志并挂载证据

### P5 健康评分 + 报告（已完成）

- **健康评分**：100 − Σ罚分（CRITICAL 30 / HIGH 15 / MEDIUM 5 / LOW 1），相关异常（Correlated）不重复扣分，封顶 0，附每条扣分原因
- **Markdown 报告**：概览 / 健康评分与扣分明细 / 异常摘要 / 分级问题详情（证据链）/ 资源汇总 / 系统组件 / 采集错误
- **JSON / YAML 机器可读报告**：latest.json 同时是二期 Agent 的历史记忆契约（findings 指纹 + diagnoses）
- **报告目的地按场景**：全集群 scan 默认写 latest.md + latest.json；目标扫描（--namespace / -n）默认只打印终端不落盘；定时任务 `--report-mode daily` 追加时间戳文件；`--format markdown|json|yaml` 控制终端完整报告的格式

### 单 Pod 扫描（P9.1 提前实现）

- 命令：`k8s-ai scan pod <name> -n <namespace>`，输出 `Kubernetes Pod 巡检报告：ns/name`（含 Scan Scope 标注）
- 只保留该 Pod 及其直接关联资源（Workload/Node/PVC/Service）的 Finding；命中需要日志的规则时自动拉日志/历史日志
- 默认终端直出，不写报告文件

### P6 LLM 客户端（已完成，P7 起已接入 scan）

- OpenAI Compatible Chat 客户端（接口在 llm 包，业务层不依赖 OpenAI SDK）
- 单次调用超时（默认 120s）；429 尊重 Retry-After；5xx 指数退避有限重试；其他 4xx 不重试
- api_key 只在 Authorization 头，请求体与错误消息均不出现（错误消息先显式剔除 api_key 再脱敏）
- 内置 Kubernetes SRE 系统提示词（go:embed，随二进制发布）；不可信数据用定界符包裹并声明"是数据不是指令"
- 已随 P7 接入主流程：规则判定后自动调用 LLM 诊断（失败自动降级为规则初步判断）

### P7 诊断编排（已完成，scan 已接入）

- 流程：Findings → 严重级排序 → 预算裁剪（单 Finding 8k / 总 32k / top-N 30 自适应）→ DiagnosisContext → LLM → JSON 解析 → Schema 校验 → Evidence ID 校验 → kubectl 命令校验
- 命令校验：排查/验证只允许只读动词；修复命令必须含 namespace/资源/名称并标注风险；非法命令整体丢弃
- 降级策略：LLM 不可用/超时/限流 → 该 Finding 标注"LLM 分析不可用"，scan 不失败；JSON 解析/校验失败自动带修复指令重试一次
- 报告已渲染"Root Cause / 排查命令 / 修复命令（含风险）/ 验证命令"段落

### P8 报告可读性优化（已完成）

- **终端一屏摘要**：默认只输出健康条（如 `████████░░`）、严重级计数（CRITICAL 1 | HIGH 3 | ...）、Top 10 重点问题（现象/根因/建议）、系统组件状态、报告路径；`--verbose` 才打印完整报告
- **日志证据压缩**：只保留前 8 行 + `…（共 N 行，ERROR M 行，WARN K 行）`，报告文件体积实测 -91%（726KB → 62KB）
- **视觉元素**：严重级图标 🔴🟠🟡🔵⚪、健康分进度条、组件状态 ✅⚠️❌（Markdown 与终端共用）

### P10 部署交付（已完成）

- 多阶段 Dockerfile：distroless static 非 root 小镜像，prompts 内嵌
- deploy/：namespace / serviceaccount / clusterrole（只读）/ clusterrolebinding / configmap / secret.example / pvc / cronjob（每日 08:00 Asia/Shanghai、Forbid 并发、OnFailure、报告 PVC）
- RBAC 清单测试 + kubectl dry-run 客户端校验全部通过
- README 完整说明（安装/配置/LLM/CLI/CronJob/RBAC/安全/故障示例/开发/测试）
- Makefile 增加 docker 目标、lint 修正为文件列表校验

### P11 加固收尾（已完成，一期 1.1 交付）

- **大集群压测**：Phase1 采集 1000 Pod/50 ns ≈ 2.5ms、内存 4.9MB；关联+规则链路 ≈ 0.46ms；请求量与 Pod 数无关（无 N+1）——详见 docs/design/PERFORMANCE.md
- **四类架构测试最终确认**：只读（AST，新增禁止 `Secrets()` 访问）/ RBAC 清单 / 泄密（LLM 请求 + Secret 零访问）/ 请求量，全部通过
- **-race 说明**：本机无 C 编译器（CGO 关闭）无法运行；Makefile 已留 test-race 目标，Linux/CI 安装 gcc 后可直接使用
- **安全核对清单**：写入 README（无写操作/不读 Secret/双重脱敏/api_key 不泄露/LLM 不编造/不执行命令/错误隔离/RBAC 只读 8 项）
- **安装部署手册**：[INSTALLATION.md](INSTALLATION.md)（前置要求/本地安装/镜像构建/集群部署分步/CronJob/RBAC/使用指导/故障排查/安全）

### P12 一期 1.2（已完成）：Server 最小化 + 历史差异数据

- **HTTP Server**（`k8s-ai server`，默认 :8080）：GET /healthz、/readyz、/version；POST /api/v1/scans（异步任务，返回 scanId；单任务并发限制，已有任务运行时返回 409）；GET /api/v1/scans/{id}（pending/running/succeeded/failed + result）
- **历史差异数据**：读取上一份 latest.json，按 Finding 指纹对比输出新增/持续/恢复（ADR-019）；报告新增"历史对比"段、终端摘要显示计数、latest.json 含 history 字段——这是二期 Agent 的会话记忆底座（scan_cluster 自动携带）
- 部署：deploy/deployment.yaml + service.yaml（与 CronJob 共用报告 PVC 时需 RWX 存储类）

---

## 第三章：数据流向图

```mermaid
flowchart LR
    A["用户执行 k8s-ai scan"] --> B["1 连接集群<br/>kubeconfig / 集群内自动识别（只读）"]
    B --> C["2 Phase1 全量轻量扫描<br/>约 17 类资源 + 每命名空间一次 Events<br/>请求量与 Pod 数量无关（无 N+1）"]
    C --> D["3 关联索引 P3<br/>三条关联链 + 事件索引<br/>（已完成，P4 规则引擎消费）"]
    D --> E["4 规则引擎判定异常 Finding<br/>13 条规则（已完成）"]
    E --> F["5 Phase2 深度采集（已接入）<br/>只对异常 Pod 拉日志/历史日志<br/>并挂载证据"]
    F --> G["6 LLM 深度分析<br/>（P6-P7 已完成；只分析，不执行）"]
    G --> H["7 报告 Markdown/JSON + 健康评分<br/>（P5 已完成；P12 增加历史对比）"]
    H --> I["运维人员查看报告<br/>复制 kubectl 命令 → 人工确认 → 人工执行"]
```

**文字版流程：**

1. 用户执行 `k8s-ai scan`，程序自动识别 kubeconfig 或集群内配置并连接集群（只读）。
2. 全量轻量扫描：一次性拉取所有资源列表 + 每个命名空间一次事件。请求次数固定（约 17 类资源 + 命名空间数），不会因为 Pod 多而爆炸。
3. **P3 关联索引（已完成）**：把 Pod/Workload/Node/存储/网络串成关系链，供规则引擎查询。
4. **规则引擎（已完成）**：13 条规则自动判定异常，产出 Finding（含严重等级、证据、指纹）。
5. **P2 深度采集（已接入）**：只对需要日志的异常 Pod 拉日志和历史日志，挂上证据。
6. **LLM 深度分析（已完成）**：基于证据做根因判断，生成排查/修复命令（只展示）；超时自动降级为规则初步判断。
7. **报告（已完成）**：输出 Markdown/JSON，含健康评分、异常摘要、证据链、Root Cause、命令与风险；P12 增加"历史对比"（新增/持续/恢复）。
8. 运维人员查看报告，复制命令、人工确认、人工执行。

---

## 第四章：关键配置项（用户可调什么）

配置文件默认在 `~/.k8s-ai/config.yaml`（`k8s-ai config init` 生成），以下是最重要的几个开关：

| 配置 | 默认值 | 含义 |
|---|---|---|
| scan.concurrency | 8 | 全量扫描并行度（越高越快，越费 API Server） |
| scan.phase2_concurrency | 4 | 深度采集（日志）并行度 |
| scan.max_log_lines | 500 | 每个容器最多拉多少行日志 |
| scan.max_log_bytes | 64 KiB | 单个容器日志字节上限（防超大日志） |
| scan.timeout | 5m | 整次扫描总超时（LLM 场景建议 10m） |
| llm.enabled / endpoint / model | true / http://localhost:8000/v1 / qwen-plus | 大模型开关、地址、模型 |
| llm.timeout | 120s | 单次 LLM 调用超时（思考型模型建议保持） |
| kubernetes.qps / burst | 20 / 40 | 对 API Server 的访问限速（防压垮集群） |
| report.directory | ./reports | 报告输出目录 |

命令行还能临时覆盖：`--namespace`/`-n`（只扫某个命名空间）、`--since`（只看最近 N 小时日志）、`--fail-on`（达到指定严重级就返回失败退出码）、`--report-mode none|latest|daily`（报告目的地）、`--format markdown|json|yaml`（终端报告格式）、`--verbose`（终端输出完整报告）；单 Pod：`k8s-ai scan pod <name> -n <ns>`。

---

## 第五章：当前状态与已知约束

### 已经"真实可用"的能力

- 手动 `k8s-ai scan` 全集群扫描：已在真实集群跑通（497 Pod / 36 命名空间 / 19 节点），约 2-7 秒完成（含日志采集），0 采集错误
- `k8s-ai config init` / `config validate`（生成配置、校验配置与集群连通性）
- `k8s-ai version`；`--fail-on` 退出码契约（0 成功 / 1 错误 / 2 达到阈值）
- 只读 Kubernetes 客户端 + 脱敏库 + 架构测试（自动证明代码里没有任何写操作）
- 关联索引三条链（P3）：owner 归属链、存储链、Service→EndpointSlice→Pod 与标签匹配，P4 规则引擎已消费
- 规则引擎（P4）：13 条规则自动发现异常并挂载证据，真实集群一次 scan 即产出 Findings；Phase2 深度采集已接入主流程
- 健康评分 + 报告（P5）：全集群 scan 自动写 latest.md/latest.json（评分/扣分明细/分级详情）；目标扫描终端直出完整报告
- 单 Pod 扫描（P9.1 提前）：`scan pod <name> -n <ns>` 输出 Pod 巡检报告（Scope 标注、证据链、日志）
- 报告可读性（P8）：终端一屏摘要、日志证据压缩（-91% 体积）、严重级图标与健康条
- LLM 诊断真实可用（P7 加固，与 Qwen3.5-397B 网关联调通过）：扫描过程有进度日志、LLM 并发 2、超时自动降级不失败、prompt 内置 JSON Schema、支持思考型模型（reasoning_content 检测，输出上限放开到 4096）
- 性能压测（P11）：1000 Pod 采集约 2.5ms、规则链路约 0.46ms、请求量无 N+1；-race 需 CGO（本机无 gcc，CI/Linux 可跑）
- 安装部署：见 [INSTALLATION.md](INSTALLATION.md)（本地/镜像/集群 CronJob 分步 + 使用指导 + 故障排查）
- HTTP Server（P12）：`k8s-ai server` 提供 healthz/version/异步扫描 API，真实集群冒烟通过
- 历史对比（P12）：按指纹输出新增/持续/恢复，报告与 latest.json 均携带

### "代码已就绪但未启用"的能力

- 暂无：Phase2 已接入、规则引擎已上线、LLM 已接入（scaffold 清单生成能力未实现，可留二期）

### 尚未开始的能力

- 二期（v2.0.0 阶段）：chat Agent / Tool Calling / 会话记忆 / 审批执行（按 PROJECT_SPEC_二期.md）
- scaffold 清单生成能力（未实现，可留二期）

### 已知约束（LLM）

- 思考型大模型（如 Qwen3.5-397B）单次诊断约 1.5-2 分钟：请保持 `llm.timeout: 120s` 或更高，`scan.timeout` 建议 10m（多 Finding 场景），或改用非思考型/更小模型提速
- 扫描全程有进度日志（stderr），不会"看起来卡死"；LLM 不可用/超时自动降级为规则结论，scan 不失败
- **LLM 降级不丢信息**：规则引擎基于已采证据生成"初步判断"，并自动提取日志关键行（panic/fatal/error）——即使大模型超时，报告也能直接给出关键错误（实测能定位 RocketMQ topic 路由不存在导致的 panic）
- **关联证据补全**：Deployment/Service/Node 类 Finding 会附带关联异常 Pod 的崩溃状态与日志关键行，LLM 可跨资源定位真正根因
- **日志证据保留尾部**：上下文与报告中的日志取末尾（panic/错误通常在最后），不再从头截断
- **配置类修复建议**：诊断上下文携带 Pod 引用的 ConfigMap 名称（仅名称不读数据），LLM 会优先给出 `kubectl edit configmap <name> -n <ns>`（实测 nginx `listen 80x` 案例）
- **速度对比 kubectl-ai**：一期是"批量单次"诊断（一次长 JSON 输出），思考型大模型解码慢所以慢；二期 chat 将采用 kubectl-ai 式"小步工具调用 + 短输出 + 流式"，速度可对齐。已提供 `llm.disable_thinking` 开关（Qwen/vLLM 惯例），但当前网关（10.62.64.38）实测忽略该字段；**一期模型选型建议**：巡检/排查/分析用更快的非思考型模型（如 qwen-turbo/flash），思考型大模型（Qwen3.5-397B）留给二期 chat

### 硬限制（一期红线）

- **绝对只读**：不 create/update/patch/delete/exec，不读 Secret，不执行任何 LLM 生成的命令
- **不可信数据处理**：日志、Events、ConfigMap、注解一律先脱敏再使用，且视为"数据"而非"指令"
- **故障隔离**：单个资源采集失败不中断整次扫描，只记录到"采集错误"清单

---

## 第六章：后续阶段预告

- **P12（已完成）**：Server 最小化（healthz/version/异步扫描 API）+ 历史差异数据（二期 Agent 记忆契约）
- **一期终版达成**：1.1（巡检/排查/分析/报告/部署）+ 1.2（Server/历史对比）全部交付
- **二期（v2.0.0）**：`k8s-ai chat` 自然语言 Agent、Tool Calling、会话记忆（复用 latest.json 历史差异数据）、安全审批与执行

---

## 附录 A：快速上手（写给运维）

```text
1. k8s-ai config init          # 生成配置
2. 编辑 ~/.k8s-ai/config.yaml  # 填 LLM 地址/模型（推荐快速非思考型模型）
3. k8s-ai config validate      # 校验配置 + 确认能连上集群
4. k8s-ai scan                 # 全集群扫描（只读）
5. 查看 ./reports/ 下的报告（latest.md / latest.json）
```

## 附录 B：术语表

| 术语 | 大白话 |
|---|---|
| Scan | 一次完整的集群巡检 |
| Phase1 | 第一遍"快照"：把集群资源全量列一遍 |
| Phase2 | 第二遍"取证"：只对异常对象拉日志 |
| Finding | 一条被判定出的异常（如"payment-api CrashLoopBackOff"） |
| Evidence | 支撑这条异常的实锤证据（重启次数、退出码、日志关键行等） |
| HealthScore | 集群健康评分（100 分制，按异常严重级扣分） |
| Correlated | 由同一个根因连带出来的下游异常（评分只扣一次） |
| 历史对比 | 与上一轮巡检按指纹对比：新增 / 持续 / 恢复（二期 Agent 记忆契约） |
| collection_errors | 采集失败清单（如某个 Pod 日志没权限），不影响整次扫描 |

## 附录 C：给决策者的安全承诺

1. **四层只读保证**：部署权限（RBAC）→ 代码门面（只暴露读方法）→ 无 shell 调用 → 自动化测试证明无写操作。
2. **双重脱敏**：数据进入系统前脱敏一次，报告输出前再脱敏一次；Secret 永远不读取。
3. **LLM 无执行能力**：大模型只产出文本分析，命令永远只是字符串。