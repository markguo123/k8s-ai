import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import ReportDetailView from '@/views/report/ReportDetailView.vue'

/**
 * P9 报告详情页回归（需求 §8.2 / 方案 18 §11）。
 *
 * 夹具是一条真实链路：PVC 一直 Pending（根因）→ Pod 挂载超时反复 CrashLoop（症状），
 * 盯住五条会被后续改版悄悄破坏的口径：
 *  1. 页面只画后端给的东西：根因、因果边、置信度、建议命令全部来自 inspection 区块；
 *  2. 根因与牵连异常必须看得出区别（强调块 vs 缩进清单），不能混成一个列表；
 *  3. 数据缺口不藏：采集失败原文、证据缺口、评分封顶原因都要出现在页面上；
 *  4. 后端回 415（只归档了 Markdown）时给空态，不渲染一套全零的空壳区块；
 *  5. 阶段账里 skipped 就是「未执行」，不能画成执行中，也不许出现编造的百分比。
 */

const POD_FINDING = {
  id: 'F-1',
  rule: 'POD_CRASHLOOP',
  severity: 'CRITICAL',
  title: 'Pod 反复崩溃重启',
  summary: '容器在 30 秒内退出 7 次，等待原因 CrashLoopBackOff',
  resource: { kind: 'Pod', namespace: 'arc', name: 'api-7d9' },
  role: 'symptom',
  evidence: [
    { id: 'E1', kind: 'state', source: 'pod.status', key: 'containerStatuses[0].state.waiting.reason', value: 'CrashLoopBackOff' },
    {
      id: 'E2',
      kind: 'event',
      source: 'events',
      key: 'reason',
      value: 'FailedMount\tWarning\tUnable to attach or mount volumes: unmounted volumes=[data], unattached volumes=[data]: timed out waiting for the condition\n',
      truncated: true,
    },
  ],
}

const PVC_FINDING = {
  id: 'F-2',
  rule: 'STORAGE_PVC_PENDING',
  severity: 'HIGH',
  title: 'PVC 长时间处于 Pending',
  summary: '卷绑定超过 5 分钟未完成',
  resource: { kind: 'PersistentVolumeClaim', namespace: 'arc', name: 'data-api-0' },
  evidence: [
    { id: 'E1', kind: 'state', source: 'pvc.status', key: 'status.phase', value: 'Pending' },
    { id: 'E2', kind: 'spec', source: 'pvc.spec', key: 'spec.storageClassName', value: 'fast-ssd' },
  ],
}

