<script setup lang="ts">
import { nextTick, onBeforeMount, onBeforeUnmount, onMounted, ref } from 'vue'
import { APIError } from '../../api/client'
import { readDashboard } from '../operations/api'
import type {
  DashboardAuditCategory,
  DashboardAuditOutcome,
  DashboardQueue,
  DashboardService,
  OperationsDashboard,
  OperationsDataState,
  RecoveryState,
} from '../operations/types'

const POLL_MILLISECONDS = 60_000
const dashboard = ref<OperationsDashboard>()
const loading = ref(false)
const error = ref('')
const requestId = ref('')
const title = ref<HTMLElement>()
let alive = true
let generation = 0
let controller: AbortController | undefined
let polling: ReturnType<typeof setInterval> | undefined

const stateLabels: Record<OperationsDataState, string> = {
  healthy: '正常',
  degraded: '需关注',
  unavailable: '未知（不可用）',
  stale: '数据陈旧',
  timeout: '未知（超时）',
  empty: '暂无数据',
}
const recoveryLabels: Record<RecoveryState, string> = {
  succeeded: '成功',
  degraded: '部分成功',
  failed: '失败',
  empty: '暂无记录',
}
const serviceLabels: Record<DashboardService, string> = {
  app: '应用服务',
  caddy: '入口服务',
  postgres: 'PostgreSQL',
  redis: 'Redis',
  object_store: '对象存储',
  worker: '任务处理器',
}
const queueLabels: Record<DashboardQueue, string> = {
  processing: '文件处理',
  ai: 'AI 任务',
  outbox: '消息投递',
}
const auditCategoryLabels: Record<DashboardAuditCategory, string> = {
  authentication: '认证',
  authorization: '授权',
  files: '文件',
  teaching: '教学',
  ai: 'AI',
  operations: '运维',
  backup: '备份',
}
const auditOutcomeLabels: Record<DashboardAuditOutcome, string> = {
  succeeded: '成功',
  failed: '失败',
  denied: '已拒绝',
  rejected: '未接受',
}

function failure(reason: unknown) {
  return reason instanceof APIError
    ? { message: reason.message || '仪表盘加载失败，请稍后重试', requestId: reason.requestId }
    : { message: '仪表盘加载失败，请稍后重试', requestId: '' }
}

async function load(restoreFocus = false) {
  const requestGeneration = ++generation
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = ''
  requestId.value = ''
  let loaded = false
  try {
    const value = await readDashboard(current.signal)
    if (!alive || current.signal.aborted || requestGeneration !== generation) return
    dashboard.value = value
    loaded = true
  } catch (reason) {
    if (!alive || current.signal.aborted || requestGeneration !== generation) return
    const details = failure(reason)
    error.value = details.message
    requestId.value = details.requestId
  } finally {
    if (alive && requestGeneration === generation) {
      loading.value = false
      if (controller === current) controller = undefined
    }
  }
  if (loaded && restoreFocus && alive && requestGeneration === generation) {
    await nextTick()
    title.value?.focus()
  }
}

function refreshWhenVisible() {
  if (document.visibilityState === 'visible') void load()
}

function handleVisibilityChange() {
  if (document.visibilityState === 'visible') void load()
}

function formatTime(value?: string): string {
  if (!value) return '尚无观测时间'
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'short',
    timeZone: 'Asia/Shanghai',
  }).format(new Date(value))
}

