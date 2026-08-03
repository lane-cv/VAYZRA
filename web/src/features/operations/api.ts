import { APIError, request, requestWithMeta } from '../../api/client'
import type {
  AuditFilters,
  AuditMetadata,
  AuditPage,
  AuditRecord,
  AlertCategory,
  AlertFilters,
  AlertPage,
  AlertSeverity,
  AlertState,
  BackupArtifact,
  BackupCursor,
  BackupPage,
  BackupRun,
  BackupRunDetail,
  DashboardAuditCategory,
  DashboardAuditOutcome,
  DashboardQueue,
  DashboardService,
  OperationalAlert,
  OperationsDashboard,
  OperationsDataState,
  OperationsSettings,
  OperationsSettingsUpdate,
  RecoveryState,
  RestoreVerification,
} from './types'

function invalidResponse(): APIError {
  return new APIError(200, 'invalid_response', '服务响应异常，请稍后重试', '')
}

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw invalidResponse()
  return value as Record<string, unknown>
}

function settingsInteger(value: unknown): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || Object.is(value, -0)) throw invalidResponse()
  return value
}

function settingsString(value: unknown): string {
  if (typeof value !== 'string' || !wellFormedUnicode(value)) throw invalidResponse()
  return value
}

function wellFormedUnicode(value: string): boolean {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index)
    if (code >= 0xd800 && code <= 0xdbff) {
      if (index + 1 >= value.length) return false
      const next = value.charCodeAt(index + 1)
      if (next < 0xdc00 || next > 0xdfff) return false
      index++
    } else if (code >= 0xdc00 && code <= 0xdfff) {
      return false
    }
  }
  return true
}

function parseSettings(value: unknown): OperationsSettings {
  const source = record(value)
  const timezone = settingsString(source.backupTimezone)
  if (timezone !== 'Asia/Shanghai') throw invalidResponse()
  const result: OperationsSettings = {
    version: settingsInteger(source.version),
    siteName: settingsString(source.siteName),
    siteAnnouncement: settingsString(source.siteAnnouncement),
    softDeleteRetentionDays: settingsInteger(source.softDeleteRetentionDays),
    auditRetentionDays: settingsInteger(source.auditRetentionDays),
    operationalSampleRetentionDays: settingsInteger(source.operationalSampleRetentionDays),
    backupHour: settingsInteger(source.backupHour),
    backupMinute: settingsInteger(source.backupMinute),
    backupTimezone: timezone,
    diskWarningPercent: settingsInteger(source.diskWarningPercent),
    diskCriticalPercent: settingsInteger(source.diskCriticalPercent),
    backupFilesystemWarningPercent: settingsInteger(source.backupFilesystemWarningPercent),
    backupFilesystemCriticalPercent: settingsInteger(source.backupFilesystemCriticalPercent),
    localBackupAgeWarningHours: settingsInteger(source.localBackupAgeWarningHours),
    localBackupAgeCriticalHours: settingsInteger(source.localBackupAgeCriticalHours),
    aiErrorWarningPercent: settingsInteger(source.aiErrorWarningPercent),
    aiErrorCriticalPercent: settingsInteger(source.aiErrorCriticalPercent),
    processingQueueWarning: settingsInteger(source.processingQueueWarning),
    processingQueueCritical: settingsInteger(source.processingQueueCritical),
    processingFailureWarningCount: settingsInteger(source.processingFailureWarningCount),
    processingFailureCriticalCount: settingsInteger(source.processingFailureCriticalCount),
    loginFailureWarningCount: settingsInteger(source.loginFailureWarningCount),
    loginFailureCriticalCount: settingsInteger(source.loginFailureCriticalCount),
    authorizationDenialWarningCount: settingsInteger(source.authorizationDenialWarningCount),
    authorizationDenialCriticalCount: settingsInteger(source.authorizationDenialCriticalCount),
    infrastructure: parseInfrastructure(source.infrastructure),
    updatedAt: settingsString(source.updatedAt),
  }
  if (
    result.version < 1
    || [...result.siteName].length < 1
    || [...result.siteName].length > 80
    || [...result.siteAnnouncement].length > 1000
    || result.softDeleteRetentionDays < 30
    || result.softDeleteRetentionDays > 365
    || result.auditRetentionDays < 365
    || result.auditRetentionDays > 2555
    || result.operationalSampleRetentionDays < 1
    || result.operationalSampleRetentionDays > 30
    || result.backupHour < 0
    || result.backupHour > 23
    || result.backupMinute < 0
    || result.backupMinute > 59
    || result.diskWarningPercent < 1
    || result.diskWarningPercent > 99
    || result.diskCriticalPercent <= result.diskWarningPercent
    || result.diskCriticalPercent > 100
    || result.backupFilesystemWarningPercent < 1
    || result.backupFilesystemWarningPercent > 99
    || result.backupFilesystemCriticalPercent <= result.backupFilesystemWarningPercent
    || result.backupFilesystemCriticalPercent > 100
    || !validCountThresholdPair(result.localBackupAgeWarningHours, result.localBackupAgeCriticalHours)
    || result.aiErrorWarningPercent < 1
    || result.aiErrorWarningPercent > 99
    || result.aiErrorCriticalPercent <= result.aiErrorWarningPercent
    || result.aiErrorCriticalPercent > 100
    || !validCountThresholdPair(result.processingQueueWarning, result.processingQueueCritical)
    || !validCountThresholdPair(result.processingFailureWarningCount, result.processingFailureCriticalCount)
    || !validCountThresholdPair(result.loginFailureWarningCount, result.loginFailureCriticalCount)
    || !validCountThresholdPair(result.authorizationDenialWarningCount, result.authorizationDenialCriticalCount)
    || !validTimestamp(result.updatedAt)
  ) throw invalidResponse()
  return result
}

