# example/ 参考示例与补丁（不是清单）

本目录的文件**不能直接 `kubectl apply -R -f`**：`deployment-*.yaml` 与
`env-overrides.example.yaml` 是 strategic merge patch，刻意不写 `apiVersion` / `kind`，
只能用 `kubectl patch --type=strategic --patch-file <file>` 打到已存在的 Deployment 上。

| 文件 | 用法 |
| --- | --- |
| `secret-mysql.example.yaml` | MySQL DSN Secret 示例 + DBA 前置 SQL，与 `../base/secret.example.yaml` 里的 `k8s-ai-store` 二选一 |
| `env-overrides.example.yaml` | 追加部署期环境变量（密钥态 / 种子态两档语义） |
| `deployment-reports-pvc.yaml` | 把 `/reports` 从 emptyDir 换成 PVC |
| `networkpolicy.yaml` | 完整资源（可直接 apply）：容器出站白名单样例，ipBlock 是文档保留地址占位，换成现场 API Server / LLM 网关 / MySQL 地址后再用；集群 CNI 不执行 NetworkPolicy 时 apply 了也不生效 |

形态 B 的「后端只跑 API」不需要补丁：直接用 `../separated/deployment.yaml` 即可。
这里是占位值与示例口径，不代表产品默认配置，也不含任何现场地址与口令。
