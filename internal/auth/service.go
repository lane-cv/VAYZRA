package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"time"

	"github.com/google/uuid"
)

const (
	sessionIdleTTL     = 7 * 24 * time.Hour
	sessionAbsoluteTTL = 30 * 24 * time.Hour
	sessionTouchPeriod = 5 * time.Minute
)

var (
	ErrUnauthenticated      = errors.New("unauthenticated")
	ErrInvalidConfiguration = errors.New("invalid auth service configuration")
)

type LoginEvent struct {
	UserID     *uuid.UUID
	Username   string
	Success    bool
	Reason     string
	IP         net.IP
	UserAgent  string
	OccurredAt time.Time
}

type LoginEventStore interface {
	RecordLoginEvent(context.Context, LoginEvent) error
}

type ServiceConfig struct {
	Users             UserStore
	Sessions          SessionStore
	LoginEvents       LoginEventStore
	PasswordRotations PasswordRotationStore
	Hasher            PasswordHasher
	Now               func() time.Time
}

type Service struct {
	users             UserStore
	sessions          SessionStore
	loginEvents       LoginEventStore
	passwordRotations PasswordRotationStore
	hasher            PasswordHasher
	dummyHash         string
	compare           func(string, string) error
	now               func() time.Time
}

type LoginInput struct {
	Username  string
	Password  string
	IP        net.IP
	UserAgent string
}

type ChangePasswordInput struct {
	SessionToken    string
	CurrentPassword string
	NewPassword     string
	IP              net.IP
	UserAgent       string
}

type Authentication struct {
	User    User
	Session Session
}

func NewService(config ServiceConfig) (*Service, error) {
	if config.Users == nil || config.Sessions == nil || config.LoginEvents == nil || config.PasswordRotations == nil {
		return nil, ErrInvalidConfiguration
	}
	hasher := config.Hasher
	if hasher.params == (Argon2Params{}) {
		hasher = NewPasswordHasher(Argon2Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32})
	}
	if err := validateArgon2Params(hasher.params); err != nil {
		return nil, ErrInvalidConfiguration
	}
	dummySalt := make([]byte, hasher.params.SaltLength)
	for i := range dummySalt {
		dummySalt[i] = byte(i)
	}
	dummyHash, err := hasher.hashWithSalt("HappyLearn invalid login dummy password", dummySalt)
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Service{users: config.Users, sessions: config.Sessions, loginEvents: config.LoginEvents, passwordRotations: config.PasswordRotations, hasher: hasher, dummyHash: dummyHash, compare: hasher.Compare, now: now}, nil
}

func (s *Service) Login(ctx context.Context, input LoginInput) (Authentication, string, error) {
	username := normalizeUsername(input.Username)
	user, err := s.users.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			_ = s.compare(s.dummyHash, input.Password)
			return Authentication{}, "", s.failedLogin(ctx, nil, username, "unknown_user", input)
		}
		return Authentication{}, "", ErrInvalidCredentials
	}
	if err := s.compare(user.PasswordHash, input.Password); err != nil {
		return Authentication{}, "", s.failedLogin(ctx, &user.ID, username, "invalid_password", input)
	}
	if user.Status != StatusActive {
		return Authentication{}, "", s.failedLogin(ctx, &user.ID, username, "disabled", input)
	}
	result, raw, err := s.createSession(ctx, user, input.IP, input.UserAgent)
	if err != nil {
		return Authentication{}, "", err
	}
	if err := s.recordLoginEvent(ctx, &user.ID, username, true, "success", input.IP, input.UserAgent); err != nil {
		_ = s.sessions.Revoke(ctx, result.Session.ID, "login event failed")
		return Authentication{}, "", ErrInvalidCredentials
	}
	return result, raw, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (Authentication, error) {
	if rawToken == "" {
		return Authentication{}, ErrUnauthenticated
	}
	now := s.now().UTC()
	hash := sha256.Sum256([]byte(rawToken))
	session, user, err := s.sessions.FindActiveByTokenHash(ctx, hash, now)
	if err != nil || user.Status != StatusActive {
		return Authentication{}, ErrUnauthenticated
	}
	if now.Sub(session.LastSeenAt) >= sessionTouchPeriod {
		idleExpiresAt := now.Add(sessionIdleTTL)
		if idleExpiresAt.After(session.AbsoluteExpiresAt) {
			idleExpiresAt = session.AbsoluteExpiresAt
		}
		if err := s.sessions.Touch(ctx, session.ID, now, idleExpiresAt); err != nil {
			return Authentication{}, ErrUnauthenticated
		}
		session.LastSeenAt = now
		session.IdleExpiresAt = idleExpiresAt
	}
	return Authentication{User: user, Session: session}, nil
}

