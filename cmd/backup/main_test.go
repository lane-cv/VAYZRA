package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/backup"
	"happylearn.local/app/internal/operations"
)

const (
	commandRunID            = "10000000-0000-4000-8000-000000000001"
	commandObjectSnapshotID = "1111111111111111111111111111111111111111111111111111111111111111"
	commandRecoveryID       = "2222222222222222222222222222222222222222222222222222222222222222"
)

type recordingActions struct {
	calls      []string
	runIDs     []uuid.UUID
	categories []string
}

func (actions *recordingActions) Prepare(_ context.Context, runID uuid.UUID) error {
	actions.record("prepare", runID)
	return nil
}
func (actions *recordingActions) Snapshot(_ context.Context, runID uuid.UUID) error {
	actions.record("snapshot", runID)
	return nil
}
func (actions *recordingActions) Verify(_ context.Context, runID uuid.UUID) error {
	actions.record("verify", runID)
	return nil
}
func (actions *recordingActions) Sync(_ context.Context, runID uuid.UUID) error {
	actions.record("sync", runID)
	return nil
}
func (actions *recordingActions) Finish(_ context.Context, runID uuid.UUID) error {
	actions.record("finish", runID)
	return nil
}
func (actions *recordingActions) Fail(_ context.Context, runID uuid.UUID, category string) error {
	actions.record("fail", runID)
	actions.categories = append(actions.categories, category)
	return nil
}
func (actions *recordingActions) record(name string, runID uuid.UUID) {
	actions.calls = append(actions.calls, name)
	actions.runIDs = append(actions.runIDs, runID)
}

func TestRunCommandExposesOnlyExactBackupSubcommandsAndFlags(t *testing.T) {
	for _, command := range []string{"prepare", "snapshot", "verify", "sync", "finish"} {
		t.Run(command, func(t *testing.T) {
			actions := &recordingActions{}
			if err := runCommand(
				context.Background(),
				[]string{command, "--run-id", commandRunID},
				actions,
			); err != nil {
				t.Fatal(err)
			}
			if len(actions.calls) != 1 ||
				actions.calls[0] != command ||
				actions.runIDs[0].String() != commandRunID {
				t.Fatalf("calls=%v runIDs=%v", actions.calls, actions.runIDs)
			}
		})
	}
	actions := &recordingActions{}
	if err := runCommand(
		context.Background(),
		[]string{"fail", "--run-id", commandRunID, "--category", "integrity"},
		actions,
	); err != nil {
		t.Fatal(err)
	}
	if len(actions.calls) != 1 ||
		actions.calls[0] != "fail" ||
		len(actions.categories) != 1 ||
		actions.categories[0] != "integrity" {
		t.Fatalf("calls=%v categories=%v", actions.calls, actions.categories)
	}
}

func TestRunCommandRejectsMalformedDuplicateAndUnexpectedInput(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"unknown", "--run-id", commandRunID},
		{"prepare"},
		{"prepare", "--run-id", ""},
		{"prepare", "--run-id", "00000000-0000-0000-0000-000000000000"},
		{"prepare", "--run-id", "10000000-0000-4000-8000-00000000000A"},
		{"prepare", "--run-id", commandRunID, "--run-id", commandRunID},
		{"prepare", "--run-id", commandRunID, "extra"},
		{"prepare", "--run-id", commandRunID, "--category", "integrity"},
		{"fail", "--run-id", commandRunID},
		{"fail", "--run-id", commandRunID, "--category", "password=/secret"},
		{"fail", "--run-id", commandRunID, "--category", "integrity", "--category", "integrity"},
	} {
		actions := &recordingActions{}
		err := runCommand(context.Background(), args, actions)
		if !errors.Is(err, errInvalidCommand) {
			t.Errorf("args=%q err=%v", args, err)
		}
		if len(actions.calls) != 0 {
			t.Fatalf("args=%q dispatched=%v", args, actions.calls)
		}
	}
}

type workflowServiceFixture struct {
	runID                    uuid.UUID
	owner                    uuid.UUID
	generation               int64
	state                    backup.State
	evidence                 backup.RecoveryEvidence
	leaseActive              bool
	claimErr                 error
	renewErr                 error
	workflowErr              error
	genericClaims            int
	targetClaims             []uuid.UUID
	workflowCalls            int
	renewAttempts            int
	renewCalls               int
	transitions              []backup.TransitionInput
	transitionResponseLossAt int
	completions              []backup.CompletionInput
	artifacts                []backup.Artifact
	strictArtifacts          bool
}

func (service *workflowServiceFixture) Claim(
	_ context.Context,
	owner uuid.UUID,
	_ time.Duration,
) (backup.Run, error) {
	service.genericClaims++
	service.owner = owner
	return backup.Run{
		ID: service.runID, State: backup.StateQueued, OwnerID: owner,
		LeaseGeneration: service.generation,
	}, nil
}

func (service *workflowServiceFixture) ClaimRunByID(
	_ context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	_ time.Duration,
) (backup.Run, error) {
	service.targetClaims = append(service.targetClaims, runID)
	if service.claimErr != nil {
		return backup.Run{}, service.claimErr
	}
	if runID != service.runID {
		return backup.Run{}, backup.ErrNotFound
	}
	if service.state == backup.StateSucceeded ||
		service.state == backup.StateDegraded ||
		service.state == backup.StateFailed {
		return backup.Run{}, backup.ErrNoClaimableRun
	}
	if service.owner != uuid.Nil && service.leaseActive {
		return backup.Run{}, backup.ErrActiveClaim
	}
	if service.owner == uuid.Nil {
		if service.generation < 1 {
			service.generation = 1
		}
	} else {
		service.generation++
	}
	service.owner = owner
	service.leaseActive = true
	return service.workflowRun(), nil
}

func (service *workflowServiceFixture) WorkflowRun(
	_ context.Context,
	runID uuid.UUID,
) (backup.Run, error) {
	service.workflowCalls++
	if service.workflowErr != nil {
		return backup.Run{}, service.workflowErr
	}
	if runID != service.runID {
		return backup.Run{}, backup.ErrNotFound
	}
	return service.workflowRun(), nil
}

func (service *workflowServiceFixture) workflowRun() backup.Run {
	run := backup.Run{
		ID: service.runID, State: service.state,
		OwnerID: service.owner, LeaseGeneration: service.generation,
	}
	if service.owner != uuid.Nil {
		expiresAt := time.Now().UTC().Add(time.Hour)
		run.LeaseExpiresAt = &expiresAt
	}
	if service.evidence.DatabaseMigrationVersion > 0 {
		migrationVersion := service.evidence.DatabaseMigrationVersion
		run.DatabaseMigrationVersion = &migrationVersion
		run.EncryptionKeyID = service.evidence.EncryptionKeyID
		run.LocalSnapshotID = service.evidence.LocalSnapshotID
		run.RemoteSnapshotID = service.evidence.RemoteSnapshotID
		run.ManifestSHA256 = append([]byte(nil), service.evidence.ManifestSHA256...)
		run.LogicalBytes = cloneInt64Test(service.evidence.LogicalBytes)
		run.StoredBytes = cloneInt64Test(service.evidence.StoredBytes)
		localExpiry := service.evidence.LocalExpiresAt
		run.LocalExpiresAt = &localExpiry
		if service.evidence.RemoteExpiresAt != nil {
			remoteExpiry := *service.evidence.RemoteExpiresAt
			run.RemoteExpiresAt = &remoteExpiry
		}
	}
	return run
}

func (service *workflowServiceFixture) Renew(
	_ context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	generation int64,
	_ time.Duration,
) (backup.Run, error) {
	service.renewAttempts++
	if service.renewErr != nil {
		return backup.Run{}, service.renewErr
	}
	if runID != service.runID || owner != service.owner || generation != service.generation {
		return backup.Run{}, backup.ErrStaleOwner
	}
	service.renewCalls++
	return service.workflowRun(), nil
}

