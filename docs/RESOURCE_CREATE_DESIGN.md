# 资源创建能力设计（Resource Create）

> 状态：设计文档，描述目标架构与迁移路径，不实现代码。
> 范围：产品架构 + 工程方案。本文档与 Approval v2、Agent 架构、Executor 写边界保持一致；
> 与 `docs/APPROVAL_V2_DESIGN.md`、`docs/AGENT_ARCHITECTURE.md` 共同构成二期 Agent 能力设计。
> 口径：`docs/PROJECT_SPEC_二期.md` 中“方法论 Skill 化不在本版范围、是否承接另行规划”，
> 本文档即该“另行规划”的产品设计；具体排期是否进入二期由后续评审决定。
> Secret 红线口径：读取集群既有 Secret 值保持 AGENTS.md 硬约束；创建走“值不透传 LLM”
> 的专用通道（§5.3），避免创建能力被整块限制住。

---

## 1. 背景与现状

### 1.1 产品现状

k8s-ai 目前已具备：集群扫描、规则巡检、诊断/故障链分析、Chat（Agentic Tool Calling）、
基于审批的修复执行。写能力集中在 `internal/tools/write.go` 的通用 `kubectl` Tool：

- 只读命令（get/describe/logs/events，无 shell 语法）自动执行；
- 写命令、shell 构造必须走人工审批；
- 执行只在 `internal/executor`，架构测试守护写边界；
- 审批链路已升级为 Approval v2（strict/smart、Policy.Evaluate、Grant、Code Guard）。

从能力上讲，LLM 现在也能通过 `cat <<EOF | kubectl apply -f -` 这类方式把 YAML 部署进集群。
但这是“自由命令行”路径，不是产品化的资源创建能力：

- 没有结构化的字段校验、GVK/kind 校验、dry-run 门；
- 没有“创建提案”概念：审批时只看到一段命令串，看不到 manifest、对象、风险；
- 没有创建后的 verify（是否 Ready、PVC 是否 Bound、Service 是否有 Endpoint）；
- 没有按资源域沉淀的创建方法论，模型每次靠 prompt 现场发挥，质量不稳定；
- 当前风险表把 `create`/`apply` 一律视为 HIGH，无法表达“创建 ConfigMap”和
  “创建带 privileged 的 DaemonSet”的真实风险差异；
- Secret 红线使含值 Secret 的创建路径天然被拦截，需要专门的设计。

### 1.2 能力缺口

“创建”不是一个新按钮，而是 Agent 在工作时缺失的一环：

1. **直接创建需求**：用户说“帮我部署一个 Redis”“给我在 test 命名空间建个 PVC”。
2. **修复闭环补资源**：诊断发现 PVC 缺失导致 Pod Pending、Service 没有 Endpoint、
   ConfigMap 不存在导致启动失败——修复链上需要“补建缺失资源”，这比 patch 更常见。
3. **蓝图化/批量化创建**：一组资源（Deployment + Service + PVC + ConfigMap）一次交付，
   为后续 GitOps/Helm 集成留出形态。

### 1.3 设计目标

- 资源创建成为 Agent 的一等公民能力：结构化成形、dry-run 先行、审批提案可读、创建后可验证。
- 只引入**一个**通用创建类写 Tool，不按资源类型造 `CREATE_*_TOOL`。
- 创建方法论按“风险/生命周期域”组织成 Skill 元数据，不按资源类型堆 Skill。
- 保持不变式：写入只发生在 `internal/executor`；审批链复用 Approval v2；
  读取集群既有 Secret 值永不进入 LLM 上下文／会话／审计；CLI / Server / WebUI
  复用同一套应用服务。创建新 Secret 通过“值不透传 LLM”的专用通道放行（§5.3）。

---

## 2. 主流做法的借鉴分析

### 2.1 kubectl-ai（已核对本地代码）

本地 kubectl-ai（`F:\Documents Library\personal collection\编程\Golang\code\kubectl-ai`）
的做法是“少工具 + 强提示”：

- 只有 `kubectl`、`bash` 等少数通用工具；`pkg/tools/kubectl_tool.go` 的入参就是
  `command` 字符串 + `modifies_resource: yes/no/unknown`；
