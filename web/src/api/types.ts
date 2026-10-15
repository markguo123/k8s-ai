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

export interface LoginRequest {
  username: string
  password: string
}

export interface LoginResponse {
  token: string
  expiresAt: string
  user: AuthUser
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
