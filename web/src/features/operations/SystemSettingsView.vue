<script setup lang="ts">
import { computed, nextTick, onBeforeMount, onBeforeUnmount, onMounted, ref } from 'vue'
import { storeToRefs } from 'pinia'
import { onBeforeRouteLeave } from 'vue-router'
import { APIError } from '../../api/client'
import { useApplicationUpdateStore } from '../../stores/applicationUpdates'
import { useSessionStore } from '../../stores/session'
import { readSettings, saveSettings } from './api'
import type {
  ApplicationUpdateStatus,
  InfrastructureKey,
  InfrastructureStatus,
  OperationsSettings,
  OperationsSettingsUpdate,
} from './types'

const session = useSessionStore()
const isAdmin = computed(() => session.user?.role === 'admin')
const current = ref<OperationsSettingsUpdate>()
const draft = ref<OperationsSettingsUpdate>()
const infrastructure = ref<InfrastructureStatus[]>([])
const updatedAt = ref('')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const requestId = ref('')
const validationError = ref('')
const conflict = ref(false)
const savedMessage = ref('')
const feedback = ref<HTMLElement>()
const pageTitle = ref<HTMLElement>()
let generation = 0
let alive = true
let loadController: AbortController | undefined
const updates = useApplicationUpdateStore()
const {
  status: updateStatus,
  checking: updateLoading,
  operation: updateAction,
  error: updateError,
  isTransient: updateTransient,
  busy: updateBusy,
  retryBusy: updateRetryBusy,
  retryLabel: updateRetryLabel,
} = storeToRefs(updates)

const dirty = computed(() => (
  current.value !== undefined
  && draft.value !== undefined
  && JSON.stringify(current.value) !== JSON.stringify(draft.value)
))

function clone(value: OperationsSettingsUpdate): OperationsSettingsUpdate {
  return { ...value }
}

function editableSettings(value: OperationsSettings): OperationsSettingsUpdate {
  const editable = { ...value }
  Reflect.deleteProperty(editable, 'infrastructure')
  Reflect.deleteProperty(editable, 'updatedAt')
  return editable
}

const infrastructureLabels: Record<InfrastructureKey, string> = {
  application_database: '应用数据库',
  redis_security: 'Redis 安全配置',
  object_store: '对象存储',
  ai_encryption: 'AI 加密',
  internal_metrics: '内部指标',
  host_metrics_ingestion: '主机指标采集',
  alert_webhook: '告警 Webhook',
  local_backup: '本地备份',
  remote_backup: '远程备份',
}

function validationTime(value: string | null): string {
  return value === null ? '尚无验证记录' : new Date(value).toLocaleString('zh-CN')
}

function failure(reason: unknown, fallback: string) {
  return reason instanceof APIError
    ? { message: reason.message || fallback, requestId: reason.requestId }
    : { message: fallback, requestId: '' }
}

function shortCommit(value: string): string {
  return value ? value.slice(0, 12) : '—'
}

function updateStateLabel(value: ApplicationUpdateStatus['state']): string {
  const labels: Record<ApplicationUpdateStatus['state'], string> = {
    disabled: '未启用',
    unknown: '待检查',
    checking: '检查中',
    current: '已是最新',
    available: '有新版本',
    updating: '更新中',
    success: '更新完成',
    failed: '更新失败',
    blocked: '已阻止',
  }
  return labels[value]
}

function updatePhaseLabel(value: ApplicationUpdateStatus['phase']): string {
  const labels: Record<ApplicationUpdateStatus['phase'], string> = {
    idle: '等待开始',
    checking: '检查版本',
    fetching: '拉取元数据',
    preparing: '准备更新',
    building: '构建服务',
    switching: '切换服务',
    verifying: '健康验证',
    merging: '合并发布',
    recovering: '恢复上一版本',
    complete: '已完成',
    failed: '已失败',
  }
  return labels[value]
}

function updateTime(value: string | null): string {
  return value ? new Date(value).toLocaleString('zh-CN') : '—'
}

function applyUpdate() {
  void updates.runAction('update')
}

