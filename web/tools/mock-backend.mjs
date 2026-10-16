/**
 * 前端联调/视觉核验用 mock 后端（P2 视觉验收工具，不参与生产构建）。
 *
 * 复刻 webui 的会话端点与 SSE 帧格式（历史快照 + 统一事件），
 * 用来在没有集群/LLM 的环境里渲染完整回合（思考 → 工具 → 审批 → 报告）。
 *
 * 用法：
 *   node tools/mock-backend.mjs            # 监听 127.0.0.1:8081，与 vite 代理默认端口一致
 *   npm run dev                            # 另开一个终端
 */
import { createServer } from 'node:http'

const PORT = Number(process.env.MOCK_PORT || 8081)

const SESSION = {
  id: 'demo-1',
  name: '诊断 default 命名空间下 web-deployment 不稳定',
  state: 'running',
  model: 'qwen3:8b',
  messageCount: 4,
  lastModified: new Date().toISOString(),
  token: { prompt_tokens: 21400, completion_tokens: 15020, total_tokens: 36420 },
}

const TOOL_PODS = {
  items: [
    { name: 'web-deployment-7d9f6c4d-8lkwj', phase: 'Running', restartCount: 2, nodeName: 'worker-01' },
    { name: 'web-deployment-7d9f6c4d-5mvzu', phase: 'CrashLoopBackOff', restartCount: 9, nodeName: 'worker-02' },
    { name: 'web-deployment-7d9f6c4d-2tqpc', phase: 'Running', restartCount: 1, nodeName: 'worker-01' },
  ],
  total: 3,
}

/** 受管集群 mock（P3.4 联调：与 internal/cluster 模型一致，不含凭据）。 */
const CLUSTER = {
  id: 'cluster-demo-1',
  name: 'prod-cluster-01',
  context: 'prod-admin',
  server: 'https://10.0.0.1:6443',
  version: 'v1.30.2',
  status: 'connected',
  createdAt: new Date().toISOString(),
  updatedAt: new Date().toISOString(),
}

const CLUSTER_VIEW = {
  cluster: CLUSTER,
  info: { name: 'prod-cluster-01', context: 'prod-admin', server: 'https://10.0.0.1:6443', version: 'v1.30.2', health: 'ok' },
}

/** 示例 Skill 列表（P3.8.4 @ 引用联调，与后端 store.Skill 结构一致）。 */
const SKILLS = [
  {
    name: 'k8s-mysql-troubleshoot',
    spec: {
      author: 'demo',
      description: 'Kubernetes 上 MySQL 故障排查与诊断方法论',
      name: 'k8s-mysql-troubleshoot',
      type: 'procedural',
      domain: 'database',
      version: '1.0.0',
      kinds: [],
      risk: { default: 'medium' },
      source: 'custom',
    },
    source: 'custom',
    enabled: true,
    validationStatus: 'valid',
    hitCount: 3,
    lastHit: new Date().toISOString(),
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  },
  {
    name: 'create-workload',
    spec: {
      author: 'builtin',
      description: '工作负载资源创建的声明式校验规则',
      name: 'create-workload',
      type: 'structured',
      domain: 'workload',
      version: '1.0.0',
      kinds: ['apps/v1/Deployment', 'apps/v1/StatefulSet'],
      risk: { default: 'medium' },
      source: 'builtin',
    },
    source: 'builtin',
    enabled: true,
    validationStatus: 'valid',
    hitCount: 7,
    createdAt: new Date().toISOString(),
    updatedAt: new Date().toISOString(),
  },
]

const LOGS = [
  '2026-10-14T10:12:03Z INFO  server started :8080',
  '2026-10-14T10:12:05Z WARN  memory usage 92% (512Mi limit)',
  '2026-10-14T10:12:07Z WARN  GC pause 480ms',
  '2026-10-14T10:12:09Z PANIC fatal: out of memory',
  '2026-10-14T10:12:10Z process exited with code 137',
].join('\n')

