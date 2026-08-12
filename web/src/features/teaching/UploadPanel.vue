<script setup lang="ts">
import { computed, ref } from 'vue'
import { browserUploadTransport } from './uploadApi'
import { createIndexedDBUploadSessionStore, createUploadManager, type CompletedUpload, type UploadManagerState } from './uploadManager'
import { fileDetail } from '../files/api'
import { listLessonFiles, replaceLessonFiles } from './api'
import type { FileVersion } from '../files/types'

type AccessPolicy = 'preview' | 'download'
const props = withDefaults(defineProps<{ lessonId?: string; lockVersion?: number; canBind?: boolean }>(), { lessonId: '', lockVersion: 0, canBind: false })
const emit = defineEmits<{ uploaded: [result: CompletedUpload, policy: AccessPolicy]; bindingChanged: [] }>()
const policy = ref<AccessPolicy | ''>('')
const state = ref<UploadManagerState>({ kind: 'idle' })
const selectedName = ref('')
const processedVersion = ref<FileVersion | null>(null)
const bindingMessage = ref('')
const checking = ref(false)
const binding = ref(false)
const manager = createUploadManager({ transport: browserUploadTransport, sessions: createIndexedDBUploadSessionStore(), onState: (next) => {
  state.value = next
  if (next.kind === 'processing' && policy.value) emit('uploaded', next.result, policy.value)
} })

const busy = computed(() => state.value.kind === 'hashing' || state.value.kind === 'uploading')
const statusText = computed(() => {
  switch (state.value.kind) {
    case 'idle': return policy.value ? '请选择文件开始上传' : '请先选择学生访问方式'
    case 'hashing': return `正在校验文件 ${state.value.progress}%`
    case 'uploading': return `正在上传 ${state.value.progress}%`
    case 'paused': return `上传已暂停 ${state.value.progress}%`
    case 'processing': return processingText(state.value.result.processingState)
    case 'cancelled': return '上传已取消'
    case 'failed': return `上传失败：${state.value.message}`
    default: return '文件状态未知'
  }
})

async function choose(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0]
  if (!file || !policy.value) return
  selectedName.value = file.name
  try { await manager.start(file) }
  catch (error) {
    if (state.value.kind !== 'failed') state.value = { kind: 'failed', message: error instanceof Error ? error.message : '请稍后重试' }
  }
}
async function resumeUpload() {
  try { await manager.resume() }
  catch (error) { state.value = { kind: 'failed', message: error instanceof Error ? error.message : '请稍后重试' } }
}
async function cancelUpload() {
  try { await manager.cancel() }
  catch (error) { state.value = { kind: 'failed', message: error instanceof Error ? error.message : '取消失败，请稍后重试' } }
}
async function checkProcessing() {
  if (state.value.kind !== 'processing' || checking.value) return
  const result = state.value.result
  checking.value = true
  bindingMessage.value = ''
  try {
    const detail = await fileDetail(result.fileId)
    const version = detail.versions.find((item) => item.id === result.fileVersionId)
    if (!version) { bindingMessage.value = '未找到上传的文件版本，请前往文件中心检查'; return }
    processedVersion.value = version
    if (version.processingState === 'rejected') bindingMessage.value = '文件未通过安全检查，不能绑定到课程'
    else if (version.processingState === 'failed') bindingMessage.value = '文件处理失败，请前往文件中心重试'
    else if (version.processingState !== 'ready') bindingMessage.value = '文件仍在安全扫描或预览转换中'
    else if (policy.value === 'preview' && version.previewState !== 'ready') bindingMessage.value = '该文件没有可用的安全预览，不能按“在线预览”绑定'
    else bindingMessage.value = '文件已就绪，可以绑定'
  } catch { bindingMessage.value = '文件状态检查失败，请稍后重试' }
  finally { checking.value = false }
}
async function bindToLesson() {
  const version = processedVersion.value
  if (!version || !props.lessonId || !props.canBind || props.lockVersion < 1 || binding.value) return
  binding.value = true
  bindingMessage.value = ''
  try {
    const existing = await listLessonFiles(props.lessonId)
    const files = existing.map(({ fileVersionId, policy: accessPolicy, displayName, description, sortPosition }) => ({ fileVersionId, policy: accessPolicy, displayName, description, sortPosition }))
    if (!files.some((item) => item.fileVersionId === version.id)) files.push({ fileVersionId: version.id, policy: policy.value as AccessPolicy, displayName: version.displayName, description: '', sortPosition: Math.max(0, ...files.map((item) => item.sortPosition)) + 10 })
    await replaceLessonFiles(props.lessonId, props.lockVersion, files)
    bindingMessage.value = '文件已绑定到课程'
    emit('bindingChanged')
  } catch { bindingMessage.value = '文件绑定失败；草稿可能已更新，请重新加载后重试' }
  finally { binding.value = false }
}
const canAttach = computed(() => processedVersion.value?.processingState === 'ready' && (policy.value !== 'preview' || processedVersion.value.previewState === 'ready'))
function processingText(value: string) {
  if (value === 'ready') return '文件已就绪'
  if (value === 'rejected') return '文件未通过安全检查'
  if (value === 'converting') return '上传完成，正在转换预览'
  return '上传完成，正在进行安全扫描和预览转换'
}
</script>

