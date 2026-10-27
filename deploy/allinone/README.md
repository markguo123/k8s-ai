# allinone/ 形态 A：前后端一体化

后端镜像用 `go:embed` 内嵌 `web/dist`，同一个进程、同一个端口同时提供 SPA 与 `/api/v1`，
因此本目录只有两份文件：`deployment.yaml`（内嵌静态托管保持默认开启）与
`ingress.yaml`（单 path `/` 直接打到 `k8s-ai-server:8080`）。

与 `../separated/` 互斥：两者的后端 Deployment 同名 `k8s-ai-server`，只能选一个目录 apply。

apply 顺序：`../base/` 前置与迁移 Job → `deployment.yaml` → `../base/service.yaml` →
`rollout status` → `ingress.yaml`（先替换 host 与 ingressClassName）。
