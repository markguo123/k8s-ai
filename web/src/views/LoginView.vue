<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'

import { useAuthStore } from '@/stores/auth'
import { ApiRequestError } from '@/api'
// P7.3：登录页科技背景（来源 prototype/login-tech.html，令牌自包含）
import '@/styles/login-tech.css'
import { initLoginNet } from './loginNetScene'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const username = ref('admin')
const password = ref('')
const loading = ref(false)
const errorMessage = ref('')

// P7.3：启动样例里的 canvas 粒子网络（逐字搬运的脚本；挂载后 canvas 已存在）
let disposeNet: (() => void) | null = null

onMounted(() => {
  try {
    disposeNet = initLoginNet()
  } catch {
    // 背景动画失败不影响登录
  }
})

// 离开登录页必须停帧并解绑，否则 pointermove/interval 会一直挂在 window 上
onUnmounted(() => {
  disposeNet?.()
  disposeNet = null
})

async function handleLogin(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    await auth.login(username.value, password.value)
    const redirect = route.query.redirect
    router.push(typeof redirect === 'string' ? redirect : '/app/chat')
  } catch (err) {
    const message =
      err instanceof ApiRequestError ? err.message : '登录失败，请稍后重试'
    errorMessage.value = message
    ElMessage.error(message)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-ep">
    <!-- ══════════ 动态背景：canvas 粒子网络 + login-tech.css（口径见方案 §8） ══════════ -->
    <div class="scene" aria-hidden="true">
      <canvas id="net"></canvas>
      <div class="glow glow-a"></div>
      <div class="glow glow-b"></div>
      <div class="glow glow-c"></div>
      <div class="grid-plane"></div>
      <div class="beam"></div>
      <div class="scanlines"></div>
      <div class="vignette"></div>
    </div>

    <!-- ══════════ 登录区 ══════════ -->
    <main class="auth">
      <section class="card">
        <span class="card-line"></span>

        <header class="card-head">
          <div class="brand">
            <div class="logo">
              <svg viewBox="0 0 48 48" fill="none" aria-hidden="true">
                <defs>
                  <linearGradient id="lg" x1="0" y1="0" x2="48" y2="48">
                    <stop stop-color="#22d3ee" />
                    <stop offset="1" stop-color="#a855f7" />
                  </linearGradient>
                </defs>
                <g>
                  <animateTransform
                    attributeName="transform"
                    type="rotate"
                    from="0 24 24"
                    to="360 24 24"
                    dur="24s"
                    repeatCount="indefinite"
                  />
                  <path
                    d="M24 3 42 13.5v21L24 45 6 34.5v-21z"
                    stroke="url(#lg)"
                    stroke-width="1.6"
                    opacity=".9"
                  />
                  <path
                    d="M24 24 24 3M24 24l18 10.5M24 24v21M24 24 6 34.5M24 24 6 13.5"
                    stroke="url(#lg)"
                    stroke-width="1"
                    opacity=".45"
                  />
                </g>
                <circle cx="24" cy="24" r="3.4" fill="#22d3ee" />
              </svg>
            </div>
            <div class="brand-text">
              <strong>K8S-AI</strong>
              <span>企业级 Kubernetes AI 运维工作台</span>
            </div>
          </div>
          <div class="status"><i></i>在线</div>
        </header>

        <div class="card-title">
          <h1>欢迎回来</h1>
          <p>登录后即可巡检、诊断并处置集群异常</p>
        </div>

        <form class="form" novalidate @submit.prevent="handleLogin">
          <div class="field">
            <label class="field-label" for="login-username">账号</label>
            <div class="field-box">
              <svg
                class="icon"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linecap="round"
                stroke-linejoin="round"
                aria-hidden="true"
              >
                <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" />
                <circle cx="12" cy="7" r="4" />
              </svg>
              <input
                id="login-username"
                v-model="username"
                name="username"
                type="text"
                autocomplete="username"
                placeholder="admin"
                :disabled="loading"
              />
            </div>
          </div>

          <div class="field">
            <label class="field-label" for="login-password">密码</label>
            <div class="field-box">
              <svg
                class="icon"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                stroke-width="1.7"
                stroke-linecap="round"
                stroke-linejoin="round"
                aria-hidden="true"
              >
                <rect x="3" y="11" width="18" height="11" rx="2" />
                <path d="M7 11V7a5 5 0 0 1 10 0v4" />
              </svg>
              <input
                id="login-password"
                v-model="password"
                name="password"
                type="password"
                autocomplete="current-password"
                placeholder="••••••••••"
                :disabled="loading"
              />
            </div>
          </div>

          <p v-if="errorMessage" class="error">{{ errorMessage }}</p>

          <button class="btn" type="submit" :disabled="loading" :class="{ 'is-loading': loading }">
            <span class="btn-text">接 入 系 统</span>
            <span class="spinner"></span>
          </button>
        </form>

        <div class="foot">
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="1.8"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <rect x="3" y="11" width="18" height="11" rx="2" />
            <path d="M7 11V7a5 5 0 0 1 10 0v4" />
          </svg>
          按授权范围访问集群资源 · 操作全程审计
        </div>
      </section>
    </main>
  </div>
</template>

<style scoped>
.login-ep {
  height: 100%;
}

.error {
  margin: 0 0 12px;
  color: #f87171;
  font-size: 12px;
}
</style>

