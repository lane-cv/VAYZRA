package backup

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

const canonicalManifestJSON = `{"schemaVersion":1,"batchId":"10000000-0000-4000-8000-000000000001","createdAt":"2026-07-28T01:02:03.000000004Z","databaseMigrationVersion":20,"databaseDumpSha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","objectSnapshotId":"8f7e6d5c4b3a21008f7e6d5c4b3a21008f7e6d5c4b3a21008f7e6d5c4b3a2100","objectCount":12,"referencedBytes":3456}`

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:            1,
		BatchID:                  "10000000-0000-4000-8000-000000000001",
		CreatedAt:                time.Date(2026, 7, 28, 1, 2, 3, 4, time.UTC),
		DatabaseMigrationVersion: 20,
		DatabaseDumpSHA256:       "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ObjectSnapshotID:         "8f7e6d5c4b3a21008f7e6d5c4b3a21008f7e6d5c4b3a21008f7e6d5c4b3a2100",
		ObjectCount:              12,
		ReferencedBytes:          3456,
	}
}

func TestManifestCanonicalRoundTrip(t *testing.T) {
	encoded, err := MarshalManifest(validManifest())
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != canonicalManifestJSON {
		t.Fatalf("encoded=%s", encoded)
	}
	decoded, err := DecodeManifest(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := MarshalManifest(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatalf("reencoded=%s", reencoded)
	}
}

func TestManifestRejectsUnknownDuplicateAndTrailingJSON(t *testing.T) {
	cases := []string{
		strings.Replace(canonicalManifestJSON, `"objectCount":12`, `"unknown":1,"objectCount":12`, 1),
		strings.Replace(canonicalManifestJSON, `"objectCount":12`, `"objectCount":11,"objectCount":12`, 1),
		canonicalManifestJSON + `{}`,
		canonicalManifestJSON + ` false`,
	}
	for _, input := range cases {
		if _, err := DecodeManifest(strings.NewReader(input)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("input accepted or leaked detail: err=%v", err)
		}
	}
}

func TestManifestRejectsOversizedInput(t *testing.T) {
	input := canonicalManifestJSON + strings.Repeat(" ", ManifestMaxBytes)
	if _, err := DecodeManifest(strings.NewReader(input)); !errors.Is(err, ErrManifestTooLarge) {
		t.Fatalf("err=%v", err)
	}
}

func TestManifestRejectsInvalidSemanticFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "schema zero", mutate: func(v *Manifest) { v.SchemaVersion = 0 }},
		{name: "future schema", mutate: func(v *Manifest) { v.SchemaVersion = 2 }},
		{name: "nil UUID", mutate: func(v *Manifest) { v.BatchID = "00000000-0000-0000-0000-000000000000" }},
		{name: "noncanonical UUID", mutate: func(v *Manifest) { v.BatchID = "{10000000-0000-4000-8000-000000000001}" }},
		{name: "zero time", mutate: func(v *Manifest) { v.CreatedAt = time.Time{} }},
		{name: "non UTC", mutate: func(v *Manifest) {
			v.CreatedAt = v.CreatedAt.In(time.FixedZone("offset", 8*60*60))
		}},
		{name: "migration zero", mutate: func(v *Manifest) { v.DatabaseMigrationVersion = 0 }},
		{name: "short sha", mutate: func(v *Manifest) { v.DatabaseDumpSHA256 = "abc" }},
		{name: "uppercase sha", mutate: func(v *Manifest) {
			v.DatabaseDumpSHA256 = strings.ToUpper(v.DatabaseDumpSHA256)
		}},
		{name: "empty snapshot", mutate: func(v *Manifest) { v.ObjectSnapshotID = "" }},
		{name: "short snapshot identity", mutate: func(v *Manifest) { v.ObjectSnapshotID = "8f7e6d5c4b3a2100" }},
		{name: "nonhex snapshot", mutate: func(v *Manifest) { v.ObjectSnapshotID = "snapshot-id" }},
		{name: "uppercase snapshot", mutate: func(v *Manifest) { v.ObjectSnapshotID = "ABCDEF0123456789" }},
		{name: "snapshot path", mutate: func(v *Manifest) { v.ObjectSnapshotID = "../../repository" }},
		{name: "negative objects", mutate: func(v *Manifest) { v.ObjectCount = -1 }},
		{name: "negative bytes", mutate: func(v *Manifest) { v.ReferencedBytes = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := validManifest()
			tc.mutate(&value)
			if _, err := MarshalManifest(value); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestManifestDecoderRejectsNonUTCWireTimestamp(t *testing.T) {
	input := strings.Replace(
		canonicalManifestJSON,
		`"2026-07-28T01:02:03.000000004Z"`,
		`"2026-07-28T09:02:03.000000004+08:00"`,
		1,
	)
	if _, err := DecodeManifest(strings.NewReader(input)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("err=%v", err)
	}
}

func TestManifestDecoderRequiresAllEightFieldsIncludingValidZeroValues(t *testing.T) {
	withZeroValues := strings.NewReplacer(
		`"objectCount":12`,
		`"objectCount":0`,
		`"referencedBytes":3456`,
		`"referencedBytes":0`,
	).Replace(canonicalManifestJSON)
	for _, field := range []string{
		`,"objectCount":0`,
		`,"referencedBytes":0`,
	} {
		input := strings.Replace(withZeroValues, field, "", 1)
		if _, err := DecodeManifest(strings.NewReader(input)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("missing field %q accepted: err=%v", field, err)
		}
	}
}

func TestManifestDecoderRequiresCanonicalFieldOrderAndWhitespace(t *testing.T) {
	reordered := `{"batchId":"10000000-0000-4000-8000-000000000001","schemaVersion":1,"createdAt":"2026-07-28T01:02:03.000000004Z","databaseMigrationVersion":20,"databaseDumpSha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","objectSnapshotId":"8f7e6d5c4b3a21008f7e6d5c4b3a21008f7e6d5c4b3a21008f7e6d5c4b3a2100","objectCount":12,"referencedBytes":3456}`
	for _, input := range []string{
		reordered,
		" " + canonicalManifestJSON,
		canonicalManifestJSON + "\n",
		strings.Replace(canonicalManifestJSON, `,"batchId"`, `, "batchId"`, 1),
	} {
		if _, err := DecodeManifest(strings.NewReader(input)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("noncanonical manifest accepted: %q", input)
		}
	}
}
