package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestSessionTokenHasEntropyAndStableHash(t *testing.T) {
	raw, hash, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("raw token invalid")
	}
	want := sha256.Sum256([]byte(raw))
	if hash != want {
		t.Fatal("hash mismatch")
	}
	if raw == "" || raw[len(raw)-1:] == "=" {
		t.Fatalf("unexpected encoded token %q", raw)
	}
}
