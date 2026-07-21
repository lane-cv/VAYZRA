package processing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const MaxClamDefinitionAge = 7 * 24 * time.Hour

type Scanner struct {
	Runner     Runner
	Executable string
}

func (s Scanner) Scan(ctx context.Context, path string) error {
	if s.Runner == nil || path == "" {
		return transient("scanner_unavailable")
	}
	executable := s.Executable
	if executable == "" {
		executable = "clamscan"
	}
	_, _, exit, err := s.Runner.Run(ctx, executable, []string{"--no-summary", "--infected", "--max-filesize=500M", "--max-scansize=500M", "--", path}, 64*1024, 64*1024)
	if err != nil {
		return transient("scanner_unavailable")
	}
	switch exit {
	case 0:
		return nil
	case 1:
		return reject("malware")
	default:
		return transient("scanner_unavailable")
	}
}

func ClamDefinitionsFresh(dir string, maxAge time.Duration, now time.Time) bool {
	if dir == "" || maxAge <= 0 || now.IsZero() {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	var newest time.Time
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		ext := filepath.Ext(name)
		if entry.IsDir() || !strings.HasPrefix(name, "daily.") || (ext != ".cvd" && ext != ".cld") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return false
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return !newest.IsZero() && !newest.After(now.Add(5*time.Minute)) && now.Sub(newest) <= maxAge
}
