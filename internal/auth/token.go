package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

func NewSessionToken() (string, [32]byte, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", [32]byte{}, fmt.Errorf("read session token: %w", err)
	}
	raw := base64.RawURLEncoding.EncodeToString(bytes)
	return raw, sha256.Sum256([]byte(raw)), nil
}
