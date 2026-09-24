# k8s-ai 项目宣讲

> Kubernetes AI 智能巡检、故障诊断与自然语言运维 Agent

---

## 一、项目概述

### 1.1 定位

k8s-ai 是一个**专业的 Kubernetes AI 智能运维agent**，分两期交付：

- **一期**：只读巡检 + 规则引擎 + 私有大模型诊断 + 报告
- **二期**：chat 对话 Agent + web 浏览器界面 + 读写操作 + 人工确认

### 1.2 核心价值

- **快速定位问题**：执行一次 `k8s-ai scan`，快速知道集群哪里有问题、为什么、影响什么、应该怎么排查和怎么修
- **自然语言交互**：通过 `k8s-ai chat` 或 `k8s-ai web`，用自然语言与 Kubernetes 对话，自动诊断、修复、验证
- **安全可控**：写操作必须人工确认，LLM 不是权限主体，真实边界来自 Kubernetes RBAC

---

## 二、整体架构

### 2.1 整体架构图

![整体架构](images/architecture-overall.svg)

**架构分层说明**：

- **CLI 入口层**：scan / chat / web / server / config 命令入口
- **服务层**：ChatService（二期 Agent 对话）、ScanService（一期巡检）
- **Agent 层**：Agent Loop（消息循环）、Tools Registry（工具注册表）
- **LLM 层**：OpenAI 兼容接口，支持流式/非流式、思考型模型
- **扫描引擎**：Scanner + Reader（client-go），并发采集集群资源
- **规则引擎**：35 条内置规则，A/B/C 分级，健康评分
- **证据链**：Evidence（状态/Events/日志），脱敏处理
- **诊断层**：Diagnosis（根因分析、修复建议）
- **工具集**：20 只读工具 + 1 写工具（kubectl）+ 诊断工具
- **审批层**：Approver（风险分级、拒绝闸门、批量审批）
- **执行层**：Executor（kubectl argv 直调、client-go 写操作）
- **会话层**：Session（持久化）、Audit（脱敏落盘）
- **报告层**：Markdown / JSON / 终端摘要
- **历史对比**：新增/持续/恢复问题追踪
- **安全层**：脱敏/定界、Secret 拒读、Prompt 注入防护
- **MCP 预留**：Client 接口、注册表适配器（Phase 3）

​	k8s-ai 采用分层架构设计，从上到下分为四层。 最上面是用户交互层 ，提供终端 chat、Web UI 和 scan 三种入口； 中间是核心能力层 ，包含 **Agent Loop**、**20+1 工具集**、审批层和执行层，负责理解用户意图、调用工具、审批写操作并执行； 下面是一期继承的能力层 ，包括扫描引擎、35 条规则、证据链、LLM 诊断和报告输出； 最底层是安全层 ，贯穿全链路，确保 Secret 不泄露、写操作必须审批、所有数据脱敏。整体设计遵循"读自动、写审批、LLM 不是权限主体"的安全原则。

---

## 三、一期：只读巡检 + 规则引擎 + LLM 诊断

### 3.1 一期架构图

![一期架构](images/architecture-phase1.svg)

**一期核心组件**：

- **CLI 入口**：`k8s-ai scan` 命令
- **配置层**：Viper + YAML，支持 CLI > 环境变量 > YAML > 默认优先级
- **Kubernetes Client**：client-go typed client，只读 RBAC
- **HTTP Server（可选）**：REST API + 异步扫描
- **扫描引擎（Scanner）**：Pod / Node / Workload / Storage / Network / Events / 系统组件，并发采集（8 worker）
- **规则引擎（Rule Engine）**：35 条内置规则，A/B/C 严重级分级，健康评分（0-100）
- **证据链（Evidence）**：状态字段、Events 事件、日志关键行
- **LLM 诊断**：OpenAI 兼容接口，根因分析 + 影响评估，排查/修复/验证命令
- **报告输出（Report）**：Markdown（latest.md）、JSON（latest.json）、终端摘要（一屏显示）
- **历史对比（History）**：新增问题、持续问题、恢复问题
- **安全层**：只读 RBAC | Secret 内容值拒读 | 双重脱敏 | Prompt 注入防护 | LLM 无执行能力

