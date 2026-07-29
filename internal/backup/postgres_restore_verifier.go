package backup

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresRestoreVerificationDatabase reads only the restored database. Its
// row-count switch and live-object query are fixed so no restored value can
// select SQL identifiers or route an object to another repository.
type PostgresRestoreVerificationDatabase struct {
	pool *pgxpool.Pool
}

func NewPostgresRestoreVerificationDatabase(
	pool *pgxpool.Pool,
) *PostgresRestoreVerificationDatabase {
	return &PostgresRestoreVerificationDatabase{pool: pool}
}

func (database *PostgresRestoreVerificationDatabase) ActiveSessionCount(
	ctx context.Context,
) (int64, error) {
	if ctx == nil || database == nil || database.pool == nil {
		return 0, ErrRestoreVerifierConfiguration
	}
	var count int64
	err := database.pool.QueryRow(
		ctx,
		`SELECT count(*) FROM sessions WHERE revoked_at IS NULL`,
	).Scan(&count)
	return count, err
}

func (database *PostgresRestoreVerificationDatabase) MigrationVersion(
	ctx context.Context,
) (int64, error) {
	if ctx == nil || database == nil || database.pool == nil {
		return 0, ErrRestoreVerifierConfiguration
	}
	var version int64
	err := database.pool.QueryRow(ctx, `
SELECT COALESCE(MAX(version_id),0)
FROM goose_db_version
WHERE is_applied`).Scan(&version)
	return version, err
}

func (database *PostgresRestoreVerificationDatabase) CountRows(
	ctx context.Context,
	table string,
) (int64, error) {
	if ctx == nil || database == nil || database.pool == nil {
		return 0, ErrRestoreVerifierConfiguration
	}
	var query string
	switch table {
	case "users":
		query = `SELECT count(*) FROM users`
	case "sessions":
		query = `SELECT count(*) FROM sessions`
	case "subjects":
		query = `SELECT count(*) FROM subjects`
	case "grades":
		query = `SELECT count(*) FROM grades`
	case "terms":
		query = `SELECT count(*) FROM terms`
	case "chapters":
		query = `SELECT count(*) FROM chapters`
	case "lessons":
		query = `SELECT count(*) FROM lessons`
	case "lesson_revisions":
		query = `SELECT count(*) FROM lesson_revisions`
	case "files":
		query = `SELECT count(*) FROM files`
	case "file_versions":
		query = `SELECT count(*) FROM file_versions`
	case "file_previews":
		query = `SELECT count(*) FROM file_previews`
	case "qa_threads":
		query = `SELECT count(*) FROM qa_threads`
	case "qa_messages":
		query = `SELECT count(*) FROM qa_messages`
	case "ai_threads":
		query = `SELECT count(*) FROM ai_threads`
	case "ai_messages":
		query = `SELECT count(*) FROM ai_messages`
	case "ai_runs":
		query = `SELECT count(*) FROM ai_runs`
	default:
		return 0, ErrRestoreVerifierConfiguration
	}
	var count int64
	err := database.pool.QueryRow(ctx, query).Scan(&count)
	return count, err
}

func (database *PostgresRestoreVerificationDatabase) ForEachLiveObject(
	ctx context.Context,
	visit func(RestoreObjectReference) error,
) error {
	if ctx == nil || database == nil || database.pool == nil || visit == nil {
		return ErrRestoreVerifierConfiguration
	}
	rows, err := database.pool.Query(ctx, `
SELECT source,repository,object_key,size_bytes
FROM (
  SELECT
    'file_versions'::text AS source,
    'originals'::text AS repository,
    fv.object_key,
    fv.size_bytes
  FROM file_versions AS fv
  WHERE fv.purged_at IS NULL
    AND fv.cleanup_state IS DISTINCT FROM 'deleting'

  UNION ALL

  SELECT
    'file_previews'::text AS source,
    'previews'::text AS repository,
    fp.object_key,
    fp.size_bytes
  FROM file_previews AS fp
  JOIN file_versions AS fv ON fv.id=fp.file_version_id
  WHERE fp.processing_state='ready'
    AND fv.purged_at IS NULL
    AND fv.cleanup_state IS DISTINCT FROM 'deleting'

  UNION ALL

  SELECT
    'file_processing_artifacts'::text AS source,
    'previews'::text AS repository,
    artifact.object_key,
    artifact.size_bytes
  FROM file_processing_artifacts AS artifact
  WHERE artifact.state='stored'
) AS live_objects
ORDER BY source,object_key`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var source string
		var repository string
		var reference RestoreObjectReference
		if err := rows.Scan(
			&source,
			&repository,
			&reference.ObjectKey,
			&reference.Size,
		); err != nil {
			return err
		}
		reference.Source = RestoreReferenceSource(source)
		reference.Repository = RestoreRepository(repository)
		if err := visit(reference); err != nil {
			return err
		}
	}
	return rows.Err()
}

var _ RestoreVerificationDatabase = (*PostgresRestoreVerificationDatabase)(nil)
