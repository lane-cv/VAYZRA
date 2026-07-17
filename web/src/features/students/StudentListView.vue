<script setup lang="ts">
import { computed, nextTick, onBeforeMount, onBeforeUnmount, ref } from 'vue'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import { createStudent, listStudents, resetStudentPassword, setStudentStatus, type Student, type StudentStatus } from './api'

type DialogKind = 'create' | 'status' | 'reset'

const session = useSessionStore()
const isAdmin = computed(() => session.user?.role === 'admin')
const students = ref<Student[]>([])
const loading = ref(false)
const loadError = ref('')
const loadRequestId = ref('')
const currentCursor = ref<string | undefined>()
const previousCursors = ref<Array<string | undefined>>([])
const nextCursor = ref<string | null>(null)
const dialog = ref<DialogKind | null>(null)
const selected = ref<Student | null>(null)
const createUsername = ref('')
const createDisplayName = ref('')
const createPassword = ref('')
const resetPassword = ref('')
const pending = ref(false)
const dialogError = ref('')
const dialogRequestId = ref('')
const firstDialogControl = ref<HTMLElement>()
let returnFocus: HTMLElement | undefined

function errorDetails(error: unknown, fallback: string) {
  if (error instanceof APIError) return { message: error.message || fallback, requestId: error.requestId }
  return { message: fallback, requestId: '' }
}

function clearSecrets() {
  createPassword.value = ''
  resetPassword.value = ''
}

function resetDialogState() {
  clearSecrets()
  createUsername.value = ''
  createDisplayName.value = ''
  selected.value = null
  dialogError.value = ''
  dialogRequestId.value = ''
  pending.value = false
}

function closeDialog(restoreFocus = true, force = false) {
  if (pending.value && !force) return
  dialog.value = null
  resetDialogState()
  if (restoreFocus) void nextTick(() => returnFocus?.focus())
}

function openDialog(kind: DialogKind, trigger: Event, student?: Student) {
  if (!isAdmin.value || pending.value) return
  returnFocus = trigger.currentTarget instanceof HTMLElement ? trigger.currentTarget : undefined
  selected.value = student ?? null
  dialogError.value = ''
  dialogRequestId.value = ''
  dialog.value = kind
  void nextTick(() => firstDialogControl.value?.focus())
}

async function load(cursor: string | undefined = currentCursor.value) {
  if (!isAdmin.value || loading.value) return
  loading.value = true
  loadError.value = ''
  loadRequestId.value = ''
  try {
    const page = await listStudents(cursor)
    students.value = page.data
    currentCursor.value = cursor
    nextCursor.value = page.nextCursor
  } catch (error) {
    const details = errorDetails(error, '学生列表加载失败，请稍后重试')
    loadError.value = details.message
    loadRequestId.value = details.requestId
  } finally {
    loading.value = false
  }
}

function retryLoad() { void load(currentCursor.value) }
function goNext() {
  if (!nextCursor.value || loading.value) return
  previousCursors.value.push(currentCursor.value)
  void load(nextCursor.value)
}
function goPrevious() {
  if (!previousCursors.value.length || loading.value) return
  const cursor = previousCursors.value.pop()
  void load(cursor)
}

function create() { void performCreate() }
async function performCreate() {
  if (pending.value) return
  dialogError.value = ''
  dialogRequestId.value = ''
  if (!createUsername.value.trim() || !createDisplayName.value.trim() || !createPassword.value) {
    dialogError.value = '请填写账号、姓名和临时密码'
    return
  }
  const temporaryPassword = createPassword.value
  createPassword.value = ''
  pending.value = true
  try {
    const student = await createStudent({ username: createUsername.value, displayName: createDisplayName.value, temporaryPassword })
    students.value = [student, ...students.value]
    closeDialog(true, true)
  } catch (error) {
    const details = errorDetails(error, '创建学生失败，请检查填写内容后重试')
    dialogError.value = details.message
    dialogRequestId.value = details.requestId
  } finally {
    pending.value = false
  }
}

