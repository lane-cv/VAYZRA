package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/files"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/config"
)

func TestBuildApplicationStartsAndStopsUploadCleanupRunner(t *testing.T) {
	cleaner := &serverUploadCleaner{}
	started, stopped := false, false
	_, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		newUploads: func(context.Context, *pgxpool.Pool, config.Config) (files.UploadHTTPService, error) {
			return cleaner, nil
		},
		startUploadCleanup: func(got files.ExpiredUploadCleaner, _ operations.ClaimGate) func() {
			if got != cleaner {
				t.Fatal("wrong cleaner")
			}
			started = true
			return func() { stopped = true }
		},
		ready: func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		close: func(*pgxpool.Pool) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("cleanup runner not started")
	}
	closeResources()
	if !stopped {
		t.Fatal("cleanup runner not stopped")
	}
}

type serverUploadCleaner struct{}

func (*serverUploadCleaner) Create(context.Context, files.Principal, files.CreateUploadInput) (files.UploadView, error) {
	return files.UploadView{}, nil
}
func (*serverUploadCleaner) Status(context.Context, files.Principal, uuid.UUID) (files.UploadView, error) {
	return files.UploadView{}, nil
}
func (*serverUploadCleaner) PutPart(context.Context, files.Principal, files.PutPartInput) (files.PartView, error) {
	return files.PartView{}, nil
}
func (*serverUploadCleaner) Complete(context.Context, files.Principal, uuid.UUID) (files.CompletedUpload, error) {
	return files.CompletedUpload{}, nil
}
func (*serverUploadCleaner) Cancel(context.Context, files.Principal, uuid.UUID) error { return nil }
func (*serverUploadCleaner) CleanupExpired(context.Context, int) error                { return nil }
