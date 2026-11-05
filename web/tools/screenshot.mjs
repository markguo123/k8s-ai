/**
 * 前端视觉核验脚本（P2 验收工具，不参与生产构建）。
 *
 * 用本机 Edge/Chrome（无头）打开运行中的开发服务器，注入登录态后截图，
 * 便于与 `docs/02-架构与设计/prototype/index.html` 原型逐块比对。
 *
 * 用法：
 *   node tools/mock-backend.mjs &        # 终端 1
 *   npm run dev &                        # 终端 2
 *   node tools/screenshot.mjs            # 输出 web/.visual/
 */
import { existsSync, mkdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import puppeteer from 'puppeteer-core'

const here = dirname(fileURLToPath(import.meta.url))
const outDir = resolve(here, '..', '.visual')
const appUrl = process.env.APP_URL || 'http://127.0.0.1:5173'

const EDGE_CANDIDATES = [
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
]

const executablePath =
  process.env.BROWSER_PATH || EDGE_CANDIDATES.find((path) => existsSync(path))

if (!executablePath) {
  console.error('未找到 Edge/Chrome，请设置 BROWSER_PATH 指向浏览器可执行文件')
  process.exit(1)
}

mkdirSync(outDir, { recursive: true })

const browser = await puppeteer.launch({
  executablePath,
  headless: true,
  args: ['--no-sandbox', '--disable-gpu'],
  defaultViewport: { width: 1600, height: 1000 },
})

try {
  const page = await browser.newPage()
  page.on('console', (msg) => {
    if (msg.type() === 'error') {
      console.error('[browser]', msg.text())
    }
  })
  const shots = [
    { name: 'login', path: '/login' },
    { name: 'chat', path: '/app/chat' },
  ]
  await page.goto(`${appUrl}/login`, { waitUntil: 'networkidle2' })
  await page.evaluate(() => {
    localStorage.setItem('k8s_ai_token', 'demo-token')
    localStorage.setItem(
      'k8s_ai_user',
      JSON.stringify({ userId: 'u1', username: 'admin', displayName: '管理员', role: 'admin' }),
    )
  })
  for (const shot of shots) {
    await page.goto(`${appUrl}${shot.path}`, { waitUntil: 'networkidle2' })
    await page.waitForSelector('.app-shell, .login-card', { timeout: 10_000 }).catch(() => {})
    await new Promise((r) => setTimeout(r, 1200))
    await page.screenshot({ path: resolve(outDir, `${shot.name}.png`), fullPage: false })
    console.log(`[screenshot] ${shot.name} → .visual/${shot.name}.png`)
  }
} finally {
  await browser.close()
}
