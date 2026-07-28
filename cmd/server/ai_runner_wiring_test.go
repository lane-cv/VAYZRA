package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"happylearn.local/app/internal/aiqa"
	"happylearn.local/app/internal/auth"
	"happylearn.local/app/internal/operations"
	"happylearn.local/app/internal/platform/config"
)

type productionGatewayResolver struct {
	address netip.Addr
}

func (r productionGatewayResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{r.address}, nil
}

func TestProductionAIGatewayUsesPerRunResponseHeaderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	baseURL, err := url.Parse("http://supplier.test:" + serverURL.Port() + "/v1")
	if err != nil {
		t.Fatal(err)
	}
	cfg := aiqa.RuntimeProviderConfig{
		BaseURL: baseURL, ProtocolMode: aiqa.ProtocolChatCompletions, APIKey: []byte("secret"),
		Timeouts: aiqa.GatewayTimeouts{Connect: time.Second, ResponseHeader: 20 * time.Millisecond, IdleStream: time.Second, Total: time.Second},
	}
	request := aiqa.GatewayRequest{
		RunID: uuid.New(), Model: "model", MaxOutputTokens: 1,
		Turns: []aiqa.GatewayTurn{{Role: "student", Text: "x"}},
	}
	err = newProductionAIGateway(aiqa.URLPolicy{
		DevelopmentAllowPrivate: true,
		Resolver:                productionGatewayResolver{address: netip.MustParseAddr("127.0.0.1")},
	}).Stream(
		context.Background(), cfg, request, func(aiqa.GatewayEvent) error { return nil })
	var gatewayErr *aiqa.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Category != "timeout" {
		t.Fatalf("error=%v", err)
	}
}

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
		startAIRunner: func(context.Context, *pgxpool.Pool, config.Config, operations.ClaimGate) (func(), error) {
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
		startAIRunner: func(context.Context, *pgxpool.Pool, config.Config, operations.ClaimGate) (func(), error) {
			return nil, errors.New("private runner detail")
		},
		close: func(*pgxpool.Pool) { order = append(order, "database-close") },
	})
	if handler != nil || closeResources != nil || err == nil || err.Error() != "initialize AI runner" ||
		strings.Join(order, ",") != "database-close" {
		t.Fatalf("handler=%v closeNil=%t err=%v order=%v", handler, closeResources == nil, err, order)
	}
}
