<script setup lang="ts">
import { computed, ref, watch, type Component } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  ArrowDown,
  ChatDotRound,
  Connection,
  Document,
  Finished,
  MagicStick,
  Monitor,
  Moon,
  Sunny,
  Expand,
  Fold,
  Setting,
  User,
} from '@element-plus/icons-vue'

import { useAuthStore } from '@/stores/auth'
import { useTheme, type ThemeMode } from '@/composables/useTheme'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const SIDEBAR_KEY = 'k8s_ai_sidebar_collapsed'
const sidebarCollapsed = ref(localStorage.getItem(SIDEBAR_KEY) === '1')

watch(sidebarCollapsed, (collapsed) => {
  localStorage.setItem(SIDEBAR_KEY, collapsed ? '1' : '0')
})

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

const PLATFORM_VERSION = 'v4.0.0-dev'
const isAdmin = computed(() => auth.user?.role === 'admin')

// P7.1：主题切换（light | dark | system），默认深色，选择持久化到 localStorage。
const { mode: themeMode, setThemeMode } = useTheme()

const THEME_LABELS: Record<ThemeMode, string> = {
  dark: '深色',
  light: '浅色',
  system: '跟随系统',
}
const THEME_OPTIONS: ThemeMode[] = ['dark', 'light', 'system']
const THEME_ICONS: Record<ThemeMode, Component> = { dark: Moon, light: Sunny, system: Monitor }
const themeIcon = computed(() => THEME_ICONS[themeMode.value])

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
  <el-container class="app-shell" :class="{ 'sidebar-collapsed': sidebarCollapsed }">
    <el-aside :width="sidebarCollapsed ? '64px' : '232px'" class="app-aside">
      <div class="brand">
        <div class="brand-logo">
          <el-icon class="brand-mark"><Connection /></el-icon>
        </div>
        <div v-show="!sidebarCollapsed" class="brand-text">
          <strong>K8S-AI</strong>
        </div>
      </div>

      <!-- 主侧栏：展开为分组菜单，收起为 64px 图标栏（data-tip 自绘 tooltip） -->
      <el-menu :default-active="activeMenu" router class="app-menu">
        <template v-for="group in menuGroups" :key="group.title">
          <div class="menu-group" :title="group.title" aria-hidden="true">
            {{ group.title }}
          </div>
          <el-menu-item
            v-for="item in group.items"
            :key="item.index"
            :index="item.index"
            :data-tip="item.title"
          >
            <el-icon class="menu-icon"><component :is="item.icon" /></el-icon>
            <span class="menu-label">{{ item.title }}</span>
          </el-menu-item>
        </template>
      </el-menu>

      <div class="sidebar-foot">
        <span>K8S-AI · Operations Console</span>
      </div>
    </el-aside>

    <el-container class="app-body">
      <el-header class="app-header" height="56px">
        <div class="header-left">
          <button
            type="button"
            class="icon-btn"
            :title="sidebarCollapsed ? '展开侧栏' : '收起侧栏'"
            @click="sidebarCollapsed = !sidebarCollapsed"
          >
            <el-icon><Expand v-if="sidebarCollapsed" /><Fold v-else /></el-icon>
          </button>
          <h1>{{ pageTitle }}</h1>
        </div>
        <div class="header-right">
          <!-- P7.1：主题独立入口（不放进用户菜单，避免和账号操作混在一起） -->
          <el-tooltip content="主题" placement="bottom">
            <el-dropdown trigger="click" @command="setThemeMode">
              <button type="button" class="theme-trigger" :aria-label="`主题：${THEME_LABELS[themeMode]}`">
                <el-icon><component :is="themeIcon" /></el-icon>
              </button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item v-for="option in THEME_OPTIONS" :key="option" :command="option">
                    <span class="theme-option">{{ THEME_LABELS[option] }}</span>
                    <span v-if="themeMode === option" class="theme-check">当前</span>
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>
          </el-tooltip>
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
                <el-dropdown-item disabled class="user-version">
                  <span>平台版本</span>
                  <el-tag size="small" effect="plain" type="info">{{ PLATFORM_VERSION }}</el-tag>
                </el-dropdown-item>
                <el-dropdown-item v-if="isAdmin" divided command="admin">
                  管理控制台
                </el-dropdown-item>
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
  /* P7.6 海拔：侧栏是玻璃层；用发丝线 + 右侧微阴影拉开层级，不用生硬描边 */
  background: var(--app-surface);
  backdrop-filter: blur(16px) saturate(120%);
  border-right: 1px solid var(--app-hairline);
  box-shadow: var(--app-panel-shadow);
  color: var(--app-sidebar-text);
  transition: width 0.2s ease;
  overflow: hidden;
}

