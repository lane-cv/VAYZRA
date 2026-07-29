<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { request } from '../api/client'
import { useSessionStore } from '../stores/session'
import { useNotificationStore } from '../stores/notifications'
import { useAIRunStore } from '../stores/aiRuns'

const session = useSessionStore()
const notifications = useNotificationStore()
const aiRuns = useAIRunStore()
const router = useRouter()
const route = useRoute()
const drawerOpen = ref(false)
const logoutPending = ref(false)
const menuTrigger = ref<HTMLButtonElement>()
const navigation = ref<HTMLElement>()
const mediaQuery = typeof window === 'undefined' ? undefined : window.matchMedia('(max-width: 760px)')
const isMobile = ref(mediaQuery?.matches ?? false)
const isAdmin = computed(() => session.user?.role === 'admin')
function closeDrawer(restoreFocus = false) { drawerOpen.value = false; if (restoreFocus) void nextTick(() => menuTrigger.value?.focus()) }
function openDrawer() { drawerOpen.value = true; void nextTick(() => navigation.value?.querySelector<HTMLElement>('a, button')?.focus()) }
function handleKeydown(event: KeyboardEvent) { if (event.key === 'Escape' && drawerOpen.value) closeDrawer(true) }
function updateViewport(event: MediaQueryListEvent) { isMobile.value = event.matches; if (!event.matches) drawerOpen.value = false }
async function logout() { if (logoutPending.value) return; logoutPending.value = true; notifications.stop(); try { await request('/auth/logout', { method: 'POST' }) } catch { /* The server session may already be expired. */ } finally { aiRuns.clearAll(); session.clear(); closeDrawer(); logoutPending.value = false; await router.replace('/login') } }
async function logoutOthers() { try { await request('/auth/logout-others', { method: 'POST' }) } catch { /* The current session remains usable; no secret is displayed. */ } }
onMounted(() => { document.addEventListener('keydown', handleKeydown); mediaQuery?.addEventListener('change', updateViewport) })
onBeforeUnmount(() => { notifications.stop(); aiRuns.clearAll(); document.removeEventListener('keydown', handleKeydown); mediaQuery?.removeEventListener('change', updateViewport) })
watch(() => route.fullPath, () => closeDrawer())
watch(() => session.user?.id, (userId) => { if (userId) notifications.start(userId); else notifications.stop() }, { immediate: true })
watch(() => session.user?.id, (userId, previousUserId) => {
  if (previousUserId && userId !== previousUserId) aiRuns.clearAll()
}, { flush: 'pre' })
</script>

<template>
  <div class="console-shell" :class="{ 'drawer-open': drawerOpen }">
    <button class="scrim" aria-label="关闭导航" @click="closeDrawer()"></button>
    <aside class="sidebar" aria-label="主导航" :aria-hidden="isMobile && !drawerOpen ? 'true' : undefined" :inert="isMobile && !drawerOpen || undefined">
      <div class="brand"><span class="brand-mark" aria-hidden="true">H</span><span>HappyLearn</span></div>
      <p class="role-label">{{ isAdmin ? '教师空间' : '学生空间' }}</p>
      <nav id="console-navigation" ref="navigation" aria-label="主导航">
        <RouterLink v-if="isAdmin" to="/admin" @click="closeDrawer()">仪表盘</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/students" @click="closeDrawer()">学生管理</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/teaching" @click="closeDrawer()">教学管理</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/files" @click="closeDrawer()">文件中心</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/questions" @click="closeDrawer()">问题答疑</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/ai" @click="closeDrawer()">AI 管理</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/ai-usage" @click="closeDrawer()">用量统计</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/settings" @click="closeDrawer()">系统设置</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/alerts" @click="closeDrawer()">告警中心</RouterLink>
        <RouterLink v-if="isAdmin" to="/admin/backups" @click="closeDrawer()">备份记录</RouterLink>
        <RouterLink v-if="!isAdmin" to="/student" @click="closeDrawer()">学习首页</RouterLink>
        <RouterLink v-if="!isAdmin" to="/student/learning" @click="closeDrawer()">课程学习</RouterLink>
        <RouterLink v-if="!isAdmin" to="/student/questions" @click="closeDrawer()">答疑中心</RouterLink>
        <RouterLink to="/notifications" @click="closeDrawer()">通知中心</RouterLink>
      </nav>
      <p class="sidebar-note">高中数学 · 物理<br>循序渐进，稳步提升</p>
    </aside>
    <div class="content-wrap">
      <header><button ref="menuTrigger" class="menu-button" aria-label="打开导航" :aria-expanded="drawerOpen" aria-controls="console-navigation" @click="openDrawer">菜单</button><div class="header-actions"><RouterLink class="unread" to="/notifications" :aria-label="`未读消息 ${notifications.unreadCount}`">消息 <b aria-hidden="true">{{ notifications.badgeText }}</b></RouterLink><button class="quiet-button" type="button" @click="logoutOthers">结束其他会话</button><span class="display-name">{{ session.user?.displayName }}</span><button class="logout-button" type="button" :disabled="logoutPending" @click="logout">退出</button></div></header>
      <main class="page-content"><RouterView /></main>
    </div>
  </div>
