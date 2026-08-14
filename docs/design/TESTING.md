# k8s-ai 一期测试架构设计

- 版本：v1.0
- 日期：2026-08-12
- 状态：待评审

## 1. 测试分层

| 层 | 工具 | 覆盖 |
|---|---|---|
| 单元测试 | `go test` + table-driven + fake client | model/config/scanner/rule/evidence/diagnosis/llm/report/security |
| 架构测试 | `tests/arch`（AST + YAML 解析 + fake ActionRecorder） | 只读/RBAC/依赖方向/禁项 |
| 泄密测试 | fake 集群 + httptest LLM 捕获请求体 | Secret/敏感值零泄漏 |
| 契约测试 | golden files | prompt、JSON schema、Markdown/JSON 报告 |
| 集成测试（可选） | envtest（`//go:build integration` tag） | 真实 apiserver 的 list/logs 行为 |

## 2. 单元测试矩阵

| 包 | 必测场景 | 方法 |
|---|---|---|
| model | 指纹稳定（含 owner 归一化）、严重级枚举、Budget | 表驱动 |
| config | 优先级 CLI>ENV>YAML>默认、`~` 展开、非法值 | 临时文件 + env 组合 |
| kubernetes | kubeconfig/InCluster 选择、QPS/Burst 注入、Reader 方法映射 | fake client + 可注入 rest.Config |
| scanner | Phase1 请求数、错误隔离、events 索引、Phase2 上限/超时 | fake client + ActionRecorder |
| correlation | owner 链、PVC 链、Service-selector 匹配 | 构造快照 |
| rule | 13+ 规则命中/不命中、严重级加权、correlated 标记 | 表驱动 |
| evidence | 排序、截断、ID 稳定、指纹签名 | 表驱动 |
| security | 每个脱敏模式、敏感键、幂等 | 表驱动 |
| diagnosis | 预算裁剪、JSON 解析、校验链、降级路径 | httptest + mock |
| llm | 200/500/429/超时/坏 JSON、Retry-After、api_key 不落日志 | httptest |
| report | Markdown/JSON/YAML golden、命名、二次脱敏、评分公式 | golden files |
| service | 全链路编排、LLM 关闭时降级、ctx 取消 | 全部 mock 注入 |
| cli | flag 映射、退出码 0/1/2、`--fail-on` | cobra SetArgs + 输出捕获 |

## 3. 架构测试（tests/arch）

### 3.1 AST 只读扫描

遍历 `internal/**/*.go`（排除 `_test.go`），用 `go/ast` 断言：

```text
- 无 import "os/exec"
- 无 import k8s.io/client-go/kubernetes/fake
- 无 dynamic.Interface 使用
- 无 clientset 方法调用：Create/Update/Patch/Delete/Apply/Replace/Scale/Exec
- 无对 secrets 资源的任何调用
- 无 CommandExecutor.Execute 调用
```

### 3.2 RBAC 清单测试

解析 `deploy/clusterrole.yaml`（yaml → struct），断言：

```text
verbs ⊆ {get, list, watch}
resources 不含 secrets、pods/exec、pods/portforward、pods/attach
无 metrics.k8s.io（1.1）
```

### 3.3 依赖方向测试

扫描 import 图，断言禁止边不存在：

```text
任何包 → cli
model → 任何内部包
kubernetes → rule/evidence/diagnosis/llm/report/cli
cli → scanner/kubernetes/llm/diagnosis/report
```

### 3.4 只读行为测试（fake ActionRecorder）

构造含 Secret 与异常 Pod 的 fake 集群，跑完整 `ScanService.Run`，断言：

```text
actions 全部 ∈ {list, get, get-logs}
无 secret 请求
无 create/update/patch/delete/exec
```

## 4. 泄密测试

1. **Secret 泄密测试**：fake 集群 Secret 值 `s3cr3t-pass`；断言报告、LLM 请求体、日志输出中零出现。
2. **LLM 请求泄密测试**：httptest LLM server 记录每个请求体；断言不包含：api_key、密码模式、token、私钥块、敏感注解值。
3. **日志泄密测试**：捕获 slog 输出，断言 api_key/敏感值不出现。

## 5. Kubernetes 请求数量测试

fake client + ActionRecorder，构造 1000 Pod / 50 namespace / 7 异常 Pod：

```text
断言 Phase1 list 请求数 ≈ 资源类型数 + namespace 数（不含分页附加）
断言 Phase2 get-logs 请求数 ≤ 异常容器数 × 2
断言不存在按 Pod 循环 list events 的行为（N+1 回归保护）
```

## 6. 配置优先级与 CLI 测试

- 配置：同一键分别设默认/YAML/ENV/flag，断言合并结果与优先级。
- CLI：`cmd.SetArgs` 驱动 `k8s-ai scan --fail-on HIGH`，断言退出码；help 文本 golden。
- `config validate`：非法配置与不可达集群的失败路径。

## 7. envtest 集成测试（可选）

`//go:build integration` 独立 tag：

- 下载/指定 `KUBEBUILDER_ASSETS` 起真实 kube-apiserver。
- 验证：真实 list 分页、`ResourceVersion="0"`、logs 截断、RBAC 拒绝写操作的端到端表现。
- 默认 CI 不执行（避免二进制依赖）；`make test-integration` 手动触发。

## 8. 出口标准

- 每阶段：`go test ./...`、`go vet ./...` 全绿。
- 核心包覆盖率目标 ≥ 80%（model/rule/evidence/security/diagnosis/report）。
- 架构测试四类全部通过；`go test -race` 通过。