function formatBytes(value: number): string {
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

function formatAge(seconds: number): string {
  if (seconds < 60) return `${seconds} 秒`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`
  return `${Math.floor(seconds / 3600)} 小时`
}

function stateClass(state: OperationsDataState | RecoveryState): string {
  if (state === 'healthy' || state === 'succeeded') return 'good'
  if (state === 'degraded' || state === 'stale') return 'warning'
  if (state === 'failed') return 'danger'
  return 'unknown'
}

function hasObservedValue(state: OperationsDataState): boolean {
  return state === 'healthy' || state === 'degraded' || state === 'stale'
}

function hasDependencyEvidence(state: OperationsDataState): boolean {
  return state !== 'unavailable' && state !== 'timeout'
}

function storagePercent(value: OperationsDashboard['storage']): string {
  if (!hasObservedValue(value.state) || value.capacityBytes === 0) return '—'
  return `${Math.round((value.usedBytes / value.capacityBytes) * 100)}%`
}

onBeforeMount(() => { void load() })
onMounted(() => {
  polling = setInterval(refreshWhenVisible, POLL_MILLISECONDS)
  document.addEventListener('visibilitychange', handleVisibilityChange)
})
onBeforeUnmount(() => {
  alive = false
  generation++
  controller?.abort()
  if (polling !== undefined) clearInterval(polling)
  document.removeEventListener('visibilitychange', handleVisibilityChange)
})
</script>

<template>
  <section class="home" aria-labelledby="admin-home-title">
    <header class="page-heading">
      <div>
        <p class="eyebrow">教师工作台 · 系统运维</p>
        <h1
          id="admin-home-title"
          ref="title"
          data-testid="dashboard-title"
          tabindex="-1"
        >运行仪表盘</h1>
        <p class="lead">学习服务与恢复能力的安全聚合视图。</p>
      </div>
      <p v-if="dashboard" class="observed">
        总览观测于
        <time data-testid="dashboard-observed-at" :datetime="dashboard.observedAt">
          {{ formatTime(dashboard.observedAt) }}
        </time>
      </p>
    </header>

    <p v-if="loading && !dashboard" role="status" aria-live="polite">正在加载运行状态…</p>
    <div v-if="error" class="feedback error" role="alert">
      <p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button data-testid="dashboard-retry" type="button" :disabled="loading" @click="load(true)">重试</button>
    </div>

    <div v-if="dashboard" class="dashboard-grid" :aria-busy="loading">
      <section class="panel alerts" data-mobile-section="alerts" data-testid="alert-summary">
        <header>
          <div><p class="kicker">优先处理</p><h2>当前告警</h2></div>
          <RouterLink to="/admin/alerts">进入告警中心</RouterLink>
        </header>
        <div class="alert-counts">
          <div class="critical"><span>严重</span><strong>{{ hasObservedValue(dashboard.alerts.state) ? dashboard.alerts.openCritical : '—' }}</strong></div>
          <div class="warning-count"><span>警告</span><strong>{{ hasObservedValue(dashboard.alerts.state) ? dashboard.alerts.openWarning : '—' }}</strong></div>
        </div>
        <p class="state-line" :class="stateClass(dashboard.alerts.state)">
          {{ stateLabels[dashboard.alerts.state] }} · 观测于 {{ formatTime(dashboard.alerts.observedAt) }}
        </p>
      </section>

      <section class="panel backup" data-mobile-section="backup" data-testid="backup-summary">
        <header>
          <div><p class="kicker">恢复能力</p><h2>备份与恢复</h2></div>
          <RouterLink to="/admin/backups">查看备份记录</RouterLink>
        </header>
        <p
          data-testid="backup-state"
          class="state-line"
          :class="stateClass(dashboard.backup.state)"
        >
          {{ stateLabels[dashboard.backup.state] }}
          <template v-if="dashboard.backup.observedAt">
            · 观测于
            <time :datetime="dashboard.backup.observedAt">{{ formatTime(dashboard.backup.observedAt) }}</time>
          </template>
        </p>
        <div class="recovery-points">
          <article>
            <span>本地恢复点</span>
            <strong :class="stateClass(dashboard.backup.local.state)">
              {{ hasDependencyEvidence(dashboard.backup.state) ? recoveryLabels[dashboard.backup.local.state] : '—' }}
            </strong>
            <time v-if="hasDependencyEvidence(dashboard.backup.state) && dashboard.backup.local.completedAt" :datetime="dashboard.backup.local.completedAt">
              {{ formatTime(dashboard.backup.local.completedAt) }}
            </time>
          </article>
          <article>
            <span>远端恢复点</span>
            <strong :class="stateClass(dashboard.backup.remote.state)">
              {{ hasDependencyEvidence(dashboard.backup.state) ? recoveryLabels[dashboard.backup.remote.state] : '—' }}
            </strong>
            <time v-if="hasDependencyEvidence(dashboard.backup.state) && dashboard.backup.remote.completedAt" :datetime="dashboard.backup.remote.completedAt">
              {{ formatTime(dashboard.backup.remote.completedAt) }}
            </time>
          </article>
          <article data-testid="restore-evidence">
            <span>最近恢复验证</span>
            <strong :class="stateClass(dashboard.backup.restore.state)">
              {{ hasDependencyEvidence(dashboard.backup.state) ? recoveryLabels[dashboard.backup.restore.state] : '—' }}
            </strong>
            <time
              v-if="hasDependencyEvidence(dashboard.backup.state) && dashboard.backup.restore.completedAt"
              :datetime="dashboard.backup.restore.completedAt"
            >{{ formatTime(dashboard.backup.restore.completedAt) }}</time>
            <small
              v-if="
                hasDependencyEvidence(dashboard.backup.state)
                && dashboard.backup.restore.state === 'succeeded'
                && dashboard.backup.restore.completedAt
              "
            >
              RTO {{ dashboard.backup.restore.rtoSeconds }} 秒
            </small>
          </article>
        </div>
      </section>

      <section class="panel services" data-mobile-section="services">
        <header><div><p class="kicker">依赖状态</p><h2>服务健康</h2></div></header>
        <ul class="health-list">
          <li
            v-for="service in dashboard.services"
            :key="service.service"
            :data-testid="`service-${service.service}`"
          >
            <div>
              <strong>{{ serviceLabels[service.service] }}</strong>
              <span :class="stateClass(service.state)">{{ stateLabels[service.state] }}</span>
            </div>
            <p>
              {{ hasObservedValue(service.state) ? `${service.latencyMilliseconds} ms` : '延迟 —' }}
              · 观测于 {{ formatTime(service.observedAt) }}
            </p>
          </li>
        </ul>
      </section>

      <section class="panel queues" data-mobile-section="queues">
        <header><div><p class="kicker">工作负载</p><h2>任务队列</h2></div></header>
        <ul class="health-list">
          <li v-for="queue in dashboard.queues" :key="queue.queue">
            <div>
              <strong>{{ queueLabels[queue.queue] }}</strong>
              <span :class="stateClass(queue.state)">{{ stateLabels[queue.state] }}</span>
            </div>
            <p v-if="hasObservedValue(queue.state)">
              等待 {{ queue.queued }} · 进行中 {{ queue.streaming }} ·
              失败 {{ queue.failed }} · 过期 {{ queue.expired }}
              <template v-if="queue.observedAt">
                · 观测于 <time :datetime="queue.observedAt">{{ formatTime(queue.observedAt) }}</time>
              </template>
            </p>
            <p v-else>
              运行指标 —
              <template v-if="queue.observedAt">
                · 观测于 <time :datetime="queue.observedAt">{{ formatTime(queue.observedAt) }}</time>
              </template>
            </p>
          </li>
        </ul>
      </section>

      <section class="summaries" data-mobile-section="summaries" aria-label="运行摘要">
        <article data-testid="student-summary">
          <span>学生</span><strong>{{ hasObservedValue(dashboard.students.state) ? dashboard.students.active : '—' }}</strong>
          <p v-if="hasObservedValue(dashboard.students.state)">活跃 · 停用 {{ dashboard.students.disabled }}</p>
          <p v-else>运行指标 —</p>
          <small :class="stateClass(dashboard.students.state)">
            {{ stateLabels[dashboard.students.state] }} · 观测于 {{ formatTime(dashboard.students.observedAt) }}
          </small>
        </article>
        <article data-testid="question-summary">
          <span>待答问题</span><strong>{{ hasObservedValue(dashboard.questions.state) ? dashboard.questions.waiting : '—' }}</strong>
          <p v-if="hasObservedValue(dashboard.questions.state)">最长等待 {{ formatAge(dashboard.questions.oldestWaitSeconds) }}</p>
          <p v-else>运行指标 —</p>
          <small :class="stateClass(dashboard.questions.state)">
            {{ stateLabels[dashboard.questions.state] }} · 观测于 {{ formatTime(dashboard.questions.observedAt) }}
          </small>
        </article>
        <article data-testid="ai-summary">
          <span>AI 成功率</span><strong>{{ hasObservedValue(dashboard.ai.state) ? `${dashboard.ai.successRatePercent.toFixed(1)}%` : '—' }}</strong>
          <p v-if="hasObservedValue(dashboard.ai.state)">{{ dashboard.ai.requests }} 次请求 · ${{ (dashboard.ai.dailyCostMicroUSD / 1_000_000).toFixed(2) }}</p>
          <p v-else>运行指标 —</p>
          <small :class="stateClass(dashboard.ai.state)">
            {{ stateLabels[dashboard.ai.state] }} · 观测于 {{ formatTime(dashboard.ai.observedAt) }}
          </small>
        </article>
        <article data-testid="storage-summary">
          <span>存储占用</span>
          <strong>{{ storagePercent(dashboard.storage) }}</strong>
          <p v-if="hasObservedValue(dashboard.storage.state) && dashboard.storage.capacityBytes > 0">{{ formatBytes(dashboard.storage.usedBytes) }} / {{ formatBytes(dashboard.storage.capacityBytes) }}</p>
          <p v-else>运行指标 —</p>
          <small :class="stateClass(dashboard.storage.state)">
            {{ stateLabels[dashboard.storage.state] }}
            <template v-if="hasObservedValue(dashboard.storage.state)">
              · 告警线 {{ dashboard.storage.warningPercent }}%
            </template>
            <template v-if="dashboard.storage.observedAt">
              · 观测于 <time :datetime="dashboard.storage.observedAt">{{ formatTime(dashboard.storage.observedAt) }}</time>
            </template>
          </small>
        </article>
      </section>

      <section class="panel audit" aria-labelledby="recent-audit-title">
        <header>
          <div><p class="kicker">安全活动</p><h2 id="recent-audit-title">最近审计</h2></div>
          <RouterLink to="/admin/audit">查看审计日志</RouterLink>
        </header>
        <p
          data-testid="recent-audit-state"
          class="state-line"
          :class="stateClass(dashboard.recentAuditState)"
        >{{ stateLabels[dashboard.recentAuditState] }}</p>
        <p v-if="dashboard.recentAuditState === 'empty'">暂无安全活动摘要</p>
        <ol v-if="dashboard.recentAudit.length">
          <li v-for="(item, index) in dashboard.recentAudit" :key="`${item.occurredAt}-${index}`" data-testid="recent-audit">
            <span>{{ auditCategoryLabels[item.category] }} · {{ auditOutcomeLabels[item.outcome] }}</span>
            <time :datetime="item.occurredAt">{{ formatTime(item.occurredAt) }}</time>
          </li>
        </ol>
      </section>
    </div>
  </section>
</template>

<style scoped>
.home{max-width:1180px;margin:0 auto}.page-heading,.panel header{display:flex;align-items:flex-start;justify-content:space-between;gap:20px}.eyebrow,.kicker{margin:0;color:#1673b9;font-size:.78rem;font-weight:800;letter-spacing:.08em;text-transform:uppercase}.home h1{margin:.45rem 0;font-size:clamp(2rem,4vw,3rem)}.lead,.observed,.panel p,.summaries p{color:#637086}.observed{max-width:260px;text-align:right}.feedback{margin:20px 0;padding:16px;border-radius:12px}.feedback.error{border:1px solid #e4b7b3;background:#fff5f4;color:#8d2e29}.feedback button,.panel a{border:0;border-radius:8px;background:#176eb5;color:#fff;padding:8px 12px;font:inherit;font-weight:700;text-decoration:none;cursor:pointer}.dashboard-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:18px;margin-top:28px}.panel,.summaries article{border:1px solid #dbe4f0;border-radius:16px;background:#fff;box-shadow:0 8px 28px #163a6010}.panel{padding:22px}.panel h2{margin:5px 0 0}.alerts,.backup,.summaries,.audit{grid-column:1/-1}.alert-counts{display:flex;gap:14px;margin:24px 0}.alert-counts>div{display:flex;align-items:baseline;gap:8px;min-width:140px;padding:18px;border-radius:12px}.alert-counts strong{font-size:2.1rem}.critical{background:#fff0ef;color:#a22b25}.warning-count{background:#fff7e8;color:#8a5910}.state-line{font-weight:700}.recovery-points,.summaries{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px;margin-top:20px}.recovery-points article{display:grid;gap:7px;padding:16px;border-radius:12px;background:#f6f9fc}.recovery-points time,.recovery-points small{color:#64738a}.health-list,.audit ol{display:grid;gap:10px;margin:18px 0 0;padding:0;list-style:none}.health-list li,.audit li{padding:14px;border-radius:10px;background:#f7f9fc}.health-list li>div,.audit li{display:flex;justify-content:space-between;gap:16px}.health-list p{margin:6px 0 0;font-size:.86rem}.summaries{grid-template-columns:repeat(4,minmax(0,1fr));margin-top:0}.summaries article{padding:20px}.summaries span,.summaries p{color:#64738a}.summaries strong{display:block;margin:8px 0;color:#183b67;font-size:1.8rem}.summaries small{font-weight:700}.good{color:#167248}.warning{color:#8a5910}.danger{color:#a22b25}.unknown{color:#5e6878}.audit li time{color:#637086}.audit>p{margin-bottom:0}@media(min-width:801px){.summaries{order:-1}}@media(max-width:800px){.dashboard-grid{grid-template-columns:1fr}.alerts,.backup,.summaries,.audit{grid-column:auto}.summaries,.recovery-points{grid-template-columns:1fr}.page-heading,.panel header{align-items:flex-start;flex-direction:column}.observed{text-align:left}.alert-counts{flex-direction:column}.alert-counts>div{box-sizing:border-box;width:100%}.audit li{align-items:flex-start;flex-direction:column}}@media(prefers-reduced-motion:reduce){*{scroll-behavior:auto}}
</style>
