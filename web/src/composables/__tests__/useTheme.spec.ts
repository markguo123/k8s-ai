import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * P7.1 / ADR-033：主题系统契约。
 * 覆盖三条验收：无存储时默认深色、跟随系统时按媒体查询取值、切换后持久化并落到 DOM。
 */
const STORAGE_KEY = 'k8s_ai_theme_mode'
let mediaMatches = false
let changeListener: ((event: MediaQueryListEvent) => void) | null = null

function stubMatchMedia(): void {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: mediaMatches,
    media: query,
    onchange: null,
    addEventListener: (_: string, cb: (event: MediaQueryListEvent) => void) => {
      changeListener = cb
    },
    removeEventListener: () => {},
    addListener: (cb: (event: MediaQueryListEvent) => void) => {
      changeListener = cb
    },
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia
}

async function loadThemeModule() {
  vi.resetModules()
  return import('../useTheme')
}

beforeEach(() => {
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
  document.documentElement.classList.remove('dark')
  mediaMatches = false
  changeListener = null
  stubMatchMedia()
})

describe('useTheme', () => {
  it('无存储时默认深色，并把属性与 class 落到 <html>', async () => {
    const { initTheme, useTheme } = await loadThemeModule()

    expect(useTheme().mode.value).toBe('dark')

    initTheme()
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  it('system 模式跟随系统深色偏好，并在偏好变化时重算', async () => {
    localStorage.setItem(STORAGE_KEY, 'system')
    mediaMatches = true
    const { initTheme, useTheme } = await loadThemeModule()
    const theme = useTheme()

    initTheme()
    expect(theme.resolved.value).toBe('dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')

    mediaMatches = false
    changeListener?.({ matches: false } as MediaQueryListEvent)
    expect(theme.resolved.value).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('切换主题后持久化，并在 system 模式下不写入解析后的具体值', async () => {
    const { setThemeMode, useTheme } = await loadThemeModule()
    const theme = useTheme()

    setThemeMode('light')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('light')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')

    setThemeMode('system')
    expect(localStorage.getItem(STORAGE_KEY)).toBe('system')
    expect(theme.mode.value).toBe('system')
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })
})
