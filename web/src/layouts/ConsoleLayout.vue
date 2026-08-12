<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { request } from '../api/client'
import { useSessionStore } from '../stores/session'
import { useNotificationStore } from '../stores/notifications'
import { useAIRunStore } from '../stores/aiRuns'
import ApplicationUpdateBadge from '../features/operations/ApplicationUpdateBadge.vue'

const session = useSessionStore()
const notifications = useNotificationStore()
const aiRuns = useAIRunStore()
const router = useRouter()
const route = useRoute()
const drawerOpen = ref(false)
const logoutPending = ref(false)
const menuTrigger = ref<HTMLButtonElement>()
const sidebar = ref<HTMLElement>()
const navigation = ref<HTMLElement>()
const mediaQuery = typeof window === 'undefined' ? undefined : window.matchMedia('(max-width: 1023px)')
const isMobile = ref(mediaQuery?.matches ?? false)
let previousBodyOverflow: string | undefined
const isAdmin = computed(() => session.user?.role === 'admin')
type Theme = 'light' | 'dark'
type NavigationItem = { to: string; label: string; icon: string }
function readPreference(key: string): string | null {
  try { return window.localStorage.getItem(key) } catch { return null }
}
function writePreference(key: string, value: string) {
  try { window.localStorage.setItem(key, value) } catch { /* Preference persistence is optional. */ }
}
const storedTheme = readPreference('happylearn-theme')
const theme = ref<Theme>(storedTheme === 'dark' || storedTheme === 'light'
  ? storedTheme
  : document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light')
const sidebarCollapsed = ref(readPreference('happylearn-sidebar-collapsed') === 'true')
const adminNavigation: NavigationItem[] = [
  { to: '/admin', label: '仪表盘', icon: '▦' },
  { to: '/admin/students', label: '学生管理', icon: '人' },
  { to: '/admin/teaching', label: '教学管理', icon: '△' },
  { to: '/admin/files', label: '文件中心', icon: '□' },
  { to: '/admin/questions', label: '问题答疑', icon: '?' },
  { to: '/admin/ai', label: 'AI 管理', icon: '✦' },
  { to: '/admin/ai-usage', label: '用量统计', icon: '▥' },
  { to: '/admin/settings', label: '系统设置', icon: '⚙' },
  { to: '/admin/alerts', label: '告警中心', icon: '!' },
  { to: '/admin/backups', label: '备份记录', icon: '↻' }
]
const studentNavigation: NavigationItem[] = [
  { to: '/student', label: '学习首页', icon: '⌂' },
  { to: '/student/learning', label: '课程学习', icon: '▤' },
  { to: '/student/questions', label: '答疑中心', icon: '?' }
]
const notificationNavigation: NavigationItem = { to: '/notifications', label: '通知中心', icon: '◉' }
const navigationItems = computed(() => [...(isAdmin.value ? adminNavigation : studentNavigation), notificationNavigation])
const currentPath = computed(() => route.fullPath.split('?')[0].split('#')[0])
const pageTitle = computed(() => navigationItems.value.find((item) => isItemActive(item.to))?.label ?? (isAdmin.value ? '教师空间' : '学生空间'))
const userInitial = computed(() => session.user?.displayName?.trim().slice(0, 1) || 'H')
function closeDrawer(restoreFocus = false) { drawerOpen.value = false; if (restoreFocus) void nextTick(() => menuTrigger.value?.focus()) }
function openDrawer() { drawerOpen.value = true; void nextTick(() => navigation.value?.querySelector<HTMLElement>('a, button')?.focus()) }
function syncScrollLock() {
  if (isMobile.value && drawerOpen.value) {
    if (previousBodyOverflow === undefined) previousBodyOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    return
  }
  if (previousBodyOverflow === undefined) return
  document.body.style.overflow = previousBodyOverflow
  previousBodyOverflow = undefined
}
function handleKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && drawerOpen.value) {
    closeDrawer(true)
    return
  }
  if (event.key !== 'Tab' || !isMobile.value || !drawerOpen.value) return
  const focusable = Array.from(sidebar.value?.querySelectorAll<HTMLElement>(
    'a[href], button:not([disabled]):not([tabindex="-1"]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
  ) ?? []).filter((element) => element.getAttribute('aria-hidden') !== 'true')
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (!first || !last) return
  const active = document.activeElement
  if (event.shiftKey && (active === first || !sidebar.value?.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (active === last || !sidebar.value?.contains(active))) {
    event.preventDefault()
    first.focus()
  }
}
function updateViewport(event: MediaQueryListEvent) { isMobile.value = event.matches; if (!event.matches) drawerOpen.value = false }
function isItemActive(path: string) { return currentPath.value === path || (path !== '/admin' && path !== '/student' && currentPath.value.startsWith(`${path}/`)) }
function applyTheme(nextTheme: Theme) { document.documentElement.dataset.theme = nextTheme; document.documentElement.style.colorScheme = nextTheme }
function toggleTheme() { theme.value = theme.value === 'dark' ? 'light' : 'dark'; writePreference('happylearn-theme', theme.value); applyTheme(theme.value) }
function toggleSidebar() { sidebarCollapsed.value = !sidebarCollapsed.value; writePreference('happylearn-sidebar-collapsed', String(sidebarCollapsed.value)) }
async function logout() { if (logoutPending.value) return; logoutPending.value = true; notifications.stop(); try { await request('/auth/logout', { method: 'POST' }) } catch { /* The server session may already be expired. */ } finally { aiRuns.clearAll(); session.clear(); closeDrawer(); logoutPending.value = false; await router.replace('/login') } }
async function logoutOthers() { try { await request('/auth/logout-others', { method: 'POST' }) } catch { /* The current session remains usable; no secret is displayed. */ } }
applyTheme(theme.value)
onMounted(() => { document.addEventListener('keydown', handleKeydown); mediaQuery?.addEventListener('change', updateViewport) })
onBeforeUnmount(() => { notifications.stop(); aiRuns.clearAll(); drawerOpen.value = false; syncScrollLock(); document.removeEventListener('keydown', handleKeydown); mediaQuery?.removeEventListener('change', updateViewport) })
watch(() => route.fullPath, () => closeDrawer())
watch([isMobile, drawerOpen], syncScrollLock, { flush: 'sync' })
watch(() => session.user?.id, (userId) => { if (userId) notifications.start(userId); else notifications.stop() }, { immediate: true })
watch(() => session.user?.id, (userId, previousUserId) => {
  if (previousUserId && userId !== previousUserId) aiRuns.clearAll()
}, { flush: 'pre' })
</script>

