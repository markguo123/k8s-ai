# k8s-ai 四期方案：页面发起巡检与 CLI 边界（Scan Trigger）

- 版本：**v1.2**（2026-11-05）
- 状态：**代码已落地**（P8.13 已按本文实现；落地形状与本文口径的八处差异集中在 §0，**冲突时以代码为准**，§11/§12 保留为原始任务书不逐字改写。v1.1 只在 §11 登记与 **P8.14 报告清单清理** 的页面分工，巡检口径一字未改）
- 定位：把已有的巡检能力（`internal/service.ScanService.Run`）**接到页面**，不新增采集能力、不改规则引擎、不动 CLI 行为。
- 关联：方案 15 **v1.13**（存储边界与对象存储：报告归档、读鉴权与报告清单清理）、方案 07（一期扫描流程，已封版）、ADR-037（权限与会话 owner）、ADR-043 v1.1（双存储）、`03-API接口文档.md` §8、`02-数据模型与数据字典.md` §13
- 评审基线：DEV-v4.0.0 @ `a437d81` + 工作区改动，逐条按真实代码核对；**代码与文档冲突时以代码为准**。v1.2 落地基线：`004908f` + 工作区内 P8.13/P8.14 改动。

## 0. v1.2 落地回写（P8.13 代码已落地，与 §11/§12 的差异以代码为准）

落地基线 `004908f` + 工作区改动。`go build`/`go vet` 干净、`go test ./...` 39 包全绿；`web` 侧 `npm run typecheck` 干净、`npm run test` 28 文件 172 用例全绿。§11/§12 保留为原始任务书，本节只登记实际落地形状与本文口径的差异，不改任何设计结论。

| # | §11/§12 原口径 | 代码事实 | 处理 |
| --- | --- | --- | --- |
| 1 | §11 第 7 条「顶部『发起巡检』**抽屉**」 | `ReportsView.vue` 用 `el-dialog`（540px）而非 `el-drawer`，任务状态用 3000ms 轮询 `GET /scans/{id}` | 交互收口径为**弹窗 + 轮询**：表单只有范围与档位两组控件，弹窗比抽屉少一层遮挡，且与页面既有确认弹层同族；结束后按 `scanId` 过滤刷新，顶部保留巡检状态横幅与「只看本次产物」开关 |
| 2 | §12 第 6 条「读他人任务 → 403 **或**与不存在同形 404」 | `handleGetScan` 走 **404 同形**：job id 是 8 字节随机串，回 403 等于向调用方确认「这个 id 存在且属于别人」 | 定死 404，响应形状与「任务不存在」一致；admin 可读 |
| 3 | §11 第 3 条「补进 operator 角色种子」 | `defaultRoles()` 落地为 admin `*` / operator `[scan.execute]` / user **空数组**。`reconcileRoles` 每次启动**覆盖式**写内置角色，种子一改，按旧值断言的两处测试必须同批改：`internal/auth/service_test.go`、`tests/platform/capability_test.go` | 已同批改。**以后凡是改内置角色权限，先预期这两个测试会红**——覆盖式 reconcile 是有意设计（存量库自动补齐），不是漏洞 |
| 4 | §7.1 只要求给 `reports` 加两列 | v8 实际是**两列 + 一个索引**：`schemaV8ReportIndexes` 多建 `idx_reports_scan(scan_id)`，因为轮询结束后按 `scanId` 反查产物是每次巡检的必经路径，而 `scan_id` 此前无索引 | 按代码收口径。存量行两列**不回填**，空串即页面「范围未记录」 |
| 5 | §7.2 示例名 `巡检-命名空间arc-20261103-1712.md` | 实际 `巡检-<维度label>-<本地时区 20060102-1504><ext>`（`reportFilename`），md/json 同名换扩展名，时间戳走 `CreatedAt.Local()` | 与设计一致。跨时区部署时下载名与页面时间同源，不会出现两个钟 |
| 6 | §6「多集群本期只留口，不实现连通」 | `clusterId` 非空一律 **400**，而不是静默忽略后按本集群执行 | 留口的落地语义 = **显式拒绝**，绝不静默降级成本集群巡检 |
| 7 | §11 未写预设的基线从哪来 | 三档预设是在**服务端基线**上覆盖的，基线由 `internal/cli/server.go` 经 `WithScanBaseline` 从 config 注入；未注入时基线为零值 | 装配漏注入不会 panic，但会丢掉 kubeconfig/QPS 一类的基线，改 server 装配时看这条 |
| 8 | §11 第 9 条「清理入口不归本任务」 | 清理由 P8.14 实现，但两者改的是同一个 `ReportsView.vue`，实际同一批提交落地 | 分工口径不变（依赖、事务边界、验收各自独立），只是提交同批：真要回滚，按文件想而不是按任务想 |