一期的核心流程是一条直线：CLI 触发扫描 → Scanner 并发采集集群资源 → Rule Engine 匹配 35 条规则发现异常 → Evidence 收集证据链 → LLM 基于证据做根因分析 → Report 输出报告。全程只读，不操作集群。

### 3.2 一期核心能力

#### 全集群巡检

- **7 大维度**：Pod / Node / Workload / Storage / Network / Events / 系统组件
- **并发采集**：8 worker 并发，单集群 45s 完成
- **资源覆盖**：Deployment / StatefulSet / DaemonSet / Job / CronJob / Service / Ingress / PVC / ConfigMap / Secret（仅元数据）/ NetworkPolicy / HPA / PDB 等

#### 35 条内置规则

- **A 级（严重）**：Pod CrashLoopBackOff / OOMKilled / Node NotReady / PVC 绑定失败
- **B 级（中等）**：Deployment 副本不匹配 / Service 无 Endpoint / HPA 目标缺失
- **C 级（建议）**：CronJob 挂起 / 默认拒绝网络策略 / Ingress 主机冲突

#### 证据链驱动

- **每个异常带真实证据**：状态字段 / Events / 日志关键行
- **LLM 只能基于证据分析**：不能编造资源或证据
- **双重脱敏**：采集边界 + 报告渲染

#### LLM 诊断

- **OpenAI 兼容接口**：支持 OpenAI / Qwen / DeepSeek / vLLM / Ollama
- **输出内容**：根因分析、影响评估、排查/修复/验证命令（只展示，不执行）
- **降级策略**：LLM 不可用/超时自动降级为"规则初步判断 + 日志关键行"

#### 健康评分

- **0-100 分制**：根据问题严重级扣分
- **扣分明细**：每个问题列出扣分项
- **历史对比**：新增/持续/恢复问题追踪

#### 报告格式

- **Markdown**：概览 / 健康评分 / 异常摘要 / 分级问题 / 历史对比 / 资源汇总 / 系统组件
- **JSON**：机器可读，二期 Agent 的历史记忆契约
- **终端摘要**：一屏显示健康条、严重级计数、Top 10 重点问题

### 3.3 一期使用示例

```bash
# 生成配置
k8s-ai config init

# 校验配置 + 集群连通性
k8s-ai config validate

# 全集群巡检
k8s-ai scan

# 指定命名空间巡检
k8s-ai scan -n production

# 单 Pod 诊断
k8s-ai scan pod payment-api-7f9b5c8d-2kq4p -n production

# 日报模式
k8s-ai scan --report-mode daily

# CI 模式（达到 HIGH 返回退出码 2）
k8s-ai scan --fail-on HIGH
```

### 3.4 一期输出示例

![Scan 输出示例](images/scan-output-demo.svg)

**输出说明**：

- **健康评分**：72/100，根据问题严重级扣分
- **问题分级**：3 个 HIGH 级别问题，5 个 MEDIUM 级别问题
- **问题详情**：每个问题包含现象、根因、建议修复命令
- **报告路径**：reports/latest.md | reports/latest.json

---

## 四、二期：自然语言 Agent + 读写操作 + Web UI

### 4.1 二期架构图

![二期架构](images/architecture-phase2.svg)

**二期新增组件**：

- **用户交互层**：k8s-ai chat（终端）、k8s-ai web（浏览器）、k8s-ai scan（继承一期）
- **服务层**：ChatService（CLI/Web 复用）、ScanService（一期能力）
- **会话与审计**：Session（持久化）、Audit（脱敏落盘）
- **Agent 核心层**：Agent Loop（消息循环）、四层意图决策树、流式输出（SSE）
- **工具集**：20 只读工具 + 5 诊断工具 + 1 写工具（kubectl）
- **审批层**：Approver 接口、风险分级（SAFE→CRITICAL）、批量审批面板、拒绝闸门（2次硬拦截）
- **执行层**：kubectl argv 直调（默认）、policy-gated shell（审批后）、client-go 写操作、执行后验证（Verify）
- **LLM 层**：OpenAI 兼容接口、流式/非流式、思考型模型
- **一期能力继承**：Scanner / Rule Engine / Evidence / Diagnosis / Report / History
- **MCP 预留**：Client 接口、注册表适配器（Phase 3）
- **安全闭环**：审批唯一安全门 | 脱敏 | Secret 拒读

