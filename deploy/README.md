# k8s-ai 部署清单（四期 P6.2 / P6.5）

同一套后端清单支持两种部署形态，差别只在「前端由谁提供」。两种形态的 YAML **分目录存放**，
`base/` 是两者共用的部分，`allinone/` 与 `separated/` 互斥、**只能选一个目录 apply**。

| 形态 | 前端由谁托管 | 需要构建的镜像 | 清单目录 | 入口 |
| --- | --- | --- | --- | --- |
| A 前后端一体化（默认） | 后端进程 `go:embed` 内嵌 SPA | `k8s-ai` | `base/` + `allinone/` | `allinone/ingress.yaml` 单 path `/` |
| B 前后端分离 | 独立 Nginx 镜像 | `k8s-ai` + `k8s-ai-web` | `base/` + `separated/` | `separated/ingress.yaml` 按路径分流 |

形态 B 的后端仍是同一个镜像，只多一条 `K8S_AI_PLATFORM_SERVER_STATIC_ENABLED=false`
关掉内嵌静态托管。两份 `deployment.yaml` 除这条 env 之外必须逐字段一致，由
`tests/arch/deploy_manifest_test.go` 挡住漂移。

## 目录布局

```
deploy/
  base/          两种形态共用的前置清单
  allinone/      形态 A：内嵌 SPA（后端 Deployment + Ingress）
  separated/     形态 B：前后端分离（后端 Deployment + 前端 Deployment/Service + 分流 Ingress + 前端镜像构建文件）
  scan/          可选：一期每日巡检 CronJob（独立 ConfigMap）
  example/       参考与补丁示例，不是清单，不能 apply
  rbac-write.example.yaml  可选：开放写权限的 ClusterRole（跨形态）
```

| 文件 | 产物 | 必需性 |
| --- | --- | --- |
| `base/namespace.yaml` | Namespace `k8s-ai`（PSS warn/audit: restricted） | 必需 |
| `base/serviceaccount.yaml` | SA `k8s-ai` | 必需 |
| `base/clusterrole.yaml` | ClusterRole `k8s-ai-readonly`（只读动词面） | 必需 |
| `base/clusterrolebinding.yaml` | 把只读角色绑到本命名空间的 SA | 必需 |
| `base/configmap.yaml` | `k8s-ai-config`：`platform:` 静态段 + 落盘目录 | 必需 |
| `base/secret.example.yaml` | `k8s-ai-store` / `k8s-ai-enc` / `k8s-ai-admin` / `k8s-ai-llm` | 必需（复制后填值，勿直接 apply） |
| `base/db-migrate-job.yaml` | Job `k8s-ai-db-migrate`（`k8s-ai db migrate`） | 必需（首次与每次升级） |
| `base/service.yaml` | Service `k8s-ai-server`:8080 | 必需（两形态共用） |
| `base/pvc.yaml` | PVC `k8s-ai-data`（5Gi RWO） | 可选（报告留档或 SQLite 兜底） |
| `allinone/deployment.yaml` | Deployment `k8s-ai-server`（内嵌 SPA） | 形态 A |
| `allinone/ingress.yaml` | Ingress `k8s-ai`（单入口，含 SSE 关缓冲） | 形态 A |
| `separated/deployment.yaml` | Deployment `k8s-ai-server`（纯 API） | 形态 B |
| `separated/web-deployment.yaml` | Deployment `k8s-ai-web`（Nginx + dist） | 形态 B |
| `separated/web-service.yaml` | Service `k8s-ai-web`:80 | 形态 B |
| `separated/ingress.yaml` | Ingress `k8s-ai-api` + `k8s-ai-web`（同域分流） | 形态 B |
| `separated/web.Dockerfile` / `separated/nginx.conf` | 前端镜像构建（与仓库根 `Dockerfile` 互不依赖） | 形态 B |
| `scan/configmap.yaml` + `scan/cronjob.yaml` | 一期每日巡检通道（`k8s-ai-scan-config`） | 可选 |
| `example/secret-mysql.example.yaml` | MySQL DSN 示例 + DBA 前置 SQL | 参考 |
| `example/env-overrides.example.yaml` | 部署期种子态/密钥态环境变量示例 | 参考（patch） |
| `example/deployment-reports-pvc.yaml` | `/reports` 换 PVC 的 patch | 可选（patch） |
| `example/networkpolicy.yaml` | 容器出站白名单样例（占位地址，换成现场地址再 apply） | 可选 |

> 不要整目录递归 apply：`deploy/example/` 下是 `kubectl patch --patch-file` 用的补丁，
> 刻意不写 `apiVersion`/`kind`，`kubectl apply -R -f deploy/` 会在它这里报错；
> `allinone/` 与 `separated/` 的同名 Deployment 也只能二选一。按下面顺序逐文件 apply。

## 前置

