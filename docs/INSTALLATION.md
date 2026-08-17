# k8s-ai 安装部署手册（一期）

> 版本：1.0.0（一期 MVP）
> 适用：本地命令行使用 + Kubernetes 集群内 CronJob 每日巡检
> 说明：一期严格只读；LLM 生成的 kubectl 命令仅供人工复制执行

---

## 1. 前置要求

| 项 | 要求 | 说明 |
|---|---|---|
| 操作系统 | Windows / Linux / macOS | 二进制或 Docker 镜像均可 |
| Go（源码构建） | ≥ 1.26（go.mod 指定 1.26.0） | 仅源码构建需要；直接使用镜像不需要 |
| Kubernetes 集群 | ≥ 1.27（CronJob `timeZone` 字段需要 1.27+） | 已实测 1.28 |
| 访问凭证 | kubeconfig 或集群内 ServiceAccount | 只读权限即可（见 §4.4） |
| LLM 网关 | OpenAI Compatible Chat Completions | OpenAI / Qwen / DeepSeek / vLLM / Ollama |
| Docker（可选） | 构建镜像时需要 | 本机需启动 Docker Desktop/daemon |

---

## 2. 安装方式

### 2.1 源码构建（本地命令行）

```bash
# 1) 获取源码后进入项目根目录
cd k8s-ai

# 2) 构建二进制
make build
# 产物：bin/k8s-ai(.exe)

# 3) 验证
./bin/k8s-ai version
# 期望输出：k8s-ai dev (commit none, built unknown)
```

### 2.2 Docker 镜像构建

```bash
# 构建（多阶段：golang:1.26-alpine → distroless static nonroot）
make docker VERSION=1.0.0

# 推送私有仓库（按需）
docker tag k8s-ai:1.0.0 registry.example.com/k8s-ai:1.0.0
docker push registry.example.com/k8s-ai:1.0.0
```

镜像说明：基于 `gcr.io/distroless/static-debian12:nonroot`，非 root（uid 65532）、无 shell、小体积；系统提示词已 go:embed 内嵌，无需额外挂载。

### 2.3 升级

- 本地：重新 `make build` 或拉新镜像；
- 集群内：更新 CronJob 的 image 标签（`kubectl edit cronjob k8s-ai-daily -n k8s-ai`）或重新 apply 新清单；
- 报告 PVC 保留历史报告，升级不丢失。

---

## 3. 配置

### 3.1 生成配置

```bash
./bin/k8s-ai config init
# 默认生成 ~/.k8s-ai/config.yaml（可用 --config 指定路径，--force 覆盖）
```

### 3.2 配置项详解

```yaml
kubernetes:
  kubeconfig: ""      # 留空 = 使用 KUBECONFIG 环境变量或默认 ~/.kube/config
  context: ""         # kubeconfig 上下文；留空用当前
  namespace: ""       # 留空 = 全集群；非空 = 默认只扫该命名空间
  timeout: 30s        # 单次 API 请求超时
  qps: 20             # API Server 访问 QPS 上限
  burst: 40           # 突发上限

llm:
  enabled: true       # 关闭则只出规则结论
  endpoint: "http://localhost:8000/v1"   # OpenAI Compatible 基础地址（自动拼 /chat/completions）
  api_key: ""         # 也可用环境变量 K8S_AI_LLM_API_KEY 注入
  model: "qwen-plus"  # 建议：快速非思考型模型；思考型大模型较慢
  temperature: 0.1
  max_tokens: 4096
  max_input_tokens: 8192    # 单 Finding 上下文上限
  max_total_tokens: 32768   # 诊断阶段总预算
  max_findings: 30          # 最多送诊 Finding 数
  timeout: 120s             # 单次 LLM 调用超时
  disable_thinking: false   # true=发送 enable_thinking=false（部分网关支持，可显著提速）

scan:
  concurrency: 8            # Phase1 并发
  phase2_concurrency: 4     # Phase2 日志采集并发
  collect_logs: true
  collect_previous_logs: true
  collect_events: true
  max_log_lines: 500        # 每容器日志行数上限
  max_log_bytes: 65536      # 单容器日志字节上限
  max_log_line_bytes: 1024  # 单行截断
  pod_logs_timeout: 30s     # 单 Pod 日志超时
  timeout: 10m              # 整次扫描超时（LLM 场景建议 10m）

report:
  directory: "./reports"    # 报告目录（集群内 CronJob 用 /reports）
  format: markdown

rules:
  enabled: []               # 非空则只启用这些规则
  disabled: []              # 禁用指定规则
```

### 3.3 环境变量与优先级

优先级：**CLI > 环境变量 > YAML > 默认**。

常用环境变量：`KUBECONFIG`、`K8S_AI_LLM_ENDPOINT`、`K8S_AI_LLM_API_KEY`、`K8S_AI_LLM_MODEL`、`K8S_AI_LLM_TIMEOUT`、`K8S_AI_SCAN_TIMEOUT`、`K8S_AI_LLM_DISABLE_THINKING`（true/false）。