<template>
  <section class="upload-panel" aria-labelledby="upload-title">
    <header><div><h2 id="upload-title">课程文件</h2><p>文件先进入安全扫描和预览转换；完成绑定前不会向学生开放。</p></div></header>
    <fieldset :disabled="busy"><legend>学生访问方式</legend><label><input v-model="policy" type="radio" value="preview">在线预览</label><label><input v-model="policy" type="radio" value="download">允许下载</label></fieldset>
    <label class="file-picker">选择文件<input type="file" :disabled="!policy || busy" @change="choose"></label>
    <p role="status" aria-live="polite">{{ selectedName ? `${selectedName}：` : '' }}{{ statusText }}</p>
    <div class="actions">
      <button v-if="state.kind === 'uploading' || state.kind === 'hashing'" type="button" aria-label="暂停上传" @click="manager.pause">暂停</button>
      <button v-if="state.kind === 'paused'" type="button" aria-label="继续上传" @click="resumeUpload">继续</button>
      <button v-if="busy || state.kind === 'paused'" type="button" aria-label="取消上传" @click="cancelUpload">取消</button>
    </div>
    <p v-if="state.kind === 'processing'">文件已进入文件中心。处理完成后才能安全绑定到课程。</p>
    <div v-if="state.kind === 'processing' && lessonId" class="binding-controls">
      <button type="button" aria-label="检查文件处理状态" :disabled="checking" @click="checkProcessing">{{ checking ? '正在检查…' : '检查处理状态' }}</button>
      <button v-if="canAttach" type="button" aria-label="绑定文件到课程" :disabled="!canBind || binding" @click="bindToLesson">{{ binding ? '正在绑定…' : '绑定到课程' }}</button>
      <p v-if="canAttach && !canBind">请先等待课程正文保存完成，再绑定文件。</p>
      <p v-if="bindingMessage" :role="bindingMessage.includes('失败') || bindingMessage.includes('不能') ? 'alert' : 'status'">{{ bindingMessage }}</p>
    </div>
  </section>
</template>

<style scoped>
.upload-panel{display:grid;gap:12px;padding:16px;border:1px solid var(--hl-border);border-radius:10px;background:var(--hl-surface-solid)}.upload-panel h2,.upload-panel p{margin:.2rem 0}.upload-panel fieldset{display:flex;gap:18px;border:0;padding:0}.upload-panel fieldset label{display:flex;gap:7px}.file-picker{display:grid;gap:7px}.file-picker input{padding:10px;border:1px dashed var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text)}.actions,.binding-controls{display:flex;flex-wrap:wrap;align-items:center;gap:8px}.actions button,.binding-controls button{padding:8px 12px;border:1px solid var(--hl-border-strong);border-radius:7px;background:var(--hl-surface-solid);color:var(--hl-text)}
</style>