/** 一个完整回合：思考 → 工具（列表/日志/创建）→ 审批 → 报告。 */
const TURN_EVENTS = [
  { event_type: 'turn.started', turn_id: 't-demo', data: { input: 'web-deployment 最近经常不稳定，帮我查一下 default 命名空间的情况，看是什么原因。' } },
  { event_type: 'reason.started', turn_id: 't-demo', step_id: 's-1', data: { kind: 'reason' } },
  {
    event_type: 'message.delta',
    turn_id: 't-demo',
    step_id: 's-1',
    data: { role: 'reasoning', delta: '用户反馈 web-deployment 不稳定。先获取工作负载运行状态与事件，再检查副本、重启、就绪探测，最后结合资源与网络层下结论。' },
  },
  { event_type: 'reason.completed', turn_id: 't-demo', step_id: 's-1', data: { kind: 'reason', status: 'done' } },
  { event_type: 'tool.started', turn_id: 't-demo', step_id: 's-2', data: { kind: 'tool', tool: 'list_pods', risk: 'SAFE', params: '{"namespace":"default"}' } },
  { event_type: 'observation.created', turn_id: 't-demo', step_id: 's-2', data: { tool: 'list_pods', ok: true, result: TOOL_PODS } },
  { event_type: 'tool.completed', turn_id: 't-demo', step_id: 's-2', data: { kind: 'tool', tool: 'list_pods', status: 'ok', risk: 'SAFE', result: TOOL_PODS } },
  { event_type: 'tool.started', turn_id: 't-demo', step_id: 's-3', data: { kind: 'tool', tool: 'get_logs', risk: 'SAFE', params: '{"namespace":"default","name":"web-deployment-7d9f6c4d-5mvzu","tail":40}' } },
  { event_type: 'observation.created', turn_id: 't-demo', step_id: 's-3', data: { tool: 'get_logs', ok: true, result: LOGS } },
  { event_type: 'tool.completed', turn_id: 't-demo', step_id: 's-3', data: { kind: 'tool', tool: 'get_logs', status: 'ok', risk: 'SAFE', result: LOGS } },
  {
    event_type: 'tool.started',
    turn_id: 't-demo',
    step_id: 's-4',
    data: {
      kind: 'tool',
      tool: 'create_resource',
      risk: 'HIGH',
      params: JSON.stringify({
        namespace: 'default',
        status: 'dry_run validated',
        manifest: [
          'apiVersion: apps/v1',
          'kind: Deployment',
          'metadata:',
          '  name: web-deployment',
          '  namespace: default',
          'spec:',
          '  replicas: 3',
          '  template:',
          '    spec:',
          '      containers:',
          '      - name: web',
          '        resources:',
          '          limits:',
          '            memory: 1Gi',
          '            cpu: 500m',
        ].join('\n'),
      }),
    },
  },
  {
    event_type: 'observation.created',
    turn_id: 't-demo',
    step_id: 's-4',
    data: { tool: 'create_resource', ok: true, result: 'dry-run 校验通过（1 个对象），等待人工确认' },
  },
  {
    event_type: 'tool.completed',
    turn_id: 't-demo',
    step_id: 's-4',
    data: { kind: 'tool', tool: 'create_resource', status: 'ok', risk: 'HIGH', result: 'dry-run 校验通过（1 个对象），等待人工确认' },
  },
  {
    event_type: 'approval.required',
    turn_id: 't-demo',
    step_id: 's-5',
    data: {
      prompt: 'create_resource 将对 Deployment/web-deployment（default）执行 apply：提升内存 limit 至 1Gi 并补探针。',
      options: [
        { label: '确认并执行', value: 'yes' },
        { label: '本次会话都允许', value: 'always' },
      ],
      tool: 'create_resource',
      risk: 'HIGH',
      params: 'mode=dry_run → apply（两段式）· Grant 未命中写白名单 · 需自确认',
      resource: { kind: 'Deployment', name: 'web-deployment', namespace: 'default' },
      reason: 'Grant 未命中写白名单 · 需自确认',
    },
  },
  {
    event_type: 'message.completed',
    turn_id: 't-demo',
    step_id: 's-6',
    data: {
      role: 'assistant',
      index: 0,
      delta: [
        '### 诊断结论：web-deployment 不稳定根因',
        '',
        '- 副本中 1 个处于 `CrashLoopBackOff`，原因链为 `OOMKilled` → 内存超限被杀（退出码 137）。',
        '',
        '| 副本 | 状态 | 重启 | 根因 |',
        '| --- | --- | --- | --- |',
        '| …-8lkwj | Running | 2 | - |',
        '| …-5mvzu | CrashLoopBackOff | 9 | OOMKilled |',
        '',
        '> 建议修复：将内存 limit 从 512Mi 提升至 1Gi 并补充 liveness 探针；本轮已生成 dry-run 通过的 manifest，待你确认后执行 apply。',
        '',
        '- 已完成：采集工作负载状态 / 事件 / 日志',
        '- 未执行：资源变更（等待人工确认）',
      ].join('\n'),
    },
  },
  { event_type: 'turn.completed', turn_id: 't-demo', data: { status: 'done', token: { prompt_tokens: 300, completion_tokens: 120, total_tokens: 420 } } },
]

