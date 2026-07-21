import { createRouter, createWebHistory, type RouterHistory, type RouteRecordRaw } from 'vue-router'
import ConsoleLayout from '../layouts/ConsoleLayout.vue'
import LoginView from '../features/auth/LoginView.vue'
import ChangePasswordView from '../features/auth/ChangePasswordView.vue'
import AdminHomeView from '../features/home/AdminHomeView.vue'
import StudentListView from '../features/students/StudentListView.vue'
import StudentHomeView from '../features/home/StudentHomeView.vue'
import TeachingManagerView from '../features/teaching/TeachingManagerView.vue'
import { useSessionStore } from '../stores/session'
import type { Role } from '../api/client'

declare module 'vue-router' { interface RouteMeta { requiresAuth?: boolean; roles?: Role[]; allowDuringPasswordChange?: boolean } }

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/login' },
  { path: '/login', name: 'login', component: LoginView },
  { path: '/change-password', name: 'change-password', component: ChangePasswordView, meta: { requiresAuth: true, allowDuringPasswordChange: true } },
  { path: '/admin', component: ConsoleLayout, meta: { requiresAuth: true, roles: ['admin'] }, children: [{ path: '', name: 'admin-home', component: AdminHomeView }, { path: 'students', name: 'admin-students', component: StudentListView }, { path: 'teaching', name: 'admin-teaching', component: TeachingManagerView }] },
  { path: '/student', component: ConsoleLayout, meta: { requiresAuth: true, roles: ['student'] }, children: [{ path: '', name: 'student-home', component: StudentHomeView }] },
  { path: '/:pathMatch(.*)*', redirect: '/login' },
]

const homeFor = (role: Role) => role === 'admin' ? '/admin' : '/student'

export function createAppRouter(history: RouterHistory = createWebHistory()) {
  const router = createRouter({ history, routes })
  let skipBootstrapForLogin = false
  router.beforeEach(async (to) => {
    const session = useSessionStore()
    if (to.name === 'login' && skipBootstrapForLogin) { skipBootstrapForLogin = false; return true }
    try { await session.bootstrap() } catch {
      if (to.name === 'login') return true
      skipBootstrapForLogin = true
      return { path: '/login' }
    }
    const user = session.user
    if (!user) return to.meta.requiresAuth ? { path: '/login' } : true
    if (user.mustChangePassword && !to.meta.allowDuringPasswordChange) return { path: '/change-password' }
    if (to.name === 'login') return { path: homeFor(user.role) }
    if (to.meta.roles && !to.meta.roles.includes(user.role)) return { path: homeFor(user.role) }
    return true
  })
  return router
}

export const router = createAppRouter()
