<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import { useAIRunStore } from '../../stores/aiRuns'
import QuestionAttachmentUploader from '../questions/QuestionAttachmentUploader.vue'
import type { AttachmentInput } from '../questions/types'
import AIMessageTimeline from './AIMessageTimeline.vue'
import AIRunStatusCard from './AIRunStatusCard.vue'
import {
  addAIMessage,
  cancelAIRun,
  getAIThread,
  newAIIdempotencyKey,
  retryAIRun,
} from './studentApi'
import type { AIThreadDetail, AIRun } from './types'

const props = withDefaults(defineProps<{ threadId: string; userId?: string; backTo?: string }>(), {
  userId: '',
  backTo: '/student/questions',
})
const session = props.userId ? undefined : useSessionStore()
const aiRuns = useAIRunStore()
const detail = ref<AIThreadDetail>()
const loading = ref(false)
const actionPending = ref(false)
const error = ref('')
const requestId = ref('')
const refreshError = ref('')
const refreshRequestId = ref('')
const refreshPending = ref(false)
const errorBox = ref<HTMLElement>()
const refreshErrorBox = ref<HTMLElement>()
const followup = ref('')
const attachments = ref<AttachmentInput[]>([])
const uploadsPending = ref(false)
const uploaderKey = ref(0)
let loadController: AbortController | undefined
let actionController: AbortController | undefined
let generation = 0
let retryKey = ''
let followupKey = ''
let followupFingerprint = ''
let authoritativeRunId = ''
let refreshingRunId = ''

const subjectLabel = computed(() => detail.value?.thread.subject === 'physics' ? '物理' : '数学')
const currentRun = computed<AIRun | undefined>(() => {
  const server = detail.value?.activeRun
  if (!server) return undefined
  const live = aiRuns.runs[server.id]
  return live ? { ...server, ...live } : server
})
const streamingText = computed(() => {
  const run = currentRun.value
  if (!run) return ''
  const text = aiRuns.runs[run.id]?.text ?? ''
  if (run.status === 'queued' || run.status === 'streaming') return text
  return run.status === 'succeeded' && authoritativeRunId !== run.id ? text : ''
})
const awaitingAuthoritativeAnswer = computed(() => {
  const run = currentRun.value
  return run?.status === 'succeeded' && authoritativeRunId !== run.id
})
const streamRequestId = computed(() => currentRun.value ? aiRuns.runs[currentRun.value.id]?.requestId ?? '' : '')
const streamErrorCode = computed(() => currentRun.value ? aiRuns.runs[currentRun.value.id]?.subscriptionErrorCode ?? '' : '')

function isCurrent(token: number, id: string): boolean {
  return token === generation && id === props.threadId
}

function stopCurrentSubscription(): void {
  const runId = detail.value?.activeRun?.id
  if (runId) aiRuns.stopSubscription(runId)
}

function reset(): void {
  generation += 1
  loadController?.abort()
  actionController?.abort()
  stopCurrentSubscription()
  detail.value = undefined
  loading.value = false
  actionPending.value = false
  error.value = ''
  requestId.value = ''
  followup.value = ''
  attachments.value = []
  uploadsPending.value = false
  retryKey = ''
  followupKey = ''
  followupFingerprint = ''
  authoritativeRunId = ''
  refreshingRunId = ''
  refreshError.value = ''
  refreshRequestId.value = ''
  refreshPending.value = false
  uploaderKey.value += 1
}

function mergeMessages(first: AIThreadDetail['messages'], second: AIThreadDetail['messages']): AIThreadDetail['messages'] {
  const byID = new Map(first.map((message) => [message.id, message]))
  for (const message of second) {
    if (!byID.has(message.id)) byID.set(message.id, message)
  }
  return [...byID.values()]
}

async function fetchCompleteThread(
  id: string,
  controller: AbortController,
  token: number,
): Promise<AIThreadDetail> {
  let result = await getAIThread(id, { limit: 100 }, controller.signal)
  let messages = result.messages
  const seenCursors = new Set<string>()
  while (result.nextMessageCursor) {
    if (controller.signal.aborted || !isCurrent(token, id)) throw controller.signal.reason
    const cursor = result.nextMessageCursor
    if (seenCursors.has(cursor)) {
      throw new APIError(0, 'invalid_response', '消息分页响应异常，请稍后重试', '')
    }
    seenCursors.add(cursor)
    const page = await getAIThread(id, { cursor, limit: 100 }, controller.signal)
    messages = mergeMessages(messages, page.messages)
    result = { ...result, activeRun: page.activeRun ?? result.activeRun, messages, nextMessageCursor: page.nextMessageCursor }
  }
  return { ...result, messages, nextMessageCursor: undefined }
}

