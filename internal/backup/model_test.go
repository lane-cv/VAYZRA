package backup

import (
	"testing"
)

func TestStateTransitionContract(t *testing.T) {
	allowed := map[State][]State{
		StateQueued:       {StateDraining, StateFailed},
		StateDraining:     {StateSnapshotting, StateFailed},
		StateSnapshotting: {StateEncrypting, StateFailed},
		StateEncrypting:   {StateVerifying, StateFailed},
		StateVerifying:    {StateSyncing, StateSucceeded, StateFailed},
		StateSyncing:      {StateSucceeded, StateDegraded, StateFailed},
	}
	all := []State{
		StateQueued, StateDraining, StateSnapshotting, StateEncrypting,
		StateVerifying, StateSyncing, StateSucceeded, StateDegraded, StateFailed,
	}
	for _, from := range all {
		for _, to := range all {
			want := false
			for _, candidate := range allowed[from] {
				if candidate == to {
					want = true
					break
				}
			}
			if got := ValidTransition(from, to); got != want {
				t.Errorf("ValidTransition(%q,%q)=%t want=%t", from, to, got, want)
			}
		}
	}
}

func TestBackupStateAndTriggerValuesAreExact(t *testing.T) {
	states := []State{
		StateQueued, StateDraining, StateSnapshotting, StateEncrypting,
		StateVerifying, StateSyncing, StateSucceeded, StateDegraded, StateFailed,
	}
	wantStates := []string{
		"queued", "draining", "snapshotting", "encrypting",
		"verifying", "syncing", "succeeded", "degraded", "failed",
	}
	for i := range states {
		if string(states[i]) != wantStates[i] {
			t.Fatalf("state[%d]=%q want=%q", i, states[i], wantStates[i])
		}
	}
	triggers := []Trigger{TriggerScheduled, TriggerManual, TriggerPreRelease}
	wantTriggers := []string{"scheduled", "manual", "pre_release"}
	for i := range triggers {
		if string(triggers[i]) != wantTriggers[i] {
			t.Fatalf("trigger[%d]=%q want=%q", i, triggers[i], wantTriggers[i])
		}
	}
}

func TestBackupErrorCategoriesAreFiniteAndCannotCarryArbitraryLabels(t *testing.T) {
	for _, category := range []string{
		"drain_timeout", "database_dump", "object_store_stop", "snapshot",
		"object_store_restart", "integrity", "remote_sync", "remote_unavailable",
		"retention", "lease_lost", "cancelled", "internal",
		"repository_integrity", "restore_database", "restore_object_store",
		"session_revocation", "readiness", "reference_check",
		"authorization_check", "timeout",
	} {
		if !validSafeError(category, "") {
			t.Errorf("safe category rejected: %q", category)
		}
	}
	for _, category := range []string{
		"student_name", "repository_path", "secret", "/private/repository",
		"remote failure output",
	} {
		if validSafeError(category, "") {
			t.Errorf("arbitrary category accepted: %q", category)
		}
		if safeText(category) != "" {
			t.Errorf("arbitrary category exposed: %q", category)
		}
	}
}
