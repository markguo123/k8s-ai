# AegisOps 巡检采集能力与 RBAC 权限优化指导方案

> **实施状态（2026-11-04）**：本方案要求的采集/RBAC 闭环已落地。当前权威口径见
> `docs/02-架构与设计/18-方案-体检诊断体系升级-四期.md` §16，数据字段见
> `docs/02-架构与设计/02-数据模型与数据字典.md`。已落地项包括：修正 PV/PVC API Group，
> 补 HPA/PDB、RBAC 四类、ConfigMap 元数据与 Secret 键级元数据采集，统一
> `internal/scanner/collection_contract.go` 契约，部署 ClusterRole 与 Collector 自动化交叉校验，
> 命名空间/Pod 作用域隔离，以及采集缺失时禁止输出“未绑定”结论。本文以下“现有缺陷”
> 保留为实施前分析，不代表当前代码状态。

## 一、优化目标

本次优化聚焦于 Kubernetes 巡检采集链路和 RBAC 权限配置，核心目标是：

1. 保证巡检所需 Kubernetes 资源能够完整采集。
2. 修复现有 `clusterrole.yaml` 中 API Group 配置错误和资源权限缺失问题。
3. 保证 RBAC 权限清单与 Scanner 实际调用的 Kubernetes API 保持一致。
4. 确保巡检采集失败、权限不足等情况能够被准确识别并反映在报告中。
5. 完善 RBAC 授权关系分析，避免只根据 ClusterRole 规则误判实际权限风险。

核心原则：优先保证巡检采集能力完整。RBAC 权限应以实际巡检采集需求为依据，不应因为过度收紧权限而导致巡检数据缺失。

## 二、现有 RBAC 配置存在的设计缺陷

### 2.1 ClusterRole API Group 配置错误

现有配置中，部分资源的 API Group 归属存在错误。

例如：

| 资源                               | 正确 API Group              | 当前问题                    |
| ---------------------------------- | --------------------------- | --------------------------- |
| PersistentVolumes                  | Core API Group：`""`        | 错误归属到 `storage.k8s.io` |
| PersistentVolumeClaims             | Core API Group：`""`        | 错误归属到 `storage.k8s.io` |
| StorageClasses                     | `storage.k8s.io`            | 应归属该组                  |
| VolumeAttachments                  | `storage.k8s.io`            | 应归属该组                  |
| HorizontalPodAutoscalers           | `autoscaling`               | 需要单独授权                |
| PodDisruptionBudgets               | `policy`                    | 需要单独授权                |
| Roles / RoleBindings               | `rbac.authorization.k8s.io` | 需要单独授权                |
| ClusterRoles / ClusterRoleBindings | `rbac.authorization.k8s.io` | 需要单独授权                |

其中，PV 和 PVC 属于 Kubernetes Core API Group，不能配置在 `storage.k8s.io` 下。

这会导致 API 权限与实际资源 API 不匹配，最终表现为巡检采集失败。

### 2.2 巡检所需资源权限缺失

现有 Reader 接口和巡检逻辑已经涉及部分资源，但 RBAC 配置没有完整覆盖。

重点需要核对：

- HPA：`horizontalpodautoscalers`
- PDB：`poddisruptionbudgets`
- Role：`roles`
- RoleBinding：`rolebindings`
- ClusterRole：`clusterroles`
- ClusterRoleBinding：`clusterrolebindings`
- PV：`persistentvolumes`
- PVC：`persistentvolumeclaims`

例如，代码中存在 `ListHPA`、`ListPDB`、`ListClusterRoles` 等采集能力，但 RBAC 没有对应权限时，代码虽然具备采集逻辑，实际运行仍然会收到 `Forbidden`。

这属于典型的采集代码能力与部署权限配置不一致。

### 2.3 ClusterRole 权限清单缺乏统一维护机制

当前需要重点解决一个问题：

Scanner 新增资源采集能力时，RBAC 是否能够同步更新？

如果没有统一约束，就容易出现：

```
Scanner 新增资源采集
        ↓
Reader 接口正常
        ↓
RBAC 未同步增加权限
        ↓
Kubernetes API 返回 Forbidden
        ↓
巡检报告缺少对应资源数据
```

