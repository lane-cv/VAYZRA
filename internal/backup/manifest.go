package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const ManifestMaxBytes = 64 << 10

var (
	ErrInvalidManifest  = errors.New("invalid backup manifest")
	ErrManifestTooLarge = errors.New("backup manifest too large")

	lowerSHA256      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	resticSnapshotID = regexp.MustCompile(`^[0-9a-f]{8,64}$`)
)

type Manifest struct {
	SchemaVersion            int       `json:"schemaVersion"`
	BatchID                  string    `json:"batchId"`
	CreatedAt                time.Time `json:"createdAt"`
	DatabaseMigrationVersion int64     `json:"databaseMigrationVersion"`
	DatabaseDumpSHA256       string    `json:"databaseDumpSha256"`
	ObjectSnapshotID         string    `json:"objectSnapshotId"`
	ObjectCount              int64     `json:"objectCount"`
	ReferencedBytes          int64     `json:"referencedBytes"`
}

func MarshalManifest(manifest Manifest) ([]byte, error) {
	if validateManifest(manifest) != nil {
		return nil, ErrInvalidManifest
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) > ManifestMaxBytes {
		return nil, ErrInvalidManifest
	}
	return encoded, nil
}

func DecodeManifest(reader io.Reader) (Manifest, error) {
	if reader == nil {
		return Manifest{}, ErrInvalidManifest
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, ManifestMaxBytes+1))
	if err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	if len(encoded) > ManifestMaxBytes {
		return Manifest{}, ErrManifestTooLarge
	}
	if err := rejectDuplicateManifestKeys(encoded); err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Manifest{}, ErrInvalidManifest
	}
	if validateManifest(manifest) != nil {
		return Manifest{}, ErrInvalidManifest
	}
	canonical, err := MarshalManifest(manifest)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return Manifest{}, ErrInvalidManifest
	}
	return manifest, nil
}

func rejectDuplicateManifestKeys(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ErrInvalidManifest
	}
	seen := make(map[string]struct{}, 8)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return ErrInvalidManifest
		}
		if _, exists := seen[key]; exists {
			return ErrInvalidManifest
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return ErrInvalidManifest
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return ErrInvalidManifest
	}
	return requireJSONEOF(decoder)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidManifest
	}
	return nil
}

func validateManifest(manifest Manifest) error {
	batchID, err := uuid.Parse(manifest.BatchID)
	if err != nil || batchID == uuid.Nil || batchID.String() != manifest.BatchID {
		return ErrInvalidManifest
	}
	_, offset := manifest.CreatedAt.Zone()
	if manifest.SchemaVersion != 1 ||
		manifest.CreatedAt.IsZero() ||
		manifest.CreatedAt.Location() != time.UTC ||
		offset != 0 ||
		manifest.DatabaseMigrationVersion < 1 ||
		!lowerSHA256.MatchString(manifest.DatabaseDumpSHA256) ||
		!lowerSHA256.MatchString(manifest.ObjectSnapshotID) ||
		manifest.ObjectCount < 0 ||
		manifest.ReferencedBytes < 0 {
		return ErrInvalidManifest
	}
	return nil
}