- 系统提示词里内置“Manifest Creation Guidelines”：创建前必须收集命名空间、镜像、
  存储、端口、环境变量、RBAC 等信息，必须在得到明确确认前展示“创建摘要”；
- 通过 sandbox 执行并把写操作交给外部权限校验。

值得吸收的两点：

1. **创建前强制信息收集**：禁止用假设的默认值生成 manifest。
2. **单命令工具 + 资源修改标记**：工具数量少、通用性强。

kubectl-ai 的局限也很明显：它是“对话引导 + 让 LLM 写 kubectl”，没有结构化的
manifest 校验、diff、创建提案、结果 verify；`modifies_resource` 只有三态字符串，
无法支撑精确的风险分级和 Code Guard。我们的产品已经比它复杂，不能照搬。

### 2.2 业界 Agent / Skill 模式

当前主流 Agent 类产品（Anthropic Claude Skills、OpenAI Codex/Agents 等）普遍采用
“工具执行 + 技能知识分层”的模式：

- **工具**负责可执行、可验证的能力，数量少而稳定；
- **技能（Skill）**负责知识与方法，本质是“带说明、模板、脚本的组织化指令包”，
  由 Agent 按任务加载，本身不注册成可调用函数；
- **Agent** 负责意图识别、选择技能、编排工具调用、处理用户确认。

Kubernetes 侧的工程实践同样支持这一分层：server-side apply + `--dry-run=server`
提供结构化的安全创建通道；KRM 函数、kustomize、GitOps 控制器把“方法”从“执行”中
剥离开，执行最终都收敛到对 API Server 的一次受控写操作。

### 2.3 结论

我们的架构结论与用户草图一致，可以落为三条硬规则：

```text
Tool   = 能力（可以执行、可以被审批、可以审计）
Skill  = 方法（知识、模板、校验规则、风险提示，不直接执行）
Agent  = 编排（意图 → 选 Skill → 生成 manifest → 调 Tool → 等审批 → verify）
```

---

## 3. 核心架构决策

### 3.1 为什么不做 `CREATE_*_TOOL`

如果按资源类型出工具（`create_deployment`、`create_statefulset`、`create_configmap`…）：

- **兼容性差**：每新增一个内置 Kind 或 CRD 都要发布新工具，用户侧 Schema 变化频繁；
- **维护成本高**：每个工具都要维护 schema、description、risk、审批规则、测试、WebUI 卡片，
  N 个资源 × M 层维护；
- **扩展性差**：CRD 数量是无限的，工具化路线到头就是“内置支持不了自定义资源”；
- **上下文浪费**：几十个工具 schema 塞给 LLM，挤占推理预算，还让模型选错工具的概率上升。

### 3.2 为什么不做“逐资源 Skill”

“创建 Deployment 的 Skill”“创建 PVC 的 Skill”依然按资源类型爆炸，知识重叠严重：

- 所有 workload 共享“镜像、副本、探针、资源配额、滚动策略”方法论；
- 所有 storage 共享“StorageClass、容量、访问模式、回收策略”方法论；
- 逐资源 Skill 等于把应内聚的方法论切碎了，Agent 还得自己拼。

Skill 应按**风险/生命周期域**组织，而不是按 Kind 组织。

### 3.3 总架构

在用户草图基础上细化：

```text
                           k8s-ai
                              │
                       ┌──────┴──────┐
                       │    Agent    │  意图识别 / 信息收集 / 编排 / 提案
                       │ Reason/Plan │
                       └──────┬──────┘
                              │
              ┌───────────────┴───────────────┐
              │                               │
           Skills                           Tools
              │                               │
      ┌───────┼────────┐              ┌───────┼─────────┐
      │       │        │              │       │         │
 Diagnose  Create   Operate          Read    Write    External
      │       │        │              │       │         │
      │   resource-    scale          get   create_    prometheus
      │   create       restart        logs  resource*  gitlab
      │                rollout        events  kubectl   helm
      │
      └────────────────┬──────────────────────────────┘
                       │
                 Approval v2        （strict/smart + Grant）
                       │
                 Authorization      （Grant scope 命中判断）
                       │
                   Code Guard        （kind 黑名单 / dry-run / Secret 红线 / shell）
                       │
                    Executor         （唯一写边界：kubectl argv / client-go typed）
                       │
                   Kubernetes

* create_resource 为本设计新增的唯一通用创建类 Tool
```

