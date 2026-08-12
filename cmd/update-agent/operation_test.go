package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOperationStorePersistsRecoveryMaterialAtomically(t *testing.T) {
	directory := t.TempDir()
	store := operationStore{directory: directory}
	journal := operationJournal{
		Version:         1,
		Stage:           operationStagePrepared,
		CurrentCommit:   strings.Repeat("a", 40),
		CandidateCommit: strings.Repeat("b", 40),
		OldImages: runtimeImages{
			app:    "sha256:" + strings.Repeat("1", 64),
			worker: "sha256:" + strings.Repeat("2", 64),
		},
		CandidateImages: runtimeImages{
			app:    "sha256:" + strings.Repeat("3", 64),
			worker: "sha256:" + strings.Repeat("4", 64),
		},
	}

	if err := store.save(journal); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != operationFileName {
		t.Fatalf("state directory entries = %v", entries)
	}
	loaded, ok, err := store.load()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || !reflect.DeepEqual(loaded, journal) {
		t.Fatalf("loaded = %+v, ok=%v, want %+v", loaded, ok, journal)
	}
	if _, err := os.Stat(filepath.Join(directory, operationFileName+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary journal remained: %v", err)
	}
	if err := store.remove(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.load(); err != nil || ok {
		t.Fatalf("load after remove: ok=%v err=%v", ok, err)
	}
}

func TestSaveOperationKeepsAgentPendingAfterPostRenameFailure(t *testing.T) {
	directory := t.TempDir()
	injected := errors.New("injected directory fsync failure")
	store := operationStore{
		directory: directory,
		syncDirectory: func(string) error {
			return injected
		},
	}
	a := &agent{
		cfg:     config{ref: "master"},
		status:  initialStatus(config{ref: "master"}),
		journal: store,
	}
	operation := recoveryOperation(operationStagePrepared)

	if err := a.saveOperation(operation); !errors.Is(err, injected) {
		t.Fatalf("save error = %v, want injected post-rename error", err)
	}
	loaded, ok, err := (operationStore{directory: directory}).load()
	if err != nil || !ok || !reflect.DeepEqual(loaded, operation) {
		t.Fatalf("committed journal = %+v, ok=%v err=%v", loaded, ok, err)
	}
	if _, err := a.reserveOperation(stateChecking, phaseChecking, 5, "check", time.Now(), false); !errors.Is(err, errBusy) {
		t.Fatalf("reservation after uncertain save = %v, want busy", err)
	}
}

func TestStartupReconcileRestoresOldRuntimeBeforeCheckoutMerge(t *testing.T) {
	for _, test := range []struct {
		name    string
		stage   string
		runtime runtimeImages
	}{
		{
			name:    "pre-switch",
			stage:   operationStagePrepared,
			runtime: recoveryOldImages(),
		},
		{
			name:  "partial switch",
			stage: operationStageSwitching,
			runtime: runtimeImages{
				app:    recoveryCandidateImages().app,
				worker: recoveryOldImages().worker,
			},
		},
		{
			name:    "post-health pre-merge",
			stage:   operationStageSwitched,
			runtime: recoveryCandidateImages(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, test.stage, recoveryOldCommit())
			runtime := &fakeRecoveryRuntime{
				head:   recoveryOldCommit(),
				images: test.runtime,
			}

			agent, err := newAgentWithComponents(
				context.Background(),
				fixture.cfg,
				fixture.statuses,
				fixture.operations,
				nil,
				func(*agent) agentActions { return runtime.actions() },
			)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runtime.images, recoveryOldImages()) {
				t.Fatalf("runtime images = %+v, want old %+v", runtime.images, recoveryOldImages())
			}
			status := agent.snapshot()
			if status.State != stateFailed || status.Phase != phaseFailed ||
				status.CurrentCommit != recoveryOldCommit() || !status.UpdateAvailable ||
				status.FinishedAt == nil {
				t.Fatalf("recovered status = %+v", status)
			}
			if _, ok, err := fixture.operations.load(); err != nil || ok {
				t.Fatalf("journal after recovery: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestStartupReconcileUsesJournalWhenPublicStatusIsUnavailable(t *testing.T) {
	for _, test := range []struct {
		name          string
		invalidStatus bool
	}{
		{name: "missing status"},
		{name: "invalid status", invalidStatus: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			cfg := config{repository: filepath.Join(directory, "repository"), ref: "master", stateDirectory: directory}
			operations := operationStore{directory: directory}
			if err := operations.save(recoveryOperation(operationStageSwitching)); err != nil {
				t.Fatal(err)
			}
			if test.invalidStatus {
				if err := os.WriteFile(filepath.Join(directory, statusFileName), []byte("not-json\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runtime := &fakeRecoveryRuntime{
				head: recoveryOldCommit(),
				images: runtimeImages{
					app:    recoveryCandidateImages().app,
					worker: recoveryOldImages().worker,
				},
			}

			agent, err := newAgentWithComponents(
				context.Background(), cfg, statusStore{directory: directory}, operations, nil,
				func(*agent) agentActions { return runtime.actions() },
			)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(runtime.images, recoveryOldImages()) {
				t.Fatalf("runtime images = %+v, want old images", runtime.images)
			}
			status := agent.snapshot()
			if status.State != stateFailed || status.CurrentCommit != recoveryOldCommit() ||
				status.LatestCommit != recoveryCandidateCommit() || status.FinishedAt == nil {
				t.Fatalf("synthesized terminal status = %+v", status)
			}
			if _, ok, err := operations.load(); err != nil || ok {
				t.Fatalf("journal after recovery: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestStartupReconcileFinalizesCandidateAfterCheckoutMerge(t *testing.T) {
	fixture := newRecoveryFixture(t, operationStageMerged, recoveryCandidateCommit())
	runtime := &fakeRecoveryRuntime{
		head: recoveryCandidateCommit(),
		images: runtimeImages{
			app:    recoveryCandidateImages().app,
			worker: recoveryOldImages().worker,
		},
	}

	agent, err := newAgentWithComponents(
		context.Background(), fixture.cfg, fixture.statuses, fixture.operations, nil,
		func(*agent) agentActions { return runtime.actions() },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runtime.images, recoveryCandidateImages()) {
		t.Fatalf("runtime images = %+v, want candidate %+v", runtime.images, recoveryCandidateImages())
	}
	status := agent.snapshot()
	if status.State != stateSuccess || status.Phase != phaseComplete || status.Progress != 100 ||
		status.CurrentCommit != recoveryCandidateCommit() || status.CurrentVersion != "1.1.0" ||
		status.PreviousVersion != "1.0.0" || status.UpdateAvailable || status.FinishedAt == nil {
		t.Fatalf("finalized status = %+v", status)
	}
	if _, ok, err := fixture.operations.load(); err != nil || ok {
		t.Fatalf("journal after finalize: ok=%v err=%v", ok, err)
	}
}

func TestStartupReconcileKeepsJournalWhenFinalStatusCannotPersist(t *testing.T) {
	fixture := newRecoveryFixture(t, operationStageMerged, recoveryCandidateCommit())
	failing := &terminalFailingStatusStore{delegate: fixture.statuses, failSuccess: true}
	runtime := &fakeRecoveryRuntime{head: recoveryCandidateCommit(), images: recoveryCandidateImages()}

	if _, err := newAgentWithComponents(
		context.Background(), fixture.cfg, failing, fixture.operations, nil,
		func(*agent) agentActions { return runtime.actions() },
	); !errors.Is(err, errInjectedStatusSave) {
		t.Fatalf("startup error = %v, want injected save failure", err)
	}
	if _, ok, err := fixture.operations.load(); err != nil || !ok {
		t.Fatalf("journal after failed final save: ok=%v err=%v", ok, err)
	}
	persisted, ok := fixture.statuses.load()
	if !ok || persisted.State != stateUpdating || persisted.CurrentCommit != recoveryOldCommit() {
		t.Fatalf("persisted status after failed final save = %+v, ok=%v", persisted, ok)
	}

	failing.failSuccess = false
	agent, err := newAgentWithComponents(
		context.Background(), fixture.cfg, failing, fixture.operations, nil,
		func(*agent) agentActions { return runtime.actions() },
	)
	if err != nil {
		t.Fatal(err)
	}
	if status := agent.snapshot(); status.State != stateSuccess || status.CurrentCommit != recoveryCandidateCommit() {
		t.Fatalf("status after retry = %+v", status)
	}
	if _, ok, err := fixture.operations.load(); err != nil || ok {
		t.Fatalf("journal after successful retry: ok=%v err=%v", ok, err)
	}
}

func TestStartupReconcileIsIdempotentAfterTerminalSaveBeforeJournalRemoval(t *testing.T) {
	fixture := newRecoveryFixture(t, operationStageMerged, recoveryCandidateCommit())
	finished := time.Now().UTC()
	status := recoveryUpdatingStatus(fixture.cfg)
	status.State = stateSuccess
	status.CurrentVersion = "1.1.0"
	status.CurrentCommit = recoveryCandidateCommit()
	status.UpdateAvailable = false
	status.PreviousVersion = "1.0.0"
	status.Phase = phaseComplete
	status.Progress = 100
	status.Message = "completed before journal removal"
	status.FinishedAt = &finished
	if err := fixture.statuses.save(status); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRecoveryRuntime{head: recoveryCandidateCommit(), images: recoveryCandidateImages()}

	agent, err := newAgentWithComponents(
		context.Background(), fixture.cfg, fixture.statuses, fixture.operations, nil,
		func(*agent) agentActions { return runtime.actions() },
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := agent.snapshot(); got.PreviousVersion != "1.0.0" || got.CurrentVersion != "1.1.0" {
		t.Fatalf("idempotent finalized versions = current %q previous %q", got.CurrentVersion, got.PreviousVersion)
	}
	if _, ok, err := fixture.operations.load(); err != nil || ok {
		t.Fatalf("journal after idempotent finalize: ok=%v err=%v", ok, err)
	}
}

func TestStartupReconcileBlocksUnsafeCheckoutAndKeepsRecoveryEvidence(t *testing.T) {
	for _, test := range []struct {
		name  string
		head  string
		dirty bool
	}{
		{name: "unexpected head", head: strings.Repeat("c", 40)},
		{name: "dirty checkout", head: recoveryOldCommit(), dirty: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, operationStageSwitching, recoveryOldCommit())
			runtime := &fakeRecoveryRuntime{head: test.head, dirty: test.dirty, images: recoveryCandidateImages()}
			agent, err := newAgentWithComponents(
				context.Background(), fixture.cfg, fixture.statuses, fixture.operations, nil,
				func(*agent) agentActions { return runtime.actions() },
			)
			if err != nil {
				t.Fatal(err)
			}
			if status := agent.snapshot(); status.State != stateBlocked || status.UpdateAvailable ||
				status.Phase != phaseComplete || status.Progress != 100 {
				t.Fatalf("blocked status = %+v", status)
			}
			if _, ok, err := fixture.operations.load(); err != nil || !ok {
				t.Fatalf("journal for manual recovery: ok=%v err=%v", ok, err)
			}
			if !reflect.DeepEqual(runtime.images, recoveryCandidateImages()) {
				t.Fatalf("unsafe checkout changed runtime to %+v", runtime.images)
			}
		})
	}
}

func TestStartupReconcileKeepsJournalWhenRecoveryHealthFails(t *testing.T) {
	fixture := newRecoveryFixture(t, operationStageSwitching, recoveryOldCommit())
	healthFailure := errors.New("injected compose health failure")
	actions := agentActions{
		checkoutSnapshot: func(context.Context) (checkoutSnapshot, error) {
			return checkoutSnapshot{Commit: recoveryOldCommit(), BranchMatches: true}, nil
		},
		switchRuntime: func(context.Context, string, runtimeImages) error {
			return healthFailure
		},
	}

	if _, err := newAgentWithComponents(
		context.Background(), fixture.cfg, fixture.statuses, fixture.operations, nil,
		func(*agent) agentActions { return actions },
	); !errors.Is(err, healthFailure) {
		t.Fatalf("startup recovery error = %v, want health failure", err)
	}
	if _, ok, err := fixture.operations.load(); err != nil || !ok {
		t.Fatalf("journal after failed health recovery: ok=%v err=%v", ok, err)
	}
	if status, ok := fixture.statuses.load(); !ok || status.State != stateUpdating || status.CurrentCommit != recoveryOldCommit() {
		t.Fatalf("status after failed health recovery = %+v, ok=%v", status, ok)
	}
}

func TestDockerContainerImageReadsImmutableImageID(t *testing.T) {
	want := "sha256:" + strings.Repeat("d", 64)
	got, err := dockerContainerImageWithOutput(
		context.Background(),
		"container-id",
		func(command *exec.Cmd) (string, error) {
			wantArgs := []string{"docker", "inspect", "--format", "{{.Image}}", "container-id"}
			if !reflect.DeepEqual(command.Args, wantArgs) {
				t.Fatalf("docker inspect args = %q, want %q", command.Args, wantArgs)
			}
			return want, nil
		},
	)
	if err != nil || got != want {
		t.Fatalf("image ID = %q, err=%v, want %q", got, err, want)
	}
}

func TestRunUpdateCapturesOldImagesBeforeBuildAndPersistsSwitchJournal(t *testing.T) {
	directory := t.TempDir()
	cfg := config{
		repository:     filepath.Join(directory, "repository"),
		stateDirectory: directory,
		ref:            "master",
		project:        "happylearn-dev",
	}
	started := time.Now().UTC().Add(-time.Minute)
	status := recoveryUpdatingStatus(cfg)
	status.Phase = phaseChecking
	status.Progress = 1
	status.StartedAt = &started
	statuses := statusStore{directory: directory}
	if err := statuses.save(status); err != nil {
		t.Fatal(err)
	}
	operations := operationStore{directory: directory}
	buildStarted := false
	head := recoveryOldCommit()
	activeImages := recoveryOldImages()
	candidate := recoveryInspection(cfg)
	a := &agent{
		cfg: cfg, status: status, store: statuses, journal: operations,
	}
	a.actions = agentActions{
		inspect: func(context.Context) (inspection, error) { return candidate, nil },
		createWorktree: func(_ context.Context, commit string) (string, error) {
			if commit != recoveryCandidateCommit() {
				return "", errors.New("worktree did not use immutable candidate commit")
			}
			return filepath.Join(directory, "candidate"), nil
		},
		removeWorktree: func(string) {},
		verifyCandidateSource: func(_ context.Context, source, commit string) error {
			if source != filepath.Join(directory, "candidate") || commit != recoveryCandidateCommit() {
				return errors.New("candidate source commit was not verified")
			}
			return nil
		},
		verifyBuildInputs: func(string) error { return nil },
		captureRuntimeImages: func(context.Context) (runtimeImages, error) {
			if buildStarted {
				return runtimeImages{}, errors.New("old runtime captured after candidate build")
			}
			return activeImages, nil
		},
		buildImage: func(_ context.Context, _ string, dockerfile, _ string) (string, error) {
			buildStarted = true
			if dockerfile == "Dockerfile" {
				return recoveryCandidateImages().app, nil
			}
			return recoveryCandidateImages().worker, nil
		},
		verifyCheckout: func(_ context.Context, expected string) error {
			if head != expected {
				return errors.New("checkout changed")
			}
			return nil
		},
		checkoutSnapshot: func(context.Context) (checkoutSnapshot, error) {
			return checkoutSnapshot{Commit: head, BranchMatches: true}, nil
		},
		switchRuntime: func(_ context.Context, _ string, images runtimeImages) error {
			journal, ok, err := operations.load()
			if err != nil || !ok || journal.Stage != operationStageSwitching ||
				!reflect.DeepEqual(journal.OldImages, recoveryOldImages()) ||
				!reflect.DeepEqual(journal.CandidateImages, recoveryCandidateImages()) {
				return errors.New("switch began without durable recovery journal")
			}
			activeImages = images
			return nil
		},
		mergeCandidate: func(_ context.Context, commit string) error {
			if commit != recoveryCandidateCommit() {
				return errors.New("merge did not use immutable candidate commit")
			}
			head = recoveryCandidateCommit()
			return nil
		},
	}

	a.runUpdate()
	if !reflect.DeepEqual(activeImages, recoveryCandidateImages()) {
		t.Fatalf("active images = %+v", activeImages)
	}
	if _, ok, err := operations.load(); err != nil || ok {
		t.Fatalf("journal after committed success: ok=%v err=%v", ok, err)
	}
	if status := a.snapshot(); status.State != stateSuccess || status.CurrentCommit != recoveryCandidateCommit() {
		t.Fatalf("completed status = %+v", status)
	}
}

func TestRunUpdateBlocksUnpinnedBuildInputsBeforeCandidateBuild(t *testing.T) {
	directory := t.TempDir()
	cfg := config{
		repository:     filepath.Join(directory, "repository"),
		stateDirectory: directory,
		ref:            "master",
		project:        "happylearn-dev",
	}
	started := time.Now().UTC().Add(-time.Minute)
	status := recoveryUpdatingStatus(cfg)
	status.Phase = phaseChecking
	status.Progress = 1
	status.StartedAt = &started
	statuses := statusStore{directory: directory}
	if err := statuses.save(status); err != nil {
		t.Fatal(err)
	}
	operations := operationStore{directory: directory}
	buildCalled := false
	candidate := recoveryInspection(cfg)
	a := &agent{cfg: cfg, status: status, store: statuses, journal: operations}
	a.actions = agentActions{
		inspect:               func(context.Context) (inspection, error) { return candidate, nil },
		createWorktree:        func(context.Context, string) (string, error) { return filepath.Join(directory, "candidate"), nil },
		removeWorktree:        func(string) {},
		verifyCandidateSource: func(context.Context, string, string) error { return nil },
		verifyBuildInputs:     func(string) error { return errors.New("unpinned base") },
		captureRuntimeImages:  func(context.Context) (runtimeImages, error) { return recoveryOldImages(), nil },
		buildImage: func(context.Context, string, string, string) (string, error) {
			buildCalled = true
			return "", nil
		},
		verifyCheckout:   func(context.Context, string) error { return nil },
		switchRuntime:    func(context.Context, string, runtimeImages) error { return nil },
		mergeCandidate:   func(context.Context, string) error { return nil },
		checkoutSnapshot: func(context.Context) (checkoutSnapshot, error) { return checkoutSnapshot{}, nil },
	}

	a.runUpdate()
	if buildCalled {
		t.Fatal("candidate build ran before immutable Dockerfile gate")
	}
	if status := a.snapshot(); status.State != stateBlocked || status.UpdateAvailable || status.Phase != phaseComplete {
		t.Fatalf("blocked build-input status = %+v", status)
	}
	if _, ok, err := operations.load(); err != nil || ok {
		t.Fatalf("unexpected journal before build: ok=%v err=%v", ok, err)
	}
}

func TestRunUpdateKeepsMergedJournalWhenFinalStatusSaveFails(t *testing.T) {
	directory := t.TempDir()
	cfg := config{
		repository:     filepath.Join(directory, "repository"),
		stateDirectory: directory,
		ref:            "master",
		project:        "happylearn-dev",
	}
	started := time.Now().UTC().Add(-time.Minute)
	status := recoveryUpdatingStatus(cfg)
	status.Phase = phaseChecking
	status.Progress = 1
	status.StartedAt = &started
	statuses := statusStore{directory: directory}
	if err := statuses.save(status); err != nil {
		t.Fatal(err)
	}
	failing := &terminalFailingStatusStore{delegate: statuses, failSuccess: true}
	operations := operationStore{directory: directory}
	head := recoveryOldCommit()
	activeImages := recoveryOldImages()
	candidate := recoveryInspection(cfg)
	a := &agent{cfg: cfg, status: status, store: failing, journal: operations}
	a.actions = agentActions{
		inspect: func(context.Context) (inspection, error) { return candidate, nil },
		createWorktree: func(_ context.Context, commit string) (string, error) {
			if commit != recoveryCandidateCommit() {
				return "", errors.New("worktree did not use immutable candidate commit")
			}
			return filepath.Join(directory, "candidate"), nil
		},
		removeWorktree:        func(string) {},
		verifyCandidateSource: func(context.Context, string, string) error { return nil },
		verifyBuildInputs:     func(string) error { return nil },
		captureRuntimeImages:  func(context.Context) (runtimeImages, error) { return activeImages, nil },
		buildImage: func(_ context.Context, _ string, dockerfile, _ string) (string, error) {
			if dockerfile == "Dockerfile" {
				return recoveryCandidateImages().app, nil
			}
			return recoveryCandidateImages().worker, nil
		},
		verifyCheckout: func(_ context.Context, expected string) error {
			if head != expected {
				return errors.New("checkout changed")
			}
			return nil
		},
		checkoutSnapshot: func(context.Context) (checkoutSnapshot, error) {
			return checkoutSnapshot{Commit: head, BranchMatches: true}, nil
		},
		switchRuntime: func(_ context.Context, _ string, images runtimeImages) error {
			activeImages = images
			return nil
		},
		mergeCandidate: func(_ context.Context, commit string) error {
			if commit != recoveryCandidateCommit() {
				return errors.New("merge did not use immutable candidate commit")
			}
			head = recoveryCandidateCommit()
			return nil
		},
	}

	a.runUpdate()
	journal, ok, err := operations.load()
	if err != nil || !ok || journal.Stage != operationStageMerged {
		t.Fatalf("journal after final status failure = %+v, ok=%v err=%v", journal, ok, err)
	}
	if persisted, ok := statuses.load(); !ok || persisted.State != stateUpdating ||
		persisted.CurrentCommit != recoveryOldCommit() || persisted.Phase != phaseMerging {
		t.Fatalf("persisted status after final status failure = %+v, ok=%v", persisted, ok)
	}
	if inMemory := a.snapshot(); inMemory.State == stateFailed || inMemory.CurrentCommit != recoveryOldCommit() {
		t.Fatalf("in-memory status falsely reported old commit failure = %+v", inMemory)
	}
	if head != recoveryCandidateCommit() || !reflect.DeepEqual(activeImages, recoveryCandidateImages()) {
		t.Fatalf("merged runtime lost: head=%s images=%+v", head, activeImages)
	}

	failing.failSuccess = false
	runtime := &fakeRecoveryRuntime{head: head, images: activeImages}
	restarted, err := newAgentWithComponents(
		context.Background(), cfg, failing, operations, nil,
		func(*agent) agentActions { return runtime.actions() },
	)
	if err != nil {
		t.Fatal(err)
	}
	if final := restarted.snapshot(); final.State != stateSuccess || final.CurrentCommit != recoveryCandidateCommit() {
		t.Fatalf("reconciled status = %+v", final)
	}
	if _, ok, err := operations.load(); err != nil || ok {
		t.Fatalf("journal after reconciled final status: ok=%v err=%v", ok, err)
	}
}

func TestRunUpdateHandlesJournalStageSaveFailuresWithoutLeavingPartialRuntime(t *testing.T) {
	for _, test := range []struct {
		stage        string
		wantSwitches int
	}{
		{stage: operationStageSwitching, wantSwitches: 0},
		{stage: operationStageSwitched, wantSwitches: 2},
	} {
		t.Run(test.stage, func(t *testing.T) {
			directory := t.TempDir()
			cfg := config{
				repository:     filepath.Join(directory, "repository"),
				stateDirectory: directory,
				ref:            "master",
				project:        "happylearn-dev",
			}
			started := time.Now().UTC().Add(-time.Minute)
			status := recoveryUpdatingStatus(cfg)
			status.Phase = phaseChecking
			status.Progress = 1
			status.StartedAt = &started
			statuses := statusStore{directory: directory}
			if err := statuses.save(status); err != nil {
				t.Fatal(err)
			}
			operations := &stageFailingOperationStore{
				delegate:  operationStore{directory: directory},
				failStage: test.stage,
			}
			head := recoveryOldCommit()
			activeImages := recoveryOldImages()
			switches := 0
			candidate := recoveryInspection(cfg)
			a := &agent{cfg: cfg, status: status, store: statuses, journal: operations}
			a.actions = agentActions{
				inspect:               func(context.Context) (inspection, error) { return candidate, nil },
				createWorktree:        func(context.Context, string) (string, error) { return filepath.Join(directory, "candidate"), nil },
				removeWorktree:        func(string) {},
				verifyCandidateSource: func(context.Context, string, string) error { return nil },
				verifyBuildInputs:     func(string) error { return nil },
				captureRuntimeImages:  func(context.Context) (runtimeImages, error) { return activeImages, nil },
				buildImage: func(_ context.Context, _ string, dockerfile, _ string) (string, error) {
					if dockerfile == "Dockerfile" {
						return recoveryCandidateImages().app, nil
					}
					return recoveryCandidateImages().worker, nil
				},
				verifyCheckout: func(_ context.Context, expected string) error {
					if head != expected {
						return errors.New("checkout changed")
					}
					return nil
				},
				switchRuntime: func(_ context.Context, _ string, images runtimeImages) error {
					switches++
					activeImages = images
					return nil
				},
				mergeCandidate: func(context.Context, string) error {
					head = recoveryCandidateCommit()
					return nil
				},
			}

			a.runUpdate()
			if switches != test.wantSwitches || !reflect.DeepEqual(activeImages, recoveryOldImages()) {
				t.Fatalf("switches=%d images=%+v, want switches=%d old images", switches, activeImages, test.wantSwitches)
			}
			if status := a.snapshot(); status.State != stateFailed || status.CurrentCommit != recoveryOldCommit() {
				t.Fatalf("status after stage save failure = %+v", status)
			}
			if _, ok, err := operations.load(); err != nil || ok {
				t.Fatalf("journal after safe terminal recovery: ok=%v err=%v", ok, err)
			}
		})
	}
}

type recoveryFixture struct {
	cfg        config
	statuses   statusStore
	operations operationStore
}

func newRecoveryFixture(t *testing.T, stage, _ string) recoveryFixture {
	t.Helper()
	directory := t.TempDir()
	cfg := config{repository: filepath.Join(directory, "repository"), ref: "master", stateDirectory: directory}
	status := recoveryUpdatingStatus(cfg)
	statuses := statusStore{directory: directory}
	if err := statuses.save(status); err != nil {
		t.Fatal(err)
	}
	operations := operationStore{directory: directory}
	if err := operations.save(operationJournal{
		Version:         operationVersion,
		Stage:           stage,
		CurrentCommit:   recoveryOldCommit(),
		CandidateCommit: recoveryCandidateCommit(),
		OldImages:       recoveryOldImages(),
		CandidateImages: recoveryCandidateImages(),
	}); err != nil {
		t.Fatal(err)
	}
	return recoveryFixture{cfg: cfg, statuses: statuses, operations: operations}
}

func recoveryUpdatingStatus(cfg config) updateStatus {
	started := time.Now().UTC().Add(-time.Hour)
	published := started.Add(-24 * time.Hour)
	status := initialStatus(cfg)
	status.State = stateUpdating
	status.Repository = "https://github.com/example/project.git"
	status.CurrentVersion = "1.0.0"
	status.LatestVersion = "1.1.0"
	status.CurrentCommit = recoveryOldCommit()
	status.LatestCommit = recoveryCandidateCommit()
	status.ReleaseName = "v1.1.0"
	status.ReleaseURL = "https://github.com/example/project/releases/tag/v1.1.0"
	status.PublishedAt = &published
	status.UpdateAvailable = true
	status.Phase = phaseMerging
	status.Progress = 92
	status.Message = "updating"
	status.StartedAt = &started
	return status
}

func recoveryInspection(cfg config) inspection {
	published := time.Now().UTC().Add(-24 * time.Hour)
	status := recoveryUpdatingStatus(cfg)
	status.State = stateAvailable
	status.Phase = phaseComplete
	status.Progress = 100
	status.StartedAt = nil
	status.FinishedAt = nil
	return inspection{
		status: status,
		release: stableRelease{
			Tag: "v1.1.0", Version: "1.1.0", Name: "v1.1.0",
			URL:         "https://github.com/example/project/releases/tag/v1.1.0",
			PublishedAt: published,
		},
		candidateRef: isolatedReleaseRef("v1.1.0"),
	}
}

func recoveryOldCommit() string       { return strings.Repeat("a", 40) }
func recoveryCandidateCommit() string { return strings.Repeat("b", 40) }

func recoveryOldImages() runtimeImages {
	return runtimeImages{
		app:    "sha256:" + strings.Repeat("1", 64),
		worker: "sha256:" + strings.Repeat("2", 64),
	}
}

func recoveryCandidateImages() runtimeImages {
	return runtimeImages{
		app:    "sha256:" + strings.Repeat("3", 64),
		worker: "sha256:" + strings.Repeat("4", 64),
	}
}

func recoveryOperation(stage string) operationJournal {
	return operationJournal{
		Version:         operationVersion,
		Stage:           stage,
		CurrentCommit:   recoveryOldCommit(),
		CandidateCommit: recoveryCandidateCommit(),
		OldImages:       recoveryOldImages(),
		CandidateImages: recoveryCandidateImages(),
	}
}

type fakeRecoveryRuntime struct {
	head   string
	dirty  bool
	images runtimeImages
}

func (r *fakeRecoveryRuntime) actions() agentActions {
	return agentActions{
		checkoutSnapshot: func(context.Context) (checkoutSnapshot, error) {
			return checkoutSnapshot{Commit: r.head, Dirty: r.dirty, BranchMatches: true}, nil
		},
		switchRuntime: func(_ context.Context, _ string, images runtimeImages) error {
			r.images = images
			return nil
		},
	}
}

var errInjectedStatusSave = errors.New("injected terminal status save failure")

type terminalFailingStatusStore struct {
	delegate    statusStore
	failSuccess bool
}

var errInjectedOperationSave = errors.New("injected operation journal save failure")

type stageFailingOperationStore struct {
	delegate  operationStore
	failStage string
}

func (s *stageFailingOperationStore) load() (operationJournal, bool, error) {
	return s.delegate.load()
}

func (s *stageFailingOperationStore) save(operation operationJournal) error {
	if err := s.delegate.save(operation); err != nil {
		return err
	}
	if operation.Stage == s.failStage {
		return errInjectedOperationSave
	}
	return nil
}

func (s *stageFailingOperationStore) remove() error {
	return s.delegate.remove()
}

func (s *terminalFailingStatusStore) load() (updateStatus, bool) {
	return s.delegate.load()
}

func (s *terminalFailingStatusStore) save(status updateStatus) error {
	if s.failSuccess && status.State == stateSuccess {
		return errInjectedStatusSave
	}
	return s.delegate.save(status)
}
