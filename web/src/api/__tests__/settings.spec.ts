import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  describeSettingsError,
  getSettings,
  patchSettings,
} from '@/api/settings'
import { TOKEN_KEY } from '@/api/request'

function envelopeResponse<T>(status: number, data?: T): Response {
  return new Response(
    JSON.stringify({
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      data,
      error: undefined,
    }),
    { status, headers: { 'Content-Type': 'application/json' } },
  )
}

function makeGroupView(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    group: 'model',
    items: [
      {
        key: 'model.provider',
        kind: 'string',
        value: 'openai',
        hasValue: true,
        source: 'default',
        desc: '模型提供方',
      },
      {
        key: 'model.api_key',
        kind: 'string',
        value: null,
        hasValue: true,
        sensitive: true,
        source: 'file',
        desc: 'API Key（只读状态，不回显）',
      },
    ],
    ...overrides,
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(TOKEN_KEY)
})

describe('settings API（P3.9/P3.10 前端契约）', () => {
  it('getSettings 解析分组视图，敏感键 value 为 null 且仅暴露 hasValue', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, makeGroupView()))
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem(TOKEN_KEY, 'tok-demo')
    const view = await getSettings('model')
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/settings/model')
    expect(init.method).toBe('GET')
    expect(view.group).toBe('model')
    expect(view.items).toHaveLength(2)
    const apiKey = view.items.find((item) => item.key === 'model.api_key')
    expect(apiKey?.sensitive).toBe(true)
    expect(apiKey?.value).toBeNull()
    expect(apiKey?.hasValue).toBe(true)
  })

  it('getSettings 对分组名做 URL 编码', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, makeGroupView()))
    vi.stubGlobal('fetch', fetchMock)
    await getSettings('a/b')
    const [url] = fetchMock.mock.calls[0]
    expect(url).toContain(`/settings/${encodeURIComponent('a/b')}`)
  })

  it('patchSettings 走 PATCH /settings/{group} 且只提交扁平请求体键', async () => {
    const next = makeGroupView()
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, next))
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem(TOKEN_KEY, 'tok-demo')
    const view = await patchSettings('model', { 'model.provider': 'azure' })
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/settings/model')
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body as string)).toEqual({ 'model.provider': 'azure' })
    const headers = init.headers as Headers
    expect(headers.get('Authorization')).toBe('Bearer tok-demo')
    expect(view.group).toBe('model')
  })

  it('getSettings 空 data 视为分组不存在并抛出可读错误', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, undefined)))
    await expect(getSettings('nope')).rejects.toThrow('配置分组不存在')
  })

  it('403 错误归一：describeSettingsError 不透出请求体', async () => {
    const errorEnvelope = {
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      error: { code: 'forbidden', message: 'permission denied' },
    }
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => new Response(JSON.stringify(errorEnvelope), {
      status: 403,
      headers: { 'Content-Type': 'application/json' },
    })))
    await expect(getSettings('model')).rejects.toMatchObject({ status: 403, code: 'forbidden' })
    await expect(patchSettings('model', { 'model.api_key': 'secret' })).rejects.toMatchObject({ status: 403 })
    const normalized = describeSettingsError('加载配置', new Error('permission denied'))
    expect(normalized).toContain('permission denied')
    expect(normalized).not.toContain('secret')
    expect(describeSettingsError('保存配置', null)).toBe('保存配置失败，请稍后重试')
  })

  it('覆盖态字段与保存警告按契约透传（P6.12 ⑩）', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, makeGroupView({
      items: [
        {
          key: 'api_key',
          kind: 'string',
          value: null,
          hasValue: true,
          sensitive: true,
          envOverride: 'K8S_AI_LLM_API_KEY',
          envOverrideTier: 'secret',
        },
      ],
      warnings: ['api_key 已写入配置库，但当前运行值仍由部署密钥 K8S_AI_LLM_API_KEY 提供。'],
    })))
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem(TOKEN_KEY, 'tok-demo')

    const view = await patchSettings('model', { api_key: 'sk-page' })
    expect(view.items[0]?.envOverride).toBe('K8S_AI_LLM_API_KEY')
    expect(view.items[0]?.envOverrideTier).toBe('secret')
    expect(view.warnings).toHaveLength(1)
    expect(view.warnings?.[0]).toContain('K8S_AI_LLM_API_KEY')
  })
})
