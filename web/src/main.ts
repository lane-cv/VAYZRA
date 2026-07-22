import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { registerUnauthorizedHandler } from './api/client'
import { useSessionStore } from './stores/session'
import { useNotificationStore } from './stores/notifications'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
const session = useSessionStore(pinia), notifications = useNotificationStore(pinia)
registerUnauthorizedHandler(() => { notifications.stop(); session.clear() })
app.use(router)
app.mount('#app')
