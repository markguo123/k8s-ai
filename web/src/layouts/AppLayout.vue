<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ArrowDown,
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

const menuGroups = [
  {
    title: '工作区',
    items: [
      { index: '/app/chat', title: '聊天', icon: ChatDotRound },
      { index: '/app/cluster', title: '集群', icon: Connection },
    ],
  },
  {
    title: '治理',
    items: [
      { index: '/app/approval', title: '审批中心', icon: Finished },
      { index: '/app/audit', title: '审计', icon: Document },
    ],
  },
  {
    title: '平台管理',
    items: [
      { index: '/app/skill', title: 'Skill', icon: MagicStick },
      { index: '/app/settings', title: '配置中心', icon: Setting },
      { index: '/app/users', title: '用户与权限', icon: User },
    ],
  },
]

const activeMenu = computed(() => route.path)
const pageTitle = computed<string>(() => (route.meta.title as string | undefined) ?? '控制台')
const displayName = computed(() => auth.user?.username ?? '未登录')
const displayRole = computed(() => auth.user?.role ?? 'viewer')
const userInitial = computed(() => displayName.value.charAt(0).toUpperCase() || 'K')

function handleUserCommand(command: string | number | object): void {
  if (command === 'logout') {
    auth.logout()
    router.push({ name: 'login' })
  } else if (command === 'admin') {
    router.push({ path: '/app/admin' })
  }
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
        <el-sub-menu v-for="group in menuGroups" :key="group.title" :index="group.title">
          <template #title>
            <span class="group-title">{{ group.title }}</span>
          </template>
          <el-menu-item v-for="item in group.items" :key="item.index" :index="item.index">
            <el-icon><component :is="item.icon" /></el-icon>
            <span>{{ item.title }}</span>
          </el-menu-item>
        </el-sub-menu>
      </el-menu>
    </el-aside>

    <el-container class="app-body">
      <el-header class="app-header" height="56px">
        <h1>{{ pageTitle }}</h1>
        <div class="header-right">
          <el-tag size="small" effect="plain" type="info">v4.0.0-dev</el-tag>
          <el-dropdown trigger="click" @command="handleUserCommand">
            <div class="user-menu">
              <el-avatar :size="28" class="user-avatar">{{ userInitial }}</el-avatar>
              <div class="user-text">
                <strong>{{ displayName }}</strong>
                <span>{{ displayRole }}</span>
              </div>
              <el-icon class="user-caret"><ArrowDown /></el-icon>
            </div>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item disabled class="user-roles">
                  <div class="user-roles-head">
                    <strong>{{ displayName }}</strong>
                    <span>{{ displayRole }}</span>
                  </div>
                </el-dropdown-item>
                <el-dropdown-item divided command="admin">管理控制台</el-dropdown-item>
                <el-dropdown-item command="logout">退出登录</el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
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

.app-menu :deep(.el-sub-menu__title) {
  height: 34px;
  line-height: 34px;
  color: var(--app-sidebar-muted);
  font-size: 12px;
}

.app-menu :deep(.el-sub-menu__title:hover) {
  background: transparent;
  color: var(--app-sidebar-muted);
}

.group-title {
  font-weight: 600;
}

.header-right {
  display: flex;
  align-items: center;
  gap: 16px;
}

.user-menu {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  border-radius: 8px;
  cursor: pointer;
  outline: none;
}

.user-menu:hover {
  background: var(--app-bg);
}

.user-menu:focus-visible {
  min-width: 0;
  box-shadow: 0 0 0 2px var(--app-accent);
}

.user-avatar {
  flex: none;
  background: var(--app-accent);
  color: #fff;
}

.user-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
  line-height: 1.2;
  text-align: left;
}

.user-text strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
}

.user-text span {
  font-size: 11px;
  color: var(--app-muted);
  text-transform: capitalize;
}

.user-caret {
  color: var(--app-muted);
}

.user-roles-head {
  display: flex;
  flex-direction: column;
  line-height: 1.3;
}

.user-roles-head strong {
  font-size: 13px;
}

.user-roles-head span {
  font-size: 11px;
  color: var(--app-muted);
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
