<script setup lang="ts">
import { computed, onBeforeMount, onBeforeUnmount, ref } from 'vue'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import CatalogTree from './CatalogTree.vue'
import { createCatalog, createLesson, listCatalog, renameCatalog, reorderCatalog, setCatalogArchived } from './api'
import type { CatalogKind, CatalogNode } from './types'

type DialogKind = 'create' | 'rename' | 'archive'
const session = useSessionStore()
const isAdmin = computed(() => session.user?.role === 'admin')
const nodes = ref<CatalogNode[]>([])
const selected = ref<CatalogNode | null>(null)
const loading = ref(false)
const pending = ref(false)
const loadError = ref('')
const requestId = ref('')
const dialog = ref<DialogKind | null>(null)
const name = ref('')
const dialogError = ref('')
let activeLoad: AbortController | undefined

const childKind: Record<Exclude<CatalogKind, 'lesson'>, CatalogKind> = { grade: 'term', term: 'subject', subject: 'chapter', chapter: 'lesson' }
const kindLabel: Record<CatalogKind, string> = { grade: '年级', term: '学期', subject: '学科', chapter: '章节', lesson: '课程' }
const createKind = computed<CatalogKind>(() => selected.value && selected.value.kind !== 'lesson' ? childKind[selected.value.kind] : 'grade')
const canCreate = computed(() => !selected.value || selected.value.kind !== 'lesson')

function errorDetails(error: unknown, fallback: string) {
  return error instanceof APIError ? { message: error.message || fallback, requestId: error.requestId } : { message: fallback, requestId: '' }
}

async function load() {
  if (!isAdmin.value || loading.value) return
  activeLoad?.abort()
  const controller = new AbortController()
  activeLoad = controller
  loading.value = true
  loadError.value = ''
  requestId.value = ''
  try { nodes.value = await listCatalog(controller.signal) }
  catch (error) {
    if (controller.signal.aborted) return
    const details = errorDetails(error, '教学目录加载失败，请稍后重试')
    loadError.value = details.message
    requestId.value = details.requestId
  } finally { if (activeLoad === controller) { activeLoad = undefined; loading.value = false } }
}

function openCreate() { dialog.value = 'create'; name.value = ''; dialogError.value = '' }
function openRename(node: CatalogNode) { selected.value = node; dialog.value = 'rename'; name.value = node.name; dialogError.value = '' }
function openArchive(node: CatalogNode) { selected.value = node; dialog.value = 'archive'; dialogError.value = '' }
function closeDialog() { if (!pending.value) { dialog.value = null; name.value = ''; dialogError.value = '' } }

async function submitDialog() {
  if (pending.value || !dialog.value) return
  if (dialog.value !== 'archive' && !name.value.trim()) { dialogError.value = '请输入目录名称'; return }
  pending.value = true
  dialogError.value = ''
  try {
    if (dialog.value === 'create') {
      const kind = createKind.value
      if (kind === 'lesson' && selected.value?.kind === 'chapter') await createLesson(selected.value.id, name.value.trim())
      else if (kind !== 'lesson') await createCatalog({ kind, parentId: selected.value?.id ?? '', name: name.value.trim(), sortKey: (nodes.value.length + 1) * 10 })
    } else if (dialog.value === 'rename' && selected.value) await renameCatalog(selected.value, name.value.trim())
    else if (dialog.value === 'archive' && selected.value) await setCatalogArchived(selected.value, true)
    dialog.value = null
    name.value = ''
    await load()
  } catch (error) { dialogError.value = errorDetails(error, '目录操作失败，请稍后重试').message }
  finally { pending.value = false }
}

async function reorder(node: CatalogNode, direction: -1 | 1) {
  if (pending.value) return
  pending.value = true
  try { await reorderCatalog(node, node.sortKey + direction * 10); await load() }
  catch (error) { const details = errorDetails(error, '目录排序失败，请稍后重试'); loadError.value = details.message; requestId.value = details.requestId }
  finally { pending.value = false }
}

onBeforeMount(() => { void load() })
onBeforeUnmount(() => activeLoad?.abort())
</script>

