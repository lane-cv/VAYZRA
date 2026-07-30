<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeMount,
  onBeforeUnmount,
  ref,
} from 'vue'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import { uuidV4 } from '../../utils/uuid'
import {
  listBackups,
  queueBackup,
  readBackup,
} from './api'
import type {
  BackupCursor,
  BackupRun,
  BackupRunDetail,
  BackupState,
  BackupTrigger,
  RestoreVerification,
} from './types'

const session = useSessionStore()
const isAdmin = computed(() => session.user?.role === 'admin')
const items = ref<BackupRun[]>([])
const nextCursor = ref<BackupCursor | null>(null)
const selected = ref<BackupRunDetail>()
const loading = ref(false)
const loadingMore = ref(false)
const loadingDetail = ref(false)
const queueing = ref(false)
const queueDialog = ref(false)
const error = ref('')
const requestId = ref('')
const detailError = ref('')
const queueError = ref('')
const queueRequestId = ref('')
const notice = ref('')
const feedback = ref<HTMLElement>()
const queueFeedback = ref<HTMLElement>()
const noticeFeedback = ref<HTMLElement>()
const queueOpenButton = ref<HTMLButtonElement>()
const queueDialogElement = ref<HTMLDialogElement>()
const queueCancelButton = ref<HTMLButtonElement>()
let alive = true
let generation = 0
let queueKey = ''
let queueCloseFocus: 'trigger' | 'notice' = 'trigger'
let listController: AbortController | undefined
let detailController: AbortController | undefined

const stateLabels: Record<BackupState, string> = {
  queued: '等待执行',
  draining: '正在排空任务',
  snapshotting: '正在创建快照',
  encrypting: '正在加密',
  verifying: '正在校验',
  syncing: '正在同步远端',
  succeeded: '备份成功',
  degraded: '远端副本异常',
  failed: '备份失败',
}
const triggerLabels: Record<BackupTrigger, string> = {
  scheduled: '定时备份',
  manual: '手动备份',
  pre_release: '发布前备份',
}

function failure(reason: unknown, fallback: string) {
  return reason instanceof APIError
    ? { message: reason.message || fallback, requestId: reason.requestId }
    : { message: fallback, requestId: '' }
}

async function focus(element: typeof feedback) {
  await nextTick()
  element.value?.focus()
}

