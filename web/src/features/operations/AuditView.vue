<script setup lang="ts">
import { computed, nextTick, onBeforeMount, onBeforeUnmount, reactive, ref } from 'vue'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import {
  listAudit,
} from './api'
import type { AuditFilters, AuditMetadata, AuditRecord } from './types'

type FilterForm = {
  action: string
  targetType: string
  outcome: string
  actorId: string
  from: string
  to: string
}

const PAGE_SIZE = 50
const session = useSessionStore()
const isAdmin = computed(() => session.user?.role === 'admin')
const filters = reactive<FilterForm>({
  action: '',
  targetType: '',
  outcome: '',
  actorId: '',
  from: '',
  to: '',
})
const activeFilters = ref<AuditFilters>({})
const items = ref<AuditRecord[]>([])
const nextBeforeId = ref<number | null>(null)
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const requestId = ref('')
const appendError = ref('')
const appendRequestId = ref('')
const resultsTitle = ref<HTMLElement>()
let generation = 0
let alive = true
let replaceController: AbortController | undefined
let appendController: AbortController | undefined

function failure(reason: unknown, fallback: string) {
  return reason instanceof APIError
    ? { message: reason.message || fallback, requestId: reason.requestId }
    : { message: fallback, requestId: '' }
}

function merge(existing: AuditRecord[], incoming: AuditRecord[]): AuditRecord[] {
  const seen = new Set(existing.map((item) => item.id))
  const result = [...existing]
  for (const item of incoming) {
    if (seen.has(item.id)) continue
    seen.add(item.id)
    result.push(item)
  }
  return result
}

async function focusResults() {
  await nextTick()
  resultsTitle.value?.focus()
}

