package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/platform/database"
)

const (
	retentionTimeout    = 45 * time.Minute
	retentionWorkRoot   = "/work"
	retentionObjectRoot = "/source/aistor"
)

var (
	errRetentionCommand = errors.New("invalid retention command")
	retentionSnapshotID = regexp.MustCompile(`^[0-9a-f]{64}$`)
	retentionStageName  = regexp.MustCompile(`^[a-z_]+$`)
)

type retentionStageError struct {
	stage string
}

func (err retentionStageError) Error() string {
	return backup.ErrRetention.Error()
}

func (err retentionStageError) Unwrap() error {
	return backup.ErrRetention
}

func failRetentionStage(stage string) error {
	return retentionStageError{stage: stage}
}

type retentionConfig struct {
	databaseHost    string
	databasePort    string
	databaseUser    string
	databaseName    string
	databaseSSLMode string
	ageRecipient    string
	encryptionKey   string
}

type retentionArtifactRow struct {
	RunID              uuid.UUID
	Trigger            backup.Trigger
	State              backup.State
	RequestedAt        time.Time
	Kind               backup.ArtifactKind
	Repository         backup.Repository
	ArtifactSnapshotID string
	RunSnapshotID      string
}

type retentionApplication struct {
	pool     *pgxpool.Pool
	service  *backup.Service
	executor backup.Executor
	now      func() time.Time
}

func main() {
	signalContext, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, retentionTimeout)
	defer cancel()
	repository, runID, err := parseRetentionArguments(os.Args[1:])
	if err == nil {
		var application *retentionApplication
		var closeApplication func()
		application, closeApplication, err = newRetentionApplication(
			ctx,
			os.Getenv,
		)
		if err == nil {
			defer closeApplication()
			var deleted int
			deleted, err = application.Run(ctx, repository, runID)
			if err == nil {
				log.Printf("retention_deleted=%d", deleted)
			}
		}
	}
	if err != nil {
		var stageError retentionStageError
		if errors.As(err, &stageError) &&
			retentionStageName.MatchString(stageError.stage) {
			log.Printf("retention_error_%s", stageError.stage)
		} else {
			log.Print("retention_error")
		}
		os.Exit(1)
	}
}