---

## 4. 通用创建 Tool 设计：`create_resource`

### 4.1 为什么是“一个”工具

所有创建本质上是同一件事：**把一个声明式 manifest 结构化地提交给 API Server**。
差异（Kind 不同、校验要求不同、风险不同）不属于工具，而属于 Skill 与 Code Guard 元数据。

命名建议：`create_resource`。

- 强调“创建”语义，与修复类动作（patch/scale/restart）在 prompt 和审批文案上区分开；
- 内部用 server-side apply 实现（天然幂等、支持字段所有权，未来可平滑升级为 update 通道）；
- 若目标资源已存在，**不静默覆盖**：dry-run 阶段检测并存性，返回“已存在”并建议
  patch/update 语义，由 Agent 转为更新提案。

### 4.2 工具契约（v1）

```json
{
  "manifest": "<YAML 或 JSON，单文档或多文档>",
  "mode": "dry_run | apply",
  "verify": true,
  "timeout_seconds": 60
}
```

约束：

- `manifest` 必须包含 `apiVersion`、`kind`、`metadata.name`；缺少即拒绝；
- 单次调用对象数量、字节数有上限（默认 3 个对象 / 8KB，可配置），防止上下文爆炸；
- v1 不支持模板表达式（`{{ }}`、Helm 模板、kustomize 函数），manifests 必须是字面 YAML/JSON；
- `mode=dry_run` 是可自动执行的只读路径；`mode=apply` 是写路径，触发审批。

### 4.3 工具执行管线

```text
create_resource(manifest, mode)
    │
    1. 解析 YAML/JSON（多文档拆分，拒绝模板表达式）
    │
    2. GVK 解析：DiscoveryClient 校验 kind/resource，未知或不在白名单 → 拒绝
    │
    3. Skill 规则校验：必填字段 / 危险字段 / 命名规范（命中 Skill 元数据）
    │
    4. 风险分级：Risk(kind, scope, manifest 字段) → MEDIUM/HIGH（见 §5.2）
    │
    5. 存在性检查：get 目标资源
    │       ├─ 已存在 → 返回“已存在”，建议走 patch/update，不覆盖
    │       └─ 不存在 → 继续
    │
    6. dry-run（client + server）：
    │       └─ mode=dry_run → 返回校验结果、渲染后的 manifest、风险评估
    │
    7. mode=apply：
    │       └─ Agent Loop 审批（Approval v2）→ Code Guard → Executor
    │              └─ server-side apply（fieldManager=k8s-ai）
    │
    8. verify（可选）：按 Kind 检查就绪条件（Deployment Available、PVC Bound、
    │      Service Endpoints、Ingress Address…），未就绪时附最近 Events
    │
    9. 结构化结果：对象清单（kind/name/ns/UID）、applied 字段、verify 结果、事件
```

### 4.4 输出

```json
{
  "objects": [
    {
      "apiVersion": "apps/v1",
      "kind": "StatefulSet",
      "namespace": "prod",
      "name": "redis",
      "uid": "…"
    }
  ],
  "dryRun": "passed",
  "risk": "MEDIUM",
  "validation": [],
  "verify": {
    "status": "ready",
    "summary": "3/3 replicas ready; 3 PVC bound"
  }
}
```

### 4.5 与现有 `kubectl` 写工具的关系

| 维度 | `create_resource` | `kubectl`（现有写工具） |
| --- | --- | --- |
| 定位 | 受控创建主通道 | 自由命令兜底/复杂操作 |
| 输入 | 结构化 manifest | 命令字符串 |
| 适用 | 创建常见内置资源、多对象编排 | 不在白名单、自定义 CRD、组合 shell 修复 |
| 校验 | GVK 白名单 + dry-run + Skill 规则 | deny_ops/Secret/交互式红线 |
| 审批 | 创建提案可读 | 命令串展示 |
| 落点 | 同为 Executor | 同为 Executor |

