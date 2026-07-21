package files

import (
	"errors"
	"testing"

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
