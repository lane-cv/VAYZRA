<script setup lang="ts">
import { computed, onBeforeMount, reactive, ref } from 'vue'
import { APIError } from '../../api/client'
import {
  listAIConfigStudents,
  putGlobalLimits,
  putStudentLimits,
  readLimits,
  type LimitMode,
  type LimitView,
  type LimitWriteInput,
  type AIConfigStudent,
} from './adminApi'

type Scope = 'global' | 'student'
type LimitKey = 'dailyRequests' | 'monthlyRequests' | 'dailyTokens' | 'monthlyTokens'
type LimitFormValue = { mode: LimitMode; value: number }
type LimitForm = Record<LimitKey, LimitFormValue> & { version: number }

const definitions: Array<{ key: LimitKey; label: string }> = [
  { key: 'dailyRequests', label: '每日请求' },
  { key: 'monthlyRequests', label: '每月请求' },
  { key: 'dailyTokens', label: '每日 Token' },
  { key: 'monthlyTokens', label: '每月 Token' },
]
const loading = ref(false)
const pending = ref<Scope>()
const error = ref('')
const requestId = ref('')
const notice = ref('')
const students = ref<AIConfigStudent[]>([])
const studentLimits = ref<Record<string, LimitView>>({})
const search = ref('')
const selectedStudentId = ref('')

function emptyStudentForm(): LimitForm {
  return {
    dailyRequests: { mode: 'inherit', value: 1 },
    monthlyRequests: { mode: 'inherit', value: 1 },
    dailyTokens: { mode: 'inherit', value: 1 },
    monthlyTokens: { mode: 'inherit', value: 1 },
    version: 0,
  }
}
function emptyGlobalForm(): LimitForm {
  return {
    dailyRequests: { mode: 'disabled', value: 1 },
    monthlyRequests: { mode: 'disabled', value: 1 },
    dailyTokens: { mode: 'disabled', value: 1 },
    monthlyTokens: { mode: 'disabled', value: 1 },
    version: 1,
  }
}
const globalForm = reactive<LimitForm>(emptyGlobalForm())
const studentForm = reactive<LimitForm>(emptyStudentForm())

function formFrom(view: LimitView, student: boolean): LimitForm {
  const fallback = student ? emptyStudentForm() : emptyGlobalForm()
  for (const { key } of definitions) {
    fallback[key] = { mode: view[key].mode, value: view[key].value ?? 1 }
  }
  fallback.version = view.version
  return fallback
}
function assignForm(target: LimitForm, source: LimitForm) {
  for (const { key } of definitions) target[key] = { ...source[key] }
  target.version = source.version
}

async function loadStudents(): Promise<AIConfigStudent[]> {
  const result: AIConfigStudent[] = []
  let cursor: string | undefined
  do {
    const page = await listAIConfigStudents(cursor)
    result.push(...page.items)
    cursor = page.nextCursor
  } while (cursor)
  return result
}

async function load(preserveFailure = false) {
  loading.value = true
  if (!preserveFailure) {
    error.value = ''
    requestId.value = ''
  }
  try {
    const [limits, studentPage] = await Promise.all([readLimits(), loadStudents()])
    studentLimits.value = limits.students
    students.value = studentPage
    assignForm(globalForm, formFrom(limits.global, false))
    if (selectedStudentId.value) selectStudent(selectedStudentId.value)
  } catch (reason) {
    if (!preserveFailure) {
      error.value = reason instanceof APIError ? reason.message : '额度策略加载失败，请稍后重试'
      requestId.value = reason instanceof APIError ? reason.requestId : ''
    }
  } finally {
    loading.value = false
  }
}

const filteredStudents = computed(() => {
  const query = search.value.trim().toLocaleLowerCase('zh-CN')
  if (!query) return students.value
  return students.value.filter((student) =>
    student.username.toLocaleLowerCase('zh-CN').includes(query)
    || student.displayName.toLocaleLowerCase('zh-CN').includes(query))
})

function selectStudent(id: string) {
  selectedStudentId.value = id
  const stored = studentLimits.value[id]
  assignForm(studentForm, stored ? formFrom(stored, true) : emptyStudentForm())
}

function inputFor(form: LimitForm, isGlobal: boolean): LimitWriteInput | undefined {
  const input = { expectedVersion: form.version } as LimitWriteInput
  for (const { key } of definitions) {
    const current = form[key]
    if (isGlobal && current.mode === 'inherit') {
      error.value = '全局额度不能继承'
      return undefined
    }
    if (current.mode === 'limit' && (!Number.isSafeInteger(current.value) || current.value < 1)) {
      error.value = '额度上限必须为正整数'
      return undefined
    }
    input[key] = current.mode === 'limit'
      ? { mode: 'limit', value: current.value }
      : { mode: current.mode }
  }
  return input
}

