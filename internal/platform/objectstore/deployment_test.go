package objectstore

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const patchedAIStorImage = "quay.io/minio/aistor/minio:RELEASE.2026-06-06T02-44-06Z@sha256:5dbb753c0dbe6a987dd30ce564f66c0042e291e464d10e792443451d4fec2120"
const pinnedInitImage = "debian:12.12-slim@sha256:d5d3f9c23164ea16f31852f95bd5959aad1c5e854332fe00f7b3a20fcc9f635c"

func TestMinIODeploymentSecurityContract(t *testing.T) {
	compose := repositoryFile(t, "deploy", "compose.dev.yml")
	if err := validateMinIODeployment(compose); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repositoryPath(t, "Dockerfile.minio")); !os.IsNotExist(err) {
		t.Fatalf("vulnerable source-build Dockerfile still exists: %v", err)
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

func TestMinIODeploymentSecurityContractRejectsUnsafeTopology(t *testing.T) {
	compose := repositoryFile(t, "deploy", "compose.dev.yml")
	tests := map[string]string{
		"initializer gains all capabilities": strings.Replace(
			compose,
			"    cap_add:\n      - CHOWN\n      - DAC_OVERRIDE\n      - FOWNER\n",
			"    cap_add:\n      - ALL\n      - CHOWN\n      - DAC_OVERRIDE\n      - FOWNER\n",
			1,
		),
		"server starts before license initializer completes": strings.Replace(
			compose,
			"      aistor-license-init:\n        condition: service_completed_successfully",
			"      aistor-license-init:\n        condition: service_started",
			1,
		),
		"application consumes the license secret": strings.Replace(
			compose,
			"  app:\n    logging: *default-logging\n",
			"  app:\n    logging: *default-logging\n    secrets:\n      - source: aistor_license\n        target: /run/secrets/aistor-license\n",
			1,
		),
		"server exposes license through environment": strings.Replace(
			compose,
			"    environment:\n      MINIO_ROOT_USER:",
			"    environment:\n      MINIO_LICENSE: forbidden\n      MINIO_ROOT_USER:",
			1,
		),
		"initializer weakens final license permissions": strings.Replace(
			compose,
			"        mv -f /license/.minio.license.new /license/minio.license\n",
			"        mv -f /license/.minio.license.new /license/minio.license\n        chmod 0644 /license/minio.license\n",
			1,
		),
		"server restores loopback port bindings": strings.Replace(
			compose,
			"      - \"0.0.0.0:${HAPPYLEARN_AISTOR_API_PORT:-59000}:9000\"\n      - \"0.0.0.0:${HAPPYLEARN_AISTOR_CONSOLE_PORT:-59001}:9001\"",
			"      - \"127.0.0.1:59000:9000\"\n      - \"127.0.0.1:59001:9001\"",
			1,
		),
	}
	for name, mutated := range tests {
		t.Run(name, func(t *testing.T) {
			if mutated == compose {
				t.Fatal("test mutation did not change the Compose document")
			}
			if err := validateMinIODeployment(mutated); err == nil {
				t.Fatal("unsafe deployment topology passed validation")
			}
		})
	}
}

type deploymentCompose struct {
	Services map[string]deploymentService    `yaml:"services"`
	Volumes  map[string]any                  `yaml:"volumes"`
	Secrets  map[string]deploymentFileSecret `yaml:"secrets"`
}

type deploymentService struct {
	Image       string                          `yaml:"image"`
	User        string                          `yaml:"user"`
	Entrypoint  []string                        `yaml:"entrypoint"`
	Command     []string                        `yaml:"command"`
	ReadOnly    bool                            `yaml:"read_only"`
	CapDrop     []string                        `yaml:"cap_drop"`
	CapAdd      []string                        `yaml:"cap_add"`
	SecurityOpt []string                        `yaml:"security_opt"`
	Volumes     []string                        `yaml:"volumes"`
	Secrets     []deploymentSecretMount         `yaml:"secrets"`
	NetworkMode string                          `yaml:"network_mode"`
	Restart     string                          `yaml:"restart"`
	DependsOn   map[string]deploymentDependency `yaml:"depends_on"`
	Ports       []string                        `yaml:"ports"`
	Networks    []string                        `yaml:"networks"`
	Healthcheck deploymentHealthcheck           `yaml:"healthcheck"`
	Environment map[string]any                  `yaml:"environment"`
}

type deploymentSecretMount struct {
	Source string `yaml:"source"`
	Target string `yaml:"target"`
}

type deploymentDependency struct {
	Condition string `yaml:"condition"`
}

type deploymentHealthcheck struct {
	Test []string `yaml:"test"`
}

type deploymentFileSecret struct {
	File string `yaml:"file"`
}

func validateMinIODeployment(compose string) error {
	if strings.Contains(compose, "Dockerfile.minio") || strings.Contains(compose, "RELEASE.2025-10-15T17-29-55Z") {
		return fmt.Errorf("development Compose still builds or references the vulnerable OSS server")
	}
	var document deploymentCompose
	if err := yaml.Unmarshal([]byte(compose), &document); err != nil {
		return fmt.Errorf("parse development Compose: %w", err)
	}

	dataInitializer, ok := document.Services["minio-data-init"]
	if !ok {
		return fmt.Errorf("MinIO data initializer is absent")
	}
	for _, check := range []struct {
		label string
		got   any
		want  any
	}{
		{"MinIO data initializer image", dataInitializer.Image, "${HAPPYLEARN_AISTOR_IMAGE:-" + patchedAIStorImage + "}"},
		{"MinIO data initializer user", dataInitializer.User, "0:0"},
		{"MinIO data initializer command", dataInitializer.Command, []string{"chown 1000:0 /data && chmod 0750 /data"}},
		{"MinIO data initializer volumes", dataInitializer.Volumes, []string{"minio_data:/data"}},
	} {
		if err := requireDeploymentValue(check.label, check.got, check.want); err != nil {
			return err
		}
	}
	if dataInitializer.NetworkMode != "none" || dataInitializer.Restart != "no" {
		return fmt.Errorf("MinIO data initializer must be a no-network one-shot service")
	}

	licenseInitializer, ok := document.Services["aistor-license-init"]
	if !ok {
		return fmt.Errorf("AIStor license initializer is absent")
	}
	for _, check := range []struct {
		label string
		got   any
		want  any
	}{
		{"AIStor license initializer image", licenseInitializer.Image, "${HAPPYLEARN_LOCAL_INIT_IMAGE:-" + pinnedInitImage + "}"},
		{"AIStor license initializer user", licenseInitializer.User, "0:0"},
		{"AIStor license initializer entrypoint", licenseInitializer.Entrypoint, []string{"/bin/sh", "-ceu"}},
		{"AIStor license initializer capabilities dropped", licenseInitializer.CapDrop, []string{"ALL"}},
		{"AIStor license initializer capabilities added", licenseInitializer.CapAdd, []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}},
		{"AIStor license initializer security options", licenseInitializer.SecurityOpt, []string{"no-new-privileges:true"}},
		{"AIStor license initializer volumes", licenseInitializer.Volumes, []string{"aistor_license_runtime:/license:rw"}},
		{"AIStor license initializer secrets", licenseInitializer.Secrets, []deploymentSecretMount{{Source: "aistor_license", Target: "/source/minio.license"}}},
	} {
		if err := requireDeploymentValue(check.label, check.got, check.want); err != nil {
			return err
		}
	}
	if !licenseInitializer.ReadOnly || licenseInitializer.NetworkMode != "none" || licenseInitializer.Restart != "no" {
		return fmt.Errorf("AIStor license initializer must be read-only, no-network, and one-shot")
	}
	expectedLicenseCommand := []string{strings.Join([]string{
		"cp /source/minio.license /license/.minio.license.new",
		"chown 1000:0 /license/.minio.license.new",
		"chmod 0440 /license/.minio.license.new",
		"mv -f /license/.minio.license.new /license/minio.license",
		"",
	}, "\n")}
	if err := requireDeploymentValue("AIStor license initializer command", licenseInitializer.Command, expectedLicenseCommand); err != nil {
		return err
	}

	server, ok := document.Services["minio"]
	if !ok {
		return fmt.Errorf("MinIO server is absent")
	}
	for _, check := range []struct {
		label string
		got   any
		want  any
	}{
		{"MinIO server image", server.Image, "${HAPPYLEARN_AISTOR_IMAGE:-" + patchedAIStorImage + "}"},
		{"MinIO server user", server.User, "1000:0"},
		{"MinIO server command", server.Command, []string{"minio", "server", "/data", "--console-address", ":9001", "--license", "/license/minio.license"}},
		{"MinIO server volumes", server.Volumes, []string{"minio_data:/data", "aistor_license_runtime:/license:ro"}},
		{"MinIO server dependencies", server.DependsOn, map[string]deploymentDependency{
			"minio-data-init":     {Condition: "service_completed_successfully"},
			"aistor-license-init": {Condition: "service_completed_successfully"},
		}},
		{"MinIO server ports", server.Ports, []string{"0.0.0.0:${HAPPYLEARN_AISTOR_API_PORT:-59000}:9000", "0.0.0.0:${HAPPYLEARN_AISTOR_CONSOLE_PORT:-59001}:9001"}},
		{"MinIO server networks", server.Networks, []string{"happylearn"}},
		{"MinIO server health check", server.Healthcheck.Test, []string{"CMD", "curl", "--fail", "--silent", "http://127.0.0.1:9000/minio/health/ready"}},
	} {
		if err := requireDeploymentValue(check.label, check.got, check.want); err != nil {
			return err
		}
	}
	if server.Restart != "unless-stopped" || len(server.Secrets) != 0 {
		return fmt.Errorf("MinIO server must restart safely without directly consuming Compose secrets")
	}
	for name := range server.Environment {
		if strings.Contains(strings.ToUpper(name), "LICENSE") {
			return fmt.Errorf("MinIO server must not receive license material through environment %q", name)
		}
	}

	licenseConsumers := make(map[string][]deploymentSecretMount)
	licenseVolumeConsumers := make(map[string][]string)
	for name, service := range document.Services {
		for _, secret := range service.Secrets {
			if secret.Source == "aistor_license" {
				licenseConsumers[name] = append(licenseConsumers[name], secret)
			}
		}
		for _, volume := range service.Volumes {
			if strings.HasPrefix(volume, "aistor_license_runtime:") {
				licenseVolumeConsumers[name] = append(licenseVolumeConsumers[name], volume)
			}
		}
	}
	if err := requireDeploymentValue("AIStor license secret consumers", licenseConsumers, map[string][]deploymentSecretMount{
		"aistor-license-init": {{Source: "aistor_license", Target: "/source/minio.license"}},
	}); err != nil {
		return err
	}
	if err := requireDeploymentValue("AIStor license volume consumers", licenseVolumeConsumers, map[string][]string{
		"aistor-license-init": {"aistor_license_runtime:/license:rw"},
		"minio":               {"aistor_license_runtime:/license:ro"},
	}); err != nil {
		return err
	}
	if _, ok := document.Volumes["aistor_license_runtime"]; !ok {
		return fmt.Errorf("AIStor license runtime volume is absent")
	}
	secret, ok := document.Secrets["aistor_license"]
	if !ok || !strings.HasPrefix(secret.File, "${HAPPYLEARN_AISTOR_LICENSE_FILE:?") {
		return fmt.Errorf("AIStor license file secret must be required")
	}
	return nil
}

func requireDeploymentValue(label string, got, want any) error {
	if reflect.DeepEqual(got, want) {
		return nil
	}
	return fmt.Errorf("%s mismatch: got %#v, want %#v", label, got, want)
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
			"/license/minio.license",
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