另有两条实测结论补进验收证据：越权 ns 的 403 **原样点名 ns**，页面把这句文案直接显示给用户、不留半截横幅；前端提交只发 `scope`/`namespace`/`pod`/`preset` 四个白名单字段（连 `clusterId` 都不发），`reportDirectory` 这类连接字段在 DTO 层就进不来。

## 1. 为什么单独立一份方案

真机验收时用户的原话是「我进入 server pod 执行 `k8s-ai scan -n arc`，页面也没生成报告啊」。这不是 bug，是两条通道本来就没有接上，而这个分界此前**没有任何文档写过**，所以每换一次接手都要重新推一遍。本文把边界与页面侧口径一次钉死，P8.13 的实现照抄即可。

## 2. 边界（事实，不是设计选择）

CLI 与页面是两条**互不相通**的巡检通道，当前代码就是这么写的：

| # | 事实 | 代码位置 |
| --- | --- | --- |
| 1 | 平台库只在 `blobs` / `db` / `migrate --platform` / `server` 四处装配，`scan` 命令从不接平台库 | `internal/cli/server.go` 的 `openPlatformStore` 调用点 |
| 2 | 报告归档器只在 server 装配时注入，未注入即整段跳过 → CLI 扫描永远不写 `objects`/`reports`，页面看不到 CLI 产物 | `internal/service/report_archive.go` 的 `archiveReportsIfNeeded`（写入器为 nil 直接返回） |
| 3 | 定向扫描（带 `-n` 或 Pod 目标）的 `--report-mode` 默认 `none`，**连文件都不写**，只出终端 | `internal/cli/scan.go`（`podTarget != "" || opts.Namespace != ""` → `none`） |
| 4 | 页面报告的**唯一**来源是 `POST /api/v1/scans` 起的任务 | `internal/server/server.go` 的 `handleCreateScan` → `runJob` → `svc.Run` |

**签收口径**：CLI 保持独立（写自己配置的 `report.directory`，或只上终端），页面报告只来自页面发起的巡检。不把 CLI 产物回填进平台库——那要给 CLI 装平台库连接与凭据注入链路，与「CLI 零影响」硬约束（方案 15 §16）正面冲突。

## 3. 页面发起巡检的三个维度

「往页面靠一下」真正要定的只有三个维度，缺一即回答不了「这份报告是扫什么出来的」：

| 维度 | 取值 | 说明 |
| --- | --- | --- |
| **目标集群** | 平台自身集群（默认）/ 接入集群 ID | 多集群留口子，见 §6 |
| **范围 scope** | `cluster`（全集群）/ `namespace`（单命名空间）/ `pod`（单 Pod） | 决定授权闸门与产物命名，见 §4、§7 |
| **档位 preset** | 快速体检 / 标准 / 深度 | 参数预设见 §5，用户不逐项调参 |

巡检的产品定位是**加强 Agent 能力的那一半**：不依赖 LLM 也能在故障第一分钟给出可用结论，LLM 只是把结论讲清楚。

## 4. 授权闸门（P0：这一条不过关就不能开接口）

现状（按代码）：`POST /api/v1/scans` 挂 `chat.write`，`GET /api/v1/scans/{id}` 挂 `cluster.read`；`defaultRoles()` 给 user/operator 种的是**空权限数组**，只有 admin 是 `["*"]`，且 `reconcileRoles` 每次启动覆盖内置角色。

签收闸门（服务端判定，前端隐藏不算安全机制）：

1. **新立权限位 `scan.execute`**，并补进 operator 角色种子——否则按钮只有 admin 点得动，operator 形同虚设。admin 持 `*` 天然通过；user 角色**不加**（普通用户默认不给发起权，需要就单授）。
2. `scope=cluster` 仅 admin。全集群巡检会读全命名空间的清单、事件与指标，超出 namespace 授权模型的表达力。
3. `scope=namespace`：非 admin 时该 namespace **必须** ∈ 调用者的有效 AuthzGrant（复用 `EffectiveScope` 的 principal + 过期 + cluster 匹配遍历）。**越界返回 403 并点名是哪个 ns 越权，绝不静默裁剪成授权子集**——静默裁剪会让人以为扫了全量。
4. `scope=pod`：同 `namespace` 判定（Pod 所在 ns 必须 ∈ 有效 AuthzGrant）。
5. `GET /api/v1/scans/{id}` 从 `cluster.read` 改为 **任务发起人本人可读 + admin 全可读**。现状任何持 `cluster.read` 的用户都能凭 job id 读到别人的任务结果，而结果里含别人命名空间的问题清单，这是越权。
6. 报告读侧沿用方案 15 §13.1 的三层归属判定（owner 自有 / `created_by='system'` 按集群授权可见 / admin 跨 owner 且落 `report.download` 审计），本方案只追加 `scanId` 过滤，**不放宽任何一条**。

