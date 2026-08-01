package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInfrastructureStatusesAreStableOrderedAndDefaultAbsentRows(t *testing.T) {
	validatedAt := time.Date(2026, 7, 28, 1, 2, 3, 0, time.FixedZone("CST", 8*60*60))
	statuses := NormalizeInfrastructureStatuses([]InfrastructureStatus{
		{Key: InfrastructureRemoteBackup, Configured: true, LastValidatedAt: &validatedAt},
		{Key: InfrastructureApplicationDatabase, Configured: true, LastValidatedAt: &validatedAt},
	})
	want := []InfrastructureKey{
		InfrastructureApplicationDatabase,
		InfrastructureRedisSecurity,
		InfrastructureObjectStore,
		InfrastructureAIEncryption,
		InfrastructureInternalMetrics,
		InfrastructureHostMetricsIngestion,
		InfrastructureAlertWebhook,
		InfrastructureLocalBackup,
		InfrastructureRemoteBackup,
	}
	if len(statuses) != len(want) {
		t.Fatalf("statuses=%+v", statuses)
	}
	for index, key := range want {
		if statuses[index].Key != key {
			t.Fatalf("status[%d].key=%v want=%v", index, statuses[index].Key, key)
		}
		if key != InfrastructureApplicationDatabase && key != InfrastructureRemoteBackup &&
			(statuses[index].Configured || statuses[index].LastValidatedAt != nil) {
			t.Fatalf("absent status[%d]=%+v", index, statuses[index])
		}
	}
	if statuses[0].LastValidatedAt == nil ||
		!statuses[0].LastValidatedAt.Equal(validatedAt.UTC()) ||
		statuses[len(statuses)-1].LastValidatedAt == nil ||
		!statuses[len(statuses)-1].LastValidatedAt.Equal(validatedAt.UTC()) {
		t.Fatalf("normalized timestamps=%+v", statuses)
	}
}

func TestPostgresInfrastructureStatusesAreDurableAndVersionIndependent(t *testing.T) {
	ctx := context.Background()
	pool := migratedOperationsStore(t)
	store := NewPostgresStore(pool)
	admin := seedOperationsAdmin(t, ctx, pool)
	service := NewService(store)

	initial, err := service.GetSettings(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range initial.Infrastructure {
		if status.Configured || status.LastValidatedAt != nil {
			t.Fatalf("initial status=%+v", status)
		}
	}
	first := time.Date(2026, 7, 28, 2, 3, 4, 123456000, time.UTC)
	second := first.Add(time.Minute)
	if err := store.RecordInfrastructureStatus(
		ctx,
		InfrastructureApplicationDatabase,
		true,
		first,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordInfrastructureStatus(
		ctx,
		InfrastructureRemoteBackup,
		false,
		second,
	); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != initial.Version {
		t.Fatalf("settings version changed from %d to %d", initial.Version, after.Version)
	}
	if len(after.Infrastructure) != 9 ||
		!after.Infrastructure[0].Configured ||
		after.Infrastructure[0].LastValidatedAt == nil ||
		!after.Infrastructure[0].LastValidatedAt.Equal(first) ||
		after.Infrastructure[8].Configured ||
		after.Infrastructure[8].LastValidatedAt == nil ||
		!after.Infrastructure[8].LastValidatedAt.Equal(second) {
		t.Fatalf("statuses=%+v", after.Infrastructure)
	}
	after.SiteAnnouncement = "status writes do not conflict"
	updated, err := service.UpdateSettings(ctx, admin, after)
	if err != nil {
		t.Fatalf("status-only writes caused settings conflict: %v", err)
	}
	if updated.Version != initial.Version+1 || len(updated.Infrastructure) != 9 {
		t.Fatalf("updated=%+v", updated)
	}
	if err := store.RecordInfrastructureStatus(
		ctx,
		InfrastructureKey(255),
		true,
		first,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid key error=%v", err)
	}
	if err := store.RecordInfrastructureStatus(
		ctx,
		InfrastructureLocalBackup,
		true,
		time.Time{},
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero time error=%v", err)
	}
}
