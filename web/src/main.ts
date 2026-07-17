import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { bindSessionUnauthorizedHandler, useSessionStore } from './stores/session'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
bindSessionUnauthorizedHandler(useSessionStore(pinia))
app.use(router)
app.mount('#app')