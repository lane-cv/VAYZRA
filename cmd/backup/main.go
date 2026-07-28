package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/platform/database"
)

const (
	workflowLease      = time.Hour
	workflowStateLimit = 64 << 10
	defaultWorkRoot    = "/work"
	defaultObjectRoot  = "/source/aistor"
	defaultStateRoot   = "/state"
	maxPlaintextBytes  = 8 << 30
)

var (
	errInvalidCommand      = errors.New("invalid backup command")
	errWorkflowState       = errors.New("invalid backup workflow state")
	errWorkflowStateAbsent = errors.New("backup workflow state absent")
	errWorkflowUnavailable = errors.New("backup workflow unavailable")
)

type commandActions interface {
	Prepare(context.Context, uuid.UUID) error
	Snapshot(context.Context, uuid.UUID) error
	Verify(context.Context, uuid.UUID) error
	Sync(context.Context, uuid.UUID) error
	Finish(context.Context, uuid.UUID) error
	Fail(context.Context, uuid.UUID, string) error
}

func main() {
	signalContext, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, 45*time.Minute)
	defer cancel()
	actions, closeActions, err := newProductionActions(ctx, os.Getenv)
	if err == nil {
		defer closeActions()
		err = runCommand(ctx, os.Args[1:], actions)
	}
	if err != nil {
		log.Print("backup_error")
		os.Exit(1)
	}
}