<template>
  <div class="console-shell" :class="{ 'drawer-open': drawerOpen, 'sidebar-collapsed': sidebarCollapsed }">
    <button class="scrim" type="button" aria-label="关闭导航" @click="closeDrawer()"></button>
    <aside ref="sidebar" class="sidebar" aria-label="主导航" :aria-hidden="isMobile && !drawerOpen ? 'true' : undefined" :inert="isMobile && !drawerOpen || undefined">
      <div class="brand">
        <span class="brand-mark" aria-hidden="true">H</span>
        <span class="brand-copy"><strong>HappyLearn</strong><small>{{ isAdmin ? '教师控制台' : '学习控制台' }}</small></span>
      </div>
      <div class="sidebar-version-slot" aria-live="polite"><ApplicationUpdateBadge v-if="isAdmin" /></div>
      <p class="role-label">{{ isAdmin ? '教学管理' : '我的学习' }}</p>
      <nav id="console-navigation" ref="navigation" aria-label="主导航">
        <RouterLink v-for="item in navigationItems" :key="item.to" :to="item.to" :aria-label="sidebarCollapsed ? item.label : undefined" :title="sidebarCollapsed ? item.label : undefined" :class="{ 'is-active': isItemActive(item.to) }" @click="closeDrawer()">
          <span class="nav-icon" aria-hidden="true">{{ item.icon }}</span>
          <span class="nav-label">{{ item.label }}</span>
          <span v-if="item.to === '/notifications' && notifications.unreadCount" class="nav-badge" aria-hidden="true">{{ notifications.badgeText }}</span>
        </RouterLink>
      </nav>
      <div class="sidebar-footer">
        <button type="button" class="sidebar-action" :aria-label="theme === 'dark' ? '切换至浅色模式' : '切换至深色模式'" :title="sidebarCollapsed ? (theme === 'dark' ? '浅色模式' : '深色模式') : undefined" @click="toggleTheme">
          <span class="nav-icon" aria-hidden="true">{{ theme === 'dark' ? '☀' : '☾' }}</span>
          <span class="nav-label">{{ theme === 'dark' ? '浅色模式' : '深色模式' }}</span>
        </button>
        <button type="button" class="sidebar-action collapse-button" :aria-hidden="isMobile ? 'true' : undefined" :tabindex="isMobile ? -1 : undefined" :aria-label="sidebarCollapsed ? '展开侧栏' : '收起侧栏'" :title="sidebarCollapsed ? '展开侧栏' : undefined" @click="toggleSidebar">
          <span class="nav-icon collapse-icon" aria-hidden="true">‹</span><span class="nav-label">收起侧栏</span>
        </button>
      </div>
    </aside>
    <div class="content-wrap" :aria-hidden="isMobile && drawerOpen ? 'true' : undefined" :inert="isMobile && drawerOpen || undefined">
      <header class="topbar">
        <div class="header-leading"><button ref="menuTrigger" class="icon-button menu-button" type="button" aria-label="打开导航" :aria-expanded="drawerOpen" aria-controls="console-navigation" @click="openDrawer"><span class="button-icon" aria-hidden="true">☰</span></button><div class="page-heading"><strong>{{ pageTitle }}</strong><small>{{ isAdmin ? 'HappyLearn 教学管理平台' : '保持节奏，稳步进步' }}</small></div></div>
        <div class="header-actions">
          <RouterLink class="unread icon-button" to="/notifications" :aria-label="`未读消息 ${notifications.unreadCount}`"><span class="button-icon notification-icon" aria-hidden="true">◉</span><b aria-hidden="true">{{ notifications.badgeText }}</b></RouterLink>
          <button class="quiet-button" type="button" @click="logoutOthers">结束其他会话</button>
          <span class="profile-avatar" aria-hidden="true">{{ userInitial }}</span><span class="display-name">{{ session.user?.displayName }}</span>
          <button class="logout-button" type="button" :disabled="logoutPending" @click="logout">{{ logoutPending ? '退出中…' : '退出' }}</button>
        </div>
      </header>
      <main class="page-content"><RouterView /></main>
    </div>
  </div>
