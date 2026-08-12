package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	statusFileName = "status.json"

	stateUnknown   = "unknown"
	stateChecking  = "checking"
	stateCurrent   = "current"
	stateAvailable = "available"
	stateUpdating  = "updating"
	stateSuccess   = "success"
	stateFailed    = "failed"
	stateBlocked   = "blocked"

	strategyGitHubRelease = "github-release"
	channelStable         = "stable"

	phaseIdle       = "idle"
	phaseChecking   = "checking"
	phaseFetching   = "fetching"
	phasePreparing  = "preparing"
	phaseBuilding   = "building"
	phaseSwitching  = "switching"
	phaseVerifying  = "verifying"
	phaseMerging    = "merging"
	phaseRecovering = "recovering"
	phaseComplete   = "complete"
	phaseFailed     = "failed"
)

type updateStatus struct {
	Enabled         bool       `json:"enabled"`
	State           string     `json:"state"`
	Strategy        string     `json:"strategy"`
	Repository      string     `json:"repository"`
	Ref             string     `json:"ref"`
	Channel         string     `json:"channel"`
	CurrentVersion  string     `json:"currentVersion"`
	LatestVersion   string     `json:"latestVersion"`
	CurrentCommit   string     `json:"currentCommit"`
	LatestCommit    string     `json:"latestCommit"`
	ReleaseName     string     `json:"releaseName"`
	ReleaseNotes    string     `json:"releaseNotes"`
	ReleaseURL      string     `json:"releaseURL"`
	PublishedAt     *time.Time `json:"publishedAt"`
	UpdateAvailable bool       `json:"updateAvailable"`
	Dirty           bool       `json:"dirty"`
	CanRollback     bool       `json:"canRollback"`
	PreviousVersion string     `json:"previousVersion"`
	Phase           string     `json:"phase"`
	Progress        int        `json:"progress"`
	Message         string     `json:"message"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
}

type statusStore struct {
	directory string
}

func initialStatus(cfg config) updateStatus {
	return updateStatus{
		Enabled:     true,
		State:       stateUnknown,
		Strategy:    strategyGitHubRelease,
		Ref:         cfg.ref,
		Channel:     channelStable,
		CanRollback: false,
		Phase:       phaseIdle,
		Progress:    0,
		Message:     "尚未检查 GitHub Release 更新",
	}
}

func recoverPersistedStatus(status updateStatus, cfg config, now time.Time) updateStatus {
	if !validPersistedStatus(status) {
		return initialStatus(cfg)
	}
	status.Enabled = true
	status.Strategy = strategyGitHubRelease
	status.Channel = channelStable
	status.Ref = cfg.ref
	status.CanRollback = false
	if status.State == stateChecking || status.State == stateUpdating {
		finished := now.UTC()
		status.State = stateFailed
		status.Phase = phaseFailed
		status.Message = "更新代理重启中断了上一次操作，请重新检查后再试"
		status.FinishedAt = &finished
	}
	return status
}

func (s statusStore) load() (updateStatus, bool) {
	if s.directory == "" {
		return updateStatus{}, false
	}
	file, err := os.Open(filepath.Join(s.directory, statusFileName))
	if err != nil {
		return updateStatus{}, false
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 128*1024+1))
	if err != nil || len(raw) > 128*1024 {
		return updateStatus{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var status updateStatus
	if err := decoder.Decode(&status); err != nil || !validPersistedStatus(status) {
		return updateStatus{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return updateStatus{}, false
	}
	return status, true
}

func (s statusStore) save(status updateStatus) error {
	if s.directory == "" || !validPersistedStatus(status) {
		return errors.New("invalid update status")
	}
	if err := ensureStateDirectory(s.directory); err != nil {
		return err
	}
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	temporary := filepath.Join(s.directory, statusFileName+".tmp")
	target := filepath.Join(s.directory, statusFileName)
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
	if err := os.Chmod(target, 0o600); err != nil {
		return err
	}
	directory, err := os.Open(s.directory)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func validPersistedStatus(status updateStatus) bool {
	if !status.Enabled || status.Strategy != strategyGitHubRelease || status.Channel != channelStable {
		return false
	}
	switch status.State {
	case stateUnknown, stateChecking, stateCurrent, stateAvailable, stateUpdating,
		stateSuccess, stateFailed, stateBlocked:
	default:
		return false
	}
	switch status.Phase {
	case phaseIdle, phaseChecking, phaseFetching, phasePreparing, phaseBuilding,
		phaseSwitching, phaseVerifying, phaseMerging, phaseRecovering,
		phaseComplete, phaseFailed:
	default:
		return false
	}
	if status.Progress < 0 || status.Progress > 100 || status.CanRollback {
		return false
	}
	if status.CurrentCommit != "" && !validCommit(status.CurrentCommit) {
		return false
	}
	if status.LatestCommit != "" && !validCommit(status.LatestCommit) {
		return false
	}
	if !boundedText(status.Repository, 512, false) ||
		!boundedText(status.Ref, 128, false) ||
		!boundedText(status.CurrentVersion, 128, false) ||
		!boundedText(status.LatestVersion, 128, false) ||
		!boundedText(status.ReleaseName, 256, false) ||
		!boundedText(status.ReleaseNotes, 32*1024, true) ||
		!boundedText(status.ReleaseURL, 2048, false) ||
		!boundedText(status.PreviousVersion, 128, false) ||
		!boundedText(status.Message, 512, false) {
		return false
	}
	if status.Repository != "" {
		repository, err := parseGitHubRepository(status.Repository)
		if err != nil || repository.canonicalURL() != status.Repository {
			return false
		}
	}
	for _, version := range []string{status.CurrentVersion, status.LatestVersion, status.PreviousVersion} {
		if version != "" {
			parsed, ok := parseNormalizedSemanticVersion(version)
			if !ok || parsed.normalized != version {
				return false
			}
		}
	}
	if status.ReleaseURL == "" {
		if status.PublishedAt != nil {
			return false
		}
	} else if status.PublishedAt == nil || !validStatusReleaseURL(status.ReleaseURL, status.Repository, status.LatestVersion) {
		return false
	}
	if status.FinishedAt != nil && status.StartedAt != nil && status.FinishedAt.Before(*status.StartedAt) {
		return false
	}
	switch status.State {
	case stateAvailable:
		if !status.UpdateAvailable {
			return false
		}
	case stateCurrent, stateSuccess, stateBlocked, stateUnknown:
		if status.UpdateAvailable {
			return false
		}
	}
	return validPersistedStateProgress(status)
}

func validPersistedStateProgress(status updateStatus) bool {
	switch status.State {
	case stateChecking:
		return status.Phase == phaseChecking && status.Progress < 100 && status.FinishedAt == nil
	case stateUpdating:
		return status.Phase != phaseIdle && status.Phase != phaseComplete && status.Phase != phaseFailed &&
			status.Progress < 100 && status.StartedAt != nil && status.FinishedAt == nil
	case stateCurrent, stateAvailable, stateBlocked, stateSuccess:
		return status.Phase == phaseComplete && status.Progress == 100
	case stateFailed:
		return status.Phase == phaseFailed && status.FinishedAt != nil
	case stateUnknown:
		return status.Phase == phaseIdle && status.Progress == 0
	default:
		return false
	}
}

func validStatusReleaseURL(raw, repository, latestVersion string) bool {
	parsedRepository, err := parseGitHubRepository(repository)
	if err != nil || parsedRepository.canonicalURL() != repository {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return false
	}
	prefix := "/" + parsedRepository.Owner + "/" + parsedRepository.Name + "/releases/tag/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return false
	}
	tag := strings.TrimPrefix(parsed.Path, prefix)
	version, ok := parseStableSemanticVersion(tag)
	return ok && version.normalized == latestVersion
}

func boundedText(value string, maximum int, multiline bool) bool {
	return len(value) <= maximum && utf8.ValidString(value) && !hasControl(value, multiline)
}

func validCommit(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
