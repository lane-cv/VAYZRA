package release

import (
	"strings"
	"testing"
	"time"
)

func TestStateAcceptsDurableSafeRecord(t *testing.T) {
	now := time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC)
	state := State{
		ReleaseID: "release-20260801", ManifestSHA256: strings.Repeat("a", 64),
		PreviousSHA256: strings.Repeat("b", 64), State: "backup_verified", Attempt: 1,
		StartedAt: now, UpdatedAt: now.Add(time.Second), BackupEvidenceID: "backup-001",
		TraceID: "trace_12345678", Result: "pending",
	}
	data, err := state.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseState(data); err != nil {
		t.Fatal(err)
	}
}

func TestStateRejectsUnsafeOrUnknownData(t *testing.T) {
	now := time.Now().UTC()
	state := State{ReleaseID: "release-1", ManifestSHA256: strings.Repeat("a", 64), State: "unknown", Attempt: 1, StartedAt: now, UpdatedAt: now, TraceID: "trace_12345678", Result: "pending"}
	if err := state.Validate(); err == nil {
		t.Fatal("expected unknown state rejection")
	}
	state.State = "preflight"
	data, _ := state.CanonicalJSON()
	data = []byte(strings.Replace(string(data), `"state":`, `"environment":"secret","state":`, 1))
	if _, err := ParseState(data); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}
