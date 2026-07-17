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

var ErrUnauthenticated = errors.New("unauthenticated")

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
	Users       UserStore
	Sessions    SessionStore
	LoginEvents LoginEventStore
	Hasher      PasswordHasher
	Now         func() time.Time
}

type Service struct {
	users       UserStore
	sessions    SessionStore
	loginEvents LoginEventStore
	hasher      PasswordHasher
	dummyHash   string
	now         func() time.Time
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

func NewService(config ServiceConfig) *Service {
	hasher := config.Hasher
	if hasher.params == (Argon2Params{}) {
		hasher = NewPasswordHasher(Argon2Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32})
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	dummyHash, _ := hasher.hashWithSalt("HappyLearn invalid login dummy password", []byte("0123456789abcdef"))
	return &Service{users: config.Users, sessions: config.Sessions, loginEvents: config.LoginEvents, hasher: hasher, dummyHash: dummyHash, now: now}
}

func (s *Service) Login(ctx context.Context, input LoginInput) (Authentication, string, error) {
	username := normalizeUsername(input.Username)
	user, err := s.users.FindByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			_ = s.hasher.Compare(s.dummyHash, input.Password)
			return Authentication{}, "", s.failedLogin(ctx, nil, username, "unknown_user", input)
		}
		return Authentication{}, "", ErrInvalidCredentials
	}
	if err := s.hasher.Compare(user.PasswordHash, input.Password); err != nil {
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
		return Authentication{}, "", err
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
	if err != nil {
		return Authentication{}, ErrUnauthenticated
	}
	if user.Status != StatusActive {
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
	if err := s.hasher.Compare(current.User.PasswordHash, input.CurrentPassword); err != nil {
		return Authentication{}, "", ErrInvalidCredentials
	}
	if err := ValidatePassword(input.NewPassword); err != nil {
		return Authentication{}, "", err
	}
	newHash, err := s.hasher.Hash(input.NewPassword)
	if err != nil {
		return Authentication{}, "", err
	}
	if err := s.users.UpdatePassword(ctx, current.User.ID, newHash, false); err != nil {
		return Authentication{}, "", err
	}
	if err := s.sessions.RevokeAllForUser(ctx, current.User.ID, "password changed"); err != nil {
		return Authentication{}, "", err
	}
	current.User.PasswordHash = newHash
	current.User.MustChangePassword = false
	return s.createSession(ctx, current.User, input.IP, input.UserAgent)
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
	return s.sessions.RevokeAllExceptForUser(ctx, current.User.ID, current.Session.ID, "logout others")
}

func (s *Service) createSession(ctx context.Context, user User, ip net.IP, userAgent string) (Authentication, string, error) {
	raw, hash, err := NewSessionToken()
	if err != nil {
		return Authentication{}, "", err
	}
	now := s.now().UTC()
	session := Session{ID: uuid.New(), UserID: user.ID, TokenHash: hash, UserAgent: userAgent, IP: ip, CreatedAt: now, LastSeenAt: now, IdleExpiresAt: now.Add(sessionIdleTTL), AbsoluteExpiresAt: now.Add(sessionAbsoluteTTL)}
	if err := s.sessions.Create(ctx, CreateSessionParams{ID: session.ID, UserID: session.UserID, TokenHash: session.TokenHash, UserAgent: session.UserAgent, IP: session.IP, CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, IdleExpiresAt: session.IdleExpiresAt, AbsoluteExpiresAt: session.AbsoluteExpiresAt}); err != nil {
		return Authentication{}, "", err
	}
	return Authentication{User: user, Session: session}, raw, nil
}

func (s *Service) failedLogin(ctx context.Context, userID *uuid.UUID, username, reason string, input LoginInput) error {
	if err := s.recordLoginEvent(ctx, userID, username, false, reason, input.IP, input.UserAgent); err != nil {
		return ErrInvalidCredentials
	}
	return ErrInvalidCredentials
}

func (s *Service) recordLoginEvent(ctx context.Context, userID *uuid.UUID, username string, success bool, reason string, ip net.IP, userAgent string) error {
	if s.loginEvents == nil {
		return nil
	}
	return s.loginEvents.RecordLoginEvent(ctx, LoginEvent{UserID: userID, Username: username, Success: success, Reason: reason, IP: ip, UserAgent: userAgent, OccurredAt: s.now().UTC()})
}
