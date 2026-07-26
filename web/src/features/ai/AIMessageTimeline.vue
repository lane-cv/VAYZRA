<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import type { AIMessage } from './types'
import FinalAIAnswer from './FinalAIAnswer.vue'

const props = withDefaults(defineProps<{
  messages: AIMessage[]
  streamingText?: string
}>(), { streamingText: '' })

const announcedText = ref(props.streamingText)
let announcementTimer: ReturnType<typeof setTimeout> | undefined
let pendingAnnouncement = props.streamingText
watch(() => props.streamingText, (value) => {
  if (!value) return
  pendingAnnouncement = value
  if (announcementTimer) return
  announcementTimer = setTimeout(() => {
    announcedText.value = pendingAnnouncement
    announcementTimer = undefined
  }, 500)
})
onBeforeUnmount(() => {
  if (announcementTimer) clearTimeout(announcementTimer)
  announcementTimer = undefined
})
</script>

<template>
  <ol class="timeline" aria-label="AI 答疑对话">
    <li v-for="message in messages" :key="message.id" :class="message.role">
      <strong>{{ message.role === 'student' ? '我' : 'AI 助教' }}</strong>
      <FinalAIAnswer v-if="message.role === 'assistant'" :source="message.body" />
      <p v-else class="plain-message">{{ message.body }}</p>
      <ul v-if="message.attachments.length" aria-label="消息附件">
        <li v-for="attachment in message.attachments" :key="attachment.fileVersionId">{{ attachment.displayName }}</li>
      </ul>
    </li>
    <li v-if="streamingText" class="assistant streaming">
      <strong>AI 助教</strong>
      <pre data-testid="streaming-answer">{{ streamingText }}</pre>
    </li>
    <li class="sr-only" aria-live="polite" aria-atomic="true">{{ announcedText }}</li>
  </ol>
</template>

<style scoped>
.timeline{display:grid;gap:14px;margin:0;padding:0;list-style:none}.timeline>li{padding:16px;border:1px solid #dbe4f0;border-radius:12px;background:#fff}.timeline>li.student{margin-left:clamp(0px,8vw,72px);background:#eef7fd}.timeline>li.assistant{margin-right:clamp(0px,8vw,72px)}strong{display:block;margin-bottom:8px;color:#176faf}.plain-message,pre{margin:0;white-space:pre-wrap;overflow-wrap:anywhere;font:inherit;line-height:1.65}ul{margin-block:10px 0}.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap}
</style>
