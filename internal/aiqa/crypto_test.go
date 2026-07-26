package aiqa

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSecretSealUsesDistinctRandomNonces(t *testing.T) {
	box, err := NewAESGCMSecretBox(bytes.Repeat([]byte{1}, 32), 1, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	providerID := uuid.MustParse("2e61ddd6-352a-4182-9841-ef2d7f0e4914")
	first, err := box.Seal(providerID, []byte("provider-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := box.Seal(providerID, []byte("provider-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Blob, second.Blob) {
		t.Fatal("distinct random nonces produced the same encrypted blob")
	}
}

func TestSecretRoundTripSurvivesCallerZeroingPlaintext(t *testing.T) {
	box, err := NewAESGCMSecretBox(bytes.Repeat([]byte{2}, 32), 1, bytes.NewReader(bytes.Repeat([]byte{3}, 12)))
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("provider-api-key")
	secret, err := box.Seal(uuid.MustParse("3b96ef95-fcb2-4791-b71a-777dc457325c"), plaintext)
	if err != nil {
		t.Fatal(err)
	}
	clear(plaintext)

	opened, err := box.Open(uuid.MustParse("3b96ef95-fcb2-4791-b71a-777dc457325c"), secret)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(opened), "provider-api-key"; got != want {
		t.Fatalf("opened secret = %q, want %q", got, want)
	}
}

func TestSecretOpenRejectsWrongProviderWithoutPlaintextLeak(t *testing.T) {
	box, err := NewAESGCMSecretBox(bytes.Repeat([]byte{4}, 32), 1, bytes.NewReader(bytes.Repeat([]byte{5}, 12)))
	if err != nil {
		t.Fatal(err)
	}

	secret, err := box.Seal(uuid.MustParse("d5a94404-ef43-4b59-b65c-7e76d9e0c077"), []byte("provider-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = box.Open(uuid.MustParse("4e62053d-f661-4359-a0d6-69d7b49f836a"), secret)
	if !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("error = %v, want ErrSecretUnavailable", err)
	}
	if strings.Contains(err.Error(), "provider-api-key") {
		t.Fatalf("plaintext leaked in error: %v", err)
	}
}

func TestSecretOpenFailsClosedForWrongKeyOrVersion(t *testing.T) {
	key := bytes.Repeat([]byte{6}, 32)
	providerID := uuid.MustParse("c1e94322-04c7-4fa8-a7fc-cc5e794f80ef")
	box, err := NewAESGCMSecretBox(key, 1, bytes.NewReader(bytes.Repeat([]byte{7}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := box.Seal(providerID, []byte("provider-api-key"))
	if err != nil {
		t.Fatal(err)
	}

	for name, other := range map[string]SecretBox{
		"wrong key": func() SecretBox {
			b, err := NewAESGCMSecretBox(bytes.Repeat([]byte{8}, 32), 1, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}(),
		"wrong version": func() SecretBox {
			b, err := NewAESGCMSecretBox(key, 2, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := other.Open(providerID, secret)
			if !errors.Is(err, ErrSecretUnavailable) {
				t.Fatalf("error = %v, want ErrSecretUnavailable", err)
			}
			if strings.Contains(err.Error(), "provider-api-key") {
				t.Fatalf("plaintext leaked in error: %v", err)
			}
		})
	}
}
