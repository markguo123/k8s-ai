import { computed, ref } from 'vue'

/**
 * P7.1 / ADR-033 / D-23 主题系统。
 *
 * 约定：`<html data-theme="dark|light">` 承载应用令牌，同时同步 `<html class="dark">`
 * （Element Plus 深色变量）；模式为 light | dark | system，**默认 dark**，持久化键
 * `k8s_ai_theme_mode`。首屏由 `index.html` 内联脚本提前设置，本模块负责运行时切换。
 */
export type ThemeMode = 'light' | 'dark' | 'system'
export type ResolvedTheme = 'light' | 'dark'

export const THEME_STORAGE_KEY = 'k8s_ai_theme_mode'
export const DARK_MEDIA_QUERY = '(prefers-color-scheme: dark)'

function readStoredMode(): ThemeMode {
  try {
    const raw = localStorage.getItem(THEME_STORAGE_KEY)
    if (raw === 'light' || raw === 'dark' || raw === 'system') {
      return raw
    }
  } catch {
    // localStorage 不可用时按默认深色处理
  }
  return 'dark'
}

function systemPrefersDark(): boolean {
  try {
    return window.matchMedia(DARK_MEDIA_QUERY).matches
  } catch {
    return true
  }
}

const mode = ref<ThemeMode>(readStoredMode())
const systemDark = ref(systemPrefersDark())
const resolved = computed<ResolvedTheme>(() =>
  mode.value === 'system' ? (systemDark.value ? 'dark' : 'light') : mode.value,
)

function applyTheme(theme: ResolvedTheme): void {
  const root = document.documentElement
  root.setAttribute('data-theme', theme)
  root.classList.toggle('dark', theme === 'dark')
}

let mediaBound = false

function bindSystemListener(): void {
  if (mediaBound) {
    return
  }
  mediaBound = true
  try {
    const media = window.matchMedia(DARK_MEDIA_QUERY)
    const onChange = (event: MediaQueryListEvent): void => {
      systemDark.value = event.matches
      if (mode.value === 'system') {
        applyTheme(resolved.value)
      }
    }
    if (typeof media.addEventListener === 'function') {
      media.addEventListener('change', onChange)
    } else if (typeof media.addListener === 'function') {
      media.addListener(onChange)
    }
  } catch {
    // 无 matchMedia 环境（极端降级）不做跟随
  }
}

/** 应用启动时调用：绑定系统偏好监听并把当前模式同步到 DOM（幂等）。 */
export function initTheme(): void {
  bindSystemListener()
  applyTheme(resolved.value)
}

export function setThemeMode(next: ThemeMode): void {
  mode.value = next
  try {
    localStorage.setItem(THEME_STORAGE_KEY, next)
  } catch {
    // 持久化失败不影响本次切换
  }
  applyTheme(resolved.value)
}

export function useTheme() {
  return { mode, resolved, setThemeMode, initTheme }
}

export default useTheme
