import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

import AppLayout from '@/layouts/AppLayout.vue'
import { useAuthStore } from '@/stores/auth'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { title: '登录', public: true },
  },
  {
    path: '/app',
    component: AppLayout,
    meta: { requiresAuth: true },
    children: [
      { path: '', redirect: '/app/chat' },
      {
        path: 'chat',
        name: 'chat',
        component: () => import('@/views/chat/ChatView.vue'),
        meta: { title: '聊天' },
      },
      {
        path: 'cluster',
        name: 'cluster',
        component: () => import('@/views/cluster/ClusterView.vue'),
        meta: { title: '集群' },
      },
      {
        path: 'skill',
        name: 'skill',
        component: () => import('@/views/skill/SkillView.vue'),
        meta: { title: 'Skill' },
      },
      {
        path: 'approval',
        name: 'approval',
        component: () => import('@/views/approval/ApprovalView.vue'),
        meta: { title: '审批中心' },
      },
      {
        path: 'audit',
        name: 'audit',
        component: () => import('@/views/audit/AuditView.vue'),
        meta: { title: '审计' },
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('@/views/settings/SettingsView.vue'),
        meta: { title: '配置中心' },
      },
      {
        path: 'users',
        name: 'users',
        component: () => import('@/views/users/UsersView.vue'),
        meta: { title: '用户与权限' },
      },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/app/chat' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

router.beforeEach((to) => {
  const auth = useAuthStore()
  if (to.meta.requiresAuth && !auth.isAuthenticated) {
    return { name: 'login', query: to.fullPath === '/app/chat' ? {} : { redirect: to.fullPath } }
  }
  if (to.name === 'login' && auth.isAuthenticated) {
    return { path: '/app/chat' }
  }
  return true
})

export default router
