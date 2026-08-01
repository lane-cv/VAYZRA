export type InfrastructureKey =
  | 'application_database'
  | 'redis_security'
  | 'object_store'
  | 'ai_encryption'
  | 'internal_metrics'
  | 'host_metrics_ingestion'
  | 'alert_webhook'
  | 'local_backup'
  | 'remote_backup'

export type InfrastructureStatus = {
  key: InfrastructureKey
  configured: boolean
  lastValidatedAt: string | null
}

export type OperationsSettingsUpdate = {
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
  backupFilesystemWarningPercent: number
  backupFilesystemCriticalPercent: number
  localBackupAgeWarningHours: number
  localBackupAgeCriticalHours: number
  aiErrorWarningPercent: number
  aiErrorCriticalPercent: number
  processingQueueWarning: number
  processingQueueCritical: number
  processingFailureWarningCount: number
  processingFailureCriticalCount: number
  loginFailureWarningCount: number
  loginFailureCriticalCount: number
  authorizationDenialWarningCount: number
  authorizationDenialCriticalCount: number
}

export type OperationsSettings = OperationsSettingsUpdate & {
  infrastructure: InfrastructureStatus[]
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

export type OperationsDataState =
  | 'healthy'
  | 'degraded'
  | 'unavailable'
  | 'stale'
  | 'timeout'
  | 'empty'
export type DashboardService =
  | 'app'
  | 'caddy'
  | 'postgres'
  | 'redis'
  | 'object_store'
  | 'worker'
export type DashboardQueue = 'processing' | 'ai' | 'outbox'
export type RecoveryState = 'succeeded' | 'degraded' | 'failed' | 'empty'
export type DashboardAuditCategory =
  | 'authentication'
  | 'authorization'
  | 'files'
  | 'teaching'
  | 'ai'
  | 'operations'
  | 'backup'
export type DashboardAuditOutcome = 'succeeded' | 'failed' | 'denied' | 'rejected'

export type ObservedSummary = {
  state: OperationsDataState
  observedAt?: string
}

export type OperationsDashboard = {
  observedAt: string
  students: ObservedSummary & { active: number; disabled: number }
  questions: ObservedSummary & { waiting: number; oldestWaitSeconds: number }
  ai: ObservedSummary & {
    requests: number
    successRatePercent: number
    firstByteLatencyMilliseconds: number
    totalLatencyMilliseconds: number
    dailyCostMicroUSD: number
  }
  storage: ObservedSummary & {
    usedBytes: number
    capacityBytes: number
    warningPercent: number
  }
  services: Array<ObservedSummary & {
    service: DashboardService
    latencyMilliseconds: number
  }>
  queues: Array<ObservedSummary & {
    queue: DashboardQueue
    queued: number
    streaming: number
    failed: number
    expired: number
  }>
  backup: ObservedSummary & {
    local: { state: RecoveryState; completedAt?: string }
    remote: { state: RecoveryState; completedAt?: string }
    restore: { state: RecoveryState; completedAt?: string; rtoSeconds: number }
  }
  alerts: ObservedSummary & {
    openWarning: number
    openCritical: number
  }
  recentAuditState: OperationsDataState
  recentAudit: Array<{
    category: DashboardAuditCategory
    outcome: DashboardAuditOutcome
    occurredAt: string
  }>
}

export type AlertSeverity = 'warning' | 'critical'
export type AlertState = 'open' | 'acknowledged' | 'resolved'
export type AlertCategory = 'storage' | 'backup' | 'ai' | 'processing' | 'security'

export type OperationalAlert = {
  id: string
  dedupeKey: string
  category: AlertCategory
  severity: AlertSeverity
  state: AlertState
  firstObservedAt: string
  lastObservedAt: string
  acknowledgedBy?: string
  acknowledgedAt?: string
  resolvedAt?: string
  currentValue: number
  thresholdValue: number
  summary: string
}

export type AlertFilters = {
  state?: AlertState
  severity?: AlertSeverity
  category?: AlertCategory
  before?: string
  limit?: number
}

export type AlertPage = {
  items: OperationalAlert[]
  next: string | null
}
