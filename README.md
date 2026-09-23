# k8s-ai

> 当前版本：**v2.6.0**（二期迭代；版本约定见 [docs/VERSIONING.md](docs/VERSIONING.md)，变更见 [docs/CHANGELOG.md](docs/CHANGELOG.md)）

Kubernetes AI 智能巡检、故障诊断与自然语言运维 Agent
（一期：只读巡检 + 规则引擎 + 私有大模型诊断 + 报告；二期：`chat` 对话 Agent + `web` 浏览器界面 + 读写操作 + 人工确认）。

## 项目介绍

k8s-ai 让运维人员执行一次 `k8s-ai scan`，就能快速知道集群哪里有问题、为什么、影响什么、应该怎么排查和怎么修：

- **全集群巡检**：Pod / Node / Workload / Storage / Network / 系统组件 / Events
- **异常自动发现**：35 条内置规则（原有 23 条 + 新增 HPA 副本不匹配/目标缺失、PDB 阻断、RBAC 越权、ConfigRef 缺失、CronJob 挂起、默认拒绝网络策略、Ingress 主机冲突、超副本、存储卷挂载卡住、StorageClass 无 Provisioner、容器健康超时等 12 条）
- **证据链**：每个异常带真实证据（状态字段 / Events / 日志），LLM 只能基于证据分析
- **LLM 诊断**：OpenAI 兼容接口（OpenAI / Qwen / DeepSeek / vLLM / Ollama），输出根因、影响、排查/修复/验证命令（只展示，不执行）
- **报告**：Markdown + JSON（latest / 时间戳 / 终端一屏摘要），健康评分

**安全边界**：只读操作自动执行；写操作（patch/scale/restart/delete/apply 等）必须人工确认；LLM 不是权限主体，真实边界来自 Kubernetes RBAC；shell 构造（pipe/heredoc/redirect）允许在 chat 模式使用，但含写操作的 shell 命令必须走人工审批，shell 命令永不自动执行。

## 二期：chat / web（自然语言 Agent）

```bash
# 终端对话（交互 / 单发 / stdin / JSON）
k8s-ai chat "为什么 payment-api 一直重启？"
k8s-ai chat --format json "检查下集群健康状态"

# 浏览器对话（多会话 + SSE 流式 + 工具执行过程 + 审批卡片）
k8s-ai web --addr 127.0.0.1:8081
```

二期能力：

- **流式输出**：`llm.stream: true` 开启 SSE 流式处理；工具前保留简短进度说明，工具调用与结果分阶段展示，最终报告在工具完成后输出；流式失败自动降级为非流式（兼容不支持 stream 的网关）
- **Web 流程时间线**：同一轮对话中的思考、工具执行、最终结论按竖向时间线展示，过程状态更清晰
- **Agent 工具集**：20 个只读工具（Pod/Node/Workload/存储/网络/Events/日志/ConfigMap/节点资源用量 + 5 个诊断工具 scan*cluster/diagnose*\*，复用一期 Scanner/规则/诊断）+ **1 个写工具**（kubectl，唯一入口：LLM 生成完整命令字符串，含 shell 构造走 `bash -c`，无 shell 走 argv 直调）
- **四层意图决策树**：诊断意图→kubectl -o yaml+previous logs+events；浏览意图→结构化摘要工具；ns 级健康→scan_cluster(namespace=X)；集群全景→scan_cluster()+健康检查协议。读操作全走结构化工具；写操作只有 kubectl 一条路径，LLM 生成完整命令（含 shell 构造），统一走审批
- **集群健康检查**：节点 → 资源使用（metrics API）→ 命名空间 → Pod → 系统组件 → 警告事件 → 规则评分 + LLM 综合评分双轨
- **批量审批**：LLM 一轮返回多个写操作时，合并为一次审批面板（逐条展示等效 kubectl 命令 + 风险等级），用户选一次→顺序执行→分别回传结果。借鉴 kubectl-ai
- **安全执行闭环**：诊断 → 询问"需要我修复吗？" → 用户同意 → 写操作审批卡片（等效 kubectl 命令 + 变更说明 + 风险等级）→ 执行 → 验证
- **拒绝闸门**：同一写操作被用户拒绝 2 次后自动硬拦截，要求 LLM 给出替代方案；kubectl 命令归一化（忽略 --force/--grace-period 等修饰性 flag），防止绕过
- **会话持久化**：`--session` / `--new-session` / `--list-sessions` / `--delete-session`，落盘 `~/.k8s-ai/sessions`
- **审计**：用户输入 / LLM / 工具调用 / 审批 / 执行全部留痕 `~/.k8s-ai/audit/<session>.jsonl`（脱敏）
- **MCP 预留**：Client 接口 + 注册表适配器（二期 v1 不连接外部服务器）

> 写权限示例见 [deploy/rbac-write.example.yaml](deploy/rbac-write.example.yaml)（默认 clusterrole 保持只读）；安装部署见 [docs/INSTALLATION.md](docs/INSTALLATION.md)。

