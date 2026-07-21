<script setup lang="ts">
import { computed, onBeforeMount, onBeforeUnmount, ref, watch } from 'vue'
import { APIError } from '../../api/client'
import { listStudents, type Student } from '../students/api'
import type { LessonAudience } from './types'

const props = defineProps<{ modelValue: LessonAudience }>()
const emit = defineEmits<{ 'update:modelValue': [value: LessonAudience]; 'validationChange': [problems: string[]] }>()
const students = ref<Student[]>([])
const search = ref('')
const loading = ref(false)
const error = ref('')
const nextCursor = ref<string | null>(null)
const exhausted = ref(false)
let controller: AbortController | undefined

const activeStudents = computed(() => students.value.filter((student) => student.status === 'active'))
const visibleStudents = computed(() => {
  const query = search.value.trim().toLocaleLowerCase()
  return activeStudents.value.filter((student) => !query || `${student.displayName} ${student.username}`.toLocaleLowerCase().includes(query))
})
const selectedProblems = computed(() => props.modelValue.mode === 'selected'
  ? props.modelValue.userIds.flatMap((id) => {
      const student = students.value.find((item) => item.id === id)
      if (student?.status === 'disabled') return [{ id, message: `${student.displayName || student.username}已被禁用，请从受众中移除` }]
      if (!student && exhausted.value) return [{ id, message: `学生 ${id} 已不存在，请从受众中移除` }]
      return []
    })
  : [])

function setMode(mode: LessonAudience['mode']) {
  emit('update:modelValue', { mode, userIds: mode === 'all' ? [] : [...props.modelValue.userIds] })
}
function setSelected(id: string, selected: boolean) {
  const ids = new Set(props.modelValue.userIds)
  if (selected) ids.add(id); else ids.delete(id)
  emit('update:modelValue', { mode: 'selected', userIds: [...ids] })
}
function removeSelected(id: string) { setSelected(id, false) }
async function loadMore() {
  if (loading.value || exhausted.value) return
  controller?.abort()
  controller = new AbortController()
  loading.value = true
  error.value = ''
  try {
    const page = await listStudents(nextCursor.value ?? undefined, controller.signal)
    const known = new Set(students.value.map((student) => student.id))
    students.value.push(...page.data.filter((student) => !known.has(student.id)))
    nextCursor.value = page.nextCursor
    exhausted.value = !page.nextCursor
  } catch (cause) {
    if (!controller.signal.aborted) error.value = cause instanceof APIError ? cause.message : '学生列表加载失败'
  } finally { loading.value = false }
}
onBeforeMount(() => { void loadMore() })
onBeforeUnmount(() => controller?.abort())
watch(selectedProblems, (problems) => emit('validationChange', problems.map((problem) => problem.message)), { immediate: true })
</script>

<template>
  <fieldset class="audience-picker">
    <legend>课程受众</legend>
    <label><input type="radio" value="all" :checked="modelValue.mode === 'all'" @change="setMode('all')">全部启用学生</label>
    <label><input type="radio" value="selected" :checked="modelValue.mode === 'selected'" @change="setMode('selected')">指定学生</label>
    <template v-if="modelValue.mode === 'selected'">
      <p>已选择 {{ modelValue.userIds.length }} 人</p>
      <label>搜索学生<input v-model="search" aria-label="搜索学生" type="search" placeholder="姓名或账号"></label>
      <p v-if="loading" role="status">正在加载学生…</p>
      <p v-if="error" role="alert">{{ error }} <button type="button" @click="loadMore">重试</button></p>
      <div v-for="problem in selectedProblems" :key="problem.id" role="alert" class="problem"><span>{{ problem.message }}</span><button type="button" @click="removeSelected(problem.id)">移除</button></div>
      <div class="student-list">
        <label v-for="student in visibleStudents" :key="student.id"><input type="checkbox" :aria-label="`选择学生 ${student.username}`" :checked="modelValue.userIds.includes(student.id)" @change="setSelected(student.id, ($event.target as HTMLInputElement).checked)">{{ student.displayName }} <small>{{ student.username }}</small></label>
      </div>
      <button v-if="nextCursor" type="button" :disabled="loading" @click="loadMore">加载更多学生</button>
    </template>
  </fieldset>
</template>

<style scoped>
.audience-picker{display:grid;gap:10px;margin:0;padding:16px;border:1px solid #d7e2ed;border-radius:10px}.audience-picker>label,.student-list label{display:flex;align-items:center;gap:8px}.audience-picker input[type=search]{flex:1;min-width:0;padding:8px;border:1px solid #aac0d4;border-radius:7px}.student-list{display:grid;gap:8px;max-height:220px;overflow:auto}.student-list small{color:#61758a}.problem{display:flex;justify-content:space-between;align-items:center;gap:12px;margin:0;color:#982f2a}
</style>