两条路径不被移除：`kubectl` 仍是复杂/自定义场景的兜底；创建能力优先引导走
`create_resource`。二者在 Executor 层共用同一写边界。

---

## 5. 风险分级与 Code Guard

### 5.1 与现有风险表的衔接

当前 `internal/approval/risk.go` 把 `apply`/`create` 一律判 HIGH。创建能力上线后，
风险应同时考虑 kind、scope 与 manifest 字段：

| 对象/条件 | 默认风险 |
| --- | --- |
| namespace-scoped 常见资源（Deployment/STS/DaemonSet/Job/CronJob/ConfigMap/Service/Ingress/PVC/NetworkPolicy/SA/Role/RoleBinding） | MEDIUM |
| manifest 含 `hostNetwork`、`privileged`、`hostPath`、任意挂载 SA token 等敏感字段 | 提升 HIGH |
| cluster-scoped 资源（Namespace/PV/StorageClass/ClusterRole/ClusterRoleBinding/CRD/PriorityClass） | HIGH |
| 含值 Secret（用户提供/Executor 生成，值不透传 LLM） | MEDIUM（见 §5.3） |
| 未命中上表的 Kind（CRD / 自定义资源，任意 scope） | HIGH（必人工审批） |
| 目标资源已存在却要求覆盖 | 拒绝，转 patch/update 提案 |

该表落地为 Code Guard 的确定性函数，而不是 prompt 文字。Skill（含用户 Skill）里的风险规则
只能提出“升档建议”，最终由 Code Guard 的强规则裁决；CRD / 未知 Kind 的 HIGH 基线
不因命中任何 Skill 而降级或免审。

### 5.2 Code Guard 强化点

在现有 `internal/executor` / `internal/approval` 之上新增：

- **kind 黑名单**：`agent.create.deny_kinds`（默认不限制，CRD 与未知 Kind 均可创建，见三期计划 D-11）、
  `cluster_kinds`（cluster-scoped 授权闸门）；`allow_kinds` 只作为可选收紧开关（空＝不限制），
  未知 Kind 一律按 HIGH 走人工审批；
- **manifest 静态检查**：危险字段（privileged/hostNetwork/hostPath）、空镜像 tag、
  明显缺失的 namespace；检查结果进入审批提案；
- **dry-run 强制**：`apply` 前必须通过 server-side dry-run；未 dry-run 的 manifest 拒绝审批；
- **namespace 存在性检查**：目标 namespace 不存在时默认拒绝，提示先创建 namespace
  （不自动创建，避免隐式扩大爆炸半径）；
- **Secret 通道**：只有 `create_resource` 结构化通道可携带 `stringData`/`data`；
  值必须来自用户审批载荷或 Executor 生成，做“不透传 LLM”与指纹审计；
  自由 `kubectl` 命令含 Secret 资源字样仍按 `IsForbiddenCommand` 硬拦（见下）。

### 5.3 Secret：创建放开，读取保持红线

先说结论：创建能力块**放开含值 Secret 的创建**，但采用“值不透传 LLM”的结构化通道；
读取集群既定 Secret 内容值仍保持硬红线。

为什么读和写要分开：

- **创建**承载的是“用户本轮主动给出的新值”（如“密码设为 xxx”）或 Executor 生成的随机值，
  不暴露集群中我们不知道的敏感数据；真正被创建能力卡住的是“不能建含值的 Secret”，
  而不是“不能读旧值”。
- **读取**是把集群里已存在的 Secret 值拉进 LLM 上下文/会话/审计，这是 AGENTS.md 的
  数据安全不变量，与创建能力无关，不宜在创建块内一并放开。

创建通道规则：

```text
用户输入值 / Executor 生成随机值
    │
    ▼
审批载荷（值只存在审批提交中，不进入 LLM 上下文）
    │
    ▼
create_resource(apply)
    ├─ Code Guard：只允许结构化通道携带 stringData，值来源白名单校验
    ├─ Executor：组装 Secret 并提交（fieldManager=k8s-ai）
    └─ 回显/审计：只记录 key 名、值长度、SHA-256 指纹
```