func runCommand(ctx context.Context, args []string, actions commandActions) error {
	if ctx == nil || actions == nil || len(args) == 0 {
		return errInvalidCommand
	}
	command := args[0]
	if command != "prepare" &&
		command != "snapshot" &&
		command != "verify" &&
		command != "sync" &&
		command != "finish" &&
		command != "fail" {
		return errInvalidCommand
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var runIDValue singleFlag
	flags.Var(&runIDValue, "run-id", "canonical backup run UUID")
	var categoryValue singleFlag
	if command == "fail" {
		flags.Var(&categoryValue, "category", "safe failure category")
	}
	if err := flags.Parse(args[1:]); err != nil ||
		flags.NArg() != 0 ||
		runIDValue.count != 1 {
		return errInvalidCommand
	}
	runID, err := uuid.Parse(runIDValue.value)
	if err != nil ||
		runID == uuid.Nil ||
		runID.String() != runIDValue.value {
		return errInvalidCommand
	}
	if command == "fail" {
		if categoryValue.count != 1 ||
			!backup.ValidErrorCategory(categoryValue.value) {
			return errInvalidCommand
		}
		return actions.Fail(ctx, runID, categoryValue.value)
	}
	switch command {
	case "prepare":
		return actions.Prepare(ctx, runID)
	case "snapshot":
		return actions.Snapshot(ctx, runID)
	case "verify":
		return actions.Verify(ctx, runID)
	case "sync":
		return actions.Sync(ctx, runID)
	case "finish":
		return actions.Finish(ctx, runID)
	default:
		return errInvalidCommand
	}
}

type singleFlag struct {
	value string
	count int
}

func (value *singleFlag) String() string {
	return value.value
}

func (value *singleFlag) Set(input string) error {
	value.value = input
	value.count++
	if value.count > 1 {
		return errInvalidCommand
	}
	return nil
}

type backupWorkflowService interface {
	ClaimRunByID(context.Context, uuid.UUID, uuid.UUID, time.Duration) (backup.Run, error)
	WorkflowRun(context.Context, uuid.UUID) (backup.Run, error)
	Renew(context.Context, uuid.UUID, uuid.UUID, int64, time.Duration) (backup.Run, error)
	Transition(context.Context, backup.TransitionInput) (backup.Run, error)
	Complete(context.Context, backup.CompletionInput) (backup.Run, error)
	AddArtifact(context.Context, backup.Artifact) error
}

type backupWorkflowExecutor interface {
	Snapshot(context.Context, backup.SnapshotInput) (backup.SnapshotResult, error)
	Verify(context.Context, backup.VerifyInput) (backup.VerifyResult, error)
	RemoteConfigured() (bool, error)
	Sync(context.Context, backup.SyncInput) (string, error)
}

type workflowStates interface {
	Load(uuid.UUID) (workflowState, error)
	Save(workflowState) error
	Delete(uuid.UUID) error
}

type workflowState struct {
	RunID              uuid.UUID               `json:"runId"`
	OwnerID            uuid.UUID               `json:"ownerId"`
	LeaseGeneration    int64                   `json:"leaseGeneration"`
	State              backup.State            `json:"state"`
	Evidence           backup.RecoveryEvidence `json:"evidence"`
	ObjectSnapshotID   string                  `json:"objectSnapshotId"`
	DatabaseDumpSHA256 string                  `json:"databaseDumpSha256"`
	DatabaseDumpBytes  int64                   `json:"databaseDumpBytes"`
	ReferencedBytes    int64                   `json:"referencedBytes"`
	ManifestBytes      int64                   `json:"manifestBytes"`
	LocalVerifiedAt    time.Time               `json:"localVerifiedAt"`
	RemoteVerifiedAt   time.Time               `json:"remoteVerifiedAt"`
	RemoteConfigured   bool                    `json:"remoteConfigured"`
	RemoteSucceeded    bool                    `json:"remoteSucceeded"`
	ErrorCategory      string                  `json:"errorCategory"`
}

type commandApplication struct {
	service          backupWorkflowService
	executor         backupWorkflowExecutor
	states           workflowStates
	newOwner         func() uuid.UUID
	migrationVersion func(context.Context) (int64, error)
	now              func() time.Time
}

func (application *commandApplication) Prepare(
	ctx context.Context,
	runID uuid.UUID,
) error {
	if !application.ready() || runID == uuid.Nil {
		return errWorkflowUnavailable
	}
	local, loadErr := application.states.Load(runID)
	if loadErr == nil {
		if !validWorkflowState(local) ||
			local.RunID != runID ||
			local.State != backup.StateDraining {
			return errWorkflowState
		}
		resumed, terminal, err := application.resume(ctx, runID)
		if err != nil || terminal || resumed.State != backup.StateDraining {
			return errWorkflowUnavailable
		}
		return nil
	}
	if !errors.Is(loadErr, errWorkflowStateAbsent) {
		return errWorkflowState
	}
	owner := application.newOwner()
	if owner == uuid.Nil {
		return errWorkflowUnavailable
	}
	claimed, err := application.service.ClaimRunByID(
		ctx,
		runID,
		owner,
		workflowLease,
	)
	if err != nil ||
		claimed.ID != runID ||
		claimed.OwnerID != owner ||
		claimed.LeaseGeneration < 1 ||
		(claimed.State != backup.StateQueued &&
			claimed.State != backup.StateDraining) {
		return errWorkflowUnavailable
	}
	state := workflowState{
		RunID: runID, OwnerID: owner,
		LeaseGeneration: claimed.LeaseGeneration,
		State:           claimed.State,
	}
	if claimed.State == backup.StateQueued {
		if err := application.transition(ctx, &state, backup.StateDraining); err != nil {
			return err
		}
	}
	return application.states.Save(state)
}

func (application *commandApplication) Snapshot(
	ctx context.Context,
	runID uuid.UUID,
) error {
	state, terminal, err := application.resume(ctx, runID)
	if err != nil || terminal {
		return errWorkflowUnavailable
	}
	switch state.State {
	case backup.StateDraining:
		if err := application.transition(ctx, &state, backup.StateSnapshotting); err != nil {
			return err
		}
		if err := application.states.Save(state); err != nil {
			return errWorkflowState
		}
	case backup.StateSnapshotting:
	case backup.StateEncrypting:
		return nil
	default:
		return errWorkflowUnavailable
	}
	migrationVersion, err := application.migrationVersion(ctx)
	if err != nil || migrationVersion < 1 {
		return errWorkflowUnavailable
	}
	result, err := application.executor.Snapshot(ctx, backup.SnapshotInput{
		RunID:                    runID.String(),
		DatabaseMigrationVersion: migrationVersion,
	})
	if err != nil {
		return errWorkflowUnavailable
	}
	manifestBytes, manifestErr := backup.MarshalManifest(result.Manifest)
	manifestHash := sha256.Sum256(manifestBytes)
	if manifestErr != nil ||
		!bytes.Equal(manifestHash[:], result.ManifestSHA256[:]) ||
		result.Manifest.BatchID != runID.String() ||
		result.Manifest.DatabaseMigrationVersion != migrationVersion ||
		result.EncryptionKeyID == "" ||
		result.LocalSnapshotID == "" ||
		result.LogicalBytes < 0 ||
		result.StoredBytes < 0 {
		return errWorkflowUnavailable
	}
	logicalBytes := result.LogicalBytes
	storedBytes := result.StoredBytes
	state.Evidence = backup.RecoveryEvidence{
		DatabaseMigrationVersion: migrationVersion,
		EncryptionKeyID:          result.EncryptionKeyID,
		LocalSnapshotID:          result.LocalSnapshotID,
		ManifestSHA256:           append([]byte(nil), result.ManifestSHA256[:]...),
		LogicalBytes:             &logicalBytes,
		StoredBytes:              &storedBytes,
		LocalExpiresAt:           application.now().UTC().Add(7 * 24 * time.Hour),
	}
	state.ObjectSnapshotID = result.Manifest.ObjectSnapshotID
	state.DatabaseDumpSHA256 = result.Manifest.DatabaseDumpSHA256
	state.DatabaseDumpBytes = result.DatabaseDumpBytes
	state.ReferencedBytes = result.Manifest.ReferencedBytes
	state.ManifestBytes = int64(len(manifestBytes))
	if err := application.transitionWithEvidence(
		ctx,
		&state,
		backup.StateEncrypting,
		&state.Evidence,
	); err != nil {
		return err
	}
	return application.states.Save(state)
}

func (application *commandApplication) Verify(
	ctx context.Context,
	runID uuid.UUID,
) error {
	state, terminal, err := application.resume(ctx, runID)
	if err != nil || terminal ||
		(state.State != backup.StateEncrypting &&
			state.State != backup.StateVerifying) {
		return errWorkflowUnavailable
	}
	if len(state.Evidence.ManifestSHA256) != sha256.Size ||
		state.Evidence.LocalSnapshotID == "" {
		return errWorkflowUnavailable
	}
	var manifestHash [sha256.Size]byte
	copy(manifestHash[:], state.Evidence.ManifestSHA256)
	verified, err := application.executor.Verify(ctx, backup.VerifyInput{
		RunID:          runID.String(),
		SnapshotID:     state.Evidence.LocalSnapshotID,
		ManifestSHA256: manifestHash,
	})
	if err != nil {
		return errWorkflowUnavailable
	}
	manifestBytes, err := backup.MarshalManifest(verified.Manifest)
	if err != nil ||
		verified.Manifest.BatchID != runID.String() ||
		verified.Manifest.DatabaseMigrationVersion !=
			state.Evidence.DatabaseMigrationVersion {
		return errWorkflowUnavailable
	}
	state.ObjectSnapshotID = verified.Manifest.ObjectSnapshotID
	state.DatabaseDumpSHA256 = verified.Manifest.DatabaseDumpSHA256
	state.ReferencedBytes = verified.Manifest.ReferencedBytes
	state.ManifestBytes = int64(len(manifestBytes))
	if state.Evidence.LogicalBytes == nil ||
		*state.Evidence.LogicalBytes < state.ReferencedBytes {
		return errWorkflowUnavailable
	}
	state.DatabaseDumpBytes =
		*state.Evidence.LogicalBytes - state.ReferencedBytes
	if state.LocalVerifiedAt.IsZero() {
		state.LocalVerifiedAt = artifactTimestamp(application.now())
		if application.states.Save(state) != nil {
			return errWorkflowState
		}
	}
	if application.addArtifacts(
		ctx,
		state,
		backup.RepositoryLocal,
		state.LocalVerifiedAt,
		state.Evidence.LocalExpiresAt,
	) != nil {
		return errWorkflowUnavailable
	}
	if state.State == backup.StateEncrypting {
		if err := application.transition(ctx, &state, backup.StateVerifying); err != nil {
			return err
		}
	}
	return application.states.Save(state)
}

func (application *commandApplication) Sync(
	ctx context.Context,
	runID uuid.UUID,
) error {
	state, terminal, err := application.resume(ctx, runID)
	if err != nil || terminal ||
		(state.State != backup.StateVerifying &&
			state.State != backup.StateSyncing) {
		return errWorkflowUnavailable
	}
	configured, err := application.executor.RemoteConfigured()
	if err != nil || !configured {
		return errWorkflowUnavailable
	}
	if state.State == backup.StateVerifying {
		if err := application.transition(ctx, &state, backup.StateSyncing); err != nil {
			return err
		}
		state.RemoteConfigured = true
		if err := application.states.Save(state); err != nil {
			return errWorkflowState
		}
	} else {
		state.RemoteConfigured = true
	}
	remoteSnapshotID, syncErr := application.executor.Sync(
		ctx,
		backup.SyncInput{
			RunID:            runID.String(),
			SourceSnapshotID: state.Evidence.LocalSnapshotID,
			ManifestSHA256:   manifestHash(state.Evidence.ManifestSHA256),
		},
	)
	if syncErr != nil {
		if errors.Is(syncErr, backup.ErrCancelled) {
			return errWorkflowUnavailable
		}
		state.RemoteSucceeded = false
		state.ErrorCategory = "remote_unavailable"
		return application.states.Save(state)
	}
	if state.Evidence.RemoteSnapshotID != "" &&
		state.Evidence.RemoteSnapshotID != remoteSnapshotID {
		return errWorkflowUnavailable
	}
	state.RemoteSucceeded = true
	state.ErrorCategory = ""
	state.Evidence.RemoteSnapshotID = remoteSnapshotID
	if state.RemoteVerifiedAt.IsZero() {
		if state.Evidence.RemoteExpiresAt == nil {
			state.RemoteVerifiedAt = artifactTimestamp(application.now())
			remoteExpiry := state.RemoteVerifiedAt.Add(30 * 24 * time.Hour)
			state.Evidence.RemoteExpiresAt = &remoteExpiry
		} else {
			state.RemoteVerifiedAt = artifactTimestamp(
				state.Evidence.RemoteExpiresAt.Add(-30 * 24 * time.Hour),
			)
		}
	}
	if state.Evidence.RemoteExpiresAt == nil ||
		state.Evidence.RemoteExpiresAt.IsZero() ||
		application.states.Save(state) != nil {
		return errWorkflowState
	}
	if application.addArtifacts(
		ctx,
		state,
		backup.RepositoryRemote,
		state.RemoteVerifiedAt,
		*state.Evidence.RemoteExpiresAt,
	) != nil {
		return errWorkflowUnavailable
	}
	return application.states.Save(state)
}

func (application *commandApplication) addArtifacts(
	ctx context.Context,
	state workflowState,
	repository backup.Repository,
	verifiedAt time.Time,
	expiresAt time.Time,
) error {
	if verifiedAt.IsZero() || expiresAt.IsZero() {
		return errWorkflowState
	}
	databaseDumpSHA256, err := hex.DecodeString(state.DatabaseDumpSHA256)
	if err != nil || len(databaseDumpSHA256) != sha256.Size {
		return errWorkflowState
	}
	objectSnapshotSHA256, err := hex.DecodeString(state.ObjectSnapshotID)
	if err != nil || len(objectSnapshotSHA256) != sha256.Size {
		return errWorkflowState
	}
	recoverySnapshotID := state.Evidence.LocalSnapshotID
	if repository == backup.RepositoryRemote {
		recoverySnapshotID = state.Evidence.RemoteSnapshotID
	}
	for _, artifact := range []backup.Artifact{
		{
			Kind:       backup.ArtifactDatabaseDump,
			SnapshotID: recoverySnapshotID,
			SHA256:     databaseDumpSHA256,
			SizeBytes:  state.DatabaseDumpBytes,
		},
		{
			Kind:       backup.ArtifactObjectSnapshot,
			SnapshotID: recoverySnapshotID,
			SHA256:     objectSnapshotSHA256,
			SizeBytes:  state.ReferencedBytes,
		},
		{
			Kind:       backup.ArtifactManifest,
			SnapshotID: recoverySnapshotID,
			SHA256:     append([]byte(nil), state.Evidence.ManifestSHA256...),
			SizeBytes:  state.ManifestBytes,
		},
	} {
		artifact.BackupRunID = state.RunID
		artifact.OwnerID = state.OwnerID
		artifact.LeaseGeneration = state.LeaseGeneration
		artifact.Repository = repository
		artifact.VerifiedAt = verifiedAt
		artifact.ExpiresAt = expiresAt
		if application.service.AddArtifact(ctx, artifact) != nil {
			return errWorkflowUnavailable
		}
	}
	return nil
}

func artifactTimestamp(now time.Time) time.Time {
	return now.UTC().Truncate(time.Microsecond)
}

func manifestHash(value []byte) [sha256.Size]byte {
	var result [sha256.Size]byte
	copy(result[:], value)
	return result
}

func (application *commandApplication) Finish(
	ctx context.Context,
	runID uuid.UUID,
) error {
	state, terminal, err := application.resume(ctx, runID)
	if err != nil {
		return errWorkflowUnavailable
	}
	if terminal {
		if state.State == backup.StateSucceeded ||
			state.State == backup.StateDegraded {
			return nil
		}
		return errWorkflowUnavailable
	}
	if state.State != backup.StateVerifying &&
		state.State != backup.StateSyncing {
		return errWorkflowUnavailable
	}
	if state.State == backup.StateVerifying && state.RemoteConfigured {
		return errWorkflowUnavailable
	}
	completed, err := application.service.Complete(ctx, backup.CompletionInput{
		RunID:            runID,
		OwnerID:          state.OwnerID,
		LeaseGeneration:  state.LeaseGeneration,
		From:             state.State,
		Evidence:         cloneEvidence(state.Evidence),
		RemoteConfigured: state.RemoteConfigured,
		RemoteSucceeded:  state.RemoteSucceeded,
		ErrorCategory:    state.ErrorCategory,
	})
	if err != nil {
		return errWorkflowUnavailable
	}
	if completed.ID != runID ||
		(completed.State != backup.StateSucceeded &&
			completed.State != backup.StateDegraded) {
		return errWorkflowUnavailable
	}
	if err := application.states.Delete(runID); err != nil {
		return errWorkflowState
	}
	return nil
}

func (application *commandApplication) Fail(
	ctx context.Context,
	runID uuid.UUID,
	category string,
) error {
	if !backup.ValidErrorCategory(category) {
		return errInvalidCommand
	}
	state, terminal, err := application.resume(ctx, runID)
	if err != nil {
		return errWorkflowUnavailable
	}
	if terminal {
		if state.State == backup.StateFailed {
			return nil
		}
		return errWorkflowUnavailable
	}
	_, err = application.service.Transition(ctx, backup.TransitionInput{
		RunID: runID, OwnerID: state.OwnerID,
		LeaseGeneration: state.LeaseGeneration,
		From:            state.State, To: backup.StateFailed,
		At: application.now().UTC(), ErrorCategory: category,
	})
	if err != nil {
		return errWorkflowUnavailable
	}
	if err := application.states.Delete(runID); err != nil {
		return errWorkflowState
	}
	return nil
}

func (application *commandApplication) ready() bool {
	return application != nil &&
		application.service != nil &&
		application.executor != nil &&
		application.states != nil &&
		application.newOwner != nil &&
		application.migrationVersion != nil &&
		application.now != nil
}

func (application *commandApplication) resume(
	ctx context.Context,
	runID uuid.UUID,
) (workflowState, bool, error) {
	if !application.ready() || runID == uuid.Nil {
		return workflowState{}, false, errWorkflowUnavailable
	}
	state, err := application.states.Load(runID)
	if err != nil || !validWorkflowState(state) || state.RunID != runID {
		durable, lookupErr := application.service.WorkflowRun(ctx, runID)
		if lookupErr == nil &&
			durable.ID == runID &&
			terminalWorkflowState(durable.State) {
			return workflowState{
				RunID: runID,
				State: durable.State,
			}, true, nil
		}
		return workflowState{}, false, errWorkflowState
	}
	renewed, err := application.service.Renew(
		ctx,
		runID,
		state.OwnerID,
		state.LeaseGeneration,
		workflowLease,
	)
	if err != nil {
		owner := application.newOwner()
		if owner == uuid.Nil {
			return workflowState{}, false, errWorkflowUnavailable
		}
		renewed, err = application.service.ClaimRunByID(
			ctx,
			runID,
			owner,
			workflowLease,
		)
		if err != nil {
			durable, lookupErr := application.service.WorkflowRun(ctx, runID)
			if lookupErr == nil && terminalWorkflowState(durable.State) {
				if application.states.Delete(runID) != nil {
					return workflowState{}, false, errWorkflowState
				}
				return workflowState{
					RunID: runID,
					State: durable.State,
				}, true, nil
			}
			return workflowState{}, false, errWorkflowUnavailable
		}
	}
	if renewed.ID != runID ||
		renewed.OwnerID == uuid.Nil ||
		renewed.LeaseGeneration < 1 ||
		terminalWorkflowState(renewed.State) ||
		renewed.LeaseExpiresAt == nil {
		return workflowState{}, false, errWorkflowUnavailable
	}
	if renewed.State != state.State &&
		!backup.ValidTransition(state.State, renewed.State) {
		return workflowState{}, false, errWorkflowUnavailable
	}
	changed := renewed.OwnerID != state.OwnerID ||
		renewed.LeaseGeneration != state.LeaseGeneration ||
		renewed.State != state.State
	state.OwnerID = renewed.OwnerID
	state.LeaseGeneration = renewed.LeaseGeneration
	state.State = renewed.State
	evidence, hasEvidence, evidenceErr := workflowEvidenceFromRun(renewed)
	if evidenceErr != nil {
		return workflowState{}, false, errWorkflowUnavailable
	}
	if hasEvidence &&
		evidence.RemoteSnapshotID == "" &&
		evidence.RemoteExpiresAt == nil {
		localHasRemoteID := state.Evidence.RemoteSnapshotID != ""
		localHasRemoteExpiry := state.Evidence.RemoteExpiresAt != nil
		if localHasRemoteID != localHasRemoteExpiry {
			return workflowState{}, false, errWorkflowUnavailable
		}
		if localHasRemoteID {
			if state.Evidence.RemoteExpiresAt.IsZero() {
				return workflowState{}, false, errWorkflowUnavailable
			}
			evidence.RemoteSnapshotID = state.Evidence.RemoteSnapshotID
			remoteExpiry := state.Evidence.RemoteExpiresAt.UTC()
			evidence.RemoteExpiresAt = &remoteExpiry
		}
	}
	if hasEvidence && !equalWorkflowEvidence(state.Evidence, evidence) {
		state.Evidence = evidence
		changed = true
	}
	if state.State == backup.StateSyncing && !state.RemoteConfigured {
		state.RemoteConfigured = true
		changed = true
	}
	if changed && application.states.Save(state) != nil {
		return workflowState{}, false, errWorkflowState
	}
	return state, false, nil
}

func (application *commandApplication) transition(
	ctx context.Context,
	state *workflowState,
	to backup.State,
) error {
	return application.transitionWithEvidence(ctx, state, to, nil)
}

func (application *commandApplication) transitionWithEvidence(
	ctx context.Context,
	state *workflowState,
	to backup.State,
	evidence *backup.RecoveryEvidence,
) error {
	transitioned, err := application.service.Transition(ctx, backup.TransitionInput{
		RunID: state.RunID, OwnerID: state.OwnerID,
		LeaseGeneration: state.LeaseGeneration,
		From:            state.State, To: to, At: application.now().UTC(),
		Evidence: evidence,
	})
	if err != nil ||
		transitioned.ID != state.RunID ||
		transitioned.OwnerID != state.OwnerID ||
		transitioned.LeaseGeneration != state.LeaseGeneration ||
		transitioned.State != to {
		return errWorkflowUnavailable
	}
	state.State = to
	return nil
}

func workflowEvidenceFromRun(
	run backup.Run,
) (backup.RecoveryEvidence, bool, error) {
	hasEvidence := run.DatabaseMigrationVersion != nil ||
		run.EncryptionKeyID != "" ||
		run.LocalSnapshotID != "" ||
		run.RemoteSnapshotID != "" ||
		len(run.ManifestSHA256) != 0 ||
		run.LogicalBytes != nil ||
		run.StoredBytes != nil ||
		run.LocalExpiresAt != nil ||
		run.RemoteExpiresAt != nil
	if !hasEvidence {
		return backup.RecoveryEvidence{}, false, nil
	}
	if run.DatabaseMigrationVersion == nil ||
		*run.DatabaseMigrationVersion < 1 ||
		run.EncryptionKeyID == "" ||
		run.LocalSnapshotID == "" ||
		len(run.ManifestSHA256) != sha256.Size ||
		run.LocalExpiresAt == nil ||
		run.LocalExpiresAt.IsZero() {
		return backup.RecoveryEvidence{}, false, errWorkflowState
	}
	if (run.RemoteSnapshotID == "") != (run.RemoteExpiresAt == nil) {
		return backup.RecoveryEvidence{}, false, errWorkflowState
	}
	evidence := backup.RecoveryEvidence{
		DatabaseMigrationVersion: *run.DatabaseMigrationVersion,
		EncryptionKeyID:          run.EncryptionKeyID,
		LocalSnapshotID:          run.LocalSnapshotID,
		RemoteSnapshotID:         run.RemoteSnapshotID,
		ManifestSHA256:           append([]byte(nil), run.ManifestSHA256...),
		LogicalBytes:             cloneInt64(run.LogicalBytes),
		StoredBytes:              cloneInt64(run.StoredBytes),
		LocalExpiresAt:           run.LocalExpiresAt.UTC(),
	}
	if run.RemoteExpiresAt != nil {
		remoteExpiry := run.RemoteExpiresAt.UTC()
		evidence.RemoteExpiresAt = &remoteExpiry
	}
	return evidence, true, nil
}

func equalWorkflowEvidence(
	left backup.RecoveryEvidence,
	right backup.RecoveryEvidence,
) bool {
	return left.DatabaseMigrationVersion == right.DatabaseMigrationVersion &&
		left.EncryptionKeyID == right.EncryptionKeyID &&
		left.LocalSnapshotID == right.LocalSnapshotID &&
		left.RemoteSnapshotID == right.RemoteSnapshotID &&
		bytes.Equal(left.ManifestSHA256, right.ManifestSHA256) &&
		equalInt64(left.LogicalBytes, right.LogicalBytes) &&
		equalInt64(left.StoredBytes, right.StoredBytes) &&
		left.LocalExpiresAt.Equal(right.LocalExpiresAt) &&
		equalTime(left.RemoteExpiresAt, right.RemoteExpiresAt)
}

func equalInt64(left *int64, right *int64) bool {
	return left == nil && right == nil ||
		left != nil && right != nil && *left == *right
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func equalTime(left *time.Time, right *time.Time) bool {
	return left == nil && right == nil ||
		left != nil && right != nil && left.Equal(*right)
}

func terminalWorkflowState(state backup.State) bool {
	return state == backup.StateSucceeded ||
		state == backup.StateDegraded ||
		state == backup.StateFailed
}

func validWorkflowState(state workflowState) bool {
	if state.RunID == uuid.Nil ||
		state.OwnerID == uuid.Nil ||
		state.LeaseGeneration < 1 {
		return false
	}
	switch state.State {
	case backup.StateDraining,
		backup.StateSnapshotting,
		backup.StateEncrypting,
		backup.StateVerifying,
		backup.StateSyncing:
	default:
		return false
	}
	if state.ErrorCategory != "" &&
		!backup.ValidErrorCategory(state.ErrorCategory) {
		return false
	}
	return true
}

func cloneEvidence(value backup.RecoveryEvidence) backup.RecoveryEvidence {
	cloned := value
	cloned.ManifestSHA256 = append([]byte(nil), value.ManifestSHA256...)
	if value.LogicalBytes != nil {
		logicalBytes := *value.LogicalBytes
		cloned.LogicalBytes = &logicalBytes
	}
	if value.StoredBytes != nil {
		storedBytes := *value.StoredBytes
		cloned.StoredBytes = &storedBytes
	}
	if value.RemoteExpiresAt != nil {
		remoteExpiry := value.RemoteExpiresAt.UTC()
		cloned.RemoteExpiresAt = &remoteExpiry
	}
	return cloned
}

type fileWorkflowStates struct {
	root          string
	syncDirectory func(string) error
}

func newFileWorkflowStates() fileWorkflowStates {
	return fileWorkflowStates{
		root:          defaultStateRoot,
		syncDirectory: syncWorkflowStateDirectory,
	}
}

func (states fileWorkflowStates) Load(runID uuid.UUID) (workflowState, error) {
	if runID == uuid.Nil || !secureStateDirectory(states.root) {
		return workflowState{}, errWorkflowState
	}
	path := states.path(runID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return workflowState{}, errWorkflowStateAbsent
	}
	if err != nil ||
		!info.Mode().IsRegular() ||
		info.Mode().Perm() != 0o600 ||
		info.Mode()&os.ModeSymlink != 0 ||
		!stateOwnedByCurrentUser(info) {
		return workflowState{}, errWorkflowState
	}
	file, err := os.Open(path)
	if err != nil {
		return workflowState{}, errWorkflowState
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return workflowState{}, errWorkflowState
	}
	encoded, err := io.ReadAll(io.LimitReader(file, workflowStateLimit+1))
	if err != nil || len(encoded) == 0 || len(encoded) > workflowStateLimit {
		return workflowState{}, errWorkflowState
	}
	var state workflowState
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return workflowState{}, errWorkflowState
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) ||
		state.RunID != runID ||
		!validWorkflowState(state) {
		return workflowState{}, errWorkflowState
	}
	return state, nil
}

func (states fileWorkflowStates) Save(state workflowState) error {
	if !validWorkflowState(state) || !secureStateDirectory(states.root) {
		return errWorkflowState
	}
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > workflowStateLimit {
		return errWorkflowState
	}
	file, err := os.CreateTemp(states.root, ".state-")
	if err != nil {
		return errWorkflowState
	}
	tempPath := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return errWorkflowState
	}
	if _, err := file.Write(encoded); err != nil {
		cleanup()
		return errWorkflowState
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return errWorkflowState
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return errWorkflowState
	}
	if err := os.Rename(tempPath, states.path(state.RunID)); err != nil {
		_ = os.Remove(tempPath)
		return errWorkflowState
	}
	if states.syncRoot() != nil {
		return errWorkflowState
	}
	return nil
}

