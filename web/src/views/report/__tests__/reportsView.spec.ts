import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { DOMWrapper, VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ElMessageBox } from 'element-plus'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import ReportsView from '@/views/report/ReportsView.vue'

/**
 * P8.11 报告页回归：列表只画后端给的东西，可见性与越权判定一律在后端。
 *
 * 这里盯住三件容易在后续改版中悄悄丢掉的事：
 *  1. 存储健康属于设置域，没有 settings.read 的人不该看到徽标，也不该发出那次请求；
 *  2. 内容已不可恢复（available=false）的行必须把下载按钮关掉，而不是让用户点了吃一个 410；
 *  3. 跨 owner 的行要提示"下载将记入审计"，这是追责链条在前端唯一的可见信号。
 *
 * P8.13 / P8.14 追加的口径：
 *  4. 「开始体检」只对持 scan.execute 的角色出现，请求体只有白名单五项；
 *  5. 每条产物都要看得出是哪种体检（集群 / 业务）扫出来的，范围留痕之前的历史行标「未记录」；
 *  6. 清理只删业务引用，且以后端返回的 deleted 为准更新界面，skipped 不假装成功。
 *
 * P9.13 追加的口径（术语统一到「体检」，功能面扩到四件事）：
 *  7. 行标题统一叫「体检报告」，格式只在旁边用小字标 JSON / Markdown；
 *  8. 顶部统计的是可见报告的类别分布，不是「当前第几页」，且与筛选同源；
 *  9. 体检进行中必须有阶段进度，并且随时可以「停止体检」；
 * 10. 一次体检只归档所选那一种格式，另一种走「导出 Markdown」按需生成；
 */

const REPORTS = [
  {
    id: 'r1',
    clusterId: 'c1',
    kind: 'latest-md',
    size: 2048,
    sha256: 'a'.repeat(64),
    findings: 3,
    createdBy: 'u1',
    scopeKind: 'namespace',
    scopeValue: 'arc',
    available: true,
    createdAt: '2026-11-01T02:00:00Z',
  },
  {
    id: 'r2',
    clusterId: 'c1',
    kind: 'latest-json',
    size: 512,
    sha256: 'b'.repeat(64),
    findings: 0,
    createdBy: 'u2',
    available: false,
    createdAt: '2026-11-02T02:00:00Z',
  },
]

/** JSON 产物且正文可读：这一行才该出现「导出 Markdown」。 */
const JSON_REPORT = {
  id: 'r3',
  clusterId: 'c1',
  kind: 'latest-json',
  size: 4096,
  sha256: 'c'.repeat(64),
  findings: 5,
  createdBy: 'u1',
  createdByName: 'zhangsan',
  scopeKind: 'cluster',
  available: true,
  createdAt: '2026-11-03T02:00:00Z',
}

/** 后端阶段账的样式：一次标准体检的十个阶段里，跑到一半的样子。 */
const RUNNING_STAGES = [
  {
    name: 'collect',
    title: '资源采集',
    status: 'done',
    millis: 4200,
    elapsed: '4.2s',
    progress: '12 类资源',
  },
  { name: 'rules', title: '规则引擎', status: 'done', millis: 300, elapsed: '0.3s' },
  {
    name: 'evidence',
    title: '按需证据采集',
    status: 'running',
    millis: 0,
    detail: '正在读取 Pod 日志',
  },
  { name: 'llm', title: 'LLM 增强诊断', status: 'skipped', millis: 0, detail: '快速档不跑大模型' },
]