- LLM 负责编排 manifest 骨架与 key 名，不接收明文值；tool 结果不回显值；
- 会话/审计/LLM 请求中不落明文值（指纹可核对一致性）；
- `kubectl` 自由命令通道仍保留 `internal/approval/policy.go` 的 `IsForbiddenCommand`
  硬拦截：命令串里的明文值会进审计，这正是要避免的；
- 配置闸门：`agent.secret.access = off | create_only | read_write`，默认 `create_only`；
  未来确需“读取”做诊断时，单独设计读取通道并显式开启 `read_write`，审计打
  `SECRET_TOUCHED` 标记，不作为创建能力的默认行为。

### 5.4 审批映射

沿用 `internal/approval/decision.go` 的 `Policy.Evaluate`：

- 创建类写操作默认 `require_approval`；
- `strict` 模式：每次 apply 都需要人工确认；
- 含值 Secret 创建默认 MEDIUM，审批回显只含 key 名/长度/指纹，值不进入回显与审计；
- `smart` 模式：全新 manifest 首次仍必须人工批准；只有同任务、同 scope、MEDIUM 及以下的
  后续操作可命中 Grant 减少重复审批（`GrantRiskCeiling = MEDIUM` 已与现状一致）；
- shell/heredoc 创建的路径永不进入自动放行（现有 `DecisionRequireApproval` 前置条件不变）；
- `--skip-permissions` / `require_approval=false` 保持现有语义：仅对非 shell 普通写生效，
  且 v1 建议创建类操作仍输出审计告警，不作为产品默认推荐。

---

## 6. Skill 治理：创建方法论按域组织

### 6.1 Skill 是什么

Skill 不是工具，不注册进 Tool Schema，不直接执行。它是 Agent 可加载的：

```text
适用场景 + 前置信息清单 + 最小 manifest 模板 + 字段校验规则 + 风险提示 + 前后置检查
```

在“方法论 Skill 化”框架落地前（二期 spec 明确另行规划），第一版可以把首批创建知识
做成**内置 Skill 配置**（Go 内嵌或 YAML），由 `create_resource` 的校验器直接消费，
Phase A（三期资源创建）不引入通用 Skill 引擎；但 **Skill 接口必须与三期同期冻结**：
Skill 数据模型 + `Registry` 读接口 + `Source` 来源字段 + `Reload()` 签名（三期计划 D-15 / R5.9），
保证四期加引擎时不必返工。通用 Skill 引擎与用户自定义 Skill 目录（目录发现、来源标记、门禁、
Agent 按需加载调用）整体排在四期，在三期资源创建能力交付后再讨论方案并排期。

四期开放用户 Skill 时必须守住的边界：只声明方法论与校验规则（不含执行动作，不新增写通道）；
风险只升不降，且引用的 rule / check kind 必须命中内置注册表；与内置 Skill 同名一律拒绝加载。
用户 Skill 不是安全权威：不能覆盖 Code Guard 的基线风险，尤其不能因命中 CRD / 未知 Kind
而降低默认 HIGH 或豁免人工审批。写操作仍然只有 `create_resource` → `internal/executor` 一条路。

### 6.2 首批 Skill 域（按风险/生命周期域，不按 Kind）

| Skill | 覆盖 Kind | 方法论重点 |
| --- | --- | --- |
| `create-workload` | Deployment、StatefulSet、DaemonSet、Job、CronJob | 镜像 tag、副本、探针、资源配额、滚动策略、安全上下文 |
| `create-config` | ConfigMap、Secret（值走 §5.3 专用通道） | 数据来源、dry-run 校验、与 workload 的引用一致性、指纹审计 |
| `create-storage` | PVC、PV、StorageClass | StorageClass、容量、访问模式、回收策略、绑定检查 |
| `create-network` | Service、Ingress、NetworkPolicy | 端口/类型/selector、IngressClass、默认拒绝策略 |
| `create-rbac` | ServiceAccount、Role、RoleBinding（Cluster 级默认 HIGH） | 最小权限、绑定前确认范围 |

将来 CRD/外部域（Helm、GitOps）以同样元数据形态扩展，不新增工具。

### 6.3 Skill 元数据示例