func (service *workflowServiceFixture) Transition(
	_ context.Context,
	input backup.TransitionInput,
) (backup.Run, error) {
	if input.RunID != service.runID ||
		input.OwnerID != service.owner ||
		input.LeaseGeneration != service.generation ||
		input.From != service.state {
		return backup.Run{}, backup.ErrStaleOwner
	}
	service.transitions = append(service.transitions, input)
	service.state = input.To
	if input.Evidence != nil {
		service.evidence = cloneEvidence(*input.Evidence)
	}
	if input.To == backup.StateSucceeded ||
		input.To == backup.StateDegraded ||
		input.To == backup.StateFailed {
		service.owner = uuid.Nil
		service.leaseActive = false
	}
	if service.transitionResponseLossAt == len(service.transitions) {
		return backup.Run{}, backup.ErrUnavailable
	}
	return backup.Run{
		ID: input.RunID, State: input.To, OwnerID: input.OwnerID,
		LeaseGeneration: input.LeaseGeneration,
	}, nil
}

func (service *workflowServiceFixture) Complete(
	_ context.Context,
	input backup.CompletionInput,
) (backup.Run, error) {
	if input.RunID != service.runID ||
		input.OwnerID != service.owner ||
		input.LeaseGeneration != service.generation ||
		input.From != service.state {
		return backup.Run{}, backup.ErrStaleOwner
	}
	service.completions = append(service.completions, input)
	service.state = backup.StateSucceeded
	if input.RemoteConfigured && !input.RemoteSucceeded {
		service.state = backup.StateDegraded
	}
	service.evidence = cloneEvidence(input.Evidence)
	service.owner = uuid.Nil
	service.leaseActive = false
	return backup.Run{ID: input.RunID, State: service.state}, nil
}

func (service *workflowServiceFixture) AddArtifact(
	_ context.Context,
	artifact backup.Artifact,
) error {
	if artifact.BackupRunID != service.runID ||
		artifact.OwnerID != service.owner ||
		artifact.LeaseGeneration != service.generation {
		return backup.ErrStaleOwner
	}
	if service.strictArtifacts {
		for _, existing := range service.artifacts {
			if existing.BackupRunID != artifact.BackupRunID ||
				existing.Kind != artifact.Kind ||
				existing.Repository != artifact.Repository {
				continue
			}
			if existing.SnapshotID != artifact.SnapshotID ||
				string(existing.SHA256) != string(artifact.SHA256) ||
				existing.SizeBytes != artifact.SizeBytes ||
				!existing.VerifiedAt.Equal(artifact.VerifiedAt) ||
				!existing.ExpiresAt.Equal(artifact.ExpiresAt) {
				return backup.ErrInvalid
			}
			return nil
		}
	}
	service.artifacts = append(service.artifacts, artifact)
	return nil
}

type workflowExecutorFixture struct {
	snapshotResult backup.SnapshotResult
	snapshotErr    error
	snapshotCalls  int
	verified       []backup.VerifyInput
	remote         bool
	remoteErr      error
	syncResult     string
	syncErr        error
}

func (executor *workflowExecutorFixture) LocalConfigured() (bool, error) {
	return executor.snapshotErr == nil, executor.snapshotErr
}

type backupStatusWriterFixture struct {
	statuses []operations.InfrastructureStatus
}

func (writer *backupStatusWriterFixture) RecordInfrastructureStatus(
	_ context.Context,
	key operations.InfrastructureKey,
	configured bool,
	validatedAt time.Time,
) error {
	writer.statuses = append(writer.statuses, operations.InfrastructureStatus{
		Key: key, Configured: configured, LastValidatedAt: &validatedAt,
	})
	return nil
}

func TestBackupConfigurationStatusRecordsLocalAndPartialRemoteSafely(t *testing.T) {
	validatedAt := time.Date(2026, 7, 28, 4, 5, 6, 0, time.UTC)
	partialRemote := errors.New("partial remote configuration")
	writer := &backupStatusWriterFixture{}
	validator := &workflowExecutorFixture{remoteErr: partialRemote}
	err := recordBackupInfrastructureStatuses(
		context.Background(),
		writer,
		validator,
		validatedAt,
	)
	if !errors.Is(err, partialRemote) || len(writer.statuses) != 2 {
		t.Fatalf("err=%v statuses=%+v", err, writer.statuses)
	}
	if writer.statuses[0].Key != operations.InfrastructureLocalBackup ||
		!writer.statuses[0].Configured ||
		writer.statuses[1].Key != operations.InfrastructureRemoteBackup ||
		writer.statuses[1].Configured {
		t.Fatalf("statuses=%+v", writer.statuses)
	}
}

type snapshotExitSecrets map[backup.SecretName]string

func (secrets snapshotExitSecrets) Read(
	name backup.SecretName,
) (string, error) {
	value, ok := secrets[name]
	if !ok {
		return "", backup.ErrSecretUnavailable
	}
	return value, nil
}

type snapshotExitRunner struct {
	code   int
	secret string
}

func (runner snapshotExitRunner) Run(
	_ context.Context,
	command backup.Command,
) (backup.CommandResult, error) {
	switch filepath.Base(command.Executable) {
	case "pg_dump":
		if err := os.WriteFile(
			command.StdoutFile,
			[]byte("PGDMP"),
			0o600,
		); err != nil {
			return backup.CommandResult{}, err
		}
		return backup.CommandResult{ExitCode: 0}, nil
	case "age":
		if err := os.WriteFile(
			command.StdoutFile,
			[]byte("age-encrypted"),
			0o600,
		); err != nil {
			return backup.CommandResult{}, err
		}
		return backup.CommandResult{ExitCode: 0}, nil
	case "restic":
		return backup.CommandResult{
			ExitCode: runner.code,
			Stderr:   []byte(runner.secret),
		}, nil
	default:
		return backup.CommandResult{}, errors.New("unexpected executable")
	}
}

func (executor *workflowExecutorFixture) Snapshot(
	context.Context,
	backup.SnapshotInput,
) (backup.SnapshotResult, error) {
	executor.snapshotCalls++
	return executor.snapshotResult, executor.snapshotErr
}

func (executor *workflowExecutorFixture) Verify(
	_ context.Context,
	input backup.VerifyInput,
) (backup.VerifyResult, error) {
	executor.verified = append(executor.verified, input)
	return backup.VerifyResult{Manifest: executor.snapshotResult.Manifest}, nil
}

func (executor *workflowExecutorFixture) RemoteConfigured() (bool, error) {
	return executor.remote, executor.remoteErr
}

func (executor *workflowExecutorFixture) Sync(
	context.Context,
	backup.SyncInput,
) (string, error) {
	return executor.syncResult, executor.syncErr
}

type memoryWorkflowStates struct {
	state        *workflowState
	loadErr      error
	saveCalls    int
	failSaveAt   int
	failSave     func(workflowState, int) bool
	deleteCalls  int
	failDeleteAt int
}

func (states *memoryWorkflowStates) Load(uuid.UUID) (workflowState, error) {
	if states.loadErr != nil {
		return workflowState{}, states.loadErr
	}
	if states.state == nil {
		return workflowState{}, errWorkflowStateAbsent
	}
	return *states.state, nil
}

func (states *memoryWorkflowStates) Save(state workflowState) error {
	states.saveCalls++
	if states.failSaveAt == states.saveCalls ||
		(states.failSave != nil && states.failSave(state, states.saveCalls)) {
		return errWorkflowState
	}
	cloned := state
	states.state = &cloned
	return nil
}

func (states *memoryWorkflowStates) Delete(uuid.UUID) error {
	states.deleteCalls++
	if states.failDeleteAt == states.deleteCalls {
		return errWorkflowState
	}
	states.state = nil
	return nil
}

