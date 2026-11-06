# AegisOps 轻量身份认证与权限体系重构实施方案

## 1. 项目背景

项目：AegisOps
仓库：https://github.com/markguo123/AegisOps.git
分支：DEV-v4.0.0

AegisOps 是一个独立运行的云原生基础设施智能运维 Agent 工作台。

产品核心价值在 Agent、Tool、Skill、Scan、Approval、Code Guard、Executor 等智能运维能力，不是 IAM 身份管理系统。

本次目标是重构并简化用户身份认证和权限体系，参考 Argo CD 的成熟实现，支持本地登录和 OIDC SSO，删除语义不明确的 operator 角色，确保多集群资源授权正确。

**重要：本次不是重新开发一套 IAM，也不是重做 Agent Runtime。优先复用现有代码，控制改造范围。**

## 2. 设计原则（必须遵守）

1. 本地账号密码登录作为独立部署和应急保底方式。
2. 支持标准 OIDC Authorization Code + PKCE 单点登录。
3. OIDC 只负责 AegisOps 用户身份认证，不与 Kubernetes 集群绑定。
4. 所有认证方式最终映射为 AegisOps 内部 UserID。
5. AegisOps 自己负责平台角色、集群、Namespace 和操作权限判定。
6. 当前采用单实例单工作空间模型，不引入完整 Tenant 多租户体系。
7. 内置角色仅保留 admin、user，彻底删除 operator。
8. 不设计自定义角色编辑器，不开发复杂组织架构、部门、用户生命周期管理。
9. 保留现有 AuthzGrant 的资源授权机制，修复授权边界和多集群上下文问题。
10. 不影响现有 Agent、Tool、Skill、Session、Approval、Audit、Scan 等业务能力。
11. 不允许通过前端隐藏菜单代替后端鉴权。
12. 不允许为了简化用户体系而削弱巡检采集、Agent 工具调用或安全执行能力。

## 3. Argo CD 源码借鉴要求（明确执行）

参考项目：

https://github.com/argoproj/argo-cd

官方文档：

- https://argo-cd.readthedocs.io/en/stable/operator-manual/user-management/
- https://argo-cd.readthedocs.io/en/stable/operator-manual/rbac/
- https://argo-cd.readthedocs.io/en/stable/operator-manual/security/

重点研究源码：

- util/oidc/
- util/rbac/
- util/jwt/
- assets/builtin-policy.csv
- RBAC Model 配置
- 相关登录、Session、Token 安全实现和测试。

要求：

1. 先确定采用的 Argo CD 稳定版本或 Tag，不能直接追踪不固定的 master 分支。
2. 对源码进行依赖分析，识别可以直接复用的独立代码。
3. 对依赖 Argo CD 内部包、Kubernetes ConfigMap、Secret、gRPC 等组件的代码，不得盲目整文件复制。
4. 对能够独立复用的模块，允许直接复制到 AegisOps，并保留原始版权声明、Apache-2.0 LICENSE 和必要的 NOTICE。
5. 对无法直接复用的代码，按照 Argo CD 的成熟实现方式进行移植。
6. 不需要担心 Apache-2.0 商业使用问题
7. 不要把 Argo CD 整个 Casbin RBAC 业务模型原样搬入 AegisOps。AegisOps 有自己的 Cluster / Namespace / Resource / Action 授权模型。

Argo CD 的 RBAC 基于 Casbin，可以研究其模型、策略解析、策略验证、用户/组到角色映射和策略匹配机制。是否引入 Casbin，必须先评估能否复用现有 authz，不允许同时维护两套互相冲突的授权系统。

## 4. 角色体系调整

最终只保留两个内置角色。

### admin

拥有 AegisOps 平台管理权限：

- 用户管理
- 认证配置管理
- 集群接入和管理
- 用户及用户组授权管理
- 平台配置管理
- Skill 和插件管理
- 审批和审计管理
- 平台全部管理功能

### user

普通工作台使用权限：

- Agent 对话
- 授权范围内的集群访问
- 巡检、诊断和报告
- 使用允许的 Skill 和 Tool
- 在授权范围内发起变更申请
- 查看自身会话、任务和授权范围内的数据

### 删除 operator

全面清理 operator 角色：