const infrastructureKeys = [
  'application_database',
  'redis_security',
  'object_store',
  'ai_encryption',
  'internal_metrics',
  'host_metrics_ingestion',
  'alert_webhook',
  'local_backup',
  'remote_backup',
] as const

function parseInfrastructure(value: unknown): OperationsSettings['infrastructure'] {
  if (!Array.isArray(value) || value.length !== infrastructureKeys.length) throw invalidResponse()
  return value.map((item, index) => {
    const source = record(item)
    const keys = Object.keys(source)
    if (
      keys.length !== 3
      || !keys.includes('key')
      || !keys.includes('configured')
      || !keys.includes('lastValidatedAt')
      || source.key !== infrastructureKeys[index]
      || typeof source.configured !== 'boolean'
      || (source.lastValidatedAt !== null && (
        typeof source.lastValidatedAt !== 'string'
        || !validTimestamp(source.lastValidatedAt)
      ))
    ) throw invalidResponse()
    return {
      key: infrastructureKeys[index],
      configured: source.configured,
      lastValidatedAt: source.lastValidatedAt,
    }
  })
}

function validCountThresholdPair(warning: number, critical: number): boolean {
  return warning >= 1
    && warning <= 2_147_483_646
    && critical > warning
    && critical <= 2_147_483_647
}

export async function readSettings(signal?: AbortSignal): Promise<OperationsSettings> {
  return parseSettings(await request<unknown>('/admin/operations/settings', { signal }))
}

export async function saveSettings(value: OperationsSettingsUpdate): Promise<OperationsSettings> {
  return parseSettings(await request<unknown>('/admin/operations/settings', {
    method: 'PUT',
    json: value,
  }))
}

const metadataKeys = [
  'status',
  'reason',
  'version',
  'count',
  'provider_id',
  'model_id',
  'file_purpose',
] as const satisfies ReadonlyArray<keyof AuditMetadata>
const publicStatuses = new Set([
  'active', 'disabled',
  'normal', 'draining', 'backup', 'release',
  'queued', 'snapshotting', 'encrypting', 'verifying',
  'syncing', 'succeeded', 'degraded', 'failed',
  'restoring', 'checking',
  'open', 'acknowledged', 'resolved',
])
const publicReasons = new Set([
  'retention', 'backup_schedule', 'threshold',
  'malformed_id', 'unexpected_query',
  'invalid_actor', 'invalid_ip',
])
const publicFilePurposes = new Set(['teaching', 'qa_attachment', 'ai_attachment'])
const canonicalUUIDPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/
const nilUUID = '00000000-0000-0000-0000-000000000000'
const maxAuditVersion = '9223372036854775807'
const maxAuditCount = '1000000000'

function canonicalUUID(value: string): boolean {
  return value !== nilUUID && canonicalUUIDPattern.test(value)
}

function boundedAuditInteger(value: unknown, minimum: number, maximum: string): value is number | string {
  let canonical: string
  if (typeof value === 'number') {
    if (!Number.isSafeInteger(value) || Object.is(value, -0) || value < minimum) return false
    canonical = String(value)
  } else if (typeof value === 'string') {
    if (!/^(0|[1-9]\d*)$/.test(value) || (minimum === 1 && value === '0')) return false
    canonical = value
  } else {
    return false
  }
  return canonical.length < maximum.length
    || (canonical.length === maximum.length && canonical <= maximum)
}