function envelope(data: unknown, pagination?: unknown): Response {
  return new Response(
    JSON.stringify({
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      data,
      pagination,
    }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )
}

interface FetchOptions {
  reports?: unknown[]
  health?: Record<string, unknown> | null
  /** POST /scans 的响应（阶段进度在轮询之前就要能画出来，所以放在这里给）。 */
  scanJob?: Record<string, unknown>
  /** POST /scans/{id}/cancel 的响应。 */
  cancelled?: Record<string, unknown>
  /** GET /reports 每次返回的固定集合；清理后想让它变小就传这个函数。 */
  reportsFor?: (url: string) => unknown[]
  /**
   * 有状态列表：清理成功后真的从集合里摘掉，模拟「删除后列表立刻少一条」。
   * 用固定次数的 reportsFor 会踩坑——首屏可能就发了两次请求。
   */
  store?: { reports: unknown[] }
  /** GET /auth/scope 的返回；默认给两个命名空间，单个时会触发"唯一候选自动填"。 */
  scope?: Record<string, unknown>
  /** 体检任务状态序列：每次 GET /scans/{id} 取下一个，取完重复最后一个。 */
  scanStatuses?: Record<string, unknown>[]
  /** 非 2xx 响应：{ 路径前缀: [状态码, 错误码, 文案] }。 */
  fail?: { path: string; status: number; code: string; message: string }
}

function installFetch(opts: FetchOptions = {}): ReturnType<typeof vi.fn> {
  let scanPolls = 0
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const failure = opts.fail
    if (failure && url.startsWith(failure.path)) {
      return errorResponse(failure.status, failure.code, failure.message)
    }
    if (url.startsWith('/api/v1/reports/cleanup')) {
      const ids = (JSON.parse(String(init?.body)) as { ids: string[] }).ids
      if (opts.store) {
        const gone = new Set(ids)
        opts.store.reports = opts.store.reports.filter(
          (row) => !gone.has((row as { id: string }).id),
        )
      }
      return envelope({ deleted: ids, skipped: [] })
    }
    if (/^\/api\/v1\/reports\/[^/]+$/.test(url) && init?.method === 'DELETE') {
      const id = decodeURIComponent(url.split('/').pop() ?? '')
      if (opts.store) {
        opts.store.reports = opts.store.reports.filter((row) => (row as { id: string }).id !== id)
      }
      return envelope({ deleted: [id], skipped: [] })
    }
    if (url.startsWith('/api/v1/reports/summary')) {
      // 总览与列表共用同一条谓词，因此这里也按同一批行现算，数字才不会各说各话。
      const items = (opts.store ? opts.store.reports : (opts.reports ?? REPORTS)) as Array<{
        kind: string
        scopeKind?: string
        available: boolean
      }>
      const cluster = items.filter((row) => row.scopeKind === 'cluster').length
      const business = items.filter(
        (r) => r.scopeKind === 'namespace' || r.scopeKind === 'pod',
      ).length
      return envelope({
        total: items.length,
        cluster,
        business,
        other: items.length - cluster - business,
        markdown: items.filter((row) => row.kind === 'latest-md').length,
        json: items.filter((row) => row.kind === 'latest-json').length,
        available: items.filter((row) => row.available).length,
      })
    }
    // 报告正文是二进制附件：/content?mode=stream 与 /export 都必须绕开 JSON 信封。
    if (url.includes('/content?mode=stream') || url.endsWith('/export')) {
      return new Response('# Kubernetes 集群体检报告\n', {
        status: 200,
        headers: {
          'Content-Type': 'text/markdown; charset=utf-8',
          'Content-Disposition':
            "attachment; filename*=UTF-8''%E4%BD%93%E6%A3%80%E6%8A%A5%E5%91%8A-r3.md",
        },
      })
    }
    if (url.startsWith('/api/v1/reports')) {
      const items = opts.store
        ? opts.store.reports
        : opts.reportsFor
          ? opts.reportsFor(url)
          : (opts.reports ?? REPORTS)
      return envelope(items, { page: 1, page_size: 20, total: items.length, total_pages: 1 })
    }
    // 停止端点先于轮询分支匹配：它也是 /api/v1/scans/ 前缀，否则会去吃掉一次状态序列。
    if (url.endsWith('/cancel')) {
      return envelope(
        opts.cancelled ?? {
          id: 'job-1',
          status: 'cancelled',
          error: '已由发起人停止',
          createdAt: '',
          stages: RUNNING_STAGES,
        },
      )
    }
    if (url.startsWith('/api/v1/scans/')) {
      const sequence = opts.scanStatuses ?? []
      const job = sequence[Math.min(scanPolls, sequence.length - 1)] ?? {
        id: 'job-1',
        status: 'running',
      }
      scanPolls += 1
      return envelope(job)
    }
    if (url.startsWith('/api/v1/scans')) {
      return envelope(
        opts.scanJob ?? {
          id: 'job-1',
          status: 'pending',
          createdAt: '',
          scopeKind: 'namespace',
          scopeValue: 'arc',
        },
      )
    }
    if (url.startsWith('/api/v1/auth/scope')) {
      return envelope(
        opts.scope ?? {
          namespaces: ['arc', 'mysql-dev'],
          clusterScoped: false,
          isAdmin: false,
          canWrite: false,
          canDestroy: false,
        },
      )
    }
    if (url.startsWith('/api/v1/clusters')) {
      return envelope([{ id: 'c1', name: 'prod-a', createdAt: '', updatedAt: '' }])
    }
    if (url.startsWith('/api/v1/storage/health')) {
      return envelope(
        opts.health ?? {
          driver: 's3',
          status: 'ready',
          ready: true,
          objects: 2,
          bytes: 2560,
          observedAt: '',
        },
      )
    }
    return envelope(null)
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function errorResponse(status: number, code: string, message: string): Response {
  return new Response(
    JSON.stringify({
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      error: { code, message },
    }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
}

/** 找到文案匹配的按钮（Element Plus 的按钮没有 data-testid，只能按人话找）。 */
function findButtonByText(root: VueWrapper | DOMWrapper<Element>, text: string) {
  return root
    .findAll('button')
    .find((button) => button.text().includes(text) && button.attributes('disabled') === undefined)
}

/**
 * 页头主按钮。
 *
 * 页头和体检弹窗的确认按钮现在都叫「开始体检」，在整个组件里按文案找会先命中页头那个，
 * 于是"点确认"变成"又开一次弹窗"，所以这两处必须各自限定作用域。
 */
function findHeadButton(wrapper: VueWrapper, text: string) {
  return findButtonByText(wrapper.find('.head-actions'), text)
}

/** 弹窗 footer 里的主按钮（同上：只在已打开的弹窗内找）。 */
function findDialogButton(wrapper: VueWrapper, text: string) {
  for (const footer of wrapper.findAll('.el-dialog__footer')) {
    const hit = findButtonByText(footer, text)
    if (hit) {
      return hit
    }
  }
  return undefined
}

/**
 * 点开一个按钮并等 Vue 处理完。el-dialog 在没有 append-to-body 时渲染在组件子树里，
 * 所以弹窗内的按钮同样从 wrapper 找得到，不需要绕到 document.body。
 */
async function clickButtonByText(
  root: VueWrapper | DOMWrapper<Element>,
  text: string,
): Promise<void> {
  await vi.waitFor(() => expect(findButtonByText(root, text)).toBeTruthy())
  await findButtonByText(root, text)!.trigger('click')
  await flushPromises()
}

/** 取某个路径最后一次请求的请求体。 */
function bodyOf(fetchMock: ReturnType<typeof vi.fn>, path: string): Record<string, unknown> {
  const call = [...fetchMock.mock.calls]
    .reverse()
    .find(([url, init]) => String(url).startsWith(path) && (init as RequestInit | undefined)?.body)
  if (!call) {
    throw new Error(`没有发往 ${path} 的请求体`)
  }
  return JSON.parse(String((call[1] as RequestInit).body)) as Record<string, unknown>
}

/** GET /scans/{id} 的次数：用来证明停在终态之后真的不再轮询。 */
function pollCount(fetchMock: ReturnType<typeof vi.fn>): number {
  return fetchMock.mock.calls.filter(
    ([url, init]) =>
      String(url).startsWith('/api/v1/scans/') &&
      !String(url).endsWith('/cancel') &&
      !(init as RequestInit | undefined)?.body,
  ).length
}

/** 每个用例都从干净会话开始：sessionStorage 里的体检标记会跨用例把横幅带进来。 */
function loginAs(role: string): void {
  localStorage.clear()
  window.sessionStorage.clear()
  localStorage.setItem(TOKEN_KEY, 'tok-test')
  localStorage.setItem(USER_KEY, JSON.stringify({ userId: 'u1', username: role, role }))
  setActivePinia(createPinia())
}

enableAutoUnmount(afterEach)
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('ReportsView（P8.11 报告页）', () => {
  beforeEach(() => {
    loginAs('admin')
    installFetch()
  })

  it('渲染报告行，并展示存储徽标与集群名', async () => {
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    expect(wrapper.text()).toContain('对象存储 S3 就绪')
    expect(wrapper.text()).toContain('prod-a')
    // 大字标题只有一种：格式是这份报告的存档形态，不是另一种报告。
    expect(wrapper.text()).toContain('体检报告')
    expect(wrapper.findAll('.report-item')[0].text()).toContain('Markdown')
    expect(wrapper.text()).toContain('2.0 KiB')
  })

  it('归属写成「发起人：用户名」，内容不可恢复的行禁用下载', async () => {
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    const rows = wrapper.findAll('.report-item')
    // 用户名由后端把 created_by 换过来；查不到名字（测试里没给）就退回显示 ID。
    expect(rows[0].text()).toContain('发起人：u1')
    expect(rows[1].text()).toContain('发起人：u2')
    // 记审计这句话收在 tooltip 里：pill 保持短，跨 owner 的提醒也还在。
    expect(rows[1].find('.pill[title]').attributes('title')).toContain('下载将记入审计')
    expect(rows[0].find('.pill[title]').attributes('title')).toBe('')
    expect(rows[1].text()).toContain('内容已不可恢复')

    const deadActions = rows[1].findAll('.item-actions button')
    // 正文没了就同时关掉详情和下载：清理按钮对这一行反而是有用的，点了正好把这条废引用摘掉。
    expect(deadActions).toHaveLength(3)
    expect(deadActions[0].text()).toContain('详情')
    expect(deadActions[0].attributes('disabled')).toBeDefined()
    expect(deadActions[1].attributes('disabled')).toBeDefined()
    expect(deadActions[2].attributes('disabled')).toBeUndefined()
    // 我的报告（s3 就绪）：详情 + 直连链接 + 下载 + 清理。
    expect(rows[0].findAll('.item-actions button')).toHaveLength(4)
  })

  it('degraded 与历史对象待迁移都要在页面上说清楚', async () => {
    installFetch({
      health: {
        driver: 'dir',
        status: 'degraded',
        ready: false,
        probeError: 'bucket unreachable',
        objects: 4,
        bytes: 4096,
        driverMismatch: { db: 2 },
        observedAt: '',
      },
    })
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    expect(wrapper.text()).toContain('对象存储 DIR 暂不可用')
    expect(wrapper.find('.pill.err').exists()).toBe(true)
    expect(wrapper.find('.mismatch').text()).toContain('2 个仍在 db')
    // dir 后端没有直连能力：不显示按钮，省得用户点了才知道不支持；每行只剩详情 + 下载 + 清理。
    expect(wrapper.findAll('.item-actions button')).toHaveLength(6)
  })

  it('无 settings.read 的普通用户既看不到徽标也不请求存储健康', async () => {
    loginAs('user')
    const fetchMock = installFetch()
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    // 徽标在页头右侧；页头的说明文字里本来就会出现"对象存储"四个字，所以只查徽标本身。
    expect(wrapper.find('.page-head .pill').exists()).toBe(false)
    const called = fetchMock.mock.calls.map((call) => String(call[0]))
    expect(called.some((url) => url.startsWith('/api/v1/storage/health'))).toBe(false)
  })

  it('没有可见报告时给出空态而不是空白页', async () => {
    installFetch({ reports: [] })
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.find('.tech-empty').exists()).toBe(true))
    expect(wrapper.text()).toContain('暂无可见报告')
    // 空态文案指的是页面上真实存在的那个按钮名字。
    expect(wrapper.text()).toContain('开始体检')
  })

  it('顶部给可见报告的类别分布，不再展示「当前页」这种没有信息量的数字', async () => {
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.stat')).toHaveLength(5))

    const stats = wrapper.findAll('.stat').map((card) => ({
      label: card.find('.l').text(),
      value: card.find('.v').text(),
    }))
    expect(stats.map((item) => item.label)).toEqual([
      '可见报告',
      '集群体检',
      '业务体检',
      '范围未记录',
      '归档对象',
    ])
    // REPORTS：一条 namespace 范围 + 一条范围未记录，可见总数 2、可读正文 1。
    expect(stats[0].value).toBe('2')
    expect(stats[1].value).toBe('0')
    expect(stats[2].value).toBe('1')
    expect(stats[3].value).toBe('1')
    expect(wrapper.text()).not.toContain('当前页')
    // 有一条列表里其实已经读不到，这个数字要如实说出来，而不是假装 2 份都能下载。
    expect(wrapper.findAll('.stat')[0].text()).toContain('1 份内容已不可恢复')
  })

  it('只有 JSON 产物且正文可读的行才给「导出 Markdown」', async () => {
    const fetchMock = installFetch({ reports: [JSON_REPORT] })
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(1))

    const row = wrapper.findAll('.report-item')[0]
    expect(row.text()).toContain('体检报告')
    expect(row.text()).toContain('JSON')
    expect(row.text()).toContain('发起人：zhangsan')
    expect(findButtonByText(row, '导出 Markdown')).toBeTruthy()

    await findButtonByText(row, '导出 Markdown')!.trigger('click')
    await vi.waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([url]) => String(url) === '/api/v1/reports/r3/export'),
      ).toBe(true),
    )
  })

  it('Markdown 产物不显示导出入口：下载原文拿到的就是 Markdown', async () => {
    installFetch({ reports: [REPORTS[0]] })
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(1))

    expect(findButtonByText(wrapper.findAll('.report-item')[0], '导出 Markdown')).toBeUndefined()
  })
})