- internal/auth/service.go
- internal/auth/users.go
- internal/permission/
- Web permission.ts
- 用户管理页面
- 数据库 Seed / Migration
- 默认配置
- 测试和 Fixture
- 文档和示例

历史 operator 用户不用关注，目前是测试阶段，可以直接删除，不用考虑迁移问题

Kubernetes Operator 等技术概念不属于本次清理范围。

## 5. 本地身份认证

保留当前 AegisOps 本地用户认证能力。

要求：

1. 本地用户存储继续使用 MySQL。
2. 密码使用安全哈希，不存明文。
3. 复用当前 Session 管理体系。
4. 登录失败限制和防暴力破解。
5. 禁用用户后禁止新登录。
6. 修改密码、重置密码和禁用用户时，正确处理有效 Session 撤销。
7. 初始化时保证至少存在一个可用 admin。
8. 保留本地应急管理员登录能力。
9. 不开发用户注册、邮箱找回、复杂密码策略控制台等 IAM 功能。

本地账号可以与 OIDC 用户关联，但必须明确身份绑定流程，不能因为 email 相同自动合并账号。

## 6. OIDC 单点登录

第一版只支持一个 OIDC Provider。

支持配置：

- enabled
- issuer_url
- client_id
- client_secret
- redirect_url
- scopes
- username_claim
- email_claim
- display_name_claim
- groups_claim（第一版只读取，不自动授予角色或资源权限）

采用标准 Authorization Code Flow + PKCE。

必须实现：

1. OIDC Discovery。
2. Authorization URL 构建。
3. state 校验。
4. nonce 校验。
5. PKCE verifier/challenge。
6. Callback 处理。
7. Authorization Code 换取 Token。
8. ID Token 签名校验。
9. Issuer 校验。
10. Audience 校验。
11. Expiration / IssuedAt 等必要时间校验。
12. 用户 Claim 提取。
13. 内部 UserID 映射。
14. 登录成功后签发 AegisOps 自己的 Session。
15. 失败场景有明确日志和用户提示，但不泄露 Token、Secret。

OIDC 身份唯一标识必须使用 issuer + sub。

不得使用 email 或 username 作为 OIDC 身份唯一主键。

首次登录时：

- 查找 issuer + sub。
- 已关联则使用对应内部 UserID。
- 未关联则自动创建普通用户。
- 建立 user_identities 关联记录。
- 不自动创建 admin。
- 不自动授权 Kubernetes 集群。
- 不自动把 OIDC Group Claim 转换成平台管理员或集群权限。

OIDC 用户的禁用状态必须由 AegisOps 内部用户状态控制。

启用 OIDC 后：

- 默认展示企业 SSO 登录入口。
- 本地应急管理员入口保留。
- 本地应急账号不能被 OIDC Claim 覆盖。
- OIDC 不可用时，已配置的本地应急恢复机制仍可使用。

## 7. 数据模型

优先评估现有 users 表和 authz 表，不要无意义重建。

### users

保留现有字段，必要时补充：

- id
- username
- display_name
- email
- role（admin / user）
- status（active / disabled）
- password_hash（OIDC-only 用户允许为空）
- auth_source（local / oidc / mixed）
- created_at
- updated_at

### user_identities

新增身份关联表：

- id
- user_id
- provider
- issuer
- subject
- created_at

约束：

- UNIQUE(issuer, subject)
- user_id 有索引
- 删除用户时按既有数据保留策略处理身份关联
- 同一外部身份不能绑定多个内部用户

不要在当前阶段引入 Tenant、TenantMembership、组织架构、部门等实体。

## 8. AuthzGrant 与多集群授权

沿用现有 AuthzGrant。

重点修复：

1. EffectiveScope 的 ClusterID 匹配逻辑。
2. sessionapi/scope_resolver.go 中 ClusterID 为空的问题。
3. User Grant 和 Group Grant 的实际计算问题。
4. Namespace 授权边界。
5. destroy 等独立操作能力的授权判定。
6. 集群切换时授权上下文残留。
7. Session、Approval、Audit、ScanTask 与 ClusterID 的关联。

必须明确：

