import { fileURLToPath } from 'node:url'

import { configDefaults, defineConfig, mergeConfig } from 'vitest/config'

import viteConfig from './vite.config'

/**
 * P2.12 前端测试入口：复用 Vite 别名与插件，jsdom 环境跑组件/组合式/路由测试。
 * 组件测试统一走 Element Plus 全量插件（见 src/test/setup.ts），
 * 保证与浏览器渲染行为一致（不 mock 组件库内部实现）。
 */
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'jsdom',
      globals: true,
      root: fileURLToPath(new URL('./', import.meta.url)),
      setupFiles: ['./src/test/setup.ts'],
      include: ['src/**/*.spec.ts'],
      exclude: [...configDefaults.exclude, 'dist/**'],
      restoreMocks: true,
      clearMocks: true,
      css: false,
    },
  }),
)