async function loadInitial() {
  if (!isAdmin.value) return
  const currentGeneration = ++generation
  listController?.abort()
  const controller = new AbortController()
  listController = controller
  loading.value = true
  error.value = ''
  requestId.value = ''
  notice.value = ''
  try {
    const page = await listBackups({ limit: 25 }, controller.signal)
    if (!alive || controller.signal.aborted || currentGeneration !== generation) return
    items.value = page.items
    nextCursor.value = page.next
  } catch (reason) {
    if (!alive || controller.signal.aborted || currentGeneration !== generation) return
    const details = failure(reason, '备份历史加载失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
    await focus(feedback)
  } finally {
    if (alive && currentGeneration === generation) {
      loading.value = false
      if (listController === controller) listController = undefined
    }
  }
}

async function loadMore() {
  const before = nextCursor.value
  if (!before || loadingMore.value) return
  const currentGeneration = generation
  const controller = new AbortController()
  listController = controller
  loadingMore.value = true
  error.value = ''
  requestId.value = ''
  try {
    const page = await listBackups({ limit: 25, before }, controller.signal)
    if (!alive || controller.signal.aborted || currentGeneration !== generation) return
    const known = new Set(items.value.map((run) => run.id))
    items.value.push(...page.items.filter((run) => !known.has(run.id)))
    nextCursor.value = page.next
  } catch (reason) {
    if (!alive || controller.signal.aborted || currentGeneration !== generation) return
    const details = failure(reason, '更多备份记录加载失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
    await focus(feedback)
  } finally {
    if (alive && currentGeneration === generation) {
      loadingMore.value = false
      if (listController === controller) listController = undefined
    }
  }
}

async function openDetail(run: BackupRun) {
  detailController?.abort()
  const controller = new AbortController()
  detailController = controller
  loadingDetail.value = true
  detailError.value = ''
  selected.value = undefined
  try {
    const value = await readBackup(run.id, controller.signal)
    if (!alive || controller.signal.aborted) return
    selected.value = value
  } catch (reason) {
    if (!alive || controller.signal.aborted) return
    detailError.value = failure(reason, '备份详情加载失败，请稍后重试').message
  } finally {
    if (alive && detailController === controller) {
      loadingDetail.value = false
      detailController = undefined
    }
  }
}

function supportsNativeDialog(element: HTMLDialogElement): boolean {
  return typeof element.showModal === 'function' && typeof element.close === 'function'
}

async function openQueueDialog() {
  queueKey = ''
  queueError.value = ''
  queueRequestId.value = ''
  notice.value = ''
  queueCloseFocus = 'trigger'
  queueDialog.value = true
  await nextTick()
  const element = queueDialogElement.value
  if (!element) return
  if (!element.open) {
    if (supportsNativeDialog(element)) element.showModal()
    else element.setAttribute('open', '')
  }
  await nextTick()
  queueCancelButton.value?.focus()
}

async function handleQueueDialogClosed() {
  const focusTarget = queueCloseFocus
  queueDialog.value = false
  queueKey = ''
  queueError.value = ''
  queueRequestId.value = ''
  queueCloseFocus = 'trigger'
  await nextTick()
  if (!alive) return
  if (focusTarget === 'notice') noticeFeedback.value?.focus()
  else queueOpenButton.value?.focus()
}

function closeQueueDialogElement(focusTarget: 'trigger' | 'notice') {
  queueCloseFocus = focusTarget
  const element = queueDialogElement.value
  if (!element) {
    void handleQueueDialogClosed()
    return
  }
  if (supportsNativeDialog(element)) {
    element.close()
    return
  }
  element.removeAttribute('open')
  void handleQueueDialogClosed()
}

function closeQueueDialog() {
  if (queueing.value) return
  closeQueueDialogElement('trigger')
}

async function submitQueue() {
  if (queueing.value) return
  if (!queueKey) queueKey = uuidV4()
  queueing.value = true
  queueError.value = ''
  queueRequestId.value = ''
  notice.value = ''
  try {
    const queued = await queueBackup(queueKey)
    if (!alive) return
    const index = items.value.findIndex((run) => run.id === queued.id)
    if (index >= 0) items.value[index] = queued
    else items.value.unshift(queued)
    notice.value = '手动备份已加入队列'
    closeQueueDialogElement('notice')
  } catch (reason) {
    if (!alive) return
    const details = failure(reason, '手动备份创建失败，请稍后重试')
    queueError.value = details.message
    queueRequestId.value = details.requestId
    await focus(queueFeedback)
  } finally {
    if (alive) queueing.value = false
  }
}

function formatTime(value?: string): string {
  if (!value) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'Asia/Shanghai',
  }).format(new Date(value))
}

