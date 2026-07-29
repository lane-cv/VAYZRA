package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
)

var (
	ErrRestoreSessionRevocation       = errors.New("restored sessions remain active")
	ErrRestoreObjectIntegrity         = errors.New("restored object integrity verification failed")
	ErrRestoreObjectNotFound          = errors.New("restored object not found")
	ErrRestoreUnexpectedObject        = errors.New("unexpected restored object reference")
	ErrRestoreUnexpectedAuthorization = errors.New("unexpected restored object authorization")
	ErrRestoreUnsafeReportValue       = errors.New("unsafe restore report value")
	ErrRestoreVerifierConfiguration   = errors.New("invalid restore verifier configuration")
)

// restoreRowCountAllowlist is deliberately fixed. Database adapters receive
// only these table names and must not accept a caller-controlled table name.
var restoreRowCountAllowlist = [...]string{
	"users", "sessions",
	"subjects", "grades", "terms", "chapters",
	"lessons", "lesson_revisions",
	"files", "file_versions", "file_previews",
	"qa_threads", "qa_messages",
	"ai_threads", "ai_messages", "ai_runs",
}

type RestoreRepository string

const (
	RestoreOriginals RestoreRepository = "originals"
	RestorePreviews  RestoreRepository = "previews"
)

type RestoreReferenceSource string

const (
	RestoreFileVersion        RestoreReferenceSource = "file_versions"
	RestoreFilePreview        RestoreReferenceSource = "file_previews"
	RestoreProcessingArtifact RestoreReferenceSource = "file_processing_artifacts"
)

type RestoreObjectReference struct {
	Source     RestoreReferenceSource
	Repository RestoreRepository
	ObjectKey  string
	Size       int64
}

// RestoreVerificationDatabase is implemented by the restored PostgreSQL
// adapter. ForEachLiveObject must visit every live file_versions,
// file_previews, and file_processing_artifacts object reference exactly once.
type RestoreVerificationDatabase interface {
	ActiveSessionCount(context.Context) (int64, error)
	MigrationVersion(context.Context) (int64, error)
	CountRows(context.Context, string) (int64, error)
	ForEachLiveObject(context.Context, func(RestoreObjectReference) error) error
}

// RestoreVerificationObjects is implemented by an authenticated AIStor
// adapter. ErrRestoreObjectNotFound distinguishes absence from operational
// failures, which remain available through errors.Is without entering reports.
type RestoreVerificationObjects interface {
	Stat(context.Context, string) (int64, error)
}

type RestoreVerificationResult struct {
	RestoredMigrationVersion  int64
	DatabaseRowCounts         map[string]int64
	CheckedObjectCount        int64
	MissingObjectCount        int64
	UnexpectedObjectCount     int64
	SessionRevocationVerified bool
	ReportSHA256              []byte
}

type RestoreVerifier struct {
	database  RestoreVerificationDatabase
	originals RestoreVerificationObjects
	previews  RestoreVerificationObjects
}

func NewRestoreVerifier(
	database RestoreVerificationDatabase,
	originals RestoreVerificationObjects,
	previews RestoreVerificationObjects,
) *RestoreVerifier {
	return &RestoreVerifier{
		database:  database,
		originals: originals,
		previews:  previews,
	}
}

