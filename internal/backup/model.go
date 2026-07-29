package backup

import (
	"errors"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

type State string

const (
	StateQueued       State = "queued"
	StateDraining     State = "draining"
	StateSnapshotting State = "snapshotting"
	StateEncrypting   State = "encrypting"
	StateVerifying    State = "verifying"
	StateSyncing      State = "syncing"
	StateSucceeded    State = "succeeded"
	StateDegraded     State = "degraded"
	StateFailed       State = "failed"
)

type Trigger string

const (
	TriggerScheduled  Trigger = "scheduled"
	TriggerManual     Trigger = "manual"
	TriggerPreRelease Trigger = "pre_release"
)

type ArtifactKind string

const (
	ArtifactDatabaseDump   ArtifactKind = "database_dump"
	ArtifactObjectSnapshot ArtifactKind = "object_snapshot"
	ArtifactManifest       ArtifactKind = "manifest"
	ArtifactRecoveryReport ArtifactKind = "recovery_report"
)

type Repository string

const (
	RepositoryLocal  Repository = "local"
	RepositoryRemote Repository = "remote"
)

type RestoreState string

const (
	RestoreQueued    RestoreState = "queued"
	RestoreRestoring RestoreState = "restoring"
	RestoreChecking  RestoreState = "checking"
	RestoreSucceeded RestoreState = "succeeded"
	RestoreFailed    RestoreState = "failed"
)

var (
	ErrForbidden         = errors.New("backup forbidden")
	ErrInvalid           = errors.New("invalid backup input")
	ErrNotFound          = errors.New("backup not found")
	ErrAlreadyQueued     = errors.New("backup already queued")
	ErrUnavailable       = errors.New("backup unavailable")
	ErrNoClaimableRun    = errors.New("no claimable backup run")
	ErrActiveClaim       = errors.New("backup run already claimed")
	ErrStaleOwner        = errors.New("stale backup owner")
	ErrInvalidTransition = errors.New("invalid backup transition")
)

var nextStates = map[State]map[State]bool{
	StateQueued:       {StateDraining: true, StateFailed: true},
	StateDraining:     {StateSnapshotting: true, StateFailed: true},
	StateSnapshotting: {StateEncrypting: true, StateFailed: true},
	StateEncrypting:   {StateVerifying: true, StateFailed: true},
	StateVerifying:    {StateSyncing: true, StateSucceeded: true, StateFailed: true},
	StateSyncing:      {StateSucceeded: true, StateDegraded: true, StateFailed: true},
}

func ValidTransition(from, to State) bool {
	return nextStates[from][to]
}

type Run struct {
	ID                       uuid.UUID
	IdempotencyKey           string
	Trigger                  Trigger
	State                    State
	RequestedBy              uuid.UUID
	RequestedAt              time.Time
	StartedAt                *time.Time
	FinishedAt               *time.Time
	DatabaseMigrationVersion *int64
	EncryptionKeyID          string
	LocalSnapshotID          string
	RemoteSnapshotID         string
	ManifestSHA256           []byte
	LogicalBytes             *int64
	StoredBytes              *int64
	LocalExpiresAt           *time.Time
	RemoteExpiresAt          *time.Time
	ErrorCategory            string
	ErrorTraceID             string
	OwnerID                  uuid.UUID
	LeaseExpiresAt           *time.Time
	LeaseGeneration          int64
}

type RunSummary struct {
	ID              uuid.UUID
	Trigger         Trigger
	State           State
	RequestedAt     time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
	LogicalBytes    *int64
	StoredBytes     *int64
	LocalExpiresAt  *time.Time
	RemoteExpiresAt *time.Time
	ErrorCategory   string
}

func (r Run) Summary() RunSummary {
	return RunSummary{
		ID: r.ID, Trigger: r.Trigger, State: r.State,
		RequestedAt: r.RequestedAt, StartedAt: cloneTime(r.StartedAt),
		FinishedAt:   cloneTime(r.FinishedAt),
		LogicalBytes: cloneInt64(r.LogicalBytes), StoredBytes: cloneInt64(r.StoredBytes),
		LocalExpiresAt: cloneTime(r.LocalExpiresAt), RemoteExpiresAt: cloneTime(r.RemoteExpiresAt),
		ErrorCategory: r.ErrorCategory,
	}
}

type Artifact struct {
	BackupRunID     uuid.UUID
	OwnerID         uuid.UUID
	LeaseGeneration int64
	Kind            ArtifactKind
	Repository      Repository
	SnapshotID      string
	SHA256          []byte
	SizeBytes       int64
	VerifiedAt      time.Time
	ExpiresAt       time.Time
}

type RestoreVerification struct {
	ID                        uuid.UUID
	BackupRunID               uuid.UUID
	State                     RestoreState
	StartedAt                 *time.Time
	FinishedAt                *time.Time
	RestoredMigrationVersion  *int64
	DatabaseRowCounts         map[string]int64
	CheckedObjectCount        int64
	MissingObjectCount        int64
	UnexpectedObjectCount     int64
	SessionRevocationVerified bool
	RTOSeconds                *int64
	ReportSHA256              []byte
	ErrorCategory             string
	ErrorTraceID              string
}

type RestoreSuccessInput struct {
	VerificationID            uuid.UUID
	BackupRunID               uuid.UUID
	ManifestSHA256            []byte
	StartedAt                 time.Time
	FinishedAt                time.Time
	RestoredMigrationVersion  int64
	DatabaseRowCounts         map[string]int64
	CheckedObjectCount        int64
	SessionRevocationVerified bool
	RTOSeconds                int64
	ReportSHA256              []byte
}

type RunDetail struct {
	Run                  Run
	Artifacts            []Artifact
	RestoreVerifications []RestoreVerification
}

type CreateInput struct {
	ID             uuid.UUID
	Trigger        Trigger
	IdempotencyKey string
	RequestedBy    uuid.UUID
	RequestedAt    time.Time
	RequestID      string
	IP             net.IP
}

type RecoveryEvidence struct {
	DatabaseMigrationVersion int64
	EncryptionKeyID          string
	LocalSnapshotID          string
	RemoteSnapshotID         string
	ManifestSHA256           []byte
	LogicalBytes             *int64
	StoredBytes              *int64
	LocalExpiresAt           time.Time
	RemoteExpiresAt          *time.Time
}

type TransitionInput struct {
	RunID           uuid.UUID
	OwnerID         uuid.UUID
	LeaseGeneration int64
	From            State
	To              State
	At              time.Time
	Evidence        *RecoveryEvidence
	ErrorCategory   string
	ErrorTraceID    string
}

type CompletionInput struct {
	RunID            uuid.UUID
	OwnerID          uuid.UUID
	LeaseGeneration  int64
	From             State
	Evidence         RecoveryEvidence
	RemoteConfigured bool
	RemoteSucceeded  bool
	ErrorCategory    string
	ErrorTraceID     string
}

type Cursor struct {
	RequestedAt time.Time
	ID          uuid.UUID
}

func (c Cursor) IsZero() bool {
	return c.RequestedAt.IsZero() && c.ID == uuid.Nil
}

type Filter struct {
	Before Cursor
	Limit  int
}

type Page struct {
	Items []RunSummary
	Next  Cursor
}

type RetentionPolicy struct {
	Now                  time.Time
	Location             *time.Location
	LocalDaily           int
	RemoteDaily          int
	RemoteMonthly        int
	PreReleaseProtectFor time.Duration
	CurrentRunID         uuid.UUID
}

var safeOpaqueValue = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

var safeErrorCategories = map[string]struct{}{
	"drain_timeout": {}, "database_dump": {}, "object_store_stop": {},
	"snapshot": {}, "object_store_restart": {}, "integrity": {},
	"remote_sync": {}, "remote_unavailable": {}, "retention": {},
	"lease_lost": {}, "cancelled": {}, "internal": {},
	"repository_integrity": {}, "restore_database": {},
	"restore_object_store": {}, "session_revocation": {},
	"readiness": {}, "reference_check": {}, "authorization_check": {},
	"timeout": {},
}

var allowedRestoreRowCountTables = map[string]struct{}{
	"users": {}, "sessions": {},
	"subjects": {}, "grades": {}, "terms": {}, "chapters": {},
	"lessons": {}, "lesson_revisions": {},
	"files": {}, "file_versions": {}, "file_previews": {},
	"qa_threads": {}, "qa_messages": {},
	"ai_threads": {}, "ai_messages": {}, "ai_runs": {},
}

func validTrigger(trigger Trigger) bool {
	return trigger == TriggerScheduled || trigger == TriggerManual || trigger == TriggerPreRelease
}

func validState(state State) bool {
	switch state {
	case StateQueued, StateDraining, StateSnapshotting, StateEncrypting,
		StateVerifying, StateSyncing, StateSucceeded, StateDegraded, StateFailed:
		return true
	default:
		return false
	}
}

func validateCreate(input CreateInput) error {
	if input.ID == uuid.Nil || !validTrigger(input.Trigger) ||
		!safeIdempotencyKey(input.IdempotencyKey) ||
		input.RequestedAt.IsZero() {
		return ErrInvalid
	}
	if input.Trigger == TriggerManual {
		if input.RequestedBy == uuid.Nil ||
			strings.TrimSpace(input.RequestID) == "" ||
			len(input.RequestID) > 64 ||
			input.IP == nil || input.IP.To16() == nil {
			return ErrInvalid
		}
	} else if input.RequestedBy != uuid.Nil ||
		input.RequestID != "" || input.IP != nil {
		return ErrInvalid
	}
	return nil
}

func safeIdempotencyKey(value string) bool {
	return len(value) >= 8 && len(value) <= 128 && safeOpaqueValue.MatchString(value)
}

func validateEvidence(evidence RecoveryEvidence) error {
	if evidence.DatabaseMigrationVersion < 1 ||
		!safeOpaqueValue.MatchString(evidence.EncryptionKeyID) ||
		!safeOpaqueValue.MatchString(evidence.LocalSnapshotID) ||
		len(evidence.ManifestSHA256) != 32 ||
		evidence.LocalExpiresAt.IsZero() ||
		(evidence.RemoteSnapshotID != "" && !safeOpaqueValue.MatchString(evidence.RemoteSnapshotID)) ||
		(evidence.LogicalBytes != nil && *evidence.LogicalBytes < 0) ||
		(evidence.StoredBytes != nil && *evidence.StoredBytes < 0) ||
		(evidence.RemoteExpiresAt != nil && evidence.RemoteExpiresAt.IsZero()) {
		return ErrInvalid
	}
	return nil
}

func validateArtifact(artifact Artifact) error {
	if artifact.BackupRunID == uuid.Nil || artifact.OwnerID == uuid.Nil ||
		artifact.LeaseGeneration < 1 ||
		!validArtifactKind(artifact.Kind) || !validRepository(artifact.Repository) ||
		!safeOpaqueValue.MatchString(artifact.SnapshotID) ||
		len(artifact.SHA256) != 32 || artifact.SizeBytes < 0 ||
		artifact.VerifiedAt.IsZero() || artifact.ExpiresAt.IsZero() {
		return ErrInvalid
	}
	return nil
}

func validArtifactKind(kind ArtifactKind) bool {
	switch kind {
	case ArtifactDatabaseDump, ArtifactObjectSnapshot, ArtifactManifest, ArtifactRecoveryReport:
		return true
	default:
		return false
	}
}

func validRepository(repository Repository) bool {
	return repository == RepositoryLocal || repository == RepositoryRemote
}

func validSafeError(category, traceID string) bool {
	if category != "" {
		if !ValidErrorCategory(category) {
			return false
		}
	}
	return traceID == "" || (len(traceID) <= 64 && safeOpaqueValue.MatchString(traceID))
}

func ValidErrorCategory(category string) bool {
	_, ok := safeErrorCategories[category]
	return ok
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func normalizeRun(run Run) Run {
	run.RequestedAt = run.RequestedAt.UTC()
	run.StartedAt = cloneTime(run.StartedAt)
	run.FinishedAt = cloneTime(run.FinishedAt)
	run.LocalExpiresAt = cloneTime(run.LocalExpiresAt)
	run.RemoteExpiresAt = cloneTime(run.RemoteExpiresAt)
	run.LeaseExpiresAt = cloneTime(run.LeaseExpiresAt)
	run.ManifestSHA256 = append([]byte(nil), run.ManifestSHA256...)
	return run
}

func safeRowCounts(counts map[string]int64) map[string]int64 {
	if len(counts) == 0 {
		return map[string]int64{}
	}
	safe := make(map[string]int64)
	for table, count := range counts {
		if _, ok := allowedRestoreRowCountTables[table]; ok && count >= 0 {
			safe[table] = count
		}
	}
	return safe
}

func validRestoreRowCounts(counts map[string]int64) bool {
	if counts == nil {
		return false
	}
	for table, count := range counts {
		if _, ok := allowedRestoreRowCountTables[table]; !ok || count < 0 {
			return false
		}
	}
	return true
}

func safeText(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if _, ok := safeErrorCategories[value]; ok {
		return value
	}
	return ""
}