1. 构建并推送镜像（构建与推仓由部署方执行，清单里不写私有仓库地址）。后端有两档镜像，
   构建阶段完全相同，只差运行阶段（P6.3 / D-33）：默认档内置 bash/jq/kubectl，容器内排障
   能力与本地 CLI 对齐；无 shell 变体给安全基线禁止容器内解释器的现场，配 `ConfigMap` 里
   `platform.exec.shell_enabled: false`。

   ```bash
   # 后端默认档（形态 A 已内嵌前端产物；形态 B 复用同一镜像）
   docker build --build-arg VERSION=4.0.0 --build-arg COMMIT=<sha> -t <registry>/k8s-ai:4.0.0 .
   # 后端无 shell 变体（配 platform.exec.shell_enabled=false 使用）
   docker build -f Dockerfile.distroless --build-arg VERSION=4.0.0 --build-arg COMMIT=<sha> \
     -t <registry>/k8s-ai:4.0.0-noshell .
   # 仅形态 B 追加前端镜像
   docker build -f deploy/separated/web.Dockerfile --build-arg VERSION=4.0.0 -t <registry>/k8s-ai-web:4.0.0 .
   ```

   默认档的 `kubectl` 有两条投递路径，**离线投递优先**（细节见 `deploy/kubectl/README.md`）：

   1. **离线投递（气隙/内网交付主路径，构建全程零外网）**：把 `kubectl` 放进 `deploy/kubectl/`
      （同名再放一份内容为裸 hash 的 `kubectl.sha256` 就会一并校验），不需要任何 build-arg。
   2. **mirror 下载**：`deploy/kubectl/` 为空时按 `KUBECTL_VERSION`/`KUBECTL_DOWNLOAD_BASE`
      下载并 `sha256sum -c` 校验。base 的目录结构须与 `dl.k8s.io/release` 一致，国内无出口到
      `dl.k8s.io` 时可用 `https://files.m.daocloud.io/dl.k8s.io/release`。

   `KUBECTL_VERSION` 按现场集群次版本调（官方支持 ±1 次版本偏差）；置空且不投递则不内置 kubectl，
   只读链路不受影响。Go 依赖代理同理：`--build-arg GOPROXY=https://goproxy.cn,direct`。
   无 shell 变体的基座在 `gcr.io`，内网需先同步到内网 registry 再构建。

   构建后自查（P6.3 §8.3）：

   ```bash
   docker run --rm --entrypoint /bin/bash <registry>/k8s-ai:4.0.0 -lc \
     'id -u; command -v bash jq kubectl; kubectl version --client'   # 期望 65532 + 三个路径
   ```

2. 数据库由 DBA 预建（产品不执行 `CREATE DATABASE`）：库名建议 `k8s_ai`、字符集 `utf8mb4`，
   账号需要 `SELECT/INSERT/UPDATE/DELETE/CREATE/ALTER/INDEX/REFERENCES`。DSN 必须带 `parseTime=true`。
3. 生成凭据加密密钥（64 位 hex）：`openssl rand -hex 32`。轮换会使已加密的库内存量凭据不可解，
   参见 `base/secret.example.yaml` 的轮换须知。

## 形态 A：前后端一体化

```bash
kubectl apply -f deploy/base/namespace.yaml
kubectl apply -f deploy/base/serviceaccount.yaml
kubectl apply -f deploy/base/clusterrole.yaml
kubectl apply -f deploy/base/clusterrolebinding.yaml
# 复制 base/secret.example.yaml，填好四个 Secret 的占位值后 apply（store 与 enc 必填）
kubectl apply -f <你的>/secrets.yaml
kubectl apply -f deploy/base/configmap.yaml

# 结构升级：Job spec 不可变，重跑前先删旧 Job
kubectl -n k8s-ai delete job k8s-ai-db-migrate --ignore-not-found
kubectl -n k8s-ai apply -f deploy/base/db-migrate-job.yaml
kubectl -n k8s-ai wait --for=condition=complete job/k8s-ai-db-migrate --timeout=300s

kubectl -n k8s-ai apply -f deploy/allinone/deployment.yaml
kubectl -n k8s-ai apply -f deploy/base/service.yaml
kubectl -n k8s-ai rollout status deploy/k8s-ai-server --timeout=180s

# 替换 host 与 ingressClassName 后
kubectl apply -f deploy/allinone/ingress.yaml
```

首启用 `platform.auth.admin_username` + `K8S_AI_ADMIN_PASSWORD` 登录，初始化完成后可以把
`k8s-ai-admin` Secret 与对应环境变量摘掉。

## 形态 B：前后端分离

`base/` 前置与迁移 Job 完全同形态 A，只是工作负载换成 `separated/`：

```bash
# （前置同形态 A：namespace / rbac / secrets / configmap / 迁移 Job 已 Succeeded）
kubectl -n k8s-ai apply -f deploy/separated/deployment.yaml   # 后端纯 API（已关内嵌 SPA）
kubectl -n k8s-ai apply -f deploy/base/service.yaml
kubectl -n k8s-ai apply -f deploy/separated/web-deployment.yaml
kubectl -n k8s-ai apply -f deploy/separated/web-service.yaml
kubectl -n k8s-ai rollout status deploy/k8s-ai-server --timeout=180s
kubectl -n k8s-ai rollout status deploy/k8s-ai-web  --timeout=180s

# 替换 host 与 ingressClassName 后（替代 allinone/ingress.yaml，不要两个都留）
kubectl apply -f deploy/separated/ingress.yaml
```

