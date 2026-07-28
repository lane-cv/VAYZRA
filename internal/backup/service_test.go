package backup

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/operations"
)

type serviceStore struct {
	created       []CreateInput
	createResult  Run
	createErr     error
	claims        []claimCall
	claimResult   Run
	claimErr      error
	targetClaims  []targetClaimCall
	targetResult  Run
	targetErr     error
	renewCalls    []renewCall
	renewResult   Run
	renewErr      error
	transitions   []TransitionInput
	transitionRun Run
	transitionErr error
	artifacts     []Artifact
	listFilter    Filter
	page          Page
	detail        RunDetail
	detailErr     error
	retention     RetentionPolicy
	candidates    []Artifact
}

type claimCall struct {
	owner uuid.UUID
	lease time.Duration
}

type targetClaimCall struct {
	runID uuid.UUID
	owner uuid.UUID
	lease time.Duration
}

type renewCall struct {
	runID      uuid.UUID
	owner      uuid.UUID
	generation int64
	lease      time.Duration
}

func (s *serviceStore) Create(_ context.Context, input CreateInput) (Run, error) {
	s.created = append(s.created, input)
	if s.createResult.ID == uuid.Nil {
		s.createResult = Run{
			ID: input.ID, Trigger: input.Trigger, IdempotencyKey: input.IdempotencyKey,
			RequestedBy: input.RequestedBy, RequestedAt: input.RequestedAt, State: StateQueued,
		}
	}
	return s.createResult, s.createErr
}

func (s *serviceStore) Claim(_ context.Context, owner uuid.UUID, lease time.Duration) (Run, error) {
	s.claims = append(s.claims, claimCall{owner: owner, lease: lease})
	return s.claimResult, s.claimErr
}

func (s *serviceStore) ClaimRunByID(
	_ context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	lease time.Duration,
) (Run, error) {
	s.targetClaims = append(s.targetClaims, targetClaimCall{
		runID: runID, owner: owner, lease: lease,
	})
	return s.targetResult, s.targetErr
}

func (s *serviceStore) Renew(
	_ context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	generation int64,
	lease time.Duration,
) (Run, error) {
	s.renewCalls = append(s.renewCalls, renewCall{
		runID: runID, owner: owner, generation: generation, lease: lease,
	})
	return s.renewResult, s.renewErr
}

func (s *serviceStore) Transition(_ context.Context, input TransitionInput) (Run, error) {
	s.transitions = append(s.transitions, input)
	return s.transitionRun, s.transitionErr
}

