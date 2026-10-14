/**
 * P2.8 结构化渲染常量与工具函数。
 * 三期老 UI（internal/webui/index.html）的 TL_COLUMN_LABELS /
 * TL_LIST_COLUMNS / TL_KV_FIELDS / statusPillClass 迁入组件化层，
 * 列按 kind 挑选，单元格统一走「脱敏后转义」渲染（Vue 插值自带转义）。
 */

/** K8s 常见字段的展示名（未覆盖时原样回退）。 */
export const TL_COLUMN_LABELS: Record<string, string> = {
  name: '名称',
  namespace: '命名空间',
  phase: '阶段',
  nodeName: '节点',
  restartCount: '重启',
  readyContainers: '就绪',
  totalContainers: '容器',
  podIP: 'Pod IP',
  qosClass: 'QoS',
  status: '状态',
  state: '状态',
  type: '类型',
  reason: '原因',
  message: '信息',
  count: '次数',
  ready: '就绪',
  image: '镜像',
  lastTimestamp: '时间',
  firstTimestamp: '时间',
  kind: '类型',
  unschedulable: '不可调度',
  storageClass: '存储类',
  capacity: '容量',
  accessModes: '访问模式',
  provisioner: '供给者',
  reclaimPolicy: '回收策略',
  volumeBindingMode: '绑定模式',
  containername: '容器名',
  address: '地址',
  value: '值',
  key: '键',
  effect: '效果',
  lastExitCode: '上次退出码',
  startTime: '启动时间',
  conditions: '条件',
  containerstate: '容器状态',
}

/** 各类列表的列顺序；未命中时走启发式列探测。 */
export const TL_LIST_COLUMNS: Record<string, string[]> = {
  PodList: ['name', 'namespace', 'phase', 'restartCount', 'readyContainers', 'totalContainers'],
  PodCandidates: ['namespace', 'name', 'phase'],
  NodeList: ['name', 'unschedulable'],
  NodeUsageList: ['nodeName', 'cpu', 'memory', 'pods'],
  EventList: ['lastTimestamp', 'type', 'reason', 'message', 'count'],
  EndpointSliceList: ['namespace', 'name'],
  PersistentVolumeClaimList: ['namespace', 'name', 'phase', 'status', 'storageClass'],
  ServiceList: ['namespace', 'name', 'type', 'clusterIP', 'ports'],
  NamespaceList: ['name', 'phase'],
}

/** 单对象键值视图优先展示的顺序（仅取标量值，对象字段折叠）。 */
export const TL_KV_FIELDS: string[] = [
  'name',
  'namespace',
  'phase',
  'nodeName',
  'podIP',
  'restartCount',
  'readyContainers',
  'totalContainers',
  'qosClass',
  'status',
  'state',
  'storageClass',
  'unschedulable',
  'startTime',
]

/** 状态 Pill 语义色：ok / pending / error / warning / normal。 */
export type StatusTone = 'ok' | 'pending' | 'error' | 'warning' | 'normal'

export function statusPillClass(value: unknown): StatusTone {
  const s = String(value ?? '').toLowerCase()
  if (/running|ready|active|bound|succeeded|available/i.test(s)) return 'ok'
  if (/pending|waiting|terminated|unknown|initializing/i.test(s)) return 'pending'
  if (/error|fail|crash|denied|notready|unhealthy|imagepull|canceled/i.test(s)) return 'error'
  if (/warning|degraded/i.test(s)) return 'warning'
  return 'normal'
}

/** 字段 key → 展示名。 */
export function tlLabel(key: string): string {
  return TL_COLUMN_LABELS[key] || key
}

/** CR 状态 Pill（create_resource 两段式状态语义）。 */
export function crStatusTone(status: unknown): StatusTone {
  const st = String(status ?? 'pending').toLowerCase()
  if (st === 'validated' || st === 'applied' || st === 'ready') return 'ok'
  if (st === 'partial' || st === 'pending' || st === 'waiting') return 'pending'
  if (/rejected|failed|error|notready/.test(st)) return 'error'
  return 'normal'
}

/** 剥离 `<untrusted_k8s_data>` 包装（对齐老 UI stripUntrusted）。 */
export function stripUntrusted(text: unknown): string {
  return String(text ?? '')
    .replace(/^\s*<untrusted_k8s_data>\s*/i, '')
    .replace(/\s*<\/untrusted_k8s_data>\s*$/i, '')
    .trim()
}

/** 宽松 JSON 解析：能解析则返回对象，否则原文本。 */
export function parseJsonLoose(value: unknown): unknown {
  if (typeof value !== 'string') {
    return value
  }
  try {
    return JSON.parse(value) as unknown
  } catch {
    return value
  }
}
