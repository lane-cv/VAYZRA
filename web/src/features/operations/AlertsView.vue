<script setup lang="ts">
import {
  nextTick,
  onBeforeMount,
  onBeforeUnmount,
  onMounted,
  reactive,
  ref,
} from 'vue'
import { APIError } from '../../api/client'
import { acknowledgeAlert, listAlerts } from './api'
import type {
  AlertCategory,
  AlertFilters,
  AlertSeverity,
  AlertState,
  OperationalAlert,
} from './types'

const PAGE_SIZE = 50
const POLL_MILLISECONDS = 60_000
const filters = reactive({
  state: '' as '' | AlertState,
  severity: '' as '' | AlertSeverity,
  category: '' as '' | AlertCategory,
})
const activeFilters = ref<AlertFilters>({})
const items = ref<OperationalAlert[]>([])
const nextCursor = ref<string | null>(null)
const loading = ref(false)
const loadingMore = ref(false)
const error = ref('')
const requestId = ref('')
const appendError = ref('')
const appendRequestId = ref('')
const acknowledgementError = ref('')
const acknowledgementRequestId = ref('')
const acknowledging = ref(false)
const pendingAcknowledgement = ref<OperationalAlert>()
const resultsTitle = ref<HTMLElement>()
const acknowledgementDialog = ref<HTMLDialogElement>()
const acknowledgementCancel = ref<HTMLButtonElement>()
const acknowledgementFeedback = ref<HTMLElement>()
let alive = true
let generation = 0
let loadedPageCount = 1
let replaceController: AbortController | undefined
let appendController: AbortController | undefined
let polling: ReturnType<typeof setInterval> | undefined
let acknowledgementReturnID = ''

const severityLabels: Record<AlertSeverity, string> = {
  warning: '警告',
  critical: '严重',
}
const stateLabels: Record<AlertState, string> = {
  open: '未处理',
  acknowledged: '已确认',
  resolved: '已解决',
}
const categoryLabels: Record<AlertCategory, string> = {
  storage: '存储',
  backup: '备份',
  ai: 'AI',
  processing: '任务处理',
  security: '安全',
}

function failure(reason: unknown, fallback: string) {
  return reason instanceof APIError
    ? { message: reason.message || fallback, requestId: reason.requestId }
    : { message: fallback, requestId: '' }
}

function matchesActiveFilters(alert: OperationalAlert): boolean {
  const active = activeFilters.value
  return (
    (active.state === undefined || alert.state === active.state)
    && (active.severity === undefined || alert.severity === active.severity)
    && (active.category === undefined || alert.category === active.category)
  )
}

function uniqueAlerts(incoming: OperationalAlert[]): OperationalAlert[] {
  const seen = new Set<string>()
  return incoming.filter((item) => {
    if (seen.has(item.id)) return false
    seen.add(item.id)
    return true
  })
}

function replaceItems(incoming: OperationalAlert[]) {
  items.value = uniqueAlerts(incoming).filter(matchesActiveFilters)
}

function appendItems(incoming: OperationalAlert[]) {
  const received = uniqueAlerts(incoming)
  const updates = new Map(received.map((item) => [item.id, item]))
  items.value = items.value
    .map((item) => updates.get(item.id) ?? item)
    .filter(matchesActiveFilters)
  const known = new Set(items.value.map((item) => item.id))
  for (const item of received) {
    if (!matchesActiveFilters(item) || known.has(item.id)) continue
    known.add(item.id)
    items.value.push(item)
  }
}

async function focusResults() {
  await nextTick()
  resultsTitle.value?.focus()
}