因此，RBAC 不应只是部署目录下的一份静态 YAML，而应成为巡检采集能力的配套配置，并纳入自动化测试。

### 2.4 ClusterRoleBinding 设计检查

现有 ClusterRoleBinding 的绑定关系：

```
subjects:
  - kind: ServiceAccount
    name: AegisOps
    namespace: aegisops

roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: AegisOps
```

从 Kubernetes RBAC 资源结构来看，这种绑定方式是正确的。

但需要确认以下约束：

1. `ServiceAccount/aegisops` 必须真实存在于 `aegisops` Namespace。
2. `roleRef.name` 必须与实际创建的 `ClusterRole` 名称完全一致。
3. `roleRef.kind` 应为 `ClusterRole`，不能错误配置成 `Role`。
4. 巡检需要覆盖整个集群时，应使用 `ClusterRoleBinding`，不能只使用某个 Namespace 下的 `RoleBinding`。
5. ClusterRoleBinding 的绑定范围是集群级的，不能通过给 ServiceAccount 设置某个 Namespace 来限制它的授权范围。

结论：现有 ClusterRoleBinding 的核心绑定结构没有明显错误，主要问题在于它所绑定的 ClusterRole 权限定义不完整，以及需要确保 ServiceAccount 和 ClusterRole 的实际资源名称一致。

## 三、ClusterRole.yaml 正确设计

建议将巡检所需资源权限统一整理为以下配置。

以下 YAML 是巡检采集权限的目标配置示例，正式提交前还应逐项与当前 Scanner 的实际 API 调用核对，避免遗漏，也避免增加没有实际调用依据的权限。

```
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: AegisOps
  labels:
    app.kubernetes.io/name: AegisOps
rules:
  # Core API Group
  - apiGroups: [""]
    resources:
      - namespaces
      - nodes
      - pods
      - pods/log
      - services
      - endpoints
      - events
      - configmaps
      - persistentvolumes
      - persistentvolumeclaims
      - secrets
    verbs:
      - get
      - list

  # Apps API Group
  - apiGroups: ["apps"]
    resources:
      - deployments
      - replicasets
      - statefulsets
      - daemonsets
    verbs:
      - get
      - list

  # Batch API Group
  - apiGroups: ["batch"]
    resources:
      - jobs
      - cronjobs
    verbs:
      - get
      - list

  # Storage API Group
  - apiGroups: ["storage.k8s.io"]
    resources:
      - storageclasses
      - volumeattachments
    verbs:
      - get
      - list

  # Discovery API Group
  - apiGroups: ["discovery.k8s.io"]
    resources:
      - endpointslices
    verbs:
      - get
      - list

  # Networking API Group
  - apiGroups: ["networking.k8s.io"]
    resources:
      - ingresses
      - networkpolicies
    verbs:
      - get
      - list

  # Autoscaling API Group
  - apiGroups: ["autoscaling"]
    resources:
      - horizontalpodautoscalers
    verbs:
      - get
      - list

  # Policy API Group
  - apiGroups: ["policy"]
    resources:
      - poddisruptionbudgets
    verbs:
      - get
      - list

  # RBAC API Group
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources:
      - roles
      - rolebindings
      - clusterroles
      - clusterrolebindings
    verbs:
      - get
      - list
```

需要特别说明的权限细节：

- `persistentvolumes`、`persistentvolumeclaims` 必须放在 Core API Group。
- `pods/log` 是 Pod 子资源，读取日志使用 `get`。
- `secrets` 当前示例保留 `get/list` 中的 `list` 需求，是因为现有实现涉及 Secret 元数据采集；但 Kubernetes API 的 Secret List 会返回完整 Secret 对象，不能将它理解为 API 层只返回元数据。应用必须继续确保 Secret 明文不进入 Evidence、LLM 上下文、报告和日志。
- 上述配置采用 `get/list`，但最终应根据实际 Reader 调用逐项核对：如果某资源只调用 List，就不应无依据增加其他动词；如果代码确实调用 Get，则需要保留 Get。
- 如果当前代码尚未实际接入某个资源的采集流程，不能仅因为 YAML 增加了权限，就认为该资源已经具备巡检能力。

## 四、ClusterRoleBinding.yaml 正确设计

建议使用如下配置：

