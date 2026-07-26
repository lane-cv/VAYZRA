package files

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"happylearn.local/app/internal/auth"
)

func TestQAUploadPolicyExactBoundariesAndRoles(t *testing.T) {
	policy := QAUploadPolicy{}
	active := func(role auth.Role) auth.User {
		return auth.User{ID: uuid.New(), Role: role, Status: auth.StatusActive}
	}
	if err := policy.Authorize(active(auth.RoleStudent)); err != nil {
		t.Fatalf("student: %v", err)
	}
	if err := policy.Authorize(active(auth.RoleAdmin)); err != nil {
		t.Fatalf("admin: %v", err)
	}
	disabled := active(auth.RoleStudent)
	disabled.Status = auth.StatusDisabled
	if !errors.Is(policy.Authorize(disabled), ErrForbidden) {
		t.Fatal("disabled user must be forbidden")
	}

	tests := []struct {
		name, mime string
		size       int64
		want       error
	}{
		{"a.png", "image/png", 20 << 20, nil},
		{"a.png", "image/png", (20 << 20) + 1, ErrFileTooLarge},
		{"a.pdf", "application/pdf", 50 << 20, nil},
		{"a.pdf", "application/pdf", (50 << 20) + 1, ErrFileTooLarge},
		{"a.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", 30 << 20, nil},
		{"a.docm", "application/vnd.ms-word.document.macroEnabled.12", 1024, ErrFileTypeRejected},
		{"a.zip", "application/zip", 1024, ErrFileTypeRejected},
		{"a.svg", "image/svg+xml", 1024, ErrFileTypeRejected},
		{"a.html", "text/html", 1024, ErrFileTypeRejected},
		{"a.exe", "application/octet-stream", 1024, ErrFileTypeRejected},
	}
	for _, tc := range tests {
		in := CreateUploadInput{DisplayName: tc.name, DeclaredMIME: tc.mime, ExpectedSize: tc.size, ExpectedSHA256: digestOf([]byte("x"))}
		if err := policy.Validate(in); !errors.Is(err, tc.want) {
			t.Errorf("%s/%s/%d err=%v want=%v", tc.name, tc.mime, tc.size, err, tc.want)
		}
	}
}

func TestTeachingUploadPolicyPreservesAdminOnlyBehavior(t *testing.T) {
	policy := TeachingUploadPolicy{}
	admin := auth.User{ID: uuid.New(), Role: auth.RoleAdmin, Status: auth.StatusActive}
	student := auth.User{ID: uuid.New(), Role: auth.RoleStudent, Status: auth.StatusActive}
	if err := policy.Authorize(admin); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(policy.Authorize(student), ErrForbidden) {
		t.Fatal("student must be forbidden")
	}
	if err := policy.Validate(CreateUploadInput{DisplayName: "lesson.mp4", DeclaredMIME: "video/mp4", ExpectedSize: MaxUploadSize, ExpectedSHA256: digestOf([]byte("x"))}); err != nil {
		t.Fatalf("teaching behavior regressed: %v", err)
	}
}

func TestAIUploadPolicyStudentOnlyExactBoundariesAndPurpose(t *testing.T) {
	policy := AIUploadPolicy{}
	active := func(role auth.Role) auth.User {
		return auth.User{ID: uuid.New(), Role: role, Status: auth.StatusActive}
	}
	if policy.Purpose() != UploadPurposeAI {
		t.Fatalf("purpose=%q", policy.Purpose())
	}
	if err := policy.Authorize(active(auth.RoleStudent)); err != nil {
		t.Fatalf("student: %v", err)
	}
	if !errors.Is(policy.Authorize(active(auth.RoleAdmin)), ErrForbidden) {
		t.Fatal("admin must be forbidden")
	}
	disabled := active(auth.RoleStudent)
	disabled.Status = auth.StatusDisabled
	if !errors.Is(policy.Authorize(disabled), ErrForbidden) {
		t.Fatal("disabled student must be forbidden")
	}

	tests := []struct {
		name, mime string
		size       int64
		want       error
	}{
		{"a.jpg", "image/jpeg", 20 << 20, nil},
		{"a.png", "image/png", 20 << 20, nil},
		{"a.webp", "image/webp", 20 << 20, nil},
		{"a.gif", "image/gif", 20 << 20, nil},
		{"a.pdf", "application/pdf", 50 << 20, nil},
		{"a.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", 30 << 20, nil},
		{"a.txt", "text/plain", 10 << 20, nil},
		{"a.md", "text/markdown", 10 << 20, nil},
		{"a.png", "image/png", (20 << 20) + 1, ErrFileTooLarge},
		{"a.docm", "application/vnd.ms-word.document.macroEnabled.12", 1024, ErrFileTypeRejected},
		{"a.xlsm", "application/vnd.ms-excel.sheet.macroEnabled.12", 1024, ErrFileTypeRejected},
		{"a.zip", "application/zip", 1024, ErrFileTypeRejected},
		{"a.exe", "application/octet-stream", 1024, ErrFileTypeRejected},
		{"a.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", 1024, ErrFileTypeRejected},
	}
	for _, tc := range tests {
		in := CreateUploadInput{DisplayName: tc.name, DeclaredMIME: tc.mime, ExpectedSize: tc.size, ExpectedSHA256: digestOf([]byte("x"))}
		if err := policy.Validate(in); !errors.Is(err, tc.want) {
			t.Errorf("%s/%s/%d err=%v want=%v", tc.name, tc.mime, tc.size, err, tc.want)
		}
	}
}

func TestAIUploadPersistsDedicatedPurpose(t *testing.T) {
	store := newFakeUploadStore()
	objects := newFakeObjects()
	actor := uploadAdmin()
	actor.User.Role = auth.RoleStudent
	svc := NewUploadService(store, objects, AIUploadPolicy{}, func() time.Time {
		return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	})
	view, err := svc.Create(context.Background(), actor, CreateUploadInput{
		DisplayName: "question.txt", DeclaredMIME: "text/plain", ExpectedSize: 1, ExpectedSHA256: digestOf([]byte("x")),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := store.sessions[view.ID]
	if session.Purpose != UploadPurposeAI || session.Purpose == UploadPurposeQA {
		t.Fatalf("persisted purpose=%q", session.Purpose)
	}
}