```yaml
name: create-workload
version: 1
domain: workload
kinds: [Deployment, StatefulSet, DaemonSet]
required_inputs:
  - namespace
  - container.image
  - container.port
optional_inputs: [replicas, resources, probes, rollingUpdate]
validation:
  - rule: "image 必须显式 tag，禁止 latest / 空 tag"
    severity: error
  - rule: "必须设置 resources.requests"
    severity: suggest
  - rule: "privileged / hostNetwork / hostPath 必须由用户显式确认"
    severity: error
risk:
  default: MEDIUM
  elevate_when:
    - path: "spec.template.spec.hostNetwork"
      if: "true"
      to: HIGH
    - path: "spec.template.spec.containers[].securityContext.privileged"
      if: "true"
      to: HIGH
pre_checks:
  - kind: namespace_exists
  - kind: image_pullable
post_checks:
  - kind: workload_ready
    timeout_seconds: 120
```

### 6.4 Skill 与 Agent、Tool 的边界

```text
Agent 判定“这是创建 workload 场景”
    → 加载 create-workload Skill 的方法论
    → 按 required_inputs 向用户收集缺失信息
    → 生成 manifest 草稿
    → 调用 create_resource(dry_run) 拿校验结果
    → 修正后调用 create_resource(apply) 触发审批
    → 审批通过后由 Tool 完成执行与 verify
```

Skill 永远不“绕开”Tool 或 Executor；它可以建议调用顺序，但不能替代安全链路。

---

## 7. Agent 创建流程

### 7.1 意图识别与信息收集

创建诉求必须与诊断类问题区分开（沿用 `internal/agent/prompt.go` 的决策树扩展）：

```text
用户意图
  ├─ 检查/诊断/为什么          → 只读调查，禁止写工具
  ├─ 修复已存在资源             → patch/rollout/kubectl 修复流程
  ├─ 创建/部署/上线/建一个 X     → 创建工作流（本文档）
  └─ 删除                      → 现有 delete 红线，永远最后选择
```

创建前强制执行信息收集（借鉴 kubectl-ai 的强制确认，但结构化）：

1. 对照 Skill 的 `required_inputs` 列出缺失字段；
2. 优先从集群取证填充可查项（现有 namespace、StorageClass、已有同名资源、nodes 容量）；
3. 用户未提供的字段**不得编造默认值**，必须追问；可给建议默认值但需用户确认；
4. 收集完成后展示“创建摘要”再进入 dry-run。

### 7.2 端到端示例

```text
用户：帮我在 prod 命名空间创建一个 Redis StatefulSet，3 副本，每个 2Gi PVC

Agent：确认信息 → namespace=prod 存在、默认 StorageClass 可用
Agent：create_resource(mode=dry_run, manifest=<sts + pvc>)
Tool ：dry-run=server 通过；风险 MEDIUM；对象 sts/redis + 3 pvc，无危险字段
Agent：展示创建提案（对象、风险、manifest 摘要）并请求确认
用户：确认
Agent：create_resource(mode=apply, manifest=<同一 manifest>)
  → Approval v2：require approval → 用户批准
  → Code Guard：kind 黑名单 / dry-run 记录 / Secret 红线
  → Executor：server-side apply
Tool ：verify ready：sts 3/3，PVC 3 Bound
Agent：输出创建报告与后续建议
```

### 7.3 失败与重试策略

- dry-run 失败：返回结构化错误，Agent 修正 manifest，不原样重试同一调用（沿用现
  `toolCallFailureKey` 熔断）；
- apply 部分失败（多对象）：逐对象报告成功/失败，不实现自动回滚（v1 明确非目标）；
- verify 超时/未就绪：返回当前状态与 Events，由 Agent 判断是继续等待还是给出修复建议；
- 用户拒绝审批：沿用现有拒绝计数语义，同签名写操作被拒 2 次后不再打扰并建议换方案。

---

## 8. Web UI 与可观测性

创建能力在 Web UI 呈现为“创建提案卡片”，与现有审批卡片共用视觉与状态流：

```text
创建提案
  ├─ 对象清单：kind / name / namespace / 集群范围标记
  ├─ 风险徽标：MEDIUM / HIGH + 触发原因（privileged、cluster-scoped…）
  ├─ manifest diff（dry-run 后的渲染结果）
  ├─ 校验结果：Schema / 白名单 / 危险字段
  └─ 审批按钮（复用现有 approve/deny）

执行后状态：dry-run → pending approval → applying → verifying → done/failed
```

