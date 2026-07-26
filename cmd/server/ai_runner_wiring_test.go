package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/platform/config"
)

func TestAIRunnerWiringStartsAfterServicesAndStopsBeforeDatabase(t *testing.T) {
	var order []string
	_, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { order = append(order, "open"); return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { order = append(order, "migrate"); return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) {
			order = append(order, "services")
			return serverFakeAuth{}, nil
		},
		ready: func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		startAIRunner: func(context.Context, *pgxpool.Pool, config.Config) (func(), error) {
			order = append(order, "ai-runner-start")
			return func() { order = append(order, "ai-runner-stop") }, nil
		},
		close: func(*pgxpool.Pool) { order = append(order, "database-close") },
	})
	if err != nil {
		t.Fatal(err)
	}
	closeResources()
	want := []string{"open", "migrate", "services", "ai-runner-start", "ai-runner-stop", "database-close"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order=%v want=%v", order, want)
	}
}

func TestAIRunnerWiringFailureClosesExistingResources(t *testing.T) {
	var order []string
	handler, closeResources, err := buildApplication(context.Background(), config.Config{}, applicationDependencies{
		open:    func(context.Context, string) (*pgxpool.Pool, error) { return nil, nil },
		migrate: func(context.Context, *pgxpool.Pool) error { return nil },
		newAuth: func(*pgxpool.Pool) (auth.HTTPService, error) { return serverFakeAuth{}, nil },
		ready:   func(*pgxpool.Pool) func(context.Context) error { return func(context.Context) error { return nil } },
		startAIRunner: func(context.Context, *pgxpool.Pool, config.Config) (func(), error) {
			return nil, errors.New("private runner detail")
		},
		close: func(*pgxpool.Pool) { order = append(order, "database-close") },
	})
	if handler != nil || closeResources != nil || err == nil || err.Error() != "initialize AI runner" ||
		strings.Join(order, ",") != "database-close" {
		t.Fatalf("handler=%v closeNil=%t err=%v order=%v", handler, closeResources == nil, err, order)
	}
}