二期在一期基础上加了 Agent 闭环。用户用自然语言提问，Agent Loop 通过四层意图决策树选择工具，只读操作自动执行，写操作走审批层（风险分级 + 人工确认），Executor 执行后自动验证。同时新增 Web UI、会话持久化和审计日志，形成完整的"诊断→审批→执行→验证"闭环。

### 4.2 二期核心能力

#### Agent 工具集

**20 个只读工具**：

- Pod / Node / Workload / Storage / Network / Events / 日志 / ConfigMap / 节点资源用量
- 5 个诊断工具：scan_cluster / diagnose_pod / diagnose_node / diagnose_workload / diagnose_service

**1 个写工具**：

- **kubectl**：唯一写入口，LLM 生成完整命令字符串
- 含 shell 构造走 `bash -c`，无 shell 走 argv 直调

#### 四层意图决策树

1. **诊断意图**：kubectl -o yaml + previous logs + events
2. **浏览意图**：结构化摘要工具
3. **ns 级健康**：scan_cluster(namespace=X)
4. **集群全景**：scan_cluster() + 健康检查协议

#### 流式输出

- **SSE 逐 token 推送**：`llm.stream: true` 开启
- **Web UI 增量渲染**：appendDelta 事件
- **流式失败自动降级**：兼容不支持 stream 的网关

#### Web UI

- **多会话管理**：侧边栏会话列表
- **流式输出**：SSE 流式处理
- **工具调用卡片**：Show output 折叠查看执行命令与结果
- **思考过程展示**：reasoning 内容展示 AI 的思考过程
- **流程时间线**：同一轮对话的思考→工具→结论竖向时间线展示
- **审批卡片**：等效 kubectl 命令 + 变更说明 + 风险等级
- **暗色主题**：现代化 UI 设计
- **会话持久化**：刷新后命令/参数/结果不丢失

#### 安全执行闭环

```
诊断 → 询问"需要我修复吗？" → 用户同意
  → 写操作审批卡片（等效 kubectl 命令 + 变更说明 + 风险等级）
  → 执行 → 验证
```

**拒绝闸门**：同一写操作被用户拒绝 2 次后自动硬拦截，要求 LLM 给出替代方案

**kubectl 命令归一化**：忽略 `--force`/`--grace-period` 等修饰性 flag，防止绕过

#### 会话与审计

- **会话持久化**：`--session` / `--new-session` / `--list-sessions` / `--delete-session`，落盘 `~/.k8s-ai/sessions`
- **审计日志**：用户输入 / LLM / 工具调用 / 审批 / 执行全部留痕 `~/.k8s-ai/audit/<session>.jsonl`（脱敏）
- **MCP 预留**：Client 接口 + 注册表适配器（二期 v1 不连接外部服务器）

### 4.3 安全体系

| 检查项 | 保障机制 | 验证方式 |
|--------|----------|----------|
| 写操作必须审批 | Approver.Confirm + 风险分级 + 拒绝闸门 | agent 单测 |
| shell 命令需审批 | `HasShellSyntax()` → `IsReadOnly()=false` | approval 单测 |
| 不读 Secret 内容 | Secret 内容值禁止 + RBAC 仅元数据/键级读取 | AST 扫描 + fake Action 断言 |
| 脱敏 | 采集边界 + 报告渲染双重脱敏 | security 单测 + LLM 请求泄密测试 |
| api_key 不泄露 | 仅在 Authorization 头 + 错误显式剔除 | llm httptest |
| LLM 不编造 | Evidence ID 校验 + 命令白名单 | diagnosis 单测 |
| 交互式命令拒绝 | deny_ops 硬拒（edit/attach/port-forward 等） | executor 单测 |
| 单资源失败不中断 | collection_errors 隔离 | scanner 错误隔离测试 |
| RBAC 清单只读 | 解析 deploy/clusterrole.yaml 断言 | `tests/arch` RBAC 测试 |

### 4.4 二期使用示例

#### 终端 Chat 模式

