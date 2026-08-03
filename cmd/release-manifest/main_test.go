package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"happylearn.local/app/internal/release"
)

func commandManifest(t *testing.T, compose, caddy string) string {
	t.Helper()
	hash := func(value string) string { sum := sha256.Sum256([]byte(value)); return hex.EncodeToString(sum[:]) }
	images := map[string]string{}
	for _, name := range []string{"app", "worker", "migrate", "backup", "caddy", "postgres", "redis", "minio"} {
		images[name] = "registry.example/" + name + "@sha256:" + strings.Repeat("a", 64)
	}
	m := release.Manifest{Version: "6.0.0", Commit: strings.Repeat("b", 40), BuiltAt: time.Now().UTC(), Images: images, MinSchemaVersion: 18, MaxSchemaVersion: 20, ComposeSHA256: hash(compose), CaddySHA256: hash(caddy), BackupEvidenceID: "backup-1", CreatedBy: "test-actor", CreatedAt: time.Now().UTC()}
	data, err := m.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeCommandFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func decodeResult(t *testing.T, output bytes.Buffer) commandResult {
	t.Helper()
	var result commandResult
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	var extra any
	if decoder.Decode(&extra) == nil {
		t.Fatal("expected exactly one JSON result")
	}
	return result
}

func TestValidateEmitsOneSafeJSONResult(t *testing.T) {
	path := commandManifest(t, "compose", "caddy")
	var output bytes.Buffer
	if code := run([]string{"validate", "--file", path}, &output); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	result := decodeResult(t, output)
	if result.Category != "valid_manifest" || strings.Contains(output.String(), path) {
		t.Fatalf("unsafe result: %s", output.String())
	}
}

func TestVerifyConfigChecksExactHashes(t *testing.T) {
	compose := writeCommandFile(t, "compose.yml", "compose")
	caddy := writeCommandFile(t, "Caddyfile", "caddy")
	manifest := commandManifest(t, "compose", "caddy")
	var output bytes.Buffer
	if code := run([]string{"verify-config", "--file", manifest, "--compose", compose, "--caddy", caddy}, &output); code != 0 {
		t.Fatalf("code=%d output=%s", code, output.String())
	}
	if err := os.WriteFile(caddy, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := run([]string{"verify-config", "--file", manifest, "--compose", compose, "--caddy", caddy}, &output); code == 0 {
		t.Fatal("expected mismatch")
	}
	if got := decodeResult(t, output).Category; got != "configuration_mismatch" {
		t.Fatalf("category=%s", got)
	}
}

func TestCompatibleReturnsSafeDecision(t *testing.T) {
	manifest := commandManifest(t, "compose", "caddy")
	for schema, wantCode := range map[string]int{"19": 0, "21": 1} {
		var output bytes.Buffer
		if code := run([]string{"compatible", "--file", manifest, "--schema-version", schema}, &output); code != wantCode {
			t.Fatalf("schema=%s code=%d", schema, code)
		}
		result := decodeResult(t, output)
		if result.Compatible == nil {
			t.Fatal("missing compatibility result")
		}
	}
}

func TestCommandRejectsRelativeAndSymlinkFilesWithoutDisclosure(t *testing.T) {
	target := commandManifest(t, "compose", "caddy")
	link := filepath.Join(t.TempDir(), "manifest-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"manifest.json", link} {
		var output bytes.Buffer
		if code := run([]string{"validate", "--file", path}, &output); code == 0 {
			t.Fatal("expected file rejection")
		}
		if strings.Contains(output.String(), path) || strings.Contains(output.String(), target) {
			t.Fatalf("path disclosed: %s", output.String())
		}
	}
}

func TestInvalidManifestDoesNotEchoBody(t *testing.T) {
	secret := "password=do-not-print"
	path := writeCommandFile(t, "manifest.json", `{"version":"`+secret+`"}`)
	var output bytes.Buffer
	if code := run([]string{"validate", "--file", path}, &output); code == 0 {
		t.Fatal("expected invalid manifest")
	}
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), path) {
		t.Fatalf("sensitive output: %s", output.String())
	}
}