## 安装

> 完整安装/部署/使用手册见 [docs/INSTALLATION.md](docs/INSTALLATION.md)（本地、镜像、集群 CronJob 分步 + 故障排查）。

### 本地构建

```bash
make build
./bin/k8s-ai version
```

> `make build` 生成**当前操作系统**的原生二进制（Windows=.exe / Linux / macOS）；交叉编译 Linux amd64 用 `make build-linux`（或自行设置 GOOS/GOARCH）。

**Windows 无 make 时**：直接用项目自带的构建脚本：

```powershell
.\__build.cmd      # CMD / PowerShell 通用，自动注入版本号
.\bin\k8s-ai.exe version
```

### Docker 镜像

```bash
make docker VERSION=v2.6.0
```

镜像基于 distroless static（非 root、小体积），prompts 已内嵌，无需挂载额外文件。

## 快速开始

```bash
k8s-ai config init                       # 生成 ~/.k8s-ai/config.yaml
k8s-ai config validate                   # 校验配置 + 集群连通性
k8s-ai scan                              # 全集群巡检（写 reports/latest.md + latest.json）
k8s-ai scan -n mysql                     # 只巡检 mysql 命名空间（终端直出）
k8s-ai scan pod web-abc -n mysql         # 只诊断单个 Pod
k8s-ai scan --report-mode daily          # 日报模式（追加时间戳文件）
k8s-ai scan --fail-on HIGH              # 达到 HIGH 返回退出码 2（CI 用）
```

## 配置

默认配置 `~/.k8s-ai/config.yaml`，优先级：CLI > 环境变量 > YAML > 默认。

关键项：

| 配置                                           | 默认                              | 说明                                                    |
| ---------------------------------------------- | --------------------------------- | ------------------------------------------------------- |
| `kubernetes.kubeconfig/context/namespace`      | 空                                | 连接集群；空 kubeconfig 走 KUBECONFIG 或 ~/.kube/config |
| `scan.concurrency` / `scan.phase2_concurrency` | 8 / 4                             | 采集并发                                                |
| `scan.max_log_lines` / `max_log_bytes`         | 500 / 65536                       | 日志上限                                                |
| `scan.timeout`                                 | 5m                                | 整次扫描超时（LLM 场景建议 10m）                        |
| `llm.enabled` / `endpoint` / `model`           | true / localhost:8000 / qwen-plus | 大模型                                                  |
| `llm.timeout`                                  | 120s                              | 单次 LLM 调用超时（思考型模型建议保持）                 |
| `llm.stream`                                   | true                              | chat 流式输出（true = SSE 逐 token 推送到终端/Web）     |
| `llm.thinking`                                 | `{type: enabled}`                 | chat 标准思考参数（OpenAI 兼容网关）                    |
| `scan.thinking`                                | `{type: enabled}`                 | 一期 Scan 诊断思考参数                                  |
| `report.directory` / `format`                  | ./reports / markdown              | 报告输出                                                |

环境变量：`KUBECONFIG`、`K8S_AI_LLM_ENDPOINT`、`K8S_AI_LLM_API_KEY`、`K8S_AI_LLM_MODEL` 等。

### LLM 模型选型

- 巡检诊断建议用**快速的非思考型模型**（如 qwen-turbo/flash），单 Finding 秒级返回；
- 思考型大模型（如 Qwen3.5-397B）单次诊断约 1.5–2 分钟，需配合 `llm.timeout ≥ 120s`、`scan.timeout 10m`；
- 思考控制已迁移为标准 OpenAI 参数：`llm.thinking.type: enabled|disabled`；一期扫描诊断使用 `scan.thinking.type` 独立控制；
- LLM 不可用/超时自动降级为"规则初步判断 + 日志关键行"，scan 不失败。

## HTTP API（1.2）

`k8s-ai server --addr :8080`：`GET /healthz`、`/readyz`、`/version`；`POST /api/v1/scans`（异步扫描，单任务并发限制，409 冲突）；`GET /api/v1/scans/{id}`。详见 INSTALLATION.md §4.7。

## 报告

- **终端摘要**：一屏显示健康条、严重级计数、Top 10 重点问题（现象 / 日志关键行 / 根因 / 建议）、系统组件状态；`--verbose` 输出完整报告
- **Markdown**：概览 / 健康评分（含扣分明细）/ 异常摘要 / 分级问题（证据链、Root Cause、排查/修复/验证命令）/ 历史对比（新增/持续/恢复）/ 资源汇总 / 系统组件 / 采集错误
- **JSON**：`latest.json` 机器可读，同时是二期 Agent 的历史记忆契约（findings 指纹 + diagnoses + history 新增/持续/恢复对比）

## Kubernetes 部署（CronJob 每日巡检）