```
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: AegisOps
  labels:
    app.kubernetes.io/name: AegisOps
subjects:
  - kind: ServiceAccount
    name: AegisOps
    namespace: aegisops
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: AegisOps
```

对应的 ServiceAccount 应为：

```
apiVersion: v1
kind: ServiceAccount
metadata:
  name: AegisOps
  namespace: aegisops
```

这里有几个关键点：

- ClusterRole 定义巡检所需的权限。
- ClusterRoleBinding 将 ClusterRole 授权给运行 AegisOps 的 ServiceAccount。
- ServiceAccount 的 Namespace 必须与实际工作负载使用的 ServiceAccount 一致。
- ClusterRoleBinding 必须绑定正确的 ClusterRole 名称。
- 由于巡检需要获取集群范围内的资源，因此应保持集群级授权关系。

不要为了追求表面上的最小权限，将全局巡检需要的资源权限拆成局部 Namespace 授权，导致 Nodes、PV、Namespace 等集群级资源无法正常采集。

## 五、建立巡检采集能力与 RBAC 的一致性校验

这是本次优化中非常重要的一项。

建议增加自动化测试，确保 Scanner 所需权限与 RBAC 配置保持一致。

### 5.1 资源权限映射

建议整理统一映射关系：

| 采集模块     | Kubernetes 资源                    | API Group                 | 预期权限 |
| ------------ | ---------------------------------- | ------------------------- | -------- |
| 集群基础信息 | Nodes                              | Core                      | get/list |
| Namespace    | Namespaces                         | Core                      | get/list |
| Pod          | Pods                               | Core                      | get/list |
| Pod 日志     | Pods/log                           | Core                      | get      |
| Workload     | Deployments / ReplicaSets          | apps                      | get/list |
| Workload     | StatefulSets / DaemonSets          | apps                      | get/list |
| 服务发现     | Services / Endpoints               | Core                      | get/list |
| 服务发现     | EndpointSlices                     | discovery.k8s.io          | get/list |
| 存储         | PV / PVC                           | Core                      | get/list |
| 存储         | StorageClasses / VolumeAttachments | storage.k8s.io            | get/list |
| 配置         | ConfigMaps                         | Core                      | get/list |
| 事件         | Events                             | Core                      | get/list |
| 网络         | Ingresses / NetworkPolicies        | networking.k8s.io         | get/list |
| 弹性伸缩     | HPA                                | autoscaling               | get/list |
| 调度与可用性 | PDB                                | policy                    | get/list |
| 权限分析     | Roles / RoleBindings               | rbac.authorization.k8s.io | get/list |
| 权限分析     | ClusterRoles / ClusterRoleBindings | rbac.authorization.k8s.io | get/list |

以上是目标核对清单，最终以代码中实际启用的采集项为准。

### 5.2 自动化测试

建议新增或完善 RBAC 测试：

1. 解析 `clusterrole.yaml`，而不是简单匹配 YAML 文本。
2. 校验资源与 API Group 的对应关系。
3. 校验 Scanner 实际需要的资源是否全部具备相应权限。
4. 校验资源所需动词是否完整。
5. 校验 ClusterRoleBinding 是否正确绑定 ServiceAccount 和 ClusterRole。
6. 新增采集器或 Reader 接口时，必须同步更新权限映射和测试。

特别要覆盖 PV/PVC 的 API Group，避免再次出现配置错误。

## 六、完善 RBAC 实际授权关系分析

当前 RBAC 风险分析不能仅根据 ClusterRole 中是否存在 `*` 就判断实际授权过度。

例如：

```
ClusterRole
    ↓
ClusterRoleBinding
    ↓
ServiceAccount
```

还可能存在：

```
Role
    ↓
RoleBinding
    ↓
User / Group / ServiceAccount
```

因此建议优化 `RbacOverPrivilegedRule`：

- 同时分析 ClusterRole、ClusterRoleBinding。
- 同时分析 Role、RoleBinding。
- 解析 Subject 类型及对应身份。
- 判断权限是集群级还是 Namespace 级。
- 结合实际 Binding 关系分析权限是否真正授予。
- 区分角色定义中的潜在高权限与实际生效的高权限。

不能只看到某个 ClusterRole 定义了 `*`，就直接认定集群中存在实际过度授权。

## 七、完善采集质量与报告呈现

