import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { TOKEN_KEY, USER_KEY } from '@/api/request'
import SettingsView from '@/views/settings/SettingsView.vue'

/**
 * P6.13 配置中心布局回归：左 rail + 全宽内容。
 *
 * 长分组必须有独立的内容滚动容器（`.settings-content`），表头单独成块
 * （`.form-head`）以便滚动时吸顶；只靠外层 `.app-main` 滚动会让左侧分组
 * 随页面一起滚走，且长配置项看不全。
 */

const ITEMS = Array.from({ length: 26 }, (_, i) => ({
  key: `demo.key_${i}`,
  kind: i % 3 === 0 ? 'bool' : i % 3 === 1 ? 'string' : 'int',
  value: i % 3 === 0 ? true : i % 3 === 1 ? `value-${i}` : i,
  hasValue: true,
  source: 'demo',
  desc: `演示配置项 ${i}`,
}))

function installFetch(data: Record<string, unknown> = {}): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(
      JSON.stringify({
        request_id: 'req-test',
        timestamp: new Date().toISOString(),
        data: { group: 'model', encryptionAvailable: true, items: ITEMS, ...data },
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )),
  )
}

enableAutoUnmount(afterEach)

describe('SettingsView 布局（P6.13 左 rail + 全宽内容）', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem(TOKEN_KEY, 'tok-test')
    localStorage.setItem(
      USER_KEY,
      JSON.stringify({ userId: 'u1', username: 'admin', displayName: '管理员', role: 'admin' }),
    )
    setActivePinia(createPinia())
    installFetch()
  })

  it('渲染左右分区，长分组全部字段位于独立内容容器内', async () => {
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length))

    const layout = wrapper.find('.settings-layout')
    expect(layout.exists()).toBe(true)
    expect(layout.find('.settings-rail').exists()).toBe(true)
    expect(layout.find('.settings-content').exists()).toBe(true)

    const content = layout.find('.settings-content')
    expect(content.findAll('.field')).toHaveLength(ITEMS.length)
    expect(content.find('.form-head').exists()).toBe(true)
    expect(content.find('.form-head .el-button').text()).toContain('保存')
  })

  it('每个配置项占满内容区一整行，信息与控件左右分区', async () => {
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length))

    const fields = wrapper.findAll('.field')
    for (const field of fields) {
      expect(field.find('.field-meta').exists()).toBe(true)
      expect(field.find('.field-control').exists()).toBe(true)
      expect(field.find('.field-control .el-input, .field-control .el-switch, .field-control .el-input-number').exists()).toBe(true)
    }
  })

  it('切换分组时仍复用同一内容滚动容器', async () => {
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length))

    const railItems = wrapper.findAll('.rail-item')
    expect(railItems.length).toBeGreaterThan(1)
    await railItems[1]!.trigger('click')
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length))

    expect(wrapper.findAll('.settings-content')).toHaveLength(1)
    expect(wrapper.findAll('.field')).toHaveLength(ITEMS.length)
  })
})

describe('SettingsView 配置控件可读性', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem(TOKEN_KEY, 'tok-test')
    localStorage.setItem(
      USER_KEY,
      JSON.stringify({ userId: 'u1', username: 'admin', displayName: '管理员', role: 'admin' }),
    )
    setActivePinia(createPinia())
  })

  it('thinking 使用开关而不是 JSON 文本框', async () => {
    installFetch({
      items: [
        {
          key: 'thinking',
          kind: 'json',
          value: { type: 'enabled' },
          hasValue: true,
          desc: '思考参数',
        },
      ],
    })
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(1))

    const field = wrapper.find('.field')
    expect(field.find('.el-switch').exists()).toBe(true)
    expect(field.find('textarea').exists()).toBe(false)
    expect(field.text()).toContain('启用')
  })

  it('敏感配置在缺少加密能力时提前提示并禁用输入', async () => {
    installFetch({
      encryptionAvailable: false,
      items: [
        {
          key: 'api_key',
          kind: 'string',
          value: null,
          hasValue: false,
          sensitive: true,
          desc: '接口密钥（不回显）',
        },
      ],
    })
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(1))

    expect(wrapper.find('.encryption-alert').exists()).toBe(true)
    expect(wrapper.find('.encryption-alert').text()).toContain('K8S_AI_ENC_KEY')
    expect(wrapper.find('.field input').attributes('disabled')).toBeDefined()
  })

  it('duration、stringList 和 JSON 都展示格式示例', async () => {
    installFetch({
      items: [
        {
          key: 'timeout',
          kind: 'duration',
          value: '120s',
          hasValue: true,
          example: '120s',
          desc: '模型请求超时',
        },
        {
          key: 'reasoning_fields',
          kind: 'stringList',
          value: [],
          hasValue: false,
          example: 'reasoning_content, reasoning',
          desc: '思考字段名白名单',
        },
        {
          key: 'servers',
          kind: 'json',
          value: [],
          hasValue: false,
          example: '[{"name":"demo","command":"npx"}]',
          desc: 'MCP 服务器列表',
        },
        {
          key: 'max_tokens',
          kind: 'int',
          value: 4096,
          hasValue: true,
          min: 1,
          desc: '单次回复上限',
        },
      ],
    })
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(4))

    const text = wrapper.text()
    expect(text).toContain('时长格式：120s')
    expect(text).toContain('多值格式：reasoning_content, reasoning')
    expect(text).toContain('JSON 示例：')
    expect(text).toContain('取值范围：1 以上')
  })
})