function parseMetadata(value: unknown): AuditMetadata {
  const source = record(value)
  const result: AuditMetadata = {}
  for (const key of metadataKeys) {
    const field = source[key]
    if (field === undefined) continue
    if (key === 'version') {
      if (boundedAuditInteger(field, 1, maxAuditVersion)) result.version = field
      continue
    }
    if (key === 'count') {
      if (boundedAuditInteger(field, 0, maxAuditCount)) result.count = field
      continue
    }
    if (typeof field !== 'string' || !field) continue
    if (key === 'status' && publicStatuses.has(field)) result.status = field
    if (key === 'reason' && publicReasons.has(field)) result.reason = field
    if ((key === 'provider_id' || key === 'model_id') && canonicalUUID(field)) result[key] = field
    if (key === 'file_purpose' && publicFilePurposes.has(field)) result.file_purpose = field
  }
  return result
}

function parseAuditRecord(value: unknown): AuditRecord {
  const source = record(value)
  if (
    typeof source.id !== 'number'
    || !Number.isSafeInteger(source.id)
    || source.id < 1
    || typeof source.action !== 'string'
    || !source.action
    || typeof source.targetType !== 'string'
    || !source.targetType
    || typeof source.occurredAt !== 'string'
    || !validTimestamp(source.occurredAt)
  ) throw invalidResponse()
  if (source.actorId !== undefined && typeof source.actorId !== 'string') throw invalidResponse()
  if (source.targetId !== undefined && typeof source.targetId !== 'string') throw invalidResponse()
  return {
    id: source.id,
    actorId: typeof source.actorId === 'string' && canonicalUUID(source.actorId) ? source.actorId : '',
    action: source.action,
    targetType: source.targetType,
    targetId: typeof source.targetId === 'string' && (
      source.targetId === 'global'
      || source.targetId === 'unresolved'
      || canonicalUUID(source.targetId)
    ) ? source.targetId : '',
    metadata: parseMetadata(source.metadata),
    occurredAt: source.occurredAt,
  }
}

const rfc3339Nano = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-](\d{2}):(\d{2}))$/

function validTimestamp(value: string): boolean {
  const match = rfc3339Nano.exec(value)
  if (!match) return false
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hour = Number(match[4])
  const minute = Number(match[5])
  const second = Number(match[6])
  const offsetHour = match[9] === undefined ? 0 : Number(match[9])
  const offsetMinute = match[10] === undefined ? 0 : Number(match[10])
  if (
    month < 1 || month > 12
    || hour > 23 || minute > 59 || second > 59
    || offsetHour > 23 || offsetMinute > 59
  ) return false
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const monthDays = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  return day >= 1 && day <= monthDays[month - 1]
}

export async function listAudit(filters: AuditFilters, signal?: AbortSignal): Promise<AuditPage> {
  const query = new URLSearchParams()
  for (const key of ['action', 'targetType', 'outcome', 'actorId', 'from', 'to'] as const) {
    const value = filters[key]
    if (value) query.set(key, value)
  }
  if (filters.beforeId !== undefined) query.set('beforeId', String(filters.beforeId))
  if (filters.limit !== undefined) query.set('limit', String(filters.limit))
  const suffix = query.size ? `?${query.toString()}` : ''
  const response = await requestWithMeta<unknown>(`/admin/operations/audit${suffix}`, { signal })
  if (!Array.isArray(response.data)) throw invalidResponse()
  const nextBeforeId = response.meta?.nextBeforeId
  if (nextBeforeId !== undefined && (
    typeof nextBeforeId !== 'number'
    || !Number.isSafeInteger(nextBeforeId)
    || nextBeforeId < 1
  )) throw invalidResponse()
  return {
    items: response.data.map(parseAuditRecord),
    nextBeforeId: nextBeforeId ?? null,
  }
}

const backupSummaryKeys = new Set([
  'id',
  'trigger',
  'state',
  'requestedAt',
  'startedAt',
  'finishedAt',
  'logicalBytes',
  'storedBytes',
  'localExpiresAt',
  'remoteExpiresAt',
  'errorCategory',
])
const backupDetailKeys = new Set([
  ...backupSummaryKeys,
  'artifacts',
  'restoreVerifications',
])
const artifactKeys = new Set([
  'kind',
  'repository',
  'sizeBytes',
  'verifiedAt',
  'expiresAt',
])
const restoreVerificationKeys = new Set([
  'id',
  'state',
  'startedAt',
  'finishedAt',
  'restoredMigrationVersion',
  'databaseRowCounts',
  'checkedObjectCount',
  'missingObjectCount',
  'unexpectedObjectCount',
  'sessionRevocationVerified',
  'rtoSeconds',
  'errorCategory',
])
const backupTriggers = new Set(['scheduled', 'manual', 'pre_release'])
const backupStates = new Set([
  'queued',
  'draining',
  'snapshotting',
  'encrypting',
  'verifying',
  'syncing',
  'succeeded',
  'degraded',
  'failed',
])
const artifactKinds = new Set([
  'database_dump',
  'object_snapshot',
  'manifest',
  'recovery_report',
])
const repositories = new Set(['local', 'remote'])
const restoreStates = new Set([
  'queued',
  'restoring',
  'checking',
  'succeeded',
  'failed',
])
const backupErrorCategories = new Set([
  'drain_timeout',
  'database_dump',
  'object_store_stop',
  'snapshot',
  'object_store_restart',
  'integrity',
  'remote_sync',
  'remote_unavailable',
  'retention',
  'lease_lost',
  'cancelled',
  'internal',
  'repository_integrity',
  'restore_database',
  'restore_object_store',
  'session_revocation',
  'readiness',
  'reference_check',
  'authorization_check',
  'timeout',
])
const restoreRowCountTables = new Set([
  'users',
  'sessions',
  'subjects',
  'grades',
  'terms',
  'chapters',
  'lessons',
  'lesson_revisions',
  'files',
  'file_versions',
  'file_previews',
  'qa_threads',
  'qa_messages',
  'ai_threads',
  'ai_messages',
  'ai_runs',
])

