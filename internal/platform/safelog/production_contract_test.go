package safelog

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionProcessesUseSafeLoggingOnStderr(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, path := range []string{
		"cmd/server/main.go",
		"cmd/worker/main.go",
		"cmd/backup/main.go",
		"cmd/backup-retention/main.go",
		"cmd/maintenance/main.go",
	} {
		t.Run(path, func(t *testing.T) {
			fullPath := filepath.Join(root, filepath.FromSlash(path))
			source, err := os.ReadFile(fullPath)
			if err != nil {
				t.Fatalf("read %s: %v", fullPath, err)
			}
			parsed, err := parser.ParseFile(
				token.NewFileSet(),
				fullPath,
				source,
				parser.ImportsOnly,
			)
			if err != nil {
				t.Fatalf("parse %s: %v", fullPath, err)
			}
			for _, imported := range parsed.Imports {
				name, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					t.Fatalf("unquote import %s: %v", imported.Path.Value, err)
				}
				if name == "log" || name == "log/slog" {
					t.Fatalf("%s imports unsafe production logger %q", path, name)
				}
			}
			text := string(source)
			for _, required := range []string{
				"internal/platform/safelog",
				"os.Stderr",
			} {
				if !strings.Contains(text, required) {
					t.Fatalf("%s lacks %q", path, required)
				}
			}
			for _, forbidden := range []string{
				"os.Stdout",
				"fmt.Print",
			} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("%s writes operational output through %q", path, forbidden)
				}
			}
		})
	}
}

func TestProductionHTTPAndRunnerWiringKeepsSafeSeams(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	assertSourceContains(t, filepath.Join(root, "internal/app/app.go"), []string{
		"httpx.RequestID",
		"httpx.SafeRequestLog",
		"httpx.SafeRecoverer",
		"NewAccessHandlerWithLog",
		"NewQAAccessHandlerWithLog",
		"NewAIAccessHandlerWithLog",
	})
	assertSourceContains(t, filepath.Join(root, "cmd/server/main.go"), []string{
		"httpx.SafeRequestLog(deps.logger",
		"httpx.SafeRecoverer(deps.logger)",
		"httpx.SafeServerErrorLog(logger",
		"runnerLogs.uploadCleanup",
		"runnerLogs.outbox",
		"runnerLogs.ai",
		"runnerLogs.alert",
		"runnerLogs.webhook",
		"runnerLogs.retention",
		"runnerLogs.loginLimiter",
		"runnerLogs.progressLimiter",
		"runnerLogs.searchLimiter",
		"runnerLogs.providerTestLimiter",
		"redisx.NewLoginLimiterWithLog",
		"redisx.NewProgressWriteLimiterWithLog",
		"redisx.NewSearchLimiterWithLog",
		"redisx.NewProviderTestLimiterWithLog",
	})
	assertSourceContains(t, filepath.Join(root, "cmd/worker/main.go"), []string{
		"buildWorkerWithLog",
		`logger.Error("processing.worker"`,
		"productionWorkerHealthHandler(ready, logger)",
		"httpx.SafeRequestLog(logger",
		"httpx.SafeRecoverer(logger)",
		"httpx.SafeServerErrorLog(logger",
	})
	for _, path := range []string{
		filepath.Join(root, "internal/files/http_access.go"),
		filepath.Join(root, "internal/files/cleanup_runner.go"),
	} {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(source), `"log"`) ||
			strings.Contains(string(source), `"log/slog"`) {
			t.Fatalf("%s retains a standard logger fallback", path)
		}
	}
}

func assertSourceContains(t *testing.T, path string, required []string) {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, fragment := range required {
		if !strings.Contains(string(source), fragment) {
			t.Errorf("%s lacks production contract fragment %q", path, fragment)
		}
	}
}
