import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  describeSkillError,
  getSkill,
  importProceduralDocs,
  importProceduralZip,
  importSkills,
  listSkills,
  setSkillEnabled,
} from '@/api/skills'
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

function makeSkill(name: string): Record<string, unknown> {
  return {
    name,
    spec: {
      author: 'k8s-ai',
      description: `${name} 方法论`,
      name,
      domain: 'ops',
      version: 'v1.0.0',
      kinds: ['Deployment'],
      validation: [{ name: 'no-latest', condition: 'image != latest', severity: 'suggest', message: 'x' }],
      risk: { default: 'MEDIUM' },
      source: 'custom',
    },
    source: 'custom',
    enabled: true,
    hitCount: 3,
    lastHit: '2026-10-16T00:00:00Z',
    createdAt: '2026-10-16T00:00:00Z',
    updatedAt: '2026-10-16T00:00:00Z',
  }
}

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.removeItem(TOKEN_KEY)
})

describe('skills API（P3.5–P3.7 前端契约）', () => {
  it('listSkills 解析 Skill 列表，空 data 兜底为空数组', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, [makeSkill('deploy-safe')])))
    const list = await listSkills()
    expect(list).toHaveLength(1)
    expect(list[0].name).toBe('deploy-safe')
    expect(list[0].spec.kinds).toContain('Deployment')
    expect(list[0].hitCount).toBe(3)

    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(envelopeResponse(200, undefined)))
    await expect(listSkills()).resolves.toEqual([])
  })

  it('getSkill 按名称读取单条，名称需 URL 编码', async () => {
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, makeSkill('a/b')))
    vi.stubGlobal('fetch', fetchMock)
    const skill = await getSkill('a/b')
    const [url] = fetchMock.mock.calls[0]
    expect(url).toContain(`/skills/${encodeURIComponent('a/b')}`)
    expect(skill.name).toBe('a/b')
  })

  it('importSkills 直接发送原始 YAML 文本（Content-Type text/yaml 且不 JSON 序列化）', async () => {
    const yaml = 'name: my-skill\ndomain: ops\nversion: v1.0.0\nkinds: [Deployment]\nrisk: {}\nsource: custom\n'
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(201, [makeSkill('my-skill')]))
    vi.stubGlobal('fetch', fetchMock)
    localStorage.setItem(TOKEN_KEY, 'tok-demo')

    const imported = await importSkills(yaml)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/skills/import')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(yaml)
    const headers = init.headers as Headers
    expect(headers.get('Content-Type')).toBe('text/yaml')
    expect(headers.get('Authorization')).toBe('Bearer tok-demo')
    expect(imported).toHaveLength(1)
  })

  it('importProceduralDocs 直接发送原始 SKILL.md 文本（P3.8.2）', async () => {
    const md = '---\nname: mysql-troubleshoot\ndescription: 排查 MySQL 故障\n---\n# 步骤…\n'
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(201, [makeSkill('mysql-troubleshoot')]))
    vi.stubGlobal('fetch', fetchMock)

    const imported = await importProceduralDocs(md)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/skills/import-procedural')
    expect(init.method).toBe('POST')
    expect(init.body).toBe(md)
    const headers = init.headers as Headers
    expect(headers.get('Content-Type')).toBe('text/yaml')
    expect(imported).toHaveLength(1)
  })

  it('importProceduralZip 走 multipart form-data（P3.8.3）且不手动设 Content-Type', async () => {
    const file = new File(['zip-bytes'], 'mysql-pack.zip', { type: 'application/zip' })
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(201, [makeSkill('mysql-troubleshoot')]))
    vi.stubGlobal('fetch', fetchMock)

    const imported = await importProceduralZip(file)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/skills/import-zip')
    expect(init.method).toBe('POST')
    expect(init.body).toBeInstanceOf(FormData)
    const headers = init.headers as Headers
    expect(headers.get('Content-Type')).toBeNull()
    expect(imported).toHaveLength(1)
  })

  it('setSkillEnabled 走 PATCH /skills/{name}/enabled 并携带 { enabled }', async () => {
    const disabled = makeSkill('deploy-safe')
    disabled.enabled = false
    const fetchMock = vi.fn().mockResolvedValue(envelopeResponse(200, disabled))
    vi.stubGlobal('fetch', fetchMock)

    const updated = await setSkillEnabled('deploy-safe', false)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/skills/deploy-safe/enabled')
    expect(init.method).toBe('PATCH')
    expect(JSON.parse(init.body as string)).toEqual({ enabled: false })
    expect(updated.enabled).toBe(false)
  })

  it('403 错误归一：describeSkillError 不透出请求体', async () => {
    const errorEnvelope = {
      request_id: 'req-test',
      timestamp: new Date().toISOString(),
      error: { code: 'forbidden', message: 'permission denied' },
    }
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => new Response(JSON.stringify(errorEnvelope), {
      status: 403,
      headers: { 'Content-Type': 'application/json' },
    })))
    await expect(listSkills()).rejects.toMatchObject({ status: 403, code: 'forbidden' })
    await expect(setSkillEnabled('x', true)).rejects.toMatchObject({ status: 403 })

    const normalized = describeSkillError('加载 Skill', new Error('permission denied'))
    expect(normalized).toContain('permission denied')
    expect(normalized).not.toContain('---')
    expect(describeSkillError('导入 Skill', null)).toBe('导入 Skill失败，请稍后重试')
  })
})
