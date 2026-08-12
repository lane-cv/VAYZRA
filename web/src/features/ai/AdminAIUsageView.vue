<script lang="ts">
const SHANGHAI_OFFSET_MS = 8 * 60 * 60 * 1000
const DAY_MS = 24 * 60 * 60 * 1000

export function shanghaiDateBounds(fromDate: string, toDate: string): { from?: string; to?: string } {
  function midnight(date: string): number | undefined {
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date)
    if (!match) return undefined
    const value = Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3]))
    const check = new Date(value)
    if (
      check.getUTCFullYear() !== Number(match[1])
      || check.getUTCMonth() !== Number(match[2]) - 1
      || check.getUTCDate() !== Number(match[3])
    ) return undefined
    return value - SHANGHAI_OFFSET_MS
  }
  const from = midnight(fromDate)
  const to = midnight(toDate)
  return {
    from: from === undefined ? undefined : new Date(from).toISOString().replace('.000Z', 'Z'),
    to: to === undefined
      ? undefined
      : new Date(to + DAY_MS - 1000).toISOString().replace('.000Z', '.999999Z'),
  }
}
</script>

<script setup lang="ts">
import { computed, nextTick, onBeforeMount, onBeforeUnmount, reactive, ref } from 'vue'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import {
  listAIConfigStudents,
  listModels,
  listProviders,
  listUsageRuns,
  readUsageSummary,
  type AIConfigStudent,
  type ModelView,
  type UsageFilters,
  type UsageRun,
  type UsageSummary,
} from './adminApi'
import type { AIRunStatus } from './types'
import UsageRunTable from './UsageRunTable.vue'
import UsageSummaryCards from './UsageSummaryCards.vue'

type FilterForm = {
  fromDate: string
  toDate: string
  studentId: string
  modelId: string
  status: '' | AIRunStatus
}

const PAGE_SIZE = 25
const session = useSessionStore()
const isAdmin = computed(() => session.user?.role === 'admin')
const filters = reactive<FilterForm>({ fromDate: '', toDate: '', studentId: '', modelId: '', status: '' })
const students = ref<AIConfigStudent[]>([])
const models = ref<ModelView[]>([])
const summary = ref<UsageSummary>()
const items = ref<UsageRun[]>([])
const nextCursor = ref('')
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const requestId = ref('')
const appendError = ref('')
const appendRequestId = ref('')
const resultsTitle = ref<HTMLElement>()
const filterOptionsLoaded = ref(false)
let controller: AbortController | undefined
let optionsController: AbortController | undefined
let generation = 0

function usageFilters(cursor?: string): UsageFilters {
  const bounds = shanghaiDateBounds(filters.fromDate, filters.toDate)
  return {
    ...(filters.studentId ? { studentId: filters.studentId } : {}),
    ...(filters.modelId ? { modelId: filters.modelId } : {}),
    ...(filters.status ? { status: filters.status } : {}),
    ...bounds,
    ...(cursor ? { cursor } : {}),
    limit: PAGE_SIZE,
  }
}

function failure(reason: unknown): { message: string; requestId: string } {
  return reason instanceof APIError
    ? { message: reason.message || '用量统计加载失败，请稍后重试', requestId: reason.requestId }
    : { message: '用量统计加载失败，请稍后重试', requestId: '' }
}

function mergeUsageRuns(existing: UsageRun[], incoming: UsageRun[]): UsageRun[] {
  const seen = new Set(existing.map((item) => item.id))
  const merged = [...existing]
  for (const item of incoming) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    merged.push(item)
  }
  return merged
}

async function loadAllStudents(signal: AbortSignal): Promise<AIConfigStudent[]> {
  const result: AIConfigStudent[] = []
  let cursor: string | undefined
  do {
    const page = await listAIConfigStudents(cursor, signal)
    result.push(...page.items)
    cursor = page.nextCursor
  } while (cursor)
  return result
}

async function loadFilterOptions(signal: AbortSignal) {
  const [studentRows, providers] = await Promise.all([loadAllStudents(signal), listProviders(signal)])
  const modelPages = await Promise.all(providers.map((provider) => listModels(provider.id, signal)))
  students.value = studentRows
  const unique = new Map<string, ModelView>()
  for (const model of modelPages.flat()) unique.set(model.id, model)
  models.value = [...unique.values()]
}

