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

export type BackupTrigger = 'scheduled' | 'manual' | 'pre_release'
export type BackupState =
  | 'queued'
  | 'draining'
  | 'snapshotting'
  | 'encrypting'
  | 'verifying'
  | 'syncing'
  | 'succeeded'
  | 'degraded'
  | 'failed'
export type BackupArtifactKind =
  | 'database_dump'
  | 'object_snapshot'
  | 'manifest'
  | 'recovery_report'
export type BackupRepository = 'local' | 'remote'
export type RestoreVerificationState =
  | 'queued'
  | 'restoring'
  | 'checking'
  | 'succeeded'
  | 'failed'

export type BackupRun = {
  id: string
  trigger: BackupTrigger
  state: BackupState
  requestedAt: string
  startedAt?: string
  finishedAt?: string
  logicalBytes?: number
  storedBytes?: number
  localExpiresAt?: string
  remoteExpiresAt?: string
  errorCategory?: string
}

export type BackupCursor = {
  requestedAt: string
  id: string
}

export type BackupPage = {
  items: BackupRun[]
  next: BackupCursor | null
}

export type BackupArtifact = {
  kind: BackupArtifactKind
  repository: BackupRepository
  sizeBytes: number
  verifiedAt: string
  expiresAt: string
}

export type RestoreVerification = {
  id: string
  state: RestoreVerificationState
  startedAt?: string
  finishedAt?: string
  restoredMigrationVersion?: number
  databaseRowCounts: Record<string, number>
  checkedObjectCount: number
  missingObjectCount: number
  unexpectedObjectCount: number
  sessionRevocationVerified: boolean
  rtoSeconds?: number
  errorCategory?: string
}

export type BackupRunDetail = BackupRun & {
  artifacts: BackupArtifact[]
  restoreVerifications: RestoreVerification[]
}
