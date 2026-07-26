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

const props = withDefaults(defineProps<{ threadId: string; userId?: string }>(), { userId: '' })
const session = props.userId ? undefined : useSessionStore()
const aiRuns = useAIRunStore()
const detail = ref<AIThreadDetail>()
const loading = ref(false)
const actionPending = ref(false)
const error = ref('')
const requestId = ref('')
const errorBox = ref<HTMLElement>()
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
let refreshedRunId = ''

const subjectLabel = computed(() => detail.value?.thread.subject === 'physics' ? '物理' : '数学')
const currentRun = computed<AIRun | undefined>(() => {
  const server = detail.value?.activeRun
  if (!server) return undefined
  const live = aiRuns.runs[server.id]
  return live ? { ...server, ...live } : server
})
const streamingText = computed(() => {
  const run = currentRun.value
  return run?.status === 'queued' || run?.status === 'streaming'
    ? aiRuns.runs[run.id]?.text ?? ''
    : ''
})
const streamRequestId = computed(() => currentRun.value ? aiRuns.runs[currentRun.value.id]?.requestId ?? '' : '')

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
  refreshedRunId = ''
  uploaderKey.value += 1
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
    const result = await getAIThread(id, {}, controller.signal)
    if (!isCurrent(token, id)) return
    stopCurrentSubscription()
    detail.value = result
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
  aiRuns.seed(run.id, run.status, run.lastSequence, '', run.errorCode)
  if (run.status === 'queued' || run.status === 'streaming') aiRuns.start(run.id, run.lastSequence)
}

async function refreshAfterSuccess(runId: string): Promise<void> {
  if (refreshedRunId === runId) return
  refreshedRunId = runId
  const token = generation
  const id = props.threadId
  const controller = new AbortController()
  loadController = controller
  try {
    const result = await getAIThread(id, {}, controller.signal)
    if (isCurrent(token, id)) detail.value = result
  } catch {
    // The completed answer remains available on a normal manual reload.
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
  if (actionPending.value) return
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
    <RouterLink to="/student/questions">← 返回答疑中心</RouterLink>
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
        :pending="actionPending"
        @cancel="cancel"
        @retry="retry"
      />
      <AIMessageTimeline :messages="detail.messages" :streaming-text="streamingText" />
      <section class="followup" aria-labelledby="ai-followup-title">
        <h2 id="ai-followup-title">继续追问</h2>
        <form @submit.prevent="submitFollowup">
          <label>追问内容<textarea v-model="followup" aria-label="AI 追问内容" maxlength="20000" rows="6"></textarea></label>
          <small>{{ Array.from(followup).length }}/20000</small>
          <QuestionAttachmentUploader
            :key="uploaderKey"
            :user-id="props.userId || session?.user?.id || ''"
            purpose="ai"
            :disabled="actionPending"
            @update:attachments="attachments=$event"
            @pending-change="uploadsPending=$event"
          />
          <p v-if="error" ref="errorBox" role="alert" tabindex="-1">{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
          <button type="submit" :disabled="actionPending||uploadsPending">{{ actionPending ? '正在提交…' : '提交追问' }}</button>
        </form>
      </section>
    </template>
  </section>
</template>

<style scoped>
.detail{display:grid;gap:20px;max-width:920px}.detail>a{color:#176faf}.detail h1{margin:.2rem 0}.detail header p{color:#176faf;font-weight:700}.followup{padding:20px;border:1px solid #dbe4f0;border-radius:12px;background:#fff}.followup form,.followup label{display:grid;gap:9px}.followup textarea{padding:10px;border:1px solid #b9cadb;border-radius:8px;font:inherit;line-height:1.6}.followup small{justify-self:end}.followup button{justify-self:start;padding:9px 15px}[role=alert]{color:#a33731}
</style>