function exactKeys(source: Record<string, unknown>, allowed: Set<string>): void {
  if (Object.keys(source).some((key) => !allowed.has(key))) throw invalidResponse()
}

function safeInteger(value: unknown, minimum = 0): number {
  if (
    typeof value !== 'number'
    || !Number.isSafeInteger(value)
    || Object.is(value, -0)
    || value < minimum
  ) throw invalidResponse()
  return value
}

function safeBackupTimestamp(value: unknown): string {
  if (typeof value !== 'string' || !validCanonicalBackupTimestamp(value)) {
    throw invalidResponse()
  }
  return value
}

function safeEnum<T extends string>(value: unknown, values: Set<string>): T {
  if (typeof value !== 'string' || !values.has(value)) throw invalidResponse()
  return value as T
}

function safeErrorCategory(value: unknown): string {
  return safeEnum(value, backupErrorCategories)
}

function validCanonicalBackupTimestamp(value: string): boolean {
  const match = rfc3339Nano.exec(value)
  if (!match || match[8] !== 'Z' || !validTimestamp(value)) return false
  const fraction = match[7]
  return fraction === undefined || !fraction.endsWith('0')
}

function canonicalBackupTimestampKey(value: string): string {
  const fraction = rfc3339Nano.exec(value)?.[7] ?? ''
  return `${value.slice(0, 19)}.${fraction.padEnd(9, '0')}`
}

function parseBackupRunWithKeys(
  value: unknown,
  allowedKeys: Set<string>,
): BackupRun {
  const source = record(value)
  exactKeys(source, allowedKeys)
  if (
    typeof source.id !== 'string'
    || !canonicalUUID(source.id)
    || typeof source.requestedAt !== 'string'
    || !validCanonicalBackupTimestamp(source.requestedAt)
  ) throw invalidResponse()
  const result: BackupRun = {
    id: source.id,
    trigger: safeEnum(source.trigger, backupTriggers),
    state: safeEnum(source.state, backupStates),
    requestedAt: source.requestedAt,
  }
  for (const field of ['startedAt', 'finishedAt', 'localExpiresAt', 'remoteExpiresAt'] as const) {
    if (field in source) result[field] = safeBackupTimestamp(source[field])
  }
  for (const field of ['logicalBytes', 'storedBytes'] as const) {
    if (field in source) result[field] = safeInteger(source[field])
  }
  if ('errorCategory' in source) {
    result.errorCategory = safeErrorCategory(source.errorCategory)
  }
  return result
}

function parseBackupRun(value: unknown): BackupRun {
  return parseBackupRunWithKeys(value, backupSummaryKeys)
}

function parseBackupArtifact(value: unknown): BackupArtifact {
  const source = record(value)
  exactKeys(source, artifactKeys)
  if (
    typeof source.verifiedAt !== 'string'
    || !validCanonicalBackupTimestamp(source.verifiedAt)
    || typeof source.expiresAt !== 'string'
    || !validCanonicalBackupTimestamp(source.expiresAt)
  ) throw invalidResponse()
  return {
    kind: safeEnum(source.kind, artifactKinds),
    repository: safeEnum(source.repository, repositories),
    sizeBytes: safeInteger(source.sizeBytes),
    verifiedAt: source.verifiedAt,
    expiresAt: source.expiresAt,
  }
}

function parseRestoreRowCounts(value: unknown): Record<string, number> {
  const source = record(value)
  const result: Record<string, number> = {}
  let total = 0
  for (const [table, count] of Object.entries(source)) {
    if (!restoreRowCountTables.has(table)) throw invalidResponse()
    const safeCount = safeInteger(count)
    if (total > Number.MAX_SAFE_INTEGER - safeCount) throw invalidResponse()
    total += safeCount
    result[table] = safeCount
  }
  return result
}