async function replace(restoreFocus = false) {
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
  let loaded = false
  try {
    const page = await listAlerts(
      { ...activeFilters.value, limit: PAGE_SIZE },
      controller.signal,
    )
    if (!alive || controller.signal.aborted || requestGeneration !== generation) return
    replaceItems(page.items)
    nextCursor.value = page.next
    loadedPageCount = 1
    loaded = true
  } catch (reason) {
    if (!alive || controller.signal.aborted || requestGeneration !== generation) return
    const details = failure(reason, '告警加载失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
    items.value = []
    nextCursor.value = null
  } finally {
    if (alive && requestGeneration === generation) {
      loading.value = false
      if (replaceController === controller) replaceController = undefined
    }
  }
  if (loaded && restoreFocus && alive && requestGeneration === generation) {
    await focusResults()
  }
}

async function refreshLive() {
  if (
    document.visibilityState !== 'visible'
    || loading.value
    || loadingMore.value
    || acknowledging.value
    || replaceController !== undefined
  ) return
  const requestGeneration = generation
  const pagesToLoad = loadedPageCount
  const requestFilters = { ...activeFilters.value }
  const controller = new AbortController()
  replaceController = controller
  try {
    const refreshed: OperationalAlert[] = []
    let cursor: string | null = null
    let pagesLoaded = 0
    for (let pageIndex = 0; pageIndex < pagesToLoad; pageIndex++) {
      const page = await listAlerts(
        {
          ...requestFilters,
          ...(cursor ? { before: cursor } : {}),
          limit: PAGE_SIZE,
        },
        controller.signal,
      )
      refreshed.push(...page.items)
      cursor = page.next
      pagesLoaded++
      if (!cursor) break
    }
    if (!alive || controller.signal.aborted || requestGeneration !== generation) return
    replaceItems(refreshed)
    nextCursor.value = cursor
    loadedPageCount = pagesLoaded
  } catch {
    // Background refresh keeps the last explicit state and never hides loaded alerts.
  } finally {
    if (replaceController === controller) replaceController = undefined
  }
}

async function loadMore(restoreFocus = false) {
  const before = nextCursor.value
  if (
    !before
    || loading.value
    || loadingMore.value
    || acknowledging.value
    || replaceController !== undefined
  ) return
  appendController?.abort()
  const controller = new AbortController()
  appendController = controller
  const requestGeneration = generation
  loadingMore.value = true
  appendError.value = ''
  appendRequestId.value = ''
  try {
    const page = await listAlerts(
      { ...activeFilters.value, before, limit: PAGE_SIZE },
      controller.signal,
    )
    if (!alive || controller.signal.aborted || requestGeneration !== generation) return
    appendItems(page.items)
    nextCursor.value = page.next
    loadedPageCount++
  } catch (reason) {
    if (!alive || controller.signal.aborted || requestGeneration !== generation) return
    const details = failure(reason, '更多告警加载失败，请稍后重试')
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

function submittedFilters(): AlertFilters {
  const result: AlertFilters = {}
  if (filters.state) result.state = filters.state
  if (filters.severity) result.severity = filters.severity
  if (filters.category) result.category = filters.category
  return result
}

function applyFilters() {
  activeFilters.value = submittedFilters()
  void replace()
}

function supportsNativeDialog(element: HTMLDialogElement): boolean {
  return typeof element.showModal === 'function' && typeof element.close === 'function'
}

async function openAcknowledgement(alert: OperationalAlert) {
  acknowledgementError.value = ''
  acknowledgementRequestId.value = ''
  acknowledgementReturnID = alert.id
  pendingAcknowledgement.value = alert
  await nextTick()
  const dialog = acknowledgementDialog.value
  if (dialog && !dialog.open) {
    if (supportsNativeDialog(dialog)) dialog.showModal()
    else dialog.setAttribute('open', '')
  }
  await nextTick()
  acknowledgementCancel.value?.focus()
}

async function focusAfterAcknowledgement(target: 'trigger' | 'alert' | 'results') {
  await nextTick()
  const selector = target === 'trigger'
    ? `[data-acknowledge="${acknowledgementReturnID}"]`
    : `[data-id="${acknowledgementReturnID}"]`
  const element = target === 'results'
    ? resultsTitle.value
    : document.querySelector<HTMLElement>(selector)
  const focusTarget = element ?? resultsTitle.value
  focusTarget?.focus()
}

async function dismissAcknowledgement(target: 'trigger' | 'alert' | 'results') {
  const dialog = acknowledgementDialog.value
  if (dialog?.open && supportsNativeDialog(dialog)) dialog.close()
  else dialog?.removeAttribute('open')
  pendingAcknowledgement.value = undefined
  await focusAfterAcknowledgement(target)
}

function closeAcknowledgement() {
  if (acknowledging.value) return
  void dismissAcknowledgement('trigger')
}

async function confirmAcknowledgement() {
  const selected = pendingAcknowledgement.value
  if (!selected || acknowledging.value) return
  const invalidateReads = () => {
    replaceController?.abort()
    appendController?.abort()
    replaceController = undefined
    appendController = undefined
    loading.value = false
    loadingMore.value = false
    return ++generation
  }
  const mutationGeneration = invalidateReads()
  acknowledging.value = true
  acknowledgementError.value = ''
  acknowledgementRequestId.value = ''
  let refreshAfterMutation = false
  let focusMutationError = false
  try {
    const updated = await acknowledgeAlert(selected.id)
    if (!alive || mutationGeneration !== generation) return
    const remainsVisible = matchesActiveFilters(updated)
    const index = items.value.findIndex((item) => item.id === updated.id)
    if (index >= 0 && remainsVisible) items.value[index] = updated
    else if (index >= 0) {
      items.value.splice(index, 1)
    }
    await dismissAcknowledgement(remainsVisible ? 'alert' : 'results')
  } catch (reason) {
    if (!alive || mutationGeneration !== generation) return
    const details = failure(reason, '告警确认失败，请稍后重试')
    acknowledgementError.value = details.message
    acknowledgementRequestId.value = details.requestId
    if (
      reason instanceof APIError
      && (
        reason.status === 404
        || reason.status === 409
        || reason.code === 'alert_not_found'
        || reason.code === 'alert_already_resolved'
      )
    ) {
      items.value = items.value.filter((item) => item.id !== selected.id)
      refreshAfterMutation = true
    }
    focusMutationError = true
  } finally {
    if (alive) {
      acknowledging.value = false
      invalidateReads()
    }
  }
  if (refreshAfterMutation && alive) await refreshLive()
  if (focusMutationError && alive) {
    await nextTick()
    acknowledgementFeedback.value?.focus()
  }
}

function formatTime(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'Asia/Shanghai',
  }).format(new Date(value))
}

function formatValue(value: number): string {
  return new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value)
}