function writeJson(res, status, body) {
  res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8' })
  res.end(JSON.stringify(body))
}

function readBody(req) {
  return new Promise((resolve) => {
    let raw = ''
    req.on('data', (chunk) => {
      raw += chunk
    })
    req.on('end', () => {
      try {
        resolve(raw ? JSON.parse(raw) : {})
      } catch {
        resolve({})
      }
    })
  })
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url ?? '/', 'http://127.0.0.1')
  const path = url.pathname

  // P3.4 集群管理端点（/api/v1/clusters*）
  if (path === '/api/v1/clusters' && req.method === 'GET') {
    writeJson(res, 200, { items: [CLUSTER], total: 1 })
    return
  }
  if (path === '/api/v1/clusters' && req.method === 'POST') {
    await readBody(req)
    writeJson(res, 201, CLUSTER)
    return
  }
  if (path === '/api/v1/clusters/test' && req.method === 'POST') {
    await readBody(req)
    writeJson(res, 200, CLUSTER_VIEW.info)
    return
  }
  if (path === '/api/v1/clusters/current' && req.method === 'GET') {
    writeJson(res, 200, CLUSTER_VIEW)
    return
  }
  if (path.startsWith('/api/v1/clusters/') && req.method === 'GET') {
    writeJson(res, 200, CLUSTER)
    return
  }
  if (path.startsWith('/api/v1/clusters/') && req.method === 'DELETE') {
    writeJson(res, 200, { deleted: 'ok' })
    return
  }

  if (path === '/api/v1/skills' && req.method === 'GET') {
    writeJson(res, 200, { data: SKILLS })
    return
  }

  if (path === '/api/sessions' && req.method === 'GET') {
    writeJson(res, 200, [SESSION])
    return
  }
  if (path === '/api/sessions' && req.method === 'POST') {
    writeJson(res, 200, SESSION)
    return
  }
  if (path.endsWith('/stream')) {
    res.writeHead(200, {
      'Content-Type': 'text/event-stream; charset=utf-8',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    })
    res.write(
      `data: ${JSON.stringify({ type: 'history', sessionId: SESSION.id, state: SESSION.state, messages: [], token: SESSION.token })}\n\n`,
    )
    for (const event of TURN_EVENTS) {
      res.write(`data: ${JSON.stringify({ session_id: SESSION.id, timestamp: new Date().toISOString(), ...event })}\n\n`)
    }
    return
  }
  if (path.endsWith('/send-message') || path.endsWith('/approve') || path.endsWith('/cancel')) {
    await readBody(req)
    writeJson(res, 200, { sessionId: SESSION.id })
    return
  }
  writeJson(res, 404, { error: 'not found' })
})

server.listen(PORT, '127.0.0.1', () => {
  console.log(`[mock-backend] listening on http://127.0.0.1:${PORT}`)
})
