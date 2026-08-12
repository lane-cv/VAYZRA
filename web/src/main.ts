import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { registerUnauthorizedHandler } from './api/client'
import { useSessionStore } from './stores/session'
import { useNotificationStore } from './stores/notifications'

let savedTheme: string | null = null
try { savedTheme = localStorage.getItem('happylearn-theme') } catch { /* Storage may be unavailable in hardened browsers. */ }
const initialTheme = savedTheme === 'dark' || savedTheme === 'light'
  ? savedTheme
  : typeof matchMedia === 'function' && matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
document.documentElement.dataset.theme = initialTheme
document.documentElement.style.colorScheme = initialTheme

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
const session = useSessionStore(pinia), notifications = useNotificationStore(pinia)
registerUnauthorizedHandler(() => { notifications.stop(); session.clear() })
app.use(router)
app.mount('#app')