func cloneInt64Test(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func TestCommandApplicationFencesEveryMutationWithClaimedOwnerAndGeneration(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	owner := uuid.MustParse("20000000-0000-4000-8000-000000000002")
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	manifest := backup.Manifest{
		SchemaVersion:            1,
		BatchID:                  commandRunID,
		CreatedAt:                now,
		DatabaseMigrationVersion: 20,
		DatabaseDumpSHA256:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ObjectSnapshotID:         commandObjectSnapshotID,
		ObjectCount:              1,
		ReferencedBytes:          11,
	}
	manifestBytes, err := backup.MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(manifestBytes)
	service := &workflowServiceFixture{
		runID: runID, generation: 9, state: backup.StateQueued,
	}
	executor := &workflowExecutorFixture{snapshotResult: backup.SnapshotResult{
		Manifest:          manifest,
		ManifestSHA256:    manifestHash,
		EncryptionKeyID:   "key-2026-07",
		LocalSnapshotID:   commandRecoveryID,
		DatabaseDumpBytes: 22,
		LogicalBytes:      33,
		StoredBytes:       22,
	}}
	states := &memoryWorkflowStates{}
	application := &commandApplication{
		service:  service,
		executor: executor,
		states:   states,
		newOwner: func() uuid.UUID {
			return owner
		},
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: func() time.Time { return now },
	}

	for _, call := range []func(context.Context, uuid.UUID) error{
		application.Prepare,
		application.Snapshot,
		application.Verify,
		application.Finish,
	} {
		if err := call(context.Background(), runID); err != nil {
			t.Fatal(err)
		}
	}
	wantTransitions := []backup.State{
		backup.StateDraining,
		backup.StateSnapshotting,
		backup.StateEncrypting,
		backup.StateVerifying,
	}
	if len(service.transitions) != len(wantTransitions) {
		t.Fatalf("transitions=%+v", service.transitions)
	}
	for index, transition := range service.transitions {
		if transition.OwnerID != owner ||
			transition.LeaseGeneration != 9 ||
			transition.To != wantTransitions[index] {
			t.Fatalf("transition[%d]=%+v", index, transition)
		}
	}
	if service.renewCalls != 3 {
		t.Fatalf("renewCalls=%d", service.renewCalls)
	}
	if len(service.completions) != 1 ||
		service.completions[0].OwnerID != owner ||
		service.completions[0].LeaseGeneration != 9 ||
		service.completions[0].Evidence.EncryptionKeyID != "key-2026-07" ||
		service.completions[0].Evidence.LocalSnapshotID != commandRecoveryID {
		t.Fatalf("completions=%+v", service.completions)
	}
	if len(executor.verified) != 1 ||
		executor.verified[0].RunID != commandRunID ||
		executor.verified[0].SnapshotID != commandRecoveryID ||
		executor.verified[0].ManifestSHA256 != manifestHash {
		t.Fatalf("verified=%v", executor.verified)
	}
	if len(service.artifacts) != 3 ||
		service.artifacts[0].Kind != backup.ArtifactDatabaseDump ||
		service.artifacts[1].Kind != backup.ArtifactObjectSnapshot ||
		service.artifacts[2].Kind != backup.ArtifactManifest {
		t.Fatalf("artifacts=%+v", service.artifacts)
	}
	for _, artifact := range service.artifacts {
		if artifact.OwnerID != owner ||
			artifact.LeaseGeneration != 9 ||
			artifact.Repository != backup.RepositoryLocal ||
			len(artifact.SHA256) != 32 {
			t.Fatalf("artifact=%+v", artifact)
		}
	}
	if states.state != nil {
		t.Fatalf("terminal state file remains: %+v", states.state)
	}
}

func TestCommandApplicationLogsOneSpecificSnapshotCategoryPerFailure(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		category  string
		configure func(
			*commandApplication,
			*workflowServiceFixture,
			*workflowExecutorFixture,
			*memoryWorkflowStates,
		)
	}{
		{
			name: "resume", category: "resume",
			configure: func(
				application *commandApplication,
				_ *workflowServiceFixture,
				_ *workflowExecutorFixture,
				_ *memoryWorkflowStates,
			) {
				application.service = nil
			},
		},
		{
			name:     "transition-snapshotting",
			category: "transition_snapshotting",
			configure: func(
				_ *commandApplication,
				service *workflowServiceFixture,
				_ *workflowExecutorFixture,
				states *memoryWorkflowStates,
			) {
				service.state = backup.StateDraining
				service.transitionResponseLossAt = 1
				states.state.State = backup.StateDraining
			},
		},
		{
			name:     "state-save-snapshotting",
			category: "state_save_snapshotting",
			configure: func(
				_ *commandApplication,
				service *workflowServiceFixture,
				_ *workflowExecutorFixture,
				states *memoryWorkflowStates,
			) {
				service.state = backup.StateDraining
				states.state.State = backup.StateDraining
				states.failSaveAt = 1
			},
		},
		{
			name: "migration-version", category: "migration_version",
			configure: func(
				application *commandApplication,
				_ *workflowServiceFixture,
				_ *workflowExecutorFixture,
				_ *memoryWorkflowStates,
			) {
				application.migrationVersion = func(
					context.Context,
				) (int64, error) {
					return 0, errors.New("migration-version-secret")
				}
			},
		},
		{
			name: "executor-stage", category: "work_root",
			configure: func(
				application *commandApplication,
				_ *workflowServiceFixture,
				_ *workflowExecutorFixture,
				_ *memoryWorkflowStates,
			) {
				application.executor = &backup.Executor{}
			},
		},
		{
			name: "result-validation", category: "result_validation",
			configure: func(
				_ *commandApplication,
				_ *workflowServiceFixture,
				executor *workflowExecutorFixture,
				_ *memoryWorkflowStates,
			) {
				executor.snapshotResult = backup.SnapshotResult{}
			},
		},
		{
			name:     "transition-encrypting",
			category: "transition_encrypting",
			configure: func(
				_ *commandApplication,
				service *workflowServiceFixture,
				_ *workflowExecutorFixture,
				_ *memoryWorkflowStates,
			) {
				service.transitionResponseLossAt = 1
			},
		},
		{
			name:     "state-save-encrypting",
			category: "state_save_encrypting",
			configure: func(
				_ *commandApplication,
				_ *workflowServiceFixture,
				_ *workflowExecutorFixture,
				states *memoryWorkflowStates,
			) {
				states.failSaveAt = 1
			},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			runID := uuid.MustParse(commandRunID)
			owner := uuid.New()
			now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
			service := &workflowServiceFixture{
				runID: runID, owner: owner, generation: 4,
				state: backup.StateSnapshotting, leaseActive: true,
			}
			executor := &workflowExecutorFixture{
				snapshotResult: commandSnapshotResult(t, now),
			}
			states := &memoryWorkflowStates{state: &workflowState{
				RunID: runID, OwnerID: owner, LeaseGeneration: 4,
				State: backup.StateSnapshotting,
			}}
			application := &commandApplication{
				service: service, executor: executor, states: states,
				newOwner: uuid.New,
				migrationVersion: func(context.Context) (int64, error) {
					return 20, nil
				},
				now: func() time.Time { return now },
			}
			scenario.configure(application, service, executor, states)
			var failures []struct {
				category  string
				status    int
				hasStatus bool
			}
			application.logSnapshotCategory = func(
				category string,
				status int,
				hasStatus bool,
			) {
				failures = append(failures, struct {
					category  string
					status    int
					hasStatus bool
				}{
					category: category, status: status,
					hasStatus: hasStatus,
				})
			}

			if err := application.Snapshot(
				context.Background(),
				runID,
			); err == nil {
				t.Fatal("snapshot unexpectedly succeeded")
			}
			if len(failures) != 1 ||
				failures[0].category != scenario.category ||
				failures[0].hasStatus ||
				failures[0].status != 0 {
				t.Fatalf(
					"failures=%+v want category=%q without status",
					failures,
					scenario.category,
				)
			}
		})
	}
}