### 3.4 校验配置与连通性

```bash
./bin/k8s-ai config validate --config ~/.k8s-ai/config.yaml
# 期望：config OK: server version v1.28.13
```

---

## 4. Kubernetes 集群内部署（每日巡检 CronJob）

### 4.1 清单总览

`deploy/` 目录包含：

| 文件 | 资源 | 用途 |
|---|---|---|
| namespace.yaml | Namespace k8s-ai | 部署命名空间 |
| serviceaccount.yaml | ServiceAccount k8s-ai | 运行身份 |
| clusterrole.yaml | ClusterRole k8s-ai-readonly | 只读权限 |
| clusterrolebinding.yaml | ClusterRoleBinding | SA 绑定只读角色 |
| configmap.yaml | ConfigMap k8s-ai-config | 非敏感配置 |
| secret.example.yaml | Secret k8s-ai-llm（示例） | LLM api_key（需替换） |
| pvc.yaml | PVC k8s-ai-reports | 报告持久化 1Gi |
| cronjob.yaml | CronJob k8s-ai-daily | 每日 08:00 巡检 |

> deployment.yaml / service.yaml（Server 模式，一期 1.2）见 §4.7。

### 4.2 分步安装

```bash
# 1) 命名空间 + 身份 + 只读 RBAC
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/serviceaccount.yaml
kubectl apply -f deploy/clusterrole.yaml
kubectl apply -f deploy/clusterrolebinding.yaml

# 2) 配置（LLM 端点/模型/扫描参数）
kubectl apply -f deploy/configmap.yaml

# 3) LLM api_key（复制示例并替换）
cp deploy/secret.example.yaml deploy/secret.yaml
# 编辑 deploy/secret.yaml：把 stringData.api_key 的 REPLACE_ME 换成真实 key
kubectl apply -f deploy/secret.yaml

# 4) 报告 PVC
kubectl apply -f deploy/pvc.yaml

# 5) 每日巡检 CronJob
kubectl apply -f deploy/cronjob.yaml
```

### 4.3 安装后验证

```bash
kubectl get cronjob -n k8s-ai
# NAME           SCHEDULE      SUSPEND   ACTIVE
# k8s-ai-daily   0 8 * * *     False     0

# 手动触发一次立即巡检（可选，只读）
kubectl create job --from=cronjob/k8s-ai-daily k8s-ai-manual -n k8s-ai
kubectl logs job/k8s-ai-manual -n k8s-ai -f
# 期望看到：Phase1 采集完成 → LLM 诊断进行中 → LLM 诊断完成 → 终端摘要

# 查看报告
kubectl exec -n k8s-ai $POD -- cat /reports/latest.md | head -50
```

### 4.4 RBAC 说明（deploy/clusterrole.yaml）

- 只授权 `get/list/watch` + `pods/log get`；**不含** secrets、pods/exec、pods/portforward、metrics；
- 一期为全集群只读 ClusterRole（ClusterRoleBinding）；如需最小化，可改为仅巡检特定 namespace 的 Role；
- 清单由架构测试解析断言（`go test ./tests/arch/`），误加写权限会测试失败。

### 4.5 CronJob 特性

- 调度：`0 8 * * *`，`timeZone: Asia/Shanghai`；
- `concurrencyPolicy: Forbid`：上一次未跑完不并发；
- `restartPolicy: OnFailure` + `backoffLimit: 2`；
- 非 root：`runAsUser/Group: 65532`、`fsGroup: 65532`、drop ALL capabilities；
- 报告写入 PVC `/reports`（latest.md / latest.json / 时间戳 .md）；
- LLM api_key 通过 Secret 环境变量注入（`optional: true`，未配置时 LLM 自动降级为规则结论）。

### 4.6 Server 模式部署（一期 1.2，HTTP API）

可选部署：`k8s-ai server` 提供健康检查、版本与异步扫描 API（供平台/二期 Agent 调用）。

```bash
# 部署 Server（需先完成 4.2 的 SA/RBAC/配置/Secret）
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml

# 验证
kubectl get deploy,svc -n k8s-ai
```

API：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | /healthz | 存活探针 |
| GET | /readyz | 就绪探针 |
| GET | /version | 版本信息 |
| POST | /api/v1/scans | 创建异步扫描（body=ScanOptions），返回 scanId；已有任务运行时返回 409 |
| GET | /api/v1/scans/{id} | 任务状态 pending/running/succeeded/failed + result |

注意：与 CronJob 共用报告 PVC 时需 RWX 存储类；否则给 Server 单独挂载或不同卷。

### 4.7 历史对比（日报趋势）

每次扫描自动读取报告目录上一份 `latest.json`，按 Finding 指纹对比输出：

- **新增 / 持续 / 恢复** 三类问题；报告新增"历史对比"段，终端摘要显示计数；
- `latest.json` 的 `history` 字段为二期 Agent 会话记忆契约（scan_cluster Tool 自动携带，无需手动挂载）。

