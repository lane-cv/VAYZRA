package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestArgon2idRoundTrip(t *testing.T) {
	h := NewPasswordHasher(Argon2Params{MemoryKiB: 64 * 1024, Iterations: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32})
	encoded, err := h.Hash("Correct Horse Battery Staple 42!")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
		t.Fatalf("hash=%q", encoded)
	}
	if err := h.Compare(encoded, "Correct Horse Battery Staple 42!"); err != nil {
		t.Fatal(err)
	}
	if err := h.Compare(encoded, "wrong password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("err=%v", err)
	}
}

func TestPasswordHasherRejectsMalformedOrNonArgon2idPHC(t *testing.T) {
	h := NewPasswordHasher(Argon2Params{MemoryKiB: 64 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	for _, encoded := range []string{
		"", "$argon2id$v=18$m=65536,t=1,p=1$c2FsdA$YQ", "$argon2i$v=19$m=65536,t=1,p=1$c2FsdA$YQ",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$YQ", "$argon2id$v=19$m=65536,t=0,p=1$c2FsdA$YQ",
		"$argon2id$v=19$m=65536,t=1,p=0$c2FsdA$YQ", "$argon2id$v=19$m=65536,t=1,p=1$c2FsdA$",
		"$argon2id$v=19$m=65536,t=1,p=1,extra=1$c2FsdA$YQ", "$argon2id$v=19$m=65536,t=1,p=1$c2FsdA$YQ$extra",
	} {
		if err := h.Compare(encoded, "Correct Horse Battery Staple 42!"); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("Compare(%q) error=%v", encoded, err)
		}
	}
}

func TestPasswordPolicyUsesUnicodeCodePointsAndRejectsEdgeWhitespace(t *testing.T) {
	for _, password := range []string{"short", strings.Repeat("a", 129), " leading space password", "trailing space password ", strings.Repeat("密", 11)} {
		if err := ValidatePassword(password); err == nil {
			t.Fatalf("accepted %q", password)
		}
	}
	if err := ValidatePassword(strings.Repeat("密", 12)); err != nil {
		t.Fatalf("rejected twelve Unicode code points: %v", err)
	}
}
