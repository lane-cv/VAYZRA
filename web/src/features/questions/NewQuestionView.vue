<script setup lang="ts">
import { nextTick, ref } from 'vue'
import { useRouter } from 'vue-router'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import { createAIThread } from '../ai/studentApi'
import type { AIChannel, AISubject } from '../ai/types'
import QuestionAttachmentUploader from './QuestionAttachmentUploader.vue'
import { createQuestion, newIdempotencyKey } from './studentApi'
import type { AttachmentInput } from './types'

const props = withDefaults(defineProps<{ userId?: string }>(), { userId: '' })
const router = useRouter()
const session = props.userId ? undefined : useSessionStore()
const channel = ref<AIChannel | ''>('')
const subject = ref<AISubject | ''>('')
const title = ref('')
const body = ref('')
const attachments = ref<AttachmentInput[]>([])
const uploadsPending = ref(false)
const uploadHasState = ref(false)
const submitting = ref(false)
const error = ref('')
const requestId = ref('')
const errorBox = ref<HTMLElement>()
const uploaderRef = ref<InstanceType<typeof QuestionAttachmentUploader>>()
const aiRadio = ref<HTMLInputElement>()
let mutationKey = ''
let mutationFingerprint = ''

const chars = (value: string) => Array.from(value.trim()).length
const fingerprint = () => JSON.stringify([
  channel.value,
  channel.value === 'ai' ? subject.value : '',
  title.value.trim(),
  body.value.trim(),
  attachments.value.map((attachment) => attachment.fileVersionId),
])

async function selectChannel(next: AIChannel, event: Event): Promise<void> {
  if (next === channel.value) return
  if (channel.value === 'ai' && uploadHasState.value) {
    const discard = window.confirm('切换答疑方式会取消并清除尚未提交的 AI 附件，是否继续？')
    if (!discard) {
      const input = event.target as HTMLInputElement
      input.checked = false
      await nextTick()
      aiRadio.value!.checked = true
      return
    }
  }
  uploaderRef.value?.clear()
  attachments.value = []
  uploadsPending.value = false
  uploadHasState.value = false
  channel.value = next
  if (next === 'teacher') subject.value = ''
}

async function focusError(): Promise<void> {
  await nextTick()
  errorBox.value?.focus()
}