.brand {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: 10px;
  height: 56px;
  padding: 0 16px;
  border-bottom: 1px solid var(--app-hairline);
  flex: none;
  transition: padding 0.2s ease;
}

.brand-logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  flex: none;
  border-radius: 10px;
  /* P7.6：与登录页 .logo 同款的品牌辉光（半透明主色底 + 双色光晕） */
  background: rgba(99, 102, 241, 0.09);
  border: 1px solid rgba(99, 102, 241, 0.28);
  box-shadow:
    0 0 22px -6px rgba(99, 102, 241, 0.75),
    inset 0 0 18px -8px rgba(34, 211, 238, 0.7);
}

.brand-mark {
  font-size: 16px;
  color: var(--app-accent-2);
}

.brand-text {
  display: flex;
  flex-direction: column;
  line-height: 1.15;
  min-width: 0;
}

.brand-text strong {
  font-size: 15px;
  color: #fff;
  letter-spacing: 0.3px;
  white-space: nowrap;
}

.app-menu {
  flex: 1;
  border-right: none;
  background: transparent;
  padding: 10px 8px;
  --el-menu-bg-color: transparent;
  --el-menu-text-color: var(--app-sidebar-text);
  --el-menu-hover-text-color: #fff;
  --el-menu-hover-bg-color: rgba(255, 255, 255, 0.06);
  --el-menu-active-color: var(--app-accent);
  transition: padding 0.2s ease;
}

.menu-group {
  padding: 12px 10px 6px;
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.4px;
  text-transform: uppercase;
  color: var(--app-sidebar-muted);
  white-space: nowrap;
  overflow: hidden;
}

.app-menu :deep(.el-menu-item) {
  position: relative;
  height: 36px;
  line-height: 36px;
  color: var(--app-sidebar-text);
  border: 1px solid transparent;
  border-radius: 8px;
  margin-bottom: 2px;
  white-space: nowrap;
  transition:
    transform 0.16s ease,
    background 0.16s ease,
    border-color 0.16s ease,
    color 0.16s ease;
}

/* P7.6：hover 微动效（-2px，克制不喧哗） */
.app-menu :deep(.el-menu-item:hover) {
  transform: translateY(-2px);
  background: var(--app-surface-hover);
  color: var(--app-strong);
}

/* P7.6 选中态：半透明主色底 + 左侧 2px 高亮条 + 微弱光晕，替掉扁平色块 */
.app-menu :deep(.el-menu-item.is-active) {
  color: #fff;
  background: rgba(99, 102, 241, 0.15);
  border-color: rgba(99, 102, 241, 0.35);
  box-shadow: -8px 0 24px -12px rgba(99, 102, 241, 0.8);
}

.app-menu :deep(.el-menu-item.is-active::before) {
  content: '';
  position: absolute;
  left: 0;
  top: 20%;
  bottom: 20%;
  width: 2px;
  border-radius: 2px;
  background: linear-gradient(180deg, var(--app-accent-2), var(--app-accent-3));
  box-shadow: 0 0 10px rgba(99, 102, 241, 0.9);
}

.app-menu :deep(.el-menu-item.is-active .el-icon) {
  color: var(--app-accent-2);
}

.menu-icon {
  font-size: 15px;
  flex: none;
}

.sidebar-foot {
  flex: none;
  padding: 10px 16px;
  font-size: 11px;
  color: var(--app-sidebar-muted);
  border-top: 1px solid var(--app-hairline);
  white-space: nowrap;
  overflow: hidden;
}

/* ---------- 收起态：64px 图标栏 + 自绘 tooltip ---------- */
.app-shell.sidebar-collapsed .app-aside {
  width: 64px;
}

.app-shell.sidebar-collapsed .brand {
  justify-content: center;
  padding: 13px 0;
  gap: 0;
}

.app-shell.sidebar-collapsed .brand-text {
  display: none;
}

.app-shell.sidebar-collapsed .app-menu {
  padding: 8px 0;
}

