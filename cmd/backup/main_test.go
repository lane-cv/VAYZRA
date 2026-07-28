package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/backup"
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
	runID       uuid.UUID
	owner       uuid.UUID
	generation  int64
	state       backup.State
	renewCalls  int
	transitions []backup.TransitionInput
	completions []backup.CompletionInput
	artifacts   []backup.Artifact
}

func (service *workflowServiceFixture) Claim(
	_ context.Context,
	owner uuid.UUID,
	_ time.Duration,
) (backup.Run, error) {
	service.owner = owner
	return backup.Run{
		ID: service.runID, State: backup.StateQueued, OwnerID: owner,
		LeaseGeneration: service.generation,
	}, nil
}

func (service *workflowServiceFixture) Renew(
	_ context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	generation int64,
	_ time.Duration,
) (backup.Run, error) {
	if runID != service.runID || owner != service.owner || generation != service.generation {
		return backup.Run{}, backup.ErrStaleOwner
	}
	service.renewCalls++
	return backup.Run{
		ID: runID, State: service.state, OwnerID: owner,
		LeaseGeneration: generation,
	}, nil
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
	return backup.Run{ID: input.RunID, State: backup.StateSucceeded}, nil
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
	service.artifacts = append(service.artifacts, artifact)
	return nil
}

type workflowExecutorFixture struct {
	snapshotResult backup.SnapshotResult
	verified       []backup.VerifyInput
	remote         bool
	remoteErr      error
	syncResult     string
	syncErr        error
}

func (executor *workflowExecutorFixture) Snapshot(
	context.Context,
	backup.SnapshotInput,
) (backup.SnapshotResult, error) {
	return executor.snapshotResult, nil
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
	string,
	[]string,
) (string, error) {
	return executor.syncResult, executor.syncErr
}

type memoryWorkflowStates struct {
	state *workflowState
}

func (states *memoryWorkflowStates) Load(uuid.UUID) (workflowState, error) {
	if states.state == nil {
		return workflowState{}, errWorkflowState
	}
	return *states.state, nil
}

func (states *memoryWorkflowStates) Save(state workflowState) error {
	cloned := state
	states.state = &cloned
	return nil
}

func (states *memoryWorkflowStates) Delete(uuid.UUID) error {
	states.state = nil
	return nil
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