function parseRestoreVerification(value: unknown): RestoreVerification {
  const source = record(value)
  exactKeys(source, restoreVerificationKeys)
  if (
    typeof source.id !== 'string'
    || !canonicalUUID(source.id)
    || typeof source.sessionRevocationVerified !== 'boolean'
  ) throw invalidResponse()
  const result: RestoreVerification = {
    id: source.id,
    state: safeEnum(source.state, restoreStates),
    databaseRowCounts: parseRestoreRowCounts(source.databaseRowCounts),
    checkedObjectCount: safeInteger(source.checkedObjectCount),
    missingObjectCount: safeInteger(source.missingObjectCount),
    unexpectedObjectCount: safeInteger(source.unexpectedObjectCount),
    sessionRevocationVerified: source.sessionRevocationVerified,
  }
  for (const field of ['startedAt', 'finishedAt'] as const) {
    if (field in source) result[field] = safeBackupTimestamp(source[field])
  }
  if ('restoredMigrationVersion' in source) {
    result.restoredMigrationVersion = safeInteger(source.restoredMigrationVersion, 1)
  }
  if ('rtoSeconds' in source) result.rtoSeconds = safeInteger(source.rtoSeconds)
  if ('errorCategory' in source) {
    result.errorCategory = safeErrorCategory(source.errorCategory)
  }
  const hasStarted = result.startedAt !== undefined
  const hasFinished = result.finishedAt !== undefined
  if (
    (result.state === 'queued' && (hasStarted || hasFinished))
    || (
      (result.state === 'restoring' || result.state === 'checking')
      && (!hasStarted || hasFinished)
    )
    || (
      (result.state === 'succeeded' || result.state === 'failed')
      && (
        !hasStarted
        || !hasFinished
        || canonicalBackupTimestampKey(result.finishedAt!)
          < canonicalBackupTimestampKey(result.startedAt!)
      )
    )
    || (
      result.state === 'succeeded'
      && (
        result.restoredMigrationVersion === undefined
        || Object.keys(result.databaseRowCounts).length === 0
        || result.missingObjectCount !== 0
        || !result.sessionRevocationVerified
        || result.rtoSeconds === undefined
      )
    )
  ) throw invalidResponse()
  return result
}

function parseBackupDetail(value: unknown): BackupRunDetail {
  const source = record(value)
  const summary = parseBackupRunWithKeys(source, backupDetailKeys)
  if (!Array.isArray(source.artifacts) || !Array.isArray(source.restoreVerifications)) {
    throw invalidResponse()
  }
  return {
    ...summary,
    artifacts: source.artifacts.map(parseBackupArtifact),
    restoreVerifications: source.restoreVerifications.map(parseRestoreVerification),
  }
}

function validBackupCursor(cursor: BackupCursor): boolean {
  return canonicalUUID(cursor.id) && validCanonicalBackupTimestamp(cursor.requestedAt)
}

export async function listBackups(
  filter: { before?: BackupCursor | null; limit?: number },
  signal?: AbortSignal,
): Promise<BackupPage> {
  const query = new URLSearchParams()
  if (filter.before) {
    if (!validBackupCursor(filter.before)) throw invalidResponse()
    query.set('beforeRequestedAt', filter.before.requestedAt)
    query.set('beforeId', filter.before.id)
  }
  if (filter.limit !== undefined) {
    if (!Number.isInteger(filter.limit) || filter.limit < 1 || filter.limit > 100) {
      throw invalidResponse()
    }
    query.set('limit', String(filter.limit))
  }
  const suffix = query.size ? `?${query.toString()}` : ''
  const response = await requestWithMeta<unknown>(
    `/admin/operations/backups${suffix}`,
    { signal },
  )
  if (!Array.isArray(response.data)) throw invalidResponse()
  const meta = response.meta === undefined ? {} : record(response.meta)
  exactKeys(meta, new Set(['nextBeforeRequestedAt', 'nextBeforeId']))
  const rawAt = meta.nextBeforeRequestedAt
  const rawID = meta.nextBeforeId
  if ((rawAt === undefined) !== (rawID === undefined)) throw invalidResponse()
  let next: BackupCursor | null = null
  if (rawAt !== undefined && rawID !== undefined) {
    if (
      typeof rawAt !== 'string'
      || !validCanonicalBackupTimestamp(rawAt)
      || typeof rawID !== 'string'
      || !canonicalUUID(rawID)
    ) throw invalidResponse()
    next = { requestedAt: rawAt, id: rawID }
  }
  return { items: response.data.map(parseBackupRun), next }
}

export async function readBackup(
  id: string,
  signal?: AbortSignal,
): Promise<BackupRunDetail> {
  if (!canonicalUUID(id)) throw invalidResponse()
  return parseBackupDetail(await request<unknown>(
    `/admin/operations/backups/${id}`,
    { signal },
  ))
}