```bash
# 交互模式
k8s-ai chat

# 单发模式
k8s-ai chat "为什么 payment-api 一直重启？"

# JSON 输出
k8s-ai chat --format json "检查下集群健康状态"

# 会话管理
k8s-ai chat --list-sessions
k8s-ai chat --new-session
k8s-ai chat --session <session-id>
```

![Chat 交互示例](images/chat-demo.svg)

**交互说明**：

- **用户提问**：为什么 payment-api 一直重启？
- **AI 思考**：正在分析...需要检查 Pod 状态、日志和事件
- **工具调用**：get_pod / get_previous_logs / get_events / get_deployment
- **诊断结果**：现象、根因、影响、建议修复步骤

#### Web UI 模式

```bash
# 启动 Web UI
k8s-ai web --addr 127.0.0.1:8081
```

**浏览器访问**：`http://127.0.0.1:8081`

![Web UI 示例](images/webui-demo.svg)

**功能展示**：

- **会话列表**：侧边栏显示历史会话
- **工具执行过程**：时间线展示每个工具调用的状态和耗时
- **诊断结果**：现象、根因、影响、建议修复步骤
- **审批卡片**：询问用户是否执行修复操作

#### 安全执行闭环示例

![审批流程示例](images/approval-flow-demo.svg)

**流程说明**：

1. **用户提问**：payment-api 内存不足，帮我扩容到 512Mi
2. **AI 分析**：生成执行计划（kubectl patch 命令）
3. **审批请求**：显示风险等级、操作、目标、变更、影响
4. **用户确认**：输入 y 确认执行
5. **执行工具**：调用 kubectl 工具执行 patch 操作
6. **验证结果**：调用 get_deployment 验证修复成功
7. **完成提示**：修复完成，所有副本正常运行

### 4.5 故障诊断示例

#### CrashLoopBackOff（配置错误）

**现象**：Pod 处于 CrashLoopBackOff 状态，已重启 15 次

**证据链**：

- 日志关键行：`nginx: [emerg] host not found in "80x" of the "listen" directive`
- Events：BackOff 5m (kubelet) - Back-off restarting failed container

**LLM 诊断**：

- **根因**：nginx 配置文件 listen 指令端口格式错误（"80x" 应为 "80"）
- **影响**：服务不可用，用户无法访问
- **修复建议**：修正配置文件，重新部署

#### OOMKilled

**现象**：容器被 OOMKilled，exitCode=137

**证据链**：

- lastState.reason=OOMKilled / exitCode=137 / memory.limit=256Mi
- 实际使用内存：312Mi

**LLM 诊断**：

- **根因**：内存限制 256Mi 不足，应用实际使用 312Mi
- **影响**：服务频繁重启，用户体验差
- **修复建议**：`kubectl set resources deployment/payment-api --limits=memory=512Mi`

#### PVC 绑定失败（StorageClass 不存在）

**现象**：PVC 处于 Pending 状态，无法绑定

**证据链**：

- Events：FailedBinding - storageclass "fast-ssd" not found
- PVC spec.storageClassName: fast-ssd

**LLM 诊断**：

- **根因**：StorageClass "fast-ssd" 不存在
- **影响**：Pod 无法启动，依赖该 PVC 的服务不可用
- **修复建议**：
  1. 创建 StorageClass：`kubectl apply -f storageclass.yaml`
  2. 或修改 PVC 使用现有 StorageClass

---

## 五、部署方式

### 5.1 本地构建

```bash
# Linux / macOS
make build
./bin/k8s-ai version

# Windows（无 make）
.\__build.cmd
.\bin\k8s-ai.exe version
```

### 5.2 Docker 镜像

```bash
make docker VERSION=v2.6.0
```

镜像基于 distroless static（非 root、小体积），prompts 已内嵌，无需挂载额外文件。

### 5.3 Kubernetes 部署（CronJob 每日巡检）