function handleVisibilityChange() {
  if (document.visibilityState === 'visible') void refreshLive()
}

onBeforeMount(() => { void replace() })
onMounted(() => {
  polling = setInterval(() => { void refreshLive() }, POLL_MILLISECONDS)
  document.addEventListener('visibilitychange', handleVisibilityChange)
})
onBeforeUnmount(() => {
  alive = false
  generation++
  replaceController?.abort()
  appendController?.abort()
  if (polling !== undefined) clearInterval(polling)
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})
</script>

<template>
  <section class="alerts-page" aria-labelledby="alerts-title">
    <header class="page-heading">
      <div>
        <p class="eyebrow">系统运维</p>
        <h1 id="alerts-title">告警中心</h1>
        <p>查看聚合运行信号；确认告警不会停止持续评估。</p>
      </div>
      <RouterLink to="/admin">返回仪表盘</RouterLink>
    </header>

    <form data-testid="alert-filters" class="filters" @submit.prevent="applyFilters">
      <fieldset>
        <legend>筛选告警</legend>
        <label>状态
          <select v-model="filters.state" data-testid="alert-state-filter">
            <option value="">全部</option>
            <option value="open">未处理</option>
            <option value="acknowledged">已确认</option>
            <option value="resolved">已解决</option>
          </select>
        </label>
        <label>级别
          <select v-model="filters.severity" data-testid="alert-severity-filter">
            <option value="">全部</option>
            <option value="critical">严重</option>
            <option value="warning">警告</option>
          </select>
        </label>
        <label>类别
          <select v-model="filters.category" data-testid="alert-category-filter">
            <option value="">全部</option>
            <option value="storage">存储</option>
            <option value="backup">备份</option>
            <option value="ai">AI</option>
            <option value="processing">任务处理</option>
            <option value="security">安全</option>
          </select>
        </label>
        <button type="submit" :disabled="loading">应用筛选</button>
      </fieldset>
    </form>

    <div v-if="error" class="feedback error" role="alert">
      <p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button data-testid="alerts-retry" type="button" :disabled="loading" @click="replace(true)">重试</button>
    </div>

    <section class="results" aria-labelledby="alerts-results-title">
      <h2
        id="alerts-results-title"
        ref="resultsTitle"
        data-testid="alerts-results-title"
        tabindex="-1"
      >告警记录</h2>
      <p v-if="loading && !items.length" role="status" aria-live="polite">正在加载告警…</p>
      <p v-else-if="!error && !items.length">当前筛选条件下没有告警</p>
      <ol v-else class="alert-list">
        <li v-for="alert in items" :key="alert.id">
          <article
            class="alert-card"
            :class="[alert.severity, alert.state]"
            data-testid="alert-card"
            :data-id="alert.id"
            tabindex="-1"
          >
            <header>
              <div>
                <span class="severity">{{ severityLabels[alert.severity] }}</span>
                <span class="state">{{ stateLabels[alert.state] }}</span>
                <span class="category">{{ categoryLabels[alert.category] }}</span>
              </div>
              <button
                v-if="alert.state === 'open'"
                type="button"
                :data-acknowledge="alert.id"
                @click="openAcknowledgement(alert)"
              >确认告警</button>
            </header>
            <h3>{{ alert.summary }}</h3>
            <p>当前值 {{ formatValue(alert.currentValue) }} · 阈值 {{ formatValue(alert.thresholdValue) }}</p>
            <dl class="timeline">
              <div><dt>首次发现</dt><dd><time :datetime="alert.firstObservedAt">{{ formatTime(alert.firstObservedAt) }}</time></dd></div>
              <div><dt>最近观测</dt><dd><time :datetime="alert.lastObservedAt">{{ formatTime(alert.lastObservedAt) }}</time></dd></div>
              <div v-if="alert.acknowledgedAt"><dt>确认时间</dt><dd><time :datetime="alert.acknowledgedAt">{{ formatTime(alert.acknowledgedAt) }}</time></dd></div>
              <div v-if="alert.resolvedAt"><dt>解决时间</dt><dd><time :datetime="alert.resolvedAt">{{ formatTime(alert.resolvedAt) }}</time></dd></div>
            </dl>
          </article>
        </li>
      </ol>

      <div v-if="appendError" class="feedback error" role="alert">
        <p>{{ appendError }}<span v-if="appendRequestId">（支持编号：{{ appendRequestId }}）</span></p>
        <button type="button" :disabled="loadingMore" @click="loadMore(true)">重试加载更多</button>
      </div>
      <button
        v-if="nextCursor && !error"
        data-testid="alerts-load-more"
        type="button"
        :disabled="loadingMore || acknowledging || replaceController !== undefined"
        @click="loadMore()"
      >{{ loadingMore ? '加载中…' : '加载更多' }}</button>
    </section>

    <dialog
      v-if="pendingAcknowledgement"
      ref="acknowledgementDialog"
      data-testid="acknowledge-dialog"
      aria-labelledby="acknowledge-dialog-title"
      @cancel.prevent="closeAcknowledgement"
    >
      <h2 id="acknowledge-dialog-title">确认这条告警？</h2>
      <p>{{ pendingAcknowledgement.summary }}</p>
      <p>确认只记录教师已知悉，告警仍会继续评估，直至系统恢复健康。</p>
      <div
        v-if="acknowledgementError"
        ref="acknowledgementFeedback"
        class="feedback error"
        role="alert"
        data-testid="acknowledge-error"
        tabindex="-1"
      >
        {{ acknowledgementError }}
        <span v-if="acknowledgementRequestId">（支持编号：{{ acknowledgementRequestId }}）</span>
      </div>
      <div class="dialog-actions">
        <button
          ref="acknowledgementCancel"
          data-testid="cancel-acknowledge"
          type="button"
          :disabled="acknowledging"
          @click="closeAcknowledgement"
        >取消</button>
        <button
          data-testid="confirm-acknowledge"
          type="button"
          :disabled="acknowledging"
          @click="confirmAcknowledgement"
        >{{ acknowledging ? '正在确认…' : '确认告警' }}</button>
      </div>
    </dialog>
  </section>