/** 一条完整的新结构报告：结论 / 采集账 / 诊断账 / 阶段账 / 故障链都在。 */
function fullReport(): Record<string, unknown> {
  return {
    report: {
      id: 'r1',
      clusterId: 'c1',
      scanId: 'scan-1',
      kind: 'latest-md',
      size: 4096,
      sha256: 'a'.repeat(64),
      findings: 2,
      createdBy: 'u1',
      scopeKind: 'namespace',
      scopeValue: 'arc',
      available: true,
      createdAt: '2026-11-04T02:00:00Z',
    },
    schemaVersion: 2,
    legacy: false,
    inspection: {
      meta: {
        toolVersion: 'v4.0.0',
        cluster: 'prod-a',
        scanEndedAt: '2026-11-04T02:00:00Z',
        duration: '42s',
        namespace: 'arc',
      },
      summary: { namespaces: 1, pods: 12, nodes: 3, roles: 2, roleBindings: 1 },
      findings: [POD_FINDING, PVC_FINDING],
      diagnoses: [
        {
          findingId: 'F-2',
          incidentId: 'I-1',
          summary: '存储类 fast-ssd 没有可用卷，Pod 因挂载超时反复重启',
          rootCause: 'StorageClass fast-ssd 未绑定任何可用 PV',
          confidence: 0.9,
          evidenceChain: ['F-2#E1', 'F-2#E2'],
          impact: 'arc/api-7d9 无法就绪，命名空间内服务不可用',
          investigation: [{ category: 'investigation', text: 'kubectl get pv -o wide', risk: 'SAFE' }],
          remediationText: '为 fast-ssd 供给 PV，或改用可用的存储类后重建 PVC。',
          remediationDirection: '恢复存储供给',
          llmUsed: false,
          source: 'deterministic',
          status: 'deterministic',
          rootCauseStatus: 'confirmed',
          diagnoser: 'STORAGE_PVC_PENDING',
        },
      ],
      incidents: [
        {
          id: 'I-1',
          title: 'arc/api-7d9 因存储未就绪反复崩溃',
          severity: 'CRITICAL',
          root: { id: 'F-2', rule: 'STORAGE_PVC_PENDING', severity: 'HIGH', title: PVC_FINDING.title, resource: PVC_FINDING.resource },
          members: [
            { id: 'F-1', rule: 'POD_CRASHLOOP', severity: 'CRITICAL', title: POD_FINDING.title, resource: POD_FINDING.resource },
          ],
          rootCauseStatus: 'confirmed',
          rootCause: 'StorageClass fast-ssd 没有可绑定的 PV，挂载超时导致容器反复被杀',
          rootCauseRule: 'STORAGE_PVC_PENDING',
          rootCauseMethod: 'rule',
          confidence: 0.9,
          confidenceBasis: 'PVC 状态与挂载事件互相印证',
          causalEdges: [
            {
              sourceFinding: 'F-2',
              targetFinding: 'F-1',
              sourceResource: PVC_FINDING.resource,
              targetResource: POD_FINDING.resource,
              relationType: 'pod_uses_pvc',
              mechanism: 'PVC 未绑定 → 卷无法挂载 → 容器启动失败进入 CrashLoopBackOff',
              evidenceRefs: ['F-2#E1', 'F-1#E2'],
              confidence: 'confirmed',
            },
          ],
          impactChain: ['PersistentVolumeClaim arc/data-api-0', 'Pod arc/api-7d9', 'Deployment arc/api'],
          evidenceRefs: ['F-2#E1', 'F-1#E2'],
          evidenceGaps: ['容器上一次退出前的日志未采集（quick 档位不采日志）'],
          alternativeCauses: ['应用自身启动参数错误'],
        },
      ],
      healthScore: {
        score: 58,
        max: 100,
        dataIntegrity: 'partial',
        coverage: 0.9,
        capped: true,
        capReason: '部分资源采集失败，健康评分上限 85',
      },
      collectionErrors: [
        {
          resource: { kind: 'Ingress', namespace: '', name: '' },
          operation: 'list ingresses',
          message: 'the server could not find the requested resource',
          time: '2026-11-04T01:59:30Z',
        },
      ],
      llmSummary: { enabled: false, calls: 0, failed: 0, mode: 'rule_only' },
      schemaVersion: 2,
      conclusion: {
        level: 'critical',
        headline: '1 条严重故障链：存储未就绪导致业务 Pod 反复崩溃',
        summary: '根因集中在存储供给，Pod 侧症状是派生结果。',
        topRisks: ['arc/api-7d9 无法对外提供服务'],
        priorities: ['恢复 fast-ssd 的 PV 供给'],
        dataCaveats: ['Ingress 列表采集失败，入口层未纳入本次结论'],
      },
      collection: {
        planned: 10,
        collected: 9,
        failed: 1,
        skipped: 0,
        coverage: 0.9,
        integrity: 'partial',
        failedKinds: ['ingresses'],
        missingData: ['容器日志（本次档位未开启）'],
      },
      diagnosisSummary: {
        ruleFindings: 2,
        deterministic: 1,
        confirmed: 1,
        probable: 0,
        unknown: 0,
        llmCalls: 0,
        llmSucceeded: 0,
        llmFailed: 0,
        llmDegraded: 0,
        llmRejected: 0,
        hybrid: 0,
        mode: 'rule_only',
        degradations: [
          { incidentId: 'I-1', status: 'not_configured', reason: '未配置模型，结论来自确定性引擎', keptDeterministic: true },
        ],
      },
      stages: [
        { name: 'collect', title: '采集集群快照', status: 'done', elapsed: '12s', millis: 12000 },
        { name: 'deterministic', title: '确定性诊断', status: 'done', elapsed: '1s', millis: 1000 },
        {
          name: 'llm',
          title: '大模型增强诊断',
          status: 'skipped',
          detail: '本次体检未启用大模型',
          millis: 0,
        },
      ],
      statistics: [{ domain: 'pod', total: 12, unhealthy: 1, critical: 1, detail: '1 个 Pod 未就绪' }],
      remediation: [
        {
          kind: 'remediation',
          text: 'kubectl patch pvc data-api-0 -n arc --type merge -p ...',
          risk: 'MEDIUM',
          target: 'arc/data-api-0',
          required: true,
        },
      ],
    },
  }
}

