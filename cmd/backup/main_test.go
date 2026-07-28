package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
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
	service.evidence = cloneEvidence(input.Evidence)
	service.owner = uuid.Nil
	service.leaseActive = false
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
	snapshotCalls  int
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
	executor.snapshotCalls++
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
	backup.SyncInput,
) (string, error) {
	return executor.syncResult, executor.syncErr
}

type memoryWorkflowStates struct {
	state        *workflowState
	saveCalls    int
	failSaveAt   int
	failSave     func(workflowState, int) bool
	deleteCalls  int
	failDeleteAt int
}

func (states *memoryWorkflowStates) Load(uuid.UUID) (workflowState, error) {
	if states.state == nil {
		return workflowState{}, errWorkflowState
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
