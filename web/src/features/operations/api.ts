import { APIError, request, requestWithMeta } from '../../api/client'
import type {
  AuditFilters,
  AuditMetadata,
  AuditPage,
  AuditRecord,
  OperationsSettings,
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
    aiErrorWarningPercent: settingsInteger(source.aiErrorWarningPercent),
    aiErrorCriticalPercent: settingsInteger(source.aiErrorCriticalPercent),
    processingQueueWarning: settingsInteger(source.processingQueueWarning),
    processingQueueCritical: settingsInteger(source.processingQueueCritical),
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
    || result.aiErrorWarningPercent < 1
    || result.aiErrorWarningPercent > 99
    || result.aiErrorCriticalPercent <= result.aiErrorWarningPercent
    || result.aiErrorCriticalPercent > 100
    || result.processingQueueWarning < 1
    || result.processingQueueCritical <= result.processingQueueWarning
    || !validTimestamp(result.updatedAt)
  ) throw invalidResponse()
  return result
}

export async function readSettings(signal?: AbortSignal): Promise<OperationsSettings> {
  return parseSettings(await request<unknown>('/admin/operations/settings', { signal }))
}

export async function saveSettings(value: OperationsSettings): Promise<OperationsSettings> {
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