async function load(): Promise<void> {
  loadController?.abort()
  const token = ++generation
  const id = props.threadId
  const controller = new AbortController()
  loadController = controller
  loading.value = true
  error.value = ''
  requestId.value = ''
  try {
    const result = await fetchCompleteThread(id, controller, token)
    if (!isCurrent(token, id)) return
    stopCurrentSubscription()
    detail.value = result
    authoritativeRunId = result.activeRun?.status === 'succeeded' ? result.activeRun.id : ''
    beginActiveRun(result.activeRun)
  } catch (cause) {
    if (controller.signal.aborted || !isCurrent(token, id)) return
    showError(cause, '加载 AI 问题失败')
    loading.value = false
    await focusError()
  } finally {
    if (isCurrent(token, id)) loading.value = false
  }
}

function beginActiveRun(run?: AIRun): void {
  if (!run) return
  const existing = aiRuns.runs[run.id]
  const active = run.status === 'queued' || run.status === 'streaming'
  const afterSequence = existing?.lastSequence ?? (active ? 0 : run.lastSequence)
  aiRuns.seed(run.id, run.status, afterSequence, existing?.text, run.errorCode)
  if (active) aiRuns.start(run.id, afterSequence)
}

function reconnect(): void {
  const runId = currentRun.value?.id
  if (runId) aiRuns.retrySubscription(runId)
}

async function refreshAfterSuccess(runId: string): Promise<void> {
  if (authoritativeRunId === runId || refreshingRunId === runId) return
  refreshingRunId = runId
  refreshPending.value = true
  refreshError.value = ''
  refreshRequestId.value = ''
  const token = generation
  const id = props.threadId
  const controller = new AbortController()
  loadController = controller
  try {
    const result = await fetchCompleteThread(id, controller, token)
    if (isCurrent(token, id) && currentRun.value?.id === runId) {
      if (!result.messages.some((message) => message.role === 'assistant' && message.runId === runId)) {
        throw new APIError(0, 'invalid_response', '完整回答尚未同步，请稍后重试', '')
      }
      detail.value = result
      authoritativeRunId = runId
    }
  } catch (cause) {
    if (!controller.signal.aborted && isCurrent(token, id) && currentRun.value?.id === runId) {
      refreshError.value = cause instanceof Error ? cause.message : '暂时无法确认完整回答'
      refreshRequestId.value = cause instanceof APIError ? cause.requestId : ''
      await nextTick()
      refreshErrorBox.value?.focus()
    }
  } finally {
    if (refreshingRunId === runId) refreshingRunId = ''
    if (isCurrent(token, id)) refreshPending.value = false
  }
}

async function cancel(): Promise<void> {
  const run = currentRun.value
  if (!run || actionPending.value || !confirm('确认停止本次生成？')) return
  actionPending.value = true
  error.value = ''
  const controller = new AbortController()
  actionController = controller
  try {
    const cancelled = await cancelAIRun(run.id, controller.signal)
    if (detail.value?.activeRun?.id !== run.id) return
    aiRuns.stopSubscription(run.id)
    aiRuns.seed(run.id, cancelled.status, cancelled.lastSequence, aiRuns.runs[run.id]?.text, cancelled.errorCode)
    detail.value = { ...detail.value, activeRun: cancelled }
  } catch (cause) {
    if (!controller.signal.aborted) {
      showError(cause, '停止生成失败')
      await focusError()
    }
  } finally {
    actionPending.value = false
  }
}

async function retry(): Promise<void> {
  const source = currentRun.value
  if (!source || actionPending.value) return
  if (!retryKey) retryKey = newAIIdempotencyKey()
  actionPending.value = true
  error.value = ''
  requestId.value = ''
  const controller = new AbortController()
  actionController = controller
  try {
    const mutation = await retryAIRun(source.id, retryKey, controller.signal)
    if (controller.signal.aborted || detail.value?.activeRun?.id !== source.id) return
    retryKey = ''
    aiRuns.stopSubscription(source.id)
    detail.value = {
      ...detail.value,
      ...(mutation.thread ? { thread: mutation.thread } : {}),
      ...(mutation.message ? { messages: [...detail.value.messages, mutation.message] } : {}),
      activeRun: mutation.run,
    }
    authoritativeRunId = ''
    refreshError.value = ''
    refreshRequestId.value = ''
    beginActiveRun(mutation.run)
  } catch (cause) {
    if (!controller.signal.aborted) {
      showError(cause, '重新生成失败')
      await focusError()
    }
  } finally {
    actionPending.value = false
  }
}