RBAC 权限完善之后，还需要保证报告能够准确反映采集结果。

### 7.1 采集失败必须显式记录

例如：

```
资源类型：HorizontalPodAutoscalers
采集状态：失败
失败原因：Forbidden
影响范围：HPA 相关巡检规则无法执行
```

不能因为 HPA 无法采集，就把 HPA 相关检查结果当作正常。

### 7.2 引入或完善 CollectionQuality

建议采集质量至少包含：

| 指标               | 说明                         |
| ------------------ | ---------------------------- |
| ExpectedResources  | 预期采集的资源类型           |
| CollectedResources | 实际成功采集的资源类型       |
| FailedResources    | 采集失败的资源类型           |
| ForbiddenResources | 因 RBAC 权限不足而失败的资源 |
| CollectionCoverage | 采集覆盖率                   |
| CollectionErrors   | 采集错误详情                 |

### 7.3 健康评分必须考虑采集完整性

如果关键资源没有采集成功，就不能无条件给出 100 分。

例如：

```
集群健康评分：100 分
HPA 采集失败
PDB 采集失败
RBAC 采集失败
```

这种结果是不可信的。

报告应该明确区分：

- 资源健康。
- 采集完整性。
- 诊断可信度。

不能将“没有发现问题”解释为“资源一定正常”。

## 八、验收标准

建议从 YAML、权限、实际巡检三个层面验收。

### 8.1 YAML 配置验收

- PV/PVC API Group 修正为 Core API Group。
- HPA、PDB、RBAC 等资源权限补齐。
- ClusterRoleBinding 正确绑定 ServiceAccount。
- ClusterRole 名称与 Binding 引用一致。
- ServiceAccount Namespace 与实际部署配置一致。

### 8.2 Kubernetes 权限验收

使用以下命令检查关键资源权限：

```
kubectl auth can-i list nodes \
  --as=system:serviceaccount:aegisops:aegisops

kubectl auth can-i list persistentvolumes \
  --as=system:serviceaccount:aegisops:aegisops

kubectl auth can-i list persistentvolumeclaims \
  --as=system:serviceaccount:aegisops:aegisops

kubectl auth can-i list horizontalpodautoscalers.autoscaling \
  --all-namespaces \
  --as=system:serviceaccount:aegisops:aegisops

kubectl auth can-i list poddisruptionbudgets.policy \
  --all-namespaces \
  --as=system:serviceaccount:aegisops:aegisops

kubectl auth can-i list clusterroles.rbac.authorization.k8s.io \
  --as=system:serviceaccount:aegisops:aegisops

kubectl auth can-i list clusterrolebindings.rbac.authorization.k8s.io \
  --as=system:serviceaccount:aegisops:aegisops
```

### 8.3 实际巡检验收

- 快速巡检能够正常完成。
- 标准巡检能够正常完成。
- 深度巡检能够正常完成。
- PV/PVC 能够正常采集。
- HPA/PDB 能够正常采集。
- RBAC 资源能够正常采集。
- Pod 日志采集正常。
- 采集失败能够准确反映在 CollectionQuality。
- 采集失败不会被误判为资源健康。
- CLI 与 Web 的巡检结果保持一致。

## 九、最终实施要求

本次优化不应只修改两份 YAML 就结束，而应形成完整闭环：

```
Scanner 实际采集需求
        ↓
Reader API 调用核对
        ↓
RBAC 权限清单
        ↓
ClusterRole / ClusterRoleBinding
        ↓
RBAC 自动化一致性测试
        ↓
Kubernetes 实际权限验证
        ↓
巡检采集验证
        ↓
CollectionQuality 质量评估
        ↓
巡检报告准确呈现
```

最终要求：

1. 先检查当前 `clusterrole.yaml`、`clusterrolebinding.yaml` 和实际 Scanner/Reader 代码，不允许只根据本方案机械覆盖配置。
2. 修复 API Group 错误，补齐实际巡检所需权限。
3. 确保 ClusterRoleBinding 与实际运行 ServiceAccount 完全匹配。
4. 建立采集代码与 RBAC 权限之间的自动化一致性测试。
5. 确保资源采集失败能够被巡检报告准确识别。
6. 不允许通过降低巡检采集范围来规避权限问题。
