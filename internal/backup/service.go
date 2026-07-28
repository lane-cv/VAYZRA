package backup

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/operations"
)

var shanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

func (s *Service) RequestScheduled(ctx context.Context) (Run, error) {
	now := s.now().UTC()
	return s.create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerScheduled,
		IdempotencyKey: now.In(shanghai).Format("2006-01-02"),
		RequestedAt:    now,
	})
}

func (s *Service) RequestPreRelease(ctx context.Context, idempotencyKey string) (Run, error) {
	return s.create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerPreRelease,
		IdempotencyKey: idempotencyKey, RequestedAt: s.now().UTC(),
	})
}

func (s *Service) RequestManual(
	ctx context.Context,
	principal operations.Principal,
	idempotencyKey string,
) (Run, error) {
	if err := authorize(principal); err != nil {
		return Run{}, err
	}
	run, err := s.create(ctx, CreateInput{
		ID: uuid.New(), Trigger: TriggerManual, IdempotencyKey: idempotencyKey,
		RequestedBy: principal.User.ID, RequestedAt: s.now().UTC(),
		RequestID: principal.RequestID, IP: append(net.IP(nil), principal.IP...),
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *Service) create(ctx context.Context, input CreateInput) (Run, error) {
	if s == nil || s.store == nil || validateCreate(input) != nil {
		return Run{}, ErrInvalid
	}
	run, err := s.store.Create(ctx, input)
	if err != nil {
		return Run{}, mapStoreError(err)
	}
	return normalizeRun(run), nil
}

func (s *Service) Claim(ctx context.Context, owner uuid.UUID, lease time.Duration) (Run, error) {
	if s == nil || s.store == nil || owner == uuid.Nil ||
		lease < time.Second || lease > 24*time.Hour {
		return Run{}, ErrInvalid
	}
	run, err := s.store.Claim(ctx, owner, lease)
	if err != nil {
		return Run{}, mapStoreError(err)
	}
	return normalizeRun(run), nil
}

func (s *Service) Renew(
	ctx context.Context,
	runID uuid.UUID,
	owner uuid.UUID,
	generation int64,
	lease time.Duration,
) (Run, error) {
	if s == nil || s.store == nil || runID == uuid.Nil || owner == uuid.Nil ||
		generation < 1 || lease < time.Second || lease > 24*time.Hour {
		return Run{}, ErrInvalid
	}
	run, err := s.store.Renew(ctx, runID, owner, generation, lease)
	if err != nil {
		return Run{}, mapStoreError(err)
	}
	return normalizeRun(run), nil
}

func (s *Service) Transition(ctx context.Context, input TransitionInput) (Run, error) {
	if s == nil || s.store == nil ||
		input.RunID == uuid.Nil || input.OwnerID == uuid.Nil ||
		input.LeaseGeneration < 1 ||
		!validState(input.From) || !validState(input.To) ||
		!ValidTransition(input.From, input.To) ||
		!validSafeError(input.ErrorCategory, input.ErrorTraceID) {
		return Run{}, ErrInvalidTransition
	}
	if input.At.IsZero() {
		input.At = s.now().UTC()
	} else {
		input.At = input.At.UTC()
	}
	if input.To == StateSucceeded || input.To == StateDegraded {
		if input.Evidence == nil || validateEvidence(*input.Evidence) != nil {
			return Run{}, ErrInvalid
		}
	} else if input.Evidence != nil && validateEvidence(*input.Evidence) != nil {
		return Run{}, ErrInvalid
	}
	if input.To != StateFailed && input.To != StateDegraded &&
		(input.ErrorCategory != "" || input.ErrorTraceID != "") {
		return Run{}, ErrInvalid
	}
	run, err := s.store.Transition(ctx, input)
	if err != nil {
		return Run{}, mapStoreError(err)
	}
	return normalizeRun(run), nil
}

func (s *Service) Complete(ctx context.Context, input CompletionInput) (Run, error) {
	if validateEvidence(input.Evidence) != nil ||
		input.RunID == uuid.Nil || input.OwnerID == uuid.Nil ||
		input.LeaseGeneration < 1 ||
		!validSafeError(input.ErrorCategory, input.ErrorTraceID) {
		return Run{}, ErrInvalid
	}
	var to State
	switch {
	case !input.RemoteConfigured && input.From == StateVerifying:
		to = StateSucceeded
	case input.RemoteConfigured && input.From == StateSyncing && input.RemoteSucceeded:
		to = StateSucceeded
	case input.RemoteConfigured && input.From == StateSyncing && !input.RemoteSucceeded:
		to = StateDegraded
	default:
		return Run{}, ErrInvalidTransition
	}
	if (to == StateDegraded && input.ErrorCategory == "") ||
		(to == StateSucceeded && (input.ErrorCategory != "" || input.ErrorTraceID != "")) {
		return Run{}, ErrInvalid
	}
	return s.Transition(ctx, TransitionInput{
		RunID: input.RunID, OwnerID: input.OwnerID,
		LeaseGeneration: input.LeaseGeneration, From: input.From, To: to,
		At: s.now().UTC(), Evidence: &input.Evidence,
		ErrorCategory: input.ErrorCategory, ErrorTraceID: input.ErrorTraceID,
	})
}

func (s *Service) AddArtifact(ctx context.Context, artifact Artifact) error {
	if s == nil || s.store == nil || validateArtifact(artifact) != nil {
		return ErrInvalid
	}
	if err := s.store.AddArtifact(ctx, artifact); err != nil {
		return mapStoreError(err)
	}
	return nil
}

func (s *Service) List(
	ctx context.Context,
	principal operations.Principal,
	filter Filter,
) (Page, error) {
	if err := authorize(principal); err != nil {
		return Page{}, err
	}
	if filter.Limit < 1 || filter.Limit > 100 ||
		(filter.Before.RequestedAt.IsZero() != (filter.Before.ID == uuid.Nil)) {
		return Page{}, ErrInvalid
	}
	items, next, err := s.store.List(ctx, filter)
	if err != nil {
		return Page{}, mapStoreError(err)
	}
	for i := range items {
		items[i].RequestedAt = items[i].RequestedAt.UTC()
		items[i].StartedAt = cloneTime(items[i].StartedAt)
		items[i].FinishedAt = cloneTime(items[i].FinishedAt)
		items[i].LocalExpiresAt = cloneTime(items[i].LocalExpiresAt)
		items[i].RemoteExpiresAt = cloneTime(items[i].RemoteExpiresAt)
		items[i].ErrorCategory = safeText(items[i].ErrorCategory)
	}
	if !next.RequestedAt.IsZero() {
		next.RequestedAt = next.RequestedAt.UTC()
	}
	return Page{Items: items, Next: next}, nil
}

func (s *Service) Get(
	ctx context.Context,
	principal operations.Principal,
	id uuid.UUID,
) (RunDetail, error) {
	if err := authorize(principal); err != nil {
		return RunDetail{}, err
	}
	if id == uuid.Nil {
		return RunDetail{}, ErrNotFound
	}
	detail, err := s.store.Get(ctx, id)
	if err != nil {
		return RunDetail{}, mapStoreError(err)
	}
	detail.Run = normalizeRun(detail.Run)
	for i := range detail.Artifacts {
		detail.Artifacts[i].SHA256 = append([]byte(nil), detail.Artifacts[i].SHA256...)
		detail.Artifacts[i].VerifiedAt = detail.Artifacts[i].VerifiedAt.UTC()
		detail.Artifacts[i].ExpiresAt = detail.Artifacts[i].ExpiresAt.UTC()
	}
	for i := range detail.RestoreVerifications {
		verification := &detail.RestoreVerifications[i]
		if !validRestoreRowCounts(verification.DatabaseRowCounts) {
			return RunDetail{}, ErrUnavailable
		}
		verification.StartedAt = cloneTime(verification.StartedAt)
		verification.FinishedAt = cloneTime(verification.FinishedAt)
		verification.DatabaseRowCounts = safeRowCounts(verification.DatabaseRowCounts)
		verification.ReportSHA256 = append([]byte(nil), verification.ReportSHA256...)
		verification.ErrorCategory = safeText(verification.ErrorCategory)
		verification.ErrorTraceID = ""
	}
	return detail, nil
}

func (s *Service) RetentionCandidates(ctx context.Context, policy RetentionPolicy) ([]Artifact, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	if policy.Now.IsZero() {
		policy.Now = s.now().UTC()
	}
	if policy.Location == nil {
		policy.Location = shanghai
	}
	if policy.LocalDaily == 0 {
		policy.LocalDaily = 7
	}
	if policy.RemoteDaily == 0 {
		policy.RemoteDaily = 30
	}
	if policy.RemoteMonthly == 0 {
		policy.RemoteMonthly = 12
	}
	if policy.PreReleaseProtectFor == 0 {
		policy.PreReleaseProtectFor = 30 * 24 * time.Hour
	}
	if policy.LocalDaily < 1 || policy.RemoteDaily < 1 ||
		policy.RemoteMonthly < 1 || policy.PreReleaseProtectFor < 0 {
		return nil, ErrInvalid
	}
	artifacts, err := s.store.RetentionCandidates(ctx, policy)
	if err != nil {
		return nil, mapStoreError(err)
	}
	return artifacts, nil
}

func authorize(principal operations.Principal) error {
	if principal.User.ID == uuid.Nil ||
		principal.User.Role != auth.RoleAdmin ||
		principal.User.Status != auth.StatusActive ||
		strings.TrimSpace(principal.RequestID) == "" ||
		len(principal.RequestID) > 64 ||
		principal.IP == nil || principal.IP.To16() == nil {
		return ErrForbidden
	}
	return nil
}

func mapStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrForbidden),
		errors.Is(err, ErrNotFound), errors.Is(err, ErrAlreadyQueued),
		errors.Is(err, ErrNoClaimableRun), errors.Is(err, ErrActiveClaim),
		errors.Is(err, ErrStaleOwner), errors.Is(err, ErrInvalidTransition):
		return err
	default:
		return ErrUnavailable
	}
}