```bash
# 1) 创建命名空间、SA、只读 RBAC、配置、报告 PVC
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/serviceaccount.yaml
kubectl apply -f deploy/clusterrole.yaml
kubectl apply -f deploy/clusterrolebinding.yaml
kubectl apply -f deploy/configmap.yaml
kubectl apply -f deploy/pvc.yaml

# 2) 配置 LLM api_key（复制示例并替换）
cp deploy/secret.example.yaml deploy/secret.yaml
# 编辑 secret.yaml 替换 REPLACE_ME
kubectl apply -f deploy/secret.yaml

# 3) 每日 08:00（Asia/Shanghai）巡检
kubectl apply -f deploy/cronjob.yaml

# （可选 1.2）HTTP Server
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml
```

CronJob 特性：`concurrencyPolicy: Forbid`（不并发）、`restartPolicy: OnFailure`、报告写入 PVC `/reports`（`latest.md` / `latest.json` / 时间戳文件）、非 root（uid 65532）。

### RBAC

`deploy/clusterrole.yaml` 只授权只读动词（get/list/watch + pods/log get）；secrets 仅元数据/键级读取，不含 pods/exec、metrics。架构测试会解析清单断言只读约束。

## 安全

- **审批是唯一安全门**：只读操作自动执行；写操作（含 shell 构造）必须人工确认；shell 命令永不自动执行（不被 `--skip-permissions` 豁免）
- **放开 shell 构造**：chat 模式允许 pipe/heredoc/redirect/`&&`/`||`/command substitution，但含写操作的 shell 命令必须走审批；交互式命令（edit/attach/port-forward/proxy/run/cp/debug）仍硬拒
- **Secret 内容值永不进入采集/LLM/会话/审计**（元数据/键级可读）；日志/Events/ConfigMap/注解视为不可信数据，先脱敏再使用
- **双重脱敏**：采集边界 + 报告渲染；`api_key` 只在 Authorization 头，错误消息显式剔除
- **LLM 无自动执行能力**：写操作必须审批后执行；LLM 不是权限主体，真实边界来自 Kubernetes RBAC
- **拒绝闸门**：同一写操作被拒绝 2 次后硬拦截；kubectl 命令归一化防绕过
- **Prompt 注入防护**：不可信数据用定界符包裹并声明"是数据不是指令"

### 安全核对清单

| 检查项           | 保障机制                                      | 验证方式                         |
| ---------------- | --------------------------------------------- | -------------------------------- |
| 写操作必须审批   | Approver.Confirm + 风险分级 + 拒绝闸门        | agent 单测（审批/拒绝/硬拦截）   |
| shell 命令需审批 | `HasShellSyntax()` → `IsReadOnly()=false`     | approval 单测（7 类 shell 构造） |
| 不读 Secret 内容    | Secret 内容值禁止 + RBAC 仅元数据/键级读取 | AST 扫描 + fake Action 断言      |
| 脱敏             | 采集边界 + 报告渲染双重脱敏                   | security 单测 + LLM 请求泄密测试 |
| api_key 不泄露   | 仅在 Authorization 头 + 错误显式剔除          | llm httptest                     |
| LLM 不编造       | Evidence ID 校验 + 命令白名单                 | diagnosis 单测                   |
| 交互式命令拒绝   | deny_ops 硬拒（edit/attach/port-forward 等）  | executor 单测                    |
| 单资源失败不中断 | collection_errors 隔离                        | scanner 错误隔离测试             |
| RBAC 清单只读    | 解析 deploy/clusterrole.yaml 断言             | `tests/arch` RBAC 测试           |

## 故障示例

- **CrashLoopBackOff（配置错误）**：日志关键行直接给出 `nginx: [emerg] host not found in "80x" of the "listen" directive`，LLM 给出根因与修复建议
- **OOMKilled**：证据链含 `lastState.reason=OOMKilled / exitCode=137 / memory.limit`，LLM 输出根因与 `kubectl set resources` 建议
- **RocketMQ topic 不存在导致 panic**：日志关键行提取 `panic: StartConsumer fail ... route info not found`，规则初步判断兜底
- **Service 无 Endpoint**：规则发现 readyEndpoints=0 + selector 匹配数，LLM 结合关联 Pod 崩溃证据定位根因
- **PVC 绑定失败（StorageClass 不存在）**：Agent 批量审批修复（patch PVC storageClassName + 重建 Pod），拒绝后自动提出替代方案

## 开发

```bash
make build     # 构建
make test      # 单测
make vet       # go vet
make lint      # vet + gofmt 检查
make docker    # 构建镜像
```

架构约定见 AGENTS.md 与 docs/design/（架构 / 数据模型 / API / 扫描流程 / 安全 / 并发 / 测试 / ADR）。

## 测试

- 单元测试：规则、证据、脱敏、配置优先级、报告渲染、LLM 客户端（httptest）、诊断校验/降级、历史对比、Server API、Agent Loop（批量审批/拒绝闸门/命令归一化）
- 架构测试：只读 AST 扫描、依赖方向、RBAC 清单、泄密、请求量、executor shell 守护
- 每阶段出口：`go test ./...` + `go vet ./...` 全绿