/**
 * P6.12 ⑩ 部署期覆盖态（D-29 两档）：覆盖来源必须在页面上看得见、保存时有提示，
 * 不允许出现"页面改了但看起来没生效"的静默成功。
 */
const ENV_ITEMS = [
  {
    key: 'api_key',
    kind: 'string',
    value: null,
    hasValue: true,
    sensitive: true,
    desc: '接口密钥（不回显）',
    envOverride: 'K8S_AI_LLM_API_KEY',
    envOverrideTier: 'secret',
  },
  {
    key: 'model',
    kind: 'string',
    value: 'qwen-plus',
    hasValue: true,
    desc: '模型名',
    envOverride: 'K8S_AI_LLM_MODEL',
    envOverrideTier: 'seed',
  },
  { key: 'namespace', kind: 'string', value: 'default', hasValue: true, desc: '默认命名空间' },
]

function installScopedFetch(patchWarnings: string[] = []): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (_url: string, init?: RequestInit) => new Response(
      JSON.stringify({
        request_id: 'req-test',
        timestamp: new Date().toISOString(),
        data: {
          group: 'model',
          encryptionAvailable: true,
          items: ENV_ITEMS,
          ...(init?.method === 'PATCH' && patchWarnings.length > 0 ? { warnings: patchWarnings } : {}),
        },
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )),
  )
}

describe('SettingsView 部署期覆盖态（P6.12 ⑩）', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem(TOKEN_KEY, 'tok-test')
    localStorage.setItem(
      USER_KEY,
      JSON.stringify({ userId: 'u1', username: 'admin', displayName: '管理员', role: 'admin' }),
    )
    setActivePinia(createPinia())
    document.body.innerHTML = ''
  })

  it('密钥态键显示"部署 Secret 提供"徽标并说明保存不改运行值', async () => {
    installScopedFetch()
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ENV_ITEMS.length))

    const field = wrapper.findAll('.field')[0]
    const badge = field!.find('.pill.secret')
    expect(badge.exists()).toBe(true)
    expect(badge.text()).toContain('部署 Secret 提供')
    expect(badge.text()).toContain('K8S_AI_LLM_API_KEY')
    expect(field!.find('.env-hint.secret').text()).toContain('运行值不变')
  })

  it('种子态键显示"部署种子值"徽标并说明保存后以页面为准', async () => {
    installScopedFetch()
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ENV_ITEMS.length))

    const field = wrapper.findAll('.field')[1]
    expect(field!.find('.pill.seed').text()).toContain('部署种子值')
    expect(field!.find('.pill.seed').text()).toContain('K8S_AI_LLM_MODEL')
    expect(field!.find('.env-hint.seed').text()).toContain('以页面配置为准')
  })

  it('未被部署变量覆盖的键不显示覆盖徽标', async () => {
    installScopedFetch()
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ENV_ITEMS.length))

    const field = wrapper.findAll('.field')[2]
    expect(field!.find('.pill').exists()).toBe(false)
    expect(field!.find('.env-hint').exists()).toBe(false)
  })

  it('保存返回 warnings 时以警告收尾，不得报"配置已保存"', async () => {
    installScopedFetch([
      'namespace 已写入配置库并立即生效：该键此前由部署变量 K8S_AI_NAMESPACE 提供首启种子值，保存后以页面配置为准。',
    ])
    const wrapper = mount(SettingsView)
    await flushPromises()
    await vi.waitFor(() => expect(wrapper.findAll('.field')).toHaveLength(ENV_ITEMS.length))

    await wrapper.findAll('.field')[2]!.find('input').setValue('team-a')
    await wrapper.find('.form-head .el-button').trigger('click')

    await vi.waitFor(() => expect(document.body.textContent).toContain('保存后以页面配置为准'))
    expect(document.body.textContent).not.toContain('配置已保存')
  })
})
