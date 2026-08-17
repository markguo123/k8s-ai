# 版本约定（Versioning）

## 当前版本

- **v1.0.0**（2026-08-17）：一期 1.1 + 1.2 交付（巡检/排查/分析/报告/LLM 诊断/部署/Server/历史对比）。

## 版本阶梯

| 版本区间 | 含义 |
|---|---|
| v1.0.0（基线） | 一期交付基线（原"一期 1.1 + 1.2"） |
| v1.0.1 ~ v1.0.10（迭代） | 一期迭代：每发布一版 patch 位 +1 |
| v2.0.0 | 二期入口（chat Agent / Tool Calling / 会话记忆 / 审批执行） |
| v2.0.1 ~ | 二期迭代 |

说明：
- 本项目 1.0.0段采用"里程碑阶梯"：patch 位同时承担功能里程碑编号；每个版本的准确含义见 CHANGELOG.md。
- 后续如需正式发布，遵循严格 SemVer（feature=minor、fix=patch、breaking=major）。

## 发布流程（每次发版）

1. 更新 `Makefile` 的 `VERSION`（默认值）；
2. 在 `CHANGELOG.md` 顶部新增版本条目（Added/Changed/Fixed 结构）；
3. 更新 `README.md` 顶部"当前版本"行；
4. 本地验证：`make build && make test && make vet && make lint`；
5. 提交并打 tag：`git tag v1.0.1 && git push origin v1.0.1`。

## 版本与文档术语映射

- 文档中的"一期" = v1.0.0（及其迭代 1.0.0~1.0.10）；
- 文档中的"二期" = v2.0.0（及其迭代）；
- 文档中的 "1.1 / 1.2" 是一期内部里程碑编号（1.1=核心巡检诊断，1.2=Server+历史对比）。