async function submitFollowup(): Promise<void> {
  if (actionPending.value || awaitingAuthoritativeAnswer.value) return
  error.value = ''
  requestId.value = ''
  const body = followup.value.trim()
  if (!body || Array.from(body).length > 20_000) {
    error.value = '追问内容需为 1–20,000 个字符'
    await focusError()
    return
  }
  if (uploadsPending.value) {
    error.value = '请等待附件完成安全检查'
    await focusError()
    return
  }
  const fingerprint = JSON.stringify([props.threadId, body, attachments.value.map((item) => item.fileVersionId)])
  if (!followupKey || followupFingerprint !== fingerprint) {
    followupKey = newAIIdempotencyKey()
    followupFingerprint = fingerprint
  }
  actionPending.value = true
  const controller = new AbortController()
  actionController = controller
  try {
    const mutation = await addAIMessage(
      props.threadId,
      { body, attachments: attachments.value },
      followupKey,
      controller.signal,
    )
    if (controller.signal.aborted || !detail.value) return
    stopCurrentSubscription()
    detail.value = {
      ...detail.value,
      ...(mutation.thread ? { thread: mutation.thread } : {}),
      ...(mutation.message ? { messages: [...detail.value.messages, mutation.message] } : {}),
      activeRun: mutation.run,
    }
    authoritativeRunId = ''
    refreshError.value = ''
    refreshRequestId.value = ''
    followup.value = ''
    attachments.value = []
    followupKey = ''
    followupFingerprint = ''
    uploaderKey.value += 1
    beginActiveRun(mutation.run)
  } catch (cause) {
    if (!controller.signal.aborted) {
      showError(cause, '提交追问失败')
      await focusError()
    }
  } finally {
    actionPending.value = false
  }
}

function showError(cause: unknown, fallback: string): void {
  error.value = cause instanceof Error ? cause.message : fallback
  requestId.value = cause instanceof APIError ? cause.requestId : ''
}
async function focusError(): Promise<void> {
  await nextTick()
  errorBox.value?.focus()
}

onMounted(() => void load())
watch(() => props.threadId, () => {
  reset()
  void load()
})
watch(() => currentRun.value?.status, (status) => {
  const runId = currentRun.value?.id
  if (status === 'succeeded' && runId) void refreshAfterSuccess(runId)
})
onBeforeUnmount(() => {
  generation += 1
  loadController?.abort()
  actionController?.abort()
  stopCurrentSubscription()
})
</script>

<template>
  <section class="detail" aria-labelledby="ai-question-title">
    <RouterLink :to="backTo">← 返回答疑中心</RouterLink>
    <p v-if="loading && !detail" role="status">正在加载 AI 问题…</p>
    <div v-else-if="error && !detail" ref="errorBox" role="alert" tabindex="-1">
      <p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button type="button" aria-label="重试加载 AI 问题" @click="load">重试</button>
    </div>
    <template v-else-if="detail">
      <header>
        <div><h1 id="ai-question-title">{{ detail.thread.title }}</h1><p>AI 答疑 · {{ subjectLabel }}</p></div>
      </header>
      <AIRunStatusCard
        v-if="currentRun"
        :run="currentRun"
        :request-id="streamRequestId"
        :subscription-error-code="streamErrorCode"
        :pending="actionPending"
        @cancel="cancel"
        @retry="retry"
        @reconnect="reconnect"
      />
      <AIMessageTimeline :messages="detail.messages" :streaming-text="streamingText" />
      <div
        v-if="refreshError"
        ref="refreshErrorBox"
        data-testid="answer-refresh-error"
        role="alert"
        tabindex="-1"
      >
        <p>{{ refreshError }}<span v-if="refreshRequestId">（支持编号：{{ refreshRequestId }}）</span></p>
        <button
          type="button"
          aria-label="重试确认完整回答"
          :disabled="refreshPending"
          @click="currentRun && refreshAfterSuccess(currentRun.id)"
        >
          {{ refreshPending ? '正在确认…' : '重试' }}
        </button>
      </div>
      <section class="followup" aria-labelledby="ai-followup-title">
        <h2 id="ai-followup-title">继续追问</h2>
        <p v-if="awaitingAuthoritativeAnswer && !refreshError" role="status">
          正在确认完整回答，确认后可继续追问…
        </p>
        <form @submit.prevent="submitFollowup">
          <label>
            追问内容
            <textarea
              v-model="followup"
              aria-label="AI 追问内容"
              maxlength="20000"
              rows="6"
              :disabled="awaitingAuthoritativeAnswer"
            ></textarea>
          </label>
          <small>{{ Array.from(followup).length }}/20000</small>
          <QuestionAttachmentUploader
            :key="uploaderKey"
            :user-id="props.userId || session?.user?.id || ''"
            purpose="ai"
            :disabled="actionPending || awaitingAuthoritativeAnswer"
            @update:attachments="attachments=$event"
            @pending-change="uploadsPending=$event"
          />
          <p v-if="error" ref="errorBox" role="alert" tabindex="-1">{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
          <button
            type="submit"
            :disabled="actionPending || uploadsPending || awaitingAuthoritativeAnswer"
          >
            {{ actionPending ? '正在提交…' : '提交追问' }}
          </button>
        </form>
      </section>
    </template>
  </section>
</template>

<style scoped>
.detail{display:grid;gap:20px;max-width:920px}.detail>a{color:#176faf}.detail h1{margin:.2rem 0}.detail header p{color:#176faf;font-weight:700}.followup{padding:20px;border:1px solid #dbe4f0;border-radius:12px;background:#fff}.followup form,.followup label{display:grid;gap:9px}.followup textarea{padding:10px;border:1px solid #b9cadb;border-radius:8px;font:inherit;line-height:1.6}.followup small{justify-self:end}.followup button{justify-self:start;padding:9px 15px}[role=alert]{color:#a33731}
</style>
