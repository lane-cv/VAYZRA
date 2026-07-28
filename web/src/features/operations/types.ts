export type OperationsSettings = {
  version: number
  siteName: string
  siteAnnouncement: string
  softDeleteRetentionDays: number
  auditRetentionDays: number
  operationalSampleRetentionDays: number
  backupHour: number
  backupMinute: number
  backupTimezone: 'Asia/Shanghai'
  diskWarningPercent: number
  diskCriticalPercent: number
  aiErrorWarningPercent: number
  aiErrorCriticalPercent: number
  processingQueueWarning: number
  processingQueueCritical: number
  updatedAt: string
}

export type AuditMetadata = Partial<{
  status: string
  reason: string
  version: number | string
  count: number | string
  provider_id: string
  model_id: string
  file_purpose: string
}>

export type AuditRecord = {
  id: number
  actorId: string
  action: string
  targetType: string
  targetId: string
  metadata: AuditMetadata
  occurredAt: string
}

export type AuditFilters = {
  action?: string
  targetType?: string
  outcome?: string
  actorId?: string
  from?: string
  to?: string
  beforeId?: number
  limit?: number
}

export type AuditPage = { items: AuditRecord[]; nextBeforeId: number | null }
