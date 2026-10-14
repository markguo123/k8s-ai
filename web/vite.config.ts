import { fileURLToPath, URL } from 'node:url'

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    // 四期后端走 /api/v1（P2 接入时联调）。
   proxy: {
  '/api/v1': { target: 'http://127.0.0.1:8080', changeOrigin: true },
  '/api/sessions': { target: 'http://127.0.0.1:8081', changeOrigin: true },
  },
  },
})
