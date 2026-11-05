# scan/ 一期每日巡检 CronJob（可选通道）

这条通道与平台 Chat 无关：CronJob 用独立 ConfigMap `k8s-ai-scan-config`（引擎键
`kubernetes` / `llm` / `scan` / `report` / `agent` / `rules`），不读平台的 `k8s-ai-config`，
也不需要 Store DSN。不想要每日巡检就不 apply 本目录。

`configmap.yaml` 里的 `REPLACE_WITH_LLM_HOST` / `REPLACE_WITH_MODEL_NAME` 是占位值，
apply 前换成现场推理服务地址与模型名，或直接把 `llm.enabled` 置 false。

`cronjob.yaml` 的报告卷指向 PVC `k8s-ai-data`，跨重启留档需先 apply `../base/pvc.yaml`
并改掉 `storageClassName`。`spec.timeZone` 需要 Kubernetes ≥ 1.27。
