# base/ 两种形态共用的前置清单

命名空间、RBAC、平台 ConfigMap、Secret 模板、迁移 Job、后端 Service、报告 PVC 都放在这里，
形态 A 与形态 B 都只 apply 本目录一次，不需要按形态各存一份。

唯一与形态有关的东西在后端 Deployment 里，因此本目录不含 Deployment：
形态 A 用 `../allinone/deployment.yaml`，形态 B 用 `../separated/deployment.yaml`。

本目录的 `secret.example.yaml` / `pvc.yaml` 分别需要填占位值后才可用，见 `../README.md`。