func parseRetentionArguments(
	args []string,
) (backup.Repository, uuid.UUID, error) {
	flags := flag.NewFlagSet("backup-retention", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var repositoryValue string
	var runIDValue string
	flags.StringVar(&repositoryValue, "repository", "", "local or remote")
	flags.StringVar(&runIDValue, "run-id", "", "canonical backup run UUID")
	if err := flags.Parse(args); err != nil ||
		flags.NArg() != 0 ||
		(repositoryValue != string(backup.RepositoryLocal) &&
			repositoryValue != string(backup.RepositoryRemote)) {
		return "", uuid.Nil, errRetentionCommand
	}
	runID, err := uuid.Parse(runIDValue)
	if err != nil ||
		runID == uuid.Nil ||
		runID.String() != runIDValue {
		return "", uuid.Nil, errRetentionCommand
	}
	return backup.Repository(repositoryValue), runID, nil
}

func (application *retentionApplication) Run(
	ctx context.Context,
	repository backup.Repository,
	runID uuid.UUID,
) (int, error) {
	if application == nil ||
		application.pool == nil ||
		application.service == nil ||
		application.now == nil ||
		runID == uuid.Nil {
		return 0, backup.ErrRetention
	}
	now := application.now().UTC()
	policy, err := retentionPolicy(now, runID)
	if err != nil {
		return 0, failRetentionStage("policy")
	}
	candidates, err := application.service.RetentionCandidates(
		ctx,
		policy,
	)
	if err != nil {
		return 0, failRetentionStage("candidates")
	}
	rows, err := loadRetentionArtifactRows(
		ctx,
		application.pool,
		repository,
		runID,
	)
	if err != nil {
		return 0, failRetentionStage("artifact_rows")
	}
	state, err := buildRetentionRepositoryState(
		rows,
		candidates,
		repository,
		runID,
	)
	if err != nil {
		return 0, failRetentionStage("repository_state")
	}
	snapshots, err := application.executor.RepositorySnapshots(ctx, repository)
	if err != nil {
		inventoryStage := backup.RetentionInventoryFailureStage(err)
		switch inventoryStage {
		case "check", "list", "decode":
			return 0, failRetentionStage("snapshot_inventory_" + inventoryStage)
		default:
			return 0, failRetentionStage("snapshot_inventory")
		}
	}
	deletes, err := backup.PlanRetentionDeletes(state, snapshots, now)
	if err != nil {
		return 0, failRetentionStage("delete_plan")
	}
	if err := application.executor.ForgetRetentionSnapshots(
		ctx,
		repository,
		deletes,
	); err != nil {
		return 0, failRetentionStage("forget")
	}
	return len(deletes), nil
}

func retentionPolicy(
	now time.Time,
	runID uuid.UUID,
) (backup.RetentionPolicy, error) {
	if now.IsZero() || runID == uuid.Nil {
		return backup.RetentionPolicy{}, backup.ErrRetention
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return backup.RetentionPolicy{}, backup.ErrRetention
	}
	return backup.RetentionPolicy{
		Now:                  now.UTC(),
		Location:             location,
		CurrentRunID:         runID,
		LocalDaily:           7,
		RemoteDaily:          30,
		RemoteMonthly:        12,
		PreReleaseProtectFor: 30 * 24 * time.Hour,
	}, nil
}

func loadRetentionArtifactRows(
	ctx context.Context,
	pool *pgxpool.Pool,
	repository backup.Repository,
	runID uuid.UUID,
) ([]retentionArtifactRow, error) {
	if pool == nil ||
		runID == uuid.Nil ||
		(repository != backup.RepositoryLocal &&
			repository != backup.RepositoryRemote) {
		return nil, backup.ErrRetention
	}
	rows, err := pool.Query(ctx, `
SELECT r.id,r.trigger_kind,r.state,r.requested_at,
       a.kind,a.repository,a.snapshot_id,
       CASE
         WHEN a.repository='local' THEN r.local_snapshot_id
         WHEN r.id=$2 AND r.state='syncing' THEN a.snapshot_id
         ELSE r.remote_snapshot_id
       END
FROM backup_runs r
JOIN backup_artifacts a ON a.backup_run_id=r.id
WHERE a.repository=$1
  AND (
    (
      a.repository='local'
      AND r.state IN ('succeeded','degraded')
    )
    OR (
      a.repository='remote'
      AND r.state='succeeded'
    )
    OR (
      r.id=$2
      AND r.state IN ('verifying','syncing')
      AND (
        a.repository='local'
        OR (
          a.repository='remote'
          AND r.state='syncing'
        )
      )
    )
  )
ORDER BY r.requested_at DESC,r.id DESC,a.kind`,
		repository,
		runID,
	)
	if err != nil {
		return nil, backup.ErrRetention
	}
	defer rows.Close()
	result := make([]retentionArtifactRow, 0)
	for rows.Next() {
		var row retentionArtifactRow
		if err := rows.Scan(
			&row.RunID,
			&row.Trigger,
			&row.State,
			&row.RequestedAt,
			&row.Kind,
			&row.Repository,
			&row.ArtifactSnapshotID,
			&row.RunSnapshotID,
		); err != nil {
			return nil, backup.ErrRetention
		}
		row.RequestedAt = row.RequestedAt.UTC()
		result = append(result, row)
	}
	if rows.Err() != nil {
		return nil, backup.ErrRetention
	}
	return result, nil
}

type retentionRunArtifacts struct {
	trigger       backup.Trigger
	state         backup.State
	requestedAt   time.Time
	repository    backup.Repository
	runSnapshotID string
	kinds         map[backup.ArtifactKind]struct{}
}

func buildRetentionRepositoryState(
	rows []retentionArtifactRow,
	candidates []backup.Artifact,
	repository backup.Repository,
	currentRunID uuid.UUID,
) (backup.RetentionRepositoryState, error) {
	if currentRunID == uuid.Nil ||
		(repository != backup.RepositoryLocal &&
			repository != backup.RepositoryRemote) {
		return backup.RetentionRepositoryState{}, backup.ErrRetention
	}
	byRun := make(map[uuid.UUID]*retentionRunArtifacts)
	snapshotOwners := make(map[string]uuid.UUID)
	runOrder := make([]uuid.UUID, 0)
	for _, row := range rows {
		if row.RunID == uuid.Nil ||
			row.Repository != repository ||
			row.RequestedAt.IsZero() ||
			!retentionSnapshotID.MatchString(row.ArtifactSnapshotID) ||
			row.ArtifactSnapshotID != row.RunSnapshotID ||
			!validRetentionState(row, currentRunID) {
			return backup.RetentionRepositoryState{}, backup.ErrRetention
		}
		run := byRun[row.RunID]
		if run == nil {
			if owner, duplicate := snapshotOwners[row.RunSnapshotID]; duplicate &&
				owner != row.RunID {
				return backup.RetentionRepositoryState{}, backup.ErrRetention
			}
			snapshotOwners[row.RunSnapshotID] = row.RunID
			run = &retentionRunArtifacts{
				trigger:       row.Trigger,
				state:         row.State,
				requestedAt:   row.RequestedAt,
				repository:    row.Repository,
				runSnapshotID: row.RunSnapshotID,
				kinds:         make(map[backup.ArtifactKind]struct{}, 3),
			}
			byRun[row.RunID] = run
			runOrder = append(runOrder, row.RunID)
		} else if run.trigger != row.Trigger ||
			run.state != row.State ||
			!run.requestedAt.Equal(row.RequestedAt) ||
			run.repository != row.Repository ||
			run.runSnapshotID != row.RunSnapshotID {
			return backup.RetentionRepositoryState{}, backup.ErrRetention
		}
		switch row.Kind {
		case backup.ArtifactDatabaseDump,
			backup.ArtifactObjectSnapshot,
			backup.ArtifactManifest:
		default:
			return backup.RetentionRepositoryState{}, backup.ErrRetention
		}
		if _, duplicate := run.kinds[row.Kind]; duplicate {
			return backup.RetentionRepositoryState{}, backup.ErrRetention
		}
		run.kinds[row.Kind] = struct{}{}
	}
	for _, run := range byRun {
		if len(run.kinds) != 3 {
			return backup.RetentionRepositoryState{}, backup.ErrRetention
		}
	}
	current := byRun[currentRunID]
	if current == nil {
		return backup.RetentionRepositoryState{}, backup.ErrRetention
	}
	state := backup.RetentionRepositoryState{
		CommittedSnapshotIDs: make([]string, 0, len(runOrder)),
		CurrentSnapshotID:    current.runSnapshotID,
	}
	for _, runID := range runOrder {
		run := byRun[runID]
		state.CommittedSnapshotIDs = append(
			state.CommittedSnapshotIDs,
			run.runSnapshotID,
		)
		if runID != currentRunID &&
			state.LastGoodSnapshotID == "" &&
			terminalRetentionSuccess(run.state, repository) {
			state.LastGoodSnapshotID = run.runSnapshotID
		}
	}
	if state.LastGoodSnapshotID == "" {
		state.LastGoodSnapshotID = state.CurrentSnapshotID
	}
	candidateSet := make(map[string]struct{})
	for _, artifact := range candidates {
		if artifact.Repository != repository {
			continue
		}
		run := byRun[artifact.BackupRunID]
		if run == nil ||
			artifact.SnapshotID != run.runSnapshotID {
			return backup.RetentionRepositoryState{}, backup.ErrRetention
		}
		candidateSet[artifact.SnapshotID] = struct{}{}
	}
	state.CandidateSnapshotIDs = make([]string, 0, len(candidateSet))
	for snapshotID := range candidateSet {
		state.CandidateSnapshotIDs = append(
			state.CandidateSnapshotIDs,
			snapshotID,
		)
	}
	sort.Strings(state.CandidateSnapshotIDs)
	return state, nil
}

func validRetentionState(
	row retentionArtifactRow,
	currentRunID uuid.UUID,
) bool {
	if row.RunID == currentRunID {
		if row.Repository == backup.RepositoryLocal {
			return row.State == backup.StateVerifying ||
				row.State == backup.StateSyncing
		}
		return row.Repository == backup.RepositoryRemote &&
			row.State == backup.StateSyncing
	}
	return terminalRetentionSuccess(row.State, row.Repository)
}

func terminalRetentionSuccess(
	state backup.State,
	repository backup.Repository,
) bool {
	if repository == backup.RepositoryLocal {
		return state == backup.StateSucceeded ||
			state == backup.StateDegraded
	}
	return repository == backup.RepositoryRemote &&
		state == backup.StateSucceeded
}

func newRetentionApplication(
	ctx context.Context,
	getenv func(string) string,
) (*retentionApplication, func(), error) {
	config, err := loadRetentionConfig(getenv)
	if err != nil {
		return nil, func() {}, backup.ErrRetention
	}
	secrets := backup.NewFileSecrets()
	databasePassword, err := secrets.Read(backup.SecretDatabasePassword)
	if err != nil {
		return nil, func() {}, backup.ErrRetention
	}
	databaseURL := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.databaseUser, databasePassword),
		Host:   net.JoinHostPort(config.databaseHost, config.databasePort),
		Path:   config.databaseName,
	}
	query := databaseURL.Query()
	query.Set("sslmode", config.databaseSSLMode)
	query.Set("connect_timeout", "5")
	query.Set("statement_timeout", "30000")
	databaseURL.RawQuery = query.Encode()
	pool, err := database.Open(ctx, databaseURL.String())
	if err != nil {
		return nil, func() {}, backup.ErrRetention
	}
	executor, err := backup.NewExecutor(backup.ExecutorConfig{
		Runner:            backup.ExecRunner{},
		Secrets:           secrets,
		WorkRoot:          retentionWorkRoot,
		ObjectRoot:        retentionObjectRoot,
		DatabaseHost:      config.databaseHost,
		DatabasePort:      config.databasePort,
		DatabaseUser:      config.databaseUser,
		DatabaseName:      config.databaseName,
		DatabaseSSLMode:   config.databaseSSLMode,
		AgeRecipient:      config.ageRecipient,
		EncryptionKeyID:   config.encryptionKey,
		Now:               time.Now,
		MaxPlaintextBytes: 8 << 30,
	})
	if err != nil {
		pool.Close()
		return nil, func() {}, backup.ErrRetention
	}
	store := backup.NewPostgresStore(pool)
	return &retentionApplication{
		pool: pool, service: backup.NewService(store, time.Now),
		executor: executor, now: time.Now,
	}, pool.Close, nil
}

