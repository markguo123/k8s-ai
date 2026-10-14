import { createApp } from 'vue'
import { createPinia } from 'pinia'
import ElementPlus from 'element-plus'
import zhCn from 'element-plus/es/locale/lang/zh-cn'
import 'element-plus/dist/index.css'
import 'element-plus/theme-chalk/dark/css-vars.css'

import App from './App.vue'
import router from './router'
import { useAuthStore } from './stores/auth'

import './styles/index.css'

// P2.13：平台默认深色主题（对齐原型 §8）。在 html 上挂载 `.dark`
// 以启用 Element Plus 的 dark css-vars；后续浅色切换只需维护该 class。
document.documentElement.classList.add('dark')

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)
app.use(ElementPlus, { locale: zhCn })

// P2.3：应用启动时恢复本地会话并通过 /auth/me 校正角色。
void useAuthStore(pinia).restore()

app.mount('#app')