func TestCommandApplicationForwardsSafeSnapshotExitCode(t *testing.T) {
	const secret = "snapshot-command-stderr-secret"
	runID := uuid.MustParse(commandRunID)
	owner := uuid.New()
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	workRoot := t.TempDir()
	objectRoot := t.TempDir()
	if err := os.Chmod(workRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(objectRoot, "object"),
		[]byte("object"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	executor, err := backup.NewExecutor(backup.ExecutorConfig{
		Runner: snapshotExitRunner{code: 37, secret: secret},
		Secrets: snapshotExitSecrets{
			backup.SecretDatabasePassword: "database-password",
			backup.SecretLocalRepository:  "/repository",
			backup.SecretLocalPassword:    "repository-password",
		},
		WorkRoot:          workRoot,
		ObjectRoot:        objectRoot,
		DatabaseHost:      "postgres",
		DatabasePort:      "5432",
		DatabaseUser:      "happylearn",
		DatabaseName:      "happylearn",
		DatabaseSSLMode:   "require",
		AgeRecipient:      "age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqp5m40h",
		EncryptionKeyID:   "key-2026-07",
		Now:               func() time.Time { return now },
		MaxPlaintextBytes: 16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &workflowServiceFixture{
		runID: runID, owner: owner, generation: 4,
		state: backup.StateSnapshotting, leaseActive: true,
	}
	states := &memoryWorkflowStates{state: &workflowState{
		RunID: runID, OwnerID: owner, LeaseGeneration: 4,
		State: backup.StateSnapshotting,
	}}
	application := &commandApplication{
		service: service, executor: &executor, states: states,
		newOwner: uuid.New,
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: func() time.Time { return now },
	}
	var category string
	var status int
	var hasStatus bool
	application.logSnapshotCategory = func(
		actualCategory string,
		actualStatus int,
		actualHasStatus bool,
	) {
		category = actualCategory
		status = actualStatus
		hasStatus = actualHasStatus
	}

	if err := application.Snapshot(
		context.Background(),
		runID,
	); !errors.Is(err, errWorkflowUnavailable) {
		t.Fatalf("snapshot error=%v", err)
	}
	if category != "restic_exit" ||
		status != 37 ||
		!hasStatus {
		t.Fatalf(
			"category=%q status=%d present=%t",
			category,
			status,
			hasStatus,
		)
	}
	if strings.Contains(category, secret) {
		t.Fatalf("category leaked secret: %q", category)
	}
}

func TestCommandApplicationRejectsClaimForDifferentRun(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	service := &workflowServiceFixture{
		runID:      uuid.MustParse("30000000-0000-4000-8000-000000000003"),
		generation: 1,
		state:      backup.StateQueued,
	}
	states := &memoryWorkflowStates{}
	application := &commandApplication{
		service:  service,
		executor: &workflowExecutorFixture{},
		states:   states,
		newOwner: uuid.New,
		now:      time.Now,
	}
	if err := application.Prepare(context.Background(), runID); !errors.Is(err, errWorkflowUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if len(service.transitions) != 0 || states.state != nil {
		t.Fatalf("transitions=%v state=%+v", service.transitions, states.state)
	}
}

func TestCommandApplicationRemoteFailureCompletesDegradedWithSameFence(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	owner := uuid.MustParse("20000000-0000-4000-8000-000000000002")
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	service := &workflowServiceFixture{
		runID: runID, generation: 4, state: backup.StateVerifying,
	}
	service.owner = owner
	executor := &workflowExecutorFixture{
		remote: true, syncErr: backup.ErrRemoteSync,
	}
	evidence := backup.RecoveryEvidence{
		DatabaseMigrationVersion: 20,
		EncryptionKeyID:          "key-2026-07",
		LocalSnapshotID:          commandRecoveryID,
		ManifestSHA256:           make([]byte, 32),
		LocalExpiresAt:           now.Add(7 * 24 * time.Hour),
	}
	states := &memoryWorkflowStates{state: &workflowState{
		RunID: runID, OwnerID: owner, LeaseGeneration: 4,
		State: backup.StateVerifying, Evidence: evidence,
		ObjectSnapshotID: commandObjectSnapshotID,
	}}
	application := &commandApplication{
		service: service, executor: executor, states: states,
		newOwner: uuid.New,
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: func() time.Time { return now },
	}
	if err := application.Sync(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if states.state == nil ||
		states.state.State != backup.StateSyncing ||
		!states.state.RemoteConfigured ||
		states.state.RemoteSucceeded ||
		states.state.ErrorCategory != "remote_unavailable" {
		t.Fatalf("state=%+v", states.state)
	}
	if err := application.Finish(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if len(service.completions) != 1 ||
		service.completions[0].OwnerID != owner ||
		service.completions[0].LeaseGeneration != 4 ||
		service.completions[0].From != backup.StateSyncing ||
		service.completions[0].RemoteSucceeded ||
		service.completions[0].ErrorCategory != "remote_unavailable" {
		t.Fatalf("completions=%+v", service.completions)
	}
}

func TestCommandApplicationPostSyncRemoteFailureCompletesDegradedWithSameFence(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	owner := uuid.MustParse("20000000-0000-4000-8000-000000000002")
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	remoteExpiry := now.Add(30 * 24 * time.Hour)
	evidence := backup.RecoveryEvidence{
		DatabaseMigrationVersion: 20,
		EncryptionKeyID:          "key-2026-07",
		LocalSnapshotID:          commandRecoveryID,
		RemoteSnapshotID:         strings.Repeat("3", sha256.Size*2),
		ManifestSHA256:           make([]byte, sha256.Size),
		LocalExpiresAt:           now.Add(7 * 24 * time.Hour),
		RemoteExpiresAt:          &remoteExpiry,
	}
	service := &workflowServiceFixture{
		runID: runID, owner: owner, generation: 6,
		state: backup.StateSyncing, evidence: evidence, leaseActive: true,
	}
	states := &memoryWorkflowStates{state: &workflowState{
		RunID: runID, OwnerID: owner, LeaseGeneration: 6,
		State: backup.StateSyncing, Evidence: evidence,
		RemoteConfigured: true, RemoteSucceeded: true,
	}}
	application := &commandApplication{
		service: service, executor: &workflowExecutorFixture{}, states: states,
		newOwner: uuid.New,
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: func() time.Time { return now },
	}

	if err := application.Fail(
		context.Background(),
		runID,
		"remote_unavailable",
	); err != nil {
		t.Fatal(err)
	}
	if len(service.transitions) != 0 {
		t.Fatalf("unexpected failed transition=%+v", service.transitions)
	}
	if len(service.completions) != 1 {
		t.Fatalf("completions=%+v", service.completions)
	}
	completion := service.completions[0]
	if completion.OwnerID != owner ||
		completion.LeaseGeneration != 6 ||
		completion.From != backup.StateSyncing ||
		!completion.RemoteConfigured ||
		completion.RemoteSucceeded ||
		completion.ErrorCategory != "remote_unavailable" {
		t.Fatalf("completion=%+v", completion)
	}
	if states.state != nil {
		t.Fatalf("terminal state file remains: %+v", states.state)
	}
}

func TestCommandApplicationRetriesPostSyncRemoteDegradedTerminal(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	owner := uuid.MustParse("20000000-0000-4000-8000-000000000002")
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	evidence := backup.RecoveryEvidence{
		DatabaseMigrationVersion: 20,
		EncryptionKeyID:          "key-2026-07",
		LocalSnapshotID:          commandRecoveryID,
		ManifestSHA256:           make([]byte, sha256.Size),
		LocalExpiresAt:           now.Add(7 * 24 * time.Hour),
	}
	newApplication := func(
		service *workflowServiceFixture,
		states *memoryWorkflowStates,
	) commandApplication {
		return commandApplication{
			service: service, executor: &workflowExecutorFixture{}, states: states,
			newOwner: uuid.New,
			migrationVersion: func(context.Context) (int64, error) {
				return 20, nil
			},
			now: func() time.Time { return now },
		}
	}

	t.Run("stale-state-file-after-durable-completion", func(t *testing.T) {
		service := &workflowServiceFixture{
			runID: runID, owner: owner, generation: 6,
			state: backup.StateSyncing, evidence: evidence, leaseActive: true,
		}
		states := &memoryWorkflowStates{
			state: &workflowState{
				RunID: runID, OwnerID: owner, LeaseGeneration: 6,
				State: backup.StateSyncing, Evidence: evidence,
				RemoteConfigured: true, RemoteSucceeded: true,
			},
			failDeleteAt: 1,
		}
		application := newApplication(service, states)
		if err := application.Fail(
			context.Background(),
			runID,
			"remote_unavailable",
		); !errors.Is(err, errWorkflowState) {
			t.Fatalf("first fail err=%v", err)
		}
		if service.state != backup.StateDegraded || states.state == nil {
			t.Fatalf("durable=%s state=%+v", service.state, states.state)
		}
		if err := application.Fail(
			context.Background(),
			runID,
			"remote_unavailable",
		); err != nil {
			t.Fatalf("retry fail: %v", err)
		}
		if states.state != nil || states.deleteCalls != 2 {
			t.Fatalf("state=%+v deletes=%d", states.state, states.deleteCalls)
		}
		if len(service.completions) != 1 {
			t.Fatalf("completions=%+v", service.completions)
		}
	})

	t.Run("missing-state-file-after-durable-completion", func(t *testing.T) {
		service := &workflowServiceFixture{
			runID: runID, generation: 6,
			state: backup.StateDegraded, evidence: evidence,
		}
		states := &memoryWorkflowStates{}
		application := newApplication(service, states)
		if err := application.Fail(
			context.Background(),
			runID,
			"remote_unavailable",
		); err != nil {
			t.Fatalf("retry fail: %v", err)
		}
		if err := application.Fail(
			context.Background(),
			runID,
			"integrity",
		); !errors.Is(err, errWorkflowUnavailable) {
			t.Fatalf("ordinary fail err=%v", err)
		}
	})
}

func TestCommandApplicationClearsRemoteFailureAfterVerifiedRetry(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	owner := uuid.New()
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	result := commandSnapshotResult(t, now)
	evidence := commandEvidence(result, now)
	service := &workflowServiceFixture{
		runID: runID, owner: owner, generation: 5,
		state: backup.StateVerifying, evidence: evidence, leaseActive: true,
	}
	states := &memoryWorkflowStates{state: &workflowState{
		RunID: runID, OwnerID: owner, LeaseGeneration: 5,
		State: backup.StateVerifying, Evidence: evidence,
		ObjectSnapshotID:   result.Manifest.ObjectSnapshotID,
		DatabaseDumpSHA256: result.Manifest.DatabaseDumpSHA256,
		DatabaseDumpBytes:  result.DatabaseDumpBytes,
		ReferencedBytes:    result.Manifest.ReferencedBytes,
		ManifestBytes:      int64(len(mustManifestBytes(t, result.Manifest))),
	}}
	executor := &workflowExecutorFixture{
		snapshotResult: result,
		remote:         true,
		syncErr:        backup.ErrRemoteSync,
		syncResult:     strings.Repeat("3", sha256.Size*2),
	}
	application := commandApplication{
		service: service, executor: executor, states: states,
		newOwner: uuid.New,
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: func() time.Time { return now },
	}
	if err := application.Sync(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if states.state == nil ||
		states.state.ErrorCategory != "remote_unavailable" ||
		len(service.artifacts) != 0 {
		t.Fatalf("failed sync state=%+v artifacts=%+v", states.state, service.artifacts)
	}
	executor.syncErr = nil
	if err := application.Sync(context.Background(), runID); err != nil {
		t.Fatalf("retry sync: %v", err)
	}
	if states.state == nil || states.state.ErrorCategory != "" {
		t.Fatalf("successful retry retained failure: %+v", states.state)
	}
	if err := application.Finish(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if len(service.completions) != 1 ||
		service.completions[0].ErrorCategory != "" ||
		!service.completions[0].RemoteSucceeded {
		t.Fatalf("completions=%+v", service.completions)
	}
}

func TestCommandApplicationDoesNotConvertCancellationToRemoteDegradation(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	owner := uuid.MustParse("20000000-0000-4000-8000-000000000002")
	service := &workflowServiceFixture{
		runID: runID, owner: owner, generation: 4, state: backup.StateVerifying,
	}
	states := &memoryWorkflowStates{state: &workflowState{
		RunID: runID, OwnerID: owner, LeaseGeneration: 4,
		State: backup.StateVerifying,
		Evidence: backup.RecoveryEvidence{
			DatabaseMigrationVersion: 20,
			EncryptionKeyID:          "key-2026-07",
			LocalSnapshotID:          commandRecoveryID,
			ManifestSHA256:           make([]byte, 32),
			LocalExpiresAt:           time.Now().UTC().Add(7 * 24 * time.Hour),
		},
		ObjectSnapshotID: commandObjectSnapshotID,
	}}
	application := &commandApplication{
		service: service,
		executor: &workflowExecutorFixture{
			remote: true, syncErr: backup.ErrCancelled,
		},
		states: states, newOwner: uuid.New,
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: time.Now,
	}
	if err := application.Sync(context.Background(), runID); !errors.Is(err, errWorkflowUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if states.state == nil {
		t.Fatal("workflow state disappeared after cancellation")
	}
	if states.state.ErrorCategory == "remote_unavailable" {
		t.Fatalf("cancellation was converted to degradation: %+v", states.state)
	}
	if err := application.Fail(
		context.Background(),
		runID,
		"remote_unavailable",
	); err != nil {
		t.Fatalf("explicit remote failure completion: %v", err)
	}
	if service.state != backup.StateDegraded ||
		len(service.completions) != 1 ||
		service.completions[0].RemoteSucceeded ||
		service.completions[0].ErrorCategory != "remote_unavailable" ||
		states.state != nil {
		t.Fatalf(
			"durable=%s completions=%+v state=%+v",
			service.state,
			service.completions,
			states.state,
		)
	}
}

func TestCommandApplicationRecordsVerifiedRemoteArtifactsAfterSuccessfulSync(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	owner := uuid.MustParse("20000000-0000-4000-8000-000000000002")
	now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	service := &workflowServiceFixture{
		runID: runID, owner: owner, generation: 5, state: backup.StateVerifying,
	}
	states := &memoryWorkflowStates{state: &workflowState{
		RunID: runID, OwnerID: owner, LeaseGeneration: 5,
		State: backup.StateVerifying,
		Evidence: backup.RecoveryEvidence{
			DatabaseMigrationVersion: 20,
			EncryptionKeyID:          "key-2026-07",
			LocalSnapshotID:          commandRecoveryID,
			ManifestSHA256:           make([]byte, 32),
			LocalExpiresAt:           now.Add(7 * 24 * time.Hour),
		},
		ObjectSnapshotID:   commandObjectSnapshotID,
		DatabaseDumpSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		DatabaseDumpBytes:  22,
		ReferencedBytes:    11,
		ManifestBytes:      300,
	}}
	application := &commandApplication{
		service: service,
		executor: &workflowExecutorFixture{
			remote: true, syncResult: commandRecoveryID,
		},
		states: states, newOwner: uuid.New,
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: func() time.Time { return now },
	}
	if err := application.Sync(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if len(service.artifacts) != 3 {
		t.Fatalf("artifacts=%+v", service.artifacts)
	}
	for _, artifact := range service.artifacts {
		if artifact.Repository != backup.RepositoryRemote ||
			artifact.OwnerID != owner ||
			artifact.LeaseGeneration != 5 ||
			len(artifact.SHA256) != 32 {
			t.Fatalf("artifact=%+v", artifact)
		}
	}
}

func TestCommandApplicationPrepareClaimsOnlyTheExactTargetRun(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	service := &workflowServiceFixture{
		runID: runID, generation: 1, state: backup.StateQueued,
	}
	states := &memoryWorkflowStates{}
	application := commandApplication{
		service: service, executor: &workflowExecutorFixture{},
		states: states, newOwner: uuid.New,
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: time.Now,
	}
	if err := application.Prepare(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if service.genericClaims != 0 ||
		len(service.targetClaims) != 1 ||
		service.targetClaims[0] != runID {
		t.Fatalf(
			"generic=%d targets=%v",
			service.genericClaims,
			service.targetClaims,
		)
	}
}

func TestCommandApplicationPrepareRenewsExistingLocalFenceBeforeClaim(t *testing.T) {
	for _, scenario := range []string{
		"same-live-owner",
		"other-live-owner",
		"expired-owner",
		"corrupt-state",
		"mismatched-run",
	} {
		t.Run(scenario, func(t *testing.T) {
			runID := uuid.MustParse(commandRunID)
			localOwner := uuid.New()
			durableOwner := localOwner
			service := &workflowServiceFixture{
				runID: runID, owner: durableOwner, generation: 4,
				state: backup.StateDraining, leaseActive: true,
			}
			states := &memoryWorkflowStates{state: &workflowState{
				RunID: runID, OwnerID: localOwner, LeaseGeneration: 4,
				State: backup.StateDraining,
			}}
			switch scenario {
			case "other-live-owner":
				service.owner = uuid.New()
			case "expired-owner":
				service.leaseActive = false
				service.renewErr = backup.ErrStaleOwner
			case "corrupt-state":
				states.loadErr = errWorkflowState
			case "mismatched-run":
				states.state.RunID = uuid.New()
			}
			newOwnerCalls := 0
			newOwner := uuid.New()
			application := commandApplication{
				service: service, executor: &workflowExecutorFixture{},
				states: states,
				newOwner: func() uuid.UUID {
					newOwnerCalls++
					return newOwner
				},
				migrationVersion: func(context.Context) (int64, error) {
					return 20, nil
				},
				now: time.Now,
			}
			err := application.Prepare(context.Background(), runID)
			switch scenario {
			case "same-live-owner":
				if err != nil ||
					service.renewAttempts != 1 ||
					len(service.targetClaims) != 0 ||
					newOwnerCalls != 0 {
					t.Fatalf(
						"err=%v renew=%d claims=%v newOwners=%d",
						err,
						service.renewAttempts,
						service.targetClaims,
						newOwnerCalls,
					)
				}
			case "expired-owner":
				if err != nil ||
					service.renewAttempts != 1 ||
					len(service.targetClaims) != 1 ||
					states.state == nil ||
					states.state.OwnerID != newOwner ||
					states.state.LeaseGeneration != 5 {
					t.Fatalf(
						"err=%v renew=%d claims=%v state=%+v",
						err,
						service.renewAttempts,
						service.targetClaims,
						states.state,
					)
				}
			default:
				if !errors.Is(err, errWorkflowUnavailable) &&
					!errors.Is(err, errWorkflowState) {
					t.Fatalf("err=%v", err)
				}
				if len(service.transitions) != 0 {
					t.Fatalf("transitions=%+v", service.transitions)
				}
				if scenario == "other-live-owner" &&
					service.renewAttempts != 1 {
					t.Fatalf("renew attempts=%d", service.renewAttempts)
				}
				if (scenario == "corrupt-state" ||
					scenario == "mismatched-run") &&
					(len(service.targetClaims) != 0 || newOwnerCalls != 0) {
					t.Fatalf(
						"claims=%v newOwners=%d",
						service.targetClaims,
						newOwnerCalls,
					)
				}
			}
		})
	}
}

func TestCommandApplicationRecoversPrepareAfterDrainingStateSaveFailure(t *testing.T) {
	runID := uuid.MustParse(commandRunID)
	owners := []uuid.UUID{uuid.New(), uuid.New()}
	nextOwner := 0
	service := &workflowServiceFixture{
		runID: runID, generation: 1, state: backup.StateQueued,
	}
	states := &memoryWorkflowStates{failSaveAt: 1}
	application := commandApplication{
		service: service, executor: &workflowExecutorFixture{},
		states: states,
		newOwner: func() uuid.UUID {
			owner := owners[nextOwner]
			nextOwner++
			return owner
		},
		migrationVersion: func(context.Context) (int64, error) {
			return 20, nil
		},
		now: time.Now,
	}
	if err := application.Prepare(
		context.Background(),
		runID,
	); !errors.Is(err, errWorkflowState) {
		t.Fatalf("first prepare err=%v", err)
	}
	if service.state != backup.StateDraining || states.state != nil {
		t.Fatalf("durable=%s local=%+v", service.state, states.state)
	}
	service.leaseActive = false
	if err := application.Prepare(context.Background(), runID); err != nil {
		t.Fatal(err)
	}
	if states.state == nil ||
		states.state.State != backup.StateDraining ||
		states.state.OwnerID != owners[1] ||
		states.state.LeaseGeneration != 2 ||
		len(service.transitions) != 1 {
		t.Fatalf(
			"state=%+v transitions=%+v",
			states.state,
			service.transitions,
		)
	}
}

func TestCommandApplicationRecoversSnapshotTransitionSaveFailures(t *testing.T) {
	for _, failSaveAt := range []int{1, 2} {
		t.Run(fmt.Sprintf("save-%d", failSaveAt), func(t *testing.T) {
			runID := uuid.MustParse(commandRunID)
			owner := uuid.New()
			now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
			result := commandSnapshotResult(t, now)
			service := &workflowServiceFixture{
				runID: runID, owner: owner, generation: 4,
				state: backup.StateDraining, leaseActive: true,
			}
			states := &memoryWorkflowStates{
				state: &workflowState{
					RunID: runID, OwnerID: owner, LeaseGeneration: 4,
					State: backup.StateDraining,
				},
				failSaveAt: failSaveAt,
			}
			executor := &workflowExecutorFixture{snapshotResult: result}
			application := commandApplication{
				service: service, executor: executor, states: states,
				newOwner: uuid.New,
				migrationVersion: func(context.Context) (int64, error) {
					return 20, nil
				},
				now: func() time.Time { return now },
			}
			if err := application.Snapshot(
				context.Background(),
				runID,
			); !errors.Is(err, errWorkflowState) {
				t.Fatalf("first snapshot err=%v", err)
			}
			if err := application.Snapshot(context.Background(), runID); err != nil {
				t.Fatalf("retry snapshot: %v", err)
			}
			if service.state != backup.StateEncrypting ||
				states.state == nil ||
				states.state.State != backup.StateEncrypting ||
				service.evidence.LocalSnapshotID != commandRecoveryID {
				t.Fatalf(
					"durable=%s local=%+v evidence=%+v",
					service.state,
					states.state,
					service.evidence,
				)
			}
			wantSnapshotCalls := 1
			if failSaveAt == 1 {
				wantSnapshotCalls = 1
			}
			if executor.snapshotCalls != wantSnapshotCalls {
				t.Fatalf(
					"snapshot calls=%d want=%d",
					executor.snapshotCalls,
					wantSnapshotCalls,
				)
			}
		})
	}
}

func TestCommandApplicationRecoversVerifyAndSyncTransitionSaveFailures(t *testing.T) {
	t.Run("verify", func(t *testing.T) {
		runID := uuid.MustParse(commandRunID)
		owner := uuid.New()
		now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
		result := commandSnapshotResult(t, now)
		evidence := commandEvidence(result, now)
		service := &workflowServiceFixture{
			runID: runID, owner: owner, generation: 5,
			state: backup.StateEncrypting, evidence: evidence, leaseActive: true,
		}
		states := &memoryWorkflowStates{
			state: &workflowState{
				RunID: runID, OwnerID: owner, LeaseGeneration: 5,
				State: backup.StateEncrypting, Evidence: evidence,
			},
			failSaveAt: 1,
		}
		executor := &workflowExecutorFixture{snapshotResult: result}
		application := commandApplication{
			service: service, executor: executor, states: states,
			newOwner: uuid.New,
			migrationVersion: func(context.Context) (int64, error) {
				return 20, nil
			},
			now: func() time.Time { return now },
		}
		if err := application.Verify(
			context.Background(),
			runID,
		); !errors.Is(err, errWorkflowState) {
			t.Fatalf("first verify err=%v", err)
		}
		if err := application.Verify(context.Background(), runID); err != nil {
			t.Fatalf("retry verify: %v", err)
		}
		if states.state == nil ||
			states.state.State != backup.StateVerifying ||
			len(service.transitions) != 1 {
			t.Fatalf("state=%+v transitions=%+v", states.state, service.transitions)
		}
	})

	t.Run("sync", func(t *testing.T) {
		runID := uuid.MustParse(commandRunID)
		owner := uuid.New()
		now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
		result := commandSnapshotResult(t, now)
		evidence := commandEvidence(result, now)
		service := &workflowServiceFixture{
			runID: runID, owner: owner, generation: 6,
			state: backup.StateVerifying, evidence: evidence, leaseActive: true,
		}
		states := &memoryWorkflowStates{
			state: &workflowState{
				RunID: runID, OwnerID: owner, LeaseGeneration: 6,
				State: backup.StateVerifying, Evidence: evidence,
				ObjectSnapshotID:   result.Manifest.ObjectSnapshotID,
				DatabaseDumpSHA256: result.Manifest.DatabaseDumpSHA256,
				DatabaseDumpBytes:  result.DatabaseDumpBytes,
				ReferencedBytes:    result.Manifest.ReferencedBytes,
				ManifestBytes:      int64(len(mustManifestBytes(t, result.Manifest))),
			},
			failSaveAt: 1,
		}
		executor := &workflowExecutorFixture{
			snapshotResult: result,
			remote:         true, syncResult: commandRecoveryID,
		}
		application := commandApplication{
			service: service, executor: executor, states: states,
			newOwner: uuid.New,
			migrationVersion: func(context.Context) (int64, error) {
				return 20, nil
			},
			now: func() time.Time { return now },
		}
		if err := application.Sync(
			context.Background(),
			runID,
		); !errors.Is(err, errWorkflowState) {
			t.Fatalf("first sync err=%v", err)
		}
		if err := application.Sync(context.Background(), runID); err != nil {
			t.Fatalf("retry sync: %v", err)
		}
		if states.state == nil ||
			states.state.State != backup.StateSyncing ||
			!states.state.RemoteConfigured ||
			!states.state.RemoteSucceeded ||
			len(service.transitions) != 1 {
			t.Fatalf("state=%+v transitions=%+v", states.state, service.transitions)
		}
	})
}

func TestCommandApplicationRetriesArtifactsWithStableTimesAfterCrash(t *testing.T) {
	t.Run("local-transition-response-loss", func(t *testing.T) {
		runID := uuid.MustParse(commandRunID)
		owner := uuid.New()
		startedAt := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
		result := commandSnapshotResult(t, startedAt)
		evidence := commandEvidence(result, startedAt)
		service := &workflowServiceFixture{
			runID: runID, owner: owner, generation: 7,
			state: backup.StateEncrypting, evidence: evidence, leaseActive: true,
			transitionResponseLossAt: 1, strictArtifacts: true,
		}
		states := &memoryWorkflowStates{state: &workflowState{
			RunID: runID, OwnerID: owner, LeaseGeneration: 7,
			State: backup.StateEncrypting, Evidence: evidence,
		}}
		clockCalls := 0
		application := commandApplication{
			service:  service,
			executor: &workflowExecutorFixture{snapshotResult: result},
			states:   states, newOwner: uuid.New,
			migrationVersion: func(context.Context) (int64, error) {
				return 20, nil
			},
			now: func() time.Time {
				at := startedAt.Add(time.Duration(clockCalls) * time.Hour)
				clockCalls++
				return at
			},
		}
		if err := application.Verify(
			context.Background(),
			runID,
		); !errors.Is(err, errWorkflowUnavailable) {
			t.Fatalf("first verify err=%v", err)
		}
		if len(service.artifacts) != 3 ||
			service.state != backup.StateVerifying {
			t.Fatalf(
				"artifacts=%+v durable=%s",
				service.artifacts,
				service.state,
			)
		}
		if err := application.Verify(context.Background(), runID); err != nil {
			t.Fatalf("retry verify: %v", err)
		}
		assertStableArtifactTimes(t, service.artifacts)
	})

	t.Run("remote-state-save-crash", func(t *testing.T) {
		runID := uuid.MustParse(commandRunID)
		owner := uuid.New()
		startedAt := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
		result := commandSnapshotResult(t, startedAt)
		evidence := commandEvidence(result, startedAt)
		service := &workflowServiceFixture{
			runID: runID, owner: owner, generation: 8,
			state: backup.StateSyncing, evidence: evidence, leaseActive: true,
			strictArtifacts: true,
		}
		crashed := false
		states := &memoryWorkflowStates{
			state: &workflowState{
				RunID: runID, OwnerID: owner, LeaseGeneration: 8,
				State: backup.StateSyncing, Evidence: evidence,
				ObjectSnapshotID:   result.Manifest.ObjectSnapshotID,
				DatabaseDumpSHA256: result.Manifest.DatabaseDumpSHA256,
				DatabaseDumpBytes:  result.DatabaseDumpBytes,
				ReferencedBytes:    result.Manifest.ReferencedBytes,
				ManifestBytes:      int64(len(mustManifestBytes(t, result.Manifest))),
				RemoteConfigured:   true,
			},
			failSave: func(workflowState, int) bool {
				if !crashed && len(service.artifacts) == 3 {
					crashed = true
					return true
				}
				return false
			},
		}
		clockCalls := 0
		application := commandApplication{
			service: service,
			executor: &workflowExecutorFixture{
				snapshotResult: result,
				remote:         true,
				syncResult:     commandRecoveryID,
			},
			states: states, newOwner: uuid.New,
			migrationVersion: func(context.Context) (int64, error) {
				return 20, nil
			},
			now: func() time.Time {
				at := startedAt.Add(time.Duration(clockCalls) * time.Hour)
				clockCalls++
				return at
			},
		}
		if err := application.Sync(
			context.Background(),
			runID,
		); !errors.Is(err, errWorkflowState) {
			t.Fatalf("first sync err=%v", err)
		}
		if len(service.artifacts) != 3 {
			t.Fatalf("artifacts=%+v", service.artifacts)
		}
		if err := application.Sync(context.Background(), runID); err != nil {
			t.Fatalf("retry sync: %v", err)
		}
		assertStableArtifactTimes(t, service.artifacts)
	})
}

func assertStableArtifactTimes(t *testing.T, artifacts []backup.Artifact) {
	t.Helper()
	if len(artifacts) != 3 {
		t.Fatalf("artifacts=%+v", artifacts)
	}
	for _, artifact := range artifacts[1:] {
		if !artifact.VerifiedAt.Equal(artifacts[0].VerifiedAt) ||
			!artifact.ExpiresAt.Equal(artifacts[0].ExpiresAt) {
			t.Fatalf("artifact times are not stable: %+v", artifacts)
		}
	}
}

func TestCommandApplicationRecoversTerminalDeleteFailures(t *testing.T) {
	for _, command := range []string{"finish", "fail"} {
		t.Run(command, func(t *testing.T) {
			runID := uuid.MustParse(commandRunID)
			owner := uuid.New()
			now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
			result := commandSnapshotResult(t, now)
			evidence := commandEvidence(result, now)
			service := &workflowServiceFixture{
				runID: runID, owner: owner, generation: 7,
				state: backup.StateVerifying, evidence: evidence, leaseActive: true,
			}
			states := &memoryWorkflowStates{
				state: &workflowState{
					RunID: runID, OwnerID: owner, LeaseGeneration: 7,
					State: backup.StateVerifying, Evidence: evidence,
				},
				failDeleteAt: 1,
			}
			application := commandApplication{
				service:  service,
				executor: &workflowExecutorFixture{snapshotResult: result},
				states:   states, newOwner: uuid.New,
				migrationVersion: func(context.Context) (int64, error) {
					return 20, nil
				},
				now: func() time.Time { return now },
			}
			call := application.Finish
			if command == "fail" {
				call = func(ctx context.Context, runID uuid.UUID) error {
					return application.Fail(ctx, runID, "integrity")
				}
			}
			if err := call(
				context.Background(),
				runID,
			); !errors.Is(err, errWorkflowState) {
				t.Fatalf("first %s err=%v", command, err)
			}
			if err := call(context.Background(), runID); err != nil {
				t.Fatalf("retry %s: %v", command, err)
			}
			wantTerminal := backup.StateSucceeded
			if command == "fail" {
				wantTerminal = backup.StateFailed
			}
			if service.state != wantTerminal ||
				states.state != nil ||
				states.deleteCalls != 2 {
				t.Fatalf(
					"durable=%s local=%+v deletes=%d",
					service.state,
					states.state,
					states.deleteCalls,
				)
			}
		})
	}
}

func TestCommandApplicationRetriesMatchingTerminalAfterStateWasDeleted(t *testing.T) {
	for _, command := range []string{"finish", "fail"} {
		t.Run(command, func(t *testing.T) {
			runID := uuid.MustParse(commandRunID)
			owner := uuid.New()
			now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
			result := commandSnapshotResult(t, now)
			evidence := commandEvidence(result, now)
			service := &workflowServiceFixture{
				runID: runID, owner: owner, generation: 9,
				state: backup.StateVerifying, evidence: evidence, leaseActive: true,
			}
			states := &memoryWorkflowStates{state: &workflowState{
				RunID: runID, OwnerID: owner, LeaseGeneration: 9,
				State: backup.StateVerifying, Evidence: evidence,
			}}
			application := commandApplication{
				service:  service,
				executor: &workflowExecutorFixture{snapshotResult: result},
				states:   states, newOwner: uuid.New,
				migrationVersion: func(context.Context) (int64, error) {
					return 20, nil
				},
				now: func() time.Time { return now },
			}
			call := application.Finish
			if command == "fail" {
				call = func(ctx context.Context, runID uuid.UUID) error {
					return application.Fail(ctx, runID, "integrity")
				}
			}
			if err := call(context.Background(), runID); err != nil {
				t.Fatalf("first %s: %v", command, err)
			}
			if states.state != nil {
				t.Fatalf("state was not deleted: %+v", states.state)
			}
			if err := call(context.Background(), runID); err != nil {
				t.Fatalf("retry %s: %v", command, err)
			}
			if service.workflowCalls != 1 {
				t.Fatalf("workflow lookups=%d", service.workflowCalls)
			}
			if command == "finish" && len(service.completions) != 1 {
				t.Fatalf("completions=%+v", service.completions)
			}
			if command == "fail" && len(service.transitions) != 1 {
				t.Fatalf("transitions=%+v", service.transitions)
			}
		})
	}
}

func TestCommandApplicationMissingStateFailsClosedForWrongOrNonterminalRun(t *testing.T) {
	for _, state := range []backup.State{
		backup.StateQueued,
		backup.StateFailed,
	} {
		t.Run(string(state), func(t *testing.T) {
			runID := uuid.MustParse(commandRunID)
			service := &workflowServiceFixture{
				runID: runID, state: state, generation: 2,
			}
			application := commandApplication{
				service: service, executor: &workflowExecutorFixture{},
				states: &memoryWorkflowStates{}, newOwner: uuid.New,
				migrationVersion: func(context.Context) (int64, error) {
					return 20, nil
				},
				now: time.Now,
			}
			if err := application.Finish(
				context.Background(),
				runID,
			); !errors.Is(err, errWorkflowUnavailable) {
				t.Fatalf("state=%s finish err=%v", state, err)
			}
		})
	}
	t.Run("fail-succeeded", func(t *testing.T) {
		runID := uuid.MustParse(commandRunID)
		service := &workflowServiceFixture{
			runID: runID, state: backup.StateSucceeded, generation: 2,
		}
		application := commandApplication{
			service: service, executor: &workflowExecutorFixture{},
			states: &memoryWorkflowStates{}, newOwner: uuid.New,
			migrationVersion: func(context.Context) (int64, error) {
				return 20, nil
			},
			now: time.Now,
		}
		if err := application.Fail(
			context.Background(),
			runID,
			"integrity",
		); !errors.Is(err, errWorkflowUnavailable) {
			t.Fatalf("fail err=%v", err)
		}
	})
}

func TestCommandApplicationLeaseTakeoverRefreshesFenceAndRejectsLiveOwner(t *testing.T) {
	for _, liveOwner := range []bool{false, true} {
		name := "expired"
		if liveOwner {
			name = "live"
		}
		t.Run(name, func(t *testing.T) {
			runID := uuid.MustParse(commandRunID)
			oldOwner, durableOwner, newOwner := uuid.New(), oldOwnerPlaceholder(), uuid.New()
			if !liveOwner {
				durableOwner = oldOwner
			}
			service := &workflowServiceFixture{
				runID: runID, owner: durableOwner, generation: 4,
				state: backup.StateDraining, leaseActive: liveOwner,
				renewErr: backup.ErrStaleOwner,
			}
			states := &memoryWorkflowStates{state: &workflowState{
				RunID: runID, OwnerID: oldOwner, LeaseGeneration: 4,
				State: backup.StateDraining,
			}}
			application := commandApplication{
				service: service, executor: &workflowExecutorFixture{},
				states: states, newOwner: func() uuid.UUID { return newOwner },
				migrationVersion: func(context.Context) (int64, error) {
					return 20, nil
				},
				now: time.Now,
			}
			err := application.Fail(context.Background(), runID, "lease_lost")
			if liveOwner {
				if !errors.Is(err, errWorkflowUnavailable) ||
					len(service.transitions) != 0 ||
					len(service.targetClaims) != 1 {
					t.Fatalf(
						"err=%v transitions=%+v targets=%v",
						err,
						service.transitions,
						service.targetClaims,
					)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(service.transitions) != 1 ||
				service.transitions[0].OwnerID != newOwner ||
				service.transitions[0].LeaseGeneration != 5 ||
				service.transitions[0].OwnerID == oldOwner {
				t.Fatalf("transitions=%+v", service.transitions)
			}
		})
	}
}

func oldOwnerPlaceholder() uuid.UUID {
	return uuid.MustParse("90000000-0000-4000-8000-000000000009")
}

func TestWorkflowStateMutationsFsyncStateDirectoryAfterMetadataChange(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	saveStart := strings.Index(text, "func (states fileWorkflowStates) Save")
	deleteStart := strings.Index(text, "func (states fileWorkflowStates) Delete")
	pathStart := strings.Index(text, "func (states fileWorkflowStates) path")
	if saveStart < 0 || deleteStart <= saveStart || pathStart <= deleteStart {
		t.Fatal("workflow state method boundaries missing")
	}
	saveBody := text[saveStart:deleteStart]
	deleteBody := text[deleteStart:pathStart]
	assertOrderedCall := func(body, mutation, syncCall string) {
		t.Helper()
		mutationAt := strings.Index(body, mutation)
		syncAt := strings.Index(body, syncCall)
		if mutationAt < 0 || syncAt <= mutationAt {
			t.Fatalf(
				"%s must occur after %s:\n%s",
				syncCall,
				mutation,
				body,
			)
		}
	}
	assertOrderedCall(saveBody, "os.Rename(", "states.syncRoot(")
	assertOrderedCall(deleteBody, "os.Remove(", "states.syncRoot(")
}

func TestWorkflowStateMutationsPropagateDirectorySyncFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runID := uuid.MustParse(commandRunID)
	state := workflowState{
		RunID: runID, OwnerID: uuid.New(), LeaseGeneration: 1,
		State: backup.StateDraining,
	}
	statePath := filepath.Join(root, runID.String()+".json")
	syncCalls := 0
	states := fileWorkflowStates{
		root: root,
		syncDirectory: func(path string) error {
			syncCalls++
			if path != root {
				t.Fatalf("sync path=%q", path)
			}
			if _, err := os.Stat(statePath); err != nil {
				t.Fatalf("rename did not precede directory sync: %v", err)
			}
			return errors.New("injected directory sync failure")
		},
	}
	if err := states.Save(state); !errors.Is(err, errWorkflowState) {
		t.Fatalf("save err=%v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("save sync calls=%d", syncCalls)
	}
	states.syncDirectory = func(path string) error {
		syncCalls++
		if path != root {
			t.Fatalf("sync path=%q", path)
		}
		if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("delete did not precede directory sync: %v", err)
		}
		return errors.New("injected directory sync failure")
	}
	if err := states.Delete(runID); !errors.Is(err, errWorkflowState) {
		t.Fatalf("delete err=%v", err)
	}
	if syncCalls != 2 {
		t.Fatalf("total sync calls=%d", syncCalls)
	}
}

func commandSnapshotResult(t *testing.T, now time.Time) backup.SnapshotResult {
	t.Helper()
	manifest := backup.Manifest{
		SchemaVersion: 1, BatchID: commandRunID, CreatedAt: now,
		DatabaseMigrationVersion: 20,
		DatabaseDumpSHA256:       strings.Repeat("a", sha256.Size*2),
		ObjectSnapshotID:         commandObjectSnapshotID,
		ObjectCount:              1,
		ReferencedBytes:          11,
	}
	manifestHash := sha256.Sum256(mustManifestBytes(t, manifest))
	return backup.SnapshotResult{
		Manifest: manifest, ManifestSHA256: manifestHash,
		EncryptionKeyID: "key-2026-07", LocalSnapshotID: commandRecoveryID,
		DatabaseDumpBytes: 22, LogicalBytes: 33, StoredBytes: 21,
	}
}

func commandEvidence(
	result backup.SnapshotResult,
	now time.Time,
) backup.RecoveryEvidence {
	logicalBytes, storedBytes := result.LogicalBytes, result.StoredBytes
	return backup.RecoveryEvidence{
		DatabaseMigrationVersion: result.Manifest.DatabaseMigrationVersion,
		EncryptionKeyID:          result.EncryptionKeyID,
		LocalSnapshotID:          result.LocalSnapshotID,
		ManifestSHA256:           append([]byte(nil), result.ManifestSHA256[:]...),
		LogicalBytes:             &logicalBytes,
		StoredBytes:              &storedBytes,
		LocalExpiresAt:           now.Add(7 * 24 * time.Hour),
	}
}

func mustManifestBytes(t *testing.T, manifest backup.Manifest) []byte {
	t.Helper()
	encoded, err := backup.MarshalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