describe('ReportsView（P8.13 开始体检 / P8.14 清单清理）', () => {
  // el-dialog / ElMessageBox 都传送到 body，上一用例留下的节点会污染 textContent 断言。
  beforeEach(() => {
    document.body.innerHTML = ''
  })
  afterEach(() => {
    document.body.innerHTML = ''
  })

  const ONLY_ARC = {
    namespaces: ['arc'],
    clusterScoped: false,
    isAdmin: false,
    canWrite: false,
    canDestroy: false,
  }

  it('开始体检入口只对持 scan.execute 的角色出现', async () => {
    loginAs('operator')
    installFetch()
    const wrapper = mount(ReportsView)
    await flushPromises()
    expect(findHeadButton(wrapper, '开始体检')).toBeTruthy()
    wrapper.unmount()

    loginAs('user')
    const userWrapper = mount(ReportsView)
    await flushPromises()
    // 按钮藏起来只是体验；真正的判定在后端 scan.execute。
    expect(findHeadButton(userWrapper, '开始体检')).toBeUndefined()
  })

  it('每行标出体检类别，范围留痕之前的历史行标「未记录」', async () => {
    loginAs('admin')
    installFetch()
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    const rows = wrapper.findAll('.report-item')
    expect(rows[0].text()).toContain('业务体检')
    expect(rows[0].text()).toContain('命名空间 arc')
    expect(rows[1].text()).toContain('范围未记录')
  })

  it('没有登记集群的行不编造集群名，归属写「发起人：用户名」', async () => {
    loginAs('admin')
    installFetch({
      reports: [
        {
          id: 'r9',
          clusterId: '',
          kind: 'latest-json',
          size: 1024,
          sha256: 'd'.repeat(64),
          findings: 1,
          createdBy: 'u1',
          createdByName: 'zhangsan',
          scopeKind: 'pod',
          scopeValue: 'web-7d9',
          available: true,
          createdAt: '2026-11-04T02:00:00Z',
        },
      ],
    })
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(1))

    const row = wrapper.findAll('.report-item')[0]
    // 类别与范围各占一枚徽标：类别只说集群体检 / 业务体检，范围才说命名空间或 Pod。
    expect(row.text()).toContain('业务体检')
    expect(row.text()).toContain('单 Pod web-7d9')
    // 页面体检不绑定已登记集群，没有集群身份就什么都不显示，不拿「平台集群」占位。
    expect(row.text()).not.toContain('平台集群')
    expect(row.text()).toContain('发起人：zhangsan')
  })

  it('类别筛选把 scopeCategory 带到列表与总览上', async () => {
    loginAs('admin')
    const fetchMock = installFetch()
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    const categorySelect = wrapper
      .findAllComponents({ name: 'ElSelect' })
      .find((select) => select.props('placeholder') === '全部类别')
    expect(categorySelect, '列表头部要有类别下拉').toBeDefined()
    await categorySelect!.find('.el-select__wrapper').trigger('click')
    await flushPromises()
    const labels = Array.from(document.querySelectorAll('.el-select-dropdown__item'))
      .map((item) => item.textContent?.trim() ?? '')
      .filter(Boolean)
    // 只有「系统体检」不在里面：那是针对 K8s 系统组件与节点的预留类别，本期没有实现。
    expect(labels).toEqual(expect.arrayContaining(['集群体检', '业务体检', '范围未记录']))
    expect(labels).not.toContain('系统体检')

    categorySelect!.vm.$emit('update:modelValue', 'business')
    categorySelect!.vm.$emit('change', 'business')
    await flushPromises()

    await vi.waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([url]) => String(url).includes('scopeCategory=business')),
      ).toBe(true),
    )
    // 总览跟着筛选走：否则同一屏里「业务体检 1」和总览「2」会互相打脸。
    expect(
      fetchMock.mock.calls
        .map(([url]) => String(url))
        .some(
          (url) =>
            url.startsWith('/api/v1/reports/summary') && url.includes('scopeCategory=business'),
        ),
    ).toBe(true)
  })

  it('勾选后才允许批量清理，且只提交勾选到的 id', async () => {
    loginAs('admin')
    const store = { reports: [...REPORTS] }
    const fetchMock = installFetch({ store })
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    // 一条没勾时按钮是禁用的：避免出现「清了个寂寞」的空请求。
    expect(findButtonByText(wrapper, '清理选中')).toBeUndefined()

    const confirm = vi
      .spyOn(ElMessageBox, 'confirm')
      .mockResolvedValue({ value: '', action: 'confirm' } as never)
    await wrapper.find('.select-all input').setValue(true)
    const batch = findButtonByText(wrapper, '清理选中 (2)')
    expect(batch, '全选后按钮要报出条数，别让人盲按').toBeTruthy()

    await batch!.trigger('click')
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(0))
    expect(confirm).toHaveBeenCalledTimes(1)
    // 两条一起走批量接口（单条才用 DELETE）：请求体只带界面上勾中的 id。
    expect(bodyOf(fetchMock, '/api/v1/reports/cleanup')).toEqual({ ids: ['r1', 'r2'] })
    // 清理完必须重新拉列表：界面显示的是后端的可见集合，不是本地删掉的行。
    expect(wrapper.find('.tech-empty').exists()).toBe(true)
  })

  it('单条清理走 DELETE /reports/{id}', async () => {
    loginAs('admin')
    const store = { reports: [...REPORTS] }
    const fetchMock = installFetch({ store })
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    const confirm = vi
      .spyOn(ElMessageBox, 'confirm')
      .mockResolvedValue({ value: '', action: 'confirm' } as never)
    const row = wrapper.findAll('.report-item')[0]
    await findButtonByText(row, '清理')!.trigger('click')
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(1))

    expect(confirm).toHaveBeenCalledTimes(1)
    const call = fetchMock.mock.calls.find(
      ([url, init]) =>
        String(url) === '/api/v1/reports/r1' && (init as RequestInit)?.method === 'DELETE',
    )
    expect(call, '单条清理应走 DELETE，而不是批量接口').toBeDefined()
  })

  it('确认框取消时不发任何清理请求', async () => {
    loginAs('admin')
    const fetchMock = installFetch()
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    vi.spyOn(ElMessageBox, 'confirm').mockRejectedValue(new Error('cancel'))
    await wrapper.findAll('.row-check input')[0].setValue(true)
    await findButtonByText(wrapper, '清理选中 (1)')!.trigger('click')
    await flushPromises()

    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/reports/cleanup'))).toBe(
      false,
    )
    expect(wrapper.findAll('.report-item')).toHaveLength(2)
  })

  it('提交体检只发白名单五项，并进入进度横幅', async () => {
    loginAs('operator')
    const fetchMock = installFetch({ scope: ONLY_ARC })
    const wrapper = mount(ReportsView)
    await flushPromises()

    // 唯一授权命名空间会自动填上，省掉一次下拉操作。
    await clickButtonByText(wrapper.find('.head-actions'), '开始体检')
    await vi.waitFor(() => expect(wrapper.find('.scan-form').exists()).toBe(true))
    await vi.waitFor(() => expect(wrapper.find('.el-dialog__title').text()).toContain('开始体检'))
    await findDialogButton(wrapper, '开始体检')!.trigger('click')
    await flushPromises()

    await vi.waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url) === '/api/v1/scans')).toBe(true),
    )
    expect(bodyOf(fetchMock, '/api/v1/scans')).toEqual({
      scope: 'namespace',
      namespace: 'arc',
      pod: '',
      preset: 'standard',
      // 缺省 json：报告详情页吃的是结构化正文。
      format: 'json',
    })
    expect(wrapper.find('.scan-banner').exists()).toBe(true)
    expect(wrapper.text()).toContain('排队中')
  })

  it('选 Markdown 时只归档 Markdown：请求体带 format=markdown', async () => {
    loginAs('operator')
    const fetchMock = installFetch({ scope: ONLY_ARC })
    const wrapper = mount(ReportsView)
    await flushPromises()

    await clickButtonByText(wrapper.find('.head-actions'), '开始体检')
    await vi.waitFor(() => expect(wrapper.find('.scan-form').exists()).toBe(true))
    // 格式与档位都是单选组：文案里含「Markdown」的那一项才是格式选择。
    const formatInput = wrapper
      .findAll('.scan-form .el-radio')
      .find((node) => node.text().includes('Markdown'))
    expect(formatInput, '弹窗里要能选报告格式').toBeDefined()
    await formatInput!.find('input').setValue(true)
    await findDialogButton(wrapper, '开始体检')!.trigger('click')

    await vi.waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url) === '/api/v1/scans')).toBe(true),
    )
    expect(bodyOf(fetchMock, '/api/v1/scans').format).toBe('markdown')
  })

  it('体检进行中铺开阶段进度，阶段名与报告里的诊断信息同源', async () => {
    loginAs('operator')
    installFetch({
      scope: ONLY_ARC,
      scanJob: {
        id: 'job-1',
        status: 'running',
        createdAt: '',
        scopeKind: 'namespace',
        scopeValue: 'arc',
        stages: RUNNING_STAGES,
      },
    })
    const wrapper = mount(ReportsView)
    await flushPromises()

    await clickButtonByText(wrapper.find('.head-actions'), '开始体检')
    await vi.waitFor(() => expect(wrapper.find('.scan-form').exists()).toBe(true))
    await findDialogButton(wrapper, '开始体检')!.trigger('click')
    await vi.waitFor(() => expect(wrapper.findAll('.stage')).toHaveLength(RUNNING_STAGES.length))

    const stages = wrapper.findAll('.stage').map((node) => node.text())
    expect(stages[0]).toContain('资源采集')
    expect(stages[0]).toContain('完成')
    expect(stages[0]).toContain('4.2s')
    expect(stages[2]).toContain('进行中')
    expect(stages[2]).toContain('正在读取 Pod 日志')
    expect(stages[3]).toContain('跳过')
    // 计数器数的是「已经不会再变(stage 收尾)」的阶段：skipped 也是收尾，剩下的那一个就是进行中的。
    expect(wrapper.text()).toContain('3 / 4 阶段')
  })

  it('停止体检：确认后打 cancel 端点，横幅落到「已停止」且不再轮询', async () => {
    loginAs('operator')
    const fetchMock = installFetch({
      scope: ONLY_ARC,
      scanJob: {
        id: 'job-1',
        status: 'running',
        createdAt: '',
        scopeKind: 'namespace',
        scopeValue: 'arc',
        stages: RUNNING_STAGES,
      },
    })
    const wrapper = mount(ReportsView)
    await flushPromises()

    await clickButtonByText(wrapper.find('.head-actions'), '开始体检')
    await vi.waitFor(() => expect(wrapper.find('.scan-form').exists()).toBe(true))
    await findDialogButton(wrapper, '开始体检')!.trigger('click')
    await vi.waitFor(() => expect(findButtonByText(wrapper, '停止体检')).toBeTruthy())

    const confirm = vi
      .spyOn(ElMessageBox, 'confirm')
      .mockResolvedValue({ value: '', action: 'confirm' } as never)
    await findButtonByText(wrapper, '停止体检')!.trigger('click')

    await vi.waitFor(() =>
      expect(
        fetchMock.mock.calls.some(
          ([url, init]) =>
            String(url) === '/api/v1/scans/job-1/cancel' &&
            (init as RequestInit)?.method === 'POST',
        ),
      ).toBe(true),
    )
    expect(confirm).toHaveBeenCalledTimes(1)
    await vi.waitFor(() => expect(wrapper.text()).toContain('已停止'))
    expect(wrapper.text()).toContain('已由发起人停止')
    // 终态就停轮询：后端一次只跑一个任务，挂着定时器没有任何意义。
    const pollsBefore = pollCount(fetchMock)
    await new Promise((resolve) => window.setTimeout(resolve, 60))
    expect(pollCount(fetchMock)).toBe(pollsBefore)
    // 停止同样是终态：重挂标记一起清掉，刷新后不该把「已停止」的横幅再端回来。
    expect(window.sessionStorage.getItem('aegisops.activeScan')).toBeNull()
  })

  it('确认框取消时不停止体检', async () => {
    loginAs('operator')
    const fetchMock = installFetch({
      scope: ONLY_ARC,
      scanJob: {
        id: 'job-1',
        status: 'running',
        createdAt: '',
        scopeKind: 'namespace',
        scopeValue: 'arc',
      },
    })
    const wrapper = mount(ReportsView)
    await flushPromises()

    await clickButtonByText(wrapper.find('.head-actions'), '开始体检')
    await vi.waitFor(() => expect(wrapper.find('.scan-form').exists()).toBe(true))
    await findDialogButton(wrapper, '开始体检')!.trigger('click')
    await vi.waitFor(() => expect(findButtonByText(wrapper, '停止体检')).toBeTruthy())

    vi.spyOn(ElMessageBox, 'confirm').mockRejectedValue(new Error('cancel'))
    await findButtonByText(wrapper, '停止体检')!.trigger('click')
    await flushPromises()

    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/cancel'))).toBe(false)
  })

  it('刷新后进度横幅重新挂上：进行中的体检不因一次刷新消失', async () => {
    loginAs('operator')
    const fetchMock = installFetch({
      scope: ONLY_ARC,
      scanStatuses: [
        {
          id: 'job-9',
          status: 'running',
          createdAt: '',
          scopeKind: 'namespace',
          scopeValue: 'arc',
          stages: RUNNING_STAGES,
        },
      ],
    })
    // 模拟「上一趟点过开始体检，然后按了 F5」：id 与档位记在 sessionStorage 里。
    window.sessionStorage.setItem(
      'aegisops.activeScan',
      JSON.stringify({ id: 'job-9', preset: 'quick' }),
    )

    const wrapper = mount(ReportsView)
    await flushPromises()

    await vi.waitFor(() => expect(wrapper.findAll('.stage')).toHaveLength(RUNNING_STAGES.length))
    expect(wrapper.text()).toContain('执行中 · 命名空间 arc')
    expect(wrapper.text()).toContain('快速体检')
    expect(findButtonByText(wrapper, '停止体检')).toBeTruthy()
    expect(
      fetchMock.mock.calls.some(
        ([url, init]) =>
          String(url) === '/api/v1/scans/job-9' && !(init as RequestInit | undefined)?.body,
      ),
    ).toBe(true)
  })

  it('任务已经取不到时清掉标记，不画一条假的速度横幅', async () => {
    loginAs('operator')
    installFetch({
      scope: ONLY_ARC,
      fail: {
        path: '/api/v1/scans/job-9',
        status: 404,
        code: 'not_found',
        message: '体检任务不存在或已被回收',
      },
    })
    window.sessionStorage.setItem(
      'aegisops.activeScan',
      JSON.stringify({ id: 'job-9', preset: 'standard' }),
    )

    const wrapper = mount(ReportsView)
    await flushPromises()

    await vi.waitFor(() => expect(window.sessionStorage.getItem('aegisops.activeScan')).toBeNull())
    expect(wrapper.find('.scan-banner').exists()).toBe(false)
  })

  it('越权范围原样展示后端文案，不静默改扫别的命名空间', async () => {
    loginAs('operator')
    installFetch({
      scope: ONLY_ARC,
      fail: {
        path: '/api/v1/scans',
        status: 403,
        code: 'forbidden',
        message: '你没有命名空间 arc 的访问权限',
      },
    })
    const wrapper = mount(ReportsView)
    await flushPromises()

    await clickButtonByText(wrapper.find('.head-actions'), '开始体检')
    await vi.waitFor(() => expect(wrapper.find('.scan-form').exists()).toBe(true))
    await findDialogButton(wrapper, '开始体检')!.trigger('click')

    await vi.waitFor(() =>
      expect(document.body.textContent).toContain('你没有命名空间 arc 的访问权限'),
    )
    // 被拒之后不留横幅：否则用户会以为有一次体检在跑。
    expect(wrapper.find('.scan-banner').exists()).toBe(false)
  })
})
