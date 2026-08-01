//go:build unix

package backup

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"
)

func retentionSnapshotID(value byte) string {
	return fmt.Sprintf("%064x", value)
}

func TestDecodeRepositorySnapshotsAcceptsRandomPathsAndHostsButRejectsUnsafeInventory(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	first := retentionSnapshotID(1)
	second := retentionSnapshotID(2)
	decoded, err := decodeResticRepositorySnapshots([]byte(fmt.Sprintf(`[
	  {
	    "time": %q,
	    "parent": null,
	    "tree": %q,
	    "paths": ["/work/.backup-snapshot-random-a"],
	    "hostname": "backup-host-a",
	    "username": "10003",
	    "uid": 10003,
	    "gid": 0,
	    "tags": ["happylearn-batch:11111111-1111-4111-8111-111111111111"],
	    "id": %q,
	    "short_id": "00000001"
	  },
	  {
	    "time": %q,
	    "paths": ["/work/.backup-snapshot-random-b"],
	    "hostname": "backup-host-b",
	    "tags": ["failed-attempt"],
	    "id": %q,
	    "short_id": "00000002"
	  }
	]`, now.Add(-time.Hour).Format(time.RFC3339Nano), retentionSnapshotID(9),
		first, now.Add(-25*time.Hour).Format(time.RFC3339Nano), second)))
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 ||
		decoded[0].ID != first ||
		decoded[1].ID != second ||
		decoded[0].BatchRunID != "11111111-1111-4111-8111-111111111111" ||
		decoded[1].BatchRunID != "" ||
		!decoded[1].CreatedAt.Equal(now.Add(-25*time.Hour)) {
		t.Fatalf("decoded=%+v", decoded)
	}

	for name, raw := range map[string][]byte{
		"duplicate": []byte(fmt.Sprintf(
			`[{"time":%q,"id":%q},{"time":%q,"id":%q}]`,
			now.Format(time.RFC3339Nano), first,
			now.Format(time.RFC3339Nano), first,
		)),
		"short-id": []byte(fmt.Sprintf(
			`[{"time":%q,"id":"abcd"}]`,
			now.Format(time.RFC3339Nano),
		)),
		"future": []byte(fmt.Sprintf(
			`[{"time":%q,"id":%q}]`,
			now.Add(time.Minute).Format(time.RFC3339Nano), first,
		)),
		"trailing": []byte(fmt.Sprintf(
			`[{"time":%q,"id":%q}] []`,
			now.Format(time.RFC3339Nano), first,
		)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeResticRepositorySnapshotsAt(raw, now); err == nil {
				t.Fatal("unsafe repository inventory accepted")
			}
		})
	}
}

