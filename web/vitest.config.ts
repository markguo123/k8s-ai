import { fileURLToPath, URL } from 'node:url'

import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

/**
 * 组件级测试配置（P2.12）。
 *
 * 单独成文件而不复用 vite.config.ts：构建配置只关心打包，测试需要 jsdom 环境与用例范围；
 * 两者共享的只有 `@` 别名，重复一处比让构建配置感知测试更可控。
 */
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.spec.ts'],
  },
})
