package processing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestScannerUsesExactArgvAndMapsExitCodes(t *testing.T) {
	path := "/work/job/input;rm -rf data"
	for _, tc := range []struct {
		exit   int
		runErr error
		want   string
	}{{0, nil, ""}, {1, nil, "malware"}, {2, nil, "scanner_unavailable"}, {-1, errors.New("start"), "scanner_unavailable"}} {
		runner := &runnerStub{exit: tc.exit, err: tc.runErr}
		err := (Scanner{Runner: runner, Executable: "clamscan"}).Scan(context.Background(), path)
		if category(err) != tc.want {
			t.Fatalf("exit=%d err=%v", tc.exit, err)
		}
		want := []string{"--no-summary", "--infected", "--max-filesize=500M", "--max-scansize=500M", "--", path}
		if runner.executable != "clamscan" || !reflect.DeepEqual(runner.args, want) {
			t.Fatalf("exe=%q args=%q", runner.executable, runner.args)
		}
	}
}

func TestClamDefinitionsFreshFailsClosed(t *testing.T) {
	dir := t.TempDir()
	definitions := filepath.Join(dir, "daily.cvd")
	if err := os.WriteFile(definitions, []byte("definitions"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(definitions, now.Add(-6*24*time.Hour), now.Add(-6*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !ClamDefinitionsFresh(dir, 7*24*time.Hour, now) {
		t.Fatal("six-day-old definitions should be accepted")
	}
	if ClamDefinitionsFresh(dir, 5*24*time.Hour, now) || ClamDefinitionsFresh(filepath.Join(dir, "missing"), 7*24*time.Hour, now) {
		t.Fatal("stale or missing definitions must fail closed")
	}
}

type runnerStub struct {
	stdout, stderr []byte
	exit           int
	err            error
	executable     string
	args           []string
	hook           func([]string)
}

func (r *runnerStub) Run(_ context.Context, executable string, args []string, _, _ int64) ([]byte, []byte, int, error) {
	r.executable = executable
	r.args = append([]string(nil), args...)
	if r.hook != nil {
		r.hook(args)
	}
	return r.stdout, r.stderr, r.exit, r.err
}
