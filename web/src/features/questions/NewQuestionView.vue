<script setup lang="ts">
import { nextTick, ref } from 'vue'
import { useRouter } from 'vue-router'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import QuestionAttachmentUploader from './QuestionAttachmentUploader.vue'
import { createQuestion, newIdempotencyKey } from './studentApi'
import type { AttachmentInput } from './types'
const props = withDefaults(defineProps<{ userId?: string }>(), { userId: '' })
const router = useRouter(), session = props.userId ? undefined : useSessionStore()
const title = ref(''), body = ref(''), attachments = ref<AttachmentInput[]>([]), uploadsPending = ref(false), submitting = ref(false), error = ref(''), requestId = ref(''), errorBox = ref<HTMLElement>()
let mutationKey = '', mutationFingerprint = ''
const chars = (value: string) => Array.from(value.trim()).length
const fingerprint = () => JSON.stringify([title.value.trim(), body.value.trim(), attachments.value.map((attachment) => attachment.fileVersionId)])
async function submit() {
  if (submitting.value) return
  error.value = ''; requestId.value = ''
  if (chars(title.value) < 1 || chars(title.value) > 160) error.value = '问题标题需为 1–160 个字符'
  else if (chars(body.value) < 1 || chars(body.value) > 20000) error.value = '问题描述需为 1–20,000 个字符'
  else if (uploadsPending.value) error.value = '请等待附件完成安全检查'
  if (error.value) { await nextTick(); errorBox.value?.focus(); return }
  const currentFingerprint = fingerprint()
  if (!mutationKey || mutationFingerprint !== currentFingerprint) { mutationKey = newIdempotencyKey(); mutationFingerprint = currentFingerprint }
  submitting.value = true
  try {
    const detail = await createQuestion({ title: title.value.trim(), body: body.value.trim(), attachments: attachments.value }, mutationKey)
    mutationKey = ''; mutationFingerprint = ''
    title.value = ''; body.value = ''; attachments.value = []
    await router.replace(`/student/questions/${encodeURIComponent(detail.thread.id)}`)
  } catch (cause) { error.value = cause instanceof Error ? cause.message : '提交失败，请稍后重试'; requestId.value = cause instanceof APIError ? cause.requestId : ''; await nextTick(); errorBox.value?.focus() }
  finally { submitting.value = false }
}
</script>
<template>
  <section class="composer" aria-labelledby="new-question-title">
    <RouterLink to="/student/questions">← 返回我的问题</RouterLink><h1 id="new-question-title">提出新问题</h1>
    <form novalidate @submit.prevent="submit">
      <label>问题标题<input v-model="title" aria-label="问题标题" maxlength="160" autocomplete="off"></label><small>{{ Array.from(title).length }}/160</small>
      <label>问题描述<textarea v-model="body" aria-label="问题描述" maxlength="20000" rows="10"></textarea></label><small>{{ Array.from(body).length }}/20000</small>
      <QuestionAttachmentUploader :user-id="props.userId || session?.user?.id || ''" :disabled="submitting" @update:attachments="attachments=$event" @pending-change="uploadsPending=$event" />
      <p v-if="error" ref="errorBox" role="alert" tabindex="-1">{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button type="submit" :disabled="submitting || uploadsPending">{{ submitting ? '正在提交…' : '提交问题' }}</button>
    </form>
  </section>
</template>
<style scoped>.composer{max-width:780px}.composer>a{color:#176faf}.composer form{display:grid;gap:10px}.composer label{display:grid;gap:7px;font-weight:700}.composer input,.composer textarea{box-sizing:border-box;width:100%;padding:11px;border:1px solid #b9cadb;border-radius:8px;font:inherit}.composer textarea{resize:vertical;line-height:1.6}.composer small{justify-self:end;color:#68768a}.composer button{justify-self:start;padding:10px 18px;border:0;border-radius:8px;background:#176faf;color:#fff;font:inherit;font-weight:700}.composer button:disabled{opacity:.6}[role=alert]{color:#a33731}</style>
