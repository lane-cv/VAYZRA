package objectstore

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const patchedAIStorImage = "quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120"

func TestMinIODeploymentSecurityContract(t *testing.T) {
	compose := repositoryFile(t, "deploy", "compose.dev.yml")
	if strings.Contains(compose, "Dockerfile.minio") || strings.Contains(compose, "RELEASE.2025-10-15T17-29-55Z") {
		t.Fatal("development Compose still builds or references the vulnerable OSS server")
	}
	if strings.Count(compose, "image: "+patchedAIStorImage) != 2 {
		t.Fatalf("Compose must pin the initializer and server to exact AIStor image %q", patchedAIStorImage)
	}
	if _, err := os.Stat(repositoryPath(t, "Dockerfile.minio")); !os.IsNotExist(err) {
		t.Fatalf("vulnerable source-build Dockerfile still exists: %v", err)
	}

	server := composeService(t, compose, "minio")
	for _, required := range []string{
		`user: "1000:0"`,
		"restart: unless-stopped",
		`command: ["minio", "server", "/data", "--console-address", ":9001", "--license", "/minio.license"]`,
		"source: aistor_license",
		"target: /minio.license",
		`mode: 0440`,
		`test: ["CMD", "curl", "--fail", "--silent", "http://127.0.0.1:9000/minio/health/live"]`,
		`condition: service_completed_successfully`,
		`- "127.0.0.1:59000:9000"`,
		`- "127.0.0.1:59001:9001"`,
		"- minio_data:/data",
		"- happylearn",
	} {
		if !strings.Contains(server, required) {
			t.Fatalf("MinIO service missing required private AIStor contract %q", required)
		}
	}
	if strings.Contains(server, "MINIO_LICENSE") || strings.Contains(server, "0.0.0.0:") {
		t.Fatal("MinIO service exposes license material or a non-loopback host binding")
	}

	for _, required := range []string{
		"minio-data-init:",
		`network_mode: "none"`,
		`user: "0:0"`,
		`chown 1000:0 /data && chmod 0750 /data`,
		"aistor_license:",
		"file: ${HAPPYLEARN_AISTOR_LICENSE_FILE:?",
	} {
		if !strings.Contains(compose, required) {
			t.Fatalf("Compose missing required initializer/secret contract %q", required)
		}
	}

	ignore := repositoryFile(t, ".gitignore")
	if !strings.Contains(ignore, "*.license") || !strings.Contains(ignore, "secrets/") {
		t.Fatal("license files and the local secrets directory are not ignored")
	}
	envExample := repositoryFile(t, ".env.example")
	if !strings.Contains(envExample, "HAPPYLEARN_AISTOR_LICENSE_FILE=../secrets/minio.license") || strings.Contains(envExample, "MINIO_LICENSE=") {
		t.Fatal("environment example must expose only a safe license file path")
	}
}

func TestMinIODeploymentDocumentationLocksPatchedAIStorPolicy(t *testing.T) {
	for _, path := range [][]string{
		{"docs", "superpowers", "specs", "2026-07-18-phase2-teaching-design.md"},
		{"docs", "superpowers", "plans", "2026-07-18-phase2-secure-files.md"},
	} {
		document := repositoryFile(t, path...)
		for _, required := range []string{
			patchedAIStorImage,
			"MinIO AIStor Free",
			"HAPPYLEARN_AISTOR_LICENSE_FILE",
			"/minio.license",
			"SBOM",
			"Production deployment must omit S3 and console host-port mappings",
		} {
			if !strings.Contains(document, required) {
				t.Fatalf("%s missing locked AIStor policy %q", filepath.Join(path...), required)
			}
		}
		if strings.Contains(document, "RELEASE.2025-10-15T17-29-55Z") || strings.Contains(document, "Dockerfile.minio") {
			t.Fatalf("%s still requires the vulnerable OSS source build", filepath.Join(path...))
		}
	}
}

func composeService(t *testing.T, compose, name string) string {
	t.Helper()
	marker := "  " + name + ":\n"
	start := strings.Index(compose, marker)
	if start < 0 {
		t.Fatalf("Compose service %q not found", name)
	}
	end := strings.Index(compose[start:], "\nnetworks:")
	if end < 0 {
		t.Fatalf("Compose service %q has no terminating networks section", name)
	}
	return compose[start : start+end]
}

func repositoryFile(t *testing.T, parts ...string) string {
	t.Helper()
	content, err := os.ReadFile(repositoryPath(t, parts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func repositoryPath(t *testing.T, parts ...string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	return filepath.Join(append([]string{root}, parts...)...)
}
