package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	operationFileName = "operation.json"
	operationVersion  = 1

	operationStagePrepared  = "prepared"
	operationStageSwitching = "switching"
	operationStageSwitched  = "switched"
	operationStageMerged    = "merged"
)

type operationJournal struct {
	Version         int           `json:"version"`
	Stage           string        `json:"stage"`
	CurrentCommit   string        `json:"currentCommit"`
	CandidateCommit string        `json:"candidateCommit"`
	OldImages       runtimeImages `json:"-"`
	CandidateImages runtimeImages `json:"-"`
}

type persistedOperationJournal struct {
	Version              int    `json:"version"`
	Stage                string `json:"stage"`
	CurrentCommit        string `json:"currentCommit"`
	CandidateCommit      string `json:"candidateCommit"`
	OldAppImage          string `json:"oldAppImage"`
	OldWorkerImage       string `json:"oldWorkerImage"`
	CandidateAppImage    string `json:"candidateAppImage"`
	CandidateWorkerImage string `json:"candidateWorkerImage"`
}

func (j operationJournal) MarshalJSON() ([]byte, error) {
	return json.Marshal(persistedOperationJournal{
		Version:              j.Version,
		Stage:                j.Stage,
		CurrentCommit:        j.CurrentCommit,
		CandidateCommit:      j.CandidateCommit,
		OldAppImage:          j.OldImages.app,
		OldWorkerImage:       j.OldImages.worker,
		CandidateAppImage:    j.CandidateImages.app,
		CandidateWorkerImage: j.CandidateImages.worker,
	})
}

func (j *operationJournal) UnmarshalJSON(raw []byte) error {
	if j == nil {
		return errors.New("nil operation journal")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var persisted persistedOperationJournal
	if err := decoder.Decode(&persisted); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("invalid operation journal")
	}
	*j = operationJournal{
		Version:         persisted.Version,
		Stage:           persisted.Stage,
		CurrentCommit:   persisted.CurrentCommit,
		CandidateCommit: persisted.CandidateCommit,
		OldImages: runtimeImages{
			app:    persisted.OldAppImage,
			worker: persisted.OldWorkerImage,
		},
		CandidateImages: runtimeImages{
			app:    persisted.CandidateAppImage,
			worker: persisted.CandidateWorkerImage,
		},
	}
	return nil
}

type operationStore struct {
	directory     string
	syncDirectory func(string) error
}

func (s operationStore) load() (operationJournal, bool, error) {
	if s.directory == "" {
		return operationJournal{}, false, errors.New("operation state directory is unavailable")
	}
	path := filepath.Join(s.directory, operationFileName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return operationJournal{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return operationJournal{}, false, errors.New("operation journal is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return operationJournal{}, false, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 16*1024+1))
	if err != nil || len(raw) > 16*1024 {
		return operationJournal{}, false, errors.New("operation journal is invalid")
	}
	var journal operationJournal
	if err := json.Unmarshal(raw, &journal); err != nil || !validOperationJournal(journal) {
		return operationJournal{}, false, errors.New("operation journal is invalid")
	}
	return journal, true, nil
}

func (s operationStore) save(journal operationJournal) error {
	if s.directory == "" || !validOperationJournal(journal) {
		return errors.New("invalid operation journal")
	}
	if err := ensureStateDirectory(s.directory); err != nil {
		return err
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary := filepath.Join(s.directory, operationFileName+".tmp")
	target := filepath.Join(s.directory, operationFileName)
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(temporary)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(raw); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	cleanup = false
	return s.sync()
}

func (s operationStore) remove() error {
	if s.directory == "" {
		return errors.New("operation state directory is unavailable")
	}
	err := os.Remove(filepath.Join(s.directory, operationFileName))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.sync()
}

func (s operationStore) sync() error {
	if s.syncDirectory != nil {
		return s.syncDirectory(s.directory)
	}
	return syncStateDirectory(s.directory)
}

func syncStateDirectory(directoryPath string) error {
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func validOperationJournal(journal operationJournal) bool {
	if journal.Version != operationVersion || journal.CurrentCommit == journal.CandidateCommit ||
		!validCommit(journal.CurrentCommit) || !validCommit(journal.CandidateCommit) ||
		!validRuntimeImages(journal.OldImages) || !validRuntimeImages(journal.CandidateImages) {
		return false
	}
	switch journal.Stage {
	case operationStagePrepared, operationStageSwitching, operationStageSwitched, operationStageMerged:
		return true
	default:
		return false
	}
}

func validRuntimeImages(images runtimeImages) bool {
	return validImageID(images.app) && validImageID(images.worker)
}

func validImageID(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range strings.TrimPrefix(value, "sha256:") {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
