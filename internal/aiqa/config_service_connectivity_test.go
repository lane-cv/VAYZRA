package aiqa

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

var uuidForConnectivityTest = uuid.MustParse("84bb52bf-d088-48ef-858c-94f39b617108")

type connectivityStore struct {
	memoryConfigStore
	mu         sync.Mutex
	active     bool
	config     RuntimeProviderConfig
	audits     []providerTestAudit
	missing    bool
	acquireErr error
}

func (s *connectivityStore) AcquireProviderTest(context.Context, uuid.UUID) (RuntimeProviderConfig, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.missing {
		return RuntimeProviderConfig{}, nil, ErrNotFound
	}
	if s.acquireErr != nil {
		return s.config, nil, s.acquireErr
	}
	if s.active {
		return s.config, nil, ErrProviderTestBusy
	}
	s.active = true
	return s.config, func() {
		s.mu.Lock()
		s.active = false
		s.mu.Unlock()
	}, nil
}

func TestConnectivityServiceAuditsResolvedPreparationFailure(t *testing.T) {
	store := &connectivityStore{
		config:     RuntimeProviderConfig{ProviderID: uuidForConnectivityTest, ProtocolMode: ProtocolChatCompletions},
		acquireErr: ErrProviderUnavailable,
	}
	svc := NewAdminConfigServiceWithConnectivity(store, URLPolicy{}, testSecretBox{}, &blockingConnectivityTester{})
	admin := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}
	result, err := svc.TestProvider(context.Background(), admin, uuidForConnectivityTest)
	if !errors.Is(err, ErrProviderUnavailable) || result.Protocol != ProtocolChatCompletions || result.ErrorCategory != "unavailable" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(store.audits) != 1 || store.audits[0].category != "unavailable" || store.audits[0].protocol != ProtocolChatCompletions {
		t.Fatalf("audits=%#v", store.audits)
	}
}

func (s *connectivityStore) RecordProviderTest(_ context.Context, _ Principal, audit providerTestAudit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audits = append(s.audits, audit)
	return nil
}

type blockingConnectivityTester struct {
	started chan struct{}
	release chan struct{}
	result  ConnectivityResult
	err     error
}

func (t *blockingConnectivityTester) Test(ctx context.Context, _ RuntimeProviderConfig) (ConnectivityResult, error) {
	if t.started != nil {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
	if t.release != nil {
		select {
		case <-t.release:
		case <-ctx.Done():
			return ConnectivityResult{ErrorCategory: "cancelled"}, ctx.Err()
		}
	}
	return t.result, t.err
}

func TestConnectivityServiceRequiresAdminAndAuditsOnlySafeResult(t *testing.T) {
	store := &connectivityStore{config: RuntimeProviderConfig{ProviderID: uuidForConnectivityTest, ProtocolMode: ProtocolResponses}}
	tester := &blockingConnectivityTester{result: ConnectivityResult{OK: false, Protocol: ProtocolResponses, LatencyMS: 7, ErrorCategory: "auth"}, err: &GatewayError{Category: "auth"}}
	svc := NewAdminConfigServiceWithConnectivity(store, URLPolicy{}, testSecretBox{}, tester)
	student := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}}
	if _, err := svc.TestProvider(context.Background(), student, uuidForConnectivityTest); !errors.Is(err, ErrForbidden) {
		t.Fatalf("student error=%v", err)
	}
	admin := student
	admin.User.Role = auth.RoleAdmin
	admin.RequestID = "request-safe"
	admin.IP = net.ParseIP("192.0.2.22")
	result, err := svc.TestProvider(context.Background(), admin, uuidForConnectivityTest)
	if !errors.Is(err, ErrProviderUnavailable) || result.ErrorCategory != "auth" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(store.audits) != 1 {
		t.Fatalf("audits=%#v", store.audits)
	}
	audit := store.audits[0]
	if audit.providerID != uuidForConnectivityTest || audit.protocol != ProtocolResponses || audit.ok || audit.category != "auth" || audit.latencyMS != 7 {
		t.Fatalf("audit=%#v", audit)
	}
}

func TestConnectivityServiceAllowsOneActiveTestPerProviderAcrossInstances(t *testing.T) {
	store := &connectivityStore{config: RuntimeProviderConfig{ProviderID: uuidForConnectivityTest, ProtocolMode: ProtocolResponses}}
	tester := &blockingConnectivityTester{started: make(chan struct{}, 1), release: make(chan struct{}), result: ConnectivityResult{OK: true, Protocol: ProtocolResponses}}
	first := NewAdminConfigServiceWithConnectivity(store, URLPolicy{}, testSecretBox{}, tester)
	second := NewAdminConfigServiceWithConnectivity(store, URLPolicy{}, testSecretBox{}, tester)
	admin := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}
	done := make(chan error, 1)
	go func() {
		_, err := first.TestProvider(context.Background(), admin, uuidForConnectivityTest)
		done <- err
	}()
	select {
	case <-tester.started:
	case <-time.After(time.Second):
		t.Fatal("first test did not start")
	}
	result, err := second.TestProvider(context.Background(), admin, uuidForConnectivityTest)
	if !errors.Is(err, ErrProviderTestBusy) || result.Protocol != ProtocolResponses {
		t.Fatalf("concurrent result=%#v err=%v", result, err)
	}
	close(tester.release)
	if err := <-done; err != nil {
		t.Fatalf("first test: %v", err)
	}
	if len(store.audits) != 2 || store.audits[0].category != "busy" && store.audits[1].category != "busy" {
		t.Fatalf("audits=%#v", store.audits)
	}
}