export async function queueBackup(idempotencyKey: string): Promise<BackupRun> {
  if (
    !/^[A-Za-z0-9._:-]{8,128}$/.test(idempotencyKey)
    || !wellFormedUnicode(idempotencyKey)
  ) throw invalidResponse()
  return parseBackupRun(await request<unknown>('/admin/operations/backups', {
    method: 'POST',
    headers: { 'Idempotency-Key': idempotencyKey },
    json: {},
  }))
}

const operationsDataStates = new Set([
  'healthy',
  'degraded',
  'unavailable',
  'stale',
  'timeout',
  'empty',
])
const terminalOperationsDataStates = new Set<OperationsDataState>([
  'unavailable',
  'timeout',
  'empty',
])
const dashboardServices = new Set([
  'app',
  'caddy',
  'postgres',
  'redis',
  'object_store',
  'worker',
])
const dashboardQueues = new Set(['processing', 'ai', 'outbox'])
const recoveryStates = new Set(['succeeded', 'degraded', 'failed', 'empty'])
const dashboardAuditCategories = new Set([
  'authentication',
  'authorization',
  'files',
  'teaching',
  'ai',
  'operations',
  'backup',
])
const dashboardAuditOutcomes = new Set(['succeeded', 'failed', 'denied', 'rejected'])
const alertStates = new Set(['open', 'acknowledged', 'resolved'])
const alertSeverities = new Set(['warning', 'critical'])
const alertCategories = new Set(['storage', 'backup', 'ai', 'processing', 'security'])
const alertCursorPattern = /^[A-Za-z0-9_-]{1,256}$/
const alertIdentifierPattern = /^[a-z][a-z0-9_]{0,127}$/

function monitoringTime(value: unknown): string {
  if (typeof value !== 'string' || !validTimestamp(value)) throw invalidResponse()
  return value
}

function optionalMonitoringTime(
  source: Record<string, unknown>,
  key: string,
): string | undefined {
  if (!(key in source)) return undefined
  return monitoringTime(source[key])
}

function monitoringNumber(value: unknown, minimum = 0, maximum = Number.MAX_SAFE_INTEGER): number {
  if (
    typeof value !== 'number'
    || !Number.isFinite(value)
    || Object.is(value, -0)
    || value < minimum
    || value > maximum
  ) throw invalidResponse()
  return value
}

function observedState(
  source: Record<string, unknown>,
  allowed: Set<string>,
): { state: OperationsDataState; observedAt?: string } {
  const result: { state: OperationsDataState; observedAt?: string } = {
    state: safeEnum(source.state, allowed),
  }
  const observedAt = optionalMonitoringTime(source, 'observedAt')
  if (observedAt !== undefined) result.observedAt = observedAt
  return result
}

