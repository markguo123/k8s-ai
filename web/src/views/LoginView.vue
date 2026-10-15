<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'

import { useAuthStore } from '@/stores/auth'
import { ApiRequestError } from '@/api'

const auth = useAuthStore()
const route = useRoute()
const router = useRouter()

const username = ref('admin')
const password = ref('')
const loading = ref(false)
const errorMessage = ref('')

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
  <div class="login-page">
    <div class="login-card">
      <div class="login-brand">
        <span class="login-logo">K</span>
        <h1>K8S-AI</h1>
      </div>
      <p class="login-tip">企业级 Kubernetes AI 运维工作台 · 登录进入控制台</p>
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
        <el-form-item v-if="errorMessage" class="login-error">
          <el-alert :title="errorMessage" type="error" :closable="false" show-icon />
        </el-form-item>
        <el-button class="login-submit" type="primary" size="large" :loading="loading" @click="handleLogin">
          登录
        </el-button>
      </el-form>
      <footer class="login-foot">平台内权限由角色与 namespace 授权决定 · 真实权限来自集群 RBAC</footer>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  /* 对齐原型 .login 的径向高光底色 */
  background: radial-gradient(1200px 700px at 20% -10%, rgba(99, 102, 241, 0.25), transparent 55%),
    var(--app-bg);
}

.login-card {
  width: 390px;
  max-width: calc(100vw - 32px);
  padding: 30px 30px 22px;
  background: var(--app-surface);
  border: 1px solid var(--app-border);
  border-radius: 14px;
  box-shadow: var(--app-shadow);
}

.login-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 4px;
}

.login-logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  flex: none;
  border-radius: 9px;
  background: linear-gradient(135deg, var(--app-accent), #a855f7);
  color: #fff;
  font-weight: 700;
  font-size: 15px;
}

.login-brand h1 {
  margin: 0;
  font-size: 20px;
  font-weight: 700;
}

.login-tip {
  margin: 2px 0 20px;
  color: var(--app-muted);
  font-size: 12.5px;
}

.login-card :deep(.el-form-item__label) {
  margin: 14px 0 6px;
  padding: 0;
  color: var(--app-muted);
  font-size: 12px;
  line-height: 1.4;
}

.login-card :deep(.el-form-item) {
  margin-bottom: 0;
}

.login-card :deep(.el-input__wrapper) {
  height: 36px;
  padding: 0 12px;
  border-radius: var(--app-radius-sm);
  background: var(--app-bg);
  box-shadow: 0 0 0 1px var(--app-border) inset;
}

.login-card :deep(.el-input__wrapper.is-focus) {
  box-shadow: 0 0 0 1px var(--app-accent) inset;
}

.login-card :deep(.el-input__inner) {
  font-size: 13.5px;
}

.login-submit {
  width: 100%;
  height: 37px;
  margin-top: 20px;
  border-radius: var(--app-radius-sm);
}

.login-error {
  margin-bottom: 4px;
}

.login-foot {
  margin-top: 18px;
  color: var(--app-faint);
  font-size: 11.5px;
  text-align: center;
}
</style>
