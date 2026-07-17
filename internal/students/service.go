package students

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

var (
	ErrForbidden    = errors.New("student administration forbidden")
	ErrInvalidInput = errors.New("invalid student input")
)

type Principal struct {
	User      auth.User
	RequestID string
	IP        net.IP
}
type CreateInput struct {
	Username          string
	DisplayName       string
	TemporaryPassword string
}
type UserStore interface {
	FindByID(context.Context, uuid.UUID) (auth.User, error)
	Create(context.Context, auth.CreateUserParams) (auth.User, error)
	UpdatePassword(context.Context, uuid.UUID, string, bool) error
	SetStatus(context.Context, uuid.UUID, auth.Status) error
}
type SessionStore interface {
	RevokeAllForUser(context.Context, uuid.UUID, string) error
}
type UnitOfWork interface {
	WithinTx(context.Context, func(UserStore, SessionStore, audit.Writer) error) error
}
type HTTPService interface {
	List(context.Context, Principal, int, uuid.UUID) ([]auth.User, uuid.UUID, error)
	Create(context.Context, Principal, CreateInput) (auth.User, error)
	SetStatus(context.Context, Principal, uuid.UUID, auth.Status) error
	ResetPassword(context.Context, Principal, uuid.UUID, string) error
}
type Service struct {
	listUsers auth.UserStore
	uow       UnitOfWork
	hasher    auth.PasswordHasher
	now       func() time.Time
}

func NewService(listUsers auth.UserStore, uow UnitOfWork, hasher auth.PasswordHasher, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{listUsers: listUsers, uow: uow, hasher: hasher, now: now}
}
func (s *Service) List(ctx context.Context, actor Principal, limit int, after uuid.UUID) ([]auth.User, uuid.UUID, error) {
	if err := authorize(actor); err != nil {
		return nil, uuid.Nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, uuid.Nil, ErrInvalidInput
	}
	users, err := s.listUsers.ListStudents(ctx, limit, after)
	if err != nil {
		return nil, uuid.Nil, err
	}
	var next uuid.UUID
	if len(users) == limit {
		next = users[len(users)-1].ID
	}
	return users, next, nil
}
func (s *Service) Create(ctx context.Context, actor Principal, input CreateInput) (auth.User, error) {
	if err := authorize(actor); err != nil {
		return auth.User{}, err
	}
	input.Username = normalizeUsername(input.Username)
	if !validUsername(input.Username) || !validDisplayName(input.DisplayName) || auth.ValidatePassword(input.TemporaryPassword) != nil {
		return auth.User{}, ErrInvalidInput
	}
	hash, err := s.hasher.Hash(input.TemporaryPassword)
	if err != nil {
		return auth.User{}, err
	}
	var student auth.User
	err = s.uow.WithinTx(ctx, func(users UserStore, _ SessionStore, writer audit.Writer) error {
		student, err = users.Create(ctx, auth.CreateUserParams{Username: input.Username, DisplayName: input.DisplayName, Role: auth.RoleStudent, Status: auth.StatusActive, PasswordHash: hash, MustChangePassword: true})
		if err != nil {
			return err
		}
		return writer.Write(ctx, event(actor, "student.created", student.ID, map[string]any{"username": student.Username, "display_name": student.DisplayName}))
	})
	if err != nil {
		return auth.User{}, err
	}
	return student, nil
}
func (s *Service) SetStatus(ctx context.Context, actor Principal, studentID uuid.UUID, status auth.Status) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if studentID == uuid.Nil || (status != auth.StatusActive && status != auth.StatusDisabled) {
		return ErrInvalidInput
	}
	return s.uow.WithinTx(ctx, func(users UserStore, sessions SessionStore, writer audit.Writer) error {
		student, err := targetStudent(ctx, users, studentID)
		if err != nil {
			return err
		}
		if err := users.SetStatus(ctx, student.ID, status); err != nil {
			return err
		}
		action, metadata := "student.enabled", map[string]any{"status": string(status)}
		if status == auth.StatusDisabled {
			if err := sessions.RevokeAllForUser(ctx, student.ID, "student disabled"); err != nil {
				return err
			}
			action = "student.disabled"
		}
		return writer.Write(ctx, event(actor, action, student.ID, metadata))
	})
}
func (s *Service) ResetPassword(ctx context.Context, actor Principal, studentID uuid.UUID, temporaryPassword string) error {
	if err := authorize(actor); err != nil {
		return err
	}
	if studentID == uuid.Nil || auth.ValidatePassword(temporaryPassword) != nil {
		return ErrInvalidInput
	}
	hash, err := s.hasher.Hash(temporaryPassword)
	if err != nil {
		return err
	}
	return s.uow.WithinTx(ctx, func(users UserStore, sessions SessionStore, writer audit.Writer) error {
		student, err := targetStudent(ctx, users, studentID)
		if err != nil {
			return err
		}
		if err := users.UpdatePassword(ctx, student.ID, hash, true); err != nil {
			return err
		}
		if err := sessions.RevokeAllForUser(ctx, student.ID, "password reset"); err != nil {
			return err
		}
		return writer.Write(ctx, event(actor, "student.password_reset", student.ID, map[string]any{}))
	})
}
func authorize(actor Principal) error {
	if actor.User.Role != auth.RoleAdmin || actor.User.ID == uuid.Nil || actor.User.Status != auth.StatusActive || strings.TrimSpace(actor.RequestID) == "" || actor.IP == nil {
		return ErrForbidden
	}
	return nil
}
func targetStudent(ctx context.Context, users UserStore, id uuid.UUID) (auth.User, error) {
	student, err := users.FindByID(ctx, id)
	if err != nil {
		return auth.User{}, err
	}
	if student.Role != auth.RoleStudent {
		return auth.User{}, ErrForbidden
	}
	return student, nil
}
func event(actor Principal, action string, target uuid.UUID, metadata map[string]any) audit.Event {
	return audit.Event{ActorUserID: actor.User.ID, Action: action, TargetType: "student", TargetID: target.String(), Metadata: metadata, RequestID: actor.RequestID, IP: append(net.IP(nil), actor.IP...)}
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9._-]{3,64}$`)

func normalizeUsername(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func validUsername(value string) bool       { return usernamePattern.MatchString(value) }
func validDisplayName(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= 1 && utf8.RuneCountInString(value) <= 64 && strings.TrimSpace(value) != ""
}
