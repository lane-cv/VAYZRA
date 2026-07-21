<script setup lang="ts">
import { computed, onBeforeMount, onBeforeUnmount, ref } from 'vue'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import { deleteFile, fileDetail, listFiles, replaceFile, retryFile, rollbackFile } from './api'
import type { FileDetail, FileFilters, FileItem, FileReference, FileVersion } from './types'

const session = useSessionStore()
const isAdmin = computed(() => session.user?.role === 'admin')
const filters = ref<FileFilters>({ q: '', type: '', state: '', reference: '' })
const items = ref<FileItem[]>([])
const nextCursor = ref('')
const cursorHistory = ref<string[]>([])
const activeCursor = ref('')
const loading = ref(false)
const pending = ref(false)
const errorMessage = ref('')
const requestId = ref('')
const detail = ref<FileDetail | null>(null)
const detailLoading = ref(false)
const confirmDelete = ref<FileItem | null>(null)
const replaceTarget = ref<FileItem | null>(null)
const uploadedVersionId = ref('')
const rollbackTarget = ref<{ version: FileVersion; reference: FileReference } | null>(null)
let listController: AbortController | undefined
let detailController: AbortController | undefined

const stateLabel: Record<string, string> = { pending_scan: '等待扫描', processing: '处理中', ready: '可用', rejected: '已拒绝', failed: '处理失败' }
const failureLabel: Record<string, string> = { conversion_failed: '转换失败', scanner_unavailable: '扫描服务暂不可用', scanner_definitions_stale: '病毒库需要更新', parser_unavailable: '文件解析暂不可用', probe_failed: '视频检测失败', storage_unavailable: '存储暂不可用', preview_unavailable: '预览生成失败', lease_expired: '处理任务超时' }
function bytes(value: number) { if (value < 1024) return `${value} B`; if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`; return `${(value / 1024 / 1024).toFixed(1)} MiB` }
function friendlyError(error: unknown, fallback: string) {
  if (error instanceof APIError) {
    if (error.code === 'file_in_use') return { message: '无法删除：文件仍被以下课程引用，请先解除草稿和发布引用。', requestId: error.requestId }
    if (error.code === 'file_version_expired') return { message: '该历史版本已超过 30 天保留期，无法回滚。', requestId: error.requestId }
    return { message: error.message || fallback, requestId: error.requestId }
  }
  return { message: fallback, requestId: '' }
}
function showError(error: unknown, fallback: string) { const result = friendlyError(error, fallback); errorMessage.value = result.message; requestId.value = result.requestId }

async function load(cursor = activeCursor.value, pushHistory = false) {
  if (!isAdmin.value || loading.value) return
  listController?.abort(); const controller = new AbortController(); listController = controller
  loading.value = true; errorMessage.value = ''; requestId.value = ''
  try {
    const page = await listFiles(filters.value, cursor, controller.signal)
    if (pushHistory) cursorHistory.value.push(activeCursor.value)
    activeCursor.value = cursor; items.value = page.items; nextCursor.value = page.nextCursor ?? ''
  } catch (error) { if (!controller.signal.aborted) showError(error, '文件列表加载失败，请稍后重试') }
  finally { if (listController === controller) { listController = undefined; loading.value = false } }
}
async function applyFilters() { cursorHistory.value = []; activeCursor.value = ''; await load('') }
async function nextPage() { if (nextCursor.value) await load(nextCursor.value, true) }
async function previousPage() { const cursor = cursorHistory.value.pop(); if (cursor !== undefined) await load(cursor) }
async function openDetail(item: FileItem) {
  detailController?.abort(); const controller = new AbortController(); detailController = controller; detailLoading.value = true; errorMessage.value = ''
  try { detail.value = await fileDetail(item.id, controller.signal) }
  catch (error) { if (!controller.signal.aborted) showError(error, '文件详情加载失败，请稍后重试') }
  finally { if (detailController === controller) { detailController = undefined; detailLoading.value = false } }
}
async function retry(item: FileItem) { if (pending.value) return; pending.value = true; try { await retryFile(item.id, item.latest.id); await load() } catch (error) { showError(error, '重试失败，请稍后再试') } finally { pending.value = false } }
async function submitDelete() { if (!confirmDelete.value || pending.value) return; const target = confirmDelete.value; pending.value = true; try { await deleteFile(target.id); confirmDelete.value = null; detail.value = null; await load() } catch (error) { if (error instanceof APIError && error.code === 'file_in_use') await openDetail(target); showError(error, '删除请求失败，请稍后再试') } finally { pending.value = false } }
async function submitReplace() { if (!replaceTarget.value || !uploadedVersionId.value.trim() || pending.value) return; pending.value = true; try { await replaceFile(replaceTarget.value.id, uploadedVersionId.value.trim()); replaceTarget.value = null; uploadedVersionId.value = ''; await load() } catch (error) { showError(error, '替换失败，请检查上传版本编号') } finally { pending.value = false } }
async function submitRollback() { if (!detail.value || !rollbackTarget.value || pending.value) return; pending.value = true; try { await rollbackFile(detail.value.id, rollbackTarget.value.reference.lessonId, rollbackTarget.value.version.id); rollbackTarget.value = null; detail.value = await fileDetail(detail.value.id); await load() } catch (error) { showError(error, '回滚失败，请稍后再试') } finally { pending.value = false } }
onBeforeMount(() => { void load('') })
onBeforeUnmount(() => { listController?.abort(); detailController?.abort() })
</script>

<template>
  <section v-if="!isAdmin"><h1>无权访问文件中心</h1><p>此功能仅对教师开放。</p></section>
  <section v-else class="files-page" aria-labelledby="files-title">
    <header><div><p class="eyebrow">教师工作台</p><h1 id="files-title">文件中心</h1><p>查看处理状态、课程引用与 30 天版本保留。</p></div></header>
    <form aria-label="文件筛选" class="filters" @submit.prevent="applyFilters">
      <label>文件名<input v-model="filters.q" aria-label="文件名筛选" maxlength="100"></label>
      <label>类型<select v-model="filters.type" aria-label="文件类型筛选"><option value="">全部</option><option value="document">PDF</option><option value="image">图片</option><option value="office">Office</option><option value="video">视频</option><option value="text">文本</option></select></label>
      <label>处理状态<select v-model="filters.state" aria-label="处理状态筛选"><option value="">全部</option><option value="pending_scan">等待扫描</option><option value="processing">处理中</option><option value="ready">可用</option><option value="rejected">已拒绝</option><option value="failed">失败</option></select></label>
      <label>引用<select v-model="filters.reference" aria-label="引用状态筛选"><option value="">全部</option><option value="referenced">有引用</option><option value="unreferenced">无引用</option><option value="draft">草稿</option><option value="published">已发布</option></select></label>
      <button type="submit" :disabled="loading">筛选</button>
    </form>
    <div v-if="errorMessage" role="alert" class="notice error"><p>{{ errorMessage }}</p><p v-if="requestId">支持编号：{{ requestId }}</p></div>
    <p v-if="loading" role="status">正在加载文件…</p>
    <div v-else-if="items.length === 0" class="notice"><h2>没有匹配的文件</h2><p>调整筛选条件，或先在课程编辑器上传附件。</p></div>
    <div v-else class="table-wrap"><table><thead><tr><th>文件</th><th>状态</th><th>大小</th><th>版本</th><th>引用</th><th>操作</th></tr></thead><tbody><tr v-for="item in items" :key="item.id"><td><strong>{{ item.latest.displayName }}</strong><small>{{ item.latest.detectedMime || item.latest.declaredMime }}</small></td><td><span class="state">{{ stateLabel[item.latest.processingState] }}</span><small v-if="item.latest.failureCategory">{{ failureLabel[item.latest.failureCategory] || '处理失败' }}</small></td><td>{{ bytes(item.latest.size) }}</td><td>v{{ item.latest.version }}</td><td>{{ item.referenceCount }}</td><td class="actions"><button type="button" :aria-label="`查看 ${item.latest.displayName} 引用`" @click="openDetail(item)">详情</button><button v-if="item.latest.processingState === 'failed'" type="button" :aria-label="`重试 ${item.latest.displayName}`" :disabled="pending" @click="retry(item)">重试</button><button type="button" :aria-label="`替换 ${item.latest.displayName}`" @click="replaceTarget = item">替换</button><button class="danger" type="button" :aria-label="`删除 ${item.latest.displayName}`" @click="confirmDelete = item">删除</button></td></tr></tbody></table></div>
    <nav class="pagination" aria-label="文件分页"><button type="button" aria-label="上一页文件" :disabled="loading || cursorHistory.length === 0" @click="previousPage">上一页</button><button type="button" aria-label="下一页文件" :disabled="loading || !nextCursor" @click="nextPage">下一页</button></nav>

    <aside v-if="detail || detailLoading" aria-label="文件详情" class="detail"><button class="close" type="button" aria-label="关闭文件详情" @click="detail = null">关闭</button><p v-if="detailLoading">正在加载详情…</p><template v-else-if="detail"><h2>引用与版本</h2><h3>课程引用</h3><p v-if="detail.references.length === 0">当前无课程引用</p><ul><li v-for="reference in detail.references" :key="`${reference.kind}-${reference.lessonId}-${reference.revisionId || ''}`">{{ reference.kind === 'draft' ? '草稿' : '已发布' }} · {{ reference.lessonTitle }}</li></ul><h3>历史版本</h3><ul><li v-for="version in detail.versions" :key="version.id">v{{ version.version }} · {{ version.displayName }} · {{ stateLabel[version.processingState] }} <template v-for="reference in detail.references.filter((entry) => entry.kind === 'draft')" :key="reference.lessonId"><button type="button" :aria-label="`将 ${reference.lessonTitle} 草稿回滚到 v${version.version}`" :disabled="pending" @click="rollbackTarget = { version, reference }">回滚“{{ reference.lessonTitle }}”草稿</button></template></li></ul></template></aside>

    <div v-if="confirmDelete" class="backdrop" @click.self="confirmDelete = null"><section role="dialog" aria-modal="true"><h2>确认删除 {{ confirmDelete.latest.displayName }}</h2><p>文件将进入 30 天保留期；仍被课程引用时服务器会拒绝。</p><div><button type="button" @click="confirmDelete = null">取消</button><button class="danger" type="button" aria-label="确认删除文件" :disabled="pending" @click="submitDelete">确认删除</button></div></section></div>
    <div v-if="replaceTarget" class="backdrop" @click.self="replaceTarget = null"><section role="dialog" aria-modal="true"><h2>替换 {{ replaceTarget.latest.displayName }}</h2><label>已完成上传的版本编号<input v-model="uploadedVersionId" aria-label="上传版本编号"></label><div><button type="button" @click="replaceTarget = null">取消</button><button type="button" :disabled="pending || !uploadedVersionId.trim()" @click="submitReplace">确认替换</button></div></section></div>
    <div v-if="rollbackTarget" class="backdrop" @click.self="rollbackTarget = null"><section role="dialog" aria-modal="true"><h2>确认回滚课程草稿</h2><p>将“{{ rollbackTarget.reference.lessonTitle }}”的草稿附件切换到 v{{ rollbackTarget.version.version }}。当前已发布版本不会改变。</p><div><button type="button" @click="rollbackTarget = null">取消</button><button type="button" aria-label="确认回滚草稿" :disabled="pending" @click="submitRollback">确认回滚</button></div></section></div>
  </section>
</template>

<style scoped>
.files-page{display:grid;gap:22px;color:#182842}.eyebrow{margin:0;color:#2879b5;font-weight:800}h1{margin:.2rem 0}.filters{display:grid;grid-template-columns:2fr repeat(3,1fr) auto;gap:12px;align-items:end;padding:18px;border:1px solid #d7e2ed;border-radius:12px;background:#fff}.filters label{display:grid;gap:6px;color:#4d6178;font-size:.85rem}.filters input,.filters select{min-width:0;padding:9px;border:1px solid #afc2d5;border-radius:7px;background:#fff;font:inherit}.filters button,.actions button,.pagination button,.detail button,.backdrop button{padding:8px 11px;border:1px solid #b7cadc;border-radius:7px;background:#fff;color:#244563;font:inherit;cursor:pointer}.table-wrap{overflow:auto;border:1px solid #d7e2ed;border-radius:12px;background:#fff}table{width:100%;border-collapse:collapse}th,td{padding:14px;text-align:left;border-bottom:1px solid #e7edf4;vertical-align:top}small{display:block;margin-top:4px;color:#66778c}.actions{display:flex;flex-wrap:wrap;gap:6px}.danger{color:#a23731!important;border-color:#dfb3b0!important}.state{font-weight:700}.pagination{display:flex;justify-content:flex-end;gap:8px}.notice{padding:26px;border:1px dashed #b9cadb;border-radius:12px;background:#fff;text-align:center}.error{border-style:solid;border-color:#e0aaa6;color:#872e29}.detail{position:fixed;z-index:10;top:80px;right:20px;bottom:20px;width:min(430px,calc(100vw - 40px));overflow:auto;padding:24px;border:1px solid #c7d7e6;border-radius:14px;background:#fff;box-shadow:0 18px 55px #102b4d33}.detail .close{float:right}.backdrop{position:fixed;z-index:20;inset:0;display:grid;place-items:center;padding:20px;background:#07182888}.backdrop section{width:min(460px,100%);padding:24px;border-radius:12px;background:#fff}.backdrop section>div{display:flex;justify-content:flex-end;gap:9px;margin-top:20px}.backdrop label{display:grid;gap:7px}.backdrop input{padding:9px;border:1px solid #afc2d5;border-radius:7px}@media(max-width:850px){.filters{grid-template-columns:1fr 1fr}.table-wrap table{min-width:780px}}@media(max-width:560px){.filters{grid-template-columns:1fr}}
</style>