</template>

<style scoped>
.alerts-page{max-width:1080px;margin:0 auto}.page-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:20px}.page-heading h1{margin:.45rem 0;font-size:clamp(2rem,4vw,2.8rem)}.page-heading p{color:var(--hl-text-muted)}.page-heading a{border-radius:8px;background:var(--hl-primary);color:#fff;padding:9px 13px;font-weight:700;text-decoration:none}.eyebrow{margin:0;color:var(--hl-primary-strong);font-size:.8rem;font-weight:800;letter-spacing:.08em}.filters{margin:26px 0}.filters fieldset{display:flex;align-items:end;gap:14px;margin:0;padding:18px;border:1px solid var(--hl-border);border-radius:14px;background:var(--hl-surface-solid)}.filters legend{padding:0 7px;font-weight:800}.filters label{display:grid;gap:6px;color:var(--hl-text-muted);font-weight:700}.filters select{min-width:150px;border:1px solid var(--hl-border-strong);border-radius:8px;background:var(--hl-surface-solid);color:var(--hl-text);padding:9px;font:inherit}.filters button,.results>button,.alert-card button,.dialog-actions button,.feedback button{border:0;border-radius:8px;background:var(--hl-primary);color:#fff;padding:9px 13px;font:inherit;font-weight:700;cursor:pointer}.filters button:disabled,.results>button:disabled,.alert-card button:disabled,.dialog-actions button:disabled,.feedback button:disabled{opacity:.65;cursor:wait}.feedback{margin:16px 0;padding:14px;border-radius:10px}.feedback.error{border:1px solid #e4b7b3;background:#fff5f4;color:#8d2e29}.alert-list{display:grid;gap:14px;margin:16px 0;padding:0;list-style:none}.alert-card{padding:20px;border:1px solid var(--hl-border);border-left:5px solid #d89a31;border-radius:13px;background:var(--hl-surface-solid)}.alert-card.critical{border-left-color:#b53b34}.alert-card.resolved{border-left-color:#5b8d70}.alert-card header{display:flex;align-items:center;justify-content:space-between;gap:14px}.alert-card header>div{display:flex;flex-wrap:wrap;gap:8px}.severity,.state,.category{border-radius:999px;background:var(--hl-surface-muted);padding:5px 9px;color:var(--hl-text);font-size:.82rem;font-weight:800}.critical .severity{background:#ffe9e7;color:#9b2923}.warning .severity{background:#fff3d8;color:#81530e}.alert-card h3{margin:18px 0 8px}.alert-card>p{color:var(--hl-text-muted)}.timeline{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px;margin:16px 0 0}.timeline div{padding:10px;border-radius:8px;background:var(--hl-surface-muted)}.timeline dt{color:var(--hl-text-muted);font-size:.82rem}.timeline dd{margin:5px 0 0;font-weight:700}.results>button{margin-top:4px}dialog{max-width:min(520px,calc(100vw - 40px));border:1px solid var(--hl-border);border-radius:14px;background:var(--hl-surface-solid);color:var(--hl-text);box-shadow:var(--hl-shadow)}dialog::backdrop{background:#102b4d99}.dialog-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:20px}.dialog-actions button:first-child{border:1px solid var(--hl-border-strong);background:var(--hl-surface-solid);color:var(--hl-text)}@media(max-width:760px){.page-heading,.filters fieldset,.alert-card header{align-items:stretch;flex-direction:column}.filters select{box-sizing:border-box;width:100%}.timeline{grid-template-columns:1fr}.alert-card{padding:16px}.alert-card button{width:100%}}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto}}
</style>