```bash
# 1. 创建命名空间、SA、只读 RBAC、配置、报告 PVC
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/serviceaccount.yaml
kubectl apply -f deploy/clusterrole.yaml
kubectl apply -f deploy/clusterrolebinding.yaml
kubectl apply -f deploy/configmap.yaml
kubectl apply -f deploy/pvc.yaml

# 2. 配置 LLM api_key
cp deploy/secret.example.yaml deploy/secret.yaml
# 编辑 secret.yaml 替换 REPLACE_ME
kubectl apply -f deploy/secret.yaml

# 3. 每日 08:00（Asia/Shanghai）巡检
kubectl apply -f deploy/cronjob.yaml

# （可选）HTTP Server
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml
```

**CronJob 特性**：

- `concurrencyPolicy: Forbid`（不并发）
- `restartPolicy: OnFailure`
- 报告写入 PVC `/reports`（`latest.md` / `latest.json` / 时间戳文件）
- 非 root（uid 65532）

### 5.4 RBAC

- **一期（只读）**：`deploy/clusterrole.yaml` 只授权只读动词（get/list/watch + pods/log get）
- **二期（写权限）**：参考 `deploy/rbac-write.example.yaml`（默认 clusterrole 保持只读）

---

## 六、配置说明

### 6.1 关键配置项

默认配置 `~/.k8s-ai/config.yaml`，优先级：CLI > 环境变量 > YAML > 默认。

| 配置 | 默认 | 说明 |
|------|------|------|
| `kubernetes.kubeconfig/context/namespace` | 空 | 连接集群；空 kubeconfig 走 KUBECONFIG 或 ~/.kube/config |
| `scan.concurrency` / `scan.phase2_concurrency` | 8 / 4 | 采集并发 |
| `scan.max_log_lines` / `max_log_bytes` | 500 / 65536 | 日志上限 |
| `scan.timeout` | 5m | 整次扫描超时（LLM 场景建议 10m） |
| `llm.enabled` / `endpoint` / `model` | true / localhost:8000 / qwen-plus | 大模型 |
| `llm.timeout` | 120s | 单次 LLM 调用超时（思考型模型建议保持） |
| `llm.stream` | true | chat 流式输出（true = SSE 逐 token 推送到终端/Web） |
| `llm.thinking` | `{type: enabled}` | chat 标准思考参数（OpenAI 兼容网关） |
| `scan.thinking` | `{type: enabled}` | 一期 Scan 诊断思考参数 |
| `report.directory` / `format` | ./reports / markdown | 报告输出 |

### 6.2 环境变量

- `KUBECONFIG`
- `K8S_AI_LLM_ENDPOINT`
- `K8S_AI_LLM_API_KEY`
- `K8S_AI_LLM_MODEL`

### 6.3 LLM 模型选型

- **巡检诊断**：建议用**快速的非思考型模型**（如 qwen-turbo/flash），单 Finding 秒级返回
- **Chat 对话**：建议用**思考型大模型**（如 Qwen3.5-397B），单次诊断约 1.5–2 分钟，需配合 `llm.timeout ≥ 120s`、`scan.timeout 10m`
- **思考控制**：`llm.thinking.type: enabled|disabled`；一期扫描诊断使用 `scan.thinking.type` 独立控制
- **降级策略**：LLM 不可用/超时自动降级为"规则初步判断 + 日志关键行"，scan 不失败

---

## 七、HTTP API（一期 1.2）

```bash
k8s-ai server --addr :8080
```

**接口**：

- `GET /healthz` - 健康检查
- `GET /readyz` - 就绪检查
- `GET /version` - 版本信息
- `POST /api/v1/scans` - 异步扫描（单任务并发限制，409 冲突）
- `GET /api/v1/scans/{id}` - 查询扫描状态

详见 [INSTALLATION.md](INSTALLATION.md) §4.7。

---

## 八、技术亮点

### 8.1 架构设计

- **分层清晰**：17 个独立包，职责单一，依赖方向单向
- **可测试性**：架构测试（AST 扫描 + 依赖方向 + RBAC 清单 + 泄密检测 + 请求量 + executor shell 守护）
- **可扩展性**：MCP 预留接口、规则可插拔、工具注册表

### 8.2 安全设计

- **零信任**：LLM 不是权限主体，真实边界来自 Kubernetes RBAC
- **双重脱敏**：采集边界 + 报告渲染
- **审批唯一安全门**：写操作必须人工确认，shell 命令永不自动执行
- **拒绝闸门**：防止 LLM 反复重试被拒绝的操作

