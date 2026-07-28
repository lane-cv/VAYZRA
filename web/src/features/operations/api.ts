import { APIError, request, requestWithMeta } from '../../api/client'
import type {
  AuditFilters,
  AuditMetadata,
  AuditPage,
  AuditRecord,
  OperationsSettings,
} from './types'

const settingsKeys = [
  'version',
  'siteName',
  'siteAnnouncement',
  'softDeleteRetentionDays',
  'auditRetentionDays',
  'operationalSampleRetentionDays',
  'backupHour',
  'backupMinute',
  'backupTimezone',
  'diskWarningPercent',
  'diskCriticalPercent',
  'aiErrorWarningPercent',
  'aiErrorCriticalPercent',
  'processingQueueWarning',
  'processingQueueCritical',
  'updatedAt',
] as const satisfies ReadonlyArray<keyof OperationsSettings>

const integerSettings = new Set<keyof OperationsSettings>([
  'version',
  'softDeleteRetentionDays',
  'auditRetentionDays',
  'operationalSampleRetentionDays',
  'backupHour',
  'backupMinute',
  'diskWarningPercent',
  'diskCriticalPercent',
  'aiErrorWarningPercent',
  'aiErrorCriticalPercent',
  'processingQueueWarning',
  'processingQueueCritical',
])

function invalidResponse(): APIError {
  return new APIError(200, 'invalid_response', '服务响应异常，请稍后重试', '')
}

function record(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw invalidResponse()
  return value as Record<string, unknown>
}

function parseSettings(value: unknown): OperationsSettings {
  const source = record(value)
  const result: Record<string, unknown> = {}
  for (const key of settingsKeys) {
    const field = source[key]
    if (integerSettings.has(key)) {
      if (typeof field !== 'number' || !Number.isInteger(field)) throw invalidResponse()
    } else if (typeof field !== 'string') {
      throw invalidResponse()
    }
    result[key] = field
  }
  if (
    result.backupTimezone !== 'Asia/Shanghai'
    || typeof result.updatedAt !== 'string'
    || !validTimestamp(result.updatedAt)
  ) throw invalidResponse()
  return result as OperationsSettings
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
const canonicalUUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/
const maxAuditVersion = '9223372036854775807'
const maxAuditCount = '1000000000'

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
    if ((key === 'provider_id' || key === 'model_id') && canonicalUUID.test(field)) result[key] = field
    if (key === 'file_purpose' && publicFilePurposes.has(field)) result.file_purpose = field
  }
  return result
}

function parseAuditRecord(value: unknown): AuditRecord {
  const source = record(value)
  if (
    typeof source.id !== 'number'
    || !Number.isInteger(source.id)
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
    actorId: typeof source.actorId === 'string' && canonicalUUID.test(source.actorId) ? source.actorId : '',
    action: source.action,
    targetType: source.targetType,
    targetId: typeof source.targetId === 'string' && (
      source.targetId === 'global'
      || source.targetId === 'unresolved'
      || canonicalUUID.test(source.targetId)
    ) ? source.targetId : '',
    metadata: parseMetadata(source.metadata),
    occurredAt: source.occurredAt,
  }
}

function validTimestamp(value: string): boolean {
  return value.trim() === value && value !== '' && Number.isFinite(Date.parse(value))
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
    || !Number.isInteger(nextBeforeId)
    || nextBeforeId < 1
  )) throw invalidResponse()
  return {
    items: response.data.map(parseAuditRecord),
    nextBeforeId: nextBeforeId ?? null,
  }
}
