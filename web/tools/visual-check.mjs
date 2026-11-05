/**
 * P2 视觉核验：把原型（docs/02-架构与设计/prototype/index.html）与四期 SPA 的
 * 关键视觉量（尺寸/位置/颜色/字号）在同一浏览器里量出来做逐项对比。
 *
 * 用法：
 *   node tools/mock-backend.mjs &   # 终端 1
 *   npm run dev &                   # 终端 2
 *   node tools/visual-check.mjs     # 输出差异表与截图目录 web/.visual/
 */
import { existsSync, mkdirSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'

import puppeteer from 'puppeteer-core'

const here = dirname(fileURLToPath(import.meta.url))
const outDir = resolve(here, '..', '.visual')
const appUrl = process.env.APP_URL || 'http://127.0.0.1:5173'
const prototypeUrl = pathToFileURL(
  resolve(here, '..', '..', 'docs', '02-架构与设计', 'prototype', 'index.html'),
).href

const EDGE_CANDIDATES = [
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
]
const executablePath =
  process.env.BROWSER_PATH || EDGE_CANDIDATES.find((path) => existsSync(path))
if (!executablePath) {
  console.error('未找到 Edge/Chrome，请设置 BROWSER_PATH')
  process.exit(1)
}

/** 对比探针：原型选择器 ↔ 四期组件选择器 + 关注属性。 */
const CHAT_PROBES = [
  { label: '侧栏', proto: '.sidebar', app: '.app-aside', props: ['width', 'backgroundColor'] },
  { label: '顶栏', proto: '.topbar', app: '.app-header', props: ['height', 'backgroundColor', 'borderBottomColor'] },
  { label: '会话栏宽度', proto: '.conversations', app: '.conversation-list', props: ['width'] },
  { label: '会话项卡片', proto: '.conv', app: '.session-item', props: ['backgroundColor', 'borderColor', 'borderRadius', 'paddingTop'] },
  { label: '回合卡片', proto: '.turn', app: '.turn', props: ['borderRadius', 'borderColor', 'backgroundColor'] },
  { label: '回合头', proto: '.turn-head', app: '.turn-head', props: ['paddingTop', 'fontSize'] },
  { label: '用户诉求块', proto: '.bubble.msg-user .tool', app: '.turn-question', props: ['borderLeftColor', 'backgroundColor'] },
  { label: '思考面板', proto: '.reasoning', app: '.reasoning', props: ['borderLeftColor', 'borderLeftWidth', 'backgroundColor'] },
  { label: '工具卡', proto: '.tool.ok', app: '.toolcard', props: ['borderLeftColor', 'backgroundColor'] },
  { label: '工具卡头字号', proto: '.tool-head .name', app: '.toolcard-title', props: ['fontSize', 'fontWeight'] },
  { label: '审批卡', proto: '#approval-card', app: '.approval-card', props: ['borderLeftColor'] },
  { label: '报告块', proto: '.md', app: '.report-block', props: ['borderLeftColor', 'backgroundColor'] },
  { label: 'Token 浮标', proto: '.token-badge', app: '.token-badge', props: ['right', 'bottom', 'fontSize', 'borderRadius', 'backgroundColor'] },
  { label: '输入框', proto: '#composer-ta', app: '.composer-textarea', props: ['fontSize', 'borderRadius', 'backgroundColor'] },
  { label: '发送按钮', proto: '.send-btn', app: '.composer-actions .el-button', props: ['backgroundColor', 'borderRadius', 'height'] },
]

/** 登录页探针（原型 #view-login ↔ LoginView.vue）。 */
const LOGIN_PROBES = [
  { label: '登录卡片', proto: '.login-card', app: '.login-card', props: ['width', 'backgroundColor', 'borderRadius', 'paddingTop'] },
  // 原型输入框自身即样式元素；四期走 el-input，量最外层 wrapper 才等价，
  // 字号单独量内层 input（wrapper 只继承 body 字号）。
  { label: '登录输入框', proto: '.field input', app: '.login-card .el-input__wrapper', props: ['height', 'borderRadius', 'backgroundColor'] },
  { label: '登录输入框字号', proto: '.field input', app: '.login-card .el-input__inner', props: ['fontSize'] },
  { label: '登录按钮', proto: '.login .btn', app: '.login-card .el-button', props: ['height', 'borderRadius', 'backgroundColor'] },
]

const PROBES = [...CHAT_PROBES, ...LOGIN_PROBES]
const PROPS = ['width', 'height', 'fontSize', 'fontWeight', 'color', 'backgroundColor', 'borderRadius', 'borderColor', 'borderBottomColor', 'borderLeftColor', 'borderLeftWidth', 'paddingTop', 'right', 'bottom']

const MEASURE = (probes, props) => {
  const out = {}
  for (const probe of probes) {
    const el = document.querySelector(probe.app)
    if (!el) {
      out[probe.label] = null
      continue
    }
    const style = getComputedStyle(el)
    const rect = el.getBoundingClientRect()
    const values = {}
    for (const prop of props) {
      values[prop] = prop === 'width' || prop === 'height' ? `${Math.round(rect[prop])}px` : style[prop]
    }
    out[probe.label] = values
  }
  return out
}

const browser = await puppeteer.launch({
  executablePath,
  headless: true,
  args: ['--no-sandbox', '--disable-gpu'],
  defaultViewport: { width: 1600, height: 1000 },
})

mkdirSync(outDir, { recursive: true })

async function seedLogin(appPage) {
  await appPage.goto(`${appUrl}/login`, { waitUntil: 'networkidle2' })
  await appPage.evaluate(() => {
    localStorage.setItem('k8s_ai_token', 'demo-token')
    localStorage.setItem(
      'k8s_ai_user',
      JSON.stringify({ userId: 'u1', username: 'admin', displayName: '管理员', role: 'admin' }),
    )
  })
}

function normalize(values) {
  return values.replace(/rgb\((\d+), (\d+), (\d+)\)/g, '#$1,$2,$3').replace(/\s+/g, ' ').trim()
}

try {
  // ---- 原型 ----
  const proto = await browser.newPage()
  await proto.goto(prototypeUrl, { waitUntil: 'load' })
  const protoLogin = await proto.evaluate(MEASURE, LOGIN_PROBES.map((p) => ({ label: p.label, app: p.proto })), PROPS)
  await proto.evaluate(() => {
    document.getElementById('view-login').classList.add('hidden')
    document.getElementById('app').classList.remove('hidden')
    window.showView('chat')
  })
  await proto.screenshot({ path: resolve(outDir, 'prototype-chat.png') })
  const protoChat = await proto.evaluate(MEASURE, CHAT_PROBES.map((p) => ({ label: p.label, app: p.proto })), PROPS)

  // ---- 四期 SPA ----
  const app = await browser.newPage()
  app.on('console', (msg) => {
    if (msg.type() === 'error' && !msg.text().includes('Failed to load resource')) {
      console.error('[browser]', msg.text())
    }
  })
  await app.goto(`${appUrl}/login`, { waitUntil: 'networkidle2' })
  await app.evaluate(() => localStorage.clear())
  await app.goto(`${appUrl}/login`, { waitUntil: 'networkidle2' })
  await app.waitForSelector('.login-card', { timeout: 10_000 })
  await app.screenshot({ path: resolve(outDir, 'app-login.png') })
  const appLogin = await app.evaluate(MEASURE, LOGIN_PROBES.map((p) => ({ label: p.label, app: p.app })), PROPS)

  await seedLogin(app)
  await app.goto(`${appUrl}/app/chat`, { waitUntil: 'networkidle2' })
  await app.waitForSelector('.session-item', { timeout: 10_000 })
  await app.click('.session-item')
  await app.waitForSelector('.toolcard', { timeout: 10_000 })
  // 发送按钮的填充色只在可点击态出现（原型 .send-btn 常态即为主色），
  // 因此输入一段文本让按钮进入可发送态再量。
  await app.type('.composer-textarea', '继续排查')
  await new Promise((r) => setTimeout(r, 600))
  await app.screenshot({ path: resolve(outDir, 'app-chat.png') })
  const appChat = await app.evaluate(MEASURE, CHAT_PROBES.map((p) => ({ label: p.label, app: p.app })), PROPS)

  // 结构自检：一个完整回合应渲染出全部步骤卡，且不重复、不漏。
  const structure = await app.evaluate(() => {
    const count = (selector) => document.querySelectorAll(selector).length
    return {
      turns: count('.turn'),
      reasoning: count('.reasoning'),
      tools: count('.toolcard'),
      approvals: count('.approval-card'),
      reports: count('.report-block'),
      stepperSteps: count('.turn .step'),
      tokenBadge: document.querySelector('.token-badge')?.textContent?.trim() ?? '',
      question: count('.turn-question'),
    }
  })

  const protoValues = { ...protoLogin, ...protoChat }
  const appValues = { ...appLogin, ...appChat }

  let diffs = 0
  console.log('原型 vs 四期 Web Shell 关键视觉量（1600x1000）\n')
  for (const probe of PROBES) {
    const left = protoValues[probe.label]
    const right = appValues[probe.label]
    if (!right) {
      console.log(`- ${probe.label}: 四期侧未找到元素（${probe.app}）`)
      diffs += 1
      continue
    }
    const lines = []
    for (const prop of probe.props) {
      const a = normalize(String(left?.[prop] ?? 'missing'))
      const b = normalize(String(right[prop]))
      if (a !== b) {
        lines.push(`    ${prop}: 原型=${a} | 四期=${b}`)
      }
    }
    if (lines.length) {
      diffs += lines.length
      console.log(`- ${probe.label}`)
      console.log(lines.join('\n'))
    } else {
      console.log(`- ${probe.label}: 一致`)
    }
  }
  console.log(`\n差异项合计：${diffs}（截图：web/.visual/prototype-chat.png、web/.visual/app-chat.png）`)
  console.log('\n回合结构自检（mock 回合：1 个思考 + 3 个工具 + 1 个审批 + 1 个报告）')
  console.log(`  回合=${structure.turns} 思考=${structure.reasoning} 工具卡=${structure.tools} 审批卡=${structure.approvals} 报告=${structure.reports} 步进器节点=${structure.stepperSteps} 用户诉求=${structure.question}`)
  console.log(`  Token 浮标="${structure.tokenBadge}"`)
} finally {
  await browser.close()
}
