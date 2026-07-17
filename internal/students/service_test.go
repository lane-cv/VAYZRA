package students

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/audit"
	"happylearn.local/app/internal/auth"
)

func TestCreateHashesTemporaryPasswordAndAudits(t *testing.T) {
	users := &fakeUsers{}
	audits := &fakeAudit{}
	svc := NewService(users, fakeUOW{users: users, audit: audits}, testHasher(), fixedClock)
	student, err := svc.Create(context.Background(), adminPrincipal(), CreateInput{Username: " Student01 ", DisplayName: "林同学", TemporaryPassword: "Temporary Password 42!"})
	if err != nil {
		t.Fatal(err)
	}
	if student.Role != auth.RoleStudent || !student.MustChangePassword || student.Username != "student01" {
		t.Fatalf("student=%#v", student)
	}
	if users.created.PasswordHash == "Temporary Password 42!" || users.created.PasswordHash == "" {
		t.Fatal("temporary password was not hashed")
	}
	if audits.last.Action != "student.created" || audits.last.RequestID == "" || audits.last.ActorUserID != adminPrincipal().User.ID {
		t.Fatalf("audit=%#v", audits.last)
	}
	if len(audits.last.Metadata) == 0 || containsSecret(audits.last.Metadata) {
		t.Fatalf("unsafe audit metadata=%#v", audits.last.Metadata)
	}
}

func TestStudentMutationsRejectNonAdminAndAdminTarget(t *testing.T) {
	admin := adminPrincipal()
	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	users := &fakeUsers{byID: map[uuid.UUID]auth.User{admin.User.ID: admin.User, student.ID: student}}
	svc := NewService(users, fakeUOW{users: users, audit: &fakeAudit{}}, testHasher(), fixedClock)
	if _, err := svc.Create(context.Background(), Principal{User: student, RequestID: "request-123"}, CreateInput{Username: "student02", DisplayName: "学生", TemporaryPassword: "Temporary Password 42!"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("create err=%v", err)
	}
	if err := svc.SetStatus(context.Background(), admin, admin.User.ID, auth.StatusDisabled); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin target err=%v", err)
	}
}

func TestDisableRevokesSessionsAndResetRestoresFirstLogin(t *testing.T) {
	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive, PasswordHash: "old", MustChangePassword: false}
	users := &fakeUsers{byID: map[uuid.UUID]auth.User{student.ID: student}}
	sessions, audits := &fakeSessions{}, &fakeAudit{}
	svc := NewService(users, fakeUOW{users: users, sessions: sessions, audit: audits}, testHasher(), fixedClock)
	if err := svc.SetStatus(context.Background(), adminPrincipal(), student.ID, auth.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if sessions.revokedFor != student.ID || users.byID[student.ID].Status != auth.StatusDisabled || audits.last.Action != "student.disabled" {
		t.Fatalf("sessions=%#v user=%#v audit=%#v", sessions, users.byID[student.ID], audits.last)
	}
	if err := svc.ResetPassword(context.Background(), adminPrincipal(), student.ID, "Another Temporary Password 42!"); err != nil {
		t.Fatal(err)
	}
	updated := users.byID[student.ID]
	if !updated.MustChangePassword || updated.PasswordHash == "Another Temporary Password 42!" || audits.last.Action != "student.password_reset" {
		t.Fatalf("user=%#v audit=%#v", updated, audits.last)
	}
}

func TestListBoundsLimitAndUsesStudentStore(t *testing.T) {
	users := &fakeUsers{}
	svc := NewService(users, fakeUOW{users: users, audit: &fakeAudit{}}, testHasher(), fixedClock)
	if _, _, err := svc.List(context.Background(), adminPrincipal(), 101, uuid.Nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("limit err=%v", err)
	}
	if _, _, err := svc.List(context.Background(), adminPrincipal(), 0, uuid.Nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("limit err=%v", err)
	}
	users.list = []auth.User{{ID: uuid.New(), Role: auth.RoleStudent}}
	got, next, err := svc.List(context.Background(), adminPrincipal(), 1, uuid.Nil)
	if err != nil || len(got) != 1 || next != got[0].ID {
		t.Fatalf("got=%#v next=%s err=%v", got, next, err)
	}
}