function rollbackUpdate() {
  void updates.runAction('rollback')
}

function retryUpdate() {
  void updates.retry()
}

async function focusFeedback() {
  await nextTick()
  feedback.value?.focus()
}

async function loadSettings(focusAfter = false, preserveConflict = false) {
  if (!isAdmin.value) return
  const conflictReload = preserveConflict && conflict.value
  const requestGeneration = ++generation
  loadController?.abort()
  const controller = new AbortController()
  loadController = controller
  loading.value = true
  if (!conflictReload) {
    error.value = ''
    requestId.value = ''
    conflict.value = false
  }
  validationError.value = ''
  savedMessage.value = ''
  try {
    const value = await readSettings(controller.signal)
    if (!alive || requestGeneration !== generation || controller.signal.aborted) return
    current.value = clone(editableSettings(value))
    draft.value = clone(editableSettings(value))
    infrastructure.value = value.infrastructure.map((status) => ({ ...status }))
    updatedAt.value = value.updatedAt
    error.value = ''
    requestId.value = ''
    conflict.value = false
  } catch (reason) {
    if (!alive || requestGeneration !== generation || controller.signal.aborted) return
    const details = failure(reason, '系统设置加载失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
    conflict.value = conflictReload
  } finally {
    if (alive && requestGeneration === generation) {
      loading.value = false
      if (loadController === controller) loadController = undefined
    }
  }
  if (error.value && alive && requestGeneration === generation) await focusFeedback()
  else if (focusAfter && alive && requestGeneration === generation) {
    await nextTick()
    pageTitle.value?.focus()
  }
}

function validInteger(value: number, minimum: number, maximum?: number): boolean {
  return Number.isInteger(value) && value >= minimum && (maximum === undefined || value <= maximum)
}

function validate(value: OperationsSettingsUpdate): string {
  if ([...value.siteName].length < 1 || [...value.siteName].length > 80) return '站点名称须为 1–80 个字符'
  if ([...value.siteAnnouncement].length > 1000) return '站点公告不能超过 1000 个字符'
  if (!validInteger(value.softDeleteRetentionDays, 30, 365)) return '软删除保留天数须为 30–365'
  if (!validInteger(value.auditRetentionDays, 365, 2555)) return '审计保留天数须为 365–2555'
  if (!validInteger(value.operationalSampleRetentionDays, 1, 30)) return '运行样本保留天数须为 1–30'
  if (!validInteger(value.backupHour, 0, 23) || !validInteger(value.backupMinute, 0, 59)) return '备份时间须为 00:00–23:59'
  if (value.backupTimezone !== 'Asia/Shanghai') return '备份时区必须为 Asia/Shanghai'
  if (!validInteger(value.diskWarningPercent, 1, 99) || !validInteger(value.diskCriticalPercent, 2, 100)) return '磁盘阈值须为 1–100 的整数'
  if (value.diskCriticalPercent <= value.diskWarningPercent) return '磁盘严重阈值必须高于警告阈值'
  if (!validInteger(value.backupFilesystemWarningPercent, 1, 99) || !validInteger(value.backupFilesystemCriticalPercent, 2, 100)) return '备份存储阈值须为 1–100 的整数'
  if (value.backupFilesystemCriticalPercent <= value.backupFilesystemWarningPercent) return '备份存储严重阈值必须高于警告阈值'
  if (!validInteger(value.localBackupAgeWarningHours, 1, 2_147_483_646) || !validInteger(value.localBackupAgeCriticalHours, 2, 2_147_483_647)) return '本地备份时效阈值须为有效正整数'
  if (value.localBackupAgeCriticalHours <= value.localBackupAgeWarningHours) return '本地备份时效严重阈值必须高于警告阈值'
  if (!validInteger(value.aiErrorWarningPercent, 1, 99) || !validInteger(value.aiErrorCriticalPercent, 2, 100)) return 'AI 错误率阈值须为 1–100 的整数'
  if (value.aiErrorCriticalPercent <= value.aiErrorWarningPercent) return 'AI 错误率严重阈值必须高于警告阈值'
  if (!validInteger(value.processingQueueWarning, 1, 2_147_483_646) || !validInteger(value.processingQueueCritical, 2, 2_147_483_647)) return '处理队列阈值须为有效正整数'
  if (value.processingQueueCritical <= value.processingQueueWarning) return '处理队列严重阈值必须高于警告阈值'
  if (!validInteger(value.processingFailureWarningCount, 1, 2_147_483_646) || !validInteger(value.processingFailureCriticalCount, 2, 2_147_483_647)) return '处理失败阈值须为有效正整数'
  if (value.processingFailureCriticalCount <= value.processingFailureWarningCount) return '处理失败严重阈值必须高于警告阈值'
  if (!validInteger(value.loginFailureWarningCount, 1, 2_147_483_646) || !validInteger(value.loginFailureCriticalCount, 2, 2_147_483_647)) return '登录失败阈值须为有效正整数'
  if (value.loginFailureCriticalCount <= value.loginFailureWarningCount) return '登录失败严重阈值必须高于警告阈值'
  if (!validInteger(value.authorizationDenialWarningCount, 1, 2_147_483_646) || !validInteger(value.authorizationDenialCriticalCount, 2, 2_147_483_647)) return '授权拒绝阈值须为有效正整数'
  if (value.authorizationDenialCriticalCount <= value.authorizationDenialWarningCount) return '授权拒绝严重阈值必须高于警告阈值'
  return ''
}

async function submit() {
  if (!draft.value || saving.value) return
  validationError.value = validate(draft.value)
  error.value = ''
  requestId.value = ''
  conflict.value = false
  savedMessage.value = ''
  if (validationError.value) {
    await focusFeedback()
    return
  }
  const mutationGeneration = generation
  const submitted = clone(draft.value)
  saving.value = true
  try {
    const updated = await saveSettings(submitted)
    if (!alive || mutationGeneration !== generation) return
    current.value = clone(editableSettings(updated))
    draft.value = clone(editableSettings(updated))
    infrastructure.value = updated.infrastructure.map((status) => ({ ...status }))
    updatedAt.value = updated.updatedAt
    savedMessage.value = '系统设置已保存'
    await focusFeedback()
  } catch (reason) {
    if (!alive || mutationGeneration !== generation) return
    const details = failure(reason, '系统设置保存失败，请稍后重试')
    error.value = details.message
    requestId.value = details.requestId
    conflict.value = reason instanceof APIError && (
      reason.status === 409 || reason.code === 'settings_conflict'
    )
    await focusFeedback()
  } finally {
    if (alive && mutationGeneration === generation) saving.value = false
  }
}

function beforeUnload(event: BeforeUnloadEvent | Event) {
  if (!dirty.value) return
  event.preventDefault()
  ;(event as BeforeUnloadEvent).returnValue = true
}

onBeforeRouteLeave(() => (
  !dirty.value || window.confirm('系统设置尚未保存，确定离开吗？')
))
onBeforeMount(() => {
  void loadSettings()
})
onMounted(() => {
  updates.connect(false)
  window.addEventListener('beforeunload', beforeUnload)
})
onBeforeUnmount(() => {
  alive = false
  generation++
  loadController?.abort()
  updates.disconnect(false)
  window.removeEventListener('beforeunload', beforeUnload)
})
</script>

<template>
  <section v-if="!isAdmin" class="denied" aria-labelledby="operations-settings-title">
    <h1 id="operations-settings-title">无权访问系统设置</h1>
    <p>此功能仅对管理员开放。</p>
  </section>
  <section v-else class="page" aria-labelledby="operations-settings-title">
    <header class="page-heading">
      <p class="eyebrow">系统运维</p>
      <h1 id="operations-settings-title" ref="pageTitle" tabindex="-1">系统设置</h1>
      <p>配置站点信息、数据保留、备份时间与运行告警阈值。</p>
      <RouterLink to="/admin/audit">查看审计日志</RouterLink>
    </header>

    <p v-if="loading && !draft" role="status" aria-live="polite">正在加载系统设置…</p>
    <div
      v-else-if="error && !draft"
      ref="feedback"
      class="feedback error"
      role="alert"
      tabindex="-1"
    >
      <p>{{ error }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
      <button type="button" :disabled="loading" @click="loadSettings(true)">重试加载</button>
    </div>

    <template v-else-if="draft">
    <section class="infrastructure" data-testid="infrastructure-status" aria-labelledby="infrastructure-heading">
      <h2 id="infrastructure-heading">基础设施配置状态</h2>
      <p>以下状态仅显示是否已配置及最近验证时间。</p>
      <dl>
        <div
          v-for="status in infrastructure"
          :key="status.key"
          data-testid="infrastructure-row"
          class="infrastructure-row"
        >
          <dt>{{ infrastructureLabels[status.key] }}</dt>
          <dd>
            <span>{{ status.configured ? '已配置' : '未配置' }}</span>
            <time v-if="status.lastValidatedAt" :datetime="status.lastValidatedAt">{{ validationTime(status.lastValidatedAt) }}</time>
            <span v-else>尚无验证记录</span>
          </dd>
        </div>
      </dl>
    </section>
    <section class="updates" data-testid="application-updates" aria-labelledby="updates-heading">
      <div class="updates-heading">
        <div>
          <h2 id="updates-heading">应用版本与 OTA 更新</h2>
          <p>侧栏会定时检查更新；此详情页仅在管理员点击后刷新，避免重复远程请求。</p>
        </div>
        <button data-testid="check-updates" type="button" :disabled="updateBusy" @click="updates.refresh(true)">
          {{ updateLoading ? '检查中…' : '立即检查' }}
        </button>
      </div>
      <div v-if="updateError" class="update-feedback" role="alert">
        <p>{{ updateError }}</p>
        <button
          data-testid="retry-update"
          type="button"
          :disabled="updateRetryBusy"
          @click="retryUpdate"
        >{{ updateRetryLabel }}</button>
      </div>
      <div v-if="!updateStatus" class="update-empty" role="status">点击“立即检查”获取远程版本信息。</div>
      <template v-if="updateStatus">
        <div class="update-summary">
          <span class="update-state" :class="`state-${updateStatus.state}`">{{ updateStateLabel(updateStatus.state) }}</span>
          <strong :role="updateError ? undefined : 'status'" aria-live="polite">{{ updateStatus.message || '暂无更新信息' }}</strong>
        </div>
        <dl class="update-details">
          <div><dt>当前版本</dt><dd>{{ updateStatus.currentVersion ? `v${updateStatus.currentVersion}` : '—' }}</dd></div>
          <div><dt>最新版本</dt><dd>{{ updateStatus.latestVersion ? `v${updateStatus.latestVersion}` : '—' }}</dd></div>
          <div><dt>分支</dt><dd>{{ updateStatus.ref || '—' }}</dd></div>
          <div><dt>更新通道</dt><dd>{{ updateStatus.channel }} · GitHub Release</dd></div>
          <div><dt>当前提交</dt><dd><code>{{ shortCommit(updateStatus.currentCommit) }}</code></dd></div>
          <div><dt>远端提交</dt><dd><code>{{ shortCommit(updateStatus.latestCommit) }}</code></dd></div>
        </dl>
        <div v-if="updateTransient" class="update-progress">
          <div><span>{{ updatePhaseLabel(updateStatus.phase) }}</span><strong>{{ updateStatus.progress }}%</strong></div>
          <div
            class="update-progress-track"
            role="progressbar"
            aria-label="更新进度"
            aria-valuemin="0"
            aria-valuemax="100"
            :aria-valuenow="updateStatus.progress"
          ><span :style="{ width: `${updateStatus.progress}%` }"></span></div>
        </div>
        <section v-if="updateStatus.releaseName || updateStatus.releaseNotes" class="update-release" aria-label="发布说明">
          <div>
            <h3>{{ updateStatus.releaseName || `v${updateStatus.latestVersion}` }}</h3>
            <time v-if="updateStatus.publishedAt" :datetime="updateStatus.publishedAt">{{ updateTime(updateStatus.publishedAt) }}</time>
          </div>
          <p v-if="updateStatus.releaseNotes">{{ updateStatus.releaseNotes }}</p>
          <a
            v-if="updateStatus.releaseURL"
            :href="updateStatus.releaseURL"
            target="_blank"
            rel="noopener noreferrer"
          >查看完整发布说明 ↗</a>
        </section>
        <p v-if="updateStatus.dirty" class="update-feedback" role="alert">部署目录有未提交修改，自动更新已禁用。</p>
        <p v-if="updateStatus.finishedAt" class="update-time">最近完成：<time :datetime="updateStatus.finishedAt">{{ updateTime(updateStatus.finishedAt) }}</time></p>
        <div v-if="!updateError" class="update-actions">
          <button
            v-if="updateStatus.state === 'success'"
            data-testid="reload-application"
            type="button"
            @click="updates.reloadPage"
          >重新加载页面</button>
          <button
            v-if="updateStatus.updateAvailable"
            data-testid="apply-update"
            type="button"
            :disabled="updateStatus.dirty || updateBusy || updateTransient"
            @click="applyUpdate"
          >
            {{ updateAction === 'update' || updateTransient ? '更新中…' : `更新到 v${updateStatus.latestVersion || '最新版本'}` }}
          </button>
          <button
            v-if="updateStatus.canRollback && updateStatus.previousVersion"
            data-testid="rollback-update"
            class="rollback-button"
            type="button"
            :disabled="updateBusy || updateTransient"
            @click="rollbackUpdate"
          >回滚到 v{{ updateStatus.previousVersion }}</button>
        </div>
      </template>
    </section>
    <form novalidate @submit.prevent="submit">
      <fieldset :disabled="saving || loading">
        <legend>站点信息</legend>
        <label for="site-name">站点名称
          <input id="site-name" v-model="draft.siteName" data-testid="site-name" maxlength="80" required>
        </label>
        <label for="site-announcement">站点公告
          <textarea id="site-announcement" v-model="draft.siteAnnouncement" data-testid="site-announcement" maxlength="1000" rows="4"></textarea>
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>数据保留</legend>
        <label for="soft-delete-retention">软删除保留天数
          <input id="soft-delete-retention" v-model.number="draft.softDeleteRetentionDays" data-testid="soft-delete-retention" type="number" min="30" max="365" step="1">
        </label>
        <label for="audit-retention">审计保留天数
          <input id="audit-retention" v-model.number="draft.auditRetentionDays" data-testid="audit-retention" type="number" min="365" max="2555" step="1">
        </label>
        <label for="sample-retention">运行样本保留天数
          <input id="sample-retention" v-model.number="draft.operationalSampleRetentionDays" data-testid="sample-retention" type="number" min="1" max="30" step="1">
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>备份计划</legend>
        <label for="backup-hour">小时（0–23）
          <input id="backup-hour" v-model.number="draft.backupHour" type="number" min="0" max="23" step="1">
        </label>
        <label for="backup-minute">分钟（0–59）
          <input id="backup-minute" v-model.number="draft.backupMinute" type="number" min="0" max="59" step="1">
        </label>
        <label for="backup-timezone">时区
          <input id="backup-timezone" v-model="draft.backupTimezone" data-testid="backup-timezone" readonly>
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>磁盘告警</legend>
        <label for="disk-warning">警告阈值（%）
          <input id="disk-warning" v-model.number="draft.diskWarningPercent" data-testid="disk-warning" type="number" min="1" max="99" step="1">
        </label>
        <label for="disk-critical">严重阈值（%）
          <input id="disk-critical" v-model.number="draft.diskCriticalPercent" data-testid="disk-critical" type="number" min="2" max="100" step="1">
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>备份存储告警</legend>
        <label for="backup-filesystem-warning">警告阈值（%）
          <input id="backup-filesystem-warning" v-model.number="draft.backupFilesystemWarningPercent" data-testid="backup-filesystem-warning" type="number" min="1" max="99" step="1">
        </label>
        <label for="backup-filesystem-critical">严重阈值（%）
          <input id="backup-filesystem-critical" v-model.number="draft.backupFilesystemCriticalPercent" data-testid="backup-filesystem-critical" type="number" min="2" max="100" step="1">
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>本地备份时效告警</legend>
        <label for="local-backup-age-warning">警告时效（小时）
          <input id="local-backup-age-warning" v-model.number="draft.localBackupAgeWarningHours" data-testid="local-backup-age-warning" type="number" min="1" max="2147483646" step="1">
        </label>
        <label for="local-backup-age-critical">严重时效（小时）
          <input id="local-backup-age-critical" v-model.number="draft.localBackupAgeCriticalHours" data-testid="local-backup-age-critical" type="number" min="2" max="2147483647" step="1">
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>AI 错误率告警</legend>
        <label for="ai-warning">警告阈值（%）
          <input id="ai-warning" v-model.number="draft.aiErrorWarningPercent" type="number" min="1" max="99" step="1">
        </label>
        <label for="ai-critical">严重阈值（%）
          <input id="ai-critical" v-model.number="draft.aiErrorCriticalPercent" type="number" min="2" max="100" step="1">
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>处理队列告警</legend>
        <label for="queue-warning">警告队列长度
          <input id="queue-warning" v-model.number="draft.processingQueueWarning" type="number" min="1" max="2147483646" step="1">
        </label>
        <label for="queue-critical">严重队列长度
          <input id="queue-critical" v-model.number="draft.processingQueueCritical" type="number" min="2" max="2147483647" step="1">
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>处理失败告警</legend>
        <label for="processing-failure-warning">警告次数
          <input id="processing-failure-warning" v-model.number="draft.processingFailureWarningCount" data-testid="processing-failure-warning" type="number" min="1" max="2147483646" step="1">
        </label>
        <label for="processing-failure-critical">严重次数
          <input id="processing-failure-critical" v-model.number="draft.processingFailureCriticalCount" data-testid="processing-failure-critical" type="number" min="2" max="2147483647" step="1">
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>登录失败告警</legend>
        <label for="login-failure-warning">警告次数
          <input id="login-failure-warning" v-model.number="draft.loginFailureWarningCount" data-testid="login-failure-warning" type="number" min="1" max="2147483646" step="1">
        </label>
        <label for="login-failure-critical">严重次数
          <input id="login-failure-critical" v-model.number="draft.loginFailureCriticalCount" data-testid="login-failure-critical" type="number" min="2" max="2147483647" step="1">
        </label>
      </fieldset>

      <fieldset :disabled="saving || loading">
        <legend>授权拒绝告警</legend>
        <label for="authorization-denial-warning">警告次数
          <input id="authorization-denial-warning" v-model.number="draft.authorizationDenialWarningCount" data-testid="authorization-denial-warning" type="number" min="1" max="2147483646" step="1">
        </label>
        <label for="authorization-denial-critical">严重次数
          <input id="authorization-denial-critical" v-model.number="draft.authorizationDenialCriticalCount" data-testid="authorization-denial-critical" type="number" min="2" max="2147483647" step="1">
        </label>
      </fieldset>

      <div
        v-if="validationError || error || savedMessage"
        ref="feedback"
        class="feedback"
        :class="{ error: validationError || error }"
        :role="validationError || error ? 'alert' : 'status'"
        tabindex="-1"
        aria-live="polite"
      >
        <p>{{ validationError || error || savedMessage }}<span v-if="requestId">（支持编号：{{ requestId }}）</span></p>
        <button v-if="conflict" data-testid="reload-conflict" type="button" :disabled="loading" @click="loadSettings(true, true)">重新加载服务端设置</button>
      </div>

      <footer class="form-actions">
        <p>版本 {{ draft.version }} · 更新于 <time :datetime="updatedAt">{{ new Date(updatedAt).toLocaleString('zh-CN') }}</time></p>
        <button type="submit" :disabled="saving || loading">{{ saving ? '保存中…' : '保存设置' }}</button>
      </footer>
    </form>
    </template>
  </section>
</template>

<style scoped>
.page{max-width:1080px}.page-heading{margin-bottom:24px}.page-heading h1{margin:.35rem 0;font-size:clamp(1.75rem,4vw,2.55rem)}.page-heading p:not(.eyebrow){color:#5b6b80}.page-heading a{display:inline-block;color:#1269ad;font-weight:700}.eyebrow{margin:0;color:#1673b9;font-size:.84rem;letter-spacing:.06em;font-weight:700}.infrastructure,.updates{margin-bottom:18px;padding:20px;border:1px solid #dbe4f0;border-radius:14px;background:#fff}.infrastructure h2,.updates h2{margin:0;color:#183b67;font-size:1.15rem}.infrastructure>p,.updates-heading p{color:#5b6b80}.infrastructure dl{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin:0}.infrastructure-row{padding:12px;border-radius:9px;background:#f5f8fc}.infrastructure-row dt{font-weight:800}.infrastructure-row dd{display:grid;gap:4px;margin:6px 0 0;color:#52657c;font-size:.9rem}.updates-heading{display:flex;align-items:flex-start;justify-content:space-between;gap:20px}.updates-heading button,.update-actions button{border:0;border-radius:8px;background:#176eb5;color:#fff;padding:10px 15px;font:inherit;font-weight:700;cursor:pointer}.updates-heading button:disabled,.update-actions button:disabled{opacity:.55;cursor:not-allowed}.update-summary{display:flex;align-items:center;gap:10px;margin:14px 0;color:#183b67}.update-state{display:inline-flex;border-radius:999px;padding:4px 10px;font-size:.82rem;font-weight:800}.state-current,.state-success{background:#e4f6ed;color:#167244}.state-available{background:#fff2d9;color:#8a5600}.state-checking,.state-updating{background:#e9f2fb;color:#176eb5}.state-failed,.state-blocked{background:#fbe8e7;color:#a4332d}.state-unknown{background:#eef2f6;color:#52647a}.update-details{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin:0}.update-details div{padding:12px;border-radius:9px;background:#f5f8fc}.update-details dt{color:#68778a;font-size:.86rem}.update-details dd{margin:5px 0 0;color:#183b67;font-weight:750}.update-details code{font-size:.88rem}.update-feedback,.update-empty{margin:14px 0 0;padding:10px 12px;border-radius:8px;background:#fff5f4;color:#862b25}.update-empty{background:#f5f8fc;color:#52647a}.update-actions{display:flex;justify-content:flex-end;margin-top:14px}form{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:18px}fieldset{display:grid;align-content:start;gap:14px;margin:0;padding:20px;border:1px solid #dbe4f0;border-radius:14px;background:#fff}legend{padding:0 7px;color:#183b67;font-weight:800}label{display:grid;gap:7px;color:#40536b;font-weight:700}input,textarea{box-sizing:border-box;width:100%;border:1px solid #b8c7d9;border-radius:8px;background:#fff;color:#172b47;padding:10px 11px;font:inherit}input[readonly]{background:#eef3f8;color:#53657a}.feedback,.form-actions{grid-column:1/-1}.feedback{padding:14px 16px;border:1px solid #a9d7b7;border-radius:10px;background:#f2fbf5;outline:none}.feedback.error{border-color:#efb3ae;background:#fff5f4;color:#862b25}.feedback p{margin:0 0 8px}.feedback p:last-child{margin-bottom:0}.feedback button,.form-actions button{border:0;border-radius:8px;background:#176eb5;color:#fff;padding:10px 15px;font:inherit;font-weight:700;cursor:pointer}.form-actions{display:flex;align-items:center;justify-content:space-between;gap:16px}.form-actions p{color:#68778a;font-size:.9rem}.form-actions button:disabled,.feedback button:disabled{opacity:.55;cursor:not-allowed}.denied{max-width:650px;padding:32px;border:1px solid #efc1be;border-radius:13px;background:#fff}@media(max-width:760px){.infrastructure dl,.update-details,form{grid-template-columns:1fr}.updates-heading{display:grid}.updates-heading button{width:100%}.form-actions{align-items:stretch;flex-direction:column}.form-actions button{width:100%}}
</style>

<style scoped>
.update-release a{color:var(--hl-primary-strong)!important}.update-feedback button{color:var(--hl-on-primary,#fff)!important}
.page-heading p:not(.eyebrow),.infrastructure>p,.updates-heading p,.infrastructure-row dd,.update-details dt,.form-actions p{color:var(--hl-text-muted)}
.page-heading a,.eyebrow{color:var(--hl-primary-strong)}
.infrastructure,.updates,fieldset,.denied{border-color:var(--hl-border);background:var(--hl-surface-solid)}
.infrastructure h2,.updates h2,.update-summary,.update-details dd,legend{color:var(--hl-text)}
.infrastructure-row,.update-details div,.update-empty,input[readonly]{background:var(--hl-surface-muted)}
.update-empty,input[readonly]{color:var(--hl-text-muted)}
.updates-heading button,.update-actions button,.feedback button,.form-actions button{background:var(--hl-primary)}
.updates-heading button,.update-actions button,.feedback button,.form-actions button{color:var(--hl-on-primary,#fff)}
.state-current,.state-success{background:var(--hl-success-soft);color:var(--hl-success)}
.state-available{background:var(--hl-warning-soft);color:var(--hl-warning)}
.state-checking,.state-updating{background:var(--hl-primary-soft);color:var(--hl-primary-strong)}
.state-failed,.state-blocked,.update-feedback,.feedback.error{background:var(--hl-danger-soft);color:var(--hl-danger)}
label{color:var(--hl-text)}
input,textarea{border-color:var(--hl-border-strong);background:var(--hl-surface-solid);color:var(--hl-text)}
.update-progress{display:grid;gap:7px;margin:14px 0 0}.update-progress>div:first-child{display:flex;justify-content:space-between;color:var(--hl-text-muted,#52657c);font-size:.86rem}.update-progress-track{height:8px;overflow:hidden;border-radius:999px;background:var(--hl-surface-muted,#dce8eb)}.update-progress-track span{display:block;height:100%;border-radius:inherit;background:linear-gradient(90deg,var(--hl-primary,#238579),var(--hl-accent,#58c5b5));transition:width .25s ease}.update-release{margin-top:14px;padding:14px;border:1px solid var(--hl-border,#d8e4e8);border-radius:10px;background:var(--hl-surface-muted,#f8fbfb)}.update-release>div{display:flex;align-items:baseline;justify-content:space-between;gap:12px}.update-release h3{margin:0;color:var(--hl-text,#244d55);font-size:1rem}.update-release time,.update-time{color:var(--hl-text-soft,#6a7c85);font-size:.82rem}.update-release p{max-height:140px;overflow:auto;margin:10px 0;color:var(--hl-text-muted,#4d636c);line-height:1.55;white-space:pre-wrap}.update-release a{color:var(--hl-primary,#207e73);font-size:.86rem;font-weight:800;text-decoration:none}.update-release a:hover{text-decoration:underline}.update-time{margin:12px 0 0}.update-actions{gap:9px}.update-actions .rollback-button{border:1px solid var(--hl-border-strong,#c3d1d6);background:var(--hl-surface-muted,#edf3f4);color:var(--hl-text-muted,#435c65)}.update-feedback p{margin:0 0 8px}.update-feedback button{border:0;border-radius:8px;background:var(--hl-primary,#176eb5);color:var(--hl-surface-solid,#fff);padding:8px 12px;font:inherit;font-weight:750;cursor:pointer}.update-feedback button:disabled{opacity:.55;cursor:not-allowed}.state-disabled{background:var(--hl-surface-muted,#eef2f6);color:var(--hl-text-muted,#52647a)}@media(max-width:760px){.update-release>div{align-items:flex-start;flex-direction:column}.update-actions{display:grid}.update-actions button{width:100%}}@media(prefers-reduced-motion:reduce){.update-progress-track span{transition:none}}
</style>
