# k8s-ai 一期性能与压测报告（P11）

- 日期：2026-08-17
- 环境：Windows 11 / Intel i7-1165G7 @ 2.80GHz / amd64
- 方法：`go test -benchmem -benchtime=3x`（fake clientset，纯算法成本；真实集群以网络与 API Server 为主）

## 1. 结论摘要

- Phase1 采集 1000 Pod / 50 命名空间：**约 2.5 ms/op，内存 4.9 MB/op**；
- 关联索引 + 13 条规则判定 1000 Pod（5% 异常）：**约 0.46 ms/op，内存 236 KB/op**；
- 请求量与 Pod 数量解耦：Phase1 list 请求 = 17 类资源 + 50 个 namespace（events），与 Pod 数无关（无 N+1，见 TESTING.md 请求量测试）。

## 2. 基准数据

```text
BenchmarkPhase1LargeCluster-8   3   2511867 ns/op   4928202 B/op   2078 allocs/op
BenchmarkRulesLargeCluster-8    3    459067 ns/op    236410 B/op   3506 allocs/op
```

## 3. 说明与边界

- 压测使用 fake clientset，未包含网络/序列化/API Server 往返；真实集群一次全集群 scan（497 Pod / 36 ns，含 Phase2 日志）实测约 7 秒（LLM 关闭）。
- Phase2 只对异常 Pod 取日志，请求数 = 异常容器数 × ≤2（current/previous），受 `scan.phase2_concurrency`（4）与单 Pod 30s 超时约束。
- LLM 阶段耗时取决于模型（思考型 397B 单 Finding 约 1.5-2 分钟；非思考型快模型秒级），与集群规模无关。
- 内存占用随 Phase1 快照规模线性增长（归一化精简字段）；超大集群（10 万 Pod 级）建议分批/分 namespace 巡检。

## 4. 复现

```bash
go test -run=NONE -bench=BenchmarkPhase1LargeCluster -benchmem ./internal/scanner/
go test -run=NONE -bench=BenchmarkRulesLargeCluster  -benchmem ./internal/rule/
```