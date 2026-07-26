package aiqa

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
	"strconv"

	"github.com/google/uuid"
)

var ErrSecretUnavailable = errors.New("ai provider secret unavailable")

type EncryptedSecret struct {
	KeyVersion int16
	Blob       []byte
}

type SecretBox interface {
	Seal(providerID uuid.UUID, plaintext []byte) (EncryptedSecret, error)
	Open(providerID uuid.UUID, secret EncryptedSecret) ([]byte, error)
}

type aesGCMSecretBox struct {
	gcm     cipher.AEAD
	version int16
	random  io.Reader
}

func NewAESGCMSecretBox(key []byte, version int16, random io.Reader) (SecretBox, error) {
	if len(key) != 32 || version < 1 || random == nil {
		return nil, errors.New("invalid AI secret box configuration")
	}

	keyCopy := append([]byte(nil), key...)
	block, err := aes.NewCipher(keyCopy)
	if err != nil {
		return nil, errors.New("invalid AI secret box configuration")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("AI secret box unavailable")
	}
	return &aesGCMSecretBox{gcm: gcm, version: version, random: random}, nil
}

func (b *aesGCMSecretBox) Seal(providerID uuid.UUID, plaintext []byte) (EncryptedSecret, error) {
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(b.random, nonce); err != nil {
		return EncryptedSecret{}, ErrSecretUnavailable
	}
	blob := append(nonce, b.gcm.Seal(nil, nonce, plaintext, providerAAD(providerID, b.version))...)
	return EncryptedSecret{KeyVersion: b.version, Blob: blob}, nil
}

func (b *aesGCMSecretBox) Open(providerID uuid.UUID, secret EncryptedSecret) ([]byte, error) {
	if secret.KeyVersion != b.version || len(secret.Blob) < 12+b.gcm.Overhead() {
		return nil, ErrSecretUnavailable
	}
	nonce, ciphertext := secret.Blob[:12], secret.Blob[12:]
	plaintext, err := b.gcm.Open(nil, nonce, ciphertext, providerAAD(providerID, b.version))
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	return plaintext, nil
}

func providerAAD(id uuid.UUID, version int16) []byte {
	return []byte("happylearn:ai-provider-key:v1:" + id.String() + ":" + strconv.Itoa(int(version)))
}