func TestUnitOfWorkRollsBackWhenAuditFails(t *testing.T) {
	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	users := &fakeUsers{byID: map[uuid.UUID]auth.User{student.ID: student}}
	uow := rollbackUOW{users: users, audit: &fakeAudit{err: errors.New("audit insert failed")}}
	svc := NewService(users, uow, testHasher(), fixedClock)
	if err := svc.SetStatus(context.Background(), adminPrincipal(), student.ID, auth.StatusDisabled); err == nil {
		t.Fatal("expected audit failure")
	}
	if users.byID[student.ID].Status != auth.StatusActive {
		t.Fatal("status persisted despite failed audit")
	}
}

func testHasher() auth.PasswordHasher {
	return auth.NewPasswordHasher(auth.Argon2Params{MemoryKiB: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16})
}
func fixedClock() time.Time { return time.Date(2026, 7, 18, 1, 0, 0, 0, time.UTC) }
func adminPrincipal() Principal {
	return Principal{User: auth.User{ID: uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"), Role: auth.RoleAdmin, Status: auth.StatusActive}, RequestID: "request-123", IP: net.ParseIP("192.0.2.4")}
}
func containsSecret(m map[string]any) bool {
	for k, v := range m {
		if k == "password" || k == "passwordHash" || k == "token" || v == "Temporary Password 42!" {
			return true
		}
	}
	return false
}

type fakeUsers struct {
	created auth.CreateUserParams
	byID    map[uuid.UUID]auth.User
	list    []auth.User
}

func (f *fakeUsers) FindByUsername(context.Context, string) (auth.User, error) {
	return auth.User{}, auth.ErrNotFound
}
func (f *fakeUsers) FindByID(_ context.Context, id uuid.UUID) (auth.User, error) {
	u, ok := f.byID[id]
	if !ok {
		return auth.User{}, auth.ErrNotFound
	}
	return u, nil
}
func (f *fakeUsers) Create(_ context.Context, p auth.CreateUserParams) (auth.User, error) {
	f.created = p
	u := auth.User{ID: uuid.New(), Username: p.Username, DisplayName: p.DisplayName, Role: p.Role, Status: p.Status, PasswordHash: p.PasswordHash, MustChangePassword: p.MustChangePassword}
	if f.byID == nil {
		f.byID = map[uuid.UUID]auth.User{}
	}
	f.byID[u.ID] = u
	return u, nil
}
func (f *fakeUsers) UpdatePassword(_ context.Context, id uuid.UUID, hash string, must bool) error {
	u, ok := f.byID[id]
	if !ok {
		return auth.ErrNotFound
	}
	u.PasswordHash, u.MustChangePassword = hash, must
	f.byID[id] = u
	return nil
}
func (f *fakeUsers) SetStatus(_ context.Context, id uuid.UUID, status auth.Status) error {
	u, ok := f.byID[id]
	if !ok {
		return auth.ErrNotFound
	}
	u.Status = status
	f.byID[id] = u
	return nil
}
func (f *fakeUsers) ListStudents(context.Context, int, uuid.UUID) ([]auth.User, error) {
	return f.list, nil
}

type fakeSessions struct{ revokedFor uuid.UUID }

func (f *fakeSessions) RevokeAllForUser(_ context.Context, id uuid.UUID, _ string) error {
	f.revokedFor = id
	return nil
}

type fakeAudit struct {
	last audit.Event
	err  error
}

func (f *fakeAudit) Write(_ context.Context, event audit.Event) error { f.last = event; return f.err }

type fakeUOW struct {
	users    UserStore
	sessions SessionStore
	audit    audit.Writer
}

func (f fakeUOW) WithinTx(ctx context.Context, fn func(UserStore, SessionStore, audit.Writer) error) error {
	return fn(f.users, f.sessions, f.audit)
}

type rollbackUOW struct {
	users *fakeUsers
	audit audit.Writer
}

func (f rollbackUOW) WithinTx(ctx context.Context, fn func(UserStore, SessionStore, audit.Writer) error) error {
	copy := *f.users
	copy.byID = map[uuid.UUID]auth.User{}
	for k, v := range f.users.byID {
		copy.byID[k] = v
	}
	if err := fn(&copy, &fakeSessions{}, f.audit); err != nil {
		return err
	}
	*f.users = copy
	return nil
}
