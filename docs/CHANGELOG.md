# Changelog

本项目遵循 docs/VERSIONING.md 的里程碑阶梯约定。

## [v1.0.3] - 2026-08-19

一期体验迭代：修复命令非交互化（edit→patch/apply）+ scan 终端阶段进度 UI（Spinne + LLM 长任务抚慰）。

### Added

- **scan 终端阶段进度 UI**（internal/cli/progress.go + model.ScanProgress）：
  - 6 个阶段统一显示 `⠋ 1. 标题 ➜ 当前子项` 单行 Spinner（TTY 下 80ms/帧，行内覆盖刷新）；
  - 阶段结束时 `✓ 1. 标题 完成（耗时 Xs，细节 Y）` 换行落盘，不刷屏；
  - JSON/YAML/`--verbose` 模式自动退化为逐行日志（无 `\r`），不污染机器可读输出；
  - 进度接口仅用 stdlib 类型放在 domain 包，Server/Cron 不设置则静默，严格符合「业务不依赖 CLI」；
  - 二期 Agent 的 Thinking / Tool Calling 状态指示器可直接复用同一接口扩展实现。
- **LLM 长任务抚慰提示**（internal/diagnosis/diagnostics.go DiagnoseIncidents）：
  - 每个 Incident 独立 ticker，超过 10s 未完成时触发 `LongTask`，文本显示「仍在诊断中（已 15s，常见耗时 10-30s，请稍候）」，每 10s 再提醒一次；
  - 避免用户在 LLM 诊断 20s+ 时误以为卡死而 Ctrl+C（经验 1073789）。
- **Diagnoser / service 层进度打点**：connect、phase1、rules、phase2、enrich、llm、report 共 7 阶段统一 PhaseStart/PhaseDone；LLM 阶段用 `scanProgressLLMBridge` 把 Incident 粒度事件（Started/Done）桥接到 PhaseSub，保证"诊断 i/N incident xxx"在终端可见。

### Changed

- **修复命令非交互强制化**（system.md §三十五 规则 4 + internal/diagnosis/commands.go `remediationAllowsNonInteractive`）：
  - 提示词禁止 `kubectl edit` / `exec -it` / `run -i` / `attach` / `port-forward` 等任何 TTY/编辑器交互；
  - 给出 4 种非交互替代并按推荐度排序：① `patch --type=merge`、② `get -o yaml > file && 改 && apply`、③ `create ... --dry-run=client \| apply`、④ `set image/resources/env/selector`；
  - 命令校验层同步丢弃交互式修复命令，避免模型「不听话」时进入报告（edit/attach/port-forward 直接失败；exec/run 带 `-i -t -it -ti --stdin --tty` 失败）。
- 补全 `exec`/`run`/`port-forward` 子命令缺资源名解析（按 firstPositional 取 Pod 名，与 `logs` 同一模式），保证后续命名空间补全/校验覆盖这些子命令。

### Fixed

- 上一版本 v1.0.2 中，修复命令推荐为 `kubectl edit configmap`（交互式、不可复制执行）的问题；现在会被命令校验过滤并要求 LLM 给出 patch/apply 非交互版本。
- **诊断修复退化为纯只读**：当 LLM 只给出 `get cm -o yaml` 等只读命令（被 §三十五 要求丢弃 edit/exec -it 后，常见"只给确认步骤"）时，remediation 列表最终全是只读命令，未满足 §三十五「至少一条写入操作命令」的硬性要求。现由 `ensureRemediation` + `fallbackWriteCommands` 规则级兜底，按 Finding.Rule / Kind 自动补齐 create/patch/apply/set/scale/rollout restart 等写入类命令（带 `<待确认>` 占位），保证每条诊断都能"下一步真正动起来"。同样兜底也应用于 `degraded` 降级路径。
- **Spinner 刷屏（非 TTY）**：`useSpinner` 此前只检查报告格式（markdown 且非 verbose），未检测 stdout 是否真正是交互式终端；当输出被管道/重定向时 `\r` 覆盖退化为逐行打印，80ms/帧 × 数十秒产生数百行垃圾。现由 `isTerminal(os.Stdout)` 用 `os.ModeCharDevice` 零依赖检测，管道/重定向自动退化为单行日志模式。
- **健康度标签写死"集群健康"**：`k8s-ai scan -n xxx` 扫描的是命名空间，`k8s-ai scan pod xxx` 扫描的是 Pod，但摘要头始终打印"集群健康"。现按 scope 动态显示：cluster →"集群健康"、namespace →"命名空间健康"、pod →"Pod 健康度"。

## [v1.0.2] - 2026-08-19

一期迭代：故障等级/修复方案/健康评分/派生展示四项优化 + kubectl 命令命名空间校验生效。

### Changed

- **故障等级强化（FailedMount / CreateContainerConfigError）**：容器启动、存储挂载失败的根因，若证据消息含 ConfigMap/Secret "not found"，等级强制提升为 HIGH（服务完全不可用）；`CreateContainerError` 规则已覆盖 `CreateContainerConfigError` 的等级映射（internal/rule）。
- **修复方案必须含写入命令**：system.md §三十五 新增硬性要求——每套修复方案必须至少包含一条 create/patch/apply/delete/set/edit/scale 等写入命令，不允许只有 get/describe/logs 等只读命令；只读确认命令仅作前置步骤；字段不确定时用 `<待确认的值>` 标注而非退回只读；多步操作按顺序列出。
- **健康评分基础分调整**：存在 HIGH 及以上等级的非关联（非 Correlated）根因时，基础分由 100 直接降至 70 再叠加其他罚分，保证"服务完全不可用"场景评分 ≤ 70（internal/report/health.go）。
- **派生告警不独立显示**：Deployment 副本不足、Service 无 Endpoint 等派生告警不再作为独立问题渲染，只并入根因的"关联问题（派生影响）"影响范围字段（internal/report/render.go）。

- **`-n` 命令校验真正生效**：此前"命令含 namespace 或 Finding 有 namespace 即通过"的兜底校验恒真，导致 LLM 生成的命令缺 `-n`、甚至写错命名空间也能进报告。现在命名空间级资源（Pod/Deployment/PVC/Service/ConfigMap/Secret 等）的命令必须携带 `-n/--namespace`（含 `-n=`/`--namespace=` 形式，动词前/后均可）：
  - 命令自带 namespace 但与 Finding 命名空间不一致 → 视为 LLM 编造/笔误，整条命令丢弃（ADR-005 防编造）；
  - 命令缺省 namespace → 自动补全 Finding 命名空间（如 `kubectl get pod web-0` → `kubectl -n prod get pod web-0`），保证报告命令复制即用（system.md §三十五）；
  - 显式 `-A`/`--all-namespaces` 的跨命名空间查询不补全、不校验。
- 动词前 `-n` 解析修复：此前 `kubectl -n prod get pod ...` 中动词前的 `-n prod` 会被解析器静默吞掉（namespace 丢失），现由 `extractVerb` 正确提取并参与校验/补全。
- `kubectl logs <pod>` 支持：`logs` 动词的位置参数是 Pod 名（无资源类型），此前会被整条丢弃；现按 Pod 处理，`-n` 补全对其同样生效（system.md §八 常用排查命令）。

### Fixed

- 修复命令/排查命令缺失 `-n` 或引用错误命名空间时仍被采纳的问题（校验形同虚设）。

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
