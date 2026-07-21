package main

import (
	"os"
	"strings"
	"testing"
)

func TestWorkerDeploymentIsBoundedAndNonRoot(t *testing.T) {
	dockerfile, err := os.ReadFile("../../Dockerfile.worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"go build", "happylearn-maintenance", "clamscan", "libreoffice", "poppler-utils", "ffmpeg", "USER 10002:10002", "ENTRYPOINT [\"/app/happylearn-worker\"]"} {
		if !strings.Contains(string(dockerfile), required) {
			t.Errorf("Dockerfile.worker missing %q", required)
		}
	}
	compose, err := os.ReadFile("../../deploy/compose.dev.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"worker:", "read_only: true", "size=1024m", "cap_drop:", "no-new-privileges:true", "memory: 1792m", "cpus: 1.0", "internal: true"} {
		if !strings.Contains(string(compose), required) {
			t.Errorf("compose missing %q", required)
		}
	}
}