### 4.8 卸载

```bash
kubectl delete -f deploy/cronjob.yaml
kubectl delete -f deploy/pvc.yaml        # 报告数据随之删除（如需保留先备份）
kubectl delete -f deploy/secret.yaml
kubectl delete -f deploy/configmap.yaml
kubectl delete -f deploy/clusterrolebinding.yaml
kubectl delete -f deploy/clusterrole.yaml
kubectl delete -f deploy/serviceaccount.yaml
kubectl delete -f deploy/namespace.yaml
```

---

## 5. 使用指导

### 5.1 命令一览

| 命令 | 说明 |
|---|---|
| `k8s-ai scan` | 全集群巡检，默认写 reports/latest.md + latest.json |
| `k8s-ai scan -n <ns>` | 只巡检某命名空间，终端直出（不写文件） |
| `k8s-ai scan pod <name> -n <ns>` | 单 Pod 诊断（含关联 Deployment/Service/Node 证据） |
| `k8s-ai scan --report-mode daily` | 日报模式，追加时间戳文件 |
| `k8s-ai scan --report-mode none --format json` | 终端输出 JSON（脚本用） |
| `k8s-ai scan --fail-on HIGH` | 存在 HIGH 及以上退出码 2（CI 用） |
| `k8s-ai config init / validate` | 生成/校验配置 |
| `k8s-ai version` | 版本 |
| `k8s-ai server --addr :8080` | 启动最小化 HTTP 服务（1.2） |

### 5.2 参数详解

| 参数 | 默认 | 说明 |
|---|---|---|
| `--kubeconfig` | 配置或默认 | 指定 kubeconfig |
| `--context` | 当前 | 指定上下文 |
| `-n, --namespace` | 全集群 | 目标命名空间 |
| `--since <dur>` | 无 | 日志时间窗口（如 1h） |
| `--format` | markdown | 终端完整报告格式 markdown/json/yaml |
| `--report-mode` | none(目标)/latest(全集群) | none=仅终端 / latest=latest 文件 / daily=加时间戳 |
| `--verbose` | false | 终端输出完整报告（默认一屏摘要） |
| `--fail-on <sev>` | 无 | 达到严重级退出码 2 |
| `--config` | ~/.k8s-ai/config.yaml | 配置文件路径 |

### 5.3 输出解读

**终端一屏摘要**：

```text
k8s-ai scan　Scope: cluster
集群健康：65/100  ██████░░░░
CRITICAL 0 | HIGH 2 | MEDIUM 1 | LOW 0 | INFO 0

重点问题：
🟠 HIGH ns/deploy：副本数不匹配：0/1 ready
　现象：desiredReplicas=1；readyReplicas=0
　根因：...（LLM）或 初步判断（规则）+ 日志关键行
　建议：kubectl ...（风险 HIGH）
```

**报告文件**：`latest.md`（人读）、`latest.json`（机器读，含 findings 指纹 + diagnoses，二期 Agent 记忆契约）。

**退出码**：0 成功；1 执行错误；2 `--fail-on` 达到阈值。

### 5.4 故障示例（识别效果）

| 场景 | 工具输出 |
|---|---|
| nginx 配置错误 | 日志关键行：`[emerg] host not found in "80x" of the "listen" directive` |
| OOM | 证据：OOMKilled / exitCode 137 / memory.limit |
| RocketMQ topic 缺失 | 日志关键行：`panic: StartConsumer fail ... route info not found` |
| Service 无后端 | 规则：readyEndpoints=0 + selector 匹配数；LLM 结合关联 Pod 崩溃证据定位 |

---

## 6. 故障排查（运维）

| 现象 | 排查 |
|---|---|
| LLM 一直"诊断中" | 思考型大模型单次 1.5-2 分钟属正常；确认 `llm.timeout`/`scan.timeout` 足够（10m）；或换快速模型 / `disable_thinking` |
| 报告标注"LLM 分析不可用" | LLM 网关不可达/超时/输出校验失败；scan 不失败，规则初步判断已兜底 |
| CronJob 未执行 | `kubectl describe cronjob -n k8s-ai`；检查 schedule/timeZone 与集群版本（timeZone 需 1.27+） |
| RBAC 403 | `kubectl auth can-i list pods --as=system:serviceaccount:k8s-ai:k8s-ai -n <ns>`；确认 clusterrolebinding 已应用 |
| 报告 PVC 满 | `kubectl describe pvc -n k8s-ai`；扩容 storage 或定期清理旧时间戳文件 |

---

## 7. 安全注意事项

- 一期严格只读：程序不创建/修改/删除任何资源，不执行任何命令；
- LLM 生成的 kubectl 命令**只展示**，请人工核对后执行；
- api_key 仅存在于 Secret/环境变量，永不写入 ConfigMap、日志或报告；
- 日志/Events/ConfigMap/注解视为不可信数据，先脱敏再分析；
- 部署前建议执行 `go test ./...` 与 `go test ./tests/arch/` 验证只读约束。