async function replaceUsage(restoreFocus = false) {
  controller?.abort()
  controller = new AbortController()
  const signal = controller.signal
  const current = ++generation
  loading.value = true
  loadingMore.value = false
  error.value = ''
  requestId.value = ''
  appendError.value = ''
  appendRequestId.value = ''
  summary.value = undefined
  items.value = []
  nextCursor.value = ''
  try {
    const [summaryResult, page] = await Promise.all([
      readUsageSummary(usageFilters(), signal),
      listUsageRuns(usageFilters(), signal),
    ])
    if (current !== generation || signal.aborted) return
    summary.value = summaryResult
    items.value = mergeUsageRuns([], page.items)
    nextCursor.value = page.nextCursor ?? ''
  } catch (reason) {
    if (current !== generation || signal.aborted) return
    const details = failure(reason)
    error.value = details.message
    requestId.value = details.requestId
  } finally {
    if (current === generation) loading.value = false
  }
  if (restoreFocus && current === generation) {
    await nextTick()
    resultsTitle.value?.focus()
  }
}

async function loadMore() {
  if (!nextCursor.value || loading.value || loadingMore.value) return
  const cursor = nextCursor.value
  const current = generation
  const signal = controller?.signal
  if (!signal || signal.aborted) return
  loadingMore.value = true
  appendError.value = ''
  appendRequestId.value = ''
  try {
    const page = await listUsageRuns(usageFilters(cursor), signal)
    if (current !== generation || signal.aborted) return
    items.value = mergeUsageRuns(items.value, page.items)
    nextCursor.value = page.nextCursor ?? ''
  } catch (reason) {
    if (current !== generation || signal.aborted) return
    const details = failure(reason)
    appendError.value = details.message
    appendRequestId.value = details.requestId
  } finally {
    if (current === generation) loadingMore.value = false
  }
}

async function initialize(restoreFocus = false) {
  if (!isAdmin.value) return
  filterOptionsLoaded.value = false
  optionsController?.abort()
  optionsController = new AbortController()
  const signal = optionsController.signal
  loading.value = true
  error.value = ''
  requestId.value = ''
  try {
    await loadFilterOptions(signal)
    if (signal.aborted) return
    filterOptionsLoaded.value = true
  } catch (reason) {
    if (signal.aborted) return
    filterOptionsLoaded.value = false
    const details = failure(reason)
    error.value = details.message
    requestId.value = details.requestId
    loading.value = false
    if (restoreFocus) {
      await nextTick()
      resultsTitle.value?.focus()
    }
    return
  }
  loading.value = false
  await replaceUsage(restoreFocus)
}

function retry() {
  if (filterOptionsLoaded.value) void replaceUsage(true)
  else void initialize(true)
}

function filterChanged() {
  if (!filterOptionsLoaded.value) return
  void replaceUsage()
}

onBeforeMount(() => { void initialize() })
onBeforeUnmount(() => { generation++; controller?.abort(); optionsController?.abort() })
</script>