## 5. 三档预设（用户不逐项调参）

| 档位 | LLM 诊断 | 事件采集 | 日志采集 | 超时封顶 | 典型用途 |
| --- | --- | --- | --- | --- | --- |
| **快速体检** | 关 | 开 | 关 | 60s | 发版后、值班接手、故障第一分钟 |
| **标准**（默认） | 开 | 开 | 关 | 5min | 日常巡检、日报来源 |
| **深度** | 开 | 开 | 开（最近 1h） | 10min | 疑难定位、复盘前留证 |

档位到 `model.ScanOptions` 的映射由**服务端**完成（`CollectLogs` / `Since` / `LLM` / `Timeout`），前端只传档位名。阈值写成常量表并在测试里锁死，**不放进配置中心**——这是行为预设，不是现场调参项。

## 6. 多集群口子（本期只留口，不实现连通）

`clusterId` 语义：**空串 = 平台自身集群**（现状行为不变）；非空 = 走 `cluster.Service` 接入清单取凭据。**本期一律拒绝非空值**并返回可读错误「该集群尚未接入扫描」，但请求体、`reports.cluster_id`、对象 key 的 scope 段、页面下拉都按多集群形状预留，避免将来真接多集群时改表。

**归属集群必须服务端解析后覆盖**：现状 `handleCreateScan` 只覆盖 `Actor` 与 `ScanID`，`Attribution.ClusterID` 直通请求体，调用方可以把报告挂到任意 cluster 名下，再靠「system 报告按集群授权可见」读到别人集群的问题清单。本任务改为服务端解析后回填。

## 7. 产物命名与范围落库（要一眼看出扫的是什么）

### 7.1 `reports` 表加两列（schema v7 → v8，一次 migration，SQLite/MySQL 双驱动）

| 列 | 取值 | 说明 |
| --- | --- | --- |
| `scope_kind` | `cluster` / `namespace` / `pod` | 空串只出现在 v8 之前的存量行，前端显示「未记录」 |
| `scope_value` | `cluster` → 空；`namespace` → ns 名；`pod` → `<ns>/<pod>` | 只用于展示与过滤，不参与对象 key |

按 `internal/store/migrate.go` 既有 v7 模式加 `schemaV8ReportColumns`，补列冲突即跳过（幂等），归档时与业务行同事务落库。**内部 `kind`（`latest-md` / `latest-json`）不动**——它是归档键与既有测试的锚点，动它没有收益。

### 7.2 展示与下载名

- 列表新增「巡检范围」列：`全集群` / `命名空间 arc` / `Pod arc/web-1`。
- 下载文件名改人可读：`巡检-全集群-20261103-1712.md`、`巡检-命名空间arc-20261103-1712.md`、`巡检-Pod-arc-web1-20261103-1712.json`（时间取 `created_at` 的本地时区）。
- **配套实现约束**：`internal/server/reports.go` 的 `reportFilename` 现在把非 `[A-Za-z0-9._-]` 的字符一律替换成 `_`，中文名会被打成一串下划线。改为放行 Unicode 字母/数字（下载链路已是 RFC 5987 `filename*=UTF-8''` 百分号编码，天然支持中文），仍剔除路径分隔符、引号、控制字符与首尾空白；`reports_test.go` 对旧文件名的断言随之更新。

## 8. 请求体收窄（P0：现在是把内部结构体交给调用方）

现状 `handleCreateScan` 直接 `json.Decode` 到 `model.ScanOptions`，等于把 `Kubeconfig` / `Context` / `InCluster` / `ReportDirectory` / `Concurrency` / `QPS` / `Burst` / `Timeout` / `ReportMode` 全交给请求方——其中 `ReportDirectory` 能让服务往容器内任意可写路径落文件，`Kubeconfig` 能把扫描打到平台之外的集群。

签收：新增白名单 DTO，只接受 `scope` / `namespace` / `pod` / `preset` / `clusterId` 五个字段，未知字段拒绝（`DisallowUnknownFields`）。连接类参数（kubeconfig、context、in-cluster、QPS/Burst、并发）**一律服务端填**；`Timeout` 取档位封顶值与配置上限的较小者；`ReportMode` 在服务端固定为归档模式，不接受请求体值。

## 9. 任务状态与并发

