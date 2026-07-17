package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

type PasswordHasher struct {
	params Argon2Params
}

func NewPasswordHasher(params Argon2Params) PasswordHasher {
	return PasswordHasher{params: params}
}

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) {
		return errors.New("password must be valid UTF-8")
	}
	length := utf8.RuneCountInString(password)
	if length < 12 || length > 128 {
		return errors.New("password must contain 12 to 128 Unicode code points")
	}
	if first, _ := utf8.DecodeRuneInString(password); unicode.IsSpace(first) {
		return errors.New("password must not begin with whitespace")
	}
	last, _ := utf8.DecodeLastRuneInString(password)
	if unicode.IsSpace(last) {
		return errors.New("password must not end with whitespace")
	}
	return nil
}

func (h PasswordHasher) Hash(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	if err := validateArgon2Params(h.params); err != nil {
		return "", err
	}
	salt := make([]byte, h.params.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read password salt: %w", err)
	}
	return h.hashWithSalt(password, salt)
}

func (h PasswordHasher) Compare(encoded, password string) error {
	params, salt, want, err := parsePHC(encoded)
	if err != nil {
		return ErrInvalidCredentials
	}
	got := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, params.Parallelism, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

func (h PasswordHasher) hashWithSalt(password string, salt []byte) (string, error) {
	if err := validateArgon2Params(h.params); err != nil {
		return "", err
	}
	if len(salt) != int(h.params.SaltLength) {
		return "", errors.New("invalid password salt length")
	}
	hash := argon2.IDKey([]byte(password), salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", h.params.MemoryKiB, h.params.Iterations, h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func parsePHC(encoded string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return Argon2Params{}, nil, nil, errors.New("invalid PHC format")
	}
	paramParts := strings.Split(parts[3], ",")
	if len(paramParts) != 3 {
		return Argon2Params{}, nil, nil, errors.New("invalid PHC parameters")
	}
	memory, err := parseCanonicalUint(paramParts[0], "m=", 32)
	if err != nil {
		return Argon2Params{}, nil, nil, err
	}
	iterations, err := parseCanonicalUint(paramParts[1], "t=", 32)
	if err != nil {
		return Argon2Params{}, nil, nil, err
	}
	parallelism, err := parseCanonicalUint(paramParts[2], "p=", 8)
	if err != nil {
		return Argon2Params{}, nil, nil, err
	}
	params := Argon2Params{MemoryKiB: uint32(memory), Iterations: uint32(iterations), Parallelism: uint8(parallelism)}
	if err := validateArgon2Work(params); err != nil {
		return Argon2Params{}, nil, nil, err
	}
	salt, err := decodeCanonicalBase64(parts[4])
	if err != nil || len(salt) < 16 || len(salt) > 64 {
		return Argon2Params{}, nil, nil, errors.New("invalid PHC salt")
	}
	hash, err := decodeCanonicalBase64(parts[5])
	if err != nil || len(hash) < 16 || len(hash) > 64 {
		return Argon2Params{}, nil, nil, errors.New("invalid PHC hash")
	}
	return params, salt, hash, nil
}

func parseCanonicalUint(value, prefix string, bitSize int) (uint64, error) {
	if !strings.HasPrefix(value, prefix) {
		return 0, errors.New("invalid PHC parameter")
	}
	digits := strings.TrimPrefix(value, prefix)
	if digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return 0, errors.New("invalid PHC parameter")
	}
	n, err := strconv.ParseUint(digits, 10, bitSize)
	if err != nil || strconv.FormatUint(n, 10) != digits {
		return 0, errors.New("invalid PHC parameter")
	}
	return n, nil
}

func decodeCanonicalBase64(value string) ([]byte, error) {
	if value == "" || strings.Contains(value, "=") {
		return nil, errors.New("invalid base64")
	}
	decoded, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil || base64.RawStdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("invalid base64")
	}
	return decoded, nil
}

func validateArgon2Params(params Argon2Params) error {
	if err := validateArgon2Work(params); err != nil {
		return err
	}
	if params.SaltLength < 16 || params.SaltLength > 64 || params.KeyLength < 16 || params.KeyLength > 64 {
		return errors.New("invalid Argon2 salt or key length")
	}
	return nil
}

func validateArgon2Work(params Argon2Params) error {
	if params.MemoryKiB < 8*uint32(params.Parallelism) || params.MemoryKiB > 1024*1024 || params.Iterations == 0 || params.Iterations > 10 || params.Parallelism == 0 || params.Parallelism > 64 {
		return errors.New("invalid Argon2 parameters")
	}
	return nil
}