function changeStatus() { void performChangeStatus() }
async function performChangeStatus() {
  if (!selected.value || pending.value) return
  const target = selected.value
  const status: StudentStatus = target.status === 'active' ? 'disabled' : 'active'
  pending.value = true
  dialogError.value = ''
  dialogRequestId.value = ''
  try {
    await setStudentStatus(target.id, status)
    students.value = students.value.map((student) => student.id === target.id ? { ...student, status } : student)
    closeDialog(true, true)
  } catch (error) {
    const details = errorDetails(error, '更新学生状态失败，请稍后重试')
    dialogError.value = details.message
    dialogRequestId.value = details.requestId
  } finally {
    pending.value = false
  }
}

function reset() { void performReset() }
async function performReset() {
  if (!selected.value || pending.value) return
  if (!resetPassword.value) {
    dialogError.value = '请填写临时密码'
    return
  }
  const target = selected.value
  const temporaryPassword = resetPassword.value
  resetPassword.value = ''
  pending.value = true
  dialogError.value = ''
  dialogRequestId.value = ''
  try {
    await resetStudentPassword(target.id, temporaryPassword)
    students.value = students.value.map((student) => student.id === target.id ? { ...student, mustChangePassword: true } : student)
    closeDialog(true, true)
  } catch (error) {
    const details = errorDetails(error, '重置密码失败，请稍后重试')
    dialogError.value = details.message
    dialogRequestId.value = details.requestId
  } finally {
    pending.value = false
  }
}

function handleDialogKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape') {
    event.preventDefault()
    closeDialog()
    return
  }
  if (event.key !== 'Tab') return
  const dialogRoot = event.currentTarget instanceof HTMLElement ? event.currentTarget : undefined
  const controls = dialogRoot ? Array.from(dialogRoot.querySelectorAll<HTMLElement>('button:not([disabled]), input:not([disabled])')) : []
  if (controls.length === 0) return
  const first = controls[0]
  const last = controls[controls.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function formatDate(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

onBeforeMount(() => { void load(undefined) })
onBeforeUnmount(() => clearSecrets())
</script>

<template>
  <section v-if="!isAdmin" class="student-page denied" aria-labelledby="student-management-title">
    <h1 id="student-management-title">无权访问学生管理</h1>
    <p>此功能仅对教师开放。</p>
  </section>
  <section v-else class="student-page" aria-labelledby="student-management-title">
    <div class="page-heading">
      <div><p class="eyebrow">教师工作台</p><h1 id="student-management-title">学生管理</h1><p>创建学生账号、管理启用状态，并安全重置临时密码。</p></div>
      <button class="primary-button" type="button" aria-label="创建学生" :disabled="loading" @click="openDialog('create', $event)">创建学生</button>
    </div>

    <p v-if="loading" class="state" role="status" aria-live="polite">正在加载学生…</p>
    <section v-else-if="loadError" class="state error" role="alert" aria-live="assertive">
      <p>{{ loadError }}<span v-if="loadRequestId"> 支持编号：{{ loadRequestId }}</span></p>
      <button type="button" aria-label="重试加载学生" @click="retryLoad">重试</button>
    </section>
    <section v-else-if="students.length === 0" class="state empty" aria-live="polite"><h2>还没有学生账号</h2><p>可使用“创建学生”添加第一位学生。</p></section>
    <div v-else class="student-table-wrap">
      <table>
        <thead><tr><th scope="col">账号</th><th scope="col">姓名</th><th scope="col">状态</th><th scope="col">首次密码</th><th scope="col">创建时间</th><th scope="col"><span class="sr-only">操作</span></th></tr></thead>
        <tbody>
          <tr v-for="student in students" :key="student.id">
            <td data-label="账号"><strong>{{ student.username }}</strong></td>
            <td data-label="姓名">{{ student.displayName }}</td>
            <td data-label="状态"><span class="badge" :class="student.status">{{ student.status === 'active' ? '正常' : '已停用' }}</span></td>
            <td data-label="首次密码"><span>{{ student.mustChangePassword ? '首次登录需修改密码' : '已完成首次密码修改' }}</span></td>
            <td data-label="创建时间">{{ formatDate(student.createdAt) }}</td>
            <td data-label="操作" class="actions">
              <button type="button" :aria-label="`${student.status === 'active' ? '禁用' : '启用'} ${student.username}`" @click="openDialog('status', $event, student)">{{ student.status === 'active' ? '禁用' : '启用' }}</button>
              <button type="button" :aria-label="`重置 ${student.username} 的密码`" @click="openDialog('reset', $event, student)">重置密码</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <nav v-if="!loading && !loadError" class="pagination" aria-label="学生列表翻页">
      <button type="button" aria-label="上一页学生" :disabled="loading || previousCursors.length === 0" @click="goPrevious">上一页</button>
      <button type="button" aria-label="下一页学生" :disabled="loading || !nextCursor" @click="goNext">下一页</button>
    </nav>

    <div v-if="dialog" class="dialog-backdrop" @mousedown.self="closeDialog()">
      <section class="dialog" role="dialog" aria-modal="true" :aria-labelledby="`${dialog}-dialog-title`" :aria-describedby="`${dialog}-dialog-description`" @keydown="handleDialogKeydown">
        <template v-if="dialog === 'create'">
          <h2 id="create-dialog-title">创建学生</h2><p id="create-dialog-description">临时密码仅用于首次登录；提交后不会在此页面保留或显示。</p>
          <form @submit.prevent="create" novalidate>
            <label>学生账号<input ref="firstDialogControl" v-model="createUsername" aria-label="学生账号" autocomplete="username" :disabled="pending" /></label>
            <label>学生姓名<input v-model="createDisplayName" aria-label="学生姓名" autocomplete="name" :disabled="pending" /></label>
            <label>临时密码<input v-model="createPassword" aria-label="临时密码" type="password" autocomplete="new-password" :disabled="pending" /></label>
            <p v-if="dialogError" class="dialog-error" role="alert">{{ dialogError }}<span v-if="dialogRequestId"> 支持编号：{{ dialogRequestId }}</span></p>
            <div class="dialog-actions"><button type="button" :disabled="pending" @click="closeDialog()">取消</button><button class="primary-button" type="submit" :disabled="pending">{{ pending ? '正在创建…' : '创建学生' }}</button></div>
          </form>
        </template>
        <template v-else-if="dialog === 'status' && selected">
          <h2 id="status-dialog-title">确认{{ selected.status === 'active' ? '禁用' : '启用' }}学生</h2><p id="status-dialog-description">确认{{ selected.status === 'active' ? '禁用' : '启用' }} {{ selected.username }}。{{ selected.status === 'active' ? '禁用后该学生当前会话将失效。' : '启用后该学生可再次登录。' }}</p>
          <p v-if="dialogError" class="dialog-error" role="alert">{{ dialogError }}<span v-if="dialogRequestId"> 支持编号：{{ dialogRequestId }}</span></p>
          <div class="dialog-actions"><button ref="firstDialogControl" type="button" :disabled="pending" @click="closeDialog()">取消</button><button class="danger-button" type="button" :aria-label="`确认${selected.status === 'active' ? '禁用' : '启用'} ${selected.username}`" :disabled="pending" @click="changeStatus">{{ pending ? '正在提交…' : `确认${selected.status === 'active' ? '禁用' : '启用'} ${selected.username}` }}</button></div>
        </template>
        <template v-else-if="dialog === 'reset' && selected">
          <h2 id="reset-dialog-title">重置学生密码</h2><p id="reset-dialog-description">确认重置 {{ selected.username }} 的临时密码。此操作会使其当前会话失效，并要求首次登录后修改密码。</p>
          <label>新的临时密码<input ref="firstDialogControl" v-model="resetPassword" aria-label="重置临时密码" type="password" autocomplete="new-password" :disabled="pending" /></label>
          <p v-if="dialogError" class="dialog-error" role="alert">{{ dialogError }}<span v-if="dialogRequestId"> 支持编号：{{ dialogRequestId }}</span></p>
          <div class="dialog-actions"><button type="button" :disabled="pending" @click="closeDialog()">取消</button><button class="danger-button" type="button" :aria-label="`确认重置 ${selected.username} 的密码`" :disabled="pending" @click="reset">{{ pending ? '正在重置…' : `确认重置 ${selected.username} 的密码` }}</button></div>
        </template>
      </section>
    </div>
  </section>
</template>

<style scoped>
.student-page{max-width:1180px}.page-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:24px;margin-bottom:28px}.page-heading h1{margin:.35rem 0;font-size:clamp(1.75rem,4vw,2.55rem)}.page-heading p:not(.eyebrow){margin:0;color:#5b6b80;line-height:1.65}.eyebrow{margin:0;color:#1673b9;font-size:.84rem;font-weight:700;letter-spacing:.06em}.primary-button,.danger-button,.dialog-actions button,.pagination button,.state button,.actions button{border:1px solid #bdd0e3;border-radius:8px;padding:9px 12px;background:#fff;color:#254765;font:inherit;font-weight:650;cursor:pointer}.primary-button{border-color:#166cbb;background:#166cbb;color:#fff}.danger-button{border-color:#bf4c45;background:#b93832;color:#fff}.primary-button:disabled,.danger-button:disabled,.dialog-actions button:disabled,.pagination button:disabled{opacity:.6;cursor:wait}.state{padding:34px;border:1px solid #dbe4f0;border-radius:13px;background:#fff;color:#52647a}.state h2{margin-top:0}.error{border-color:#efc1be;color:#9e2923}.student-table-wrap{overflow-x:auto;border:1px solid #dbe4f0;border-radius:13px;background:#fff}table{width:100%;border-collapse:collapse}th,td{padding:15px 16px;border-bottom:1px solid #e6edf5;text-align:left;vertical-align:middle}th{background:#f8fbfe;color:#52647a;font-size:.86rem}tbody tr:last-child td{border-bottom:0}.badge{display:inline-block;border-radius:999px;padding:4px 9px;font-size:.82rem;font-weight:700}.badge.active{background:#e4f6ed;color:#167244}.badge.disabled{background:#fbe8e7;color:#a4332d}.actions{white-space:nowrap}.actions button+button{margin-left:8px}.pagination{display:flex;justify-content:flex-end;gap:10px;margin-top:16px}.dialog-backdrop{position:fixed;z-index:10;inset:0;display:grid;place-items:center;padding:20px;background:#071b2e88}.dialog{width:min(100%,500px);max-height:calc(100vh - 40px);overflow:auto;padding:26px;border-radius:14px;background:#fff;box-shadow:0 24px 64px #071b2e66}.dialog h2{margin:0 0 10px}.dialog>p{color:#516177;line-height:1.6}.dialog label{display:block;margin-top:17px;font-weight:650}.dialog input{box-sizing:border-box;width:100%;margin-top:7px;padding:11px;border:1px solid #b9c9da;border-radius:8px;font:inherit}.dialog-error{margin:15px 0 0;color:#aa2e28}.dialog-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:24px}.denied{max-width:650px;padding:32px;border:1px solid #efc1be;border-radius:13px;background:#fff}.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0);white-space:nowrap}@media(max-width:700px){.page-heading{display:grid}.page-heading .primary-button{justify-self:start}.student-table-wrap{overflow:visible;border:0;background:transparent}table,tbody,tr,td{display:block}thead{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0 0 0 0)}tbody{display:grid;gap:12px}tr{padding:15px;border:1px solid #dbe4f0;border-radius:12px;background:#fff}td{display:grid;grid-template-columns:minmax(88px,.7fr) 1.3fr;gap:12px;padding:7px 0;border:0}td::before{content:attr(data-label);color:#68788d;font-size:.86rem}.actions{white-space:normal}.actions button+button{margin-left:6px}.pagination{justify-content:stretch}.pagination button{flex:1}.dialog-actions{display:grid;grid-template-columns:1fr 1fr}}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto}}
</style>
