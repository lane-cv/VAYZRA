<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeMount, onBeforeUnmount, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { APIError } from '../../api/client'
import { getLesson, publishLesson, saveDraft } from './api'
import MarkdownPreview from './MarkdownPreview.vue'
import AudiencePicker from './AudiencePicker.vue'
import ExternalVideoEditor from './ExternalVideoEditor.vue'
import UploadPanel from './UploadPanel.vue'
import type { LessonDetail, LessonDraft } from './types'

type SaveKind = 'clean' | 'dirty' | 'saving' | 'failed' | 'conflict'
const props = defineProps<{ lessonId: string }>()
const detail = ref<LessonDetail | null>(null)
const draft = ref<LessonDraft | null>(null)
const loading = ref(true)
const loadError = ref('')
const saveKind = ref<SaveKind>('clean')
const saveMessage = ref('')
const publishOpen = ref(false)
const publishing = ref(false)
const publishMessage = ref('')
const audienceProblems = ref<string[]>([])
let initialized = false
let changeSequence = 0
let savedSequence = 0
let saveTimer: ReturnType<typeof setTimeout> | undefined
let saveInFlight = false
let activeLoad: AbortController | undefined
let generation = 0

const saveLabel = computed(() => ({ clean: '已保存', dirty: '有未保存更改', saving: '正在保存…', failed: '保存失败', conflict: '保存冲突' })[saveKind.value])
const blockers = computed(() => {
  const current = draft.value
  if (!current) return ['课程尚未加载']
  const result: string[] = []
  if (!current.title.trim()) result.push('请填写课程标题')
  if (!current.bodyMarkdown.trim()) result.push('请填写课程正文')
  if (current.audience.mode === 'selected' && current.audience.userIds.length === 0) result.push('请选择至少一名学生')
  result.push(...audienceProblems.value)
  return result
})
const canPublish = computed(() => Boolean(draft.value) && blockers.value.length === 0 && saveKind.value === 'clean' && !publishing.value)
const hasUnsavedChanges = computed(() => ['dirty', 'saving', 'failed', 'conflict'].includes(saveKind.value))

function cloneDraft(value: LessonDraft): LessonDraft {
  return { ...value, audience: { ...value.audience, userIds: [...value.audience.userIds] }, externalVideos: value.externalVideos.map((video) => ({ ...video })) }
}

async function load() {
  generation += 1
  activeLoad?.abort()
  const controller = new AbortController()
  activeLoad = controller
  loading.value = true
  loadError.value = ''
  clearTimeout(saveTimer)
  try {
    const loaded = await getLesson(props.lessonId, controller.signal)
    initialized = false
    detail.value = loaded
    draft.value = cloneDraft(loaded.draft)
    changeSequence = 0
    savedSequence = 0
    saveKind.value = 'clean'
    saveMessage.value = ''
    publishMessage.value = ''
    initialized = true
  } catch (error) {
    if (!controller.signal.aborted) loadError.value = error instanceof APIError ? error.message : '课程加载失败，请稍后重试'
  } finally {
    if (activeLoad === controller) { activeLoad = undefined; loading.value = false }
  }
}

function scheduleSave() {
  if (!initialized || !draft.value || saveKind.value === 'conflict') return
  changeSequence += 1
  saveKind.value = 'dirty'
  saveMessage.value = ''
  clearTimeout(saveTimer)
  saveTimer = setTimeout(() => { void persist() }, 800)
}

async function persist() {
  clearTimeout(saveTimer)
  saveTimer = undefined
  if (!draft.value || saveInFlight || saveKind.value === 'conflict' || changeSequence === savedSequence) return
  saveInFlight = true
  saveKind.value = 'saving'
  const capturedSequence = changeSequence
  const capturedGeneration = generation
  const snapshot = cloneDraft(draft.value)
  try {
    const saved = await saveDraft(snapshot)
    if (capturedGeneration !== generation || draft.value?.lessonId !== snapshot.lessonId) return
    if (draft.value) {
      draft.value.lockVersion = saved.lockVersion
      draft.value.updatedAt = saved.updatedAt
    }
    savedSequence = capturedSequence
    saveMessage.value = ''
    saveKind.value = changeSequence > capturedSequence ? 'dirty' : 'clean'
  } catch (error) {
    if (capturedGeneration !== generation) return
    if (error instanceof APIError && error.code === 'draft_conflict') {
      saveKind.value = 'conflict'
      saveMessage.value = '草稿已在其他页面更新。请重新加载服务器草稿后继续。'
    } else {
      saveKind.value = 'failed'
      saveMessage.value = error instanceof APIError ? error.message : '草稿保存失败，请检查网络后重试'
    }
  } finally {
    saveInFlight = false
    if (saveKind.value === 'dirty' && changeSequence > savedSequence) void persist()
  }
}

function openPublish() {
  if (canPublish.value) { publishMessage.value = ''; publishOpen.value = true }
}

async function publish() {
  if (!draft.value || publishing.value || !canPublish.value) return
  publishing.value = true
  publishMessage.value = ''
  try {
    const revision = await publishLesson(draft.value.lessonId, draft.value.lockVersion)
    if (detail.value) {
      detail.value.status = 'published'
      detail.value.publishedRevisionId = revision.id
      detail.value.currentPublication = revision
    }
    publishOpen.value = false
    publishMessage.value = `发布成功：第 ${revision.version} 版`
  } catch (error) {
    publishMessage.value = error instanceof APIError && error.code === 'lesson_not_publishable'
      ? '服务器检查发现课程暂不可发布，请检查目录、受众、文件预览和外部视频。'
      : error instanceof APIError ? error.message : '发布失败，请稍后重试'
  } finally { publishing.value = false }
}

