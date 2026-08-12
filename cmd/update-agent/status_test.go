package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStatusJSONContainsExactContract(t *testing.T) {
	status := initialStatus(config{ref: "master"})
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"enabled", "state", "strategy", "repository", "ref", "channel",
		"currentVersion", "latestVersion", "currentCommit", "latestCommit",
		"releaseName", "releaseNotes", "releaseURL", "publishedAt",
		"updateAvailable", "dirty", "canRollback", "previousVersion",
		"phase", "progress", "message", "startedAt", "finishedAt",
	}
	got := make([]string, 0, len(fields))
	for key := range fields {
		got = append(got, key)
	}
	if len(got) != len(want) {
		t.Fatalf("status keys = %v, want %v", got, want)
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing status field %q", key)
		}
	}
}

func TestReserveOperationIsAtomic(t *testing.T) {
	directory := t.TempDir()
	a := &agent{
		cfg:    config{ref: "master", stateDirectory: directory},
		status: initialStatus(config{ref: "master"}),
		store:  statusStore{directory: directory},
	}
	if err := a.store.save(a.status); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsByCall := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := a.reserveOperation(stateUpdating, phaseChecking, 1, "reserve", time.Now(), false)
			errorsByCall <- err
		}()
	}
	close(start)
	var successes, busy int
	for range 2 {
		err := <-errorsByCall
		switch {
		case err == nil:
			successes++
		case errors.Is(err, errBusy):
			busy++
		default:
			t.Fatalf("unexpected apply error: %v", err)
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("successes=%d busy=%d", successes, busy)
	}
}

func TestTransitionRestoresMemoryWhenPersistenceFails(t *testing.T) {
	directory := t.TempDir()
	status := initialStatus(config{ref: "master"})
	a := &agent{
		cfg:    config{ref: "master"},
		status: status,
		store:  statusStore{directory: filepath.Join(directory, "missing", "state")},
	}
	if err := os.WriteFile(filepath.Join(directory, "missing"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := a.transition(func(current *updateStatus) {
		current.State = stateUpdating
	})
	if err == nil {
		t.Fatal("transition unexpectedly succeeded")
	}
	if !reflect.DeepEqual(a.snapshot(), status) {
		t.Fatalf("status changed after failed persistence: %+v", a.snapshot())
	}
}

func TestStatusStorePersistsAtomicallyAndRecoversInterruptedState(t *testing.T) {
	directory := t.TempDir()
	store := statusStore{directory: directory}
	now := time.Now().UTC().Truncate(time.Second)
	status := initialStatus(config{ref: "master"})
	status.State = stateUpdating
	status.Phase = phaseBuilding
	status.Progress = 45
	status.StartedAt = &now
	if err := store.save(status); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(directory); err != nil {
		t.Fatal(err)
	} else if len(entries) != 1 || entries[0].Name() != statusFileName {
		t.Fatalf("state directory entries = %v", entries)
	}
	loaded, ok := store.load()
	if !ok {
		t.Fatal("persisted status was not loaded")
	}
	if !reflect.DeepEqual(loaded, status) {
		t.Fatalf("loaded = %+v, want %+v", loaded, status)
	}

	recovered := recoverPersistedStatus(loaded, config{ref: "master"}, now.Add(time.Minute))
	if recovered.State != stateFailed || recovered.Phase != phaseFailed || recovered.FinishedAt == nil {
		t.Fatalf("recovered status = %+v", recovered)
	}
	if recovered.CanRollback {
		t.Fatal("explicit rollback must remain disabled")
	}
	if _, err := os.Stat(filepath.Join(directory, statusFileName+".tmp")); !os.IsNotExist(err) {
		t.Fatalf("temporary status file remained: %v", err)
	}
}