<template>
  <section v-if="!isAdmin" aria-labelledby="teaching-title"><h1 id="teaching-title">无权访问教学管理</h1><p>此功能仅对教师开放。</p></section>
  <section v-else class="teaching-page" aria-labelledby="teaching-title">
    <header class="page-heading"><div><p class="eyebrow">教师工作台</p><h1 id="teaching-title">教学管理</h1><p>维护年级、学期、学科、章节和课程内容。</p></div><button class="primary" type="button" :aria-label="`创建${kindLabel[createKind]}`" :disabled="loading || !canCreate" @click="openCreate">创建{{ kindLabel[createKind] }}</button></header>
    <p v-if="loading" role="status">正在加载教学目录…</p>
    <div v-else-if="loadError" role="alert" class="notice error"><p>{{ loadError }}</p><p v-if="requestId">支持编号：{{ requestId }}</p><button type="button" aria-label="重试加载教学目录" @click="load">重试</button></div>
    <div v-else-if="nodes.length === 0" class="empty"><h2>还没有教学目录</h2><p>先创建第一个年级，再逐级添加教学内容。</p><button type="button" aria-label="创建年级" @click="openCreate">创建年级</button></div>
    <CatalogTree v-else :nodes="nodes" :selected-id="selected?.id ?? ''" @select="selected = $event" @rename="openRename" @archive="openArchive" @reorder="reorder" />
    <p v-if="selected?.kind === 'lesson'" class="editor-link"><a class="primary" :href="`/admin/teaching/lessons/${encodeURIComponent(selected.id)}`" :aria-label="`编辑课程 ${selected.name}`">打开课程编辑器</a></p>

    <div v-if="dialog" class="dialog-backdrop" @click.self="closeDialog">
      <section role="dialog" aria-modal="true" :aria-labelledby="`${dialog}-title`" class="dialog-card">
        <h2 :id="`${dialog}-title`">{{ dialog === 'create' ? `创建${kindLabel[createKind]}` : dialog === 'rename' ? `重命名 ${selected?.name}` : `确认归档 ${selected?.name}` }}</h2>
        <form @submit.prevent="submitDialog">
          <label v-if="dialog !== 'archive'">{{ dialog === 'create' && createKind === 'lesson' ? '课程名称' : '目录名称' }}<input v-model="name" :aria-label="dialog === 'create' && createKind === 'lesson' ? '课程名称' : '目录名称'" maxlength="160" autofocus></label>
          <p v-else>归档后学生不可再浏览其下内容。包含子项时，服务器会拒绝不安全操作。</p>
          <p v-if="dialogError" role="alert">{{ dialogError }}</p>
          <div class="dialog-actions"><button type="button" :disabled="pending" @click="closeDialog">取消</button><button class="primary" type="submit" :disabled="pending">{{ pending ? '处理中…' : '确认' }}</button></div>
        </form>
      </section>
    </div>
  </section>
</template>

<style scoped>
.teaching-page{display:grid;gap:24px}.page-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:24px}.page-heading h1{margin:.2rem 0}.eyebrow{margin:0;color:var(--hl-primary-strong);font-weight:800}.primary,.empty button{display:inline-block;border:0;border-radius:8px;padding:10px 16px;background:var(--hl-primary);color:#fff;font:inherit;font-weight:700;text-decoration:none;cursor:pointer}.primary:disabled{opacity:.55;cursor:not-allowed}.editor-link{margin:0;text-align:right}.empty,.notice{padding:32px;border:1px dashed var(--hl-border-strong);border-radius:12px;background:var(--hl-surface-solid);text-align:center}.error{border-style:solid;border-color:#e0aaa6;color:#872e29}.dialog-backdrop{position:fixed;z-index:20;inset:0;display:grid;place-items:center;padding:20px;background:#07182888}.dialog-card{width:min(460px,100%);padding:24px;border-radius:12px;background:var(--hl-surface-solid);box-shadow:var(--hl-shadow)}.dialog-card form,.dialog-card label{display:grid;gap:12px}.dialog-card input{padding:10px;border:1px solid var(--hl-border-strong);border-radius:7px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit}.dialog-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:18px}.dialog-actions button{padding:9px 14px;border:1px solid var(--hl-border-strong);border-radius:7px;background:var(--hl-surface-solid);color:var(--hl-text);font:inherit}@media(max-width:650px){.page-heading{display:grid}}
</style>
