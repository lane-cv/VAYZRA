package operations

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"happylearn.local/app/internal/auth"
)

type service struct {
	store Store
}

func NewService(store Store) HTTPService {
	return &service{store: store}
}

func (s *service) GetSettings(ctx context.Context, principal Principal) (Settings, error) {
	if err := authorizeSettings(principal); err != nil {
		return Settings{}, err
	}
	return s.store.GetSettings(ctx)
}

func (s *service) UpdateSettings(ctx context.Context, principal Principal, settings Settings) (Settings, error) {
	if err := authorizeSettings(principal); err != nil {
		return Settings{}, err
	}
	if err := ValidateSettings(settings); err != nil {
		return Settings{}, err
	}
	return s.store.UpdateSettings(ctx, principal, settings)
}

func authorizeSettings(principal Principal) error {
	if principal.User.ID == uuid.Nil ||
		principal.User.Role != auth.RoleAdmin ||
		principal.User.Status != auth.StatusActive ||
		strings.TrimSpace(principal.RequestID) == "" ||
		len(principal.RequestID) > 64 ||
		principal.IP == nil || principal.IP.To16() == nil {
		return ErrForbidden
	}
	return nil
}
