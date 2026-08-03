package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
)

// ReleaseStates is the ordered successful release protocol.
var ReleaseStates = [...]string{
	"preflight", "backup_started", "backup_verified", "release_mode",
	"maintenance", "drained", "images_pulled", "schema_compatible",
	"migrated", "services_started", "ready", "smoke_passed", "activated",
	"normal", "traffic_open", "succeeded",
}

var tracePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,127}$`)

// State is the durable, secret-free release journal record.
type State struct {
	ReleaseID        string    `json:"releaseId"`
	ManifestSHA256   string    `json:"manifestSha256"`
	PreviousSHA256   string    `json:"previousManifestSha256,omitempty"`
	State            string    `json:"state"`
	Attempt          int64     `json:"attempt"`
	StartedAt        time.Time `json:"startedAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	BackupEvidenceID string    `json:"backupEvidenceId,omitempty"`
	TraceID          string    `json:"traceId"`
	Result           string    `json:"result"`
}

func (s State) Validate() error {
	if !safeIdentifierPattern.MatchString(s.ReleaseID) {
		return invalid("releaseId", "must be a safe non-empty identifier")
	}
	if !hashPattern.MatchString(s.ManifestSHA256) {
		return invalid("manifestSha256", "must be a lowercase SHA-256")
	}
	if s.PreviousSHA256 != "" && !hashPattern.MatchString(s.PreviousSHA256) {
		return invalid("previousManifestSha256", "must be a lowercase SHA-256")
	}
	if !knownReleaseState(s.State) && s.State != "failed_safe" {
		return invalid("state", "must be a known release state")
	}
	if s.Attempt < 1 {
		return invalid("attempt", "must be positive")
	}
	if s.StartedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.StartedAt) {
		return invalid("timestamps", "must be non-zero and ordered")
	}
	if s.BackupEvidenceID != "" && !safeIdentifierPattern.MatchString(s.BackupEvidenceID) {
		return invalid("backupEvidenceId", "must be a safe identifier")
	}
	if !tracePattern.MatchString(s.TraceID) {
		return invalid("traceId", "must be a safe identifier")
	}
	if s.Result != "pending" && s.Result != "succeeded" && s.Result != "failed" && s.Result != "rolled_back" {
		return invalid("result", "must be a supported result")
	}
	return nil
}

func ParseState(data []byte) (State, error) {
	var state State
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return State{}, errors.New("release state: invalid JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return State{}, errors.New("release state: expected one JSON object")
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("release state: %w", err)
	}
	return state, nil
}

func (s State) CanonicalJSON() ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

func knownReleaseState(value string) bool {
	for _, state := range ReleaseStates {
		if value == state {
			return true
		}
	}
	return false
}
