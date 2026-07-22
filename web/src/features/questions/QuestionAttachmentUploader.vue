<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useSessionStore } from '../../stores/session'
import { createUploadManager, type UploadManagerState } from '../teaching/uploadManager'
import { questionFileStatus } from './studentApi'
import { createStudentQuestionSessionStore, studentQuestionUploadTransport } from './questionUpload'
import type { AttachmentInput, QAFileStatus } from './types'

const props = withDefaults(defineProps<{ userId?: string; disabled?: boolean }>(), { userId: '', disabled: false })
const emit = defineEmits<{ 'update:attachments': [value: AttachmentInput[]]; 'pending-change': [value: boolean] }>()
type Item = { name: string; size: number; state: 'pending' | 'ready' | 'rejected'; message: string; status?: QAFileStatus }
const items = ref<Item[]>([])
const error = ref('')
const activeControllers = new Set<AbortController>()
const session = props.userId ? undefined : useSessionStore()
const pending = computed(() => items.value.some((item) => item.state === 'pending'))
const limits: Record<string, number> = { 'image/jpeg': 20<<20, 'image/png': 20<<20, 'image/webp': 20<<20, 'image/gif': 20<<20, 'application/pdf': 50<<20, 'application/vnd.openxmlformats-officedocument.wordprocessingml.document': 30<<20, 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': 30<<20, 'application/vnd.openxmlformats-officedocument.presentationml.presentation': 30<<20, 'text/plain': 10<<20, 'text/markdown': 10<<20 }

function publish() {
  emit('pending-change', pending.value)
  emit('update:attachments', items.value.filter((item) => item.state === 'ready' && item.status).map((item, index) => ({ fileVersionId: item.status!.fileVersionId, sortPosition: index })))
}
function clientError(files: File[]): string {
  if (items.value.length + files.length > 20) return '每条消息最多添加 20 个附件'
  if (items.value.reduce((sum, item) => sum + item.size, 0) + files.reduce((sum, file) => sum + file.size, 0) > 100<<20) return '附件总大小不能超过 100 MB'
  for (const file of files) {
    const limit = limits[file.type.toLowerCase()]
    if (!limit) return `${file.name} 的文件类型不支持`
    if (file.size < 1 || file.size > limit) return `${file.name} 超过该类型允许的大小`
  }
  return ''
}
async function choose(event: Event) {
  error.value = ''
  const input = event.target as HTMLInputElement
  const files = Array.from(input.files ?? [])
  const invalid = clientError(files)
  if (invalid) { error.value = invalid; input.value = ''; return }
  for (const file of files) {
    const item: Item = { name: file.name, size: file.size, state: 'pending', message: '等待上传' }
    items.value.push(item); publish()
    const manager = createUploadManager({ transport: studentQuestionUploadTransport, sessions: createStudentQuestionSessionStore(props.userId || session?.user?.id || 'unknown'), onState: (state: UploadManagerState) => {
      if (state.kind === 'hashing') item.message = `正在校验 ${state.progress}%`
      else if (state.kind === 'uploading') item.message = `正在上传 ${state.progress}%`
      else if (state.kind === 'failed') { item.state = 'rejected'; item.message = `上传失败：${state.message}`; publish() }
    } })
    try {
      const completed = await manager.start(file)
      if (!completed) { item.state = 'rejected'; item.message = '上传已取消'; publish(); continue }
      item.message = '正在进行安全检查'; await waitUntilProcessed(completed.fileVersionId, item)
    } catch (cause) { item.state = 'rejected'; item.message = cause instanceof Error ? `上传失败：${cause.message}` : '上传失败，请稍后重试'; publish() }
  }
  input.value = ''
}
async function waitUntilProcessed(id: string, item: Item) {
  const controller = new AbortController(); activeControllers.add(controller)
  try {
    for (let attempt = 0; attempt < 40; attempt += 1) {
      const status = await questionFileStatus(id, controller.signal)
      if (status.processingState === 'ready') { item.state = 'ready'; item.status = status; item.message = '已就绪'; publish(); return }
      if (status.processingState === 'rejected' || status.processingState === 'failed') { item.state = 'rejected'; item.message = status.processingState === 'rejected' ? '未通过安全检查' : '文件处理失败'; publish(); return }
      await new Promise((resolve) => setTimeout(resolve, 1500))
    }
    item.state = 'rejected'; item.message = '文件处理超时，请重新选择'; publish()
  } finally { activeControllers.delete(controller) }
}
function remove(index: number) { items.value.splice(index, 1); publish() }
onBeforeUnmount(() => activeControllers.forEach((controller) => controller.abort()))
</script>
<template>
  <section class="uploader" aria-labelledby="qa-upload-title">
    <label id="qa-upload-title">添加附件（最多 20 个，合计不超过 100 MB）<input type="file" multiple :disabled="disabled || pending" @change="choose"></label>
    <p v-if="error" role="alert">{{ error }}</p>
    <ul v-if="items.length">
      <li v-for="(item,index) in items" :key="`${item.name}-${index}`"><span>{{ item.name }}</span><span :class="item.state">{{ item.message }}</span><button type="button" :aria-label="`移除 ${item.name}`" :disabled="item.state === 'pending'" @click="remove(index)">移除</button></li>
    </ul>
  </section>
</template>
<style scoped>.uploader{display:grid;gap:9px}.uploader label{display:grid;gap:7px}.uploader input{padding:10px;border:1px dashed #8da9c3;border-radius:8px;background:#fff}.uploader ul{display:grid;gap:7px;margin:0;padding:0;list-style:none}.uploader li{display:flex;flex-wrap:wrap;align-items:center;gap:10px;padding:8px;border-radius:8px;background:#f4f7fb}.pending{color:#876108}.ready{color:#287142}.rejected,[role=alert]{color:#a33731}.uploader button{margin-left:auto}</style>