func (verifier *RestoreVerifier) Verify(ctx context.Context) (RestoreVerificationResult, error) {
	result := RestoreVerificationResult{
		DatabaseRowCounts: make(map[string]int64, len(restoreRowCountAllowlist)),
	}
	if ctx == nil || verifier == nil || verifier.database == nil ||
		verifier.originals == nil || verifier.previews == nil {
		return result, ErrRestoreVerifierConfiguration
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	activeSessions, err := verifier.database.ActiveSessionCount(ctx)
	if err != nil {
		return result, safeRestoreDependencyError("verify restored sessions", err)
	}
	if activeSessions < 0 {
		return result, ErrRestoreUnsafeReportValue
	}
	if activeSessions != 0 {
		return result, ErrRestoreSessionRevocation
	}
	result.SessionRevocationVerified = true

	if err := ctx.Err(); err != nil {
		return result, err
	}
	migrationVersion, err := verifier.database.MigrationVersion(ctx)
	if err != nil {
		return result, safeRestoreDependencyError("read restored migration version", err)
	}
	if migrationVersion < 1 {
		return result, ErrRestoreUnsafeReportValue
	}
	result.RestoredMigrationVersion = migrationVersion

	for _, table := range restoreRowCountAllowlist {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		count, countErr := verifier.database.CountRows(ctx, table)
		if countErr != nil {
			return result, safeRestoreDependencyError("count restored rows", countErr)
		}
		if count < 0 {
			return result, ErrRestoreUnsafeReportValue
		}
		result.DatabaseRowCounts[table] = count
	}

	seen := make(map[restoreObjectIdentity]struct{})
	var referenceErr error
	enumerateErr := verifier.database.ForEachLiveObject(
		ctx,
		func(reference RestoreObjectReference) error {
			if referenceErr != nil {
				return referenceErr
			}
			referenceErr = verifier.verifyReference(ctx, &result, seen, reference)
			return referenceErr
		},
	)
	if referenceErr != nil {
		result.setReportSHA256()
		return result, referenceErr
	}
	if enumerateErr != nil {
		result.setReportSHA256()
		return result, safeRestoreDependencyError("enumerate restored object references", enumerateErr)
	}
	if err := ctx.Err(); err != nil {
		result.setReportSHA256()
		return result, err
	}

	result.setReportSHA256()
	return result, nil
}

type restoreObjectIdentity struct {
	repository RestoreRepository
	key        string
}

func (verifier *RestoreVerifier) verifyReference(
	ctx context.Context,
	result *RestoreVerificationResult,
	seen map[restoreObjectIdentity]struct{},
	reference RestoreObjectReference,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if reference.Size < 1 || reference.ObjectKey == "" {
		return ErrRestoreUnsafeReportValue
	}

	expectedRepository, knownSource := restoreRepositoryForSource(reference.Source)
	if !knownSource || !knownRestoreRepository(reference.Repository) {
		if err := incrementRestoreCount(&result.UnexpectedObjectCount); err != nil {
			return err
		}
		return newRestoreIntegrityError(ErrRestoreUnexpectedObject)
	}
	// This fixed mapping is the authorization boundary between the two
	// authenticated repositories. A database row routed elsewhere fails closed.
	if reference.Repository != expectedRepository {
		if err := incrementRestoreCount(&result.UnexpectedObjectCount); err != nil {
			return err
		}
		return newRestoreIntegrityError(ErrRestoreUnexpectedAuthorization)
	}

	identity := restoreObjectIdentity{
		repository: reference.Repository,
		key:        reference.ObjectKey,
	}
	if _, duplicate := seen[identity]; duplicate {
		if err := incrementRestoreCount(&result.UnexpectedObjectCount); err != nil {
			return err
		}
		return newRestoreIntegrityError(ErrRestoreUnexpectedObject)
	}
	seen[identity] = struct{}{}

	if err := incrementRestoreCount(&result.CheckedObjectCount); err != nil {
		return err
	}
	objects := verifier.originals
	if reference.Repository == RestorePreviews {
		objects = verifier.previews
	}
	actualSize, err := objects.Stat(ctx, reference.ObjectKey)
	if err != nil {
		if errors.Is(err, ErrRestoreObjectNotFound) {
			if countErr := incrementRestoreCount(&result.MissingObjectCount); countErr != nil {
				return countErr
			}
			return ErrRestoreObjectIntegrity
		}
		return safeRestoreDependencyError("stat restored object", err)
	}
	if actualSize != reference.Size {
		if countErr := incrementRestoreCount(&result.UnexpectedObjectCount); countErr != nil {
			return countErr
		}
		return ErrRestoreObjectIntegrity
	}
	return nil
}

func restoreRepositoryForSource(source RestoreReferenceSource) (RestoreRepository, bool) {
	switch source {
	case RestoreFileVersion:
		return RestoreOriginals, true
	case RestoreFilePreview, RestoreProcessingArtifact:
		return RestorePreviews, true
	default:
		return "", false
	}
}

func knownRestoreRepository(repository RestoreRepository) bool {
	return repository == RestoreOriginals || repository == RestorePreviews
}

func incrementRestoreCount(value *int64) error {
	if *value == math.MaxInt64 {
		return ErrRestoreUnsafeReportValue
	}
	*value++
	return nil
}

const restoreReportFormatVersion int64 = 1

// setReportSHA256 hashes only a fixed sequence of safe integers. The fixed
// row-count order is restoreRowCountAllowlist; object keys, row values,
// dependency errors, URLs, and credentials are never report inputs.
func (result *RestoreVerificationResult) setReportSHA256() {
	digest := sha256.New()
	writeRestoreReportInteger(digest, restoreReportFormatVersion)
	writeRestoreReportInteger(digest, result.RestoredMigrationVersion)
	writeRestoreReportInteger(digest, boolReportInteger(result.SessionRevocationVerified))
	writeRestoreReportInteger(digest, result.CheckedObjectCount)
	writeRestoreReportInteger(digest, result.MissingObjectCount)
	writeRestoreReportInteger(digest, result.UnexpectedObjectCount)
	for _, table := range restoreRowCountAllowlist {
		writeRestoreReportInteger(digest, result.DatabaseRowCounts[table])
	}
	result.ReportSHA256 = digest.Sum(nil)
}

type restoreHashWriter interface {
	Write([]byte) (int, error)
}

func writeRestoreReportInteger(writer restoreHashWriter, value int64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], uint64(value))
	_, _ = writer.Write(encoded[:])
}

func boolReportInteger(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

type restoreDependencyError struct {
	operation string
	cause     error
}

func (err restoreDependencyError) Error() string {
	return err.operation + " failed"
}

func (err restoreDependencyError) Unwrap() error {
	return err.cause
}

func safeRestoreDependencyError(operation string, cause error) error {
	return restoreDependencyError{operation: operation, cause: cause}
}

type restoreIntegrityError struct {
	category error
}

func (restoreIntegrityError) Error() string {
	return ErrRestoreObjectIntegrity.Error()
}

func (err restoreIntegrityError) Unwrap() []error {
	return []error{ErrRestoreObjectIntegrity, err.category}
}

func newRestoreIntegrityError(category error) error {
	return restoreIntegrityError{category: category}
}
