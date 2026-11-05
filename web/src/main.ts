import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'

import App from './App.vue'
import router from './router'
import { useAuthStore } from './stores/auth'
import { initTheme } from './composables/useTheme'

import './styles/index.css'
// P7.6：聊天页科技感视觉体系（令牌与登录页同源，覆盖 index.css 的底色）
import './styles/tech-theme.css'

// P7.1：主题由 index.html 内联脚本提前设置（防首屏闪烁），此处绑定系统偏好监听并幂等兜底。
initTheme()

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)
app.use(ElementPlus, { locale: zhCn })

// P2.3：应用启动时恢复本地会话并通过 /auth/me 校正角色。
void useAuthStore(pinia).restore()

app.mount('#app')