- 任务注册表维持**内存单飞**（全局同一时刻只跑一个扫描，冲突返回 409），前端把 409 显示成「巡检进行中」，**不做排队**：排队会引入优先级、取消、跨副本一致性，收益不抵成本。
- `scan_jobs` 落库（任务历史、发起人审计、刷新页面后找回运行中任务）**延后 v4.2**。本期靠 `reports.scan_id` 反查产物——该列已在 schema v6，列表接口只需加 `scanId` 过滤参数，不需要新端点。

## 10. 明确延后 v4.2

1. `scan_jobs` 落库与任务历史页、发起巡检的独立审计动作。
2. 多集群真实连通（接入集群凭据下发与 `Reader` 构造）。
3. 定时巡检页面化：`deploy/scan` 的 CronJob 通道**继续独立**、不接平台库（其 ConfigMap 不含平台库 DSN，报告只写 PVC），本期不动。
4. 巡检范围多选（一次勾多个 ns）与增量巡检。

## 11. 落地清单（P8.13 的代码落点）

| # | 落点 | 改动 |
| --- | --- | --- |
| 1 | `internal/server/server.go` | 请求体白名单 DTO；档位映射表；`clusterId` 服务端解析并覆盖归属；范围授权闸门 |
| 2 | `internal/server/permission.go` | `POST /scans` 改 `scan.execute`；`GET /scans/{id}` 改 `AccessAuthenticated` + handler 内 owner 判定 |
| 3 | `internal/permission/permission.go` + `internal/auth/service.go` | 新增 `ScanExecute = "scan.execute"` 常量并补进 operator 角色种子（`reconcileRoles` 下次启动覆盖式补齐） |
| 4 | `internal/store/migrate.go` | `schemaV8ReportColumns`（`scope_kind` / `scope_value`）+ `ReportQuery.ScanID` 过滤 |
| 5 | `internal/service/report_archive.go` | 归档时落 scope 两列（与业务行同事务） |
| 6 | `internal/server/reports.go` | 列表响应带 scope 两列 + `scanId` 过滤；`reportFilename` 支持中文 |
| 7 | `web/src/views/report/ReportsView.vue` | 顶部「发起巡检」抽屉（范围 + 档位）→ 轮询任务 → 完成后按 `scanId` 刷新列表；列表加「巡检范围」列；409 显示「巡检进行中」 |
| 8 | `tests/arch` + 包内测试 | 闸门负向用例（越界 ns 403、非 admin 扫全集群 403、未知字段拒绝、`reportDirectory` 不可注入、job 非本人不可读）、migration 双驱动幂等、文件名用例 |
| 9 | `web/src/views/report/ReportsView.vue` 的**清理入口** | **不归本任务**：报告清单的行首复选框、行内「删除」、工具条「清理所选」与二次确认属 **P8.14 报告清单清理**（方案 15 v1.12 §0 与 §13.1/§15/§18），依赖 P8.5 + P8.9；与本任务改同一个页面，但不同依赖、不同事务边界、不同验收。P8.13 只加「发起巡检」抽屉与「巡检范围」列，别顺手在这里加删除——两份验收混在一起，出问题时分不清是发起侧还是清理侧 |

## 12. 验收标准

1. 非 admin 请求 `scope=cluster` → 403；请求未授权 ns → 403 且点名 ns；请求授权内 ns → 成功。
2. 请求体带 `reportDirectory` / `kubeconfig` / 未知字段 → 400，且服务端未因此产生任何路径写入或外部集群连接。
3. admin 发起全集群「标准」巡检 → 页面出现「巡检范围 = 全集群」的报告，下载文件名含「巡检-全集群」与时间戳，JSON 同名换扩展名。
4. operator 发起「快速体检」扫自己授权的 ns → 60s 内出报告、正文含规则结论；无 LLM 配置时不报错、结论仍在。
5. 任务运行中第二次发起 → 409 且页面提示「巡检进行中」，不排队、不影响第一次结果。
6. 非发起人凭 job id 读他人任务 → 403 或与不存在同形 404；admin 可读。
7. 桶不可达时发起巡检 → 采集与本地副本照常、归档阶段只 WARN（方案 15 §8.2 行为不变），页面报告列表少一条并给出可读原因。
8. `k8s-ai scan -n arc` 的行为与产物位置**零变化**（CLI 零影响门禁继续绿）。
9. v7 库升级到 v8 双驱动幂等；存量报告 `scope_kind` 为空串时页面显示「未记录」且仍可下载。

## 13. 工时

P8.13 = **1.00d**（M8，依赖 P8.5）。不含多集群连通、`scan_jobs`、定时巡检页面化（均延 v4.2）。
