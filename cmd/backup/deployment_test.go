package main

import (
	"os"
	"path/filepath"
	"regexp"
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

func TestBackupImageVersionAssertionsMatchRealExecutableOutput(t *testing.T) {
	dockerfile := filepath.Join("..", "..", "Dockerfile.backup")
	contents, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, check := range []struct {
		name    string
		command string
		valid   string
		wrong   string
	}{
		{
			name: "pg_dump", command: "pg_dump --version",
			valid: "pg_dump (PostgreSQL) 18.4 (Debian 18.4-1.pgdg12+1)",
			wrong: "pg_dump (PostgreSQL) 18.5 (Debian 18.5-1.pgdg12+1)",
		},
		{
			name: "pg_restore", command: "pg_restore --version",
			valid: "pg_restore (PostgreSQL) 18.4 (Debian 18.4-1.pgdg12+1)",
			wrong: "pg_restore (PostgreSQL) 18.5 (Debian 18.5-1.pgdg12+1)",
		},
		{
			name: "restic", command: "restic version",
			valid: "restic 0.19.1 compiled with go1.26.5 on linux/arm64",
			wrong: "restic 0.19.2 compiled with go1.26.5 on linux/arm64",
		},
		{
			name: "age", command: "age --version",
			valid: "v1.3.1",
			wrong: "v1.3.2",
		},
		{
			name: "age-keygen", command: "age-keygen --version",
			valid: "v1.3.1",
			wrong: "v1.3.2",
		},
	} {
		t.Run(check.name, func(t *testing.T) {
			pattern := dockerVersionPattern(t, text, check.command)
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				t.Fatalf("invalid version assertion %q: %v", pattern, err)
			}
			if !compiled.MatchString(check.valid) {
				t.Errorf("version assertion %q rejects %q", pattern, check.valid)
			}
			if compiled.MatchString(check.wrong) {
				t.Errorf("version assertion %q accepts %q", pattern, check.wrong)
			}
		})
	}
}

func dockerVersionPattern(t *testing.T, dockerfile string, command string) string {
	t.Helper()
	commandOffset := strings.Index(dockerfile, command)
	if commandOffset < 0 {
		t.Fatalf("missing version command %q", command)
	}
	remainder := dockerfile[commandOffset+len(command):]
	grepOffset := strings.Index(remainder, "grep -Eq '")
	if grepOffset < 0 {
		t.Fatalf("missing grep assertion after %q", command)
	}
	remainder = remainder[grepOffset+len("grep -Eq '"):]
	end := strings.IndexByte(remainder, '\'')
	if end < 0 {
		t.Fatalf("unterminated grep assertion after %q", command)
	}
	return remainder[:end]
}