function parseDashboard(value: unknown): OperationsDashboard {
  const source = record(value)
  exactKeys(source, new Set([
    'observedAt',
    'releaseVersion',
    'students',
    'questions',
    'ai',
    'storage',
    'services',
    'queues',
    'backup',
    'alerts',
    'recentAuditState',
    'recentAudit',
  ]))

  const studentsSource = record(source.students)
  exactKeys(studentsSource, new Set(['state', 'observedAt', 'active', 'disabled']))
  const students = {
    ...observedState(studentsSource, operationsDataStates),
    active: safeInteger(studentsSource.active),
    disabled: safeInteger(studentsSource.disabled),
  }

  const questionsSource = record(source.questions)
  exactKeys(questionsSource, new Set(['state', 'observedAt', 'waiting', 'oldestWaitSeconds']))
  const questions = {
    ...observedState(questionsSource, operationsDataStates),
    waiting: safeInteger(questionsSource.waiting),
    oldestWaitSeconds: safeInteger(questionsSource.oldestWaitSeconds),
  }

  const aiSource = record(source.ai)
  exactKeys(aiSource, new Set([
    'state',
    'observedAt',
    'requests',
    'successRatePercent',
    'firstByteLatencyMilliseconds',
    'totalLatencyMilliseconds',
    'dailyCostMicroUSD',
  ]))
  const ai = {
    ...observedState(aiSource, operationsDataStates),
    requests: safeInteger(aiSource.requests),
    successRatePercent: monitoringNumber(aiSource.successRatePercent, 0, 100),
    firstByteLatencyMilliseconds: safeInteger(aiSource.firstByteLatencyMilliseconds),
    totalLatencyMilliseconds: safeInteger(aiSource.totalLatencyMilliseconds),
    dailyCostMicroUSD: safeInteger(aiSource.dailyCostMicroUSD),
  }

  const storageSource = record(source.storage)
  exactKeys(storageSource, new Set([
    'state',
    'observedAt',
    'usedBytes',
    'capacityBytes',
    'warningPercent',
  ]))
  const storageObservation = observedState(storageSource, operationsDataStates)
  const storage = {
    ...storageObservation,
    usedBytes: safeInteger(storageSource.usedBytes),
    capacityBytes: safeInteger(storageSource.capacityBytes),
    warningPercent: safeInteger(storageSource.warningPercent),
  }
  const terminalStorage = terminalOperationsDataStates.has(storageObservation.state)
  if (
    storage.warningPercent > 100
    || storage.usedBytes > storage.capacityBytes
    || (terminalStorage && (
      storage.usedBytes !== 0
      || storage.capacityBytes !== 0
      || storage.warningPercent !== 0
    ))
    || (!terminalStorage && storage.warningPercent < 1)
  ) {
    throw invalidResponse()
  }

  if (!Array.isArray(source.services) || source.services.length > dashboardServices.size) {
    throw invalidResponse()
  }
  const seenServices = new Set<DashboardService>()
  const services = source.services.map((value) => {
    const item = record(value)
    exactKeys(item, new Set(['service', 'state', 'observedAt', 'latencyMilliseconds']))
    const service = safeEnum<DashboardService>(item.service, dashboardServices)
    if (seenServices.has(service)) throw invalidResponse()
    seenServices.add(service)
    return {
      ...observedState(item, operationsDataStates),
      service,
      latencyMilliseconds: safeInteger(item.latencyMilliseconds),
    }
  })

  if (!Array.isArray(source.queues) || source.queues.length > dashboardQueues.size) {
    throw invalidResponse()
  }
  const seenQueues = new Set<DashboardQueue>()
  const queues = source.queues.map((value) => {
    const item = record(value)
    exactKeys(item, new Set([
      'queue',
      'state',
      'observedAt',
      'queued',
      'streaming',
      'failed',
      'expired',
    ]))
    const queue = safeEnum<DashboardQueue>(item.queue, dashboardQueues)
    if (seenQueues.has(queue)) throw invalidResponse()
    seenQueues.add(queue)
    return {
      ...observedState(item, operationsDataStates),
      queue,
      queued: safeInteger(item.queued),
      streaming: safeInteger(item.streaming),
      failed: safeInteger(item.failed),
      expired: safeInteger(item.expired),
    }
  })

  const backupSource = record(source.backup)
  exactKeys(backupSource, new Set(['state', 'observedAt', 'local', 'remote', 'restore']))
  const recoveryPoint = (
    value: unknown,
    extraKeys: string[] = [],
  ): { state: RecoveryState; completedAt?: string } => {
    const item = record(value)
    exactKeys(item, new Set(['state', 'completedAt', ...extraKeys]))
    const state = safeEnum<RecoveryState>(item.state, recoveryStates)
    const completedAt = optionalMonitoringTime(item, 'completedAt')
    if ((state === 'empty') === (completedAt !== undefined)) throw invalidResponse()
    return completedAt === undefined ? { state } : { state, completedAt }
  }
  const restoreSource = record(backupSource.restore)
  exactKeys(restoreSource, new Set(['state', 'completedAt', 'rtoSeconds']))
  const restorePoint = recoveryPoint(restoreSource, ['rtoSeconds'])
  const backup = {
    ...observedState(backupSource, operationsDataStates),
    local: recoveryPoint(backupSource.local),
    remote: recoveryPoint(backupSource.remote),
    restore: {
      ...restorePoint,
      rtoSeconds: safeInteger(restoreSource.rtoSeconds),
    },
  }

  const alertSource = record(source.alerts)
  exactKeys(alertSource, new Set(['state', 'observedAt', 'openWarning', 'openCritical']))
  const alerts = {
    ...observedState(alertSource, operationsDataStates),
    openWarning: safeInteger(alertSource.openWarning),
    openCritical: safeInteger(alertSource.openCritical),
  }

  if (!Array.isArray(source.recentAudit) || source.recentAudit.length > 10) {
    throw invalidResponse()
  }
  const recentAudit = source.recentAudit.map((value) => {
    const item = record(value)
    exactKeys(item, new Set(['category', 'outcome', 'occurredAt']))
    return {
      category: safeEnum<DashboardAuditCategory>(item.category, dashboardAuditCategories),
      outcome: safeEnum<DashboardAuditOutcome>(item.outcome, dashboardAuditOutcomes),
      occurredAt: monitoringTime(item.occurredAt),
    }
  })

  return {
    observedAt: monitoringTime(source.observedAt),
    releaseVersion: safeReleaseVersion(source.releaseVersion),
    students,
    questions,
    ai,
    storage,
    services,
    queues,
    backup,
    alerts,
    recentAuditState: safeEnum<OperationsDataState>(
      source.recentAuditState,
      operationsDataStates,
    ),
    recentAudit,
  }
}

function safeReleaseVersion(value: unknown): string {
  if (
    typeof value !== 'string'
    || value.length > 128
    || !/^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.test(value)
  ) throw invalidResponse()
  return value
}

