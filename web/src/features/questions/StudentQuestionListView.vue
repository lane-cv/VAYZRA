<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { APIError } from '../../api/client'
import { listQuestionSummaries, parseSummaryChannel } from './summaryApi'
import type { QuestionSummary, QuestionSummaryChannel } from './types'

const route = useRoute()
const router = useRouter()
const initialSearch = typeof route.query.search === 'string' ? route.query.search.slice(0, 160) : ''
const initialCursor = typeof route.query.cursor === 'string' && route.query.cursor ? route.query.cursor : undefined
const items = ref<QuestionSummary[]>([])
const channel = ref<QuestionSummaryChannel | ''>(parseSummaryChannel(route.query.channel))
const search = ref(initialSearch)
const activeCursor = ref(initialCursor)
const nextCursor = ref<string>()
const loading = ref(true)
const error = ref('')
const requestId = ref('')
type LoadMode = 'replace' | 'append'
const errorMode = ref<LoadMode>()
const retryCursor = ref<string>()
let controller: AbortController | undefined
let generation = 0
let searchTimer: ReturnType<typeof setTimeout> | undefined

const statusLabels: Record<QuestionSummaryChannel, Record<string, string>> = {
  ai: {
    queued: '等待生成',
    streaming: '生成中',
    succeeded: '已完成',
    failed: '生成失败',
    cancelled: '已停止',
  },
  teacher: {
    pending: '待处理',
    in_progress: '处理中',
    waiting_student: '等待我回复',
    completed: '已完成',
  },
}

function statusLabel(item: QuestionSummary): string {
  return statusLabels[item.channel][item.rawStatus] ?? '状态更新中'
}

function detailPath(item: QuestionSummary): string {
  return `/student/questions/${item.channel}/${encodeURIComponent(item.id)}`
}

function canonicalQuery(): Record<string, string> {
  const query: Record<string, string> = {}
  if (channel.value) query.channel = channel.value
  const normalizedSearch = search.value.trim()
  if (normalizedSearch) query.search = normalizedSearch
  if (activeCursor.value) query.cursor = activeCursor.value
  return query
}

async function updateQuery(): Promise<void> {
  await router.replace({ query: canonicalQuery() })
}

function invalidateActiveRequest(): void {
  generation += 1
  controller?.abort()
  controller = undefined
}

function resetRequestFeedback(): void {
  loading.value = true
  error.value = ''
  requestId.value = ''
  errorMode.value = undefined
  retryCursor.value = undefined
}

function beginReplacement(): void {
  invalidateActiveRequest()
  items.value = []
  nextCursor.value = undefined
  resetRequestFeedback()
}

async function load(cursor?: string, mode: LoadMode = 'replace'): Promise<void> {
  if (mode === 'replace') beginReplacement()
  else {
    invalidateActiveRequest()
    resetRequestFeedback()
  }
  const current = generation
  const requestController = new AbortController()
  controller = requestController
  try {
    const page = await listQuestionSummaries({
      channel: channel.value || undefined,
      search: search.value.trim() || undefined,
      ...(cursor ? { cursor } : {}),
      limit: 20,
    }, requestController.signal)
    if (current !== generation) return
    if (mode === 'append') {
      const existing = new Set(items.value.map((item) => `${item.channel}:${item.id}`))
      items.value = [...items.value, ...page.items.filter((item) => !existing.has(`${item.channel}:${item.id}`))]
    } else {
      items.value = page.items
    }
    nextCursor.value = page.nextCursor
    if (cursor) {
      activeCursor.value = cursor
      await updateQuery()
    }
  } catch (cause) {
    if (requestController.signal.aborted || current !== generation) return
    error.value = cause instanceof Error ? cause.message : '加载失败'
    errorMode.value = mode
    retryCursor.value = cursor
    requestId.value = cause instanceof APIError
      ? cause.requestId
      : typeof cause === 'object' && cause && 'requestId' in cause
        ? String(cause.requestId)
        : ''
  } finally {
    if (current === generation) loading.value = false
  }
}