/** 四期之前归档的 v1 报告：没有结论 / 采集账 / 诊断账 / 阶段账。 */
function legacyReport(): Record<string, unknown> {
  return {
    report: {
      id: 'r0',
      clusterId: 'c1',
      kind: 'latest-md',
      size: 1024,
      sha256: 'b'.repeat(64),
      findings: 1,
      createdBy: 'u1',
      available: true,
      createdAt: '2026-09-01T02:00:00Z',
    },
    schemaVersion: 0,
    legacy: true,
    inspection: {
      meta: { cluster: 'prod-a', duration: '31s' },
      summary: { pods: 8 },
      findings: [POD_FINDING],
      diagnoses: [],
      healthScore: { score: 80, max: 100 },
      llmSummary: { enabled: false },
    },
  }
}

function envelope(data: unknown): Response {
  return new Response(
    JSON.stringify({ request_id: 'req-test', timestamp: new Date().toISOString(), data }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )
}

function errorResponse(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({ request_id: 'req-test', timestamp: new Date().toISOString(), error: { code, message } }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
}

function installFetch(payload: unknown): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    // 导出 Markdown 是二进制附件，绝不能套进 JSON 信封，否则下载下来是一层壳。
    if (url.endsWith('/export')) {
      return new Response('# 集群体检报告\n', {
        status: 200,
        headers: {
          'Content-Type': 'text/markdown; charset=utf-8',
          'Content-Disposition': "attachment; filename*=UTF-8''%E8%AF%8A%E6%96%AD%E6%8A%A5%E5%91%8A-r1.md",
        },
      })
    }
    if (url.includes('/report')) {
      return payload instanceof Response ? payload : envelope(payload)
    }
    return envelope(null)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

async function mountDetail(): Promise<ReturnType<typeof mount>> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/app/report/:id', name: 'report-detail', component: ReportDetailView }],
  })
  await router.push({ name: 'report-detail', params: { id: 'r1' } })
  await router.isReady()
  const wrapper = mount(ReportDetailView, { global: { plugins: [router, createPinia()] } })
  await flushPromises()
  return wrapper
}