function parseOperationalAlert(value: unknown): OperationalAlert {
  const source = record(value)
  exactKeys(source, new Set([
    'id',
    'dedupeKey',
    'category',
    'severity',
    'state',
    'firstObservedAt',
    'lastObservedAt',
    'acknowledgedBy',
    'acknowledgedAt',
    'resolvedAt',
    'currentValue',
    'thresholdValue',
    'summary',
  ]))
  if (
    typeof source.id !== 'string'
    || !canonicalUUID(source.id)
    || typeof source.dedupeKey !== 'string'
    || !alertIdentifierPattern.test(source.dedupeKey)
    || typeof source.summary !== 'string'
    || !wellFormedUnicode(source.summary)
    || [...source.summary].length < 1
    || [...source.summary].length > 240
  ) throw invalidResponse()
  const result: OperationalAlert = {
    id: source.id,
    dedupeKey: source.dedupeKey,
    category: safeEnum<AlertCategory>(source.category, alertCategories),
    severity: safeEnum<AlertSeverity>(source.severity, alertSeverities),
    state: safeEnum<AlertState>(source.state, alertStates),
    firstObservedAt: monitoringTime(source.firstObservedAt),
    lastObservedAt: monitoringTime(source.lastObservedAt),
    currentValue: monitoringNumber(
      source.currentValue,
      -Number.MAX_SAFE_INTEGER,
      Number.MAX_SAFE_INTEGER,
    ),
    thresholdValue: monitoringNumber(
      source.thresholdValue,
      -Number.MAX_SAFE_INTEGER,
      Number.MAX_SAFE_INTEGER,
    ),
    summary: source.summary,
  }
  for (const key of ['acknowledgedAt', 'resolvedAt'] as const) {
    const value = optionalMonitoringTime(source, key)
    if (value !== undefined) result[key] = value
  }
  if ('acknowledgedBy' in source) {
    if (typeof source.acknowledgedBy !== 'string' || !canonicalUUID(source.acknowledgedBy)) {
      throw invalidResponse()
    }
    result.acknowledgedBy = source.acknowledgedBy
  }
  if (
    Date.parse(result.lastObservedAt) < Date.parse(result.firstObservedAt)
    || (result.state === 'open' && (
      result.acknowledgedAt !== undefined
      || result.acknowledgedBy !== undefined
      || result.resolvedAt !== undefined
    ))
    || (result.state === 'acknowledged' && (
      result.acknowledgedAt === undefined
      || result.acknowledgedBy === undefined
      || result.resolvedAt !== undefined
    ))
    || (result.state === 'resolved' && result.resolvedAt === undefined)
  ) throw invalidResponse()
  return result
}

function validAlertFilters(filters: AlertFilters): void {
  if (filters.state !== undefined && !alertStates.has(filters.state)) throw invalidResponse()
  if (filters.severity !== undefined && !alertSeverities.has(filters.severity)) throw invalidResponse()
  if (filters.category !== undefined && !alertCategories.has(filters.category)) throw invalidResponse()
  if (
    filters.before !== undefined
    && !alertCursorPattern.test(filters.before)
  ) throw invalidResponse()
  if (
    filters.limit !== undefined
    && (!Number.isInteger(filters.limit) || filters.limit < 1 || filters.limit > 100)
  ) throw invalidResponse()
}

export async function readDashboard(signal?: AbortSignal): Promise<OperationsDashboard> {
  return parseDashboard(await request<unknown>('/admin/operations/dashboard', { signal }))
}

export async function listAlerts(
  filters: AlertFilters,
  signal?: AbortSignal,
): Promise<AlertPage> {
  validAlertFilters(filters)
  const query = new URLSearchParams()
  for (const key of ['state', 'severity', 'category', 'before'] as const) {
    const value = filters[key]
    if (value !== undefined) query.set(key, value)
  }
  if (filters.limit !== undefined) query.set('limit', String(filters.limit))
  const suffix = query.size ? `?${query.toString()}` : ''
  const response = await requestWithMeta<unknown>(
    `/admin/operations/alerts${suffix}`,
    { signal },
  )
  if (!Array.isArray(response.data)) throw invalidResponse()
  const meta = response.meta === undefined ? {} : record(response.meta)
  exactKeys(meta, new Set(['next']))
  const next = meta.next
  if (next !== undefined && (
    typeof next !== 'string'
    || !alertCursorPattern.test(next)
  )) throw invalidResponse()
  return {
    items: response.data.map(parseOperationalAlert),
    next: next ?? null,
  }
}

export async function acknowledgeAlert(id: string): Promise<OperationalAlert> {
  if (!canonicalUUID(id)) throw invalidResponse()
  return parseOperationalAlert(await request<unknown>(
    `/admin/operations/alerts/${id}/acknowledge`,
    { method: 'POST' },
  ))
}
