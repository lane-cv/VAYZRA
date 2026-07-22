<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useSessionStore } from '../../stores/session'
import { createUploadManager, type UploadManagerState } from '../teaching/uploadManager'
import { questionFileStatus } from './studentApi'
import { adminQuestionUploadTransport, createAdminQuestionSessionStore, createStudentQuestionSessionStore, studentQuestionUploadTransport } from './questionUpload'
import type { AttachmentInput, QAFileStatus } from './types'

const props = withDefaults(defineProps<{ userId?: string; disabled?: boolean; role?: 'student'|'admin' }>(), { userId: '', disabled: false, role:'student' })
const emit = defineEmits<{ 'update:attachments': [value: AttachmentInput[]]; 'pending-change': [value: boolean] }>()
type Item = { name: string; size: number; state: 'pending' | 'ready' | 'rejected'; message: string; status?: QAFileStatus }
const items = ref<Item[]>([])
const error = ref('')
const inputRef = ref<HTMLInputElement>()
const activeControllers = new Set<AbortController>()
const activeManagers = new Set<ReturnType<typeof createUploadManager>>()
const session = props.userId ? undefined : useSessionStore()
let generation = 0
let destroyed = false
const pending = computed(() => items.value.some((item) => item.state === 'pending'))
const limits: Record<string, number> = { 'image/jpeg': 20<<20, 'image/png': 20<<20, 'image/webp': 20<<20, 'image/gif': 20<<20, 'application/pdf': 50<<20, 'application/vnd.openxmlformats-officedocument.wordprocessingml.document': 30<<20, 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': 30<<20, 'application/vnd.openxmlformats-officedocument.presentationml.presentation': 30<<20, 'text/plain': 10<<20, 'text/markdown': 10<<20 }

function isActive(token: number) { return !destroyed && token === generation }
function publish(token = generation) {
  if (!isActive(token)) return
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
  const token = generation
  for (const file of files) {
    if (!isActive(token)) break
    items.value.push({ name: file.name, size: file.size, state: 'pending', message: '等待上传' })
    const item = items.value[items.value.length - 1]!
    publish(token)
    const uid=props.userId||session?.user?.id||'unknown'
    const manager = createUploadManager({ transport: props.role==='admin'?adminQuestionUploadTransport:studentQuestionUploadTransport, sessions: props.role==='admin'?createAdminQuestionSessionStore(uid):createStudentQuestionSessionStore(uid), onState: (state: UploadManagerState) => {
      if (!isActive(token)) return
      if (state.kind === 'hashing') item.message = `正在校验 ${state.progress}%`
      else if (state.kind === 'uploading') item.message = `正在上传 ${state.progress}%`
      else if (state.kind === 'failed') { item.state = 'rejected'; item.message = `上传失败：${state.message}`; publish(token) }
    } })
    activeManagers.add(manager)
    try {
      const completed = await manager.start(file)
      activeManagers.delete(manager)
      if (!isActive(token)) break
      if (!completed) { item.state = 'rejected'; item.message = '上传已取消'; publish(token); continue }
      item.message = '正在进行安全检查'; await waitUntilProcessed(completed.fileVersionId, item, token)
    } catch (cause) {
      activeManagers.delete(manager)
      if (!isActive(token)) break
      item.state = 'rejected'; item.message = cause instanceof Error ? `上传失败：${cause.message}` : '上传失败，请稍后重试'; publish(token)
    }
  }
  if (isActive(token)) input.value = ''
}
async function waitUntilProcessed(id: string, item: Item, token: number) {
  const controller = new AbortController(); activeControllers.add(controller)
  try {
    for (let attempt = 0; attempt < 40; attempt += 1) {
      if (!isActive(token)) return
      const status = await questionFileStatus(id, controller.signal)
      if (!isActive(token)) return
      if (status.processingState === 'ready') { item.state = 'ready'; item.status = status; item.message = '已就绪'; publish(token); return }
      if (status.processingState === 'rejected' || status.processingState === 'failed') { item.state = 'rejected'; item.message = status.processingState === 'rejected' ? '未通过安全检查' : '文件处理失败'; publish(token); return }
      await abortableDelay(1500, controller.signal)
    }
    if (isActive(token)) { item.state = 'rejected'; item.message = '文件处理超时，请重新选择'; publish(token) }
  } finally { activeControllers.delete(controller) }
}
function remove(index: number) { items.value.splice(index, 1); publish() }
function abortableDelay(milliseconds: number, signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const onAbort=()=>{clearTimeout(timer);reject(new DOMException('aborted','AbortError'))}
    const timer=setTimeout(()=>{signal.removeEventListener('abort',onAbort);resolve()},milliseconds)
    signal.addEventListener('abort',onAbort,{once:true})
  })
}
function invalidate(clear: boolean) {
  generation += 1
  activeControllers.forEach((controller) => controller.abort()); activeControllers.clear()
  const managers = [...activeManagers]; activeManagers.clear(); managers.forEach((manager) => { try { const cancellation=manager.cancel();if(cancellation !== undefined)void cancellation.catch(()=>undefined) } catch { /* Teardown remains best-effort. */ } })
  if (clear && !destroyed) { items.value = []; error.value = '';if(inputRef.value)inputRef.value.value='';emit('pending-change', false); emit('update:attachments', []) }
}
watch(() => props.userId || session?.user?.id || '', (next, previous) => { if (next !== previous) invalidate(true) })
onBeforeUnmount(() => { destroyed = true; invalidate(false) })
</script>
<template>
  <section class="uploader" aria-labelledby="qa-upload-title">
    <label id="qa-upload-title">添加附件（最多 20 个，合计不超过 100 MB）<input ref="inputRef" type="file" multiple :disabled="disabled || pending" @change="choose"></label>
    <p v-if="error" role="alert">{{ error }}</p>
    <ul v-if="items.length">
      <li v-for="(item,index) in items" :key="`${item.name}-${index}`"><span>{{ item.name }}</span><span :class="item.state">{{ item.message }}</span><button type="button" :aria-label="`移除 ${item.name}`" :disabled="item.state === 'pending'" @click="remove(index)">移除</button></li>
    </ul>
  </section>
</template>
<style scoped>.uploader{display:grid;gap:9px}.uploader label{display:grid;gap:7px}.uploader input{padding:10px;border:1px dashed #8da9c3;border-radius:8px;background:#fff}.uploader ul{display:grid;gap:7px;margin:0;padding:0;list-style:none}.uploader li{display:flex;flex-wrap:wrap;align-items:center;gap:10px;padding:8px;border-radius:8px;background:#f4f7fb}.pending{color:#876108}.ready{color:#287142}.rejected,[role=alert]{color:#a33731}.uploader button{margin-left:auto}</style>