enableAutoUnmount(afterEach)

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem(TOKEN_KEY, 'tok-test')
  localStorage.setItem(USER_KEY, JSON.stringify({ userId: 'u1', username: 'admin', role: 'admin' }))
  setActivePinia(createPinia())
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ReportDetailView（P9 报告详情页）', () => {
  it('渲染总体结论、健康评分与封顶原因', async () => {
    installFetch(fullReport())
    const wrapper = await mountDetail()

    expect(wrapper.text()).toContain('存在严重风险')
    expect(wrapper.text()).toContain('PersistentVolumeClaim arc/data-api-0：arc/api-7d9 因存储未就绪反复崩溃')
    expect(wrapper.find('.score-v').text()).toBe('58')
    expect(wrapper.text()).toContain('受数据完整性封顶')
    expect(wrapper.text()).toContain('部分资源采集失败，健康评分上限 85')
    // 结论里的数据缺口必须出现在结论区，而不是只藏在质量账里。
    expect(wrapper.find('.conclusion-card').text()).toContain('Ingress 列表采集失败')
  })

  it('故障链区分根因与被牵连异常，证据按引用号标注', async () => {
    installFetch(fullReport())
    const wrapper = await mountDetail()

    const root = wrapper.find('.root-band')
    expect(root.exists()).toBe(true)
    expect(root.text()).toContain('根因')
    expect(root.text()).toContain('根因已确认')
    expect(root.text()).toContain('StorageClass fast-ssd 未绑定任何可用 PV')

    const members = wrapper.find('.member-list')
    expect(members.exists()).toBe(true)
    expect(members.text()).toContain('F-1')
    expect(members.text()).toContain('Pod arc/api-7d9')

    // 因果边要同时给出资源关系类型与传导机制，否则读者无法判断这条边是不是同命名空间硬凑的。
    const edge = wrapper.find('.edge-list')
    expect(edge.text()).toContain('pod_uses_pvc')
    expect(edge.text()).toContain('卷无法挂载')
    // 证据引用号形如「异常 id#证据 id」：诊断结论里的 evidenceChain 才能落到具体证据上。
    const evidenceIds = wrapper.findAll('.ev-id').map((node) => node.text())
    expect(evidenceIds).toContain('F-2#E1')
    expect(evidenceIds).not.toContain('F-1#E2')
    expect(members.text()).not.toContain('F-2')
  })

  it('采集失败与证据缺口原文展示，不折叠成统计数字', async () => {
    installFetch(fullReport())
    const wrapper = await mountDetail()

    const quality = wrapper.find('.quality-card')
    expect(quality.text()).toContain('list ingresses')
    expect(quality.text()).toContain('the server could not find the requested resource')
    expect(quality.text()).toContain('计划 10')
    expect(quality.text()).toContain('覆盖率 90%')
    expect(quality.text()).toContain('部分资源采集失败')
    expect(quality.text()).toContain('未采集到的数据')
    expect(wrapper.text()).toContain('容器上一次退出前的日志未采集')
    // 降级原因要说清「这次到底是哪种失败」，且保留确定性结论这件事要标出来。
    expect(quality.text()).toContain('未配置模型')
    expect(quality.text()).toContain('已保留确定性结论')
  })

  it('处置建议突出必须处理项，不只给命令', async () => {
    installFetch(fullReport())
    const wrapper = await mountDetail()

    const remedy = wrapper.find('.remedy-card')
    expect(remedy.text()).toContain('必须处理')
    expect(remedy.text()).toContain('arc/data-api-0')
    expect(remedy.text()).toContain('中风险')
  })

  it('汇总标签展示 RBAC 资源计数', async () => {
    installFetch(fullReport())
    const wrapper = await mountDetail()

    expect(wrapper.text()).toContain('Role 2')
    expect(wrapper.text()).toContain('RoleBinding 1')
  })

  it('未执行的阶段画成未执行，不画成执行中', async () => {
    installFetch(fullReport())
    const wrapper = await mountDetail()

    expect(wrapper.findAll('.stage')).toHaveLength(3)
    expect(wrapper.findAll('.stage.running')).toHaveLength(0)
    // 状态类名必须绑上：样式按 .stage.done / .stage.skipped 上色，漏绑就整片静默掉色。
    expect(wrapper.findAll('.stage.done')).toHaveLength(2)
    const skipped = wrapper.find('.stage.skipped')
    expect(skipped.exists()).toBe(true)
    expect(skipped.text()).toContain('未执行')
    expect(skipped.text()).toContain('本次体检未启用大模型')
    // 禁止伪造进度：页面里不许出现「完成 60%」这类百分比。
    expect(wrapper.find('.stage-card').text()).not.toMatch(/\d+%/)
  })

  it('只有 Markdown 正文的报告给空态与下载入口，不渲染空壳区块', async () => {
    installFetch(errorResponse(415, 'unsupported_media_type', '该报告没有结构化形态，请使用原文下载'))
    const wrapper = await mountDetail()

    expect(wrapper.find('.tech-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('这份报告只归档了 Markdown 正文')
    expect(wrapper.text()).toContain('下载原文')
    // 没有结构化内容就不该有区块：一套全是 0 的区块会被读成「扫到 0 个异常」。
    expect(wrapper.findAll('.block')).toHaveLength(0)
    expect(wrapper.find('.stat-grid').exists()).toBe(false)
  })

  it('归档的是 JSON 正文时给「导出 Markdown」，按需求才渲染且不落第二份存档', async () => {
    const payload = fullReport()
    ;(payload.report as Record<string, unknown>).kind = 'latest-json'
    const fetchMock = installFetch(payload)
    const wrapper = await mountDetail()

    const exportBtn = wrapper
      .findAll('.page-head button')
      .find((node) => node.text().includes('导出 Markdown'))
    expect(exportBtn?.exists()).toBe(true)

    await exportBtn!.trigger('click')
    await flushPromises()

    // 只打导出端点：归档里仍然只有一份事实源，导出是当场渲染而不是再存一份。
    expect(fetchMock.mock.calls.some(([url]) => String(url) === '/api/v1/reports/r1/export')).toBe(true)
  })

  it('归档的已经是 Markdown 正文时不再给导出入口', async () => {
    installFetch(fullReport())
    const wrapper = await mountDetail()

    expect(wrapper.text()).not.toContain('导出 Markdown')
  })

  it('旧版报告结构标出徽标，且不用零值伪造质量账', async () => {
    installFetch(legacyReport())
    const wrapper = await mountDetail()

    expect(wrapper.find('.page-head').text()).toContain('旧版报告结构')
    expect(wrapper.find('.conclusion-card').text()).toContain('这份报告没有总体结论区块')
    expect(wrapper.find('.quality-card').text()).toContain('采集质量账未记录')
    expect(wrapper.find('.quality-card').text()).toContain('这份报告没有质量账区块')
    expect(wrapper.findAll('.stage')).toHaveLength(0)
  })

  it('完整异常清单展示后端给出的 Finding 角色', async () => {
    installFetch(fullReport())
    const wrapper = await mountDetail()

    const section = wrapper.findAll('section.block').find((item) => item.text().includes('完整异常清单'))
    const finding = section?.findAll('.item').find((item) => item.text().includes('Pod 反复崩溃重启'))
    expect(finding?.text()).toContain('关联症状')
  })

  it('正文读不出来时给重试与返回，不静默留白', async () => {
    installFetch(errorResponse(410, 'report_unavailable', '报告内容已被回收'))
    const wrapper = await mountDetail()

    expect(wrapper.find('.tech-empty').exists()).toBe(true)
    expect(wrapper.text()).toContain('报告内容读不出来')
    // 已知错误码走统一翻译（410 = 内容已不可恢复），不是把后端原文糊在页面上。
    expect(wrapper.text()).toContain('该报告的内容已不可恢复')
    expect(wrapper.findAll('button').length).toBeGreaterThanOrEqual(2)
  })

  it('业务故障优先展示诊断根因，并把治理建议从故障链中拆出', async () => {
    const payload = fullReport() as {
      inspection: {
        findings: Array<Record<string, unknown>>
        incidents: Array<Record<string, unknown>>
        diagnoses: Array<Record<string, unknown>>
        remediation: Array<Record<string, unknown>>
      }
    }
    payload.inspection.incidents[0].rootCause = '容器反复退出，具体原因待确认'
    payload.inspection.diagnoses[0].rootCause =
      '探针路径与镜像实际内容不匹配：**/ready、/healthz 返回 404**，就绪探针永不通过'
    payload.inspection.diagnoses[0].remediation = [
      {
        category: 'remediation',
        text: "kubectl -n arc patch deployment app-backend --type=merge -p '{}'",
        risk: 'MEDIUM',
      },
    ]
    payload.inspection.diagnoses[0].remediationText =
      '先确认 **/ready、/healthz 返回 404** 是探针配置错误，再修改探针路径。'
    payload.inspection.diagnoses[0].verification = [
      { category: 'verification', text: 'kubectl -n arc get endpoints app-service', risk: '' },
    ]
    ;(payload.inspection.diagnoses[0].investigation as Array<Record<string, unknown>>)[0].risk = ''
    payload.inspection.diagnoses[0].validation = {
      passed: true,
      commandIssues: ['1 条修复命令因缺少确定字段值未收录'],
    }
    payload.inspection.remediation = [
      { kind: 'remediation', format: 'text', text: '**修复目标**：恢复 app-backend 业务。' },
      {
        kind: 'investigation',
        format: 'command',
        text: 'kubectl -n arc logs api-0 --tail=50',
        risk: 'SAFE',
        target: 'arc/api-0',
      },
    ]
    payload.inspection.findings.push({
      id: 'F-3',
      rule: 'NetworkPolicyNoDefaultDeny',
      severity: 'LOW',
      title: '命名空间缺少默认拒绝 NetworkPolicy',
      summary: '治理建议',
      suggestion: true,
      resource: { kind: 'Namespace', name: 'arc' },
      evidence: [],
    })
    payload.inspection.findings.push({
      id: 'F-DEP',
      rule: 'DeploymentReplica',
      severity: 'MEDIUM',
      title: '副本数不匹配：0/2 ready',
      summary: '业务不可用',
      resource: { kind: 'Deployment', namespace: 'arc', name: 'app-backend' },
      evidence: [],
    })
    ;(payload.inspection.incidents[0].members as Array<Record<string, unknown>>).push({
      id: 'F-DEP',
      rule: 'DeploymentReplica',
      severity: 'MEDIUM',
      title: '副本数不匹配：0/2 ready',
      resource: { kind: 'Deployment', namespace: 'arc', name: 'app-backend' },
    })
    payload.inspection.incidents.push({
      id: 'F-3',
      title: '命名空间缺少默认拒绝 NetworkPolicy',
      severity: 'LOW',
      root: {
        id: 'F-3',
        rule: 'NetworkPolicyNoDefaultDeny',
        severity: 'LOW',
        title: '命名空间缺少默认拒绝 NetworkPolicy',
        resource: { kind: 'Namespace', name: 'arc' },
      },
      members: [],
      rootCauseStatus: 'probable',
      rootCause: '未配置 default-deny',
      independent: true,
    })

    installFetch(payload)
    const wrapper = await mountDetail()

    expect(wrapper.find('.root-text').text()).toContain('/ready、/healthz 返回 404')
    expect(wrapper.find('.fault-chain-list .impact-pill').text()).toContain('0/2 ready')
    expect(wrapper.find('.summary').text()).toContain('业务故障链 1 条')
    expect(wrapper.find('.summary').text()).toContain('治理建议 1 项')
    expect(wrapper.find('.fault-chain-list').text()).not.toContain('缺少默认拒绝')
    expect(wrapper.find('.advisory-list').text()).toContain('缺少默认拒绝')

    const evidence = wrapper.find('details.evidence-panel')
    expect(evidence.exists()).toBe(true)
    expect(evidence.attributes('open')).toBeUndefined()
    expect(evidence.text()).toContain('F-2#E1')
    expect(evidence.text()).not.toContain('F-1#E2')
    expect(wrapper.find('details.causal-panel').attributes('open')).toBeUndefined()
    expect(wrapper.find('details.member-panel').attributes('open')).toBeUndefined()

    const actions = wrapper.find('.action-panel')
    expect(actions.exists()).toBe(true)
    for (const heading of ['排查建议', '修复建议', '验证建议', '修复说明']) {
      expect(actions.text()).toContain(heading)
    }
    expect(actions.text()).toContain('恢复存储供给')
    expect(actions.text()).toContain('1 条修复命令因缺少确定字段值未收录')
    expect(actions.findAll('.pill').some((node) => node.text() === '只读')).toBe(true)
    expect(actions.text()).not.toContain('未标注风险')
    expect(actions.find('strong').text()).toContain('/ready、/healthz 返回 404')
    expect(actions.text()).toContain('kubectl -n arc get endpoints app-service')

    const remedy = wrapper.find('.remedy-card')
    expect(remedy.find('.md strong').text()).toContain('修复目标')
    expect(remedy.find('pre.cmd').text()).toContain('kubectl -n arc logs api-0')
  })
})