func (s *Service) ChangePassword(ctx context.Context, input ChangePasswordInput) (Authentication, string, error) {
	current, err := s.Authenticate(ctx, input.SessionToken)
	if err != nil {
		return Authentication{}, "", err
	}
	if err := s.compare(current.User.PasswordHash, input.CurrentPassword); err != nil {
		return Authentication{}, "", ErrInvalidCredentials
	}
	if err := ValidatePassword(input.NewPassword); err != nil {
		return Authentication{}, "", err
	}
	newHash, err := s.hasher.Hash(input.NewPassword)
	if err != nil {
		return Authentication{}, "", err
	}
	session, raw, err := s.newSession(current.User, input.IP, input.UserAgent)
	if err != nil {
		return Authentication{}, "", err
	}
	if err := s.passwordRotations.RotatePassword(ctx, PasswordRotationParams{UserID: current.User.ID, ExpectedPasswordHash: current.User.PasswordHash, PasswordHash: newHash, MustChangePassword: false, ReplacementSession: createSessionParams(session)}); err != nil {
		return Authentication{}, "", ErrInvalidCredentials
	}
	current.User.PasswordHash = newHash
	current.User.MustChangePassword = false
	return Authentication{User: current.User, Session: session}, raw, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	current, err := s.Authenticate(ctx, rawToken)
	if err != nil {
		return err
	}
	if err := s.sessions.Revoke(ctx, current.Session.ID, "logout"); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

func (s *Service) LogoutOthers(ctx context.Context, rawToken string) error {
	current, err := s.Authenticate(ctx, rawToken)
	if err != nil {
		return err
	}
	if err := s.sessions.RevokeAllExceptForUser(ctx, current.User.ID, current.Session.ID, s.now().UTC(), "logout others"); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrUnauthenticated
		}
		return err
	}
	return nil
}

func (s *Service) createSession(ctx context.Context, user User, ip net.IP, userAgent string) (Authentication, string, error) {
	session, raw, err := s.newSession(user, ip, userAgent)
	if err != nil {
		return Authentication{}, "", err
	}
	if err := s.sessions.Create(ctx, createSessionParams(session)); err != nil {
		return Authentication{}, "", err
	}
	return Authentication{User: user, Session: session}, raw, nil
}

func (s *Service) newSession(user User, ip net.IP, userAgent string) (Session, string, error) {
	raw, hash, err := NewSessionToken()
	if err != nil {
		return Session{}, "", err
	}
	now := s.now().UTC()
	return Session{ID: uuid.New(), UserID: user.ID, TokenHash: hash, UserAgent: userAgent, IP: ip, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(sessionIdleTTL), AbsoluteExpiresAt: now.Add(sessionAbsoluteTTL)}, raw, nil
}

func createSessionParams(session Session) CreateSessionParams {
	return CreateSessionParams{ID: session.ID, UserID: session.UserID, TokenHash: session.TokenHash, UserAgent: session.UserAgent, IP: session.IP, CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, IdleExpiresAt: session.IdleExpiresAt, AbsoluteExpiresAt: session.AbsoluteExpiresAt}
}

func (s *Service) failedLogin(ctx context.Context, userID *uuid.UUID, username, reason string, input LoginInput) error {
	_ = s.recordLoginEvent(ctx, userID, username, false, reason, input.IP, input.UserAgent)
	return ErrInvalidCredentials
}

func (s *Service) recordLoginEvent(ctx context.Context, userID *uuid.UUID, username string, success bool, reason string, ip net.IP, userAgent string) error {
	return s.loginEvents.RecordLoginEvent(ctx, LoginEvent{UserID: userID, Username: username, Success: success, Reason: reason, IP: ip, UserAgent: userAgent, OccurredAt: s.now().UTC()})
}
