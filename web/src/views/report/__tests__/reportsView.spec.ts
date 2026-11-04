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
 *  4. 「发起巡检」只对持 scan.execute 的角色出现，请求体只有白名单四项；
 *  5. 每条产物都要看得出是哪个巡检维度扫出来的，范围留痕之前的历史行标「未记录」；
 *  6. 清理只删业务引用，且以后端返回的 deleted 为准更新界面，skipped 不假装成功。
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

function envelope(data: unknown, pagination?: unknown): Response {
  return new Response(
    JSON.stringify({ request_id: 'req-test', timestamp: new Date().toISOString(), data, pagination }),
    { status: 200, headers: { 'Content-Type': 'application/json' } },
  )
}

interface FetchOptions {
  reports?: unknown[]
  health?: Record<string, unknown> | null
  /** GET /reports 每次返回的固定集合；清理后想让它变小就传这个函数。 */
  reportsFor?: (url: string) => unknown[]
  /**
   * 有状态列表：清理成功后真的从集合里摘掉，模拟「删除后列表立刻少一条」。
   * 用固定次数的 reportsFor 会踩坑——首屏可能就发了两次请求。
   */
  store?: { reports: unknown[] }
  /** GET /auth/scope 的返回；默认给两个命名空间，单个时会触发"唯一候选自动填"。 */
  scope?: Record<string, unknown>
  /** 巡检任务状态序列：每次 GET /scans/{id} 取下一个，取完重复最后一个。 */
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
        opts.store.reports = opts.store.reports.filter((row) => !gone.has((row as { id: string }).id))
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
    if (url.startsWith('/api/v1/reports')) {
      const items = opts.store ? opts.store.reports : opts.reportsFor ? opts.reportsFor(url) : (opts.reports ?? REPORTS)
      return envelope(items, { page: 1, page_size: 20, total: items.length, total_pages: 1 })
    }
    if (url.startsWith('/api/v1/scans/')) {
      const sequence = opts.scanStatuses ?? []
      const job = sequence[Math.min(scanPolls, sequence.length - 1)] ?? { id: 'job-1', status: 'running' }
      scanPolls += 1
      return envelope(job)
    }
    if (url.startsWith('/api/v1/scans')) {
      return envelope({ id: 'job-1', status: 'pending', createdAt: '', scopeKind: 'namespace', scopeValue: 'arc' })
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
        opts.health ?? { driver: 's3', status: 'ready', ready: true, objects: 2, bytes: 2560, observedAt: '' },
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
 * 点开一个按钮并等 Vue 处理完。el-dialog 在没有 append-to-body 时渲染在组件子树里，
 * 所以弹窗内的按钮同样从 wrapper 找得到，不需要绕到 document.body。
 */
async function clickButtonByText(wrapper: VueWrapper, text: string): Promise<void> {
  await vi.waitFor(() => expect(findButtonByText(wrapper, text)).toBeTruthy())
  await findButtonByText(wrapper, text)!.trigger('click')
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


function loginAs(role: string): void {
  localStorage.clear()
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
    expect(wrapper.text()).toContain('Markdown 报告')
    expect(wrapper.text()).toContain('2.0 KiB')
  })

  it('跨 owner 提示记审计，内容不可恢复的行禁用下载', async () => {
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    const rows = wrapper.findAll('.report-item')
    expect(rows[0].text()).toContain('我的报告')
    expect(rows[1].text()).toContain('他人报告 · 下载将记入审计')
    expect(rows[1].text()).toContain('内容已不可恢复')

    const deadActions = rows[1].findAll('.item-actions button')
    // 正文没了只关下载：清理按钮对这一行反而是有用的，点了正好把这条废引用摘掉。
    expect(deadActions).toHaveLength(2)
    expect(deadActions[0].attributes('disabled')).toBeDefined()
    expect(deadActions[1].attributes('disabled')).toBeUndefined()
    // 我的报告（s3 就绪）：直连链接 + 下载 + 清理。
    expect(rows[0].findAll('.item-actions button')).toHaveLength(3)
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
    // dir 后端没有直连能力：不显示按钮，省得用户点了才知道不支持；每行只剩下载 + 清理。
    expect(wrapper.findAll('.item-actions button')).toHaveLength(4)
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
  })
})

describe('ReportsView（P8.13 发起巡检 / P8.14 清单清理）', () => {
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

  it('发起巡检入口只对持 scan.execute 的角色出现', async () => {
    loginAs('operator')
    installFetch()
    const wrapper = mount(ReportsView)
    await flushPromises()
    expect(findButtonByText(wrapper, '发起巡检')).toBeTruthy()
    wrapper.unmount()

    loginAs('user')
    const userWrapper = mount(ReportsView)
    await flushPromises()
    // 按钮藏起来只是体验；真正的判定在后端 scan.execute。
    expect(findButtonByText(userWrapper, '发起巡检')).toBeUndefined()
  })

  it('每行标出巡检维度，范围留痕之前的历史行标「未记录」', async () => {
    loginAs('admin')
    installFetch()
    const wrapper = mount(ReportsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.report-item')).toHaveLength(2))

    const rows = wrapper.findAll('.report-item')
    expect(rows[0].text()).toContain('命名空间巡检 · arc')
    expect(rows[1].text()).toContain('范围未记录')
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
      ([url, init]) => String(url) === '/api/v1/reports/r1' && (init as RequestInit)?.method === 'DELETE',
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

    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/reports/cleanup'))).toBe(false)
    expect(wrapper.findAll('.report-item')).toHaveLength(2)
  })

  it('提交巡检只发白名单四项，并进入轮询横幅', async () => {
    loginAs('operator')
    const fetchMock = installFetch({ scope: ONLY_ARC })
    const wrapper = mount(ReportsView)
    await flushPromises()

    // 唯一授权命名空间会自动填上，省掉一次下拉操作。
    await clickButtonByText(wrapper, '发起巡检')
    await vi.waitFor(() => expect(wrapper.text()).toContain('开始巡检'))
    await clickButtonByText(wrapper, '开始巡检')

    await vi.waitFor(() =>
      expect(fetchMock.mock.calls.some(([url]) => String(url) === '/api/v1/scans')).toBe(true),
    )
    expect(bodyOf(fetchMock, '/api/v1/scans')).toEqual({
      scope: 'namespace',
      namespace: 'arc',
      pod: '',
      preset: 'standard',
    })
    expect(wrapper.find('.scan-banner').exists()).toBe(true)
    expect(wrapper.text()).toContain('排队中')
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

    await clickButtonByText(wrapper, '发起巡检')
    await vi.waitFor(() => expect(wrapper.text()).toContain('开始巡检'))
    await clickButtonByText(wrapper, '开始巡检')

    await vi.waitFor(() =>
      expect(document.body.textContent).toContain('你没有命名空间 arc 的访问权限'),
    )
    // 被拒之后不留横幅：否则用户会以为有一次巡检在跑。
    expect(wrapper.find('.scan-banner').exists()).toBe(false)
  })
})
