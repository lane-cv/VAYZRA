<script setup lang="ts">
import { computed, nextTick, onBeforeMount, onBeforeUnmount, onMounted, ref } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { APIError } from '../../api/client'
import { useSessionStore } from '../../stores/session'
import { readSettings, saveSettings } from './api'
import type {
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
onBeforeMount(() => { void loadSettings() })
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
onBeforeUnmount(() => {
  alive = false
  generation++
  loadController?.abort()
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
.page{max-width:1080px}.page-heading{margin-bottom:24px}.page-heading h1{margin:.35rem 0;font-size:clamp(1.75rem,4vw,2.55rem)}.page-heading p:not(.eyebrow){color:#5b6b80}.page-heading a{display:inline-block;color:#1269ad;font-weight:700}.eyebrow{margin:0;color:#1673b9;font-size:.84rem;font-weight:700;letter-spacing:.06em}.infrastructure{margin-bottom:18px;padding:20px;border:1px solid #dbe4f0;border-radius:14px;background:#fff}.infrastructure h2{margin:0;color:#183b67;font-size:1.15rem}.infrastructure>p{color:#5b6b80}.infrastructure dl{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin:0}.infrastructure-row{padding:12px;border-radius:9px;background:#f5f8fc}.infrastructure-row dt{font-weight:800}.infrastructure-row dd{display:grid;gap:4px;margin:6px 0 0;color:#52657c;font-size:.9rem}form{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:18px}fieldset{display:grid;align-content:start;gap:14px;margin:0;padding:20px;border:1px solid #dbe4f0;border-radius:14px;background:#fff}legend{padding:0 7px;color:#183b67;font-weight:800}label{display:grid;gap:7px;color:#40536b;font-weight:700}input,textarea{box-sizing:border-box;width:100%;border:1px solid #b8c7d9;border-radius:8px;background:#fff;color:#172b47;padding:10px 11px;font:inherit}input[readonly]{background:#eef3f8;color:#53657a}.feedback,.form-actions{grid-column:1/-1}.feedback{padding:14px 16px;border:1px solid #a9d7b7;border-radius:10px;background:#f2fbf5;outline:none}.feedback.error{border-color:#efb3ae;background:#fff5f4;color:#862b25}.feedback p{margin:0 0 8px}.feedback p:last-child{margin-bottom:0}.feedback button,.form-actions button{border:0;border-radius:8px;background:#176eb5;color:#fff;padding:10px 15px;font:inherit;font-weight:700;cursor:pointer}.form-actions{display:flex;align-items:center;justify-content:space-between;gap:16px}.form-actions p{color:#68778a;font-size:.9rem}.form-actions button:disabled,.feedback button:disabled{opacity:.55;cursor:not-allowed}.denied{max-width:650px;padding:32px;border:1px solid #efc1be;border-radius:13px;background:#fff}@media(max-width:760px){.infrastructure dl,form{grid-template-columns:1fr}.form-actions{align-items:stretch;flex-direction:column}.form-actions button{width:100%}}
</style>
