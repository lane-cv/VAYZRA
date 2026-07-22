<script setup lang="ts">
import { computed } from 'vue'
import type { QuestionMessage } from './types'
const props = withDefaults(defineProps<{ messages: QuestionMessage[]; viewerRole?: 'student'|'admin' }>(),{viewerRole:'student'})
const ordered = computed(() => [...props.messages].sort((a, b) => a.createdAt.localeCompare(b.createdAt) || a.id.localeCompare(b.id)))
const mine=(message:QuestionMessage)=>message.senderRole===props.viewerRole
const speaker=(message:QuestionMessage)=>props.viewerRole==='admin'?(message.senderRole==='admin'?'我（老师）':'学生'):(message.senderRole==='student'?'我':'老师')
const filePath = (id: string, action: 'preview'|'download') => `/api/v1/question-files/${encodeURIComponent(id)}/${action}`
</script>
<template>
  <section class="timeline" aria-label="问答消息">
    <article v-for="message in ordered" :key="message.id" :class="mine(message)?'mine':'other'">
      <header><strong>{{speaker(message)}}</strong><time :datetime="message.createdAt">{{ new Date(message.createdAt).toLocaleString('zh-CN') }}</time></header>
      <p class="message-body" style="white-space: pre-wrap">{{ message.body }}</p>
      <ul v-if="message.attachments.length" aria-label="消息附件">
        <li v-for="attachment in message.attachments" :key="attachment.fileVersionId">
          <span>{{ attachment.displayName }}</span>
          <a v-if="attachment.previewAvailable" :href="filePath(attachment.fileVersionId, 'preview')" target="_blank" rel="noopener" :aria-label="`预览 ${attachment.displayName}`">预览</a>
          <a :href="filePath(attachment.fileVersionId, 'download')" :aria-label="`下载 ${attachment.displayName}`">下载</a>
        </li>
      </ul>
    </article>
  </section>
</template>
<style scoped>
.timeline{display:grid;gap:14px}.timeline article{max-width:min(760px,92%);padding:16px;border:1px solid #d9e4ef;border-radius:12px;background:#fff}.timeline article.mine{justify-self:end;background:#eef8ff}.timeline header{display:flex;justify-content:space-between;gap:18px;color:#52657a;font-size:.86rem}.message-body{margin:.75rem 0;line-height:1.65;overflow-wrap:anywhere}.timeline ul{display:grid;gap:6px;margin:.6rem 0 0;padding:0;list-style:none}.timeline li{display:flex;flex-wrap:wrap;gap:10px;align-items:center}.timeline a{color:#176faf}
</style>
