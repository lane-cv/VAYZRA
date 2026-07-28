package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupImageContractIsPinnedAndNonRoot(t *testing.T) {
	dockerfile := filepath.Join("..", "..", "Dockerfile.backup")
	contents, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"FROM golang:1.26.5-bookworm AS tools",
		"github.com/restic/restic/cmd/restic@v0.19.1",
		"filippo.io/age/cmd/age@v1.3.1",
		"filippo.io/age/cmd/age-keygen@v1.3.1",
		"FROM golang:1.26.5-bookworm AS backup-build",
		"FROM debian:12.12-slim AS runtime",
		"postgresql-client-18",
		"USER 10003:0",
		`ENTRYPOINT ["/app/happylearn-backup"]`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Dockerfile.backup missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"docker.sock",
		"docker-cli",
		"docker-ce-cli",
		"COPY .env",
		"COPY secrets",
		"ENTRYPOINT [\"/bin/sh\"",
		"CMD [\"/bin/sh\"",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Dockerfile.backup contains forbidden %q", forbidden)
		}
	}
}
