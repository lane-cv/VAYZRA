package aiqa

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type testResolver struct{}

func (testResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

type testSecretBox struct{}

func (testSecretBox) Seal(id uuid.UUID, p []byte) (EncryptedSecret, error) {
	return EncryptedSecret{KeyVersion: 1, Blob: append([]byte{1}, p...)}, nil
}
func (testSecretBox) Open(uuid.UUID, EncryptedSecret) ([]byte, error) { return nil, nil }

type memoryConfigStore struct{}

func (*memoryConfigStore) ListProviders(context.Context) ([]ProviderView, error) { return nil, nil }
func (*memoryConfigStore) CreateProvider(_ context.Context, _ Principal, id uuid.UUID, in CreateProviderInput, _ EncryptedSecret, _ [32]byte) (ProviderView, error) {
	return ProviderView{ID: id, Name: in.Name, BaseURL: in.BaseURL, ProtocolMode: in.ProtocolMode, HasKey: true}, nil
}
func (*memoryConfigStore) UpdateProvider(context.Context, Principal, UpdateProviderInput, *EncryptedSecret) (ProviderView, error) {
	return ProviderView{}, nil
}
func (*memoryConfigStore) ActivateProvider(context.Context, Principal, uuid.UUID, int64) (ProviderView, error) {
	return ProviderView{}, nil
}
func (*memoryConfigStore) ListModels(context.Context, uuid.UUID) ([]ModelView, error) {
	return nil, nil
}
func (*memoryConfigStore) PutModel(context.Context, Principal, PutModelInput) (ModelView, error) {
	return ModelView{}, nil
}
func (*memoryConfigStore) ListPrompts(context.Context) ([]PromptView, error) { return nil, nil }
func (*memoryConfigStore) PutPrompt(context.Context, Principal, PutPromptInput) (PromptView, error) {
	return PromptView{}, nil
}
func (*memoryConfigStore) GetLimits(context.Context) (LimitViews, error) { return LimitViews{}, nil }
func (*memoryConfigStore) PutGlobalLimits(context.Context, Principal, PutLimitsInput) (LimitView, error) {
	return LimitView{}, nil
}
func (*memoryConfigStore) PutStudentLimits(context.Context, Principal, uuid.UUID, PutLimitsInput) (LimitView, error) {
	return LimitView{}, nil
}

func TestConfigServiceRejectsStudentAndNormalizesProvider(t *testing.T) {
	s := NewAdminConfigService(&memoryConfigStore{}, URLPolicy{DevelopmentAllowPrivate: true, Resolver: testResolver{}}, testSecretBox{})
	student := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}, RequestID: "r", IP: net.IPv4(127, 0, 0, 1)}
	if _, err := s.CreateProvider(context.Background(), student, CreateProviderInput{Name: " x ", BaseURL: "http://api.example.test/", APIKey: "secret", ProtocolMode: ProtocolChatCompletions, IdempotencyKey: "1234567890abcdef"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
	admin := student
	admin.User.Role = auth.RoleAdmin
	got, err := s.CreateProvider(context.Background(), admin, CreateProviderInput{Name: " x ", BaseURL: "http://api.example.test/", APIKey: "secret", ProtocolMode: ProtocolChatCompletions, IdempotencyKey: "1234567890abcdef"})
	if err != nil || got.ID == uuid.Nil || got.Name != "x" || got.BaseURL != "http://api.example.test" || !got.HasKey {
		t.Fatalf("provider=%#v err=%v", got, err)
	}
}

func TestConfigServiceRejectsGlobalLimitInheritance(t *testing.T) {
	svc := NewAdminConfigService(&memoryConfigStore{}, URLPolicy{DevelopmentAllowPrivate: true, Resolver: testResolver{}}, testSecretBox{})
	actor := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}
	in := PutLimitsInput{DailyRequests: LimitValue{Mode: "inherit"}, MonthlyRequests: LimitValue{Mode: "disabled"}, DailyTokens: LimitValue{Mode: "disabled"}, MonthlyTokens: LimitValue{Mode: "disabled"}, ExpectedVersion: 1}
	if _, err := svc.PutGlobalLimits(context.Background(), actor, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("global inherit=%v", err)
	}
}

func TestConfigServiceAllowsInitialPromptVersion(t *testing.T) {
	svc := NewAdminConfigService(&memoryConfigStore{}, URLPolicy{DevelopmentAllowPrivate: true, Resolver: testResolver{}}, testSecretBox{})
	actor := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}
	if _, err := svc.PutPrompt(context.Background(), actor, PutPromptInput{Subject: SubjectMath, Body: "initial", ExpectedVersion: 0}); err != nil {
		t.Fatalf("initial prompt=%v", err)
	}
}

func TestConfigServiceEnforcesModelTimeoutBounds(t *testing.T) {
	svc := NewAdminConfigService(&memoryConfigStore{}, URLPolicy{DevelopmentAllowPrivate: true, Resolver: testResolver{}}, testSecretBox{})
	actor := Principal{User: auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}}
	valid := PutModelInput{
		ProviderID: uuid.New(), ID: uuid.New(), UpstreamModelID: "model", Modality: ModalityText,
		ContextTokens: 100, MaxOutputTokens: 50, ImageQuotaTokens: 1,
		ConnectTimeoutMS: 100, ResponseHeaderTimeoutMS: 1000, IdleStreamTimeoutMS: 1000, TotalTimeoutMS: 1000,
	}
	if _, err := svc.PutModel(context.Background(), actor, valid); err != nil {
		t.Fatalf("valid timeouts=%v", err)
	}
	for name, mutate := range map[string]func(*PutModelInput){
		"connect too low":    func(in *PutModelInput) { in.ConnectTimeoutMS = 99 },
		"headers too high":   func(in *PutModelInput) { in.ResponseHeaderTimeoutMS = 120001 },
		"idle too low":       func(in *PutModelInput) { in.IdleStreamTimeoutMS = 999 },
		"total below header": func(in *PutModelInput) { in.TotalTimeoutMS = 999 },
		"total too high":     func(in *PutModelInput) { in.TotalTimeoutMS = 600001 },
	} {
		t.Run(name, func(t *testing.T) {
			in := valid
			mutate(&in)
			if _, err := svc.PutModel(context.Background(), actor, in); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
