package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	semanticVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	commitPattern          = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)
	imagePattern           = regexp.MustCompile(`^[^[:space:]@]+@sha256:[0-9a-f]{64}$`)
	hashPattern            = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeIdentifierPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@+-]{0,127}$`)
	credentialPattern      = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|authorization)\s*[:=]`)
)

var requiredImages = [...]string{"app", "worker", "migrate", "backup", "caddy", "postgres", "redis", "minio"}

// Manifest is the immutable, secret-free description of a production release.
type Manifest struct {
	Version          string            `json:"version"`
	Commit           string            `json:"commit"`
	BuiltAt          time.Time         `json:"builtAt"`
	Images           map[string]string `json:"images"`
	MinSchemaVersion int64             `json:"minSchemaVersion"`
	MaxSchemaVersion int64             `json:"maxSchemaVersion"`
	ComposeSHA256    string            `json:"composeSha256"`
	CaddySHA256      string            `json:"caddySha256"`
	BackupEvidenceID string            `json:"backupEvidenceId"`
	CreatedBy        string            `json:"createdBy"`
	CreatedAt        time.Time         `json:"createdAt"`
}

// ValidationError intentionally contains only a field name and a rule.
type ValidationError struct {
	Field string
	Rule  string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Rule) }

// ParseManifest decodes exactly one strict JSON object and validates it.
func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, errors.New("manifest: invalid JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("manifest: expected one JSON object")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate enforces the immutable release contract without echoing values.
func (m Manifest) Validate() error {
	if !semanticVersionPattern.MatchString(m.Version) {
		return invalid("version", "must be semantic version 2.0")
	}
	if !commitPattern.MatchString(m.Commit) {
		return invalid("commit", "must be a lowercase full commit SHA")
	}
	if m.BuiltAt.IsZero() {
		return invalid("builtAt", "must be a non-zero timestamp")
	}
	if m.CreatedAt.IsZero() {
		return invalid("createdAt", "must be a non-zero timestamp")
	}
	if m.MinSchemaVersion < 0 || m.MaxSchemaVersion < m.MinSchemaVersion {
		return invalid("schemaVersion", "must be a non-negative ordered interval")
	}
	if !hashPattern.MatchString(m.ComposeSHA256) {
		return invalid("composeSha256", "must be a lowercase SHA-256")
	}
	if !hashPattern.MatchString(m.CaddySHA256) {
		return invalid("caddySha256", "must be a lowercase SHA-256")
	}
	if !safeIdentifierPattern.MatchString(m.BackupEvidenceID) {
		return invalid("backupEvidenceId", "must be a safe non-empty identifier")
	}
	if !safeIdentifierPattern.MatchString(m.CreatedBy) {
		return invalid("createdBy", "must be a safe non-empty actor")
	}
	if len(m.Images) != len(requiredImages) {
		return invalid("images", "must contain exactly the supported image keys")
	}
	allowed := make(map[string]struct{}, len(requiredImages))
	for _, name := range requiredImages {
		allowed[name] = struct{}{}
		value, ok := m.Images[name]
		if !ok {
			return invalid("images."+name, "is required")
		}
		if unsafeManifestString(value) {
			return invalid("images."+name, "must not contain secret material or paths")
		}
		if !imagePattern.MatchString(value) {
			return invalid("images."+name, "must use an immutable sha256 digest")
		}
	}
	for name := range m.Images {
		if _, ok := allowed[name]; !ok {
			return invalid("images", "contains an unsupported key")
		}
	}
	for field, value := range map[string]string{
		"version": m.Version, "commit": m.Commit, "composeSha256": m.ComposeSHA256,
		"caddySha256": m.CaddySHA256, "backupEvidenceId": m.BackupEvidenceID,
		"createdBy": m.CreatedBy,
	} {
		if unsafeManifestString(value) {
			return invalid(field, "must not contain secret material or paths")
		}
	}
	return nil
}

// CanonicalJSON validates and emits deterministic JSON suitable for hashing.
func (m Manifest) CanonicalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// SHA256 returns the hash of the canonical manifest.
func (m Manifest) SHA256() (string, error) {
	data, err := m.CanonicalJSON()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// CompatibleWithSchema reports whether a running schema can be used by this release.
func (m Manifest) CompatibleWithSchema(version int64) bool {
	return version >= m.MinSchemaVersion && version <= m.MaxSchemaVersion
}

// PreviousImagesCompatibleWithMigratedSchema makes the rollback compatibility
// decision explicit at the call site.
func PreviousImagesCompatibleWithMigratedSchema(previous Manifest, schemaVersion int64) bool {
	return previous.CompatibleWithSchema(schemaVersion)
}

func invalid(field, rule string) error { return &ValidationError{Field: field, Rule: rule} }

func unsafeManifestString(value string) bool {
	if strings.ContainsAny(value, "\r\n") || credentialPattern.MatchString(value) {
		return true
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	if parsed, err := url.Parse(value); err == nil && parsed.User != nil {
		return true
	}
	return false
}