- OIDC 登录不绑定 Kubernetes 集群。
- 用户登录一次，可以访问多个有权限的集群。
- 集群接入由 admin 管理。
- 用户只能看到和访问被授权的集群。
- 第一版不实现普通用户任意接入、共享或转让个人集群。
- 每次资源操作必须有明确 ClusterID。
- 禁止使用空 ClusterID 作为通配或隐式当前集群。
- 不允许 LLM 或前端自行指定可信 UserID、Role、授权范围。
- 平台托管 Kubernetes 身份的 RBAC/SAR 不能替代 AegisOps 内部用户授权。

保留现有资源操作授权粒度。不能将 destroy 直接归入 admin/user 角色，也不能绕过 Approval、Code Guard、Executor 安全检查。

## 9. Session 与 Agent 执行上下文

认证方式变化不能影响 Agent Runtime。

所有认证入口最终产生统一内部身份。

Agent 每次 Tool 调用必须能够取得：

- UserID
- Role
- SessionID
- ClusterID
- Namespace
- Resource
- Action

这些上下文必须由服务端可信构建。

Session 创建时明确关联当前用户。Cluster 切换时必须重新计算资源授权范围。

每次 Tool 调用执行：

用户 Session 校验
→ 内部用户状态校验
→ 平台权限校验
→ Cluster 授权校验
→ Namespace / Resource / Action 授权校验
→ Kubernetes 执行身份能力检查
→ Approval / Code Guard
→ Executor
→ 执行结果验证
→ Audit

不得为了兼容 OIDC 而修改 Agent 推理循环和 Tool 协议，除非确实发现身份上下文传递存在缺陷。

## 10. 前端调整

登录页：

- 未启用 OIDC：展示本地账号密码登录。
- 启用 OIDC：默认展示企业 SSO 按钮。
- 本地应急管理员入口保留。
- 登录失败展示明确错误。
- 不暴露敏感配置。

用户管理：

- 仅 admin 可管理用户。
- 显示用户名、显示名称、来源（local/oidc/mixed）、角色、状态。
- 支持禁用/启用用户。
- 支持修改角色 admin/user。
- 支持查看身份关联。
- 不做复杂 IAM 管理界面。

OIDC 配置：

- 仅 admin 可配置。
- 支持配置、测试连接、启用、禁用。
- Client Secret 脱敏显示。
- 修改配置后正确处理缓存和现有会话。
- 保存前验证必要参数。

角色菜单：

- 彻底移除 operator。
- 不再通过前端硬编码角色判断复杂功能。
- 前端根据后端有效权限控制显示。
- 后端始终进行真实鉴权。

集群管理：

- 管理员接入和管理集群。
- 管理员为用户授权集群和 Namespace。
- 用户仅看到自己有权访问的集群。
- 集群选择器与登录身份无关。

## 11. 配置与部署

支持环境变量或现有配置中心配置 OIDC。

敏感信息：

- Client Secret 不得明文记录日志。
- 不得返回给普通前端。
- 不得存入普通审计详情。
- 优先使用现有加密配置能力或 Secret 管理方式。
- 配置缺失时 OIDC 自动保持禁用。
- OIDC 禁用后本地登录仍然正常。

配置变更必须正确刷新 Provider、Discovery 缓存和相关会话处理逻辑。

## 12. 安全要求

必须覆盖：

- 登录失败限流。
- CSRF / state 防护。
- OIDC nonce 防重放。
- PKCE。
- ID Token 完整校验。
- Session 固定攻击防护。
- 安全 Cookie 设置。
- 登录后 Session 更新。
- 用户禁用后的 Session 撤销。
- 管理员权限不能由 OIDC Claim 自动获得。
- 外部身份不能因 email 相同自动合并。
- 不能信任前端传入的 UserID、Role、ClusterID 授权结果。
- OIDC Secret、Token、密码不得出现在日志。
- 高风险操作仍需 Approval / Code Guard。
- 认证失败不能降级成匿名访问。

## 13. 历史数据迁移

迁移必须幂等、可回滚，并在执行前备份。

重点：

1. operator 用户统一转换为 user。
2. 原有 admin 保留。
3. 原有普通用户保留。
4. 原有 AuthzGrant 按用户原样保留，进行有效性检查。
5. 不自动扩大任何用户的权限。
6. 不破坏历史 Session、Approval、Audit、ScanTask、Report 关联。
7. 增加 user_identities 表。
8. 历史用户默认不创建虚构 OIDC 身份。
9. 密码哈希字段必须兼容现有本地用户。
10. 数据库升级必须有明确版本记录。