### 8.3 用户体验

- **流式输出**：SSE 逐 token 推送，Web UI 增量渲染
- **思考过程可见**：展示 AI 的推理过程，增强可信度
- **流程时间线**：思考→工具→结论竖向时间线，过程状态清晰
- **工具卡片结构化**：Pod/Event/Node 等表格化展示，信息密度高
- **会话持久化**：刷新后命令/参数/结果不丢失

### 8.4 故障诊断

- **证据链驱动**：每个异常带真实证据（状态字段 / Events / 日志），LLM 只能基于证据分析
- **四层意图决策树**：诊断/浏览/ns 级/集群全景，自动选择最优工具组合
- **历史对比**：新增/持续/恢复问题追踪
- **健康评分双轨**：规则评分 + LLM 综合评分（0-100）+ 差异解释

---

## 九、与 kubectl-ai 对比

| 能力 | kubectl-ai | k8s-ai 二期 |
|------|------------|-------------|
| 工具执行 | shell 执行 kubectl | kubectl 二进制 argv 直调（无 shell）；写操作唯一入口为 kubectlTool，含 shell 构造的命令经人工审批后走 `bash -c` |
| 权限判断 | modifies yes/no/unknown | SAFE→CRITICAL 风险分级 + 人类确认 |
| 写操作 | 交互确认 / `--skip-permissions` | 默认确认；`--skip-permissions` 仅自动化显式开启 |
| 诊断 | 无（纯问答） | 一期 Scanner+Rules+Evidence+Diagnosis 全部 Tool 化 |
| 执行后 | 不验证 | Plan → Execute → Verify 闭环，失败继续调查 |
| 数据安全 | 原始数据进 LLM/落盘 | 截断 + 脱敏 + 不可信定界 + 审计零敏感值 |
| UI | terminal/tui/web | terminal（chat CLI）+ web（k8s-ai web） |
| 会话 | memory/filesystem | memory/filesystem + Web 会话管理 |
| 流式输出 | 支持 | 支持（SSE 逐 token + 自动降级） |
| 思考过程 | 支持 | 支持（reasoning 展示 + 动态展开/折叠） |
| 工具卡片 | 基础展示 | 结构化渲染（Pod/Event/Node 表格化） |
| 会话持久化 | 基础 | 完整（arguments/data 落库，刷新不丢失） |

**核心差异**：k8s-ai 在一期已具备专业的 Kubernetes 巡检诊断能力（Scanner + 规则引擎 + 证据链 + LLM 诊断），二期在此基础上构建自然语言 Agent，**不是从零开始的 chat 工具**，而是**有专业诊断能力的 SRE Agent**。

---

## 十、路线图

### 已完成

- ✅ 一期：只读巡检 + 规则引擎 + 私有大模型诊断 + 报告（v1.x）
- ✅ 二期：chat 对话 Agent + web 浏览器界面 + 读写操作 + 人工确认（v2.x，当前 v2.6.0）

### 未来（Phase 3）

- 🔲 MCP 外部工具集成（连接外部 MCP 服务器）
- 🔲 可观测性集成（Prometheus/Grafana 等）
- 🔲 额外会话后端（当前 filesystem）
- 🔲 方法论 skill 化

---

## 十一、总结

k8s-ai 是一个**专业的 Kubernetes AI 智能运维平台**：

- **一期**：执行一次 `k8s-ai scan`，快速知道集群哪里有问题、为什么、影响什么、应该怎么排查和怎么修
- **二期**：通过 `k8s-ai chat` 或 `k8s-ai web`，用自然语言与 Kubernetes 对话，自动诊断、修复、验证

**核心优势**：

1. **专业诊断**：35 条内置规则 + 证据链 + LLM 诊断，不是简单的 kubectl 包装
2. **安全可控**：写操作必须人工确认，LLM 不是权限主体，真实边界来自 Kubernetes RBAC
3. **体验优秀**：流式输出、思考过程可见、流程时间线、工具卡片结构化
4. **架构清晰**：17 个独立包，职责单一，可测试、可扩展

---

**文档版本**：v1.0  
