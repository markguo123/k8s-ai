/**
 * P2.12 测试环境准备：jsdom 缺失的浏览器 API 兜底 + Element Plus 全量注册。
 * 组件测试按真实渲染路径跑（不 stub 组件库），保证"视觉/交互验收"和单测口径一致。
 */
import { config } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { vi } from 'vitest'

config.global.plugins = [ElementPlus]

// jsdom 未实现：Element Plus 的滚动/自适应组件会用到这三个 API。
class ResizeObserverStub {
  observe(): void {}
  unobserve(): void {}
  disconnect(): void {}
}

if (!('ResizeObserver' in globalThis)) {
  Object.defineProperty(globalThis, 'ResizeObserver', {
    writable: true,
    value: ResizeObserverStub,
  })
}

if (!globalThis.matchMedia) {
  Object.defineProperty(globalThis, 'matchMedia', {
    writable: true,
    value: vi.fn().mockImplementation((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
  })
}

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = vi.fn()
}