function retry(): void {
  void load(retryCursor.value, errorMode.value === 'append' ? 'append' : 'replace')
}

async function changeChannel(): Promise<void> {
  activeCursor.value = undefined
  beginReplacement()
  await updateQuery()
  await load()
}

function changeSearch(): void {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    void (async () => {
      activeCursor.value = undefined
      beginReplacement()
      await updateQuery()
      await load()
    })()
  }, 300)
}

onMounted(() => void load(activeCursor.value))
onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
  controller?.abort()
})
</script>

<template>
  <section class="questions" aria-labelledby="questions-title">
    <header>
      <div>
        <p class="eyebrow">答疑中心</p>
        <h1 id="questions-title">我的问题</h1>
      </div>
      <RouterLink class="primary" to="/student/questions/new">提出新问题</RouterLink>
    </header>
    <div class="filters">
      <label>
        答疑类型
        <select v-model="channel" aria-label="答疑类型" @change="changeChannel">
          <option value="">全部</option>
          <option value="ai">AI</option>
          <option value="teacher">老师</option>
        </select>
      </label>
      <label>
        搜索标题
        <input
          v-model="search"
          aria-label="搜索问题标题"
          maxlength="160"
          autocomplete="off"
          type="search"
          @input="changeSearch"
        >
      </label>
    </div>
    <p v-if="loading && !items.length" role="status">正在加载问答…</p>
    <div v-else-if="error && !items.length" role="alert">
      <p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button type="button" aria-label="重试加载问答" @click="retry">重试</button>
    </div>
    <p v-else-if="!items.length" class="empty">还没有符合条件的问题。</p>
    <ul v-else>
      <li v-for="item in items" :key="`${item.channel}:${item.id}`">
        <RouterLink :to="detailPath(item)">
          <strong>{{ item.title }}</strong>
          <span class="channel-badge">{{ item.channel === 'ai' ? 'AI' : '老师' }}</span>
          <span>{{ statusLabel(item) }}</span>
          <time :datetime="item.lastMessageAt">最近更新 {{ new Date(item.lastMessageAt).toLocaleString('zh-CN') }}</time>
        </RouterLink>
      </li>
    </ul>
    <div v-if="error && errorMode === 'append' && items.length" role="alert">
      <p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button type="button" aria-label="重试加载更多问答" @click="retry">重试</button>
    </div>
    <button
      v-if="nextCursor && !(error && errorMode === 'append')"
      type="button"
      aria-label="加载更多问答"
      :disabled="loading"
      @click="load(nextCursor, 'append')"
    >
      {{ loading ? '正在加载…' : '加载更多' }}
    </button>
  </section>
</template>

<style scoped>
.questions{max-width:900px}.questions>header{display:flex;justify-content:space-between;gap:18px;align-items:center}.eyebrow{color:#1673b9;font-weight:700}.questions h1{margin:.35rem 0 1rem}.primary{padding:10px 15px;border-radius:8px;background:#176faf;color:#fff;text-decoration:none}.filters{display:flex;flex-wrap:wrap;gap:16px}.filters label{display:grid;gap:6px}.filters select,.filters input{min-height:40px;padding:8px;border:1px solid #b9cadb;border-radius:8px;font:inherit}.questions ul{display:grid;gap:10px;padding:0;list-style:none}.questions li a{display:grid;grid-template-columns:1fr auto auto;gap:8px;align-items:center;padding:16px;border:1px solid #dbe4f0;border-radius:11px;background:#fff;color:#182842;text-decoration:none}.channel-badge{padding:2px 8px;border-radius:999px;background:#e5f2fc;color:#176faf;font-size:.78rem;font-weight:700}.questions time{grid-column:1/-1;color:#617086;font-size:.85rem}.empty{padding:24px;border:1px dashed #bdccdb;border-radius:10px;text-align:center;color:#617086}[role=alert]{color:#a33731}@media(max-width:560px){.questions>header{align-items:flex-start;flex-direction:column}.questions li a{grid-template-columns:1fr auto}.questions li a>span:last-of-type{grid-column:1/-1}}
</style>
