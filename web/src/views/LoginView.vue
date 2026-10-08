<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useAuthStore } from '@/stores/auth'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const username = ref('admin')
const password = ref('')
const loading = ref(false)

async function handleLogin(): Promise<void> {
  loading.value = true
  try {
    await auth.login(username.value, password.value)
    const redirect = route.query.redirect
    router.push(typeof redirect === 'string' ? redirect : '/app/chat')
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <span class="login-mark">K8S</span>
        <strong>K8S-AI Platform</strong>
      </div>
      <el-form label-position="top" size="large" @submit.prevent="handleLogin">
        <el-form-item label="用户名">
          <el-input v-model="username" autocomplete="username" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="password"
            type="password"
            show-password
            autocomplete="current-password"
          />
        </el-form-item>
        <el-button class="login-submit" type="primary" size="large" :loading="loading" @click="handleLogin">
          登录
        </el-button>
      </el-form>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  background:
    linear-gradient(180deg, #151920 0%, #20263a 48%, #2b3550 100%);
}

.login-card {
  width: 360px;
  max-width: calc(100vw - 32px);
  padding: 28px 28px 24px;
  background: var(--app-surface);
  border-radius: 10px;
  box-shadow: 0 12px 32px rgba(0, 0, 0, 0.28);
}

.login-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 24px;
}

.login-mark {
  display: inline-flex;
  align-items: center;
  padding: 4px 8px;
  border-radius: 6px;
  background: var(--app-accent);
  color: #fff;
  font-weight: 700;
  font-size: 13px;
}

.login-brand strong {
  font-size: 17px;
}

.login-submit {
  width: 100%;
  margin-top: 4px;
}
</style>
