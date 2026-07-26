package aiqa

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
	"strings"
	"time"
)

type AdminConfigService interface {
	ListProviders(context.Context, Principal) ([]ProviderView, error)
	CreateProvider(context.Context, Principal, CreateProviderInput) (ProviderView, error)
	UpdateProvider(context.Context, Principal, UpdateProviderInput) (ProviderView, error)
	ActivateProvider(context.Context, Principal, uuid.UUID, int64) (ProviderView, error)
	ListModels(context.Context, Principal, uuid.UUID) ([]ModelView, error)
	PutModel(context.Context, Principal, PutModelInput) (ModelView, error)
	ListPrompts(context.Context, Principal) ([]PromptView, error)
	PutPrompt(context.Context, Principal, PutPromptInput) (PromptView, error)
	GetLimits(context.Context, Principal) (LimitViews, error)
	PutGlobalLimits(context.Context, Principal, PutLimitsInput) (LimitView, error)
	PutStudentLimits(context.Context, Principal, uuid.UUID, PutLimitsInput) (LimitView, error)
	TestProvider(context.Context, Principal, uuid.UUID) (ConnectivityResult, error)
}

type providerTestAudit struct {
	providerID uuid.UUID
	protocol   ProtocolMode
	ok         bool
	category   string
	latencyMS  int64
}

type providerConnectivityStore interface {
	AcquireProviderTest(context.Context, uuid.UUID) (RuntimeProviderConfig, func(), error)
	RecordProviderTest(context.Context, Principal, providerTestAudit) error
}

const providerTestAuditTimeout = 2 * time.Second

type configService struct {
	store        ConfigStore
	policy       URLPolicy
	box          SecretBox
	connectivity providerConnectivityStore
	tester       ConnectivityTester
}

func NewAdminConfigService(s ConfigStore, p URLPolicy, b SecretBox) AdminConfigService {
	return NewAdminConfigServiceWithConnectivity(s, p, b, NewProviderConnectivityTester(p))
}

