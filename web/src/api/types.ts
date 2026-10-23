/**
 * P2.3 API Client 类型层：与后端 `internal/server/response.go` 的统一响应信封、
 * 分页元数据及 `internal/server/auth.go` 认证端点契约保持一致。
 */

/** 统一错误对象（后端 APIError）。 */
export interface ApiErrorBody {
  code: string
  message: string
}

/** 稳定分页元数据（后端 Pagination）。 */
export interface PageMeta {
  page: number
  page_size: number
  total: number
  total_pages: number
}

/**
 * 列表查询参数。请求侧统一序列化为 `page` / `page_size`（后端同时兼容
 * `pageSize` 写法，这里按契约主用 snake_case 传输）。
 */
export interface PageQuery {
  page?: number
  pageSize?: number
  page_size?: number
}

/** 后端统一响应信封（apiResponse）。 */
export interface ApiResponse<T = unknown> {
  request_id: string
  timestamp: string
  data?: T
  error?: ApiErrorBody
  pagination?: PageMeta
}

/** 分页列表接口的 data 载体（PageResult[T]）。 */
export interface PageData<T> {
  items: T[]
}

export type PageResponse<T> = ApiResponse<PageData<T>>

/** 与后端 `response.go` 一一对应的通用错误码。 */
export const ErrorCode = {
  invalidArgument: 'invalid_argument',
  notFound: 'not_found',
  conflict: 'conflict',
  unauthorized: 'unauthorized',
  forbidden: 'forbidden',
  internal: 'internal_error',
} as const

export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode]

/** 认证端点（auth.go / store.User 结构化字段，PasswordHash 永不外泄）。 */
export interface AuthUser {
  id: string
  username: string
  displayName?: string
  roleId?: string
  status?: string
  createdAt: string
  updatedAt: string
}

export interface AuthRole {
  id: string
  name: string
  description?: string
  permissions: string[]
  createdAt: string
  updatedAt: string
}

/** 当前登录用户的 Kubernetes 有效资源范围。 */
export interface EffectiveScope {
  clusterId?: string
  namespaces: string[]
  clusterScoped: boolean
  isAdmin: boolean
  canWrite: boolean
  canDestroy: boolean
}

export interface LoginRequest {
  username: string
  password: string
}

export interface LoginResponse {
  token: string
  expiresAt: string
  user: AuthUser
}

/** POST /api/v1/auth/password 自助改密请求体（P6.12，需携带旧密码）。 */
export interface ChangePasswordRequest {
  oldPassword: string
  newPassword: string
}

export interface MeResponse {
  user: AuthUser
  role: AuthRole
}

/** 受管集群元数据（后端 store.Cluster；凭据已加密分离，永不随本对象回传）。 */
export interface Cluster {
  id: string
  name: string
  context?: string
  server?: string
  version?: string
  status?: string
  createdAt: string
  updatedAt: string
}

/** 一次 kubeconfig 解析 / 连测的结果（后端 cluster.Info）。 */
export interface ClusterInfo {
  name?: string
  context?: string
  server?: string
  version?: string
  health?: string
}

/** GET /api/v1/clusters/current 的 data（后端 cluster.StatusView）。 */
export interface ClusterStatusView {
  cluster: Cluster
  info: ClusterInfo
}

/** POST /api/v1/clusters 请求体：kubeconfig 仅作一次性提交，不回显。 */
export interface ClusterCreateRequest {
  name?: string
  kubeconfig: string
}

/** POST /api/v1/clusters/test 请求体。 */
export interface ClusterTestRequest {
  kubeconfig: string
}

/** Skill 单条声明式校验规则（后端 skills.Rule；不携带任何执行字段）。 */
export interface SkillRule {
  name: string
  path?: string
  condition: string
  severity: string
  message: string
  fix?: string
}

/** Skill 风险升档条款（后端 skills.RiskClause；只升不降，P3.8）。 */
export interface SkillRiskClause {
  name: string
  path?: string
  condition: string
  to: string
  message: string
}

/** Skill 风险元数据（后端 skills.RiskRule）。 */
export interface SkillRiskRule {
  default?: string
  elevateWhen?: SkillRiskClause[]
}

/** Skill 预/后置检查（后端 skills.Check）。 */
export interface SkillCheck {
  name: string
  params?: Record<string, string>
}

/** Skill 文档用途示例（后端 skills.Example）。 */
export interface SkillExample {
  name?: string
  purpose?: string
  content?: string
}

/** procedural Skill 技能包内资产元数据（后端 skills.SkillAsset；只记录元数据，
 * 绝不携带执行内容；scripts 一律 inert=true，永不进入工具路径）。 */
export interface SkillAsset {
  name: string
  path: string
  kind?: string
  size?: number
  sha256?: string
  inert?: boolean
}

