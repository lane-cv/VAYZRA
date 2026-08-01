package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	RetentionDeleteBatchLimit = 128
	retentionOrphanAge        = 24 * time.Hour
)

var ErrRetention = errors.New("backup retention failed")

type retentionInventoryError struct {
	stage string
}

func (err retentionInventoryError) Error() string {
	return ErrRetention.Error()
}

func (err retentionInventoryError) Unwrap() error {
	return ErrRetention
}

func RetentionInventoryFailureStage(err error) string {
	var inventoryError retentionInventoryError
	if errors.As(err, &inventoryError) {
		return inventoryError.stage
	}
	return ""
}

type RepositorySnapshot struct {
	ID         string
	CreatedAt  time.Time
	BatchRunID string
}

type RetentionRepositoryState struct {
	CandidateSnapshotIDs []string
	CommittedSnapshotIDs []string
	CurrentSnapshotID    string
	LastGoodSnapshotID   string
}

type resticRepositorySnapshot struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	Tags []string  `json:"tags"`
}

func decodeResticRepositorySnapshots(value []byte) ([]RepositorySnapshot, error) {
	return decodeResticRepositorySnapshotsAt(value, time.Now().UTC())
}

func decodeResticRepositorySnapshotsAt(
	value []byte,
	now time.Time,
) ([]RepositorySnapshot, error) {
	if now.IsZero() || len(value) == 0 || len(value) > CommandOutputLimit {
		return nil, ErrRetention
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	var encoded []resticRepositorySnapshot
	if err := decoder.Decode(&encoded); err != nil {
		return nil, ErrRetention
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrRetention
	}
	snapshots := make([]RepositorySnapshot, 0, len(encoded))
	seen := make(map[string]struct{}, len(encoded))
	for _, snapshot := range encoded {
		if !validResticSnapshotID(snapshot.ID) ||
			snapshot.Time.IsZero() ||
			snapshot.Time.After(now) {
			return nil, ErrRetention
		}
		if _, duplicate := seen[snapshot.ID]; duplicate {
			return nil, ErrRetention
		}
		seen[snapshot.ID] = struct{}{}
		snapshots = append(snapshots, RepositorySnapshot{
			ID:         snapshot.ID,
			CreatedAt:  snapshot.Time.UTC(),
			BatchRunID: canonicalBatchRunID(snapshot.Tags),
		})
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].ID < snapshots[j].ID
	})
	return snapshots, nil
}

func canonicalBatchRunID(tags []string) string {
	const prefix = "happylearn-batch:"
	result := ""
	for _, tag := range tags {
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		value := strings.TrimPrefix(tag, prefix)
		if !validCanonicalBatchRunID(value) || result != "" {
			return ""
		}
		result = value
	}
	return result
}

func validCanonicalBatchRunID(value string) bool {
	runID, err := uuid.Parse(value)
	return err == nil &&
		runID != uuid.Nil &&
		runID.String() == value
}

func validResticSnapshotID(value string) bool {
	return len(value) == 64 && resticSnapshotID.MatchString(value)
}

func normalizeSnapshotIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validResticSnapshotID(value) {
			return nil, ErrRetention
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func PlanRetentionDeletes(
	state RetentionRepositoryState,
	snapshots []RepositorySnapshot,
	now time.Time,
) ([]string, error) {
	if now.IsZero() {
		return nil, ErrRetention
	}
	candidates, err := normalizeSnapshotIDs(state.CandidateSnapshotIDs)
	if err != nil {
		return nil, err
	}
	committed, err := normalizeSnapshotIDs(state.CommittedSnapshotIDs)
	if err != nil {
		return nil, err
	}
	if !validResticSnapshotID(state.CurrentSnapshotID) ||
		!validResticSnapshotID(state.LastGoodSnapshotID) {
		return nil, ErrRetention
	}
	committedSet := make(map[string]struct{}, len(committed))
	for _, id := range committed {
		committedSet[id] = struct{}{}
	}
	if _, ok := committedSet[state.CurrentSnapshotID]; !ok {
		return nil, ErrRetention
	}
	if _, ok := committedSet[state.LastGoodSnapshotID]; !ok {
		return nil, ErrRetention
	}
	for _, id := range candidates {
		if _, ok := committedSet[id]; !ok {
			return nil, ErrRetention
		}
	}
	candidateSet := make(map[string]struct{}, len(candidates))
	for _, id := range candidates {
		candidateSet[id] = struct{}{}
	}
	inventory := make(map[string]RepositorySnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if !validResticSnapshotID(snapshot.ID) ||
			snapshot.CreatedAt.IsZero() ||
			snapshot.CreatedAt.After(now) {
			return nil, ErrRetention
		}
		if _, duplicate := inventory[snapshot.ID]; duplicate {
			return nil, ErrRetention
		}
		inventory[snapshot.ID] = snapshot
	}
	if _, ok := inventory[state.CurrentSnapshotID]; !ok {
		return nil, ErrRetention
	}
	if _, ok := inventory[state.LastGoodSnapshotID]; !ok {
		return nil, ErrRetention
	}
	for _, id := range committed {
		if _, candidate := candidateSet[id]; candidate {
			continue
		}
		if _, retained := inventory[id]; !retained {
			return nil, ErrRetention
		}
	}
	deleteSet := make(map[string]struct{}, len(candidates))
	for _, id := range candidates {
		if _, exists := inventory[id]; exists {
			deleteSet[id] = struct{}{}
		}
	}
	orphanCutoff := now.Add(-retentionOrphanAge)
	for id, snapshot := range inventory {
		if _, committedSnapshot := committedSet[id]; committedSnapshot {
			continue
		}
		if validCanonicalBatchRunID(snapshot.BatchRunID) &&
			snapshot.CreatedAt.Before(orphanCutoff) {
			deleteSet[id] = struct{}{}
		}
	}
	delete(deleteSet, state.CurrentSnapshotID)
	delete(deleteSet, state.LastGoodSnapshotID)
	result := make([]string, 0, len(deleteSet))
	for id := range deleteSet {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func (executor Executor) RepositorySnapshots(
	ctx context.Context,
	repository Repository,
) ([]RepositorySnapshot, error) {
	environment, err := executor.retentionRepositoryEnvironment(repository)
	if err != nil {
		return nil, err
	}
	for _, arguments := range [][]string{
		{"--no-cache", "check", "--read-data"},
		{"--no-cache", "snapshots", "--json"},
	} {
		result, runErr := executor.config.Runner.Run(ctx, Command{
			Executable:  resticExecutable,
			Args:        arguments,
			Env:         environment,
			Stdin:       ClosedStdin,
			StdoutLimit: CommandOutputLimit,
			StderrLimit: CommandOutputLimit,
		})
		if runErr != nil || result.ExitCode != 0 {
			if arguments[1] == "check" {
				return nil, retentionInventoryError{stage: "check"}
			}
			return nil, retentionInventoryError{stage: "list"}
		}
		if arguments[1] == "snapshots" {
			snapshots, decodeErr := decodeResticRepositorySnapshots(result.Stdout)
			if decodeErr != nil {
				return nil, retentionInventoryError{stage: "decode"}
			}
			return snapshots, nil
		}
	}
	return nil, ErrRetention
}

func (executor Executor) ForgetRetentionSnapshots(
	ctx context.Context,
	repository Repository,
	snapshotIDs []string,
) error {
	ids, err := normalizeSnapshotIDs(snapshotIDs)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	environment, err := executor.retentionRepositoryEnvironment(repository)
	if err != nil {
		return err
	}
	for start := 0; start < len(ids); start += RetentionDeleteBatchLimit {
		end := start + RetentionDeleteBatchLimit
		if end > len(ids) {
			end = len(ids)
		}
		arguments := []string{"--no-cache", "forget", "--group-by", ""}
		arguments = append(arguments, ids[start:end]...)
		arguments = append(arguments, "--prune")
		result, runErr := executor.config.Runner.Run(ctx, Command{
			Executable:  resticExecutable,
			Args:        arguments,
			Env:         environment,
			Stdin:       ClosedStdin,
			StdoutLimit: CommandOutputLimit,
			StderrLimit: CommandOutputLimit,
		})
		if runErr != nil || result.ExitCode != 0 {
			return ErrRetention
		}
	}
	return nil
}

func (executor Executor) retentionRepositoryEnvironment(
	repository Repository,
) ([]string, error) {
	if err := secureDirectory(executor.config.WorkRoot); err != nil {
		return nil, ErrRetention
	}
	switch repository {
	case RepositoryLocal:
		location, err := executor.config.Secrets.Read(SecretLocalRepository)
		if err != nil {
			return nil, ErrRetention
		}
		password, err := executor.config.Secrets.Read(SecretLocalPassword)
		if err != nil {
			return nil, ErrRetention
		}
		return []string{
			"LC_ALL=C",
			"TMPDIR=" + executor.config.WorkRoot,
			"RESTIC_REPOSITORY=" + location,
			"RESTIC_PASSWORD=" + password,
		}, nil
	case RepositoryRemote:
		remote, configured, err := executor.remoteConfiguration()
		if err != nil || !configured {
			return nil, ErrRetention
		}
		return []string{
			"LC_ALL=C",
			"TMPDIR=" + executor.config.WorkRoot,
			"RESTIC_REPOSITORY=" + remote.repository,
			"RESTIC_PASSWORD=" + remote.password,
			"AWS_ACCESS_KEY_ID=" + remote.accessKey,
			"AWS_SECRET_ACCESS_KEY=" + remote.secretKey,
		}, nil
	default:
		return nil, ErrRetention
	}
}