.app-shell.sidebar-collapsed .menu-group {
  font-size: 0;
  height: 1px;
  margin: 8px 10px;
  padding: 0;
  border-top: 1px solid rgba(255, 255, 255, 0.08);
}

.app-shell.sidebar-collapsed .menu-group:first-of-type {
  border-top: none;
}

.app-shell.sidebar-collapsed .app-menu :deep(.el-menu-item) {
  justify-content: center;
  padding: 0;
  margin: 2px 8px;
  height: 40px;
  line-height: 40px;
}

.app-shell.sidebar-collapsed .menu-label {
  display: none;
}

.app-shell.sidebar-collapsed .app-menu :deep(.el-menu-item) {
  position: relative;
}

.app-shell.sidebar-collapsed .app-menu :deep(.el-menu-item[data-tip]::after) {
  content: attr(data-tip);
  position: absolute;
  left: calc(100% + 12px);
  top: 50%;
  transform: translateY(-50%);
  padding: 5px 9px;
  border-radius: 6px;
  background: #0b0e14;
  color: #e8eaf0;
  font-size: 11.5px;
  white-space: nowrap;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.45);
  z-index: 80;
  pointer-events: none;
  opacity: 0;
  visibility: hidden;
  transition:
    opacity 0.12s ease 0.05s,
    visibility 0.12s;
}

.app-shell.sidebar-collapsed .app-menu :deep(.el-menu-item[data-tip]::before) {
  content: '';
  position: absolute;
  left: 100%;
  top: 50%;
  transform: translateY(-50%);
  border: 5px solid transparent;
  border-right-color: #0b0e14;
  z-index: 80;
  opacity: 0;
  visibility: hidden;
  transition:
    opacity 0.12s ease 0.05s,
    visibility 0.12s;
}

.app-shell.sidebar-collapsed .app-menu :deep(.el-menu-item[data-tip]:hover::after),
.app-shell.sidebar-collapsed .app-menu :deep(.el-menu-item[data-tip]:hover::before) {
  opacity: 1;
  visibility: visible;
}

.app-shell.sidebar-collapsed .sidebar-foot {
  display: none;
}

.header-left {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
}

.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  flex: none;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-surface-2);
  color: var(--app-muted);
  cursor: pointer;
  font-size: 15px;
  transition:
    color 0.15s ease,
    border-color 0.15s ease;
}

.icon-btn:hover {
  color: var(--app-text);
  border-color: var(--app-accent);
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
  background: var(--app-surface-hover);
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

.user-version {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  font-size: 12px;
  color: var(--app-muted);
}

.app-body {
  min-width: 0;
  /* P7.2 经典双栏：纵向容器必须允许收缩，否则消息区会把底部输入框顶出视口 */
  min-height: 0;
}

.app-header {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: space-between;
  /* P7.6 海拔：顶栏与侧栏同属玻璃层；分割线改渐变（见 ::after），不再用实线 */
  background: var(--app-surface);
  backdrop-filter: blur(16px) saturate(120%);
  border-bottom: none;
}

/* 顶栏底部渐变分割线：中段亮、两端透明，避免生硬横线把版面切断 */
.app-header::after {
  content: '';
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 1px;
  background: linear-gradient(90deg, transparent, rgba(99, 102, 241, 0.5), transparent);
  pointer-events: none;
}

.app-header h1 {
  margin: 0;
  font-size: 15px;
  font-weight: 650;
}

.app-main {
  /* P7.6 留白：画布四周给 20px，双栏才有"浮在背景上"的呼吸感 */
  min-height: 0;
  display: flex;
  flex-direction: column;
  padding: 20px;
  overflow: auto;
}

/* P7.1：顶栏主题入口（图标按钮，与用户菜单分离） */
.theme-trigger {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-surface);
  color: var(--app-muted);
  cursor: pointer;
}

.theme-trigger:hover {
  border-color: var(--app-accent);
  color: var(--app-accent);
}

.theme-option {
  min-width: 72px;
}

.theme-check {
  margin-left: 12px;
  color: var(--app-accent);
  font-size: 11px;
}

@media (max-width: 900px) {
  .app-main {
    padding: 12px;
  }
}

/* P7.6：动效降级 */
@media (prefers-reduced-motion: reduce) {
  .app-menu :deep(.el-menu-item:hover) {
    transform: none;
  }
}
</style>