<template>
  <section v-if="!isAdmin" class="denied" aria-labelledby="usage-title">
    <h1 id="usage-title">无权访问用量统计</h1>
    <p>此功能仅对教师开放。</p>
  </section>
  <section v-else class="page" aria-labelledby="usage-title">
    <header class="page-heading">
      <p class="eyebrow">教师工作台</p>
      <h1 id="usage-title">用量统计</h1>
      <p>按安全元数据查看 AI 请求、Token、费用与运行状态；不展示学生问题或回答正文。</p>
    </header>

    <form class="filters" aria-label="用量筛选" @submit.prevent="filterChanged">
      <label>开始日期（上海）
        <input v-model="filters.fromDate" name="fromDate" type="date" :disabled="!filterOptionsLoaded" @change="filterChanged">
      </label>
      <label>结束日期（上海）
        <input v-model="filters.toDate" name="toDate" type="date" :disabled="!filterOptionsLoaded" @change="filterChanged">
      </label>
      <label>学生
        <select v-model="filters.studentId" name="studentId" :disabled="!filterOptionsLoaded" @change="filterChanged">
          <option value="">全部学生</option>
          <option v-for="student in students" :key="student.id" :value="student.id">{{ student.displayName }}（{{ student.username }}）</option>
        </select>
      </label>
      <label>模型
        <select v-model="filters.modelId" name="modelId" :disabled="!filterOptionsLoaded" @change="filterChanged">
          <option value="">全部模型</option>
          <option v-for="model in models" :key="model.id" :value="model.id">{{ model.upstreamModelId }} · {{ model.modality === 'vision' ? '视觉' : '文本' }}</option>
        </select>
      </label>
      <label>状态
        <select v-model="filters.status" name="status" :disabled="!filterOptionsLoaded" @change="filterChanged">
          <option value="">全部状态</option>
          <option value="queued">排队中</option>
          <option value="streaming">生成中</option>
          <option value="succeeded">成功</option>
          <option value="failed">失败</option>
          <option value="cancelled">已取消</option>
        </select>
      </label>
    </form>

    <h2 id="usage-results-title" ref="resultsTitle" tabindex="-1">用量结果</h2>
    <p v-if="loading" class="state" role="status">正在加载用量统计…</p>
    <div v-else-if="error" class="error-state" role="alert">
      <p>{{ error }}</p>
      <p v-if="requestId">请求 ID：<code>{{ requestId }}</code></p>
      <button type="button" data-action="retry" @click="retry">重试</button>
    </div>
    <template v-else>
      <UsageSummaryCards v-if="summary" :summary="summary" />
      <p v-if="items.length === 0" class="state">当前筛选条件下暂无运行记录</p>
      <UsageRunTable v-else :items="items" />
      <div v-if="appendError" class="error-state append-error" role="alert">
        <p>{{ appendError }}</p>
        <p v-if="appendRequestId">请求 ID：<code>{{ appendRequestId }}</code></p>
        <button type="button" data-action="retry-more" @click="loadMore">重试加载下一页</button>
      </div>
      <div v-if="nextCursor" class="load-more">
        <button type="button" data-action="load-more" :disabled="loadingMore" @click="loadMore">
          {{ loadingMore ? '正在加载…' : '加载更多' }}
        </button>
      </div>
    </template>
  </section>
</template>

<style scoped>
.page{max-width:1280px}.page-heading{margin-bottom:22px}.page-heading h1{margin:.35rem 0;font-size:clamp(1.75rem,4vw,2.55rem)}.page-heading p:not(.eyebrow){margin:0;color:var(--hl-text-muted)}.eyebrow{margin:0;color:var(--hl-primary-strong);font-size:.84rem;font-weight:700;letter-spacing:.06em}.filters{display:grid;grid-template-columns:repeat(5,minmax(145px,1fr));gap:12px;padding:16px;border:1px solid var(--hl-border);border-radius:12px;background:var(--hl-surface-solid)}.filters label{display:grid;gap:6px;color:var(--hl-text-muted);font-size:.84rem;font-weight:700}.filters input,.filters select{box-sizing:border-box;width:100%;min-height:40px;border:1px solid var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text);padding:7px 9px;font:inherit}h2:focus{outline:3px solid var(--hl-accent);outline-offset:4px}.state,.error-state{padding:24px;border:1px solid var(--hl-border);border-radius:12px;background:var(--hl-surface-solid)}.error-state{border-color:#e4b7b3;color:#8b302c}.error-state button,.load-more button{border:1px solid var(--hl-primary);border-radius:8px;background:var(--hl-primary);color:#fff;padding:9px 14px;font:inherit;font-weight:700;cursor:pointer}.append-error{margin-top:16px}.load-more{display:flex;justify-content:center;margin-top:18px}.denied{max-width:650px;padding:32px;border:1px solid #efc1be;border-radius:13px;background:var(--hl-surface-solid)}@media(max-width:899px){.filters{grid-template-columns:1fr 1fr}}@media(max-width:560px){.filters{grid-template-columns:1fr}}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto!important;transition:none!important;animation:none!important}}
</style>
