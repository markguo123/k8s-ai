import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import ReportsView from '@/views/report/ReportsView.vue'

/**
 * P8.11 报告页回归：列表只画后端给的东西，可见性与越权判定一律在后端。
 *
 * 这里盯住三件容易在后续改版中悄悄丢掉的事：
 *  1. 存储健康属于设置域，没有 settings.read 的人不该看到徽标，也不该发出那次请求；
 *  2. 内容已不可恢复（available=false）的行必须把下载按钮关掉，而不是让用户点了吃一个 410；
 *  3. 跨 owner 的行要提示"下载将记入审计"，这是追责链条在前端唯一的可见信号。
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
}

function installFetch(opts: FetchOptions = {}): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url.startsWith('/api/v1/reports')) {
      const items = opts.reports ?? REPORTS
      return envelope(items, { page: 1, page_size: 20, total: items.length, total_pages: 1 })
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
    expect(deadActions).toHaveLength(1)
    expect(deadActions[0].attributes('disabled')).toBeDefined()
    expect(rows[0].findAll('.item-actions button')).toHaveLength(2)
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
    // dir 后端没有直连能力：不显示按钮，省得用户点了才知道不支持。
    expect(wrapper.findAll('.item-actions button')).toHaveLength(2)
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