前端只用相对路径 `/api/v1`，所以形态 B 必须同域按路径分流（`separated/nginx.conf` 里另留了一段
API 反代兜底，仅在不走 Ingress、直接暴露 Nginx 时才用得上）。

从形态 A 切到形态 B：`kubectl -n k8s-ai apply -f deploy/separated/deployment.yaml` 就地改同名
Deployment 即可，不需要先删；反向切换同理。

## 可选件

- 报告跨重启保留：先 `kubectl apply -f deploy/base/pvc.yaml`（改 `storageClassName`），再
  `kubectl -n k8s-ai patch deploy k8s-ai-server --type=strategic --patch-file deploy/example/deployment-reports-pvc.yaml`。
- 部署期种子配置（模型地址/模型名/默认命名空间）：`deploy/example/env-overrides.example.yaml`。
  密钥态变量（DSN、`K8S_AI_LLM_API_KEY`）恒优先，种子态只在配置库该键为空时生效一次。
- 开放写操作：`kubectl apply -f deploy/rbac-write.example.yaml`，并在配置中心按命名空间授权；
  镜像内无 shell/kubectl，写执行只走 client-go。
- 每日巡检：`kubectl apply -f deploy/scan/configmap.yaml -f deploy/scan/cronjob.yaml`（先把
  `scan/configmap.yaml` 里的 `REPLACE_WITH_LLM_HOST` / `REPLACE_WITH_MODEL_NAME` 换成现场值，
  或改 `llm.enabled: false`）。

## 换命名空间

清单里 namespaced 对象都显式写了 `metadata.namespace: k8s-ai`，`kubectl -n` 不会覆盖它。
换命名空间要把 `namespace: k8s-ai` 整串替换（`ClusterRoleBinding` 的 `subjects[].namespace`
同形式，一起改）：

```bash
# Linux / macOS
find deploy -name '*.yaml' -exec sed -i 's/^namespace: k8s-ai$/namespace: my-ns/' {} +
```

```powershell
# Windows PowerShell
Get-ChildItem deploy -Recurse -Filter *.yaml | ForEach-Object {
  $p = $_.FullName
  [IO.File]::WriteAllText($p, ([IO.File]::ReadAllText($p) -replace '(?m)^namespace: k8s-ai$', 'namespace: my-ns'))
}
```

## apply 前必须替换的占位值

| 位置 | 占位值 |
| --- | --- |
| `base/secret.example.yaml`、`example/secret-mysql.example.yaml` | `REPLACE_ME` |
| `allinone/ingress.yaml`、`separated/ingress.yaml` | `k8s-ai.example.com`、`ingressClassName: nginx` |
| `base/pvc.yaml` | `REPLACE_WITH_STORAGE_CLASS`（留占位会让 PVC 一直 Pending） |
| `scan/configmap.yaml` | `REPLACE_WITH_LLM_HOST`、`REPLACE_WITH_MODEL_NAME` |
| 两份 `deployment.yaml`、`base/db-migrate-job.yaml`、`separated/web-deployment.yaml` | `image:` 换成现场仓库地址与 TAG |

## 部署后自检

```bash
kubectl -n k8s-ai get deploy,svc,ingress,job
kubectl -n k8s-ai logs deploy/k8s-ai-server | head -50
kubectl -n k8s-ai exec deploy/k8s-ai-server -- version    # 镜像无 shell，直接跑子命令
curl -s https://<host>/version
```

Chat 页发一条消息，确认回答是逐 token 出现而不是一次性吐出（一次性吐出说明中间层还在缓冲）。

## 常见故障

| 现象 | 原因与处理 |
| --- | --- |
| `/readyz` 503、Pod 一直 not ready | 迁移未到位或 Store 不通：先跑迁移 Job，再核对 `k8s-ai-store` 的 DSN（`parseTime=true`、库已建、网络可达） |
| 启动即退出并提示 DSN | 没建 `k8s-ai-store` Secret，或 `driver: mysql` 却没注入 DSN |
| 集群端点列表 501 / 敏感配置保存失败 | `K8S_AI_ENC_KEY` 未注入或长度不对（需 32 字节：64 位 hex 或 base64） |
| 页面 404 但 API 正常 | 形态 A 用了未内嵌 dist 的镜像；或形态 B 忘了用 `separated/deployment.yaml` / 没部署 web |
| 形态 B 页面能开但接口 404 | `separated/ingress.yaml` 只 apply 了 web 那半，或 `/api/v1` 分流规则被覆盖 |
| SSE 攒到最后一次性输出 | Ingress 缓冲没关：核对 `proxy-buffering: "off"` 与读超时 |
| PVC 一直 Pending | `storageClassName` 还是占位值，或存储类不支持 RWO 供给 |
| 事件里有 restricted warning | 预期内的 warn（未 enforce）；确认无告警后可自行把 `pod-security.kubernetes.io/enforce` 升上来 |