## 14. 文档统一

代码完成后统一修改：

- docs/02-架构与设计/08-安全设计.md
- docs/02-架构与设计/02-数据模型与数据字典.md
- 用户认证设计文档
- 部署配置文档
- OIDC 配置说明
- API / Session / Authz 相关说明
- 开发计划中的用户权限相关描述

文档统一为：

“AegisOps 采用轻量内部身份与资源授权体系，支持本地认证和 OIDC 外部身份认证。认证与授权解耦，所有用户统一映射为内部 UserID。当前内置 admin/user 两种角色，多集群通过 Cluster / Namespace / Action 级授权控制。”

删除旧的 operator 角色设计、旧用户权限口径和互相矛盾的安全描述。

不要在文档中引入没有实际实现的功能。

## 15. 测试要求

必须补充自动化测试。

### 本地认证

- admin 登录。
- user 登录。
- 密码错误。
- 用户禁用。
- 登录失败限流。
- Session 撤销。
- 初始化管理员保护。

### OIDC

- 正常登录。
- state 错误。
- nonce 错误。
- PKCE 校验失败。
- Issuer 错误。
- Audience 错误。
- Token 签名错误。
- Token 过期。
- 首次用户自动创建。
- 已关联用户登录。
- 同名 email 不自动合并。
- OIDC 用户禁用。
- OIDC 配置禁用后本地登录。
- OIDC 故障时本地应急管理员登录。

### 角色

- admin 管理平台。
- user 无法管理平台。
- operator 不再存在。
- 历史 operator 降级为 user。
- 不因角色迁移扩大资源授权。

### 多集群授权

- 用户只访问授权集群。
- 用户访问未授权集群被拒绝。
- 集群 A 授权不影响集群 B。
- 切换集群后重新计算授权。
- ClusterID 为空时拒绝资源操作。
- Namespace 越权被拒绝。
- Group Grant 正确生效。
- destroy 未授权时拒绝。
- 已授权 destroy 仍经过 Approval / Code Guard。
- 不允许跨集群 Session 授权污染。

### 回归

执行：

- go test ./...
- go vet ./...
- 前端构建与类型检查
- 数据库 Migration 测试
- 现有 Agent / Tool / Scan / Approval 相关回归测试

## 16. 实施方式

不要直接大范围修改代码。

第一步：代码审计。

输出：

- 当前认证链路。
- 当前用户数据结构。
- 当前 Session 签发与验证方式。
- 当前 OIDC 相关能力（如果已有）。
- 当前角色引用完整清单。
- AuthzGrant 与 ScopeResolver 的问题清单。
- 可直接复用的 Argo CD 源码及依赖分析。
- 预计改动文件清单。
- 数据迁移风险。

第二步：方案与代码口径确认。

先统一设计文档、数据字典和迁移方案。

第三步：实施。

优先复用当前 auth、authz、Session 和配置中心。

第四步：测试。

确保认证、授权和 Agent 执行链路完整。

第五步：清理。

删除 operator 所有残留代码、测试、配置和文档。

第六步：文档统一。

更新实际实现，不保留过期设计。

## 17. 验收标准

最终必须达到：

1. 本地账号登录正常。
2. OIDC SSO 登录正常。
3. 本地和 OIDC 用户统一映射内部 UserID。
4. OIDC 不绑定任何 Kubernetes 集群。
5. 一次登录可切换多个授权集群。
6. 只保留 admin/user 两个角色。
7. operator 角色和其遗留判断全部清理。
8. 用户集群授权准确。
9. ClusterID 成为强制资源授权边界。
10. Agent Tool 执行上下文可信。
11. Approval、Audit、Session 等原业务功能不受破坏。
12. 不引入完整 IAM 或多租户系统。
13. 所有测试通过。
14. 文档和代码实现一致。

完成后提供：

- 架构调整总结。
- 实际修改文件清单。
- Argo CD 代码复用清单和许可证说明。
- 数据库迁移说明。
- OIDC 部署配置示例。
- 测试结果。
- 尚未完成事项及风险。

**最终目标：用尽可能少的代码建立稳定的身份认证入口和清晰的资源授权边界，把研发精力留给 AegisOps 的 Agent 运维能力。**
