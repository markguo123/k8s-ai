<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ChatDotRound,
  Connection,
  Document,
  Finished,
  MagicStick,
  Setting,
  User,
} from '@element-plus/icons-vue'

import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const menuItems = [
  { index: '/app/chat', title: '聊天', icon: ChatDotRound },
  { index: '/app/cluster', title: '集群', icon: Connection },
  { index: '/app/skill', title: 'Skill', icon: MagicStick },
  { index: '/app/approval', title: '审批中心', icon: Finished },
  { index: '/app/audit', title: '审计', icon: Document },
  { index: '/app/settings', title: '配置中心', icon: Setting },
  { index: '/app/users', title: '用户与权限', icon: User },
]

const activeMenu = computed(() => route.path)
const pageTitle = computed<string>(() => (route.meta.title as string | undefined) ?? '控制台')
const displayName = computed(() => auth.user?.username ?? '未登录')
const displayRole = computed(() => auth.user?.role ?? 'viewer')

function handleLogout(): void {
  auth.logout()
  router.push({ name: 'login' })
}
</script>

<template>
  <el-container class="app-shell">
    <el-aside width="232px" class="app-aside">
      <div class="brand">
        <el-icon class="brand-mark"><Connection /></el-icon>
        <div class="brand-text">
          <strong>K8S-AI</strong>
          <span>Platform</span>
        </div>
      </div>

      <el-menu :default-active="activeMenu" router class="app-menu">
        <el-menu-item v-for="item in menuItems" :key="item.index" :index="item.index">
          <el-icon><component :is="item.icon" /></el-icon>
          <span>{{ item.title }}</span>
        </el-menu-item>
      </el-menu>

      <div class="aside-foot">
        <div class="user-chip">
          <el-avatar :size="28" class="user-avatar">{{ displayName[0]?.toUpperCase() }}</el-avatar>
          <div class="user-meta">
            <strong>{{ displayName }}</strong>
            <span>{{ displayRole }}</span>
          </div>
        </div>
        <el-button size="small" text @click="handleLogout">退出</el-button>
      </div>
    </el-aside>

    <el-container class="app-body">
      <el-header class="app-header" height="56px">
        <h1>{{ pageTitle }}</h1>
        <el-tag size="small" effect="plain" type="info">v4.0.0-dev</el-tag>
      </el-header>
      <el-main class="app-main">
        <RouterView />
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped>
.app-shell {
  height: 100%;
}

.app-aside {
  display: flex;
  flex-direction: column;
  background: var(--app-sidebar);
  color: var(--app-sidebar-text);
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 56px;
  padding: 0 16px;
  border-bottom: 1px solid #232a36;
}

.brand-mark {
  font-size: 22px;
  color: var(--app-accent);
}

.brand-text {
  display: flex;
  flex-direction: column;
  line-height: 1.15;
}

.brand-text strong {
  font-size: 15px;
  color: #fff;
  letter-spacing: 0;
}

.brand-text span {
  font-size: 11px;
  color: var(--app-sidebar-muted);
}

.app-menu {
  flex: 1;
  border-right: none;
  background: transparent;
  padding: 8px;
}

.app-menu :deep(.el-menu-item) {
  height: 40px;
  line-height: 40px;
  color: var(--app-sidebar-text);
  border-radius: 6px;
  margin-bottom: 2px;
}

.app-menu :deep(.el-menu-item:hover) {
  background: rgba(255, 255, 255, 0.06);
}

.app-menu :deep(.el-menu-item.is-active) {
  color: #fff;
  background: var(--app-accent-soft);
}

.app-menu :deep(.el-menu-item.is-active .el-icon) {
  color: var(--app-accent);
}

.aside-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 12px 14px;
  border-top: 1px solid #232a36;
}

.user-chip {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.user-avatar {
  flex: none;
  background: var(--app-accent);
  color: #fff;
}

.user-meta {
  display: flex;
  flex-direction: column;
  min-width: 0;
  line-height: 1.2;
}

.user-meta strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  color: #fff;
}

.user-meta span {
  font-size: 11px;
  color: var(--app-sidebar-muted);
  text-transform: capitalize;
}

.app-body {
  min-width: 0;
}

.app-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid var(--app-border);
  background: var(--app-surface);
}

.app-header h1 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
}

.app-main {
  padding: 20px;
  overflow: auto;
}
</style>
