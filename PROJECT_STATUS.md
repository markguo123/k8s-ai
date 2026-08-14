# k8s-ai 项目状态

> 维护规则：每完成一个阶段（P3/P4/P5/一期终版），刷新下方"已完成模块"与"进行中"，并同步刷新 TECHNICAL_GUIDE.md 的第二章与第五章。

## 已完成模块

- [x] P0 工程与配置地基（go.mod / model / config / 只读客户端 / CLI 骨架）
- [x] P1 采集 Phase1（17 类资源 list + events 索引 + worker pool + 系统组件探测）
- [x] P2 采集 Phase2 + 采集边界脱敏（日志/历史日志上限、超时、并发、security 包）
- [x] P3 关联索引（Pod→Owner→Node / Pod→PVC→PV→SC / Service→EndpointSlice→Pod + 事件索引）
- [x] P4 规则引擎（13 条规则）+ Phase2 接入主流程 + 证据链（指纹/严重级/Correlated）
- [x] P5 健康评分 + 报告层（Markdown/JSON/YAML 渲染、latest/daily/none 模式、latest.json 记忆契约）
- [x] P9.1 提前实现：`scan pod <name> -n <ns>` 单 Pod 巡检报告（Scope 标注、证据链、日志）；命名空间/单 Pod 报告范围修复
- [x] P6 LLM 客户端（OpenAI Compatible、重试/429/超时、prompt go:embed、api_key 脱敏）
- [x] P7 诊断编排（预算裁剪、JSON Schema/Evidence ID/命令校验、修复重试、失败降级）
- [x] P8 报告可读性优化（终端一屏摘要、日志压缩 -91%、严重级图标/健康条/组件状态、--verbose）
- [x] 生成 TECHNICAL_GUIDE.md 技术手册

## 进行中

- [ ] P10 部署交付（Dockerfile / deploy RBAC / CronJob / README）→ P11 加固收尾

## 下一步

P6-P7（LLM 诊断）→ P8（报告完善）→ P9（CLI 收口：scan pod / -n 简写）

## 已知事项

- go.mod 模块路径暂为 `github.com/k8s-ai/k8s-ai`（仓库地址确定后可改）
- `go test -race` 需要 CGO（本机暂无 gcc），计划 P11 加固阶段处理
- Phase2 深度采集已随 P4 接入主流程（规则命中自动取日志并挂证据）
- LLM 已与真实网关（Qwen3.5-397B 思考型模型）联调通过：单 Finding 约 1.5-2 分钟，建议 llm.timeout ≥ 120s、scan.timeout 10m；扫描过程有进度日志，LLM 超时自动降级不失败
- 诊断增强：关联异常 Pod 证据补全、LLM 降级时规则初步判断 + 日志关键行提取、日志证据保留尾部；实测可自动定位 RocketMQ topic 路由不存在导致的 panic 根因
- LLM 尚未编码（P6）；规则引擎与报告基础已完成（P4/P5）