async function save(scope: Scope) {
  if (pending.value) return
  error.value = ''
  requestId.value = ''
  notice.value = ''
  const form = scope === 'global' ? globalForm : studentForm
  const input = inputFor(form, scope === 'global')
  if (!input) return
  if (scope === 'student' && !selectedStudentId.value) {
    error.value = '请先选择学生'
    return
  }
  pending.value = scope
  try {
    const updated = scope === 'global'
      ? await putGlobalLimits(input)
      : await putStudentLimits(selectedStudentId.value, input)
    assignForm(form, formFrom(updated, scope === 'student'))
    if (scope === 'student') studentLimits.value = { ...studentLimits.value, [selectedStudentId.value]: updated }
    notice.value = `${scope === 'global' ? '全局' : '学生'}额度已保存`
  } catch (reason) {
    error.value = reason instanceof APIError ? reason.message : '额度保存失败，请稍后重试'
    requestId.value = reason instanceof APIError ? reason.requestId : ''
    if (reason instanceof APIError && (reason.status === 409 || reason.code === 'config_conflict')) {
      await load(true)
    }
  } finally {
    pending.value = undefined
  }
}

onBeforeMount(() => { void load() })
</script>

<template>
  <section class="editor" aria-labelledby="limits-heading">
    <h2 id="limits-heading">额度策略</h2>
    <p>明确选择停用、继承或设置上限；空白字段不会被解释为策略。</p>
    <p v-if="loading" role="status">正在加载额度策略…</p>
    <div v-if="error" role="alert"><p>{{ error }}<span v-if="requestId"> 支持编号：{{ requestId }}</span></p><button type="button" aria-label="重新加载额度策略" :disabled="loading || !!pending" @click="load()">重新加载</button></div>
    <p v-if="notice" role="status">{{ notice }}</p>

    <form data-scope="global" class="limit-card" @submit.prevent="save('global')">
      <h3>全局额度</h3>
      <div v-for="definition in definitions" :key="definition.key" class="limit-row">
        <label>{{ definition.label }}模式
          <select v-model="globalForm[definition.key].mode" :aria-label="`全局${definition.label}模式`" :disabled="loading || !!pending">
            <option value="disabled">停用</option>
            <option value="limit">设置上限</option>
          </select>
        </label>
        <label v-if="globalForm[definition.key].mode === 'limit'">{{ definition.label }}上限
          <input v-model.number="globalForm[definition.key].value" type="number" min="1" step="1" :aria-label="`全局${definition.label}上限`" :disabled="loading || !!pending" />
        </label>
      </div>
      <button type="submit" aria-label="保存全局额度" :disabled="loading || !!pending" @click="save('global')">{{ pending === 'global' ? '正在保存…' : '保存全局额度' }}</button>
    </form>

    <section class="student-picker" aria-labelledby="student-limits-heading">
      <h3 id="student-limits-heading">学生额度</h3>
      <label>搜索学生<input v-model="search" aria-label="搜索学生" type="search" autocomplete="off" /></label>
      <div class="student-results" aria-label="学生搜索结果">
        <button v-for="student in filteredStudents" :key="student.id" type="button" :aria-label="`选择学生 ${student.username}`" :aria-pressed="selectedStudentId === student.id" @click="selectStudent(student.id)">
          {{ student.displayName }}（{{ student.username }}）
        </button>
      </div>
    </section>

    <form v-if="selectedStudentId" data-scope="student" class="limit-card" @submit.prevent="save('student')">
      <h3>所选学生策略</h3>
      <div v-for="definition in definitions" :key="definition.key" class="limit-row">
        <label>{{ definition.label }}模式
          <select v-model="studentForm[definition.key].mode" :aria-label="`学生${definition.label}模式`" :disabled="loading || !!pending">
            <option value="inherit">继承全局</option>
            <option value="disabled">停用</option>
            <option value="limit">设置上限</option>
          </select>
        </label>
        <label v-if="studentForm[definition.key].mode === 'limit'">{{ definition.label }}上限
          <input v-model.number="studentForm[definition.key].value" type="number" min="1" step="1" :aria-label="`学生${definition.label}上限`" :disabled="loading || !!pending" />
        </label>
      </div>
      <button type="submit" aria-label="保存学生额度" :disabled="loading || !!pending" @click="save('student')">{{ pending === 'student' ? '正在保存…' : '保存学生额度' }}</button>
    </form>
  </section>
</template>

<style scoped>
.editor{display:grid;gap:16px}.editor h2,.editor h3{margin:0}.editor>p{color:var(--hl-text-muted)}.limit-card,.student-picker{display:grid;gap:14px;padding:18px;border:1px solid var(--hl-border);border-radius:12px;background:var(--hl-surface-solid)}.limit-row{display:grid;grid-template-columns:minmax(180px,1fr) minmax(180px,1fr);gap:12px}.limit-row label,.student-picker label{display:grid;gap:6px;font-weight:650}.limit-row select,.limit-row input,.student-picker input{padding:9px;border:1px solid var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit}.student-results{display:flex;flex-wrap:wrap;gap:8px}.student-results button[aria-pressed=true]{border-color:var(--hl-primary);background:var(--hl-primary-soft)}.limit-card>button{justify-self:start;border-color:var(--hl-primary);background:var(--hl-primary);color:#fff}button{border:1px solid var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text);padding:9px 12px;font:inherit;font-weight:650;cursor:pointer}@media(max-width:640px){.limit-row{grid-template-columns:1fr}}
</style>