func (states fileWorkflowStates) Delete(runID uuid.UUID) error {
	if runID == uuid.Nil || !secureStateDirectory(states.root) {
		return errWorkflowState
	}
	if err := os.Remove(states.path(runID)); err != nil {
		return errWorkflowState
	}
	if states.syncRoot() != nil {
		return errWorkflowState
	}
	return nil
}

func (states fileWorkflowStates) syncRoot() error {
	syncDirectory := states.syncDirectory
	if syncDirectory == nil {
		syncDirectory = syncWorkflowStateDirectory
	}
	return syncDirectory(states.root)
}

func syncWorkflowStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil ||
		!info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 ||
		!stateOwnedByCurrentUser(info) {
		return errWorkflowState
	}
	directory, err := os.Open(path)
	if err != nil {
		return errWorkflowState
	}
	openedInfo, statErr := directory.Stat()
	if statErr != nil || !os.SameFile(info, openedInfo) {
		_ = directory.Close()
		return errWorkflowState
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return errWorkflowState
	}
	return nil
}

func (states fileWorkflowStates) path(runID uuid.UUID) string {
	return filepath.Join(states.root, runID.String()+".json")
}

func secureStateDirectory(path string) bool {
	info, err := os.Lstat(path)
	return err == nil &&
		info.IsDir() &&
		info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().Perm()&0o077 == 0 &&
		stateOwnedByCurrentUser(info)
}

func stateOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

type productionConfig struct {
	databaseHost    string
	databasePort    string
	databaseUser    string
	databaseName    string
	databaseSSLMode string
	ageRecipient    string
	encryptionKey   string
}

func loadProductionConfig(getenv func(string) string) (productionConfig, error) {
	if getenv == nil {
		return productionConfig{}, errWorkflowUnavailable
	}
	config := productionConfig{
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
			return productionConfig{}, errWorkflowUnavailable
		}
	}
	return config, nil
}

func newProductionActions(
	ctx context.Context,
	getenv func(string) string,
) (*commandApplication, func(), error) {
	config, err := loadProductionConfig(getenv)
	if err != nil {
		return nil, func() {}, errWorkflowUnavailable
	}
	secrets := backup.NewFileSecrets()
	databasePassword, err := secrets.Read(backup.SecretDatabasePassword)
	if err != nil {
		return nil, func() {}, errWorkflowUnavailable
	}
	port, err := strconv.ParseUint(config.databasePort, 10, 16)
	if err != nil || port == 0 {
		return nil, func() {}, errWorkflowUnavailable
	}
	databaseURL := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(config.databaseUser, databasePassword),
		Host:   net.JoinHostPort(config.databaseHost, config.databasePort),
		Path:   config.databaseName,
	}
	query := databaseURL.Query()
	query.Set("sslmode", config.databaseSSLMode)
	databaseURL.RawQuery = query.Encode()
	pool, err := database.Open(ctx, databaseURL.String())
	if err != nil {
		return nil, func() {}, errWorkflowUnavailable
	}
	executor, err := backup.NewExecutor(backup.ExecutorConfig{
		Runner:            backup.ExecRunner{},
		Secrets:           secrets,
		WorkRoot:          defaultWorkRoot,
		ObjectRoot:        defaultObjectRoot,
		DatabaseHost:      config.databaseHost,
		DatabasePort:      config.databasePort,
		DatabaseUser:      config.databaseUser,
		DatabaseName:      config.databaseName,
		DatabaseSSLMode:   config.databaseSSLMode,
		AgeRecipient:      config.ageRecipient,
		EncryptionKeyID:   config.encryptionKey,
		Now:               time.Now,
		MaxPlaintextBytes: maxPlaintextBytes,
	})
	if err != nil {
		pool.Close()
		return nil, func() {}, errWorkflowUnavailable
	}
	service := backup.NewService(backup.NewPostgresStore(pool), time.Now)
	application := &commandApplication{
		service: service, executor: &executor,
		states: newFileWorkflowStates(), newOwner: uuid.New,
		migrationVersion: postgresMigrationVersion(pool),
		now:              time.Now,
	}
	return application, pool.Close, nil
}

func postgresMigrationVersion(
	pool *pgxpool.Pool,
) func(context.Context) (int64, error) {
	return func(ctx context.Context) (int64, error) {
		var version int64
		err := pool.QueryRow(ctx, `
SELECT COALESCE(MAX(version_id),0)
FROM goose_db_version
WHERE is_applied`).Scan(&version)
		if err != nil || version < 1 {
			return 0, errWorkflowUnavailable
		}
		return version, nil
	}
}