async function submit(): Promise<void> {
  if (submitting.value) return
  error.value = ''
  requestId.value = ''
  if (!channel.value) error.value = '请选择答疑方式'
  else if (channel.value === 'ai' && !subject.value) error.value = '请选择数学或物理学科'
  else if (chars(title.value) < 1 || chars(title.value) > 160) error.value = '问题标题需为 1–160 个字符'
  else if (chars(body.value) < 1 || chars(body.value) > 20000) error.value = '问题描述需为 1–20,000 个字符'
  else if (uploadsPending.value) error.value = '请等待附件完成安全检查'
  if (error.value) {
    await focusError()
    return
  }

  const currentFingerprint = fingerprint()
  if (!mutationKey || mutationFingerprint !== currentFingerprint) {
    mutationKey = newIdempotencyKey()
    mutationFingerprint = currentFingerprint
  }
  submitting.value = true
  try {
    if (channel.value === 'ai') {
      const result = await createAIThread({
        title: title.value.trim(),
        subject: subject.value as AISubject,
        body: body.value.trim(),
        attachments: attachments.value,
      }, mutationKey)
      if (!result.thread?.id) throw new Error('服务响应异常，请稍后重试')
      await router.replace(`/student/questions/ai/${encodeURIComponent(result.thread.id)}`)
    } else {
      const detail = await createQuestion({
        title: title.value.trim(),
        body: body.value.trim(),
        attachments: attachments.value,
      }, mutationKey)
      await router.replace(`/student/questions/teacher/${encodeURIComponent(detail.thread.id)}`)
    }
    mutationKey = ''
    mutationFingerprint = ''
    title.value = ''
    body.value = ''
    subject.value = ''
    attachments.value = []
    uploaderRef.value?.clear()
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '提交失败，请稍后重试'
    requestId.value = cause instanceof APIError ? cause.requestId : ''
    await focusError()
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <section class="composer" aria-labelledby="new-question-title">
    <RouterLink to="/student/questions">← 返回答疑中心</RouterLink>
    <h1 id="new-question-title">提出新问题</h1>
    <form novalidate @submit.prevent="submit">
      <fieldset class="channel-picker">
        <legend>选择答疑方式</legend>
        <label :class="{ selected: channel === 'ai' }">
          <input
            ref="aiRadio"
            type="radio"
            name="channel"
            value="ai"
            :checked="channel === 'ai'"
            @change="selectChannel('ai', $event)"
          >
          <span><strong>AI 答疑</strong><small>即时生成数学或物理解题思路</small></span>
        </label>
        <label :class="{ selected: channel === 'teacher' }">
          <input
            type="radio"
            name="channel"
            value="teacher"
            :checked="channel === 'teacher'"
            @change="selectChannel('teacher', $event)"
          >
          <span><strong>老师答疑</strong><small>提交给老师后等待回复</small></span>
        </label>
      </fieldset>
      <fieldset v-if="channel === 'ai'" class="subject-picker">
        <legend>选择学科</legend>
        <label><input v-model="subject" type="radio" name="subject" value="math">数学</label>
        <label><input v-model="subject" type="radio" name="subject" value="physics">物理</label>
      </fieldset>
      <label>问题标题<input v-model="title" aria-label="问题标题" maxlength="160" autocomplete="off"></label>
      <small>{{ Array.from(title).length }}/160</small>
      <label>问题描述<textarea v-model="body" aria-label="问题描述" maxlength="20000" rows="10"></textarea></label>
      <small>{{ Array.from(body).length }}/20000</small>
      <QuestionAttachmentUploader
        v-if="channel"
        ref="uploaderRef"
        :key="channel"
        :user-id="props.userId || session?.user?.id || ''"
        :purpose="channel"
        :disabled="submitting"
        @update:attachments="attachments = $event"
        @pending-change="uploadsPending = $event"
        @state-change="uploadHasState = $event"
      />
      <p v-if="error" ref="errorBox" role="alert" tabindex="-1">
        {{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span>
      </p>
      <button type="submit" :disabled="submitting || uploadsPending">
        {{ submitting ? '正在提交…' : '提交问题' }}
      </button>
    </form>
  </section>
</template>

<style scoped>
.composer{max-width:780px}.composer>a{color:#176faf}.composer form{display:grid;gap:10px}.composer label{display:grid;gap:7px;font-weight:700}.composer input,.composer textarea{box-sizing:border-box;width:100%;padding:11px;border:1px solid #b9cadb;border-radius:8px;font:inherit}.composer textarea{resize:vertical;line-height:1.6}.composer small{justify-self:end;color:#68768a}.channel-picker,.subject-picker{display:flex;flex-wrap:wrap;gap:10px;margin:0;padding:0;border:0}.channel-picker legend,.subject-picker legend{width:100%;margin-bottom:6px;font-weight:800}.channel-picker label{display:flex;grid-template-columns:auto 1fr;align-items:flex-start;gap:9px;min-width:220px;padding:14px;border:1px solid #b9cadb;border-radius:10px}.channel-picker label.selected{border-color:#176faf;background:#eef7fd}.channel-picker input,.subject-picker input{width:auto}.channel-picker span{display:grid;gap:4px}.channel-picker small{font-weight:400}.subject-picker label{display:flex;grid-template-columns:auto auto;align-items:center}.composer button{justify-self:start;padding:10px 18px;border:0;border-radius:8px;background:#176faf;color:#fff;font:inherit;font-weight:700}.composer button:disabled{opacity:.6}[role=alert]{color:#a33731}
</style>