</template>

<style scoped>
.console-shell{min-height:100vh;background:#f5f8fc;color:#182842}.sidebar{position:fixed;z-index:3;inset:0 auto 0 0;width:248px;display:flex;flex-direction:column;padding:22px 16px;background:#102b4d;color:#eaf4ff}.brand{display:flex;align-items:center;gap:10px;padding:0 8px;font-size:1.15rem;font-weight:800}.brand-mark{display:grid;place-items:center;width:28px;height:28px;border-radius:8px;background:#54b7f4;color:#102b4d}.role-label{margin:36px 8px 10px;color:#9fc3e2;font-size:.8rem;font-weight:700;letter-spacing:.08em}nav{display:grid;gap:5px}nav a,.future-link{box-sizing:border-box;width:100%;padding:12px 13px;border:0;border-radius:8px;background:transparent;color:inherit;font:inherit;text-align:left;text-decoration:none;cursor:pointer}nav a.router-link-active{background:#2075bb;color:#fff}.future-link{display:flex;justify-content:space-between;align-items:center}.future-link small{color:#9fc3e2}.sidebar-note{margin:auto 8px 4px;color:#9fc3e2;font-size:.85rem;line-height:1.7}.content-wrap{min-width:0;margin-left:248px}header{position:sticky;top:0;z-index:2;display:flex;align-items:center;justify-content:flex-end;gap:16px;min-height:64px;padding:0 28px;border-bottom:1px solid #dbe4f0;background:#ffffffeb;backdrop-filter:blur(10px)}.header-actions{display:flex;align-items:center;gap:16px}.unread{color:#516177;font-size:.9rem;text-decoration:none}.unread b{display:inline-grid;place-items:center;min-width:19px;height:19px;padding:0 3px;border-radius:20px;background:#e5f2fc;color:#166cbb;font-size:.75rem}.display-name{font-weight:700}.quiet-button,.logout-button,.menu-button{border:1px solid #bed0e2;border-radius:7px;background:#fff;color:#244563;padding:8px 10px;font:inherit;cursor:pointer}.logout-button{border-color:#e2b8b5;color:#a33731}.page-content{padding:clamp(24px,5vw,56px)}.menu-button,.scrim{display:none}@media(max-width:760px){.sidebar{transform:translateX(-100%);transition:transform .2s ease}.drawer-open .sidebar{transform:translateX(0)}.drawer-open .scrim{display:block;position:fixed;z-index:2;inset:0;border:0;background:#08192a88}.content-wrap{margin-left:0}.menu-button{display:block;margin-right:auto}.header-actions{gap:9px}.quiet-button{display:none}.display-name{max-width:92px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}header{padding:0 16px}.page-content{padding:28px 18px}}@media(prefers-reduced-motion:reduce){.sidebar{transition:none}}
</style>