func NewAdminConfigServiceWithConnectivity(s ConfigStore, p URLPolicy, b SecretBox, tester ConnectivityTester) AdminConfigService {
	connectivity, _ := s.(providerConnectivityStore)
	return &configService{store: s, policy: p, box: b, connectivity: connectivity, tester: tester}
}
func admin(p Principal) error {
	if p.User.ID == uuid.Nil || p.User.Role != auth.RoleAdmin || p.User.Status != auth.StatusActive {
		return ErrForbidden
	}
	return nil
}
func (s *configService) ListProviders(c context.Context, p Principal) ([]ProviderView, error) {
	if e := admin(p); e != nil {
		return nil, e
	}
	return s.store.ListProviders(c)
}
func (s *configService) CreateProvider(c context.Context, p Principal, in CreateProviderInput) (ProviderView, error) {
	if e := admin(p); e != nil {
		return ProviderView{}, e
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || in.APIKey == "" || len(in.IdempotencyKey) < 16 || (in.ProtocolMode != ProtocolChatCompletions && in.ProtocolMode != ProtocolResponses) {
		return ProviderView{}, ErrInvalidInput
	}
	u, e := s.policy.NormalizeBaseURL(c, in.BaseURL)
	if e != nil {
		return ProviderView{}, ErrInvalidInput
	}
	in.BaseURL = canonicalBaseURL(u.String())
	id := uuid.New()
	sec, e := s.box.Seal(id, []byte(in.APIKey))
	if e != nil {
		return ProviderView{}, e
	}
	hash := sha256.Sum256([]byte(in.Name + "\x00" + in.BaseURL + "\x00" + string(in.ProtocolMode)))
	v, e := s.store.CreateProvider(c, p, id, in, sec, hash)
	if v.ID == uuid.Nil {
		v.ID = id
	}
	return v, e
}
func (s *configService) UpdateProvider(c context.Context, p Principal, in UpdateProviderInput) (ProviderView, error) {
	if e := admin(p); e != nil {
		return ProviderView{}, e
	}
	if in.ID == uuid.Nil || in.ExpectedVersion < 1 {
		return ProviderView{}, ErrInvalidInput
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || !protocolOK(in.ProtocolMode) {
		return ProviderView{}, ErrInvalidInput
	}
	u, e := s.policy.NormalizeBaseURL(c, in.BaseURL)
	if e != nil {
		return ProviderView{}, ErrInvalidInput
	}
	in.BaseURL = canonicalBaseURL(u.String())
	var sec *EncryptedSecret
	if in.APIKey != nil {
		if *in.APIKey == "" {
			return ProviderView{}, ErrInvalidInput
		}
		x, e := s.box.Seal(in.ID, []byte(*in.APIKey))
		if e != nil {
			return ProviderView{}, e
		}
		sec = &x
	}
	return s.store.UpdateProvider(c, p, in, sec)
}
func (s *configService) ActivateProvider(c context.Context, p Principal, id uuid.UUID, v int64) (ProviderView, error) {
	if e := admin(p); e != nil {
		return ProviderView{}, e
	}
	if id == uuid.Nil || v < 1 {
		return ProviderView{}, ErrInvalidInput
	}
	return s.store.ActivateProvider(c, p, id, v)
}
func (s *configService) ListModels(c context.Context, p Principal, id uuid.UUID) ([]ModelView, error) {
	if e := admin(p); e != nil {
		return nil, e
	}
	if id == uuid.Nil {
		return nil, ErrInvalidInput
	}
	return s.store.ListModels(c, id)
}
func (s *configService) PutModel(c context.Context, p Principal, in PutModelInput) (ModelView, error) {
	if e := admin(p); e != nil {
		return ModelView{}, e
	}
	if in.ProviderID == uuid.Nil || in.ID == uuid.Nil || in.ExpectedVersion < 0 || strings.TrimSpace(in.UpstreamModelID) == "" || !modalityOK(in.Modality) || in.ContextTokens < 1 || in.MaxOutputTokens < 1 || in.MaxOutputTokens > in.ContextTokens || in.ImageQuotaTokens < 1 || in.InputPriceMicroUSD < 0 || in.OutputPriceMicroUSD < 0 {
		return ModelView{}, ErrInvalidInput
	}
	in.UpstreamModelID = strings.TrimSpace(in.UpstreamModelID)
	return s.store.PutModel(c, p, in)
}
func (s *configService) ListPrompts(c context.Context, p Principal) ([]PromptView, error) {
	if e := admin(p); e != nil {
		return nil, e
	}
	return s.store.ListPrompts(c)
}
func (s *configService) PutPrompt(c context.Context, p Principal, in PutPromptInput) (PromptView, error) {
	if e := admin(p); e != nil {
		return PromptView{}, e
	}
	in.Body = strings.TrimSpace(in.Body)
	if !subjectOK(in.Subject) || in.ExpectedVersion < 0 || in.Body == "" || len(in.Body) > 100000 {
		return PromptView{}, ErrInvalidInput
	}
	return s.store.PutPrompt(c, p, in)
}
func (s *configService) GetLimits(c context.Context, p Principal) (LimitViews, error) {
	if e := admin(p); e != nil {
		return LimitViews{}, e
	}
	return s.store.GetLimits(c)
}
func (s *configService) PutGlobalLimits(c context.Context, p Principal, in PutLimitsInput) (LimitView, error) {
	if e := admin(p); e != nil {
		return LimitView{}, e
	}
	if in.ExpectedVersion < 1 || !globalLimitsOK(in) {
		return LimitView{}, ErrInvalidInput
	}
	return s.store.PutGlobalLimits(c, p, in)
}
func (s *configService) PutStudentLimits(c context.Context, p Principal, id uuid.UUID, in PutLimitsInput) (LimitView, error) {
	if e := admin(p); e != nil {
		return LimitView{}, e
	}
	if id == uuid.Nil || in.ExpectedVersion < 0 || !limitsOK(in) {
		return LimitView{}, ErrInvalidInput
	}
	return s.store.PutStudentLimits(c, p, id, in)
}

func (s *configService) TestProvider(ctx context.Context, p Principal, id uuid.UUID) (ConnectivityResult, error) {
	if err := admin(p); err != nil {
		return ConnectivityResult{}, err
	}
	if id == uuid.Nil {
		return ConnectivityResult{}, ErrInvalidInput
	}
	if s.connectivity == nil || s.tester == nil {
		return ConnectivityResult{}, ErrProviderUnavailable
	}

	cfg, release, err := s.connectivity.AcquireProviderTest(ctx, id)
	if errors.Is(err, ErrProviderTestBusy) {
		result := ConnectivityResult{Protocol: cfg.ProtocolMode, ErrorCategory: "busy"}
		auditErr := s.recordProviderTest(p, providerTestAudit{
			providerID: id, protocol: result.Protocol, category: result.ErrorCategory,
		})
		if auditErr != nil {
			return ConnectivityResult{}, auditErr
		}
		return result, ErrProviderTestBusy
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) || cfg.ProviderID != id || !protocolOK(cfg.ProtocolMode) {
			return ConnectivityResult{}, err
		}
		result := ConnectivityResult{Protocol: cfg.ProtocolMode, ErrorCategory: "unavailable"}
		auditErr := s.recordProviderTest(p, providerTestAudit{
			providerID: id, protocol: result.Protocol, category: result.ErrorCategory,
		})
		if auditErr != nil {
			return ConnectivityResult{}, auditErr
		}
		return result, ErrProviderUnavailable
	}
	defer release()

	result, testErr := s.tester.Test(ctx, cfg)
	release()
	result.Protocol = cfg.ProtocolMode
	if testErr != nil {
		result.OK = false
		if result.ErrorCategory == "" {
			result.ErrorCategory = "unavailable"
		}
	}
	auditErr := s.recordProviderTest(p, providerTestAudit{
		providerID: id, protocol: result.Protocol, ok: result.OK,
		category: result.ErrorCategory, latencyMS: result.LatencyMS,
	})
	if auditErr != nil {
		return ConnectivityResult{}, auditErr
	}
	if testErr != nil || !result.OK {
		return result, ErrProviderUnavailable
	}
	return result, nil
}

func (s *configService) recordProviderTest(p Principal, result providerTestAudit) error {
	auditCtx, cancel := context.WithTimeout(context.Background(), providerTestAuditTimeout)
	defer cancel()
	return s.connectivity.RecordProviderTest(auditCtx, p, result)
}
func protocolOK(v ProtocolMode) bool     { return v == ProtocolChatCompletions || v == ProtocolResponses }
func modalityOK(v Modality) bool         { return v == ModalityText || v == ModalityVision }
func subjectOK(v Subject) bool           { return v == SubjectMath || v == SubjectPhysics }
func canonicalBaseURL(raw string) string { return strings.TrimSuffix(raw, "/") }
func limitsOK(in PutLimitsInput) bool {
	for _, v := range []LimitValue{in.DailyRequests, in.MonthlyRequests, in.DailyTokens, in.MonthlyTokens} {
		switch v.Mode {
		case "inherit":
			if v.Value != nil {
				return false
			}
		case "disabled":
			if v.Value != nil && *v.Value != 0 {
				return false
			}
		case "limit":
			if v.Value == nil || *v.Value < 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func globalLimitsOK(in PutLimitsInput) bool {
	for _, v := range []LimitValue{in.DailyRequests, in.MonthlyRequests, in.DailyTokens, in.MonthlyTokens} {
		if v.Mode == "inherit" || !limitValueOK(v) {
			return false
		}
	}
	return true
}
func limitValueOK(v LimitValue) bool {
	switch v.Mode {
	case "inherit":
		return v.Value == nil
	case "disabled":
		return v.Value == nil || *v.Value == 0
	case "limit":
		return v.Value != nil && *v.Value > 0
	}
	return false
}
