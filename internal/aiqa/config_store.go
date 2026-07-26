package aiqa

import "context"
import "github.com/google/uuid"

// ConfigStore is deliberately policy-free.  The service owns authorization and
// input normalization; implementations own locking and atomic persistence.
type ConfigStore interface {
	ListProviders(context.Context) ([]ProviderView, error)
	CreateProvider(context.Context, Principal, CreateProviderInput, EncryptedSecret) (ProviderView, error)
	UpdateProvider(context.Context, Principal, UpdateProviderInput, *EncryptedSecret) (ProviderView, error)
	ActivateProvider(context.Context, Principal, uuid.UUID, int64) (ProviderView, error)
	ListModels(context.Context, uuid.UUID) ([]ModelView, error)
	PutModel(context.Context, Principal, PutModelInput) (ModelView, error)
	ListPrompts(context.Context) ([]PromptView, error)
	PutPrompt(context.Context, Principal, PutPromptInput) (PromptView, error)
	GetLimits(context.Context) (LimitViews, error)
	PutGlobalLimits(context.Context, Principal, PutLimitsInput) (LimitView, error)
	PutStudentLimits(context.Context, Principal, uuid.UUID, PutLimitsInput) (LimitView, error)
}
