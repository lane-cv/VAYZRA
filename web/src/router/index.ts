import { createRouter, createWebHistory, type RouterHistory, type RouteRecordRaw } from 'vue-router'
import { defineComponent, h } from 'vue'
import ConsoleLayout from '../layouts/ConsoleLayout.vue'
import LoginView from '../features/auth/LoginView.vue'
import ChangePasswordView from '../features/auth/ChangePasswordView.vue'
import AdminHomeView from '../features/home/AdminHomeView.vue'
import StudentListView from '../features/students/StudentListView.vue'
import StudentHomeView from '../features/home/StudentHomeView.vue'
import TeachingManagerView from '../features/teaching/TeachingManagerView.vue'
import FileCenterView from '../features/files/FileCenterView.vue'
import LessonEditorView from '../features/teaching/LessonEditorView.vue'
import LearningView from '../features/learning/LearningView.vue'
import StudentQuestionListView from '../features/questions/StudentQuestionListView.vue'
import NewQuestionView from '../features/questions/NewQuestionView.vue'
import StudentQuestionDetailView from '../features/questions/StudentQuestionDetailView.vue'
import TeacherQuestionDetailView from '../features/questions/TeacherQuestionDetailView.vue'
import TeacherQuestionWorkspaceView from '../features/questions/TeacherQuestionWorkspaceView.vue'
import TeacherQuestionPlaceholder from '../features/questions/TeacherQuestionPlaceholder.vue'
import NotificationCenterView from '../features/notifications/NotificationCenterView.vue'
import { useSessionStore } from '../stores/session'
import type { Role } from '../api/client'

declare module 'vue-router' { interface RouteMeta { requiresAuth?: boolean; roles?: Role[]; allowDuringPasswordChange?: boolean } }

const AIQuestionPlaceholder = defineComponent({
  name: 'AIQuestionPlaceholder',
  setup: () => () => h('section', { 'aria-labelledby': 'ai-question-title' }, [
    h('h1', { id: 'ai-question-title' }, 'AI 答疑'),
    h('p', 'AI 会话详情正在准备中。'),
  ]),
})

const routes: RouteRecordRaw[] = [
  { path: '/', redirect: '/login' },
  { path: '/login', name: 'login', component: LoginView },
  { path: '/change-password', name: 'change-password', component: ChangePasswordView, meta: { requiresAuth: true, allowDuringPasswordChange: true } },
  { path: '/admin', component: ConsoleLayout, meta: { requiresAuth: true, roles: ['admin'] }, children: [{ path: '', name: 'admin-home', component: AdminHomeView }, { path: 'students', name: 'admin-students', component: StudentListView }, { path: 'teaching', name: 'admin-teaching', component: TeachingManagerView }, { path: 'teaching/lessons/:lessonId', name: 'admin-lesson-editor', component: LessonEditorView, props: true }, { path: 'files', name: 'admin-files', component: FileCenterView }, {path:'questions',component:TeacherQuestionWorkspaceView,children:[{path:'',name:'admin-questions',component:TeacherQuestionPlaceholder},{path:':questionId',name:'admin-question-detail',component:TeacherQuestionDetailView,props:true}]}] },
  { path: '/student', component: ConsoleLayout, meta: { requiresAuth: true, roles: ['student'] }, children: [
    { path: '', name: 'student-home', component: StudentHomeView },
    { path: 'learning', name: 'student-learning', component: LearningView },
    { path: 'learning/:lessonId', name: 'student-lesson', component: LearningView, props: true },
    { path: 'questions', name: 'student-questions', component: StudentQuestionListView },
    { path: 'questions/new', name: 'student-question-new', component: NewQuestionView },
    { path: 'questions/ai/:threadId', name: 'student-ai-question-detail', component: AIQuestionPlaceholder, props: true },
    {
      path: 'questions/teacher/:threadId',
      name: 'student-teacher-question-detail',
      component: StudentQuestionDetailView,
      props: (route) => ({ questionId: route.params.threadId }),
    },
    {
      path: 'questions/:questionId',
      name: 'student-question-legacy',
      redirect: (route) => canonicalUUID(String(route.params.questionId))
        ? `/student/questions/teacher/${String(route.params.questionId)}`
        : { name: 'student-questions' },
    },
  ] },
  { path: '/notifications', component: ConsoleLayout, meta: { requiresAuth: true }, children: [{ path: '', name: 'notifications', component: NotificationCenterView }] },
  { path: '/:pathMatch(.*)*', redirect: '/login' },
]

const homeFor = (role: Role) => role === 'admin' ? '/admin' : '/student'
const canonicalUUID = (value: string) => /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(value)

export function createAppRouter(history: RouterHistory = createWebHistory()) {
  const router = createRouter({ history, routes })
  let skipBootstrapForLogin = false
  router.beforeEach(async (to) => {
    if (
      (to.name === 'student-ai-question-detail' || to.name === 'student-teacher-question-detail')
      && !canonicalUUID(String(to.params.threadId))
    ) return { name: 'student-questions' }
    if (to.name === 'admin-question-detail' && !canonicalUUID(String(to.params.questionId))) return { name: 'admin-questions' }
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
