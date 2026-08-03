package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"happylearn.local/app/internal/operations"
)

type fakeManager struct {
	acquired   operations.LeaseRequest
	released   bool
	acquireErr error
	releaseErr error
}

func (f *fakeManager) AcquireLease(_ context.Context, request operations.LeaseRequest) (operations.Lease, error) {
	f.acquired = request
	if f.acquireErr != nil {
		return operations.Lease{}, f.acquireErr
	}
	return operations.Lease{Mode: "release", OwnerID: request.OwnerID, Token: []byte("not-output"), ExpiresAt: request.ExpiresAt, Version: 1}, nil
}
func (f *fakeManager) RenewLease(_ context.Context, lease operations.Lease, _ time.Time) (operations.Lease, error) {
	return lease, nil
}
func (f *fakeManager) ReleaseLease(context.Context, operations.Lease) error {
	f.released = true
	return f.releaseErr
}

func TestHoldAcquiresReleaseAndNormalizesOnSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := &fakeManager{}
	var output bytes.Buffer
	err := hold(ctx, manager, &output, func() time.Time { return time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC) }, time.NewTicker)
	if err != nil || manager.acquired.Mode != "release" || !manager.released {
		t.Fatalf("err=%v request=%+v released=%v", err, manager.acquired, manager.released)
	}
	if strings.Contains(output.String(), "not-output") || !strings.Contains(output.String(), "normal_mode_restored") {
		t.Fatalf("output=%s", output.String())
	}
}

func TestHoldFailsSafeWhenLeaseCannotBeAcquired(t *testing.T) {
	manager := &fakeManager{acquireErr: errors.New("postgres://secret")}
	var output bytes.Buffer
	if err := hold(context.Background(), manager, &output, time.Now, time.NewTicker); err == nil || strings.Contains(output.String(), "postgres") || !strings.Contains(output.String(), "lease_unavailable") {
		t.Fatalf("err=%v output=%s", err, output.String())
	}
}

func TestDatabaseURLRequiresFile(t *testing.T) {
	if _, err := databaseURL(func(name string) string {
		if name == "HAPPYLEARN_DATABASE_URL" {
			return "direct"
		}
		return ""
	}); err == nil {
		t.Fatal("direct URL accepted")
	}
}
