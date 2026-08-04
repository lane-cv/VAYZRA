package objectstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/minio/madmin-go/v3"
)

func TestObjectStoreMinIOImplementsStore(t *testing.T) {
	var _ Store = (*MinIOStore)(nil)
}

func TestObjectStoreSentinelErrorsRemainComparable(t *testing.T) {
	for _, err := range []error{ErrNotFound, ErrConflict, ErrUnavailable} {
		if !errors.Is(err, err) {
			t.Fatalf("sentinel error is not comparable: %v", err)
		}
	}
}

func TestMinIOStorageUsageSumsReportedDisks(t *testing.T) {
	stores := &MinIOStores{
		admin: storageAdminStub{info: madmin.StorageInfo{Disks: []madmin.Disk{
			{TotalSpace: 100, UsedSpace: 30},
			{TotalSpace: 200, UsedSpace: 70},
		}}},
		operationTimeout: time.Second,
	}
	used, capacity, err := stores.StorageUsage(nil)
	if err == nil {
		t.Fatal("expected nil context to be rejected")
	}
	used, capacity, err = stores.StorageUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if used != 100 || capacity != 300 {
		t.Fatalf("used=%d capacity=%d", used, capacity)
	}
}

func TestMinIOStorageUsageRejectsEmptyStorageInfo(t *testing.T) {
	stores := &MinIOStores{
		admin: storageAdminStub{info: madmin.StorageInfo{}},
	}
	if _, _, err := stores.StorageUsage(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

type storageAdminStub struct {
	info madmin.StorageInfo
	err  error
}

func (stub storageAdminStub) StorageInfo(context.Context) (madmin.StorageInfo, error) {
	return stub.info, stub.err
}