/** 声明式 Skill 规格（后端 skills.Skill；刻意与运行元数据分离）。 */
export interface SkillSpec {
  author?: string
  description?: string
  name: string
  /** Skill 类型（P3.8）：structured=资源创建规则 / procedural=标准 SKILL.md 方法论。 */
  type: 'structured' | 'procedural' | string
  domain: string
  version: string
  kinds: string[]
  /** procedural Skill 的 SKILL.md 正文（P3.8.2）：仅作文本参考注入，从不执行。 */
  body?: string
  /** procedural Skill 技能包资产元数据（P3.8.3）。 */
  assets?: SkillAsset[]
  requiredInputs?: string[]
  optionalInputs?: string[]
  validation?: SkillRule[]
  risk: SkillRiskRule
  preChecks?: SkillCheck[]
  postChecks?: SkillCheck[]
  examples?: SkillExample[]
  source: string
  path?: string
}

/** Skill 平台记录（后端 store.Skill）：规格 + 运行元数据（enabled/hit 统计）。 */
export interface Skill {
  name: string
  spec: SkillSpec
  source: string
  enabled: boolean
  validationStatus?: string
  hitCount: number
  lastHit?: string
  lastValidation?: string
  createdAt: string
  updatedAt: string
}

/** 配置中心：键取值类型（后端 settings.Kind）。 */
export type SettingsKind =
  | 'string'
  | 'bool'
  | 'int'
  | 'float'
  | 'duration'
  | 'stringList'
  | 'json'

/** 配置中心单个配置键视图。敏感键（api_key/kubeconfig/mcp）的 value 恒为 null，
 * 只暴露 hasValue（是否已配置），后端落库存静态加密，前端永不回显明文（P3.9）。 */
export interface SettingsItem {
  key: string
  kind: SettingsKind
  value: unknown
  hasValue: boolean
  sensitive?: boolean
  default?: unknown
  enum?: string[]
  /** 配置取值示例，用于配置中心提示格式而不要求用户猜。 */
  example?: string
  /** 数值配置的下限；后端按条目类型返回。 */
  min?: number
  /** 数值配置的上限；未设置时省略。 */
  max?: number
  source?: string
  desc?: string
  updatedAt?: string
}

/** GET /api/v1/settings/{group} 的 data（后端 settings.GroupView）。 */
export interface SettingsGroupView {
  group: string
  /** 服务端是否已配置 K8S_AI_ENC_KEY；false 时敏感配置只能读状态不能保存。 */
  encryptionAvailable?: boolean
  items: SettingsItem[]
}

/** 用户账号状态（后端 auth 仅开放 active | disabled；内置 admin 受保护）。 */
export type UserStatus = 'active' | 'disabled' | string

/** 授权委派主体类型（P3.11 后端放行 user | group）。 */
export type GrantPrincipalType = 'user' | 'group' | string

/** 授权委派权限白名单（P3.11 后端仅放行 * 全量 与 destroy 销毁委派）。 */
export type GrantPermission = '*' | 'destroy' | string

/** API Token 生命周期状态（active | revoked | disabled）。 */
export type TokenStatus = 'active' | 'revoked' | 'disabled' | string

/** POST /api/v1/users 请求体（auth.CreateUserRequest；PasswordHash 由 json:"-" 挡掉）。 */
export interface CreateUserRequest {
  username: string
  displayName?: string
  password: string
  roleId: string
  status?: string
}

/** PATCH /api/v1/users/{id} 部分更新请求体（auth.UpdateUserRequest）。 */
export interface UpdateUserRequest {
  displayName?: string
  password?: string
  roleId?: string
  status?: string
}

/** namespace 授权/销毁委派（后端 authz.Grant；不含任何敏感实体）。 */
export interface AuthzGrant {
  id: string
  principalType: GrantPrincipalType
  principalId: string
  clusterId?: string
  namespaces: string[]
  permissions: string[]
  expiresAt?: string
  createdBy?: string
  createdAt: string
  updatedAt: string
}

/** POST /api/v1/grants 请求体（authz.CreateGrantRequest）。 */
export interface CreateGrantRequest {
  principalType: GrantPrincipalType
  principalId: string
  clusterId?: string
  namespaces: string[]
  permissions: string[]
  expiresAt?: string
}

/** PATCH /api/v1/grants/{id} 部分更新请求体（authz.UpdateGrantRequest；
 * expiresAt 省略表示保持原值）。 */
export interface UpdateGrantRequest {
  clusterId?: string
  namespaces?: string[]
  permissions?: string[]
  expiresAt?: string
}

/** 用户 API Token 元数据（authz.Token；TokenHash 永不回传）。 */
export interface ApiToken {
  id: string
  userId: string
  name?: string
  expiresAt?: string
  status?: TokenStatus
  lastUsedAt?: string
  createdAt: string
  updatedAt: string
}

/** POST /api/v1/users/{userId}/tokens 请求体（authz.CreateTokenRequest）。 */
export interface CreateTokenRequest {
  name: string
  expiresAt?: string
}

/** 签发 API Token 的响应：明文 token 仅此一次返回，服务端只存 SHA-256（authz.CreateTokenResult）。 */
export interface CreateTokenResult {
  apiToken: ApiToken
  token: string
}
