package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/database"
	"happylearn.local/app/internal/platform/secretfile"
)

const (
	leaseTTL      = 2 * time.Minute
	renewInterval = 30 * time.Second
)

type leaseManager interface {
	AcquireLease(context.Context, operations.LeaseRequest) (operations.Lease, error)
	RenewLease(context.Context, operations.Lease, time.Time) (operations.Lease, error)
	ReleaseLease(context.Context, operations.Lease) error
}

type safeResult struct {
	Status   string `json:"status"`
	Category string `json:"category"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	url, err := databaseURL(os.Getenv)
	if err != nil {
		writeResult(os.Stdout, "fail", "configuration_unavailable")
		os.Exit(1)
	}
	pool, err := database.Open(ctx, url)
	url = ""
	if err != nil {
		writeResult(os.Stdout, "fail", "database_unavailable")
		os.Exit(1)
	}
	store := operations.NewPostgresStore(pool)
	if err := hold(ctx, store, os.Stdout, time.Now, time.NewTicker); err != nil {
		pool.Close()
		os.Exit(1)
	}
	pool.Close()
}

func hold(ctx context.Context, manager leaseManager, output io.Writer, now func() time.Time, ticker func(time.Duration) *time.Ticker) error {
	owner := uuid.New()
	lease, err := manager.AcquireLease(ctx, operations.LeaseRequest{Mode: "release", OwnerID: owner, ExpiresAt: now().UTC().Add(leaseTTL)})
	if err != nil {
		writeResult(output, "fail", "lease_unavailable")
		return errors.New("lease unavailable")
	}
	writeResult(output, "pass", "release_mode_ready")
	renew := ticker(renewInterval)
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := manager.ReleaseLease(releaseCtx, lease)
			cancel()
			if err != nil {
				writeResult(output, "fail", "normalization_failed")
				return err
			}
			writeResult(output, "pass", "normal_mode_restored")
			return nil
		case <-renew.C:
			renewCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			lease, err = manager.RenewLease(renewCtx, lease, now().UTC().Add(leaseTTL))
			cancel()
			if err != nil {
				writeResult(output, "fail", "lease_renewal_failed")
				return err
			}
		}
	}
}

func databaseURL(getenv func(string) string) (string, error) {
	if getenv == nil || getenv("HAPPYLEARN_DATABASE_URL") != "" || getenv("HAPPYLEARN_DATABASE_URL_FILE") == "" {
		return "", errors.New("configuration unavailable")
	}
	value, err := secretfile.Read(getenv("HAPPYLEARN_DATABASE_URL_FILE"))
	if err != nil {
		return "", errors.New("configuration unavailable")
	}
	return string(value), nil
}

func writeResult(output io.Writer, status, category string) {
	_ = json.NewEncoder(output).Encode(safeResult{Status: status, Category: category})
}