async function replace(restoreFocus = false) {
  if (!isAdmin.value) return
  const requestGeneration = ++generation
  replaceController?.abort()
  appendController?.abort()
  appendController = undefined
  const controller = new AbortController()
  replaceController = controller
  loading.value = true
  loadingMore.value = false
  error.value = ''
  requestId.value = ''
  appendError.value = ''
  appendRequestId.value = ''
  try {
    const page = await listAudit({ ...activeFilters.value, limit: PAGE_SIZE }, controller.signal)
    if (!alive || requestGeneration !== generation || controller.signal.aborted) return
    items.value = merge([], page.items)
    nextBeforeId.value = page.nextBeforeId
  } catch (reason) {
    if (!alive || requestGeneration !== generation || controller.signal.aborted) return
    const details = failure(reason, '审计日志加载失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
    items.value = []
    nextBeforeId.value = null
  } finally {
    if (alive && requestGeneration === generation) {
      loading.value = false
      if (replaceController === controller) replaceController = undefined
    }
  }
  if (restoreFocus && alive && requestGeneration === generation) await focusResults()
}

async function loadMore(restoreFocus = false) {
  if (!isAdmin.value || loading.value || loadingMore.value || nextBeforeId.value === null) return
  appendController?.abort()
  const controller = new AbortController()
  appendController = controller
  const requestGeneration = generation
  const cursor = nextBeforeId.value
  loadingMore.value = true
  appendError.value = ''
  appendRequestId.value = ''
  try {
    const page = await listAudit({
      ...activeFilters.value,
      beforeId: cursor,
      limit: PAGE_SIZE,
    }, controller.signal)
    if (!alive || requestGeneration !== generation || controller.signal.aborted) return
    items.value = merge(items.value, page.items)
    nextBeforeId.value = page.nextBeforeId
  } catch (reason) {
    if (!alive || requestGeneration !== generation || controller.signal.aborted) return
    const details = failure(reason, '更多审计日志加载失败，请稍后重试')
    appendError.value = details.message
    appendRequestId.value = details.requestId
  } finally {
    if (alive && requestGeneration === generation) {
      loadingMore.value = false
      if (appendController === controller) appendController = undefined
    }
  }
  if (restoreFocus && alive && requestGeneration === generation) await focusResults()
}

function submittedFilters(): AuditFilters {
  const result: AuditFilters = {}
  for (const key of ['action', 'targetType', 'outcome', 'actorId', 'from', 'to'] as const) {
    const value = filters[key].trim()
    if (value) result[key] = value
  }
  return result
}

function applyFilters() {
  activeFilters.value = submittedFilters()
  void replace()
}

const metadataLabels: Record<keyof AuditMetadata, string> = {
  status: '状态',
  reason: '原因',
  version: '版本',
  count: '数量',
  provider_id: '供应商 ID',
  model_id: '模型 ID',
  file_purpose: '文件用途',
}

function safeMetadata(metadata: AuditMetadata): Array<{ key: keyof AuditMetadata; label: string; value: string }> {
  const rows: Array<{ key: keyof AuditMetadata; label: string; value: string }> = []
  for (const key of Object.keys(metadataLabels) as Array<keyof AuditMetadata>) {
    const value = metadata[key]
    if (typeof value !== 'string' && typeof value !== 'number') continue
    rows.push({ key, label: metadataLabels[key], value: String(value) })
  }
  return rows
}

onBeforeMount(() => { void replace() })
onBeforeUnmount(() => {
  alive = false
  generation++
  replaceController?.abort()
  appendController?.abort()
})
</script>

<template>
  <section v-if="!isAdmin" class="denied" aria-labelledby="operations-audit-title">
    <h1 id="operations-audit-title">无权访问审计日志</h1>
    <p>此功能仅对管理员开放。</p>
  </section>
  <section v-else class="page" aria-labelledby="operations-audit-title">
    <header class="page-heading">
      <p class="eyebrow">系统运维</p>
      <h1 id="operations-audit-title">审计日志</h1>
      <p>按安全字段追踪管理操作。请求正文、IP 与未公开元数据不会显示。</p>
      <RouterLink to="/admin/settings">返回系统设置</RouterLink>
    </header>

    <form class="filters" @submit.prevent="applyFilters">
      <fieldset>
        <legend>筛选审计记录</legend>
        <label for="audit-action">操作
          <input id="audit-action" v-model="filters.action" data-testid="audit-action" placeholder="例如 operations.settings_updated">
        </label>
        <label for="audit-target-type">目标类型
          <input id="audit-target-type" v-model="filters.targetType" data-testid="audit-target-type" placeholder="例如 system_settings">
        </label>
        <label for="audit-outcome">结果
          <select id="audit-outcome" v-model="filters.outcome" data-testid="audit-outcome">
            <option value="">全部</option>
            <option value="succeeded">成功</option>
            <option value="failed">失败</option>
            <option value="rejected">已拒绝</option>
          </select>
        </label>
        <label for="audit-actor">操作人 ID
          <input id="audit-actor" v-model="filters.actorId" data-testid="audit-actor" placeholder="UUID">
        </label>
        <label for="audit-from">开始时间（RFC 3339）
          <input id="audit-from" v-model="filters.from" data-testid="audit-from" placeholder="2026-07-01T00:00:00Z">
        </label>
        <label for="audit-to">结束时间（RFC 3339）
          <input id="audit-to" v-model="filters.to" data-testid="audit-to" placeholder="2026-07-28T00:00:00Z">
        </label>
        <button type="submit" :disabled="loading">应用筛选</button>
      </fieldset>
    </form>

    <div
      v-if="error"
      class="feedback error"
      role="alert"
    >
      <p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button data-testid="audit-retry" type="button" :disabled="loading" @click="replace(true)">重试</button>
    </div>

    <section class="results" aria-labelledby="audit-results-title">
      <h2 id="audit-results-title" ref="resultsTitle" data-testid="audit-results-title" tabindex="-1">审计记录</h2>
      <p v-if="loading" role="status" aria-live="polite">正在加载审计日志…</p>
      <p v-else-if="!error && !items.length">暂无审计记录</p>
      <ol v-else-if="items.length" aria-labelledby="audit-results-title">
        <li v-for="item in items" :key="item.id" data-testid="audit-record" :data-id="item.id">
          <article>
            <header>
              <div>
                <strong>{{ item.action }}</strong>
                <span>{{ item.targetType }}</span>
              </div>
              <time :datetime="item.occurredAt">{{ new Date(item.occurredAt).toLocaleString('zh-CN') }}</time>
            </header>
            <dl>
              <div><dt>操作人</dt><dd>{{ item.actorId || '系统操作' }}</dd></div>
              <div><dt>目标</dt><dd>{{ item.targetId || '已隐藏' }}</dd></div>
              <div v-for="entry in safeMetadata(item.metadata)" :key="entry.key">
                <dt>{{ entry.label }}：</dt><dd>{{ entry.value }}</dd>
              </div>
            </dl>
          </article>
        </li>
      </ol>
      <div v-if="appendError" class="feedback error" role="alert">
        <p>{{ appendError }}<span v-if="appendRequestId">（支持编号：{{ appendRequestId }}）</span></p>
        <button type="button" :disabled="loadingMore" @click="loadMore(true)">重试加载更多</button>
      </div>
      <button
        v-if="nextBeforeId !== null && !error"
        data-testid="audit-load-more"
        class="load-more"
        type="button"
        :disabled="loading || loadingMore"
        @click="loadMore()"
      >{{ loadingMore ? '加载中…' : '加载更多' }}</button>
    </section>
  </section>
</template>

<style scoped>
.page{max-width:1120px}.page-heading{margin-bottom:22px}.page-heading h1{margin:.35rem 0;font-size:clamp(1.75rem,4vw,2.55rem)}.page-heading p:not(.eyebrow){color:#5b6b80}.page-heading a{color:#1269ad;font-weight:700}.eyebrow{margin:0;color:#1673b9;font-size:.84rem;font-weight:700;letter-spacing:.06em}.filters fieldset{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px;margin:0;padding:20px;border:1px solid #dbe4f0;border-radius:14px;background:#fff}.filters legend{padding:0 7px;color:#183b67;font-weight:800}.filters label{display:grid;gap:7px;color:#40536b;font-weight:700}.filters input,.filters select{box-sizing:border-box;width:100%;border:1px solid #b8c7d9;border-radius:8px;background:#fff;color:#172b47;padding:10px 11px;font:inherit}.filters button,.load-more,.feedback button{align-self:end;border:0;border-radius:8px;background:#176eb5;color:#fff;padding:11px 15px;font:inherit;font-weight:700;cursor:pointer}.filters button:disabled,.load-more:disabled,.feedback button:disabled{opacity:.55;cursor:not-allowed}.results{margin-top:26px}.results h2{outline:none}.results ol{display:grid;gap:14px;margin:0;padding:0;list-style:none}.results article{padding:18px 20px;border:1px solid #dbe4f0;border-radius:13px;background:#fff}.results article header{display:flex;align-items:start;justify-content:space-between;gap:18px}.results article header div{display:grid;gap:5px}.results article header strong{overflow-wrap:anywhere;color:#173d68}.results article header span,.results time{color:#68788d;font-size:.9rem}.results dl{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px 20px;margin:16px 0 0}.results dl div{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:8px}.results dt{color:#63738a}.results dd{margin:0;overflow-wrap:anywhere;color:#253a54}.feedback{margin:18px 0;padding:14px 16px;border:1px solid #efb3ae;border-radius:10px;background:#fff5f4;color:#862b25}.feedback p{margin:0 0 9px}.load-more{margin-top:18px}.denied{max-width:650px;padding:32px;border:1px solid #efc1be;border-radius:13px;background:#fff}@media(max-width:760px){.filters fieldset{grid-template-columns:1fr}.filters button{width:100%}.results article header{align-items:stretch;flex-direction:column}.results dl{grid-template-columns:1fr}.load-more{width:100%}}
</style>