func (s *serviceStore) AddArtifact(_ context.Context, artifact Artifact) error {
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func (s *serviceStore) List(_ context.Context, filter Filter) ([]RunSummary, Cursor, error) {
	s.listFilter = filter
	return s.page.Items, s.page.Next, nil
}

func (s *serviceStore) Get(context.Context, uuid.UUID) (RunDetail, error) {
	return s.detail, s.detailErr
}

func (s *serviceStore) RetentionCandidates(_ context.Context, policy RetentionPolicy) ([]Artifact, error) {
	s.retention = policy
	return s.candidates, nil
}

func adminPrincipal() operations.Principal {
	return operations.Principal{
		User: auth.User{
			ID:   uuid.MustParse("11111111-1111-4111-8111-111111111111"),
			Role: auth.RoleAdmin, Status: auth.StatusActive,
		},
		RequestID: "request-backup-1",
		IP:        net.ParseIP("192.0.2.10"),
	}
}

func TestScheduledIdempotencyUsesAsiaShanghaiCalendarDate(t *testing.T) {
	store := &serviceStore{}
	now := time.Date(2026, 7, 28, 16, 30, 0, 0, time.UTC)
	service := NewService(store, func() time.Time { return now })

	run, err := service.RequestScheduled(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.Trigger != TriggerScheduled || run.IdempotencyKey != "2026-07-29" {
		t.Fatalf("trigger=%q key=%q", run.Trigger, run.IdempotencyKey)
	}
	if len(store.created) != 1 ||
		!store.created[0].RequestedAt.Equal(now) ||
		store.created[0].RequestedBy != uuid.Nil {
		t.Fatalf("created=%+v", store.created)
	}
}

func TestManualAndPreReleaseRequestsPreserveCallerIdempotency(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger Trigger
		key     string
		call    func(*Service) (Run, error)
	}{
		{
			name: "manual", trigger: TriggerManual, key: "manual:key-123",
			call: func(s *Service) (Run, error) {
				return s.RequestManual(context.Background(), adminPrincipal(), "manual:key-123")
			},
		},
		{
			name: "pre release", trigger: TriggerPreRelease, key: "release:key-123",
			call: func(s *Service) (Run, error) {
				return s.RequestPreRelease(context.Background(), "release:key-123")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &serviceStore{}
			now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
			service := NewService(store, func() time.Time { return now })
			run, err := tc.call(service)
			if err != nil {
				t.Fatal(err)
			}
			if run.Trigger != tc.trigger || run.IdempotencyKey != tc.key {
				t.Fatalf("run=%+v", run)
			}
			got := store.created[0]
			if got.Trigger != tc.trigger || got.IdempotencyKey != tc.key {
				t.Fatalf("created=%+v", got)
			}
			if tc.trigger == TriggerManual && got.RequestedBy != adminPrincipal().User.ID {
				t.Fatalf("manual requested_by=%s", got.RequestedBy)
			}
			if tc.trigger == TriggerManual &&
				(got.RequestID != adminPrincipal().RequestID ||
					!got.IP.Equal(adminPrincipal().IP)) {
				t.Fatalf("manual audit context request_id=%q ip=%v", got.RequestID, got.IP)
			}
			if tc.trigger == TriggerPreRelease && got.RequestedBy != uuid.Nil {
				t.Fatalf("pre-release requested_by=%s", got.RequestedBy)
			}
		})
	}
}

func TestBackupServiceRejectsInvalidPrincipalAndIdempotency(t *testing.T) {
	store := &serviceStore{}
	service := NewService(store, time.Now)
	for _, tc := range []struct {
		name      string
		principal operations.Principal
		key       string
	}{
		{name: "student", principal: func() operations.Principal {
			p := adminPrincipal()
			p.User.Role = auth.RoleStudent
			return p
		}(), key: "manual:key-123"},
		{name: "inactive", principal: func() operations.Principal {
			p := adminPrincipal()
			p.User.Status = auth.StatusDisabled
			return p
		}(), key: "manual:key-123"},
		{name: "invalid key", principal: adminPrincipal(), key: "contains space"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.RequestManual(context.Background(), tc.principal, tc.key)
			if !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if len(store.created) != 0 {
		t.Fatalf("unexpected creates=%d", len(store.created))
	}
}

func TestBackupServiceRenewsCurrentLeaseGeneration(t *testing.T) {
	runID, owner := uuid.New(), uuid.New()
	store := &serviceStore{renewResult: Run{
		ID: runID, OwnerID: owner, LeaseGeneration: 7,
		LeaseExpiresAt: timePointer(time.Now().UTC().Add(time.Minute)),
	}}
	service := NewService(store, time.Now)
	renewed, err := service.Renew(context.Background(), runID, owner, 7, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.LeaseGeneration != 7 ||
		len(store.renewCalls) != 1 ||
		store.renewCalls[0] != (renewCall{
			runID: runID, owner: owner, generation: 7, lease: 2 * time.Minute,
		}) {
		t.Fatalf("renewed=%+v calls=%+v", renewed, store.renewCalls)
	}
}

func TestBackupServiceClaimsOnlyTheExactRunID(t *testing.T) {
	runID, owner := uuid.New(), uuid.New()
	store := &serviceStore{targetResult: Run{
		ID: runID, OwnerID: owner, LeaseGeneration: 3,
		LeaseExpiresAt: timePointer(time.Now().UTC().Add(time.Minute)),
	}}
	service := NewService(store, time.Now)
	claimed, err := service.ClaimRunByID(
		context.Background(),
		runID,
		owner,
		2*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != runID ||
		claimed.OwnerID != owner ||
		claimed.LeaseGeneration != 3 ||
		len(store.targetClaims) != 1 ||
		store.targetClaims[0] != (targetClaimCall{
			runID: runID, owner: owner, lease: 2 * time.Minute,
		}) {
		t.Fatalf("claimed=%+v calls=%+v", claimed, store.targetClaims)
	}
}

func TestCompleteChoosesSucceededOrDegradedFromRemoteOutcome(t *testing.T) {
	for _, tc := range []struct {
		name             string
		from             State
		remoteConfigured bool
		remoteSucceeded  bool
		want             State
	}{
		{name: "local only", from: StateVerifying, want: StateSucceeded},
		{name: "remote success", from: StateSyncing, remoteConfigured: true, remoteSucceeded: true, want: StateSucceeded},
		{name: "remote failure", from: StateSyncing, remoteConfigured: true, remoteSucceeded: false, want: StateDegraded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &serviceStore{}
			store.transitionRun = Run{ID: uuid.New(), State: tc.want}
			now := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
			service := NewService(store, func() time.Time { return now })
			evidence := RecoveryEvidence{
				DatabaseMigrationVersion: 20,
				EncryptionKeyID:          "age-key-2026-07",
				LocalSnapshotID:          "aabbccddeeff0011",
				ManifestSHA256:           make([]byte, 32),
				LocalExpiresAt:           now.Add(7 * 24 * time.Hour),
			}
			_, err := service.Complete(context.Background(), CompletionInput{
				RunID: uuid.New(), OwnerID: uuid.New(), LeaseGeneration: 1, From: tc.from,
				Evidence: evidence, RemoteConfigured: tc.remoteConfigured,
				RemoteSucceeded: tc.remoteSucceeded,
				ErrorCategory: func() string {
					if tc.want == StateDegraded {
						return "remote_unavailable"
					}
					return ""
				}(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(store.transitions) != 1 || store.transitions[0].To != tc.want {
				t.Fatalf("transitions=%+v want=%q", store.transitions, tc.want)
			}
			if tc.want == StateDegraded &&
				store.transitions[0].ErrorCategory != "remote_unavailable" {
				t.Fatalf("degraded transition=%+v", store.transitions[0])
			}
			if !store.transitions[0].At.Equal(now) {
				t.Fatalf("transition at=%v want=%v", store.transitions[0].At, now)
			}
		})
	}
}

func TestCompleteRequiresMigrationManifestKeySnapshotAndLocalExpiry(t *testing.T) {
	valid := RecoveryEvidence{
		DatabaseMigrationVersion: 20,
		EncryptionKeyID:          "age-key-2026-07",
		LocalSnapshotID:          "aabbccddeeff0011",
		ManifestSHA256:           make([]byte, 32),
		LocalExpiresAt:           time.Now().Add(24 * time.Hour),
	}
	cases := []func(*RecoveryEvidence){
		func(v *RecoveryEvidence) { v.DatabaseMigrationVersion = 0 },
		func(v *RecoveryEvidence) { v.EncryptionKeyID = "" },
		func(v *RecoveryEvidence) { v.LocalSnapshotID = "" },
		func(v *RecoveryEvidence) { v.ManifestSHA256 = []byte{1} },
		func(v *RecoveryEvidence) { v.LocalExpiresAt = time.Time{} },
	}
	for i, mutate := range cases {
		store := &serviceStore{}
		service := NewService(store, time.Now)
		evidence := valid
		mutate(&evidence)
		_, err := service.Complete(context.Background(), CompletionInput{
			RunID: uuid.New(), OwnerID: uuid.New(), LeaseGeneration: 1, From: StateVerifying,
			Evidence: evidence,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("case %d err=%v", i, err)
		}
		if len(store.transitions) != 0 {
			t.Fatalf("case %d transitioned", i)
		}
	}
}

func TestBackupServiceFailsClosedOnInvalidRestoreRowCounts(t *testing.T) {
	for _, counts := range []map[string]int64{
		nil,
		{"secret_table": 1},
		{"users": -1},
	} {
		store := &serviceStore{detail: RunDetail{
			Run: Run{ID: uuid.New(), RequestedAt: time.Now().UTC()},
			RestoreVerifications: []RestoreVerification{{
				ID: uuid.New(), DatabaseRowCounts: counts,
			}},
		}}
		service := NewService(store, time.Now)
		_, err := service.Get(
			context.Background(),
			adminPrincipal(),
			store.detail.Run.ID,
		)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("counts=%v err=%v", counts, err)
		}
	}
}