func TestDecodeRepositorySnapshotsRequiresOneCanonicalBatchOwnershipTag(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	snapshotID := retentionSnapshotID(1)
	for name, tags := range map[string]string{
		"missing":       `[]`,
		"invalid":       `["happylearn-batch:not-a-uuid"]`,
		"noncanonical":  `["happylearn-batch:11111111-1111-4111-8111-11111111111A"]`,
		"duplicate":     `["happylearn-batch:11111111-1111-4111-8111-111111111111","happylearn-batch:11111111-1111-4111-8111-111111111111"]`,
		"mixed-invalid": `["happylearn-batch:11111111-1111-4111-8111-111111111111","happylearn-batch:bad"]`,
	} {
		t.Run(name, func(t *testing.T) {
			decoded, err := decodeResticRepositorySnapshotsAt(
				[]byte(fmt.Sprintf(
					`[{"time":%q,"tags":%s,"id":%q}]`,
					now.Add(-25*time.Hour).Format(time.RFC3339Nano),
					tags,
					snapshotID,
				)),
				now,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(decoded) != 1 || decoded[0].BatchRunID != "" {
				t.Fatalf("unsafe ownership accepted: %+v", decoded)
			}
		})
	}
}

func TestPlanRetentionDeletesOnlyDomainCandidatesAndOldOrphans(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	current := retentionSnapshotID(1)
	lastGood := retentionSnapshotID(2)
	evictedA := retentionSnapshotID(3)
	evictedB := retentionSnapshotID(4)
	recentFailure := retentionSnapshotID(5)
	oldFailure := retentionSnapshotID(6)
	preRelease := retentionSnapshotID(7)
	externalOld := retentionSnapshotID(8)
	maliciousOwnerOld := retentionSnapshotID(9)
	snapshots := []RepositorySnapshot{
		{
			ID: oldFailure, CreatedAt: now.Add(-24*time.Hour - time.Nanosecond),
			BatchRunID: "11111111-1111-4111-8111-111111111111",
		},
		{ID: externalOld, CreatedAt: now.Add(-48 * time.Hour)},
		{
			ID: maliciousOwnerOld, CreatedAt: now.Add(-48 * time.Hour),
			BatchRunID: "not-a-canonical-uuid",
		},
		{ID: recentFailure, CreatedAt: now.Add(-24 * time.Hour)},
		{ID: evictedB, CreatedAt: now.Add(-60 * 24 * time.Hour)},
		{ID: current, CreatedAt: now.Add(-time.Minute)},
		{ID: preRelease, CreatedAt: now.Add(-20 * 24 * time.Hour)},
		{ID: lastGood, CreatedAt: now.Add(-24 * time.Hour)},
		{ID: evictedA, CreatedAt: now.Add(-40 * 24 * time.Hour)},
	}
	got, err := PlanRetentionDeletes(RetentionRepositoryState{
		CandidateSnapshotIDs: []string{evictedB, evictedA, evictedA},
		CommittedSnapshotIDs: []string{
			current, lastGood, evictedA, evictedB, preRelease,
		},
		CurrentSnapshotID:  current,
		LastGoodSnapshotID: lastGood,
	}, snapshots, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{evictedA, evictedB, oldFailure}
	if !slices.Equal(got, want) {
		t.Fatalf("deletes=%v want=%v", got, want)
	}
	if slices.Contains(got, recentFailure) || slices.Contains(got, preRelease) {
		t.Fatalf("recent/protected snapshot selected: %v", got)
	}
	if slices.Contains(got, externalOld) {
		t.Fatalf("external unowned snapshot selected: %v", got)
	}
	if slices.Contains(got, maliciousOwnerOld) {
		t.Fatalf("malicious ownership selected: %v", got)
	}
}

func TestPlanRetentionFailsClosedWhenCurrentOrLastGoodIsMissing(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	current := retentionSnapshotID(1)
	lastGood := retentionSnapshotID(2)
	candidate := retentionSnapshotID(3)
	base := RetentionRepositoryState{
		CandidateSnapshotIDs: []string{candidate},
		CommittedSnapshotIDs: []string{current, lastGood, candidate},
		CurrentSnapshotID:    current,
		LastGoodSnapshotID:   lastGood,
	}
	for name, snapshots := range map[string][]RepositorySnapshot{
		"current": {
			{ID: lastGood, CreatedAt: now.Add(-time.Hour)},
			{ID: candidate, CreatedAt: now.Add(-48 * time.Hour)},
		},
		"last-good": {
			{ID: current, CreatedAt: now.Add(-time.Hour)},
			{ID: candidate, CreatedAt: now.Add(-48 * time.Hour)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PlanRetentionDeletes(base, snapshots, now); err == nil {
				t.Fatal("incomplete repository accepted")
			}
		})
	}
	got, err := PlanRetentionDeletes(base, []RepositorySnapshot{
		{ID: current, CreatedAt: now.Add(-time.Hour)},
		{ID: lastGood, CreatedAt: now.Add(-time.Hour)},
	}, now)
	if err != nil {
		t.Fatalf("idempotent retry rejected: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("already absent candidate selected: %v", got)
	}
}

func TestPlanRetentionFailsClosedWhenAnyRetainedSnapshotIsMissing(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	current := retentionSnapshotID(1)
	lastGood := retentionSnapshotID(2)
	preRelease := retentionSnapshotID(3)
	if _, err := PlanRetentionDeletes(RetentionRepositoryState{
		CommittedSnapshotIDs: []string{current, lastGood, preRelease},
		CurrentSnapshotID:    current,
		LastGoodSnapshotID:   lastGood,
	}, []RepositorySnapshot{
		{ID: current, CreatedAt: now.Add(-time.Hour)},
		{ID: lastGood, CreatedAt: now.Add(-24 * time.Hour)},
	}, now); err == nil {
		t.Fatal("missing retained pre-release/daily/monthly snapshot accepted")
	}
	t.Log("PHASE5_FAILURE_EVIDENCE case=retention_failure actual=failed maintenance=normal alert=active plaintext_dump=absent")
}

func TestExecutorForgetsInDeterministicBoundedGlobalBatches(t *testing.T) {
	runner := &recordingRunner{}
	executor, _ := executorFixture(t, runner)
	ids := make([]string, 0, RetentionDeleteBatchLimit+1)
	for value := RetentionDeleteBatchLimit + 1; value >= 1; value-- {
		ids = append(ids, retentionSnapshotID(byte(value)))
	}
	runner.run = func(_ context.Context, command Command, _ int) (CommandResult, error) {
		return CommandResult{ExitCode: 0}, nil
	}
	if err := executor.ForgetRetentionSnapshots(
		context.Background(),
		RepositoryLocal,
		ids,
	); err != nil {
		t.Fatal(err)
	}
	commands := runner.calls()
	if len(commands) != 2 {
		t.Fatalf("forget commands=%d", len(commands))
	}
	previous := ""
	seen := 0
	for _, command := range commands {
		if command.Executable != resticExecutable ||
			len(command.Args) < 6 ||
			command.Args[0] != "--no-cache" ||
			command.Args[1] != "forget" ||
			command.Args[2] != "--group-by" ||
			command.Args[3] != "" ||
			command.Args[len(command.Args)-1] != "--prune" {
			t.Fatalf("unsafe forget command=%q", command.Args)
		}
		requireResticTemporaryDirectory(t, command, executor.config.WorkRoot)
		batch := command.Args[4 : len(command.Args)-1]
		if len(batch) > RetentionDeleteBatchLimit {
			t.Fatalf("batch size=%d", len(batch))
		}
		for _, id := range batch {
			if previous != "" && id <= previous {
				t.Fatalf("non-deterministic IDs previous=%q id=%q", previous, id)
			}
			previous = id
			seen++
		}
	}
	if seen != len(ids) {
		t.Fatalf("seen=%d want=%d", seen, len(ids))
	}
}