</template>

<style scoped>
.console-shell{min-height:100vh;background:var(--hl-bg);color:var(--hl-text)}
.sidebar{position:fixed;z-index:40;inset:0 auto 0 0;width:256px;display:flex;flex-direction:column;border-right:1px solid var(--hl-border);background:var(--hl-surface-solid);box-shadow:4px 0 24px rgba(15,23,42,.025);transition:width .24s ease,transform .24s ease}
.brand{display:flex;align-items:center;gap:12px;height:72px;padding:0 18px;overflow:hidden;border-bottom:1px solid var(--hl-border)}
.brand-mark{display:grid;flex:0 0 38px;place-items:center;width:38px;height:38px;border-radius:12px;background:linear-gradient(135deg,#0f766e,#14b8a6);color:#fff;box-shadow:0 8px 18px rgba(13,148,136,.24)}
.brand-mark{font-size:1rem;font-weight:850;letter-spacing:-.04em}
.brand-copy{display:grid;min-width:0;white-space:nowrap}.brand-copy strong{font-size:1.04rem;letter-spacing:-.02em}.brand-copy small{margin-top:2px;color:var(--hl-text-muted);font-size:.72rem;font-weight:550}
.sidebar-version-slot{min-height:0;padding:12px 18px 0;overflow:visible}.role-label{margin:22px 20px 9px;color:var(--hl-text-soft);font-size:.7rem;font-weight:750;letter-spacing:.12em;white-space:nowrap}
nav{display:grid;flex:1;align-content:start;min-height:0;gap:3px;padding:0 12px;overflow:auto}nav a,.sidebar-action{display:flex;align-items:center;gap:13px;width:100%;min-height:44px;padding:10px 12px;border:0;border-radius:10px;background:transparent;color:var(--hl-text-muted);font:inherit;font-size:.9rem;font-weight:600;text-align:left;text-decoration:none;white-space:nowrap;cursor:pointer;transition:background .16s ease,color .16s ease,transform .16s ease}
nav a:hover,.sidebar-action:hover{background:var(--hl-surface-muted);color:var(--hl-text)}nav a.is-active{background:var(--hl-primary-soft);color:var(--hl-primary-strong)}
.nav-icon{display:grid;flex:0 0 20px;place-items:center;width:20px;height:20px;font-family:ui-sans-serif,system-ui,sans-serif;font-size:1.05rem;font-weight:700;line-height:1}.nav-label{min-width:0;overflow:hidden;text-overflow:ellipsis}.nav-badge{display:grid;place-items:center;min-width:20px;height:20px;margin-left:auto;padding:0 5px;border-radius:999px;background:var(--hl-primary);color:#fff;font-size:.68rem}
.sidebar-footer{display:grid;gap:3px;margin-top:auto;padding:12px;border-top:1px solid var(--hl-border)}.collapse-icon{transition:transform .24s ease}
.content-wrap{min-width:0;min-height:100vh;margin-left:256px;transition:margin-left .24s ease}.topbar{position:sticky;top:0;z-index:30;display:flex;align-items:center;justify-content:space-between;gap:18px;height:64px;padding:0 28px;border-bottom:1px solid color-mix(in srgb,var(--hl-border) 82%,transparent);background:var(--hl-surface);backdrop-filter:blur(16px);-webkit-backdrop-filter:blur(16px)}
.header-leading,.header-actions{display:flex;align-items:center;min-width:0}.header-leading{gap:12px}.header-actions{gap:10px}.page-heading{display:grid;min-width:0}.page-heading strong{font-size:.96rem}.page-heading small{margin-top:2px;color:var(--hl-text-muted);font-size:.7rem}.icon-button{position:relative;display:grid;place-items:center;width:38px;height:38px;padding:0;border:1px solid var(--hl-border);border-radius:10px;background:var(--hl-surface-solid);color:var(--hl-text-muted);text-decoration:none;cursor:pointer}.button-icon{font-size:1.15rem;font-weight:700;line-height:1}.notification-icon{color:var(--hl-primary);font-size:.9rem}.menu-button{display:none}.unread b{position:absolute;top:-5px;right:-5px;display:grid;place-items:center;min-width:18px;height:18px;padding:0 4px;border:2px solid var(--hl-surface-solid);border-radius:20px;background:var(--hl-primary);color:#fff;font-size:.62rem}.profile-avatar{display:grid;place-items:center;width:32px;height:32px;margin-left:2px;border-radius:10px;background:var(--hl-primary-soft);color:var(--hl-primary-strong);font-size:.82rem;font-weight:800}.display-name{max-width:130px;overflow:hidden;color:var(--hl-text);font-size:.86rem;font-weight:700;text-overflow:ellipsis;white-space:nowrap}.quiet-button,.logout-button{min-height:36px;padding:7px 11px;border:1px solid var(--hl-border);border-radius:9px;background:var(--hl-surface-solid);color:var(--hl-text-muted);font:inherit;font-size:.78rem;font-weight:650;cursor:pointer}.quiet-button:hover{border-color:var(--hl-border-strong);color:var(--hl-text)}.logout-button{border-color:color-mix(in srgb,var(--hl-danger) 24%,var(--hl-border));background:var(--hl-danger-soft);color:var(--hl-danger)}.logout-button:disabled{opacity:.6;cursor:wait}.page-content{position:relative;padding:clamp(24px,4vw,48px)}.scrim{display:none}
.notification-icon{color:var(--hl-primary-strong)}.unread b{color:var(--hl-on-primary,#fff)}
.sidebar-collapsed .sidebar{width:72px}.sidebar-collapsed .content-wrap{margin-left:72px}.sidebar-collapsed .brand{padding:0 17px}.sidebar-collapsed .brand-copy,.sidebar-collapsed .role-label,.sidebar-collapsed .nav-label,.sidebar-collapsed .nav-badge,.sidebar-collapsed .sidebar-version-slot{display:none}.sidebar-collapsed nav{padding:12px}.sidebar-collapsed nav a,.sidebar-collapsed .sidebar-action{justify-content:center;padding:10px}.sidebar-collapsed .collapse-icon{transform:rotate(180deg)}
@media(max-width:1023px){.sidebar,.sidebar-collapsed .sidebar{width:256px;transform:translateX(-100%);box-shadow:20px 0 50px rgba(2,6,23,.22)}.drawer-open .sidebar{transform:translateX(0)}.drawer-open .scrim{display:block;position:fixed;z-index:35;inset:0;border:0;background:rgba(2,6,23,.56);backdrop-filter:blur(2px)}.sidebar-collapsed .brand{padding:0 18px}.sidebar-collapsed .brand-copy{display:grid}.sidebar-collapsed .role-label,.sidebar-collapsed .sidebar-version-slot{display:block}.sidebar-collapsed .nav-label{display:inline}.sidebar-collapsed .nav-badge{display:grid}.sidebar-collapsed nav{padding:0 12px}.sidebar-collapsed nav a,.sidebar-collapsed .sidebar-action{justify-content:flex-start;padding:10px 12px}.collapse-button{display:none}.content-wrap,.sidebar-collapsed .content-wrap{margin-left:0}.menu-button{display:grid}.topbar{padding:0 18px}.page-content{padding:28px 20px}}
@media(max-width:720px){.page-heading small,.quiet-button{display:none}.header-actions{gap:7px}.display-name{max-width:78px}.profile-avatar{display:none}.logout-button{padding:7px 9px}.topbar{padding:0 14px}.page-content{padding:24px 16px}}
@media(max-width:440px){.display-name{display:none}.page-heading strong{max-width:92px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}}
@media(prefers-reduced-motion:reduce){.sidebar,.content-wrap,.collapse-icon{transition:none}}
</style>
