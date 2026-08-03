package release

import (
	"strings"
	"testing"
	"time"
)

func validManifest() Manifest {
	images := map[string]string{}
	for _, name := range requiredImages {
		images[name] = "registry.example/happylearn/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	return Manifest{
		Version: "6.1.0-rc.1", Commit: strings.Repeat("b", 40),
		BuiltAt: time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC), Images: images,
		MinSchemaVersion: 18, MaxSchemaVersion: 20,
		ComposeSHA256: strings.Repeat("c", 64), CaddySHA256: strings.Repeat("d", 64),
		BackupEvidenceID: "backup-20260801-001", CreatedBy: "release.bot@example.com",
		CreatedAt: time.Date(2026, 8, 1, 2, 3, 4, 0, time.UTC),
	}
}

func TestManifestAcceptsCompleteImmutableRelease(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRejectsInvalidSemanticVersion(t *testing.T) {
	for _, value := range []string{"v6", "1.0.0-01", "01.0.0", "1.0"} {
		m := validManifest()
		m.Version = value
		if err := m.Validate(); err == nil {
			t.Fatalf("expected invalid semantic version %q", value)
		}
	}
}

func TestManifestRejectsNonDigestImage(t *testing.T) {
	m := validManifest()
	m.Images["app"] = "registry.example/happylearn/app:latest"
	if err := m.Validate(); err == nil {
		t.Fatal("expected mutable image rejection")
	}
}

func TestManifestRejectsInvalidSchemaInterval(t *testing.T) {
	m := validManifest()
	m.MinSchemaVersion, m.MaxSchemaVersion = 20, 19
	if err := m.Validate(); err == nil {
		t.Fatal("expected invalid schema interval")
	}
}

func TestManifestRejectsMissingConfigurationHash(t *testing.T) {
	m := validManifest()
	m.ComposeSHA256 = ""
	if err := m.Validate(); err == nil {
		t.Fatal("expected missing hash rejection")
	}
}

func TestManifestRejectsSecretPathOrValue(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"absolute secret path":  func(m *Manifest) { m.Images["app"] = "/run/secrets/app@sha256:" + strings.Repeat("a", 64) },
		"credential assignment": func(m *Manifest) { m.CreatedBy = "password=redacted" },
		"line break":            func(m *Manifest) { m.BackupEvidenceID = "evidence\nvalue" },
		"URI user info":         func(m *Manifest) { m.CreatedBy = "https://user:pass@example.com" },
	} {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			mutate(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("expected unsafe value rejection")
			}
		})
	}
}

func TestManifestRejectsUnknownField(t *testing.T) {
	data, err := validManifest().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"createdAt":`, `"unexpected":true,"createdAt":`, 1))
	if _, err := ParseManifest(data); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestManifestCanonicalJSONIsStable(t *testing.T) {
	first := validManifest()
	second := validManifest()
	second.Images = map[string]string{}
	for index := len(requiredImages) - 1; index >= 0; index-- {
		name := requiredImages[index]
		second.Images[name] = first.Images[name]
	}
	a, err := first.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("canonical output differs\n%s\n%s", a, b)
	}
}

func TestPreviousImagesCompatibleWithMigratedSchema(t *testing.T) {
	m := validManifest()
	if !PreviousImagesCompatibleWithMigratedSchema(m, 19) {
		t.Fatal("expected schema 19 to be compatible")
	}
	if PreviousImagesCompatibleWithMigratedSchema(m, 21) {
		t.Fatal("expected schema 21 to be incompatible")
	}
}

func TestParseManifestRejectsTrailingObject(t *testing.T) {
	data, _ := validManifest().CanonicalJSON()
	if _, err := ParseManifest(append(data, []byte(` {}`)...)); err == nil {
		t.Fatal("expected trailing object rejection")
	}
}