审计记录新增：`action=resource_create_proposal`、`action=resource_create_dry_run`、
`action=resource_create_apply`，落 audit 时保留 manifest 与审批人；
Secret 创建只落 key 名/长度/SHA-256 指纹，不落明文值。

---

## 9. 迁移路线

### Phase A：受控创建 MVP（不引入通用 Skill 引擎）

- 新增 `create_resource` Tool（`dry_run` / `apply` 两阶段）；
- 实现 GVK 解析、kind 黑名单与 scope 闸门、server-side dry-run、风险分级、存在性检查；
- 首批内置 Skill 配置覆盖 workload/config/storage/network/rbac，并冻结 Skill 数据模型与 `Registry` 读接口（不含引擎与用户 Skill 目录）；
- Secret `create_only` 通道：用户值/Executor 生成值 + 指纹审计（§5.3）；
- Agent prompt 增加创建决策树与信息收集协议；
- Web UI 创建提案卡片；审计埋点。

### Phase B：Skill 框架化（四期，待三期资源创建交付后讨论方案）

- `internal/skills`：YAML/Go 定义统一加载、校验、版本化；
- Skill 从内置配置迁移到可扩展注册表；CRD 域通过配置开放；
- 用户自定义 Skill 目录（`agent.skills.dirs`）接入注册表，来源标记与门禁生效；
- 创建结果 verify 规则全部数据化；
- 用户 Skill 进入 Agent 上下文：Skill 目录注入 system prompt、`load_skill` 按需加载、热加载、
  `k8s-ai skills list/validate` 子命令与用户 Skill 来源审计。

### Phase C：体验与自动化联动

- `secret.access=read_write` 读取诊断场景专项评估（独立 feature，默认关闭）；
- Approval v2 Grant 在创建链路的精细 scope（同 task、同 kind、同 namespace）联动；
- 多对象依赖编排（先 ConfigMap 后 Deployment）、模板化创建；
- 修复闭环：诊断建议“补建缺失资源”直接生成创建提案。

### 未来（Phase 3 预留）

- MCP 外部工具、Helm/GitOps/控制器接管；
- 跨集群创建、蓝图/模板市场。

---

## 10. 待决问题

| # | 问题 | 建议默认 |
| --- | --- | --- |
| 1 | 含值 Secret 创建 | Phase A 支持（create_only 通道，值不透传 LLM）；读取保持红线 |
| 2 | 目标 namespace 不存在 | 拒绝创建，提示先建 namespace |
| 3 | CRD/自定义 Kind | 不限制：默认可创建，`deny_kinds` 黑名单 + cluster-scoped 授权闸门 + 风险默认 HIGH（对齐三期计划 D-11/D-13） |
| 4 | 多对象部分失败是否回滚 | v1 不回滚，逐对象报告 |
| 5 | `--skip-permissions` 是否覆盖创建 | 沿用现有非 shell 语义，v1 建议告警 |
| 6 | 创建操作与现有 `kubectl apply` 的 prompt 引导 | 创建意图优先 `create_resource`，兜底保留 kubectl |

---

## 11. 相关代码与文档

- `internal/tools/write.go`：现有唯一写工具与 `WriteRunner` 接口
- `internal/tools/tool.go`：Tool 契约（IsReadOnly / Risk）
- `internal/approval/decision.go`、`policy.go`、`risk.go`、`grant.go`：Approval v2
- `internal/executor/kubectl.go`、`parse.go`：唯一写执行边界与 Code Guard
- `internal/agent/prompt.go`、`loop.go`：意图决策树与审批编排
- `docs/APPROVAL_V2_DESIGN.md`、`docs/AGENT_ARCHITECTURE.md`：既有二期设计
- `docs/PROJECT_SPEC_二期.md`：方法论 Skill 化的范围基线
- 本地参考：`F:\Documents Library\personal collection\编程\Golang\code\kubectl-ai`
  （通用工具 + 创建前强制信息收集模式；本项目按自身架构重新设计，不直接复用代码）
