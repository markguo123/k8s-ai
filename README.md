# k8s-ai

> 当前版本：**v1.0.2**（一期迭代；版本约定见 [docs/VERSIONING.md](docs/VERSIONING.md)，变更见 [docs/CHANGELOG.md](docs/CHANGELOG.md)）

Kubernetes AI 智能巡检与故障诊断工具（一期：只读巡检 + 规则引擎 + 私有大模型诊断 + Markdown/JSON 报告）。

## 项目介绍

k8s-ai 让运维人员执行一次 `k8s-ai scan`，就能快速知道集群哪里有问题、为什么、影响什么、应该怎么排查和怎么修：

- **全集群巡检**：Pod / Node / Workload / Storage / Network / 系统组件 / Events
- **异常自动发现**：13 条内置规则（CrashLoopBackOff、OOMKilled、ImagePullBackOff、PendingPod、NodeNotReady、PVC Pending、Service 无 Endpoint、副本不匹配、Job Failed 等）
- **证据链**：每个异常带真实证据（状态字段 / Events / 日志），LLM 只能基于证据分析
- **LLM 诊断**：OpenAI 兼容接口（OpenAI / Qwen / DeepSeek / vLLM / Ollama），输出根因、影响、排查/修复/验证命令（只展示，不执行）
- **报告**：Markdown + JSON（latest / 时间戳 / 终端一屏摘要），健康评分

**一期红线**：严格只读，绝不创建/修改/删除资源，绝不执行任何命令；LLM 生成的 kubectl 命令仅供人工复制执行。

## 安装

> 完整安装/部署/使用手册见 [docs/INSTALLATION.md](docs/INSTALLATION.md)（本地、镜像、集群 CronJob 分步 + 故障排查）。

### 本地构建

```bash
make build
./bin/k8s-ai version
```

> `make build` 生成**当前操作系统**的原生二进制（Windows=.exe / Linux / macOS）；交叉编译 Linux amd64 用 `make build-linux`（或自行设置 GOOS/GOARCH）。

### Docker 镜像

```bash
make docker VERSION=1.0.0
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
k8s-ai scan --fail-on HIGH               # 达到 HIGH 返回退出码 2（CI 用）
```

## 配置

默认配置 `~/.k8s-ai/config.yaml`，优先级：CLI > 环境变量 > YAML > 默认。

关键项：

| 配置 | 默认 | 说明 |
|---|---|---|
| `kubernetes.kubeconfig/context/namespace` | 空 | 连接集群；空 kubeconfig 走 KUBECONFIG 或 ~/.kube/config |
| `scan.concurrency` / `scan.phase2_concurrency` | 8 / 4 | 采集并发 |
| `scan.max_log_lines` / `max_log_bytes` | 500 / 65536 | 日志上限 |
| `scan.timeout` | 5m | 整次扫描超时（LLM 场景建议 10m） |
| `llm.enabled` / `endpoint` / `model` | true / localhost:8000 / qwen-plus | 大模型 |
| `llm.timeout` | 120s | 单次 LLM 调用超时（思考型模型建议保持） |
| `report.directory` / `format` | ./reports / markdown | 报告输出 |

环境变量：`KUBECONFIG`、`K8S_AI_LLM_ENDPOINT`、`K8S_AI_LLM_API_KEY`、`K8S_AI_LLM_MODEL` 等。

### LLM 模型选型

- 巡检诊断建议用**快速的非思考型模型**（如 qwen-turbo/flash），单 Finding 秒级返回；
- 思考型大模型（如 Qwen3.5-397B）单次诊断约 1.5–2 分钟，需配合 `llm.timeout ≥ 120s`、`scan.timeout 10m`；
- 网关支持时可用 `llm.disable_thinking: true`（发送 `chat_template_kwargs.enable_thinking=false`）提速；
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

`deploy/clusterrole.yaml` 只授权只读动词（get/list/watch + pods/log get），不含 secrets、pods/exec、metrics。架构测试会解析清单断言只读约束。

## 安全

- **四层只读保证**：RBAC 只读动词 → 代码只读门面 → 无 shell/exec 调用 → 架构测试证明
- **永不读取 Secret**；日志/Events/ConfigMap/注解视为不可信数据，先脱敏再使用
- **双重脱敏**：采集边界 + 报告渲染；`api_key` 只在 Authorization 头，错误消息显式剔除
- **LLM 无执行能力**：命令永远是字符串，人工复制执行；一期不实现任何执行器
- **Prompt 注入防护**：不可信数据用定界符包裹并声明"是数据不是指令"

### 安全核对清单（P11 最终确认）

| 检查项 | 保障机制 | 验证方式 |
|---|---|---|
| 无写操作 | RBAC 只读 + Reader 只读门面 + 无 os/exec | `tests/arch` AST 只读扫描 |
| 不访问 Secret | 代码禁止 `Secrets()` 调用 + RBAC 不含 secrets | AST 扫描 + fake Action 断言 |
| 脱敏 | 采集边界 + 报告渲染双重脱敏 | security 单测 + LLM 请求泄密测试 |
| api_key 不泄露 | 仅在 Authorization 头 + 错误显式剔除 | llm httptest |
| LLM 不编造 | Evidence ID 校验 + 命令白名单 | diagnosis 单测 |
| 不执行 LLM 命令 | 命令仅字符串，无执行器 | CommandExecutor 无实现 + AST 扫描 |
| 单资源失败不中断 | collection_errors 隔离 | scanner 错误隔离测试 |
| RBAC 清单只读 | 解析 deploy/clusterrole.yaml 断言 | `tests/arch` RBAC 测试 |

## 故障示例

- **CrashLoopBackOff（配置错误）**：日志关键行直接给出 `nginx: [emerg] host not found in "80x" of the "listen" directive`，LLM 给出根因与修复建议
- **OOMKilled**：证据链含 `lastState.reason=OOMKilled / exitCode=137 / memory.limit`，LLM 输出根因与 `kubectl set resources` 建议
- **RocketMQ topic 不存在导致 panic**：日志关键行提取 `panic: StartConsumer fail ... route info not found`，规则初步判断兜底
- **Service 无 Endpoint**：规则发现 readyEndpoints=0 + selector 匹配数，LLM 结合关联 Pod 崩溃证据定位根因

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

- 单元测试：规则、证据、脱敏、配置优先级、报告渲染、LLM 客户端（httptest）、诊断校验/降级、历史对比、Server API
- 架构测试：只读 AST 扫描、依赖方向、RBAC 清单、泄密、请求量
- 每阶段出口：`go test ./...` + `go vet ./...` 全绿

## 二期预告

二期（未实现）：`k8s-ai chat` 自然语言 Agent、Tool Calling、会话记忆（复用 latest.json 历史差异数据）、安全审批与执行。