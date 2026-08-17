# k8s-ai 一期并发、限流、超时设计

- 版本：v1.0
- 日期：2026-08-12
- 状态：待评审

## 1. 参数总表（默认值，全部可配置）

| 参数 | 默认 | 作用域 | 说明 |
|---|---|---|---|
| scan.timeout | 5m | 全扫描 | 根 ctx 总预算 |
| kubernetes.timeout | 30s | 单次 API 请求 | 每个 list/get/logs 调用的 deadline |
| kubernetes.qps | 20 | 客户端 | rest.Config QPS |
| kubernetes.burst | 40 | 客户端 | rest.Config Burst |
| scan.concurrency | 8 | Phase1 | 资源类型 + namespace 任务的并行数 |
| scan.phase2_concurrency | 4 | Phase2 | 异常 Pod 日志采集并行数 |
| scan.max_log_lines | 500 | 日志 | TailLines 上限 |
| scan.max_log_bytes | 64 KiB | 日志 | 单容器字节硬上限 |
| scan.max_log_line_bytes | 1 KiB | 日志 | 单行截断长度 |
| scan.pod_logs_timeout | 30s | 日志 | 单个 Pod 日志获取 deadline |
| llm.timeout | 120s | LLM | 单次 Chat 调用 deadline |
| llm.max_retries | 2 (5xx) / 3 (429) | LLM | 重试次数上限 |
| llm.max_input_tokens | 8k | LLM | 单 Finding 上下文上限 |
| llm.max_total_tokens | 32k | LLM | 诊断阶段总预算 |
| llm.max_findings | 30 | LLM | 最多送诊 Finding 数（自适应下调） |

## 2. 取消传播（cancellation）

```text
main/signal(SIGINT/SIGTERM)
  → root ctx = signal.NotifyContext
  → scanCtx = context.WithTimeout(root, scan.timeout)
  → 每个请求：context.WithTimeout(scanCtx, kubernetes.timeout)
  → 每个 LLM 调用：context.WithTimeout(scanCtx, llm.timeout)
```

规则：
- 所有 goroutine 必须监听 `ctx.Done()`，不得阻塞等待。
- 取消后 worker pool 立即停止派发新任务；在途请求随 ctx 中止。
- 已产出的快照/Findings 保留，报告标注"扫描被中断"，退出码 1。
- 禁止在 worker 内吞掉 ctx 错误后继续循环；统一走 `collection_errors` 或直接返回。

## 3. Worker Pool 设计

### Phase1（并发 8）

任务队列 = 资源类型任务（约 16 个）+ namespace 事件任务（N 个）。实现：`errgroup` + 信号量（或固定 worker 数 + channel）。

```text
任务1: list namespaces（先行，阻塞后续事件任务生成）
任务2..17: list pods/nodes/services/...（并行）
任务18..: list events per namespace（并行，受同一信号量）
```

### Phase2（并发 4）

任务 = 异常 Pod（每 Pod 一个任务，内部串行 current + previous logs，单 Pod 有 30s deadline）。

## 4. 客户端限流与重试

- `rest.Config.QPS/Burst` 由 client-go 的 token bucket 限流器执行；Discovery/Metrics 客户端复用同一 config。
- 读请求重试：仅对 429（尊重 Retry-After）与 5xx 做有限退避重试（指数退避 + jitter，上限 3 次）；4xx 其他不重试；`ctx.Err()` 不重试。
- 重试预算计入 `kubernetes.timeout`：总耗时（含重试）不得超过单请求 deadline。
- 避免放大：重试只发生在 Reader 层，且并发被 worker pool + QPS 双重约束。

## 5. 阶段预算

```text
Phase2 截止 = min(scan.timeout 剩余, phase2_timeout 默认 2m)
LLM 预算   = min(scan.timeout 剩余, llm 阶段预算 默认 4m)
```

LLM 自适应预算（ADR-017）：

```text
可送诊数 = min(llm.max_findings, floor(llm.max_total_tokens / llm.max_input_tokens))
若剩余预算不足 → 继续下调 max_input_tokens 或截断送诊数
超出预算的 Finding → 规则结论 + Diagnosis{LLMUsed:false, Error:"预算不足"}
```

## 6. 大集群行为推导（示例：1000 Pod / 50 namespace / 7 异常 Pod）

| 阶段 | 请求数 | 耗时估算（QPS 20） |
|---|---|---|
| Phase1 list | ≈ 67（未分页） | < 5s |
| Phase2 logs | ≤ 7×2 = 14 | < 5s |
| LLM | ≤ 7（串行或并发 2） | 受 llm.timeout 约束 |

结论：API Server 压力与 Pod 数量解耦；日志请求只与异常数成正比。

实测（P11，fake 压测）：1000 Pod/50 ns 的 Phase1 约 2.5ms、关联+规则链路约 0.46ms；请求量断言（17 类资源 + N 个 namespace）随测试守护，详见 docs/design/PERFORMANCE.md。

## 7. 防 goroutine 泄漏

- 所有并发原语限定在 scanner/diagnosis 包内部，通过 `errgroup.WithContext` 管理。
- channel 全部有界；无 unbounded 队列。
- LLM 并发上限（默认 2），避免打爆私有模型服务。
- `go test -race` 纳入每阶段出口标准。