func loadRetentionConfig(
	getenv func(string) string,
) (retentionConfig, error) {
	if getenv == nil {
		return retentionConfig{}, backup.ErrRetention
	}
	config := retentionConfig{
		databaseHost:    getenv("HAPPYLEARN_DATABASE_HOST"),
		databasePort:    getenv("HAPPYLEARN_DATABASE_PORT"),
		databaseUser:    getenv("HAPPYLEARN_DATABASE_USER"),
		databaseName:    getenv("HAPPYLEARN_DATABASE_NAME"),
		databaseSSLMode: getenv("HAPPYLEARN_DATABASE_SSLMODE"),
		ageRecipient:    getenv("HAPPYLEARN_BACKUP_AGE_RECIPIENT"),
		encryptionKey:   getenv("HAPPYLEARN_BACKUP_ENCRYPTION_KEY_ID"),
	}
	for _, value := range []string{
		config.databaseHost,
		config.databasePort,
		config.databaseUser,
		config.databaseName,
		config.databaseSSLMode,
		config.ageRecipient,
		config.encryptionKey,
	} {
		if value == "" || strings.TrimSpace(value) != value {
			return retentionConfig{}, backup.ErrRetention
		}
	}
	port, err := strconv.ParseUint(config.databasePort, 10, 16)
	if err != nil || port == 0 {
		return retentionConfig{}, backup.ErrRetention
	}
	return config, nil
}