function beforeUnload(event: BeforeUnloadEvent) {
  if (!hasUnsavedChanges.value) return
  event.preventDefault()
  event.returnValue = ''
}

function confirmDiscard() {
  return !hasUnsavedChanges.value || window.confirm('课程还有未保存的更改，确定离开吗？')
}

watch(
  () => draft.value ? [draft.value.title, draft.value.summary, draft.value.bodyMarkdown, draft.value.sortKey, JSON.stringify(draft.value.audience), JSON.stringify(draft.value.externalVideos)] : null,
  scheduleSave,
  { flush: 'sync' },
)
watch(() => props.lessonId, (next, previous) => { if (next !== previous) void load() })
if (getCurrentInstance()?.appContext.config.globalProperties.$router) {
  onBeforeRouteLeave(() => confirmDiscard())
  onBeforeRouteUpdate(() => confirmDiscard())
}
onBeforeMount(() => { window.addEventListener('beforeunload', beforeUnload); void load() })
onBeforeUnmount(() => { activeLoad?.abort(); clearTimeout(saveTimer); window.removeEventListener('beforeunload', beforeUnload) })
</script>

<template>
  <section class="editor-page" aria-labelledby="lesson-editor-title">
    <p><a href="/admin/teaching">← 返回教学管理</a></p>
    <p v-if="loading" role="status">正在加载课程…</p>
    <div v-else-if="loadError" role="alert" class="notice error"><p>{{ loadError }}</p><button type="button" @click="load">重试</button></div>
    <template v-else-if="draft">
      <header class="page-heading">
        <div><p class="eyebrow">课程编辑器</p><h1 id="lesson-editor-title">{{ draft.title || '未命名课程' }}</h1><p aria-live="polite" class="save-state">{{ saveLabel }}</p></div>
        <button class="primary" type="button" aria-label="发布课程" :disabled="!canPublish" @click="openPublish">发布课程</button>
      </header>
      <p v-if="saveMessage" role="alert" class="notice error">{{ saveMessage }} <button v-if="saveKind === 'conflict'" type="button" aria-label="重新加载服务器草稿" @click="load">重新加载</button><button v-else type="button" @click="persist">重试保存</button></p>
      <p v-if="publishMessage" :role="publishMessage.startsWith('发布成功') ? 'status' : 'alert'" class="notice">{{ publishMessage }}</p>
      <div v-if="blockers.length" class="notice"><strong>发布前还需完成：</strong><ul><li v-for="blocker in blockers" :key="blocker">{{ blocker }}</li></ul></div>
      <div class="editor-grid">
        <form class="fields" @submit.prevent>
          <label>课程标题<input v-model="draft.title" aria-label="课程标题" maxlength="160"></label>
          <label>课程摘要<textarea v-model="draft.summary" aria-label="课程摘要" maxlength="500" rows="3"></textarea></label>
          <label>课程正文（Markdown）<textarea v-model="draft.bodyMarkdown" aria-label="课程正文" rows="18"></textarea></label>
        </form>
        <div><h2>实时预览</h2><MarkdownPreview :source="draft.bodyMarkdown" /></div>
      </div>
      <AudiencePicker v-model="draft.audience" @validation-change="audienceProblems = $event" />
      <UploadPanel :lesson-id="draft.lessonId" :lock-version="draft.lockVersion" :can-bind="saveKind === 'clean'" @binding-changed="load" />
      <ExternalVideoEditor v-model="draft.externalVideos" />
      <div v-if="publishOpen" class="dialog-backdrop" @click.self="publishOpen = false">
        <section role="dialog" aria-modal="true" aria-labelledby="publish-title" class="dialog-card">
          <h2 id="publish-title">发布课程</h2>
          <p>将由服务器再次检查目录、受众、正文、文件预览和外部视频。发布后学生将看到一个不可变的新版本。</p>
          <p v-if="publishMessage" role="alert">{{ publishMessage }}</p>
          <div class="dialog-actions"><button type="button" :disabled="publishing" @click="publishOpen = false">取消</button><button class="primary" type="button" aria-label="确认发布课程" :disabled="publishing" @click="publish">{{ publishing ? '正在发布…' : '确认发布' }}</button></div>
        </section>
      </div>
    </template>
  </section>
</template>

<style scoped>
.editor-page{display:grid;gap:20px}.editor-page>a,a{color:var(--hl-primary-strong)}.page-heading{display:flex;justify-content:space-between;gap:20px;align-items:flex-start}.page-heading h1{margin:.2rem 0}.eyebrow{margin:0;color:var(--hl-primary-strong);font-weight:800}.save-state{color:var(--hl-text-muted)}.editor-grid{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:24px}.fields,.fields label{display:grid;gap:8px}.fields{align-content:start;gap:16px}.fields input,.fields textarea{box-sizing:border-box;width:100%;padding:10px;border:1px solid var(--hl-border-strong);border-radius:7px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit}.fields textarea{resize:vertical}.primary{border:0;border-radius:8px;padding:10px 16px;background:var(--hl-primary);color:#fff;font:inherit;font-weight:700;cursor:pointer}.primary:disabled{opacity:.5;cursor:not-allowed}.notice{padding:14px 16px;border:1px solid var(--hl-border);border-radius:9px;background:var(--hl-surface-solid)}.error{border-color:#e0aaa6;color:#872e29}.dialog-backdrop{position:fixed;z-index:20;inset:0;display:grid;place-items:center;padding:20px;background:#07182888}.dialog-card{width:min(520px,100%);padding:24px;border-radius:12px;background:var(--hl-surface-solid)}.dialog-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:18px}.dialog-actions button{padding:9px 14px;border:1px solid var(--hl-border-strong);border-radius:7px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit}@media(max-width:850px){.editor-grid{grid-template-columns:1fr}.page-heading{display:grid}}
</style>