function formatBytes(value?: number): string {
  if (value === undefined) return '—'
  if (value < 1024) return `${value} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let amount = value / 1024
  let unit = 0
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024
    unit++
  }
  return `${amount.toFixed(amount >= 10 ? 0 : 1)} ${units[unit]}`
}

function latestRestore(run: BackupRunDetail): RestoreVerification | undefined {
  return run.restoreVerifications[0]
}

function restoreLabel(value: RestoreVerification): string {
  switch (value.state) {
  case 'succeeded': return '恢复演练成功'
  case 'failed': return '恢复演练失败'
  case 'restoring': return '正在恢复'
  case 'checking': return '正在校验'
  default: return '等待恢复演练'
  }
}

function restoredRowCountTotal(value: RestoreVerification): number {
  return Object.values(value.databaseRowCounts).reduce((total, count) => total + count, 0)
}

onBeforeMount(() => { void loadInitial() })
onBeforeUnmount(() => {
  alive = false
  generation++
  listController?.abort()
  detailController?.abort()
})
</script>

<template>
  <section v-if="!isAdmin" class="denied" aria-labelledby="backups-title">
    <h1 id="backups-title">无权访问备份历史</h1>
    <p>此功能仅对管理员开放。</p>
  </section>
  <section v-else class="page" aria-labelledby="backups-title">
    <header class="page-heading">
      <div>
        <p class="eyebrow">系统运维</p>
        <h1 id="backups-title">备份与恢复记录</h1>
        <p>查看加密恢复点、远端副本状态与最近一次恢复演练结果。</p>
      </div>
      <button
        ref="queueOpenButton"
        data-testid="queue-open"
        class="primary"
        type="button"
        @click="openQueueDialog"
      >
        创建手动备份
      </button>
    </header>

    <p
      v-if="notice"
      ref="noticeFeedback"
      data-testid="queue-notice"
      class="feedback success"
      role="status"
      aria-live="polite"
      tabindex="-1"
    >
      {{ notice }}
    </p>
    <p v-if="loading && items.length === 0" role="status" aria-live="polite">
      正在加载备份历史…
    </p>
    <div
      v-if="error"
      ref="feedback"
      class="feedback error"
      role="alert"
      tabindex="-1"
    >
      <p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button
        v-if="items.length === 0"
        data-testid="retry-load"
        type="button"
        :disabled="loading"
        @click="loadInitial"
      >
        重试加载
      </button>
    </div>

    <div v-if="!loading && !error && items.length === 0" class="empty">
      <h2>还没有备份记录</h2>
      <p>可创建第一份手动备份；定时备份也会在这里显示。</p>
    </div>

    <div v-if="items.length" class="backup-grid" aria-label="备份记录">
      <article
        v-for="run in items"
        :key="run.id"
        data-testid="backup-card"
        class="backup-card"
        :class="`state-${run.state}`"
      >
        <div class="card-top">
          <div>
            <span class="trigger">{{ triggerLabels[run.trigger] }}</span>
            <h2>{{ stateLabels[run.state] }}</h2>
          </div>
          <span class="state-pill">{{ stateLabels[run.state] }}</span>
        </div>
        <p v-if="run.state === 'degraded'" class="warning">
          本地恢复点可用，远端副本未能完成，请关注后续重试。
        </p>
        <dl>
          <div><dt>请求时间</dt><dd>{{ formatTime(run.requestedAt) }}</dd></div>
          <div><dt>原始数据</dt><dd>{{ formatBytes(run.logicalBytes) }}</dd></div>
          <div><dt>加密存储</dt><dd>{{ formatBytes(run.storedBytes) }}</dd></div>
          <div><dt>本地到期</dt><dd>{{ formatTime(run.localExpiresAt) }}</dd></div>
        </dl>
        <button data-testid="open-detail" type="button" @click="openDetail(run)">
          查看恢复证据
        </button>
      </article>
    </div>

    <button
      v-if="nextCursor"
      data-testid="load-more"
      class="load-more"
      type="button"
      :disabled="loadingMore"
      @click="loadMore"
    >
      {{ loadingMore ? '正在加载…' : '加载更早记录' }}
    </button>

    <section
      v-if="loadingDetail || detailError || selected"
      data-testid="backup-detail"
      class="detail"
      aria-labelledby="backup-detail-title"
    >
      <h2 id="backup-detail-title">恢复点详情</h2>
      <p v-if="loadingDetail" role="status">正在加载恢复证据…</p>
      <p v-else-if="detailError" role="alert">{{ detailError }}</p>
      <template v-else-if="selected">
        <div class="artifact-grid">
          <article v-for="artifact in selected.artifacts" :key="`${artifact.repository}-${artifact.kind}`">
            <span>{{ artifact.repository === 'local' ? '本地' : '远端' }}</span>
            <strong>{{ artifact.kind === 'database_dump' ? '数据库' : artifact.kind === 'object_snapshot' ? '对象快照' : artifact.kind === 'manifest' ? '清单' : '恢复报告' }}</strong>
            <p>{{ formatBytes(artifact.sizeBytes) }} · {{ formatTime(artifact.verifiedAt) }} 已校验</p>
            <p>{{ formatTime(artifact.expiresAt) }} 到期</p>
          </article>
        </div>
        <article v-if="latestRestore(selected)" class="restore-result">
          <h3>{{ restoreLabel(latestRestore(selected)!) }}</h3>
          <p v-if="latestRestore(selected)!.restoredMigrationVersion !== undefined">
            数据库迁移版本：{{ latestRestore(selected)!.restoredMigrationVersion }}
          </p>
          <p>固定表行数合计：{{ restoredRowCountTotal(latestRestore(selected)!) }}</p>
          <p v-if="latestRestore(selected)!.rtoSeconds !== undefined">
            恢复目标耗时：{{ latestRestore(selected)!.rtoSeconds }} 秒
          </p>
          <p>
            对象检查 {{ latestRestore(selected)!.checkedObjectCount }} 个；
            缺失 {{ latestRestore(selected)!.missingObjectCount }} 个；
            异常 {{ latestRestore(selected)!.unexpectedObjectCount }} 个。
          </p>
          <p>
            {{ latestRestore(selected)!.sessionRevocationVerified ? '会话已撤销' : '会话撤销未验证' }}
          </p>
        </article>
        <p v-else class="empty-inline">尚未记录恢复演练。</p>
      </template>
    </section>

    <dialog
      v-if="queueDialog"
      ref="queueDialogElement"
      role="dialog"
      aria-labelledby="queue-title"
      class="dialog"
      @cancel.prevent="closeQueueDialog"
      @close="handleQueueDialogClosed"
      @keydown.esc.prevent.stop="closeQueueDialog"
    >
      <h2 id="queue-title">创建手动备份</h2>
      <p>备份会进入一个短暂维护窗口；进行中的任务将先安全排空。</p>
      <p>此操作只创建恢复点，不会恢复或覆盖当前数据。</p>
      <div
        v-if="queueError"
        ref="queueFeedback"
        class="feedback error"
        role="alert"
        tabindex="-1"
      >
        {{ queueError }}<span v-if="queueRequestId">（支持编号：{{ queueRequestId }}）</span>
      </div>
      <div class="dialog-actions">
        <button
          ref="queueCancelButton"
          data-testid="queue-cancel"
          type="button"
          :disabled="queueing"
          @click="closeQueueDialog"
        >
          取消
        </button>
        <button
          v-if="queueError"
          data-testid="queue-retry"
          class="primary"
          type="button"
          :disabled="queueing"
          @click="submitQueue"
        >
          {{ queueing ? '正在重试…' : '重试创建' }}
        </button>
        <button
          v-else
          data-testid="queue-confirm"
          class="primary"
          type="button"
          :disabled="queueing"
          @click="submitQueue"
        >
          {{ queueing ? '正在创建…' : '确认创建' }}
        </button>
      </div>
    </dialog>
  </section>
</template>

<style scoped>
.page{max-width:1120px}.page-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:24px;margin-bottom:28px}.eyebrow{margin:0;color:#1673b9;font-weight:800;letter-spacing:.06em}.page-heading h1{margin:.45rem 0;font-size:clamp(1.8rem,4vw,2.7rem)}.page-heading p{color:#58667c}.primary,.backup-card button,.load-more,.dialog button{border:1px solid #b9cce0;border-radius:9px;background:#fff;color:#234766;padding:10px 14px;font:inherit;font-weight:700;cursor:pointer}.primary{border-color:#176eb5;background:#176eb5;color:#fff}.primary:disabled,.backup-card button:disabled,.load-more:disabled,.dialog button:disabled{cursor:wait;opacity:.65}.backup-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:18px}.backup-card{padding:22px;border:1px solid #dbe4f0;border-radius:14px;background:#fff;box-shadow:0 10px 28px #173a5d0a}.backup-card.state-degraded{border-color:#efc16e}.backup-card.state-failed{border-color:#e3aaa5}.card-top{display:flex;align-items:flex-start;justify-content:space-between;gap:16px}.trigger{color:#6a788c;font-size:.85rem}.card-top h2{margin:.35rem 0;font-size:1.25rem}.state-pill{border-radius:999px;background:#e9f3fb;color:#1766a4;padding:5px 9px;font-size:.78rem;font-weight:800}.state-degraded .state-pill{background:#fff2d6;color:#8a5b00}.state-failed .state-pill{background:#fde9e7;color:#a23c35}.warning{padding:10px 12px;border-radius:9px;background:#fff8e8;color:#78530d;line-height:1.55}.backup-card dl{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px;margin:18px 0}.backup-card dl div{min-width:0}.backup-card dt{color:#748297;font-size:.78rem}.backup-card dd{margin:4px 0 0;color:#213e60;font-weight:700}.feedback{margin:16px 0;padding:14px;border-radius:10px}.feedback.error{background:#fff0ef;color:#8e302b}.feedback.success{background:#eaf8ef;color:#176b37}.empty,.detail{margin-top:18px;padding:28px;border:1px solid #dbe4f0;border-radius:14px;background:#fff}.load-more{display:block;margin:22px auto}.artifact-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.artifact-grid article,.restore-result{padding:16px;border-radius:11px;background:#f3f7fb}.artifact-grid span{display:block;color:#5b7189;font-size:.78rem}.artifact-grid strong{display:block;margin:5px 0}.artifact-grid p,.restore-result p{color:#52667b}.restore-result{margin-top:14px}.restore-result h3{margin-top:0}.dialog{width:min(480px,calc(100% - 36px));padding:24px;border:0;border-radius:14px;background:#fff;box-shadow:0 24px 70px #08172666}.dialog::backdrop{background:#0b223a99}.dialog h2{margin-top:0}.dialog p{color:#52667b;line-height:1.6}.dialog-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:22px}.denied{max-width:680px}@media(max-width:760px){.page-heading{align-items:stretch;flex-direction:column}.backup-grid,.artifact-grid{grid-template-columns:1fr}.backup-card dl{grid-template-columns:1fr 1fr}.page-heading .primary{width:100%}}@media(max-width:420px){.backup-card dl{grid-template-columns:1fr}.dialog-actions{align-items:stretch;flex-direction:column-reverse}.dialog-actions button{width:100%}}
</style>
